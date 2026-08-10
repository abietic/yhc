//go:build !darwin && !linux && !windows

package mcp

import (
	"errors"
	"os/exec"
)

type stdioProcessTree interface {
	terminate() error
	kill() error
	close() error
}

func stdioPlatformSupported() error {
	return errors.New("mcp: stdio process trees are unsupported on this platform")
}

func configureStdioProcess(*exec.Cmd) error {
	return errors.New("mcp: stdio process trees are unsupported on this platform")
}

func attachStdioProcessTree(*exec.Cmd) (stdioProcessTree, error) {
	return nil, errors.New("mcp: stdio process trees are unsupported on this platform")
}
