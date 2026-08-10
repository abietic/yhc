//go:build !darwin && !linux && !windows

package tools

import "os"

func configFileHasSingleLink(*os.File) bool {
	return false
}
