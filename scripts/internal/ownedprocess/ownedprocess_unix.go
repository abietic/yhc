//go:build darwin || linux

package ownedprocess

import (
	"errors"
	"os/exec"
	"syscall"
)

type unixTree struct{ processGroupID int }

func configure(command *exec.Cmd) error {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}

func attach(command *exec.Cmd) (tree, error) {
	if command.Process == nil {
		return nil, errors.New("started process unavailable")
	}
	return unixTree{processGroupID: command.Process.Pid}, nil
}

func (tree unixTree) terminate() error { return signalGroup(tree.processGroupID, syscall.SIGTERM) }
func (tree unixTree) kill() error      { return signalGroup(tree.processGroupID, syscall.SIGKILL) }

func (tree unixTree) close() error {
	return signalGroup(tree.processGroupID, syscall.SIGKILL)
}

func signalGroup(processGroupID int, signal syscall.Signal) error {
	err := syscall.Kill(-processGroupID, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
