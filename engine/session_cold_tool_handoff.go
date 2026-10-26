package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/google/uuid"

	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/internal/statemigration"
	"github.com/abietic/yhc/tools"
)

var ErrColdToolHandoff = errors.New("cold tool handoff is unverified or already claimed")

// ColdToolHandoff is an externally anchored operator assertion, not a spending
// grant. Its usage snapshot must also be read from the actual retained store.
type ColdToolHandoff struct {
	Version        int                          `json:"version"`
	Scope          string                       `json:"scope"`
	NativeProof    SessionRecoveryProof         `json:"native_proof"`
	CheckpointPath string                       `json:"checkpoint_path"`
	Checkpoint     execution.RunUsageCheckpoint `json:"checkpoint"`
	ReadGuards     tools.ReadGuardSnapshot      `json:"read_guards"`
}

type ColdToolStateProof struct {
	Version              int    `json:"version"`
	Scope                string `json:"scope"`
	SessionID            string `json:"session_id"`
	NativeSHA256         string `json:"native_sha256"`
	SourceSegmentID      string `json:"source_segment_id"`
	NewSegmentID         string `json:"new_segment_id"`
	GuardCount           int    `json:"guard_count"`
	ReadGuardsRestored   bool   `json:"read_guards_restored"`
	DispatchClaimed      bool   `json:"dispatch_claimed"`
	DispatchMode         string `json:"dispatch_mode"`
	HistoricalBudgetUsed bool   `json:"historical_budget_used"`
}

// ReadColdToolHandoff reads one private regular file under a pinned directory.
// The digest is of the original file bytes, retained independently by the host.
func ReadColdToolHandoff(path, expectedSHA256 string) (ColdToolHandoff, error) {
	var result ColdToolHandoff
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(expectedSHA256) != 64 {
		return result, ErrColdToolHandoff
	}
	store, exists, err := statemigration.OpenCanonicalStore(filepath.Dir(path), ".", false)
	if err != nil || !exists {
		return result, ErrColdToolHandoff
	}
	defer store.Close() //nolint:errcheck // Read-only pinned file owner.
	file, info, exists, err := store.OpenRegular(filepath.Base(path), os.O_RDONLY, false)
	if err != nil || !exists {
		return result, ErrColdToolHandoff
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, (8<<20)+1))
	closeErr := file.Close()
	hash := sha256.Sum256(raw)
	if readErr != nil || closeErr != nil || len(raw) > 8<<20 || hex.EncodeToString(hash[:]) != expectedSHA256 ||
		store.ValidateRegular(filepath.Base(path), info) != nil || store.Revalidate() != nil {
		return result, ErrColdToolHandoff
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || decoder.Decode(new(any)) != io.EOF {
		return ColdToolHandoff{}, ErrColdToolHandoff
	}
	return result, nil
}

// ResumeInfoWithColdToolHandoff joins restored native/file/financial evidence
// before returning an isolated guard owner for this explicit new user turn.
// Unsupported runtime state and every failed/consumed claim remain blocked.
func (s *SessionService) ResumeInfoWithColdToolHandoff(ctx context.Context, info session.SessionInfo, handoff ColdToolHandoff) (*tools.FileReadGuards, ColdToolStateProof, error) {
	var proof ColdToolStateProof
	if s == nil || s.engine == nil || s.engine.administrationOnly || handoff.Version != 1 || handoff.Scope != "cold-file-read-guard-handoff-v1" {
		return nil, proof, ErrColdToolHandoff
	}
	eng := s.engine
	native := handoff.NativeProof
	if eng.config.RunUsage == nil {
		return nil, proof, ErrColdToolHandoff
	}
	fresh := eng.config.RunUsage.Snapshot()
	if fresh.Checkpoint == nil || fresh.Checkpoint.Failed || fresh.Checkpoint.Sequence != 1 || fresh.ProviderCalls != 0 || fresh.DeniedCalls != 0 ||
		fresh.UntrackedCalls != 0 || fresh.ReleasedCalls != 0 || fresh.StopReason != "" || !fresh.Limits.ReserveCostBeforeDispatch || fresh.Limits.MaxProviderCalls <= 0 ||
		fresh.CallLedger.SegmentID == handoff.Checkpoint.SegmentID {
		return nil, proof, ErrColdToolHandoff
	}
	if native.Version != 1 || native.Scope != canonicalAdministrationRestoreScope || native.ValidationComplete || native.CanResumeAgent ||
		!reflect.DeepEqual(native.BlockingReasons, []string{"native_turn_incomplete"}) || native.SessionID != info.SessionID || native.CWD != info.CWD || native.CWD != eng.GetCWD() ||
		handoff.ReadGuards.CWD != native.CWD || native.ToolBoundary == nil || !native.ToolBoundary.WorkspaceLocal {
		return nil, proof, ErrColdToolHandoff
	}
	path := handoff.CheckpointPath
	if filepath.ToSlash(filepath.Clean(path)) != path || !strings.HasPrefix(path, ".yhc/usage-checkpoints/") || strings.Contains(path, "../") || filepath.IsAbs(path) {
		return nil, proof, ErrColdToolHandoff
	}
	checkpoint, err := execution.ReadRunUsageCheckpointDir(filepath.Join(native.CWD, filepath.FromSlash(path)), handoff.Checkpoint.SegmentID)
	if err != nil || !reflect.DeepEqual(checkpoint, handoff.Checkpoint) || !checkpoint.AdmissionClosed || !checkpoint.AccountingComplete ||
		checkpoint.Phase != "sealed" || checkpoint.Snapshot.StopReason != "run_budget_exceeded" || checkpoint.Snapshot.DeniedCalls == 0 || checkpoint.Snapshot.KnownCalls == 0 ||
		checkpoint.Snapshot.Cost == nil || checkpoint.Snapshot.Cost.Reservation == nil ||
		checkpoint.Snapshot.Cost.Reservation.OutstandingAmount != "0.000000000000" || checkpoint.Snapshot.Cost.Reservation.RetainedAmount != "0.000000000000" {
		return nil, proof, ErrColdToolHandoff
	}
	guards, err := tools.RestoreFileReadGuards(handoff.ReadGuards)
	if err != nil {
		return nil, proof, ErrColdToolHandoff
	}
	claimStore, unlock, err := openColdToolDispatchClaim(ctx, native.CWD, checkpoint.SegmentID)
	if err != nil {
		return nil, proof, err
	}
	defer claimStore.Close() //nolint:errcheck // Publication and parent sync are checked below.
	defer unlock()
	resumed, err := s.ResumeInfo(ctx, info)
	if err != nil {
		return nil, proof, err
	}
	messages := eng.GetMessages()
	actualSHA, err := recoveryProofMessagesSHA256(messages)
	resumedSHA, resumedErr := recoveryProofMessagesSHA256(resumed.Messages)
	if err != nil || resumedErr != nil || actualSHA != resumedSHA || actualSHA != native.MessagesSHA256 || len(messages) != native.MessageCount ||
		eng.SessionID() != native.SessionID || eng.ThreadID() != native.ThreadID || eng.GetCWD() != native.CWD ||
		!reflect.DeepEqual(eng.providerUsageSummary(), native.NativeUsage) || !reflect.DeepEqual(recoveryProofToolBoundary(messages, native.CWD), native.ToolBoundary) ||
		!reflect.DeepEqual(recoveryProofBlockers(resumed, eng.RuntimeItems(), eng.providerUsageSummary()), []string{"native_turn_incomplete"}) {
		return nil, proof, ErrColdToolHandoff
	}
	// Revalidate file bytes after canonical loading and before consuming the
	// durable launch claim. A fresh process must hydrate them again after death.
	if _, err := guards.Snapshot(native.CWD); err != nil {
		return nil, proof, ErrColdToolHandoff
	}
	proof = ColdToolStateProof{
		Version: 1, Scope: "cold_file_tool_state_restore", SessionID: native.SessionID, NativeSHA256: actualSHA,
		SourceSegmentID: checkpoint.SegmentID, NewSegmentID: fresh.CallLedger.SegmentID, GuardCount: len(handoff.ReadGuards.Entries),
		ReadGuardsRestored: true, DispatchClaimed: true, DispatchMode: "explicit_new_user_turn",
	}
	if err := writeColdToolDispatchClaim(claimStore, proof); err != nil {
		return nil, ColdToolStateProof{}, err
	}
	return guards, proof, nil
}

func claimColdToolDispatch(cwd string, proof ColdToolStateProof) error {
	store, unlock, err := openColdToolDispatchClaim(context.Background(), cwd, proof.SourceSegmentID)
	if err != nil {
		return err
	}
	defer store.Close() //nolint:errcheck // The claim itself and parent are explicitly synced.
	defer unlock()
	return writeColdToolDispatchClaim(store, proof)
}

func openColdToolDispatchClaim(ctx context.Context, cwd, sourceID string) (*statemigration.CanonicalStore, func(), error) {
	if value, err := uuid.Parse(sourceID); err != nil || value == uuid.Nil || value.String() != sourceID {
		return nil, nil, ErrColdToolHandoff
	}
	store, _, err := statemigration.OpenCanonicalStore(filepath.Join(cwd, ".yhc"), "cold-dispatch-claims", true)
	if err != nil {
		return nil, nil, ErrColdToolHandoff
	}
	unlock, err := store.Lock(ctx, sourceID+".lock")
	if err != nil {
		_ = store.Close()
		return nil, nil, ErrColdToolHandoff
	}
	_, err = store.Root().Lstat(sourceID + ".json")
	if !errors.Is(err, os.ErrNotExist) {
		unlock()
		_ = store.Close()
		return nil, nil, ErrColdToolHandoff
	}
	return store, unlock, nil
}

func writeColdToolDispatchClaim(store *statemigration.CanonicalStore, proof ColdToolStateProof) error {
	name := proof.SourceSegmentID + ".json"
	file, err := store.Root().OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return ErrColdToolHandoff
	}
	raw, err := json.Marshal(proof)
	if err != nil {
		_ = file.Close()
		return ErrColdToolHandoff
	}
	n, writeErr := file.Write(raw)
	syncErr := file.Sync()
	info, statErr := file.Stat()
	closeErr := file.Close()
	if writeErr != nil || n != len(raw) || syncErr != nil || statErr != nil || closeErr != nil || store.ValidateRegular(name, info) != nil || store.Sync() != nil || store.Revalidate() != nil {
		return ErrColdToolHandoff
	}
	return nil
}
