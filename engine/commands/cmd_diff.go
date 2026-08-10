package commands

import (
	"context"
	"fmt"
	"strings"
)

const maxDiffLines = 200

type WorkspaceDiffMode string

const (
	WorkspaceDiffStat   WorkspaceDiffMode = "stat"
	WorkspaceDiffFull   WorkspaceDiffMode = "full"
	WorkspaceDiffStaged WorkspaceDiffMode = "staged"
)

type WorkspaceDiffState string

const (
	WorkspaceDiffReady             WorkspaceDiffState = "ready"
	WorkspaceDiffNotGit            WorkspaceDiffState = "not-git"
	WorkspaceDiffRunnerUnavailable WorkspaceDiffState = "runner-unavailable"
	WorkspaceDiffRunnerFailed      WorkspaceDiffState = "runner-failed"
	WorkspaceDiffRunnerTimedOut    WorkspaceDiffState = "runner-timed-out"
)

type WorkspaceDiffSnapshot struct {
	State     WorkspaceDiffState
	Mode      WorkspaceDiffMode
	Numstat   string
	Patch     string
	Untracked string
	Reason    string
	HasHead   bool
}

type WorkspaceDiffProvider interface {
	WorkspaceDiff(context.Context, WorkspaceDiffMode) (WorkspaceDiffSnapshot, error)
}

func executeDiff(ctx *CommandContext, args string) (*CommandResult, error) {
	mode, err := parseWorkspaceDiffMode(args)
	if err != nil {
		return &CommandResult{Output: err.Error()}, nil
	}
	provider, ok := ctx.Engine.(WorkspaceDiffProvider)
	if !ok || provider == nil {
		return &CommandResult{
			Output:       "/diff is unavailable: the workspace command service is not attached.",
			Availability: AvailabilityUnavailable,
		}, nil
	}
	dispatchCtx := ctx.Context
	if dispatchCtx == nil {
		dispatchCtx = context.Background()
	}
	snapshot, err := provider.WorkspaceDiff(dispatchCtx, mode)
	if err != nil {
		return nil, err
	}
	switch snapshot.State {
	case WorkspaceDiffNotGit:
		return &CommandResult{Output: "The active workspace is not a Git repository."}, nil
	case WorkspaceDiffRunnerUnavailable:
		return &CommandResult{
			Output:       "/diff is unavailable: Git command runner is unavailable" + formatDiffReason(snapshot.Reason) + ".",
			Availability: AvailabilityUnavailable,
		}, nil
	case WorkspaceDiffRunnerFailed:
		return &CommandResult{Output: "Git command runner failed" + formatDiffReason(snapshot.Reason) + "."}, nil
	case WorkspaceDiffRunnerTimedOut:
		return &CommandResult{Output: "Git command runner timed out before producing a complete workspace result."}, nil
	case WorkspaceDiffReady:
		return formatWorkspaceDiff(snapshot), nil
	default:
		return &CommandResult{Output: "Git command runner returned an unknown terminal state."}, nil
	}
}

func parseWorkspaceDiffMode(args string) (WorkspaceDiffMode, error) {
	switch strings.ToLower(strings.TrimSpace(args)) {
	case "", "stat":
		return WorkspaceDiffStat, nil
	case "full":
		return WorkspaceDiffFull, nil
	case "staged", "cached":
		return WorkspaceDiffStaged, nil
	default:
		return "", fmt.Errorf("usage: /diff [full|staged|stat]")
	}
}

func formatWorkspaceDiff(snapshot WorkspaceDiffSnapshot) *CommandResult {
	if snapshot.Mode == WorkspaceDiffFull {
		if strings.TrimSpace(snapshot.Patch) == "" {
			return &CommandResult{Output: "No unstaged or staged patch is available."}
		}
		lines := strings.Split(snapshot.Patch, "\n")
		var sb strings.Builder
		if snapshot.HasHead {
			sb.WriteString("Full diff (git diff HEAD):\n\n")
		} else {
			sb.WriteString("Full staged diff before the initial commit:\n\n")
		}
		if len(lines) > maxDiffLines {
			sb.WriteString(strings.Join(lines[:maxDiffLines], "\n"))
			fmt.Fprintf(&sb, "\n\n... truncated (%d more lines)", len(lines)-maxDiffLines)
		} else {
			sb.WriteString(snapshot.Patch)
		}
		return &CommandResult{Output: sb.String()}
	}

	title := "Uncommitted changes (git diff HEAD):"
	empty := "No uncommitted changes."
	if !snapshot.HasHead {
		title = "Changes before the initial commit:"
	}
	if snapshot.Mode == WorkspaceDiffStaged {
		title = "Staged changes (git diff --cached):"
		empty = "No staged changes."
	}
	numstat := strings.TrimSpace(snapshot.Numstat)
	untracked := strings.TrimSpace(snapshot.Untracked)
	if numstat == "" && untracked == "" {
		return &CommandResult{Output: empty}
	}

	var sb strings.Builder
	sb.WriteString(title)
	sb.WriteString("\n\n")
	files, totalAdd, totalDel := formatNumstat(&sb, numstat)
	if untracked != "" {
		if files > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("Untracked files:\n")
		for _, file := range strings.Split(untracked, "\n") {
			if strings.TrimSpace(file) != "" {
				fmt.Fprintf(&sb, "  %s\n", file)
				files++
			}
		}
	}
	fmt.Fprintf(&sb, "\n%d file(s), +%d -%d", files, totalAdd, totalDel)
	return &CommandResult{Output: sb.String()}
}

func formatNumstat(sb *strings.Builder, numstat string) (files, totalAdd, totalDel int) {
	if numstat == "" {
		return 0, 0, 0
	}
	for _, line := range strings.Split(numstat, "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		added, deleted := parts[0], parts[1]
		if added == "-" {
			added = "bin"
		}
		if deleted == "-" {
			deleted = "bin"
		}
		fmt.Fprintf(sb, "  %-50s +%-6s -%s\n", parts[2], added, deleted)
		files++
		if value := parseInt(added); value >= 0 {
			totalAdd += value
		}
		if value := parseInt(deleted); value >= 0 {
			totalDel += value
		}
	}
	return files, totalAdd, totalDel
}

func formatDiffReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ""
	}
	return ": " + reason
}

func parseInt(s string) int {
	n := 0
	for _, char := range s {
		if char < '0' || char > '9' {
			return -1
		}
		n = n*10 + int(char-'0')
	}
	return n
}
