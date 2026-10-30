package filehistory

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// LockWorkspaceHistoryDirectory excludes another process's append/apply while
// the session owner deletes exact private sidecars. It never creates a file.
func LockWorkspaceHistoryDirectory(root *os.Root) (func(), error) {
	file, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	unlock, err := lockRestoreDirectory(file)
	if err != nil {
		file.Close()
		return nil, err
	}
	return func() { unlock(); _ = file.Close() }, nil
}

// RestoreStoreDeletionReady is read-only. Deleting recovery authority is
// refused while an operation is unfinished, or while any sibling artifact
// remains in the workspace. Session deletion never follows stored paths to
// remove an arbitrary tree or silently abandons a recoverable operation.
func RestoreStoreDeletionReady(dir, sessionID string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	info, err := root.Lstat(restoreOperationFile)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !restoreFileSingleLink(info) {
		return fmt.Errorf("unsafe restore store")
	}
	file, err := root.Open(restoreOperationFile)
	if err != nil {
		return err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		file.Close()
		return fmt.Errorf("restore store changed")
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, maxRestoreEncodedBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(raw) > maxRestoreEncodedBytes {
		return fmt.Errorf("restore store cannot be inspected")
	}
	var envelope restoreOperationEnvelope
	if json.Unmarshal(raw, &envelope) != nil || envelope.Kind != restoreOperationKind || envelope.Version != restoreOperationVersion || envelope.Operation.SessionID != sessionID || !terminalRestoreState(envelope.Operation.State) {
		return fmt.Errorf("resolve interrupted workspace restore before deleting this session")
	}
	journal, err := OpenRevisionJournal(envelope.Operation.Root, dir, sessionID)
	if err != nil {
		return fmt.Errorf("restore root/history cannot be revalidated before deletion")
	}
	defer journal.Close()
	operation, found, err := journal.LoadRestoreOperation()
	if err != nil || !found || !terminalRestoreState(operation.State) {
		return fmt.Errorf("restore state changed before deletion")
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if err := journal.restoreSiblingsAbsent(operation); err != nil {
		return fmt.Errorf("restore artifacts remain; preserve recovery authority before deleting this session")
	}
	return nil
}

// SyncWorkspaceHistoryDirectory persists deletion metadata on hosts supporting
// guarded restoration; other platforms retain their existing deletion behavior.
func SyncWorkspaceHistoryDirectory(root *os.Root) error {
	if !restoreFilesystemSupported() {
		return nil
	}
	return defaultSyncDirectory(root)
}
