//go:build !darwin && !linux

package filehistory

import "os"

// Storage remains readable on other platforms; filesystem restore is disabled.
func lockRestoreDirectory(*os.File) (func(), error) { return func() {}, nil }

func restoreFileSingleLink(info os.FileInfo) bool { return info != nil && info.Mode().IsRegular() }
