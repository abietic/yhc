package filehistory

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

const coldOriginFile = "cold-origin.json"

// ColdHistoryProof is a bounded observation, never permission or spending
// authority. Physical files and root-independent logical state are bound apart.
type ColdHistoryProof struct {
	Version        int    `json:"version"`
	Scope          string `json:"scope"`
	SessionID      string `json:"session_id"`
	Root           string `json:"root"`
	RootIdentity   string `json:"root_identity"`
	JournalSHA256  string `json:"journal_sha256"`
	JournalBytes   int64  `json:"journal_bytes"`
	RestoreSHA256  string `json:"restore_sha256"`
	RestoreBytes   int64  `json:"restore_bytes"`
	OriginSHA256   string `json:"origin_sha256"`
	OriginBytes    int64  `json:"origin_bytes"`
	FrameSequence  uint64 `json:"frame_sequence"`
	RestoreState   string `json:"restore_state"`
	SemanticSHA256 string `json:"semantic_sha256"`
	SHA256         string `json:"sha256"`
}

// ColdHistoryImport must be anchored outside the restored task workspace. Its
// target binding belongs to the caller's admitted, quiescent workspace epoch.
type ColdHistoryImport struct {
	Version       int              `json:"version"`
	Source        ColdHistoryProof `json:"source"`
	SourceSHA256  string           `json:"source_sha256"`
	MigrationID   string           `json:"migration_id"`
	BindingSHA256 string           `json:"binding_sha256"`
}

// ColdHistoryReceipt preserves source provenance alongside a target-local
// journal. Confirmation diagnostics in migrated operations are not grants.
type ColdHistoryReceipt struct {
	Version          int              `json:"version"`
	Scope            string           `json:"scope"`
	SessionID        string           `json:"session_id"`
	MigrationID      string           `json:"migration_id"`
	BindingSHA256    string           `json:"binding_sha256"`
	Source           ColdHistoryProof `json:"source"`
	Root             string           `json:"root"`
	RootIdentity     string           `json:"root_identity"`
	JournalSHA256    string           `json:"journal_sha256"`
	JournalBytes     int64            `json:"journal_bytes"`
	RestoreSHA256    string           `json:"restore_sha256"`
	RestoreBytes     int64            `json:"restore_bytes"`
	ArchiveDirectory string           `json:"archive_directory"`
	CanResumeAgent   bool             `json:"can_resume_agent"`
}

type coldState struct {
	files     map[string][]byte
	frames    []journalFrame
	journal   *RevisionJournal
	operation *RestoreOperation
	evolution *restoreEvolution
}

func coldSHA(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func coldFileSHA(files map[string][]byte, name string) string {
	if raw, ok := files[name]; ok {
		return coldSHA(raw)
	}
	return ""
}

func coldProofDigest(p ColdHistoryProof) string {
	p.SHA256 = ""
	raw, _ := json.Marshal(p)
	return coldSHA(raw)
}

func coldHex(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}

func validColdSession(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for i, c := range []byte(value) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			continue
		}
		if i == 0 || c != '.' && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

// ValidateColdHistoryProof checks the exact versioned scalar contract.
func ValidateColdHistoryProof(p ColdHistoryProof) error {
	if p.Version != 1 || p.Scope != "workspace-history-state-v1" || !validColdSession(p.SessionID) || !filepath.IsAbs(p.Root) || filepath.Clean(p.Root) != p.Root || p.Root == string(filepath.Separator) || len(p.Root) > 2048 || strings.ContainsRune(p.Root, 0) || len(p.RootIdentity) == 0 || len(p.RootIdentity) > 256 || !coldHex(p.SemanticSHA256) || !coldHex(p.SHA256) || p.SHA256 != coldProofDigest(p) || p.FrameSequence > 1200 {
		return fmt.Errorf("invalid cold history proof")
	}
	for _, f := range []struct {
		digest      string
		size, limit int64
	}{{p.JournalSHA256, p.JournalBytes, maxJournalBytes}, {p.RestoreSHA256, p.RestoreBytes, maxRestoreEncodedBytes}, {p.OriginSHA256, p.OriginBytes, 16384}} {
		if f.size < 0 || f.size > f.limit || f.digest == "" && f.size != 0 || f.digest != "" && !coldHex(f.digest) {
			return fmt.Errorf("invalid cold history file binding")
		}
	}
	if p.JournalSHA256 == "" && p.FrameSequence != 0 || p.RestoreSHA256 == "" && p.RestoreState != "none" || p.RestoreSHA256 != "" && (p.RestoreState != "completed" && p.RestoreState != "rolled_back") {
		return fmt.Errorf("unverified cold history operation")
	}
	return nil
}

func coldDecode(raw []byte, to any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(to); err != nil {
		return fmt.Errorf("invalid cold history encoding")
	}
	if dec.Decode(new(any)) != io.EOF {
		return fmt.Errorf("trailing cold history data")
	}
	return nil
}

func readColdFiles(parent *os.Root, name string) (map[string][]byte, error) {
	info, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return nil, fmt.Errorf("cold history directory unavailable")
	}
	r, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	opened, err := r.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("cold history directory changed")
	}
	dir, err := r.Open(".")
	if err != nil {
		return nil, err
	}
	names, readErr := dir.Readdirnames(4)
	closeErr := dir.Close()
	if readErr != nil && readErr != io.EOF || closeErr != nil || len(names) > 3 {
		return nil, fmt.Errorf("unknown cold history files")
	}
	result := make(map[string][]byte, len(names))
	for _, file := range names {
		limit := int64(maxJournalBytes)
		switch file {
		case journalFile:
		case restoreOperationFile:
			limit = maxRestoreEncodedBytes
		case coldOriginFile:
			limit = 16384
		default:
			return nil, fmt.Errorf("unverified history staging artifact")
		}
		before, err := r.Lstat(file)
		if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0o600 || !restoreFileSingleLink(before) || before.Size() > limit {
			return nil, fmt.Errorf("invalid cold history file")
		}
		f, err := r.Open(file)
		if err != nil {
			return nil, err
		}
		start, statErr := f.Stat()
		raw, readErr := io.ReadAll(io.LimitReader(f, limit+1))
		end, endErr := f.Stat()
		closeErr := f.Close()
		current, currentErr := r.Lstat(file)
		if statErr != nil || readErr != nil || endErr != nil || closeErr != nil || currentErr != nil || !os.SameFile(before, start) || !os.SameFile(start, end) || !os.SameFile(end, current) || !sameCaptureMeta(before, end) || !sameCaptureMeta(end, current) || int64(len(raw)) != before.Size() || int64(len(raw)) > limit {
			return nil, fmt.Errorf("cold history file changed")
		}
		result[file] = raw
	}
	current, err := parent.Lstat(name)
	if err != nil || !os.SameFile(info, current) {
		return nil, fmt.Errorf("cold history namespace changed")
	}
	return result, nil
}

func decodeColdState(files map[string][]byte, root, rootIdentity, session string, actual *RevisionJournal) (*coldState, error) {
	view := &RevisionJournal{rootPath: root, rootIdentity: rootIdentity, sessionID: session, records: map[string]RevisionRecord{}, turns: map[string]TurnRecord{}}
	s := &coldState{files: files, journal: view, frames: []journalFrame{}}
	raw := files[journalFile]
	if len(raw) > maxJournalBytes || len(raw) > 0 && raw[len(raw)-1] != '\n' {
		return nil, fmt.Errorf("truncated cold journal")
	}
	if len(raw) > 0 {
		for _, line := range bytes.Split(raw[:len(raw)-1], []byte{'\n'}) {
			var frame journalFrame
			if len(line) == 0 || len(line) > maxFrameBytes || coldDecode(line, &frame) != nil {
				return nil, fmt.Errorf("invalid cold journal frame")
			}
			if err := view.applyFrame(frame); err != nil {
				return nil, err
			}
			s.frames = append(s.frames, frame)
		}
	}
	for _, turn := range view.turns {
		if turn.FinishedAt.IsZero() || !turn.ToolCountsBounded || turn.KnownToolsRunning != 0 || turn.PendingInteractions != 0 {
			return nil, fmt.Errorf("unclosed history turn")
		}
	}
	for _, record := range view.records {
		if record.Path != "" && (!validOutcome(record.Outcome) || !record.After.Captured) {
			return nil, fmt.Errorf("unclosed history file effect")
		}
	}
	if data, ok := files[restoreOperationFile]; ok {
		var envelope restoreOperationEnvelope
		if coldDecode(data, &envelope) != nil || view.validRestoreEnvelope(envelope) != nil {
			return nil, fmt.Errorf("invalid cold restore operation")
		}
		op := envelope.Operation
		if op.Root != root || op.RootIdentity != rootIdentity || op.SessionID != session || !terminalRestoreState(op.State) {
			return nil, fmt.Errorf("unfinished cold restore operation")
		}
		s.operation = &op
		s.evolution = envelope.Evolution
		if actual != nil {
			check := cloneRestoreOperation(op)
			check.Root, check.RootIdentity = actual.rootPath, actual.rootIdentity
			check.PlanDigest = RestoreOperationPlanDigest(check)
			validator := RevisionJournal{rootPath: actual.rootPath, rootIdentity: actual.rootIdentity, root: actual.root, dir: actual.dir, sessionID: actual.sessionID, frameSequence: view.frameSequence}
			if op.PlanDigest != RestoreOperationPlanDigest(op) || validator.validRestoreOperation(check) != nil {
				return nil, fmt.Errorf("invalid cold restore plan")
			}
			for _, path := range check.Paths {
				if actual.privateRestorePath(path.Path) {
					return nil, fmt.Errorf("private cold restore path")
				}
			}
		}
		if err := s.verifyRestoreReceipts(op); err != nil {
			return nil, err
		}
		if err := s.verifyRestoreEvolution(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *coldState) verifyRestoreReceipts(op RestoreOperation) error {
	for i, path := range op.Paths {
		id := fmt.Sprintf("restore-%s-%d-%s", op.ID, i, op.Direction)
		before, after, tool := path.Original, path.Target, "WorkspaceRestore"
		if op.Direction == "rollback" {
			before, after, tool = path.Target, path.Original, "WorkspaceRestoreRollback"
			for _, key := range s.journal.order {
				if key == id {
					break
				}
				r := s.journal.records[key]
				if r.Path == path.Path {
					before = r.After
				}
			}
		}
		if sameState(before, after) {
			continue
		}
		r, ok := s.journal.records[id]
		if !ok || r.ToolName != tool || r.TurnID != op.ID || r.ToolUseID != op.ID || r.Path != path.Path || r.Outcome != "succeeded" || !sameState(r.Before, before) || !sameState(r.After, after) {
			return fmt.Errorf("cold restore receipts incomplete")
		}
	}
	return nil
}

func (s *coldState) semanticSHA() string {
	frames := make([]journalFrame, len(s.frames))
	copy(frames, s.frames)
	for i := range frames {
		frames[i].Root, frames[i].RootIdentity = "", ""
		frames[i].ColdMigrationID = ""
	}
	var op *RestoreOperation
	if s.operation != nil {
		value := cloneRestoreOperation(*s.operation)
		value.Root, value.RootIdentity, value.PlanDigest = "", "", ""
		op = &value
	}
	raw, _ := json.Marshal(struct {
		Version   int
		SessionID string
		Frames    []journalFrame
		Operation *RestoreOperation
		Evolution *restoreEvolution `json:",omitempty"`
	}{1, s.journal.sessionID, frames, op, s.evolution})
	return coldSHA(raw)
}

func (s *coldState) verifyFiles(actual *RevisionJournal) error {
	latest := map[string]RevisionFileState{}
	for _, id := range s.journal.order {
		r := s.journal.records[id]
		if r.Path != "" {
			latest[r.Path] = r.After
		}
	}
	for path, state := range latest {
		got, err := actual.capture(path)
		if err != nil || !sameState(got, state) {
			return fmt.Errorf("history file bytes changed")
		}
	}
	if s.operation != nil {
		if err := actual.restoreSiblingsAbsent(*s.operation); err != nil {
			return err
		}
	}
	return s.verifyEvolutionSiblingsAbsent(actual)
}

func (s *coldState) proof(root, identity string) ColdHistoryProof {
	p := ColdHistoryProof{
		Version: 1, Scope: "workspace-history-state-v1", SessionID: s.journal.sessionID, Root: root, RootIdentity: identity,
		JournalSHA256: coldFileSHA(s.files, journalFile), JournalBytes: int64(len(s.files[journalFile])), RestoreSHA256: coldFileSHA(s.files, restoreOperationFile), RestoreBytes: int64(len(s.files[restoreOperationFile])),
		OriginSHA256: coldFileSHA(s.files, coldOriginFile), OriginBytes: int64(len(s.files[coldOriginFile])), FrameSequence: s.journal.frameSequence, RestoreState: "none", SemanticSHA256: s.semanticSHA(),
	}
	if s.operation != nil {
		p.RestoreState = s.operation.State
	}
	p.SHA256 = coldProofDigest(p)
	return p
}

// ExportColdState observes only closed, revalidated files. The caller must
// separately establish workspace/service quiescence; this is not a lease.
func (j *RevisionJournal) ExportColdState() (ColdHistoryProof, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !restoreFilesystemSupported() || j.closed || j.sealed || j.warning != "" || j.restorePreview != nil || j.coldImportValidation {
		return ColdHistoryProof{}, fmt.Errorf("history cold export unavailable")
	}
	if err := j.checkRootIdentity(); err != nil {
		return ColdHistoryProof{}, err
	}
	parent, err := os.OpenRoot(filepath.Dir(j.dir))
	if err != nil {
		return ColdHistoryProof{}, err
	}
	defer parent.Close()
	files, err := readColdFiles(parent, filepath.Base(j.dir))
	if err != nil {
		return ColdHistoryProof{}, err
	}
	s, err := decodeColdState(files, j.rootPath, j.rootIdentity, j.sessionID, j)
	if err != nil {
		return ColdHistoryProof{}, err
	}
	if s.journal.frameSequence != j.frameSequence || int64(len(files[journalFile])) != j.bytes || !reflect.DeepEqual(s.journal.records, j.records) || !reflect.DeepEqual(s.journal.turns, j.turns) || !reflect.DeepEqual(s.journal.order, j.order) || !reflect.DeepEqual(s.journal.turnOrder, j.turnOrder) {
		return ColdHistoryProof{}, fmt.Errorf("stale history export")
	}
	if err := s.verifyFiles(j); err != nil {
		return ColdHistoryProof{}, err
	}
	if err := verifyColdOriginChain(parent, s, j); err != nil {
		return ColdHistoryProof{}, err
	}
	again, err := readColdFiles(parent, filepath.Base(j.dir))
	if err != nil || !reflect.DeepEqual(files, again) || j.checkRootIdentity() != nil {
		return ColdHistoryProof{}, fmt.Errorf("history changed during export")
	}
	return s.proof(j.rootPath, j.rootIdentity), nil
}
