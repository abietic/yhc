//go:build windows

package hooks

import (
	"os"
	"os/exec"
	"strconv"
	"time"
)

func prepareShellProcess(_ *exec.Cmd) {}

func terminateShellProcessTree(cmd *exec.Cmd, wait <-chan error, grace time.Duration) bool {
	if cmd == nil || cmd.Process == nil {
		<-wait
		return false
	}

	_ = cmd.Process.Signal(os.Interrupt)
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-wait:
		return false
	case <-timer.C:
		_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
		_ = cmd.Process.Kill()
		<-wait
		return true
	}
}
