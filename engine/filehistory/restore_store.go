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
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	restoreOperationFile    = "restore.json"
	restoreOperationTemp    = "restore.json.tmp"
	maxRestorePaths         = 32
	maxRestoreRawBytes      = 8 << 20
	maxRestoreEncodedBytes  = 12 << 20
	restoreOperationKind    = "workspace-restore-operation"
	restoreOperationVersion = 1
)

// RestoreOperation is private recovery intent and progress. Its confirmation
// fields are diagnostic only; callers must re-authorize any workspace action.
type RestoreOperation struct {
	previewToken                                                                                                string
	ID, SessionID, TurnID, Root, RootIdentity, PlanDigest, ConfirmationID, PermissionRevision, Direction, State string
	JournalRevision                                                                                             uint64
	StartedAt                                                                                                   time.Time
	Paths                                                                                                       []RestorePath
}

// RestorePath records one precomputed, non-authoritative workspace transition.
type RestorePath struct {
	Path, BackupPath, TempPath, Status string
	Original, Target                   RevisionFileState
}

type restoreOperationEnvelope struct {
	Kind      string           `json:"kind"`
	Version   int              `json:"version"`
	Operation RestoreOperation `json:"operation"`
}

type restorePlanState struct {
	Captured bool   `json:"captured"`
	Exists   bool   `json:"exists"`
	Mode     uint32 `json:"mode"`
	Digest   string `json:"digest"`
}

type restorePlanPath struct {
	Path       string           `json:"path"`
	BackupPath string           `json:"backup_path"`
	TempPath   string           `json:"temp_path"`
	Original   restorePlanState `json:"original"`
	Target     restorePlanState `json:"target"`
}

type restorePlan struct {
	ID              string            `json:"id"`
	SessionID       string            `json:"session_id"`
	TurnID          string            `json:"turn_id"`
	Root            string            `json:"root"`
	RootIdentity    string            `json:"root_identity"`
	JournalRevision uint64            `json:"journal_revision"`
	StartedAt       time.Time         `json:"started_at"`
	Paths           []restorePlanPath `json:"paths"`
}

// RestoreOperationPlanDigest returns the stable digest of immutable restore
// intent. It intentionally omits payload bytes and all progress/auth fields.
func RestoreOperationPlanDigest(op RestoreOperation) string {
	plan := restorePlan{ID: op.ID, SessionID: op.SessionID, TurnID: op.TurnID, Root: op.Root, RootIdentity: op.RootIdentity, JournalRevision: op.JournalRevision, StartedAt: op.StartedAt, Paths: make([]restorePlanPath, len(op.Paths))}
	for i, path := range op.Paths {
		plan.Paths[i] = restorePlanPath{Path: path.Path, BackupPath: path.BackupPath, TempPath: path.TempPath, Original: restorePlanState{Captured: path.Original.Captured, Exists: path.Original.Exists, Mode: path.Original.Mode, Digest: path.Original.Digest}, Target: restorePlanState{Captured: path.Target.Captured, Exists: path.Target.Exists, Mode: path.Target.Mode, Digest: path.Target.Digest}}
	}
	b, _ := json.Marshal(plan)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// LoadRestoreOperation reads only the stable operation file. A leftover temp
// file is intentionally ignored: it was never atomically published.
func (j *RevisionJournal) LoadRestoreOperation() (RestoreOperation, bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.loadRestoreOperationLocked()
}

func (j *RevisionJournal) loadRestoreOperationLocked() (RestoreOperation, bool, error) {
	if j.closed {
		return RestoreOperation{}, false, ErrJournalClosed
	}
	if err := j.checkRootIdentity(); err != nil {
		return RestoreOperation{}, false, err
	}
	info, err := os.Lstat(j.dir)
	if os.IsNotExist(err) {
		if j.dirRoot != nil {
			return RestoreOperation{}, false, fmt.Errorf("restore store directory namespace changed")
		}
		return RestoreOperation{}, false, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return RestoreOperation{}, false, fmt.Errorf("restore store directory must be a non-symlink 0700 directory")
	}
	root, err := os.OpenRoot(j.dir)
	if err != nil {
		return RestoreOperation{}, false, fmt.Errorf("open restore store: %w", err)
	}
	defer root.Close()
	if err := j.checkRestoreStoreDirectory(root); err != nil {
		return RestoreOperation{}, false, err
	}
	return j.decodeRestoreOperation(root)
}

func (j *RevisionJournal) decodeRestoreOperation(root *os.Root) (RestoreOperation, bool, error) {
	before, err := root.Lstat(restoreOperationFile)
	if os.IsNotExist(err) {
		return RestoreOperation{}, false, nil
	}
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || before.Mode().Perm() != 0o600 || !restoreFileSingleLink(before) {
		return RestoreOperation{}, false, fmt.Errorf("restore operation must be a non-symlink 0600 regular file")
	}
	f, err := root.Open(restoreOperationFile)
	if err != nil {
		return RestoreOperation{}, false, fmt.Errorf("open restore operation: %w", err)
	}
	opened, statErr := f.Stat()
	if statErr != nil || !os.SameFile(before, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm() != 0o600 || !restoreFileSingleLink(opened) {
		f.Close()
		return RestoreOperation{}, false, fmt.Errorf("restore operation changed during open")
	}
	b, readErr := io.ReadAll(io.LimitReader(f, maxRestoreEncodedBytes+1))
	closeErr := f.Close()
	if readErr != nil || closeErr != nil || len(b) > maxRestoreEncodedBytes {
		return RestoreOperation{}, false, fmt.Errorf("read restore operation")
	}
	var envelope restoreOperationEnvelope
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return RestoreOperation{}, false, fmt.Errorf("decode restore operation: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return RestoreOperation{}, false, fmt.Errorf("restore operation contains trailing data")
	}
	if envelope.Kind != restoreOperationKind || envelope.Version != restoreOperationVersion || j.validRestoreOperation(envelope.Operation) != nil {
		return RestoreOperation{}, false, fmt.Errorf("invalid restore operation")
	}
	return cloneRestoreOperation(envelope.Operation), true, nil
}

// SaveRestoreOperation atomically publishes a fully validated operation. It
// persists intent/progress but never applies it to the workspace.
func (j *RevisionJournal) SaveRestoreOperation(operation RestoreOperation) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return ErrJournalClosed
	}
	if err := j.validRestoreOperation(operation); err != nil {
		return err
	}
	unlock, err := j.lockStoreLocked()
	if err != nil {
		return err
	}
	defer unlock()
	return j.saveRestoreOperationLocked(operation)
}

func (j *RevisionJournal) saveRestoreOperationLocked(operation RestoreOperation) error {
	if j.closed {
		return ErrJournalClosed
	}
	if err := j.checkRootIdentity(); err != nil {
		return err
	}
	if err := j.validRestoreOperation(operation); err != nil {
		return err
	}
	if err := j.ensureDirForAppend(); err != nil {
		return err
	}
	if j.dirRoot == nil {
		return fmt.Errorf("restore store directory unavailable")
	}
	if err := j.checkRestoreStoreDirectory(j.dirRoot); err != nil {
		return err
	}
	prior, found, err := j.decodeRestoreOperation(j.dirRoot)
	if err != nil {
		return err
	}
	if err := j.validRestoreTransition(prior, found, operation); err != nil {
		return err
	}
	if (!found && operation.JournalRevision != j.frameSequence) || (found && operation.ID != prior.ID && operation.JournalRevision != j.frameSequence) || (found && operation.ID == prior.ID && operation.JournalRevision > j.frameSequence) {
		return fmt.Errorf("restore operation journal revision mismatch")
	}
	encoded, err := json.Marshal(restoreOperationEnvelope{Kind: restoreOperationKind, Version: restoreOperationVersion, Operation: operation})
	if err != nil || len(encoded) > maxRestoreEncodedBytes {
		return fmt.Errorf("encode restore operation")
	}
	before, err := stableRestoreFile(j.dirRoot)
	if err != nil {
		return err
	}
	f, err := j.openRestoreTemp()
	if err != nil {
		return err
	}
	n, writeErr := f.Write(encoded)
	syncErr := f.Sync()
	writtenInfo, statErr := f.Stat()
	closeErr := f.Close()
	if writeErr != nil || n != len(encoded) || syncErr != nil || closeErr != nil || statErr != nil {
		return fmt.Errorf("restore operation write uncertainty")
	}
	if j.restoreBeforeStorePublish != nil {
		j.restoreBeforeStorePublish()
	}
	tempInfo, tempErr := j.dirRoot.Lstat(restoreOperationTemp)
	if tempErr != nil || !os.SameFile(writtenInfo, tempInfo) || !sameCaptureMeta(writtenInfo, tempInfo) || !restoreFileSingleLink(tempInfo) {
		return fmt.Errorf("restore intent temporary namespace changed")
	}
	if err := j.checkRestoreStoreDirectory(j.dirRoot); err != nil {
		return err
	}
	after, err := stableRestoreFile(j.dirRoot)
	if err != nil || !sameStableRestoreFile(before, after) {
		return fmt.Errorf("restore operation changed before publish")
	}
	if err := j.dirRoot.Rename(restoreOperationTemp, restoreOperationFile); err != nil {
		return fmt.Errorf("publish restore operation: %w", err)
	}
	published, publishErr := j.dirRoot.Lstat(restoreOperationFile)
	if publishErr != nil || !os.SameFile(writtenInfo, published) || !sameCaptureMeta(writtenInfo, published) || !restoreFileSingleLink(published) {
		return fmt.Errorf("restore intent changed during publication")
	}
	if err := j.syncDirectory(j.dirRoot); err != nil {
		return fmt.Errorf("sync restore operation directory: %w", err)
	}
	return j.checkRestoreStoreDirectory(j.dirRoot)
}

func (j *RevisionJournal) checkRestoreStoreDirectory(root *os.Root) error {
	namespace, err := os.Lstat(j.dir)
	if err != nil || namespace.Mode()&os.ModeSymlink != 0 || !namespace.IsDir() || namespace.Mode().Perm() != 0o700 {
		return fmt.Errorf("restore store directory must be a non-symlink 0700 directory")
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(namespace, opened) {
		return fmt.Errorf("restore store directory changed during open")
	}
	if j.dirRoot != nil {
		bound, err := j.dirRoot.Stat(".")
		if err != nil || !os.SameFile(namespace, bound) {
			return fmt.Errorf("restore store directory namespace changed")
		}
	}
	return nil
}

func (j *RevisionJournal) openRestoreTemp() (*os.File, error) {
	info, err := j.dirRoot.Lstat(restoreOperationTemp)
	if os.IsNotExist(err) {
		f, openErr := j.dirRoot.OpenFile(restoreOperationTemp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if openErr != nil {
			return nil, openErr
		}
		if chmodErr := f.Chmod(0o600); chmodErr != nil {
			f.Close()
			return nil, chmodErr
		}
		return f, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !restoreFileSingleLink(info) {
		return nil, fmt.Errorf("restore operation temp must be a non-symlink 0600 regular file")
	}
	f, err := j.dirRoot.OpenFile(restoreOperationTemp, os.O_WRONLY, 0)
	if err != nil {
		return nil, err
	}
	opened, statErr := f.Stat()
	if statErr != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm() != 0o600 || !restoreFileSingleLink(opened) {
		f.Close()
		return nil, fmt.Errorf("restore operation temp changed during open")
	}
	if err := f.Truncate(0); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

type stableRestoreIdentity struct {
	info os.FileInfo
	size int64
	mode os.FileMode
	mod  time.Time
}

func stableRestoreFile(root *os.Root) (*stableRestoreIdentity, error) {
	info, err := root.Lstat(restoreOperationFile)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !restoreFileSingleLink(info) {
		return nil, fmt.Errorf("restore operation must be a non-symlink 0600 regular file")
	}
	return &stableRestoreIdentity{info: info, size: info.Size(), mode: info.Mode(), mod: info.ModTime()}, nil
}

func sameStableRestoreFile(a, b *stableRestoreIdentity) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && os.SameFile(a.info, b.info) && a.size == b.size && a.mode == b.mode && a.mod.Equal(b.mod))
}

func (j *RevisionJournal) validRestoreOperation(op RestoreOperation) error {
	if !validRestoreText(op.ID) || !validRestoreText(op.SessionID) || !validRestoreText(op.TurnID) || !validRestoreText(op.PlanDigest) || !validRestoreText(op.ConfirmationID) || !validRestoreText(op.PermissionRevision) || op.SessionID != j.sessionID || op.Root != j.rootPath || op.RootIdentity != j.rootIdentity || op.JournalRevision > j.frameSequence || op.StartedAt.IsZero() || op.StartedAt.Location() != time.UTC || !validRestoreDirection(op.Direction) || !validRestoreState(op.State) || len(op.Paths) == 0 || len(op.Paths) > maxRestorePaths {
		return fmt.Errorf("invalid restore operation identity")
	}
	var raw int
	seen := map[string]struct{}{}
	previous := ""
	for index, path := range op.Paths {
		if !validRestoreSiblingNames(op.ID, index, path) {
			return fmt.Errorf("invalid owned restore sibling names")
		}
		if path.Path <= previous || !validRestoreStatus(path.Status) || j.validRestorePath(path, seen) != nil || !validCapturedRestoreState(path.Original) || !validCapturedRestoreState(path.Target) {
			return fmt.Errorf("invalid restore operation path")
		}
		previous = path.Path
		raw += len(path.Original.Data) + len(path.Target.Data)
		if raw > maxRestoreRawBytes {
			return fmt.Errorf("restore operation raw bytes exceed limit")
		}
	}
	if op.PlanDigest != RestoreOperationPlanDigest(op) {
		return fmt.Errorf("restore operation plan digest mismatch")
	}
	if op.State == "completed" && (op.Direction != "restore" || !allRestoreStatuses(op.Paths, "applied", "already_applied")) {
		return fmt.Errorf("invalid completed restore operation")
	}
	if op.State == "rolled_back" && (op.Direction != "rollback" || !allRestoreStatuses(op.Paths, "rolled_back", "already_applied")) {
		return fmt.Errorf("invalid rolled back restore operation")
	}
	return nil
}

func validRestoreSiblingNames(id string, index int, path RestorePath) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	parent := filepath.Dir(path.Path)
	return path.BackupPath == filepath.Join(parent, fmt.Sprintf(".yhc-restore-%s-%d.backup", id, index)) && path.TempPath == filepath.Join(parent, fmt.Sprintf(".yhc-restore-%s-%d.tmp", id, index))
}

func (j *RevisionJournal) validRestorePath(path RestorePath, seen map[string]struct{}) error {
	for _, name := range []string{path.Path, path.BackupPath, path.TempPath} {
		if !validStoredPath(name) {
			return fmt.Errorf("invalid restore path")
		}
		rel, err := j.cleanPath(name)
		if err != nil || rel != name {
			return fmt.Errorf("invalid restore path")
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("duplicate restore path")
		}
		seen[name] = struct{}{}
	}
	if filepath.Dir(path.BackupPath) != filepath.Dir(path.Path) || filepath.Dir(path.TempPath) != filepath.Dir(path.Path) {
		return fmt.Errorf("restore sibling path mismatch")
	}
	return nil
}

func validCapturedRestoreState(state RevisionFileState) bool {
	return state.Captured && validState(state) && (!state.Exists || state.Digest != "")
}

func validRestoreText(value string) bool {
	if value == "" || len(value) > 256 || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func validRestoreDirection(value string) bool { return value == "restore" || value == "rollback" }
func validRestoreState(value string) bool {
	return value == "applying" || value == "partial" || value == "completed" || value == "rolled_back"
}

func validRestoreStatus(value string) bool {
	return value == "pending" || value == "applied" || value == "already_applied" || value == "conflict" || value == "failed" || value == "rolled_back"
}

func (j *RevisionJournal) validRestoreTransition(previous RestoreOperation, found bool, next RestoreOperation) error {
	if !found {
		return nil
	}
	if previous.ID != next.ID {
		if previous.State == "applying" || previous.State == "partial" {
			return fmt.Errorf("active restore operation cannot be replaced")
		}
		return nil
	}
	if !sameRestoreImmutable(previous, next) {
		return fmt.Errorf("restore operation immutable fields changed")
	}
	if previous.State == "completed" && next.State == "applying" {
		return fmt.Errorf("completed restore operation cannot reapply")
	}
	if terminalRestoreState(previous.State) && (next.State != previous.State || next.Direction != previous.Direction || !sameRestoreStatuses(previous.Paths, next.Paths)) {
		return fmt.Errorf("terminal restore operation cannot change progress")
	}
	return nil
}

func terminalRestoreState(state string) bool { return state == "completed" || state == "rolled_back" }

func allRestoreStatuses(paths []RestorePath, allowed ...string) bool {
	for _, path := range paths {
		found := false
		for _, status := range allowed {
			found = found || path.Status == status
		}
		if !found {
			return false
		}
	}
	return true
}

func sameRestoreStatuses(a, b []RestorePath) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Status != b[i].Status {
			return false
		}
	}
	return true
}

func sameRestoreImmutable(a, b RestoreOperation) bool {
	if a.ID != b.ID || a.SessionID != b.SessionID || a.TurnID != b.TurnID || a.Root != b.Root || a.RootIdentity != b.RootIdentity || a.PlanDigest != b.PlanDigest || a.JournalRevision != b.JournalRevision || !a.StartedAt.Equal(b.StartedAt) || len(a.Paths) != len(b.Paths) {
		return false
	}
	for i := range a.Paths {
		x, y := a.Paths[i], b.Paths[i]
		if x.Path != y.Path || x.BackupPath != y.BackupPath || x.TempPath != y.TempPath || !sameState(x.Original, y.Original) || !sameState(x.Target, y.Target) {
			return false
		}
	}
	return true
}

func cloneRestoreOperation(op RestoreOperation) RestoreOperation {
	op.Paths = append([]RestorePath(nil), op.Paths...)
	for i := range op.Paths {
		op.Paths[i].Original.Data = append([]byte(nil), op.Paths[i].Original.Data...)
		op.Paths[i].Target.Data = append([]byte(nil), op.Paths[i].Target.Data...)
	}
	return op
}
