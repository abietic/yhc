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
	"unicode/utf8"

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
	owner     string
	mu        sync.Mutex
	status    string
	outcome   string
	output    string
	done      chan struct{}
}

func (entry *backgroundShellEntry) finish(command string, result *ShellResult, err error) {
	status, outcome, output := "failed", "[execution failed]\n", ""
	if result != nil {
		status, outcome = shellResultOutcome(result)
		output = formatShellResult(command, result)
	}
	if err != nil {
		status = "failed"
		if result != nil {
			outcome += "[execution failed]\n"
		}
		output += "[execution error: " + truncateShellText(err.Error(), bashMaxCommandPreview, "error") + "]\n"
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	entry.status, entry.outcome, entry.output = status, outcome, output
	if entry.done != nil {
		close(entry.done)
	}
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
	var filter *regexp.Regexp
	if params.Filter != "" {
		var err error
		filter, err = regexp.Compile(params.Filter)
		if err != nil {
			return "", fmt.Errorf("bash_output: invalid filter: %w", err)
		}
	}

	value, ok := backgroundShells.Load(params.BashID)
	if !ok || manager == nil || manager.backgroundOwner == "" || value.(*backgroundShellEntry).owner != manager.backgroundOwner {
		return fmt.Sprintf("No background shell found with ID %q", params.BashID), nil
	}
	entry := value.(*backgroundShellEntry)
	entry.mu.Lock()
	status, outcome, output := entry.status, entry.outcome, entry.output
	entry.output = ""
	entry.mu.Unlock()
	if filter != nil && output != "" {
		var kept []string
		for _, line := range strings.Split(output, "\n") {
			if filter.MatchString(line) {
				kept = append(kept, line)
			}
		}
		output = strings.Join(kept, "\n")
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Background shell: %s\n", params.BashID)
	fmt.Fprintf(&sb, "Status: %s\n", status)
	// Terminal facts survive output consumption and user-supplied filters.
	sb.WriteString(outcome)
	if output != "" {
		fmt.Fprintf(&sb, "Output:\n%s\n", output)
	} else if status == "running" {
		sb.WriteString("(command still running; completed output is not available yet)\n")
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

	if value, ok := backgroundShells.Load(params.ShellID); ok && value.(*backgroundShellEntry).owner == manager.backgroundOwner {
		backgroundShells.CompareAndDelete(params.ShellID, value)
	}
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
	entry := &backgroundShellEntry{
		ShellID:   shellID,
		Command:   truncateShellText(command, bashMaxCommandPreview, "command"),
		StartedAt: time.Now(),
		owner:     mgr.backgroundOwner,
		status:    "running",
		done:      make(chan struct{}),
	}
	backgroundShells.Store(shellID, entry)

	// Fire and forget — execute in background goroutine.
	go func() {
		backgroundCtx := context.WithoutCancel(ctx)
		result, err := mgr.ExecuteAt(backgroundCtx, shellID, cwd, command, 10*time.Minute)
		entry.finish(command, result, err)
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
	bashMaxCommandPreview   = 512    // the tool-call input already retains the full command
	bashMaxOutputPerStream  = 10000  // byte budget per stdout/stderr preview, including notice
	bashMaxOutputDefault    = 30000  // combined byte budget, including notices
	bashMaxOutputUpperLimit = 150000 // absolute ceiling
)

// formatShellResult formats the output of a shell execution.
func formatShellResult(command string, result *ShellResult) string {
	var sb strings.Builder
	sb.WriteString("$ ")
	sb.WriteString(truncateShellText(command, bashMaxCommandPreview, "command"))
	sb.WriteByte('\n')

	// Keep both initial diagnostics and final assertions/test summaries.
	stdout := truncateShellText(result.Stdout, bashMaxOutputPerStream, "stdout")
	stderr := truncateShellText(result.Stderr, bashMaxOutputPerStream, "stderr")

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

	_, outcome := shellResultOutcome(result)
	sb.WriteString(outcome)

	// Sed-edit detection: warn when `sed -i` detected, suggest Edit tool.
	// Mirrors TS parseSedEditCommand() warning.
	if isSedInPlace(command) {
		sb.WriteString("\n[Warning: sed -i detected. Consider using the Edit tool for file modifications instead.]\n")
	}

	// Keep terminal markers even if a future section reaches the safety limit.
	return truncateShellText(sb.String(), bashMaxOutputDefault, "output")
}

func shellResultOutcome(result *ShellResult) (status, marker string) {
	if result.TimedOut {
		return "timed_out", "[timeout]\n"
	}
	if result.Canceled {
		return "canceled", "[canceled]\n"
	}
	if result.ExitCode != 0 {
		return "failed", fmt.Sprintf("[exit code: %d]\n", result.ExitCode)
	}
	return "completed", ""
}

// truncateShellText bounds a preview without splitting valid UTF-8 input.
// All callers use fixed budgets large enough for the truncation notice.
func truncateShellText(value string, maxBytes int, label string) string {
	if len(value) <= maxBytes {
		return value
	}
	notice := "\n[" + label + " truncated; showing head and tail]\n"
	budget := maxBytes - len(notice)
	headEnd := budget / 2
	tailStart := len(value) - (budget - headEnd)
	for headEnd > 0 && !utf8.RuneStart(value[headEnd]) {
		headEnd--
	}
	for tailStart < len(value) && !utf8.RuneStart(value[tailStart]) {
		tailStart++
	}
	return value[:headEnd] + notice + value[tailStart:]
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
