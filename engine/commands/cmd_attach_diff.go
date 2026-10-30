package commands

import (
	"context"
	"strings"
)

type DiffContextSource string

const (
	DiffContextAll      DiffContextSource = "all"
	DiffContextStaged   DiffContextSource = "staged"
	DiffContextUnstaged DiffContextSource = "unstaged"
	ActionAttachDiff    CommandAction     = "attach_diff"
)

type DiffContextSnapshot struct {
	Source  DiffContextSource
	State   WorkspaceDiffState
	Patch   string
	Reason  string
	HasHead bool
	CWD     string
}

func executeAttachDiff(_ context.Context, ctx *CommandContext) (*CommandResult, error) {
	source := DiffContextSource(strings.TrimSpace(strings.Join(ctx.Args, " ")))
	if source == "" {
		source = DiffContextAll
	}
	switch source {
	case DiffContextAll, DiffContextStaged, DiffContextUnstaged:
		return &CommandResult{Action: ActionAttachDiff, Data: map[string]any{"source": source}}, nil
	default:
		return &CommandResult{Output: "Usage: /attach-diff [all|staged|unstaged]"}, nil
	}
}
