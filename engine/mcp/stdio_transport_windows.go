//go:build windows

package mcp

import (
	"errors"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type stdioProcessTree interface {
	terminate() error
	kill() error
	close() error
}

type windowsProcessTree struct{ job windows.Handle }

func stdioPlatformSupported() error { return nil }

func configureStdioProcess(cmd *exec.Cmd) error {
	// Start suspended so assignment to the kill-on-close job happens before the
	// program can create a descendant outside the job tree.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
	return nil
}

func attachStdioProcessTree(cmd *exec.Cmd) (stdioProcessTree, error) {
	if cmd.Process == nil {
		return nil, errors.New("mcp: stdio process ownership unavailable")
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, errors.New("mcp: stdio process ownership unavailable")
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return nil, errors.New("mcp: stdio process ownership unavailable")
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return nil, errors.New("mcp: stdio process ownership unavailable")
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		_ = windows.CloseHandle(job)
		return nil, errors.New("mcp: stdio process ownership unavailable")
	}
	if err := resumePrimaryThread(uint32(cmd.Process.Pid)); err != nil {
		_ = windows.TerminateJobObject(job, 1)
		_ = windows.CloseHandle(job)
		return nil, errors.New("mcp: stdio process ownership unavailable")
	}
	return windowsProcessTree{job: job}, nil
}

func (p windowsProcessTree) terminate() error { return windows.TerminateJobObject(p.job, 1) }
func (p windowsProcessTree) kill() error      { return windows.TerminateJobObject(p.job, 1) }
func (p windowsProcessTree) close() error     { return windows.CloseHandle(p.job) }

func resumePrimaryThread(processID uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)

	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err := windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != processID {
			continue
		}
		thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return err
		}
		defer windows.CloseHandle(thread)
		_, err = windows.ResumeThread(thread)
		return err
	}
	return errors.New("primary thread not found")
}
