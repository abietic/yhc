//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func publishNoReplace(target reportTarget, data []byte, options publicationOptions) error {
	path := target.path
	parent := filepath.Dir(path)
	directory, err := openLockedReportDirectory(parent, target.parentInfo)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(directory)
	temporary, err := os.CreateTemp(parent, ".evaluation-report-*")
	if err != nil {
		return fail("report_write_failed", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	err = temporary.Chmod(0o600)
	if err == nil {
		_, err = temporary.Write(data)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fail("report_write_failed", err)
	}
	if options.beforeLink != nil {
		if err := options.beforeLink(parent); err != nil {
			return fail("report_parent_replaced", err)
		}
	}
	if err := validateLockedReportDirectory(parent, target.parentInfo); err != nil {
		return err
	}
	if err := os.Link(temporaryPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fail("report_collision", err)
		}
		return fail("report_write_failed", err)
	}
	if err := validateLockedReportDirectory(parent, target.parentInfo); err != nil {
		_ = os.Remove(path)
		return err
	}
	if err := os.Remove(temporaryPath); err != nil {
		_ = os.Remove(path)
		return fail("report_write_failed", err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != int64(len(data)) {
		_ = os.Remove(path)
		return fail("report_write_failed", err)
	}
	return nil
}

func openLockedReportDirectory(path string, expected os.FileInfo) (windows.Handle, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, fail("report_parent_invalid", err)
	}
	// Omitting FILE_SHARE_DELETE prevents the approved directory from being
	// renamed or replaced while path-based Windows publication is in progress.
	handle, err := windows.CreateFile(
		pointer,
		windows.FILE_LIST_DIRECTORY,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return windows.InvalidHandle, fail("report_parent_invalid", err)
	}
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil ||
		information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 ||
		information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(handle)
		return windows.InvalidHandle, fail("report_parent_invalid", err)
	}
	if err := validateLockedReportDirectory(path, expected); err != nil {
		_ = windows.CloseHandle(handle)
		return windows.InvalidHandle, err
	}
	return handle, nil
}

func validateLockedReportDirectory(path string, expected os.FileInfo) error {
	current, err := os.Lstat(path)
	if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || current.Mode().Perm() != 0o700 {
		return fail("report_parent_replaced", err)
	}
	if !os.SameFile(current, expected) {
		return fail("report_parent_replaced", nil)
	}
	return nil
}
