package main

import (
	"context"
	"os/exec"

	"github.com/abietic/yhc/scripts/internal/ownedprocess"
)

func runOwnedCommand(ctx context.Context, command *exec.Cmd) error {
	if err := ownedprocess.Run(ctx, command); err != nil {
		return fail(ownedprocess.Code(err), err)
	}
	return nil
}
