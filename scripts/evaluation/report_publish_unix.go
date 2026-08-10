//go:build darwin || linux

package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func publishNoReplace(target reportTarget, data []byte, options publicationOptions) error {
	parent := filepath.Dir(target.path)
	name := filepath.Base(target.path)
	directoryFD, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fail("report_parent_invalid", err)
	}
	defer unix.Close(directoryFD)
	if err := validateOpenReportDirectory(directoryFD, parent, target.parentInfo); err != nil {
		return err
	}
	var existing unix.Stat_t
	if err := unix.Fstatat(directoryFD, name, &existing, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		return fail("report_collision", nil)
	} else if !errors.Is(err, unix.ENOENT) {
		return fail("report_path_invalid", err)
	}
	temporaryName, temporaryFD, err := createReportTemporary(directoryFD)
	if err != nil {
		return err
	}
	temporaryExists := true
	defer func() {
		if temporaryExists {
			_ = unix.Unlinkat(directoryFD, temporaryName, 0)
		}
	}()
	temporary := os.NewFile(uintptr(temporaryFD), temporaryName)
	if temporary == nil {
		_ = unix.Close(temporaryFD)
		return fail("report_write_failed", nil)
	}
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
	if err := unix.Linkat(directoryFD, temporaryName, directoryFD, name, 0); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return fail("report_collision", err)
		}
		return fail("report_write_failed", err)
	}
	if err := unix.Unlinkat(directoryFD, temporaryName, 0); err != nil {
		_ = unix.Unlinkat(directoryFD, name, 0)
		return fail("report_write_failed", err)
	}
	temporaryExists = false
	if err := unix.Fsync(directoryFD); err != nil {
		_ = unix.Unlinkat(directoryFD, name, 0)
		return fail("report_write_failed", err)
	}
	if err := validateOpenReportDirectory(directoryFD, parent, target.parentInfo); err != nil {
		_ = unix.Unlinkat(directoryFD, name, 0)
		_ = unix.Fsync(directoryFD)
		return err
	}
	var published unix.Stat_t
	if err := unix.Fstatat(directoryFD, name, &published, unix.AT_SYMLINK_NOFOLLOW); err != nil ||
		published.Mode&unix.S_IFMT != unix.S_IFREG || published.Mode&0o777 != 0o600 || published.Size != int64(len(data)) {
		_ = unix.Unlinkat(directoryFD, name, 0)
		return fail("report_write_failed", err)
	}
	return nil
}

func validateOpenReportDirectory(directoryFD int, path string, expected os.FileInfo) error {
	var opened unix.Stat_t
	if err := unix.Fstat(directoryFD, &opened); err != nil || opened.Mode&unix.S_IFMT != unix.S_IFDIR || opened.Mode&0o777 != 0o700 {
		return fail("report_parent_invalid", err)
	}
	expectedStat, ok := expected.Sys().(*syscall.Stat_t)
	if !ok || opened.Dev != expectedStat.Dev || opened.Ino != expectedStat.Ino {
		return fail("report_parent_replaced", nil)
	}
	currentFD, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fail("report_parent_replaced", err)
	}
	defer unix.Close(currentFD)
	var current unix.Stat_t
	if err := unix.Fstat(currentFD, &current); err != nil || current.Mode&unix.S_IFMT != unix.S_IFDIR || current.Mode&0o777 != 0o700 {
		return fail("report_parent_replaced", err)
	}
	if opened.Dev != current.Dev || opened.Ino != current.Ino {
		return fail("report_parent_replaced", nil)
	}
	return nil
}

func createReportTemporary(directoryFD int) (string, int, error) {
	for attempt := 0; attempt < 32; attempt++ {
		entropy := make([]byte, 16)
		if _, err := rand.Read(entropy); err != nil {
			return "", -1, fail("report_write_failed", err)
		}
		name := ".evaluation-report-" + hex.EncodeToString(entropy)
		fd, err := unix.Openat(directoryFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		if err == nil {
			return name, fd, nil
		}
		if !errors.Is(err, unix.EEXIST) {
			return "", -1, fail("report_write_failed", err)
		}
	}
	return "", -1, fail("report_write_failed", fmt.Errorf("temporary name retries exhausted"))
}
