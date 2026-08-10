//go:build windows

package tasks

import "os/exec"

// setProcessGroup is a no-op on Windows.
func setProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup falls back to os.Process.Kill on Windows.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
