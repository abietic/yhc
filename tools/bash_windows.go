//go:build windows

package tools

import "os/exec"

// setProcessGroup is a no-op on Windows; bash is not available natively, and
// the shell manager will return an error when trying to start a shell.
func setProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup falls back to os.Process.Kill on Windows (no process groups).
func killProcessGroup(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
