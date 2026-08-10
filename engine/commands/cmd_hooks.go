package commands

import (
	"fmt"
	"strings"

	"github.com/abietic/yhc/engine/hooks"
)

func executeHooks(ctx *CommandContext, args string) (*CommandResult, error) {
	if strings.TrimSpace(args) != "" {
		return &CommandResult{
			Output: "Hook inspection is read-only. Edit the owning configuration and restart the runtime to change hooks.",
		}, nil
	}
	snapshot, ok := runtimeInspectionSnapshot(ctx)
	if !ok {
		return &CommandResult{
			Output: "Hook inspection is unavailable for this runtime.",
		}, nil
	}
	cfg := snapshot.Hooks

	if cfg == nil || (len(cfg.PreToolHooks) == 0 && len(cfg.PostToolHooks) == 0 && len(cfg.UserPromptHooks) == 0) {
		source := "runtime hook executor"
		if cfg != nil && cfg.Source != "" {
			source = cfg.Source
		}
		return &CommandResult{
			Output: fmt.Sprintf(
				"No active shell hooks.\n\nSource: %s\nHealth: healthy",
				source,
			),
		}, nil
	}

	var sb strings.Builder
	sb.WriteString("Hook Configurations\n")
	sb.WriteString("===================\n\n")
	fmt.Fprintf(&sb, "Source: %s\n", firstNonEmpty(cfg.Source, "runtime hook executor"))
	sb.WriteString("Health: healthy\n\n")

	writeHookSection(&sb, "PreToolUse", cfg.PreToolHooks)
	writeHookSection(&sb, "PostToolUse", cfg.PostToolHooks)
	writeHookSection(&sb, "UserPromptSubmit", cfg.UserPromptHooks)

	total := len(cfg.PreToolHooks) + len(cfg.PostToolHooks) + len(cfg.UserPromptHooks)
	fmt.Fprintf(&sb, "Total: %d hook(s)\n", total)
	sb.WriteString("\nHooks are read-only runtime inspection.")

	return &CommandResult{Output: sb.String()}, nil
}

func writeHookSection(sb *strings.Builder, event string, shellHooks []hooks.ShellHook) {
	if len(shellHooks) == 0 {
		return
	}

	fmt.Fprintf(sb, "%s (%d):\n", event, len(shellHooks))
	for _, h := range shellHooks {
		matcher := h.ToolPattern
		if matcher == "" {
			matcher = "*"
		}
		fmt.Fprintf(sb, "  [%s] %s", matcher, h.Command)

		var flags []string
		if h.Async {
			flags = append(flags, "async")
		}
		if h.Timeout > 0 {
			flags = append(flags, fmt.Sprintf("timeout=%s", h.Timeout))
		}
		if h.StatusMessage != "" {
			flags = append(flags, fmt.Sprintf("status=%q", h.StatusMessage))
		}
		if len(flags) > 0 {
			fmt.Fprintf(sb, "  (%s)", strings.Join(flags, ", "))
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
}
