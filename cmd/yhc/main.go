package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/abietic/yhc/cmd/yhc/cmd"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	err := cmd.ExecuteContext(ctx)
	stop()
	if err != nil {
		if !cmd.IsSilentError(err) {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		os.Exit(cmd.ExitCode(err))
	}
}
