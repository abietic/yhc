package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/abietic/yhc/engine/containment"
	"github.com/cloudwego/eino/schema"
)

// defaultShellManager is the package-level persistent shell manager.
// It is lazily initialized on first Bash tool invocation.
var (
	defaultShellManager     *ShellManager
	defaultShellManagerOnce sync.Once
)

func getDefaultShellManager() *ShellManager {
	defaultShellManagerOnce.Do(func() {
		defaultShellManager = NewShellManager()
	})
	return defaultShellManager
}

// backgroundShells tracks shells running in background mode.
var backgroundShells sync.Map // shellID -> *backgroundShellEntry

type backgroundShellEntry struct {
	ShellID   string
	Command   string
	StartedAt time.Time
}

func BashTool() ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: "Bash",
			Desc: "Executes a shell command in a persistent shell session.",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"command":           {Type: schema.String, Desc: "Command to execute", Required: true},
				"timeout":           {Type: schema.Integer, Desc: "Max execution time in milliseconds (default 120000, max 600000)"},
				"description":       {Type: schema.String, Desc: "Short description of what the command does"},
				"run_in_background": {Type: schema.Boolean, Desc: "Run in background, returns shell ID for later monitoring"},
			}),
		},
		SkipResultBudget: true,
		ExecuteCtx: func(ctx context.Context, input string) (string, error) {
			var params struct {
				Command         string `json:"command"`
				Timeout         *int   `json:"timeout"`
				Description     string `json:"description"`
				RunInBackground *bool  `json:"run_in_background"`
			}
			if err := json.Unmarshal([]byte(input), &params); err != nil {
				return "", fmt.Errorf("bash: invalid params: %w", err)
			}
			if params.Command == "" {
				return "", fmt.Errorf("bash: command is required")
			}

			timeoutMs := 120000
			if params.Timeout != nil && *params.Timeout > 0 {
				timeoutMs = *params.Timeout
				if timeoutMs > 600000 {
					timeoutMs = 600000
				}
			}

			mgr := ShellManagerFromCtx(ctx)
			scopedManager := mgr != nil
			if !scopedManager {
				mgr = getDefaultShellManager()
			}
			cwd := ""
			if ExecutionCWDFromCtx(ctx) != "" {
				var err error
				cwd, err = effectiveExecutionCWD(ctx)
				if err != nil {
					return "", fmt.Errorf("bash: %w", err)
				}
			}

			// Background execution mode.
			if params.RunInBackground != nil && *params.RunInBackground {
				return runBackgroundCommand(ctx, mgr, cwd, params.Command, params.Description)
			}

			// Foreground execution in the default persistent shell.
			timeout := time.Duration(timeoutMs) * time.Millisecond

			// Emit progress event when execution starts.
			EmitProgress(ctx, ToolProgressEvent{
				ToolName: "Bash",
				Content:  fmt.Sprintf("$ %s", params.Command),
			})

			shellID := ""
			if scopedManager || hasRuntimeShellIdentity(ctx) || cwd != "" {
				shellID = foregroundShellID(ctx, cwd)
			}
			result, err := mgr.ExecuteAt(ctx, shellID, cwd, params.Command, timeout)
			if err != nil {
				return "", fmt.Errorf("bash: shell execution failed: %w", err)
			}

			// Emit final progress event with completion.
			EmitProgress(ctx, ToolProgressEvent{
				ToolName: "Bash",
				IsFinal:  true,
				Content:  fmt.Sprintf("exit code: %d", result.ExitCode),
			})

			// Track git operations for session metadata
			if result.ExitCode == 0 && DefaultGitTracker != nil {
				trackGitOperation(params.Command, result.Stdout, DefaultGitTracker)
			}

			return formatShellResult(params.Command, result), nil
		},
	}
}

func hasRuntimeShellIdentity(ctx context.Context) bool {
	return SessionIDFromCtx(ctx) != "" ||
		ThreadIDFromCtx(ctx) != "" ||
		AgentIDFromCtx(ctx) != ""
}

func foregroundShellID(ctx context.Context, cwd string) string {
	identity := strings.Join([]string{
		SessionIDFromCtx(ctx),
		ThreadIDFromCtx(ctx),
		AgentIDFromCtx(ctx),
		cwd,
	}, "\x00")
	sum := sha256.Sum256([]byte(identity))
	return fmt.Sprintf("runtime-%x", sum[:12])
}

// trackGitOperation detects git commands and records them.
func trackGitOperation(command, stdout string, tracker *GitOperationTracker) {
	cmd := strings.TrimSpace(command)
	if !strings.HasPrefix(cmd, "git ") {
		return
	}
	parts := strings.Fields(cmd)
	if len(parts) < 2 {
		return
	}
	subCmd := parts[1]
	switch subCmd {
	case "commit":
		commitID := extractCommitID(stdout)
		tracker.Record(GitOperation{Type: GitOpCommit, CommitID: commitID})
	case "push":
		tracker.Record(GitOperation{Type: GitOpPush})
	case "checkout":
		branch := ""
		if len(parts) >= 3 {
			branch = parts[2]
		}
		tracker.Record(GitOperation{Type: GitOpCheckout, Branch: branch})
	case "rebase":
		tracker.Record(GitOperation{Type: GitOpRebase})
	case "merge":
		tracker.Record(GitOperation{Type: GitOpMerge})
	case "stash":
		tracker.Record(GitOperation{Type: GitOpStash})
	case "reset":
		tracker.Record(GitOperation{Type: GitOpReset})
	}
}

func extractCommitID(stdout string) string {
	// git commit output: "[branch abc1234] commit message"
	re := regexp.MustCompile(`\[[\w/.-]+ ([0-9a-f]{7,40})\]`)
	m := re.FindStringSubmatch(stdout)
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

// DefaultGitTracker is the global git operation tracker.
var DefaultGitTracker *GitOperationTracker

// BashOutputTool returns a tool that reads output from a background shell.
func BashOutputTool() ToolImpl {
	impl := ToolImpl{
		Info: &schema.ToolInfo{
			Name: "BashOutput",
			Desc: "Retrieves output from a running or completed background bash shell.",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"bash_id": {Type: schema.String, Desc: "The ID of the background shell", Required: true},
				"filter":  {Type: schema.String, Desc: "Optional regex to filter output lines"},
			}),
		},
		Execute: func(input string) (string, error) {
			return executeBashOutput(getDefaultShellManager(), input)
		},
	}
	impl.ExecuteCtx = func(ctx context.Context, input string) (string, error) {
		manager := ShellManagerFromCtx(ctx)
		if manager == nil {
			manager = getDefaultShellManager()
		}
		return executeBashOutput(manager, input)
	}
	return impl
}

func executeBashOutput(manager *ShellManager, input string) (string, error) {
	var params struct {
		BashID string `json:"bash_id"`
		Filter string `json:"filter"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("bash_output: invalid params: %w", err)
	}
	if params.BashID == "" {
		return "", fmt.Errorf("bash_output: bash_id is required")
	}

	if _, ok := backgroundShells.Load(params.BashID); !ok {
		return fmt.Sprintf("No background shell found with ID %q", params.BashID), nil
	}

	stderr := ""
	manager.mu.RLock()
	shell := manager.shells[params.BashID]
	manager.mu.RUnlock()
	if shell != nil {
		stderr, _ = shell.getStderrBuffer()
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Background shell: %s\n", params.BashID)
	if stderr != "" {
		fmt.Fprintf(&sb, "Output:\n%s\n", stderr)
	} else {
		sb.WriteString("(no new output captured)\n")
	}
	return sb.String(), nil
}

// KillShellTool returns a tool that terminates a background shell.
func KillShellTool() ToolImpl {
	impl := ToolImpl{
		Info: &schema.ToolInfo{
			Name: "KillShell",
			Desc: "Kills a running background bash shell by its ID.",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"shell_id": {Type: schema.String, Desc: "The ID of the background shell to kill", Required: true},
			}),
		},
		Execute: func(input string) (string, error) {
			return executeKillShell(getDefaultShellManager(), input)
		},
	}
	impl.ExecuteCtx = func(ctx context.Context, input string) (string, error) {
		manager := ShellManagerFromCtx(ctx)
		if manager == nil {
			manager = getDefaultShellManager()
		}
		return executeKillShell(manager, input)
	}
	return impl
}

func executeKillShell(manager *ShellManager, input string) (string, error) {
	var params struct {
		ShellID string `json:"shell_id"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("kill_shell: invalid params: %w", err)
	}
	if params.ShellID == "" {
		return "", fmt.Errorf("kill_shell: shell_id is required")
	}

	err := manager.Kill(params.ShellID)
	if err != nil {
		return fmt.Sprintf("Failed to kill shell %q: %s", params.ShellID, err.Error()), nil
	}

	backgroundShells.Delete(params.ShellID)
	return fmt.Sprintf("Shell %q terminated successfully.", params.ShellID), nil
}

// runBackgroundCommand starts a command in a dedicated background shell.
func runBackgroundCommand(ctx context.Context, mgr *ShellManager, cwd, command, description string) (string, error) {
	// Create a unique shell ID for the background task.
	shellID := fmt.Sprintf("bg-%d", time.Now().UnixNano())

	// Start a new dedicated shell for this background command.
	requested, _ := containment.FromContext(ctx)
	_, err := mgr.getOrCreateAt(shellID, cwd, requested)
	if err != nil {
		return "", fmt.Errorf("bash: failed to create background shell: %w", err)
	}

	// Track it.
	backgroundShells.Store(shellID, &backgroundShellEntry{
		ShellID:   shellID,
		Command:   command,
		StartedAt: time.Now(),
	})

	// Fire and forget — execute in background goroutine.
	go func() {
		backgroundCtx := context.WithoutCancel(ctx)
		_, _ = mgr.ExecuteAt(backgroundCtx, shellID, cwd, command, 10*time.Minute)
	}()

	var sb strings.Builder
	sb.WriteString("Command started in background.\n")
	fmt.Fprintf(&sb, "Shell ID: %s\n", shellID)
	if description != "" {
		fmt.Fprintf(&sb, "Description: %s\n", description)
	}
	sb.WriteString("Use BashOutput with this ID to check output later.")
	return sb.String(), nil
}

const (
	bashMaxOutputPerStream  = 10000  // max chars per stdout/stderr stream; mirrors TS BashTool
	bashMaxOutputDefault    = 30000  // combined fallback (used for overall truncation)
	bashMaxOutputUpperLimit = 150000 // absolute ceiling
)

// formatShellResult formats the output of a shell execution.
func formatShellResult(command string, result *ShellResult) string {
	var sb strings.Builder
	sb.WriteString("$ ")
	sb.WriteString(command)
	sb.WriteByte('\n')

	// Truncate stdout and stderr separately (10K each, mirrors TS BashTool).
	stdout := result.Stdout
	if len(stdout) > bashMaxOutputPerStream {
		stdout = stdout[:bashMaxOutputPerStream] + "\n[stdout truncated]"
	}

	stderr := result.Stderr
	if len(stderr) > bashMaxOutputPerStream {
		stderr = stderr[:bashMaxOutputPerStream] + "\n[stderr truncated]"
	}

	if stdout != "" {
		sb.WriteString(stdout)
		if !strings.HasSuffix(stdout, "\n") {
			sb.WriteByte('\n')
		}
	}

	if stderr != "" {
		sb.WriteString(stderr)
		if !strings.HasSuffix(stderr, "\n") {
			sb.WriteByte('\n')
		}
	}

	if result.TimedOut {
		sb.WriteString("[timeout]\n")
	} else if result.Canceled {
		sb.WriteString("[canceled]\n")
	} else if result.ExitCode != 0 {
		fmt.Fprintf(&sb, "[exit code: %d]\n", result.ExitCode)
	}

	// Sed-edit detection: warn when `sed -i` detected, suggest Edit tool.
	// Mirrors TS parseSedEditCommand() warning.
	if isSedInPlace(command) {
		sb.WriteString("\n[Warning: sed -i detected. Consider using the Edit tool for file modifications instead.]\n")
	}

	output := sb.String()

	// Final combined truncation (safety net).
	if len(output) > bashMaxOutputDefault {
		output = output[:bashMaxOutputDefault] + "\n\n[output truncated]"
	}

	return output
}

// isSedInPlace detects `sed -i` commands that modify files in-place.
// Mirrors TS parseSedEditCommand() detection logic.
func isSedInPlace(command string) bool {
	parts := strings.Fields(command)
	for i, part := range parts {
		if part == "sed" {
			// Look for -i flag after sed
			for j := i + 1; j < len(parts); j++ {
				if parts[j] == "-i" || strings.HasPrefix(parts[j], "-i") {
					return true
				}
				// Stop at the sed expression (first non-flag argument)
				if !strings.HasPrefix(parts[j], "-") {
					break
				}
			}
		}
	}
	return false
}
