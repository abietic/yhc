//go:build !darwin && !linux && !windows

package main

import (
	"errors"
	"os"
)

func openRegularNoFollow(string) (*os.File, error) {
	return nil, errors.New("sha256 capture is not supported safely on this platform")
}
