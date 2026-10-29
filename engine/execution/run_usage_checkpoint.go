package execution

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
)

var ErrRunUsageCheckpoint = errors.New("run usage checkpoint unavailable or inconsistent")

// RunUsageCheckpointStatus describes persistence separately from token coverage.
// A known provider receipt can survive in the terminal result even when writing
// its checkpoint failed. Such a failure always fences subsequent dispatch.
type RunUsageCheckpointStatus struct {
	Version  int    `json:"version"`
	Sequence uint64 `json:"sequence"`
	Failed   bool   `json:"failed"`
}

type runUsageCheckpointState struct {
	store       runUsageCheckpointStore
	sequence    uint64
	metadataSHA string
}

type runUsageCheckpointPayload struct {
	SegmentID       string           `json:"segment_id"`
	MetadataSHA256  string           `json:"metadata_sha256"`
	Sequence        uint64           `json:"sequence"`
	Phase           string           `json:"phase"`
	AdmissionClosed bool             `json:"admission_closed"`
	Snapshot        RunUsageSnapshot `json:"snapshot"`
}

type runUsageCheckpointEnvelope struct {
	Version int             `json:"version"`
	SHA256  string          `json:"sha256"`
	Data    json.RawMessage `json:"data"`
}

// RunUsageCheckpoint is a read-only accounting view, never a new RunUsage or a
// spending grant. No API in this workflow imports its limits into a live run.
type RunUsageCheckpoint struct {
	Version               int              `json:"version"`
	SegmentID             string           `json:"segment_id"`
	Sequence              uint64           `json:"sequence"`
	Phase                 string           `json:"phase"`
	AdmissionClosed       bool             `json:"admission_closed"`
	Snapshot              RunUsageSnapshot `json:"snapshot"`
	TokenUsageComplete    bool             `json:"token_usage_complete"`
	CostComplete          bool             `json:"cost_complete"`
	AccountingComplete    bool             `json:"accounting_complete"`
	CanResumeAgent        bool             `json:"can_resume_agent"`
	NewSpendingAuthorized bool             `json:"new_spending_authorized"`
}

// EnableCheckpointDir opts a fresh collector into synchronous private state
// publication. The directory is exclusively claimed and can never be reopened
// for writing. Defaults remain memory-only; existing history is never backfilled.
func (r *RunUsage) EnableCheckpointDir(path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.checkpoint != nil || r.sealed || r.totals.ProviderCalls != 0 || r.totals.ReleasedCalls != 0 || r.denied != 0 || r.untracked != 0 {
		if r.checkpoint == nil {
			r.checkpoint = &runUsageCheckpointState{}
		}
		return r.failCheckpointLocked()
	}
	snapshot := r.snapshotLocked()
	metadata, err := checkpointMetadataSHA(snapshot)
	r.checkpoint = &runUsageCheckpointState{metadataSHA: metadata}
	if err != nil {
		return r.failCheckpointLocked()
	}
	store, err := newRunUsageCheckpointFileStore(path, r.segmentID, metadata)
	if err != nil {
		return r.failCheckpointLocked()
	}
	r.checkpoint.store = store
	if err := r.persistCheckpointLocked("initial"); err != nil {
		_ = store.Close()
		return err
	}
	return nil
}

func (r *RunUsage) failCheckpointLocked() error {
	if r.checkpointErr == nil {
		r.checkpointErr = errors.Join(ErrRunUsageUnknown, ErrRunUsageCheckpoint)
	}
	r.stopReason, r.budgetCause = "run_usage_unknown", ""
	return r.checkpointErr
}

func (r *RunUsage) persistCheckpointLocked(phase string) error {
	if r.checkpoint == nil {
		return nil
	}
	if r.checkpointErr != nil {
		return r.checkpointErr
	}
	snapshot := r.snapshotLocked()
	snapshot.Checkpoint = nil
	for i := range snapshot.Routes {
		route := &snapshot.Routes[i]
		route.Model, route.Source = runUsageModelLabel(route.Model), runUsageSource(route.Source)
		route.Role, route.Effort = runUsageRole(route.Role), runUsageEffort(route.Effort)
	}
	payload := runUsageCheckpointPayload{
		SegmentID: r.segmentID, MetadataSHA256: r.checkpoint.metadataSHA,
		Sequence: r.checkpoint.sequence + 1, Phase: phase, AdmissionClosed: r.sealed, Snapshot: snapshot,
	}
	data, err := json.Marshal(payload)
	if err != nil || payload.Sequence == 0 {
		return r.failCheckpointLocked()
	}
	raw, err := json.Marshal(runUsageCheckpointEnvelope{Version: 1, SHA256: checkpointSHA(data), Data: data})
	if err != nil || len(raw) > maxRunUsageCheckpointBytes || r.checkpoint.store == nil {
		return r.failCheckpointLocked()
	}
	if err := r.checkpoint.store.WriteCheckpoint(raw); err != nil {
		return r.failCheckpointLocked()
	}
	r.checkpoint.sequence = payload.Sequence
	return nil
}

func checkpointMetadataSHA(snapshot RunUsageSnapshot) (string, error) {
	var card *RunPriceCard
	if snapshot.Cost != nil {
		card = &snapshot.Cost.PriceCard
	}
	data, err := json.Marshal(struct {
		Limits RunUsageLimits `json:"limits"`
		Card   *RunPriceCard  `json:"price_card"`
	}{snapshot.Limits, card})
	if err != nil {
		return "", err
	}
	return checkpointSHA(data), nil
}

func checkpointSHA(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func decodeCheckpointJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return ErrRunUsageCheckpoint
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return ErrRunUsageCheckpoint
	}
	return nil
}

// ReadRunUsageCheckpointDir validates a retained record against the caller's
// expected invocation identity. It does not open a provider, mutate history,
// release a reservation, restore a budget, or activate an Agent.
func ReadRunUsageCheckpointDir(path, expectedSegmentID string) (RunUsageCheckpoint, error) {
	var result RunUsageCheckpoint
	raw, metadata, err := readRunUsageCheckpointFile(path, expectedSegmentID)
	if err != nil || len(raw) > maxRunUsageCheckpointBytes {
		return result, ErrRunUsageCheckpoint
	}
	var envelope runUsageCheckpointEnvelope
	if decodeCheckpointJSON(raw, &envelope) != nil || envelope.Version != 1 || envelope.SHA256 != checkpointSHA(envelope.Data) {
		return result, ErrRunUsageCheckpoint
	}
	var payload runUsageCheckpointPayload
	if decodeCheckpointJSON(envelope.Data, &payload) != nil || payload.SegmentID != expectedSegmentID ||
		runUsageUUID(payload.SegmentID) != payload.SegmentID || payload.Sequence == 0 || payload.MetadataSHA256 != metadata {
		return result, ErrRunUsageCheckpoint
	}
	actualMetadata, err := checkpointMetadataSHA(payload.Snapshot)
	if err != nil || actualMetadata != metadata || validateCheckpointSnapshot(payload) != nil {
		return result, ErrRunUsageCheckpoint
	}
	result = RunUsageCheckpoint{
		Version: 1, SegmentID: payload.SegmentID, Sequence: payload.Sequence,
		Phase: payload.Phase, AdmissionClosed: payload.AdmissionClosed, Snapshot: payload.Snapshot,
		TokenUsageComplete: payload.Snapshot.Complete,
	}
	result.CostComplete = payload.Snapshot.Cost != nil && payload.Snapshot.Cost.Complete
	result.AccountingComplete = result.AdmissionClosed && result.TokenUsageComplete && result.CostComplete
	return result, nil
}
