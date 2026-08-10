//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
)

// setProcessGroup sets the Setpgid attribute on Unix so the shell and its
// children form a process group that can be killed in one syscall.Kill call.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup sends SIGKILL to the entire process group.  The negative
// PID signals "process group" on Unix.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
