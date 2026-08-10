package engine

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/tools"
)

func TestP20H0ExactPlanCapabilityPrecedesOrdinaryAsk(t *testing.T) {
	home := p17H0RealTempDir(t)
	t.Setenv("HOME", home)
	registry := p20H0PlanMutationRegistry()
	exact := tools.GetPlanFilePath("session", "agent")

	for _, toolName := range []string{"Write", "Edit"} {
		t.Run(toolName, func(t *testing.T) {
			projection := evaluatePlanToolPolicy(planToolPolicyRequest{
				Active:     true,
				Projection: true,
				ToolName:   toolName,
				Registry:   registry,
			})
			if !projection.Allowed || projection.hasExactFileCapability() {
				t.Fatalf("%s projection decision = %#v", toolName, projection)
			}
			decision := evaluatePlanToolPolicy(planToolPolicyRequest{
				Active:    true,
				ToolName:  toolName,
				Input:     map[string]any{"file_path": exact},
				SessionID: "session",
				AgentID:   "agent",
				Registry:  registry,
			})
			if !decision.hasExactFileCapability() {
				t.Fatalf("exact %s decision = %#v", toolName, decision)
			}

			var innerCalls atomic.Int32
			eng := p20H0PlanEngine(
				registry,
				permission.ModePlan,
				[]permission.PermissionRule{{
					ToolName: toolName,
					Action:   permission.ActionAsk,
				}},
			)
			canUse := eng.wrapCanUseTool(func(
				context.Context,
				string,
				map[string]any,
				*ToolUseContext,
			) (bool, string) {
				innerCalls.Add(1)
				return false, "ordinary prompt reached"
			})
			probe := &projectGraphHITLProbe{}
			allowed, reason := canUse(
				withProjectGraphHITLProbe(context.Background(), probe),
				toolName,
				map[string]any{"file_path": exact},
				p20H0ActivePlanContext(),
			)
			if !allowed || reason != "" {
				t.Fatalf("exact %s result = (%v, %q)", toolName, allowed, reason)
			}
			if innerCalls.Load() != 0 || probe.captured != nil {
				t.Fatalf(
					"exact %s reached ordinary permission: calls=%d interrupt=%#v",
					toolName,
					innerCalls.Load(),
					probe.captured,
				)
			}
			if len(eng.permissionDenials) != 0 ||
				eng.denialTracking.ConsecutiveDenials != 0 ||
				eng.denialTracking.TotalDenials != 0 {
				t.Fatalf(
					"exact %s changed denial bookkeeping: denials=%d tracking=%#v",
					toolName,
					len(eng.permissionDenials),
					eng.denialTracking,
				)
			}
		})
	}
}

func TestP20H0ExplicitDenyWinsExactPlanCapability(t *testing.T) {
	home := p17H0RealTempDir(t)
	t.Setenv("HOME", home)
	registry := p20H0PlanMutationRegistry()
	exact := tools.GetPlanFilePath("session", "agent")

	for _, toolName := range []string{"Write", "Edit"} {
		t.Run(toolName, func(t *testing.T) {
			var innerCalls atomic.Int32
			eng := p20H0PlanEngine(
				registry,
				permission.ModePlan,
				[]permission.PermissionRule{{
					ToolName: toolName,
					Action:   permission.ActionDeny,
				}},
			)
			canUse := eng.wrapCanUseTool(func(
				context.Context,
				string,
				map[string]any,
				*ToolUseContext,
			) (bool, string) {
				innerCalls.Add(1)
				return true, ""
			})
			allowed, reason := canUse(
				context.Background(),
				toolName,
				map[string]any{"file_path": exact},
				p20H0ActivePlanContext(),
			)
			if allowed || reason != "permission rule denied tool use" {
				t.Fatalf("exact %s deny result = (%v, %q)", toolName, allowed, reason)
			}
			if innerCalls.Load() != 0 {
				t.Fatalf("exact %s deny reached ordinary permission", toolName)
			}
		})
	}
}

func TestP20H0RegisteredAliasUsesCanonicalCapabilityAndDeny(t *testing.T) {
	home := p17H0RealTempDir(t)
	t.Setenv("HOME", home)
	registry := tools.NewRegistry()
	registry.Register(tools.ToolImpl{
		Info:    &schema.ToolInfo{Name: "Write"},
		Aliases: []string{"PlanWrite"},
	})
	exact := tools.GetPlanFilePath("session", "agent")

	for _, test := range []struct {
		name        string
		action      permission.PermissionAction
		wantAllowed bool
		wantReason  string
	}{
		{
			name:        "canonical ask is ordinary",
			action:      permission.ActionAsk,
			wantAllowed: true,
		},
		{
			name:       "canonical deny is authoritative",
			action:     permission.ActionDeny,
			wantReason: "permission rule denied tool use",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var innerCalls atomic.Int32
			eng := p20H0PlanEngine(
				registry,
				permission.ModePlan,
				[]permission.PermissionRule{{
					ToolName: "Write",
					Action:   test.action,
				}},
			)
			canUse := eng.wrapCanUseTool(func(
				context.Context,
				string,
				map[string]any,
				*ToolUseContext,
			) (bool, string) {
				innerCalls.Add(1)
				return false, "ordinary prompt reached"
			})
			allowed, reason := canUse(
				context.Background(),
				"PlanWrite",
				map[string]any{"file_path": exact},
				p20H0ActivePlanContext(),
			)
			if allowed != test.wantAllowed || reason != test.wantReason {
				t.Fatalf(
					"alias result = (%v, %q), want (%v, %q)",
					allowed,
					reason,
					test.wantAllowed,
					test.wantReason,
				)
			}
			if innerCalls.Load() != 0 {
				t.Fatalf("alias reached ordinary permission %d times", innerCalls.Load())
			}
		})
	}
}

func TestP20H0ContainmentPrecedesRulesAndBypass(t *testing.T) {
	home := p17H0RealTempDir(t)
	t.Setenv("HOME", home)
	registry := p20H0PlanMutationRegistry()
	wrong := tools.GetPlanFilePath("session", "agent") + "-other"

	for _, test := range []struct {
		name   string
		mode   permission.Mode
		action permission.PermissionAction
	}{
		{name: "allow rule", mode: permission.ModePlan, action: permission.ActionAllow},
		{name: "ask rule", mode: permission.ModePlan, action: permission.ActionAsk},
		{name: "bypass mode", mode: permission.ModeBypassPermissions},
	} {
		t.Run(test.name, func(t *testing.T) {
			rules := []permission.PermissionRule(nil)
			if test.action != "" {
				rules = []permission.PermissionRule{{
					ToolName: "Write",
					Action:   test.action,
				}}
			}
			eng := p20H0PlanEngine(registry, test.mode, rules)
			var innerCalls atomic.Int32
			canUse := eng.wrapCanUseTool(func(
				context.Context,
				string,
				map[string]any,
				*ToolUseContext,
			) (bool, string) {
				innerCalls.Add(1)
				return true, ""
			})
			allowed, reason := canUse(
				context.Background(),
				"Write",
				map[string]any{"file_path": wrong},
				p20H0ActivePlanContext(),
			)
			if allowed || !strings.Contains(reason, "exact session plan file") {
				t.Fatalf("wrong path result = (%v, %q)", allowed, reason)
			}
			if innerCalls.Load() != 0 {
				t.Fatalf("wrong path reached ordinary permission %d times", innerCalls.Load())
			}
		})
	}
}

func TestP20H0PreToolHardDenyWinsExactPlanCapability(t *testing.T) {
	home := p17H0RealTempDir(t)
	t.Setenv("HOME", home)
	registry := p20H0PlanMutationRegistry()
	eng := p20H0PlanEngine(
		registry,
		permission.ModePlan,
		[]permission.PermissionRule{{
			ToolName: "Write",
			Action:   permission.ActionAsk,
		}},
	)
	var innerCalls atomic.Int32
	var executions atomic.Int32
	canUse := eng.wrapCanUseTool(func(
		context.Context,
		string,
		map[string]any,
		*ToolUseContext,
	) (bool, string) {
		innerCalls.Add(1)
		return true, ""
	})
	hookExecutor := hooks.NewExecutor()
	hookExecutor.RegisterPreTool(func(
		context.Context,
		string,
		string,
		map[string]any,
	) *hooks.PreToolHookResult {
		return &hooks.PreToolHookResult{
			PermissionBehavior: hooks.HookPermissionDeny,
		}
	})
	outcome := executeToolCall(
		context.Background(),
		QueryParams{
			ToolRegistry: registry,
			CanUseTool:   canUse,
			ToolExecutor: func(
				context.Context,
				string,
				string,
			) (string, error) {
				executions.Add(1)
				return "executed", nil
			},
		},
		hookExecutor,
		p20H0ActivePlanContext(),
		toolCallWithArgs(
			"exact-write",
			"Write",
			`{"file_path":`+
				p17H0JSONString(
					t,
					tools.GetPlanFilePath("session", "agent"),
				)+`}`,
		),
		nil,
	)
	if outcome == nil || outcome.Result == nil ||
		!strings.Contains(outcome.Result.Content, "denied by pre-tool hook") {
		t.Fatalf("pre-tool deny outcome = %#v", outcome)
	}
	if innerCalls.Load() != 0 || executions.Load() != 0 {
		t.Fatalf(
			"pre-tool deny reached permission/execution = %d/%d",
			innerCalls.Load(),
			executions.Load(),
		)
	}
}

func TestP20H0ExitPlanModeNeverUsesFileCapability(t *testing.T) {
	registry := p20H0PlanMutationRegistry()
	registry.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{Name: "ExitPlanMode"},
	})
	eng := p20H0PlanEngine(
		registry,
		permission.ModeBypassPermissions,
		[]permission.PermissionRule{{
			ToolName: "ExitPlanMode",
			Action:   permission.ActionAllow,
		}},
	)
	var innerCalls atomic.Int32
	allowed, reason := eng.wrapCanUseTool(func(
		context.Context,
		string,
		map[string]any,
		*ToolUseContext,
	) (bool, string) {
		innerCalls.Add(1)
		return true, ""
	})(
		context.Background(),
		"ExitPlanMode",
		map[string]any{},
		p20H0ActivePlanContext(),
	)
	if allowed || reason != "structured Plan approval prompting not available" {
		t.Fatalf("ExitPlanMode result = (%v, %q)", allowed, reason)
	}
	if innerCalls.Load() != 0 {
		t.Fatalf("ExitPlanMode reached generic permission %d times", innerCalls.Load())
	}
	decision := evaluatePlanToolPolicy(planToolPolicyRequest{
		Active:   true,
		ToolName: "ExitPlanMode",
		Registry: registry,
	})
	if !decision.Allowed || decision.hasExactFileCapability() {
		t.Fatalf("ExitPlanMode policy decision = %#v", decision)
	}
}

func p20H0PlanMutationRegistry() *tools.Registry {
	registry := tools.NewRegistry()
	for _, name := range []string{"Write", "Edit"} {
		registry.Register(tools.ToolImpl{
			Info: &schema.ToolInfo{Name: name},
		})
	}
	return registry
}

func p20H0PlanEngine(
	registry *tools.Registry,
	mode permission.Mode,
	rules []permission.PermissionRule,
) *QueryEngine {
	return &QueryEngine{
		config: QueryEngineConfig{
			SessionID:      "session",
			AgentID:        "agent",
			PermissionMode: mode,
			ToolRegistry:   registry,
		},
		toolRegistry:    registry,
		approvalTracker: permission.NewApprovalTracker(),
		permissionRules: permission.NewRulesEngine(rules),
		denialTracking:  permission.NewDenialTrackingState(),
	}
}

func p20H0ActivePlanContext() *ToolUseContext {
	return &ToolUseContext{
		SessionID: "session",
		AgentID:   "agent",
		PlanMode:  true,
		Options: &ToolUseOptions{
			PermissionMode: permission.ModePlan,
		},
	}
}
