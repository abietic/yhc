//go:build !darwin && !linux

package tools

import "os"

func readGuardSingleOwner(_ *os.File) bool { return false }

func openReadGuardFile(_ *os.Root, _ string) (*os.File, error) { return nil, ErrReadGuardRecovery }
