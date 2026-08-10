//go:build darwin || linux

package mcp

import (
	"errors"
	"os/exec"
	"syscall"
)

type stdioProcessTree interface {
	terminate() error
	kill() error
	close() error
}

type unixProcessTree struct{ pgid int }

func stdioPlatformSupported() error { return nil }

func configureStdioProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}

func attachStdioProcessTree(cmd *exec.Cmd) (stdioProcessTree, error) {
	if cmd.Process == nil {
		return nil, errors.New("mcp: stdio process ownership unavailable")
	}
	return unixProcessTree{pgid: cmd.Process.Pid}, nil
}

func (p unixProcessTree) terminate() error { return signalProcessGroup(p.pgid, syscall.SIGTERM) }
func (p unixProcessTree) kill() error      { return signalProcessGroup(p.pgid, syscall.SIGKILL) }
func (unixProcessTree) close() error       { return nil }

func signalProcessGroup(pgid int, signal syscall.Signal) error {
	err := syscall.Kill(-pgid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
