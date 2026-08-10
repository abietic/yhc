package commands

import (
	"fmt"
	"path/filepath"
	"strings"

	promptctx "github.com/abietic/yhc/engine/context"
)

func executeMemory(ctx *CommandContext, args string) (*CommandResult, error) {
	fields := strings.Fields(strings.TrimSpace(args))
	if len(fields) == 0 || (len(fields) == 1 && (fields[0] == "status" || fields[0] == "browse")) {
		return memoryStatus(ctx.CWD)
	}
	if len(fields) != 2 || fields[1] != "project" {
		return &CommandResult{Output: memoryUsage()}, nil
	}

	operation := strings.ToLower(fields[0])
	if operation != "edit" && operation != "delete" && operation != "migrate" {
		return &CommandResult{Output: memoryUsage()}, nil
	}
	return &CommandResult{
		Output: memoryMutationPrompt(ctx.CWD, operation),
		Action: ActionPrompt,
	}, nil
}

func memoryStatus(cwd string) (*CommandResult, error) {
	discovery := promptctx.NewInstructionDiscovery(nil)
	files, err := discovery.Discover(cwd)
	if err != nil {
		return nil, fmt.Errorf("discover instruction memory: %w", err)
	}
	if len(files) == 0 {
		return &CommandResult{Output: "No instruction memory files are active. Use /init to create a project AGENTS.md through the ordinary Agent permission flow."}, nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Active instruction memory (%d):\n", len(files))
	for _, file := range files {
		displayPath := file.Path
		if rel, relErr := filepath.Rel(cwd, file.Path); relErr == nil &&
			(rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))) {
			displayPath = "." + string(filepath.Separator) + rel
		}
		lines := strings.Count(file.Content, "\n")
		if file.Content != "" {
			lines++
		}
		fmt.Fprintf(&sb, "  [%s] %s (%d lines, scope %s)\n", file.Source, displayPath, lines, file.ScopeRoot)
	}
	sb.WriteString("\nMutation is explicit and project-scoped: /memory edit project, /memory delete project, or /memory migrate project.")
	return &CommandResult{Output: sb.String()}, nil
}

func memoryMutationPrompt(cwd, operation string) string {
	agentsPath := filepath.Join(cwd, "AGENTS.md")
	claudePath := filepath.Join(cwd, "CLAUDE.md")
	switch operation {
	case "edit":
		return fmt.Sprintf(`Inspect and update the project instruction memory at %s. Use ordinary Read, Write, and Edit tools so the active permission policy governs every filesystem operation. Preserve accurate user-authored constraints and make only requested or evidence-backed changes.`, agentsPath)
	case "delete":
		return fmt.Sprintf(`Delete only the project-scoped instruction file %s. First read it, report exactly what will be removed, and use the ordinary Bash tool for deletion so the active permission policy and confirmation flow apply. Do not delete user-level, parent, CLAUDE.md, or rules files.`, agentsPath)
	case "migrate":
		return fmt.Sprintf(`Migrate project instructions from %s to the project-native %s. Use ordinary Read, Write, and Edit tools under the active permission policy. Preserve accurate instructions, remove Claude-specific wording, do not delete the source automatically, and report any semantic conflicts that need user choice.`, claudePath, agentsPath)
	default:
		return ""
	}
}

func memoryUsage() string {
	return "Usage: /memory [status|browse|edit project|delete project|migrate project]\nMutations are intentionally limited to the explicit project scope and run through ordinary Agent tools and permissions."
}
