package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/tools"
)

type planToolPolicyRequest struct {
	Active           bool
	Projection       bool
	ToolName         string
	Input            map[string]any
	SessionID        string
	AgentID          string
	PlanFileIdentity string
	Registry         *tools.Registry
}

type planToolPolicyDecision struct {
	Allowed bool
	Reason  string
	Class   planToolPolicyClass
}

type planToolPolicyClass uint8

const (
	planToolPolicyOrdinary planToolPolicyClass = iota
	planToolPolicyExactFileCapability
)

func (d planToolPolicyDecision) hasExactFileCapability() bool {
	return d.Allowed && d.Class == planToolPolicyExactFileCapability
}

var planActiveToolAllowlist = map[string]struct{}{
	"AskUserQuestion":      {},
	"Glob":                 {},
	"Grep":                 {},
	"LSP":                  {},
	"ListMcpResourcesTool": {},
	"Read":                 {},
	"ReadMcpResourceTool":  {},
	"Skill":                {},
	"TodoWrite":            {},
	"WebFetch":             {},
	"WebSearch":            {},
}

// evaluatePlanToolPolicy is the single Plan admission decision shared by the
// model-visible projection and runtime tool execution. Projection requests
// intentionally omit input; runtime Write/Edit requests must prove the exact
// session/Agent plan-file capability.
func evaluatePlanToolPolicy(
	request planToolPolicyRequest,
) planToolPolicyDecision {
	toolName := canonicalPlanToolName(request.Registry, request.ToolName)
	if !request.Active {
		if toolName == "ExitPlanMode" {
			return planToolPolicyDecision{
				Reason: "ExitPlanMode is available only while plan mode is active",
			}
		}
		return planToolPolicyDecision{Allowed: true}
	}

	switch toolName {
	case "EnterPlanMode":
		return planToolPolicyDecision{
			Reason: "EnterPlanMode is unavailable while plan mode is already active",
		}
	case "ExitPlanMode":
		return planToolPolicyDecision{Allowed: true}
	case "Write", "Edit":
		if request.Projection {
			return planToolPolicyDecision{Allowed: true}
		}
		allowed, reason := isExactPlanFileMutation(
			request.Input,
			request.SessionID,
			request.AgentID,
			request.PlanFileIdentity,
		)
		decision := planToolPolicyDecision{
			Allowed: allowed,
			Reason:  reason,
		}
		if allowed {
			decision.Class = planToolPolicyExactFileCapability
		}
		return decision
	default:
		if _, ok := planActiveToolAllowlist[toolName]; ok {
			return planToolPolicyDecision{Allowed: true}
		}
		return planToolPolicyDecision{
			Reason: fmt.Sprintf(
				"tool %s is not available while plan mode is active",
				toolName,
			),
		}
	}
}

func canonicalPlanToolName(
	registry *tools.Registry,
	toolName string,
) string {
	toolName = strings.TrimSpace(toolName)
	if registry == nil {
		return toolName
	}
	impl, ok := registry.Get(toolName)
	if !ok || impl.Info == nil || strings.TrimSpace(impl.Info.Name) == "" {
		return toolName
	}
	return strings.TrimSpace(impl.Info.Name)
}

func filterPlanModelVisibleTools(
	infos []*schema.ToolInfo,
	active bool,
	registry *tools.Registry,
) []*schema.ToolInfo {
	filtered := make([]*schema.ToolInfo, 0, len(infos))
	for _, info := range infos {
		if info == nil {
			continue
		}
		decision := evaluatePlanToolPolicy(planToolPolicyRequest{
			Active:     active,
			Projection: true,
			ToolName:   info.Name,
			Registry:   registry,
		})
		if decision.Allowed {
			filtered = append(filtered, info)
		}
	}
	return filtered
}

func toolContextSessionID(
	toolCtx *ToolUseContext,
	fallback string,
) string {
	if toolCtx != nil && strings.TrimSpace(toolCtx.SessionID) != "" {
		return toolCtx.SessionID
	}
	return fallback
}

func toolContextPlanActive(toolCtx *ToolUseContext) bool {
	return toolCtx != nil &&
		(toolCtx.PlanMode ||
			toolCtx.Options != nil &&
				toolCtx.Options.PermissionMode == permission.ModePlan)
}

func evaluateToolContextPlanPolicy(
	toolCtx *ToolUseContext,
	registry *tools.Registry,
	toolName string,
	input map[string]any,
) planToolPolicyDecision {
	return evaluatePlanToolPolicy(planToolPolicyRequest{
		Active:           toolContextPlanActive(toolCtx),
		ToolName:         toolName,
		Input:            input,
		SessionID:        toolContextSessionID(toolCtx, ""),
		AgentID:          toolContextAgentID(toolCtx, ""),
		PlanFileIdentity: toolContextPlanFileIdentity(toolCtx),
		Registry:         registry,
	})
}

func toolContextAgentID(
	toolCtx *ToolUseContext,
	fallback string,
) string {
	if toolCtx != nil && strings.TrimSpace(toolCtx.AgentID) != "" {
		return toolCtx.AgentID
	}
	return fallback
}

func toolContextPlanFileIdentity(toolCtx *ToolUseContext) string {
	if toolCtx == nil || toolCtx.Options == nil {
		return ""
	}
	return toolCtx.Options.PlanFilePath
}

func isExactPlanFileMutation(
	input map[string]any,
	sessionID string,
	agentID string,
	identities ...string,
) (bool, string) {
	filePath, _ := input["file_path"].(string)
	expected := filepath.Clean(tools.GetPlanFilePath(sessionID, agentID))
	if len(identities) > 0 && strings.TrimSpace(identities[0]) != "" {
		expected = identities[0]
	}
	reason := fmt.Sprintf(
		"plan mode permits only the exact session plan file %s",
		expected,
	)

	if strings.TrimSpace(filePath) == "" ||
		filePath != strings.TrimSpace(filePath) ||
		!filepath.IsAbs(filePath) ||
		filePath != filepath.Clean(filePath) {
		return false, reason
	}
	if strings.TrimSpace(expected) == "" ||
		expected != strings.TrimSpace(expected) ||
		!filepath.IsAbs(expected) ||
		expected != filepath.Clean(expected) ||
		filePath != expected {
		return false, reason
	}

	if !pathHasNoSymlinkComponents(filepath.Dir(expected)) {
		return false, reason
	}
	info, err := os.Lstat(expected)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return false, reason
		}
		return true, ""
	}
	if !os.IsNotExist(err) {
		return false, reason
	}
	return true, ""
}

func pathHasNoSymlinkComponents(path string) bool {
	path = filepath.Clean(path)
	for {
		info, err := os.Lstat(path)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return false
			}
			resolved, resolveErr := filepath.EvalSymlinks(path)
			return resolveErr == nil && filepath.Clean(resolved) == path
		}
		if !os.IsNotExist(err) {
			return false
		}
		parent := filepath.Dir(path)
		if parent == path {
			return false
		}
		path = parent
	}
}
