//go:build !darwin && !linux

package filehistory

import "fmt"

func restoreFilesystemSupported() bool { return false }

func (*RevisionJournal) restorePathFilesystem(RestorePath, string) (string, error) {
	return "", fmt.Errorf("guarded workspace restore is unavailable on this platform")
}

func (*RevisionJournal) preflightRestorePath(RestorePath, string, bool) error {
	return fmt.Errorf("guarded workspace restore is unavailable on this platform")
}

func (*RevisionJournal) captureRestorePath(string) (RevisionFileState, error) {
	return RevisionFileState{}, fmt.Errorf("guarded workspace restore is unavailable on this platform")
}

func (*RevisionJournal) cleanupRestoreSiblings(RestoreOperation) error {
	return fmt.Errorf("restore cleanup is unavailable on this platform")
}

func (*RevisionJournal) restoreSiblingsAbsent(RestoreOperation) error {
	return fmt.Errorf("native restore artifact validation is unavailable")
}
