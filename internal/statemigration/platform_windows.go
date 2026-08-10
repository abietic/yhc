//go:build windows

package statemigration

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsFileRenameInformation struct {
	ReplaceIfExists uint32
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [1]uint16
}

func tryLockMigration(file *os.File) (bool, error) {
	var overlapped windows.Overlapped
	err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&overlapped,
	)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return false, err
}

func unlockMigration(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(
		windows.Handle(file.Fd()),
		0,
		1,
		0,
		&overlapped,
	)
}

func validateSingleLink(file *os.File) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil || info.NumberOfLinks != 1 {
		return errMigrationUnsafe
	}
	return nil
}

func syncDirectoryFile(directory *os.File) error {
	if err := windows.FlushFileBuffers(windows.Handle(directory.Fd())); err != nil &&
		!errors.Is(err, windows.ERROR_INVALID_FUNCTION) &&
		!errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return err
	}
	return nil
}

func renameNoReplace(
	sourceDirectory *os.File,
	sourceName string,
	targetDirectory *os.File,
	targetName string,
) error {
	objectName, err := windows.NewNTUnicodeString(sourceName)
	if err != nil {
		return err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: windows.Handle(sourceDirectory.Fd()),
		ObjectName:    objectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	var (
		handle     windows.Handle
		status     windows.IO_STATUS_BLOCK
		allocation int64
	)
	err = windows.NtCreateFile(
		&handle,
		windows.DELETE|windows.SYNCHRONIZE|windows.FILE_READ_ATTRIBUTES,
		attributes,
		&status,
		&allocation,
		0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN,
		windows.FILE_OPEN_REPARSE_POINT|
			windows.FILE_OPEN_FOR_BACKUP_INTENT|
			windows.FILE_SYNCHRONOUS_IO_NONALERT,
		0,
		0,
	)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle) //nolint:errcheck

	name, err := windows.UTF16FromString(targetName)
	if err != nil {
		return err
	}
	nameBytes := (len(name) - 1) * 2
	var layout windowsFileRenameInformation
	bufferSize := int(unsafe.Offsetof(layout.FileName)) + nameBytes
	buffer := make([]byte, bufferSize)
	rename := (*windowsFileRenameInformation)(unsafe.Pointer(&buffer[0]))
	rename.RootDirectory = windows.Handle(targetDirectory.Fd())
	rename.FileNameLength = uint32(nameBytes)
	copy(
		(*[windows.MAX_LONG_PATH]uint16)(unsafe.Pointer(&rename.FileName[0]))[:nameBytes/2:nameBytes/2],
		name,
	)
	return windows.NtSetInformationFile(
		handle,
		&status,
		&buffer[0],
		uint32(bufferSize),
		windows.FileRenameInformation,
	)
}
