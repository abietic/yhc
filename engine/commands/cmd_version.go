package commands

import "github.com/abietic/yhc/internal/buildinfo"

// executeVersion shows detailed version information including build metadata.
// Mirrors reference commands/version/index.ts.
func executeVersion(ctx *CommandContext, args string) (*CommandResult, error) {
	return &CommandResult{Output: buildinfo.DetailedText(buildinfo.Current())}, nil
}
