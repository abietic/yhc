package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/abietic/yhc/engine/worktree"
	"github.com/cloudwego/eino/schema"
)

// Agent tool name constants mirroring src/tools/AgentTool/constants.ts.
const (
	AgentToolName       = "Agent"
	LegacyAgentToolName = "Task" // backward compat for permission rules, hooks, resumed sessions

	VerificationAgentType = "verification"
)

// OneShotBuiltinAgentTypes lists agent types that run once and return a report.
// The parent never sends messages back to continue them. We skip the agentId/SendMessage
// trailer for these to save tokens.
var OneShotBuiltinAgentTypes = map[string]bool{
	"Explore": true,
	"Plan":    true,
}

// AgentTypeInfo describes an available agent type for dynamic prompt generation.
type AgentTypeInfo struct {
	Name      string
	WhenToUse string
	Tools     []string // nil means all tools
}

// SetAgentTypeDescriptions updates the Agent tool's description in the
// process-wide compatibility registry.
func SetAgentTypeDescriptions(agentTypes []AgentTypeInfo) {
	SetAgentTypeDescriptionsForRegistry(DefaultRegistry, agentTypes)
}

// SetAgentTypeDescriptionsForRegistry updates only the supplied registry.
// Engine-owned registries must use this form so one runtime cannot invalidate
// another runtime's capability generation.
func SetAgentTypeDescriptionsForRegistry(
	registry *Registry,
	agentTypes []AgentTypeInfo,
) {
	if registry == nil {
		return
	}
	impl, ok := registry.Get("Agent")
	if !ok || impl.Info == nil {
		return
	}

	desc := buildAgentDescription(agentTypes)
	info := *impl.Info
	info.Desc = desc
	impl.Info = &info
	registry.Update("Agent", impl)
}

// buildAgentDescription generates the Agent tool description with agent type catalog.
func buildAgentDescription(agentTypes []AgentTypeInfo) string {
	var sb strings.Builder
	sb.WriteString("Launch a new agent to handle complex, multi-step tasks autonomously. ")
	sb.WriteString("The Agent tool launches specialized agents (subprocesses) that autonomously handle complex tasks. ")
	sb.WriteString("Each agent type has specific capabilities and tools available to it.\n\n")
	sb.WriteString("Available agent types and the tools they have access to:\n")

	// Sort for deterministic output.
	sorted := make([]AgentTypeInfo, len(agentTypes))
	copy(sorted, agentTypes)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	for _, at := range sorted {
		toolsStr := "*"
		if len(at.Tools) > 0 {
			toolsStr = strings.Join(at.Tools, ", ")
		}
		fmt.Fprintf(&sb, "- %s: %s\n (Tools: %s)\n", at.Name, at.WhenToUse, toolsStr)
	}

	sb.WriteString("\nWhen using the Agent tool, you must specify a subagent_type parameter to select which agent type to use.")
	return sb.String()
}

func AgentTool() ToolImpl {
	tool := ToolImpl{
		Info: &schema.ToolInfo{
			Name: "Agent",
			Desc: "Launch a new agent to handle complex, multi-step tasks autonomously.",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"description":       {Type: schema.String, Desc: "A short (3-5 word) description of the task", Required: true},
				"prompt":            {Type: schema.String, Desc: "The task for the agent to perform", Required: true},
				"subagent_type":     {Type: schema.String, Desc: "The type of specialized agent to use for this task"},
				"model":             {Type: schema.String, Desc: "Optional model override"},
				"run_in_background": {Type: schema.Boolean, Desc: "Set to true to run this agent in the background. You will be notified when it completes."},
				"name":              {Type: schema.String, Desc: "Name for the spawned agent. Makes it addressable via SendMessage while running."},
				"team_name":         {Type: schema.String, Desc: "Team name for spawning. Uses current team context if omitted."},
				"mode":              {Type: schema.String, Desc: "Permission mode for spawned teammate (e.g., \"plan\" to require plan approval)."},
				"isolation":         {Type: schema.String, Desc: "Isolation mode. \"worktree\" creates a temporary git worktree for the agent."},
				"cwd":               {Type: schema.String, Desc: "Absolute path to run a non-worktree agent in. Mutually exclusive with isolation=\"worktree\"."},
				"worktree_source":   {Type: schema.String, Desc: "Source policy for worktree isolation. Omit to require a clean parent; set \"ignore_dirty\" to use committed HEAD and record omitted files."},
				"work_item": {
					Type: schema.Object,
					Desc: "Optional opaque WorkBoard reference obtained from the current engine Task Explorer. The exact item revision must still be open when this Agent generation is admitted.",
					SubParams: map[string]*schema.ParameterInfo{
						"board_id": {
							Type:     schema.String,
							Desc:     "Opaque WorkBoard identity from the current engine snapshot",
							Required: true,
						},
						"work_item_id": {
							Type:     schema.String,
							Desc:     "Opaque WorkItem identity from the current engine snapshot",
							Required: true,
						},
						"expected_item_revision": {
							Type:     schema.Integer,
							Desc:     "Exact positive WorkItem revision displayed by the current engine snapshot",
							Required: true,
						},
					},
				},
			}),
		},
	}
	tool.Execute = func(input string) (string, error) {
		return executeAgentTool(context.Background(), input)
	}
	tool.ExecuteCtx = executeAgentTool
	return tool
}

func executeAgentTool(ctx context.Context, input string) (string, error) {
	var params struct {
		Description     string `json:"description"`
		Prompt          string `json:"prompt"`
		SubagentType    string `json:"subagent_type"`
		Model           string `json:"model"`
		RunInBackground *bool  `json:"run_in_background"`
		Name            string `json:"name"`
		TeamName        string `json:"team_name"`
		Mode            string `json:"mode"`
		Isolation       string `json:"isolation"`
		CWD             string `json:"cwd"`
		WorktreeSource  string `json:"worktree_source"`
		WorkItem        *struct {
			BoardID              string `json:"board_id"`
			WorkItemID           string `json:"work_item_id"`
			ExpectedItemRevision uint64 `json:"expected_item_revision"`
		} `json:"work_item"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("agent: invalid params: %w", err)
	}
	if strings.TrimSpace(params.Description) == "" || strings.TrimSpace(params.Prompt) == "" {
		return "", fmt.Errorf("agent: description and prompt are required")
	}
	var workItem *AgentWorkItemReference
	if params.WorkItem != nil {
		workItem = &AgentWorkItemReference{
			BoardID:              strings.TrimSpace(params.WorkItem.BoardID),
			WorkItemID:           strings.TrimSpace(params.WorkItem.WorkItemID),
			ExpectedItemRevision: params.WorkItem.ExpectedItemRevision,
		}
		if workItem.BoardID == "" ||
			workItem.WorkItemID == "" ||
			workItem.ExpectedItemRevision == 0 {
			return "", fmt.Errorf(
				"agent: work_item requires board_id, work_item_id, and a positive expected_item_revision",
			)
		}
	}

	worktreeOptions := AgentExecOptions{
		Isolation:          strings.TrimSpace(params.Isolation),
		CWD:                strings.TrimSpace(params.CWD),
		WorktreeSourceMode: worktree.SourceMode(strings.TrimSpace(params.WorktreeSource)),
	}
	if err := validateAgentWorktreeOptions(worktreeOptions); err != nil {
		return "", fmt.Errorf("agent: %w", err)
	}

	// If an executor is registered, run a real sub-agent.
	runner := AgentRunnerFromCtx(ctx)
	if runner == nil {
		return "", ErrMissingToolOwner
	}
	if runner != nil && runner.HasExecutor() {
		opts := AgentExecOptions{
			Task:                    strings.TrimSpace(params.Prompt),
			Description:             strings.TrimSpace(params.Description),
			Model:                   strings.TrimSpace(params.Model),
			AllowedTools:            nil,
			SystemPromptSuffix:      fmt.Sprintf("You are a sub-agent. Your task: %s", strings.TrimSpace(params.Description)),
			ParentSessionID:         SessionIDFromCtx(ctx),
			ParentThreadID:          ThreadIDFromCtx(ctx),
			ParentAgentID:           AgentIDFromCtx(ctx),
			ToolUseID:               ToolUseIDFromCtx(ctx),
			SubagentType:            strings.TrimSpace(params.SubagentType),
			Name:                    strings.TrimSpace(params.Name),
			TeamName:                strings.TrimSpace(params.TeamName),
			Mode:                    strings.TrimSpace(params.Mode),
			InheritedPermissionMode: InheritedPermissionModeFromContext(ctx),
			Isolation:               strings.TrimSpace(params.Isolation),
			CWD:                     strings.TrimSpace(params.CWD),
			WorktreeSourceMode:      worktreeOptions.WorktreeSourceMode,
			WorkItem:                workItem,
		}

		// Background execution mode.
		if params.RunInBackground != nil && *params.RunInBackground {
			if running := runner.ActiveCount(); running >= 4 {
				return "", fmt.Errorf("agent: too many background agents running (%d), wait for some to complete", running)
			}
			return runAgentInBackground(ctx, runner, opts, params.Description)
		}

		result, err := RunAgent(ctx, runner, opts)
		if err != nil {
			return "", fmt.Errorf("agent: sub-agent execution failed: %w", err)
		}

		return formatAgentResult(params.Description, result), nil
	}

	// An explicitly bound runner may still have no executor in an embedding
	// that intentionally disables Agent execution.
	if workItem != nil {
		return "", fmt.Errorf(
			"agent: work_item admission requires an engine-owned executor",
		)
	}
	result := "Sub-agent spawning is not available — no executor has been registered. Continue the work in the main agent loop instead."
	if strings.TrimSpace(params.Model) != "" {
		result += fmt.Sprintf(" Requested model: %s.", params.Model)
	}
	return result, nil
}

// runAgentInBackground starts an agent in a goroutine and returns the agent ID for polling.
func runAgentInBackground(
	ctx context.Context,
	runner *AgentRunner,
	opts AgentExecOptions,
	description string,
) (string, error) {
	agent, err := RunAgentBackground(context.WithoutCancel(ctx), runner, opts)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString("Agent started in background.\n")
	fmt.Fprintf(&sb, "Agent ID: %s\n", agent.ID)
	fmt.Fprintf(&sb, "Status: %s\n", agent.Status)
	if agent.OutputFile != "" {
		fmt.Fprintf(&sb, "Output file: %s\n", agent.OutputFile)
	}
	fmt.Fprintf(&sb, "Task: %s\n", description)
	sb.WriteString("You will be notified when it completes.")
	return sb.String(), nil
}

// formatAgentResult produces a structured summary of the sub-agent execution.
func formatAgentResult(description string, result *AgentExecResult) string {
	var sb strings.Builder
	if result != nil && result.Outcome == AgentExecOutcomeBackgrounded {
		sb.WriteString("Agent moved to background.\n")
		fmt.Fprintf(&sb, "Agent ID: %s\n", result.AgentID)
		fmt.Fprintf(&sb, "Generation: %d\n", result.Generation)
		fmt.Fprintf(&sb, "Status: running\n")
		fmt.Fprintf(&sb, "Task: %s\n", description)
		sb.WriteString("The same Agent continues running and can be inspected, messaged, or aborted.")
		return sb.String()
	}
	fmt.Fprintf(&sb, "Sub-agent completed task: %s\n\n", description)
	fmt.Fprintf(&sb, "Result:\n%s\n", result.Result)
	if result.TurnCount > 0 {
		fmt.Fprintf(&sb, "\nTurns used: %d", result.TurnCount)
	}
	if len(result.ToolsUsed) > 0 {
		fmt.Fprintf(&sb, "\nTools used: %s", strings.Join(result.ToolsUsed, ", "))
	}
	if result.WorktreePath != "" {
		fmt.Fprintf(&sb, "\nWorktree path: %s", result.WorktreePath)
	}
	if result.WorktreeBranch != "" {
		fmt.Fprintf(&sb, "\nWorktree branch: %s", result.WorktreeBranch)
	}
	if details := formatAgentWorktreeDetails(result.Worktree); details != "" {
		fmt.Fprintf(&sb, "\n%s", details)
	}
	return sb.String()
}

func formatAgentWorktreeDetails(result *AgentWorktreeResult) string {
	if result == nil || strings.TrimSpace(result.RecordID) == "" {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Worktree record: %s", result.RecordID)
	fmt.Fprintf(&sb, "\nWorktree state: %s", result.State)
	if result.State != worktree.StateRemoved && result.Path != "" {
		fmt.Fprintf(&sb, "\nWorktree path: %s", result.Path)
	}
	if result.State != worktree.StateRemoved && result.Branch != "" {
		fmt.Fprintf(&sb, "\nWorktree branch: %s", result.Branch)
	}
	if result.BaseCommit != "" {
		fmt.Fprintf(&sb, "\nWorktree base: %s", result.BaseCommit)
	}
	if result.SourceDirtyReport != nil && result.SourceDirtyReport.Dirty {
		sb.WriteString("\nSource changes were omitted from the child worktree.")
		appendAgentDirtyReport(&sb, "Omitted source files", result.SourceDirtyReport)
	}
	if result.ResultDirtyReport != nil && result.ResultDirtyReport.Dirty {
		appendAgentDirtyReport(&sb, "Retained changed files", result.ResultDirtyReport)
		if result.ResultDirtyReport.Patch != "" {
			sb.WriteString("\nBounded worktree patch:\n")
			sb.WriteString(result.ResultDirtyReport.Patch)
		}
		if result.ResultDirtyReport.PatchTruncated {
			sb.WriteString("\nWorktree patch is partial; inspect the retained path before integration.")
		}
	}
	if result.LastErrorCategory != "" {
		fmt.Fprintf(&sb, "\nWorktree status category: %s", result.LastErrorCategory)
	}
	if result.LastError != "" {
		fmt.Fprintf(&sb, "\nWorktree status detail: %s", result.LastError)
	}
	return sb.String()
}

func appendAgentDirtyReport(
	sb *strings.Builder,
	label string,
	report *worktree.DirtyReport,
) {
	if report == nil {
		return
	}
	if len(report.ChangedFiles) > 0 {
		fmt.Fprintf(sb, "\n%s: %s", label, strings.Join(report.ChangedFiles, ", "))
	}
	if report.NewCommits > 0 {
		fmt.Fprintf(sb, "\nNew commits: %d", report.NewCommits)
	}
	if report.Truncated {
		sb.WriteString("\nChanged-file summary is truncated.")
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func withTaskHashes(ids []string) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			continue
		}
		parts = append(parts, "#"+id)
	}
	return strings.Join(parts, ", ")
}
