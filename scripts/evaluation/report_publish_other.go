//go:build !darwin && !linux && !windows

package main

import (
	"errors"
	"os"
	"path/filepath"
)

func publishNoReplace(target reportTarget, data []byte, options publicationOptions) error {
	path := target.path
	parent := filepath.Dir(path)
	directory, err := os.Open(parent)
	if err != nil {
		return fail("report_parent_invalid", err)
	}
	defer directory.Close()
	if err := validateOpenReportDirectoryFallback(directory, parent, target.parentInfo); err != nil {
		return err
	}
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
	if err := validateOpenReportDirectoryFallback(directory, parent, target.parentInfo); err != nil {
		return err
	}
	if err := os.Link(temporaryPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fail("report_collision", err)
		}
		return fail("report_write_failed", err)
	}
	if err := validateOpenReportDirectoryFallback(directory, parent, target.parentInfo); err != nil {
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

func validateOpenReportDirectoryFallback(directory *os.File, path string, expected os.FileInfo) error {
	opened, err := directory.Stat()
	if err != nil || !opened.IsDir() || opened.Mode().Perm() != 0o700 {
		return fail("report_parent_invalid", err)
	}
	current, err := os.Lstat(path)
	if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || current.Mode().Perm() != 0o700 {
		return fail("report_parent_replaced", err)
	}
	if !os.SameFile(opened, current) {
		return fail("report_parent_replaced", nil)
	}
	if !os.SameFile(opened, expected) {
		return fail("report_parent_replaced", nil)
	}
	return nil
}
