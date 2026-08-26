//go:build !linux

package containment

import (
	"context"
	"os"
)

func verifyBubblewrapExecutable() error { return errBubblewrapUnsupported }

func newSocketDenySeccompFile() (*os.File, error) { return nil, errBubblewrapUnsupported }

func runBubblewrapSpawn(context.Context, SpawnSpec) error { return errBubblewrapUnsupported }
