package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/schema"
)

type sessionIDCtxKey struct{}

// WithSessionID returns a context carrying the session ID for plan file scoping.
func WithSessionID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, sessionIDCtxKey{}, id)
}

// SessionIDFromCtx extracts the session ID from context.
func SessionIDFromCtx(ctx context.Context) string {
	if id, ok := ctx.Value(sessionIDCtxKey{}).(string); ok {
		return id
	}
	return ""
}

type agentIDCtxKey struct{}

// WithAgentID returns a context carrying the agent ID for plan file scoping.
func WithAgentID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, agentIDCtxKey{}, id)
}

// AgentIDFromCtx extracts the agent ID from context. Returns "" for the main agent.
func AgentIDFromCtx(ctx context.Context) string {
	if id, ok := ctx.Value(agentIDCtxKey{}).(string); ok {
		return id
	}
	return ""
}

type planFileIdentityCtxKey struct{}

// WithPlanFileIdentity carries the engine-owned exact Plan file capability
// through canonical tool execution. It is intentionally narrower than a
// session/Agent-derived path so resumed sessions keep one identity.
func WithPlanFileIdentity(ctx context.Context, path string) context.Context {
	return context.WithValue(ctx, planFileIdentityCtxKey{}, path)
}

// PlanFileIdentityFromCtx returns the engine-owned exact Plan file capability.
func PlanFileIdentityFromCtx(ctx context.Context) string {
	if path, ok := ctx.Value(planFileIdentityCtxKey{}).(string); ok {
		return path
	}
	return ""
}

// EnterPlanModeTool returns a tool that transitions the agent into plan mode.
func EnterPlanModeTool() ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: "EnterPlanMode",
			Desc: `Transitions into plan mode where the model explores the codebase and designs an implementation approach for user approval. Use when starting non-trivial implementation tasks.

In plan mode, focus on:
1. Understanding the current code structure and relevant files
2. Identifying what changes are needed and where
3. Designing the implementation approach
4. Writing a clear plan for user review before making changes`,
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
		},
		IsReadOnly:           true,
		IsPlanModeTransition: true,
		ExecuteCtx: func(ctx context.Context, input string) (string, error) {
			sessionID := SessionIDFromCtx(ctx)
			agentID := AgentIDFromCtx(ctx)
			planPath := planFilePathFromCtx(ctx, sessionID, agentID)

			return fmt.Sprintf(`Plan mode entered. You are now in planning mode.

## Plan File Info:
You should create or edit your plan at: %s
Use the Write tool to create the file or Edit tool to modify it.
This is the ONLY file you are allowed to write during plan mode.

## Instructions:
1. Explore the codebase and understand the requirements
2. Design your implementation approach
3. Write your plan to the file above
4. When your plan is complete, use ExitPlanMode to present it for user approval`, planPath), nil
		},
	}
}

// AllowedPrompt is deprecated display metadata supplied by the model when
// exiting Plan mode. It never creates a permission rule or runtime grant.
type AllowedPrompt struct {
	Tool   string `json:"tool"`
	Prompt string `json:"prompt"`
}

// ExitPlanModeInput is the input schema for ExitPlanMode V2. AllowedPrompts is
// retained for compatibility as display-only metadata.
type ExitPlanModeInput struct {
	AllowedPrompts []AllowedPrompt `json:"allowedPrompts,omitempty"`
}

// ExitPlanModeOutput is the structured output from ExitPlanMode V2.
type ExitPlanModeOutput struct {
	Plan                   string `json:"plan"`
	IsAgent                bool   `json:"isAgent"`
	FilePath               string `json:"filePath,omitempty"`
	HasTaskTool            bool   `json:"hasTaskTool,omitempty"`
	PlanWasEdited          bool   `json:"planWasEdited,omitempty"`
	AwaitingLeaderApproval bool   `json:"awaitingLeaderApproval,omitempty"`
	RequestID              string `json:"requestId,omitempty"`
}

const (
	configDirName        = ".claude"
	plansDirName         = "plans"
	exitPlanModeToolName = "ExitPlanMode"
)

var planRequestIDCounter atomic.Uint64

// GetPlansDirPath returns the plans directory path without creating it.
func GetPlansDirPath() string {
	home := os.Getenv("HOME")
	return filepath.Join(home, configDirName, plansDirName)
}

// GetPlanFilePath returns the session-scoped plan file path.
// Plans are stored in ~/.claude/plans/ to avoid CWD conflicts and to be
// writable in plan mode (which blocks writes to the project directory).
// Each session gets its own file; sub-agents get a suffix.
// Mirrors plans.ts:getPlanFilePath from the reference.
func GetPlanFilePath(sessionID, agentID string) string {
	slug := sessionID
	if slug == "" {
		slug = "default"
	}
	if agentID != "" {
		return filepath.Join(GetPlansDirPath(), fmt.Sprintf("%s-agent-%s.md", slug, agentID))
	}
	return filepath.Join(GetPlansDirPath(), fmt.Sprintf("%s.md", slug))
}

// GetPlan reads the plan content from disk.
func GetPlan(sessionID, agentID string) string {
	return readPlanFile(GetPlanFilePath(sessionID, agentID))
}

// SavePlan writes plan content to disk.
func SavePlan(sessionID, agentID, content string) error {
	return savePlanFile(GetPlanFilePath(sessionID, agentID), content)
}

// generatePlanRequestID creates a unique request ID for plan approval requests.
func generatePlanRequestID(agentName string) string {
	return fmt.Sprintf("plan_approval_%s_%d_%d", agentName, time.Now().UnixNano(), planRequestIDCounter.Add(1))
}

// ExitPlanModeTool returns the V2 ExitPlanMode tool with allowedPrompts
// support, plan validation, plan persistence, and structured output.
//
// Reference: src/tools/ExitPlanModeTool/ExitPlanModeV2Tool.ts (493 lines)
//
// Key reference behaviors ported:
// - validateInput rejects calls when not in plan mode
// - checkPermissions asks user for non-teammates, allows for teammates
// - Teammate flow sends plan_approval_request to team-lead mailbox
// - Non-teammate flow reads plan from disk, persists it, restores permission mode
// - Output includes plan content, file path, hasTaskTool, and team approval state
// - allowedPrompts retained as non-authoritative implementation metadata
func ExitPlanModeTool() ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: exitPlanModeToolName,
			Desc: `Use this tool when you are in plan mode and have finished planning and are ready for user approval to begin implementation.

## How This Tool Works
- You should have already written your plan to the plan file specified in the plan mode system message
- This tool reads the plan from the file you wrote and presents it to the user for approval
- This tool simply signals that you are done planning and ready for the user to review

## When to Use This Tool

IMPORTANT: Only use this tool when the task requires planning the implementation steps of a task that requires writing code. For research tasks where you're gathering information, searching files, reading files, or trying to understand the codebase — do NOT use this tool.

Use this tool after you have presented your plan in a <proposed_plan> block and are ready for the user to review and approve it.

## Before Using This Tool

Ensure your plan is complete and unambiguous:
- If you have unresolved questions about requirements or approach, use AskUserQuestion first
- Once your plan is finalized, use THIS tool to request approval

**Important:** Do NOT use AskUserQuestion to ask "Is this plan okay?" or "Should I proceed?" — that's exactly what THIS tool does. ExitPlanMode inherently requests user approval of your plan.

## Examples

1. User asked: "Search for and understand the implementation of X in the codebase" — Do NOT use ExitPlanMode because you are not planning implementation steps.
2. User asked: "Help me implement feature Y" — Use ExitPlanMode after you have finished planning the implementation steps.
3. User asked: "Add a new feature to handle Z" — If unsure about the approach, use AskUserQuestion first, then use ExitPlanMode after clarifying and finalizing the plan.`,
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"allowedPrompts": {
					Desc:     "Deprecated display-only capability notes. These values never grant runtime permission or create permission rules.",
					Type:     "array",
					Required: false,
				},
			}),
		},
		IsPlanModeTransition: true,
		NeedsPermissions:     true,
		ValidateInput: func(input map[string]any) error {
			// Reference: validateInput rejects calls when not in plan mode.
			// In the Go port, actual mode checking is handled by the engine's
			// plan mode transition logic. This validator ensures the input shape
			// is valid (allowedPrompts must be well-formed if provided).
			if prompts, ok := input["allowedPrompts"]; ok && prompts != nil {
				raw, err := json.Marshal(prompts)
				if err != nil {
					return fmt.Errorf("invalid allowedPrompts: %w", err)
				}
				var aps []AllowedPrompt
				if err := json.Unmarshal(raw, &aps); err != nil {
					return fmt.Errorf("invalid allowedPrompts format: %w", err)
				}
				for i, ap := range aps {
					if ap.Tool == "" {
						return fmt.Errorf("allowedPrompts[%d].tool is required", i)
					}
					if ap.Prompt == "" {
						return fmt.Errorf("allowedPrompts[%d].prompt is required", i)
					}
				}
			}
			return nil
		},
		ExecuteCtx: func(ctx context.Context, input string) (string, error) {
			var parsed ExitPlanModeInput
			if input != "" && input != "{}" {
				if err := json.Unmarshal([]byte(input), &parsed); err != nil { //nolint:staticcheck
					// Continue with no allowed prompts — lenient parsing
				}
			}

			sessionID := SessionIDFromCtx(ctx)
			agentID := AgentIDFromCtx(ctx)
			filePath := planFilePathFromCtx(ctx, sessionID, agentID)

			plan := readPlanFile(filePath)

			// Persist plan to disk so the approval dialog can read it.
			if plan != "" {
				_ = savePlanFile(filePath, plan)
			}

			// Teammate approval flow: if running as a teammate with required plan mode,
			// send plan to team lead for approval via mailbox instead of prompting locally.
			// Reference: ExitPlanModeV2Tool.ts lines 264-313
			if IsTeammateMode() {
				if plan == "" {
					return "", fmt.Errorf("no plan file found at %s. Please write your plan to this file before calling ExitPlanMode", filePath)
				}
				agentName := GetTeammateName()
				if agentName == "" {
					agentName = "unknown"
				}
				teamName := os.Getenv("CLAUDE_CODE_TEAM_NAME")
				requestID := generatePlanRequestID(agentName)

				approvalRequest := map[string]string{
					"type":         "plan_approval_request",
					"from":         agentName,
					"timestamp":    time.Now().UTC().Format(time.RFC3339),
					"planFilePath": filePath,
					"planContent":  plan,
					"requestId":    requestID,
				}
				approvalJSON, _ := json.Marshal(approvalRequest)

				msg := MailboxMessage{
					From:      agentName,
					Text:      string(approvalJSON),
					Timestamp: time.Now().UTC().Format(time.RFC3339),
				}
				_ = WriteToMailbox("team-lead", msg, teamName)

				return fmt.Sprintf(`Your plan has been submitted to the team lead for approval.

Plan file: %s

**What happens next:**
1. Wait for the team lead to review your plan
2. You will receive a message in your inbox with approval/rejection
3. If approved, you can proceed with implementation
4. If rejected, refine your plan based on the feedback

**Important:** Do NOT proceed until you receive approval. Check your inbox for response.

Request ID: %s`, filePath, requestID), nil
			}

			// Non-teammate flow: exit plan mode directly
			// Reference: ExitPlanModeV2Tool.ts lines 316-417

			// Check if Agent tool is available (for team hint)
			hasTaskTool := hasToolInRegistry("Agent")

			if plan == "" || strings.TrimSpace(plan) == "" {
				return "User has approved exiting plan mode. You can now proceed.", nil
			}

			var result strings.Builder
			result.WriteString("User has approved your plan. You can now start coding. Start with updating your todo list if applicable\n\n")
			fmt.Fprintf(&result, "Your plan has been saved to: %s\n", filePath)
			result.WriteString("You can refer back to it if needed during implementation.")

			if hasTaskTool {
				result.WriteString("\n\nIf this plan can be broken down into multiple independent tasks, consider using the TeamCreate tool to create a team and parallelize the work.")
			}

			result.WriteString("\n\n## Approved Plan:\n")
			result.WriteString(plan)

			if len(parsed.AllowedPrompts) > 0 {
				result.WriteString(
					"\n\n## Model-provided Implementation Notes (not granted):\n",
				)
				for _, ap := range parsed.AllowedPrompts {
					fmt.Fprintf(&result, "- %s: %s\n", ap.Tool, ap.Prompt)
				}
				result.WriteString(
					"\nThese semantic descriptions do not grant runtime permission. " +
						"Each concrete action remains subject to the active permission policy.",
				)
			}

			return result.String(), nil
		},
	}
}

func planFilePathFromCtx(
	ctx context.Context,
	sessionID string,
	agentID string,
) string {
	if path := strings.TrimSpace(PlanFileIdentityFromCtx(ctx)); path != "" {
		return path
	}
	return GetPlanFilePath(sessionID, agentID)
}

func readPlanFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func savePlanFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// hasToolInRegistry checks if a tool name exists in the default registry.
func hasToolInRegistry(name string) bool {
	if DefaultRegistry == nil {
		return false
	}
	_, exists := DefaultRegistry.tools[name]
	return exists
}

// IsTeammateMode returns true if running as a teammate (sub-agent in a team).
func IsTeammateMode() bool {
	return os.Getenv("CLAUDE_CODE_AGENT_NAME") != "" && os.Getenv("CLAUDE_CODE_TEAM_NAME") != ""
}

// GetTeammateName returns the current agent's name within the team.
func GetTeammateName() string {
	return os.Getenv("CLAUDE_CODE_AGENT_NAME")
}
