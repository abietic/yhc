//go:build !darwin && !linux && !windows

package filehistory

import (
	"fmt"
	"os"
)

func durableRootIdentity(*os.Root) (string, error) {
	return "", fmt.Errorf("workspace identity is unsupported on this platform")
}
