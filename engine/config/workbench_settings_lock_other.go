//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package config

import "os"

func tryLockWorkbenchSettings(*os.File) (bool, error) {
	return false, ErrSettingsUnsafePath
}

func unlockWorkbenchSettings(*os.File) error { return nil }
