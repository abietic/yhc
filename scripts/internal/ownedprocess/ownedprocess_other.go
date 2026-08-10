//go:build !darwin && !linux && !windows

package ownedprocess

import (
	"errors"
	"os/exec"
)

func configure(*exec.Cmd) error {
	return errors.New("owned process trees are unsupported on this platform")
}

func attach(*exec.Cmd) (tree, error) {
	return nil, errors.New("owned process trees are unsupported on this platform")
}
