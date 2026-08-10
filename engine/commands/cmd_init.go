package commands

import (
	"fmt"
	"path/filepath"
	"strings"
)

func executeInit(ctx *CommandContext, args string) (*CommandResult, error) {
	force := false
	switch strings.TrimSpace(args) {
	case "":
	case "--force":
		force = true
	default:
		return &CommandResult{Output: "Usage: /init [--force]"}, nil
	}

	agentsPath := filepath.Join(ctx.CWD, "AGENTS.md")
	return &CommandResult{
		Output: initProjectInstructionsPrompt(ctx.CWD, agentsPath, force),
		Action: ActionPrompt,
	}, nil
}

func initProjectInstructionsPrompt(cwd, agentsPath string, force bool) string {
	operation := "create or update"
	if force {
		operation = "rebuild"
	}
	return fmt.Sprintf(`Analyze the project rooted at %s and %s %s as the project-native instruction file.

Use only the ordinary Read, Glob, Grep, Write, and Edit tools, so every filesystem operation follows the active permission policy. Do not create CLAUDE.md or a .claude directory.

Verify the project before editing:
1. Read existing AGENTS.md when present and preserve accurate user-owned instructions.
2. Inspect authoritative build, test, lint, format, entrypoint, and architecture sources.
3. Record only commands and conventions supported by those sources; do not guess.
4. Keep the file concise and scoped to future coding-agent work.
5. If the existing file is already accurate and this is not a forced rebuild, report that no change is needed.

Target: %s`, cwd, operation, agentsPath, agentsPath)
}
