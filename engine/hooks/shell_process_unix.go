//go:build !windows

package hooks

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

func prepareShellProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateShellProcessTree(cmd *exec.Cmd, wait <-chan error, grace time.Duration) bool {
	if cmd == nil || cmd.Process == nil {
		<-wait
		return false
	}

	pid := cmd.Process.Pid
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}

	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-wait:
		if shellProcessGroupExists(pid) {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			return true
		}
		return false
	case <-timer.C:
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = cmd.Process.Kill()
		<-wait
		return true
	}
}

func shellProcessGroupExists(pid int) bool {
	err := syscall.Kill(-pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
