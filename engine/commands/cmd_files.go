package commands

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/abietic/yhc/engine/permission"
	"github.com/cloudwego/eino/schema"
)

// executeFiles lists all files referenced in the conversation via tool calls.
// Mirrors reference commands/files/index.ts.
func executeFiles(ctx *CommandContext, args string) (*CommandResult, error) {
	if len(ctx.Messages) == 0 {
		return &CommandResult{Output: "No files in context."}, nil
	}

	files, outsideRoots := canonicalContextPaths(ctx, extractFilePaths(ctx.Messages))
	if len(files) == 0 {
		output := "No workspace files are referenced in this conversation."
		if outsideRoots > 0 {
			output += fmt.Sprintf(" %d path(s) outside the active workspace roots were omitted.", outsideRoots)
		}
		return &CommandResult{Output: output}, nil
	}

	sort.Strings(files)

	var sb strings.Builder
	fmt.Fprintf(&sb, "Files in context (%d):\n", len(files))
	for _, f := range files {
		fmt.Fprintf(&sb, "  %s\n", f)
	}
	if outsideRoots > 0 {
		fmt.Fprintf(&sb, "\n%d path(s) outside the active workspace roots were omitted.", outsideRoots)
	}
	return &CommandResult{Output: sb.String()}, nil
}

func canonicalContextPaths(ctx *CommandContext, paths []string) ([]string, int) {
	if ctx == nil {
		return nil, len(paths)
	}
	roots := append([]string(nil), ctx.WorkingDirectories...)
	if len(roots) == 0 && strings.TrimSpace(ctx.CWD) != "" {
		roots = []string{ctx.CWD}
	}
	seen := make(map[string]struct{})
	omitted := 0
	for _, rawPath := range paths {
		resolution := permission.ResolvePermissionPath(rawPath, ctx.CWD)
		if !permission.PermissionPathsWithinRoots(resolution, roots) {
			omitted++
			continue
		}
		canonical := filepath.Clean(resolution.Effective())
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for path := range seen {
		result = append(result, path)
	}
	sort.Strings(result)
	return result, omitted
}

// extractFilePaths scans tool calls in messages for file path arguments.
func extractFilePaths(messages []*schema.Message) []string {
	seen := make(map[string]bool)

	for _, msg := range messages {
		if msg == nil || msg.Role != schema.Assistant {
			continue
		}
		for _, tc := range msg.ToolCalls {
			paths := extractPathsFromToolCall(tc.Function.Name, tc.Function.Arguments)
			for _, p := range paths {
				if p != "" && !seen[p] {
					seen[p] = true
				}
			}
		}
	}

	result := make([]string, 0, len(seen))
	for p := range seen {
		result = append(result, p)
	}
	return result
}

// extractPathsFromToolCall extracts file paths from a tool call's arguments.
func extractPathsFromToolCall(toolName, argsJSON string) []string {
	if argsJSON == "" {
		return nil
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return nil
	}

	var paths []string

	// Known path fields by tool
	switch toolName {
	case "Read", "read":
		if p, ok := args["file_path"].(string); ok {
			paths = append(paths, p)
		}
	case "Write", "write":
		if p, ok := args["file_path"].(string); ok {
			paths = append(paths, p)
		}
	case "Edit", "edit":
		if p, ok := args["file_path"].(string); ok {
			paths = append(paths, p)
		}
	case "Glob", "glob":
		if p, ok := args["path"].(string); ok {
			paths = append(paths, p)
		}
	case "Grep", "grep":
		if p, ok := args["path"].(string); ok {
			paths = append(paths, p)
		}
	case "LS", "ls":
		if p, ok := args["path"].(string); ok {
			paths = append(paths, p)
		}
	default:
		// Generic: look for common path field names
		for _, key := range []string{"file_path", "path", "filename"} {
			if p, ok := args[key].(string); ok && p != "" {
				paths = append(paths, p)
			}
		}
	}

	return paths
}
