//go:build !darwin && !linux

package containment

import (
	"context"
	"runtime"
)

func runtimeGOOS() string                               { return runtime.GOOS }
func runtimeGOARCH() string                             { return runtime.GOARCH }
func CaptureRootIdentity(string) (RootIdentity, error)  { return RootIdentity{}, errSeatbeltUnsupported }
func verifySeatbeltExecutable() error                   { return errSeatbeltUnsupported }
func runSeatbeltSpawn(context.Context, SpawnSpec) error { return errSeatbeltUnsupported }
