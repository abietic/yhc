//go:build windows

package worktree

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

func replaceRecordFile(source, target string) error {
	sourcePath, err := windows.UTF16PtrFromString(filepath.Clean(source))
	if err != nil {
		return err
	}
	targetPath, err := windows.UTF16PtrFromString(filepath.Clean(target))
	if err != nil {
		return err
	}
	return windows.MoveFileEx(
		sourcePath,
		targetPath,
		windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH,
	)
}
