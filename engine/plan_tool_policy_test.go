package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/tools"
)

func TestP17H0ModelVisiblePlanPolicyIsDeterministicAndFailClosed(
	t *testing.T,
) {
	registry := tools.NewRegistry()
	for _, name := range []string{
		"Bash",
		"Read",
		"Write",
		"Edit",
		"EnterPlanMode",
		"ExitPlanMode",
		"AskUserQuestion",
		"Agent",
		"mcp__dynamic__unknown",
		"ListMcpResourcesTool",
		"ReadMcpResourceTool",
		"Skill",
		"TodoWrite",
		"LSP",
	} {
		registry.Register(tools.ToolImpl{
			Info:       &schema.ToolInfo{Name: name},
			IsReadOnly: true,
		})
	}
	eng := &QueryEngine{
		config: QueryEngineConfig{
			SessionID:      "leader-session",
			ToolRegistry:   registry,
			PermissionMode: permission.ModeDefault,
		},
		toolRegistry:    registry,
		permissionRules: permission.NewRulesEngine(nil),
	}

	wantDefault := []string{
		"Agent",
		"AskUserQuestion",
		"Bash",
		"Edit",
		"EnterPlanMode",
		"LSP",
		"ListMcpResourcesTool",
		"Read",
		"ReadMcpResourceTool",
		"Skill",
		"TodoWrite",
		"Write",
		"mcp__dynamic__unknown",
	}
	if got := poolToolNames(eng.modelVisibleTools()); !reflect.DeepEqual(
		got,
		wantDefault,
	) {
		t.Fatalf("default model tools = %#v, want %#v", got, wantDefault)
	}

	eng.config.PermissionMode = permission.ModePlan
	eng.planState = PlanState{Phase: PlanPhaseActive}
	wantActive := []string{
		"AskUserQuestion",
		"Edit",
		"ExitPlanMode",
		"LSP",
		"ListMcpResourcesTool",
		"Read",
		"ReadMcpResourceTool",
		"Skill",
		"TodoWrite",
		"Write",
	}
	for attempt := 0; attempt < 3; attempt++ {
		if got := poolToolNames(eng.modelVisibleTools()); !reflect.DeepEqual(
			got,
			wantActive,
		) {
			t.Fatalf(
				"active model tools attempt %d = %#v, want %#v",
				attempt,
				got,
				wantActive,
			)
		}
	}

	noRegistry := &QueryEngine{
		config: QueryEngineConfig{
			PermissionMode: permission.ModePlan,
			Tools: []*schema.ToolInfo{
				{Name: "Read"},
				{Name: "Bash"},
				{Name: "ExitPlanMode"},
			},
		},
		planState: PlanState{Phase: PlanPhaseActive},
	}
	if got := poolToolNames(noRegistry.modelVisibleTools()); !reflect.DeepEqual(
		got,
		[]string{"Read", "ExitPlanMode"},
	) {
		t.Fatalf("registry-free active tools = %#v", got)
	}
}

func TestP17H0PlanPolicyUsesOneProjectionAndExecutionDecision(
	t *testing.T,
) {
	registry := tools.NewRegistry()
	names := []string{
		"Read",
		"Write",
		"Edit",
		"ExitPlanMode",
		"TodoWrite",
		"Bash",
		"Agent",
		"mcp__dynamic__unknown",
	}
	for _, name := range names {
		registry.Register(tools.ToolImpl{
			Info:       &schema.ToolInfo{Name: name},
			IsReadOnly: true,
		})
	}
	home := p17H0RealTempDir(t)
	t.Setenv("HOME", home)
	exact := tools.GetPlanFilePath("session", "agent")

	for _, name := range names {
		projection := evaluatePlanToolPolicy(planToolPolicyRequest{
			Active:     true,
			Projection: true,
			ToolName:   name,
			SessionID:  "session",
			AgentID:    "agent",
			Registry:   registry,
		})
		input := map[string]any{}
		if name == "Write" || name == "Edit" {
			input["file_path"] = exact
		}
		execution := evaluatePlanToolPolicy(planToolPolicyRequest{
			Active:    true,
			ToolName:  name,
			Input:     input,
			SessionID: "session",
			AgentID:   "agent",
			Registry:  registry,
		})
		if projection.Allowed != execution.Allowed {
			t.Fatalf(
				"%s projection/execution = %v/%v",
				name,
				projection,
				execution,
			)
		}
	}
}

func TestP17H0ExactPlanFilePolicyRejectsAliasesAndSymlinks(
	t *testing.T,
) {
	home := p17H0RealTempDir(t)
	t.Setenv("HOME", home)
	sessionID := "session-a"
	agentID := "agent-a"
	exact := tools.GetPlanFilePath(sessionID, agentID)
	plansDir := tools.GetPlansDirPath()

	tests := []struct {
		name string
		path string
	}{
		{name: "plans prefix sibling", path: plansDir + "-evil/" + filepath.Base(exact)},
		{name: "target prefix sibling", path: exact + "-evil"},
		{
			name: "traversal alias",
			path: filepath.Dir(exact) + string(os.PathSeparator) +
				"nested" + string(os.PathSeparator) + ".." +
				string(os.PathSeparator) + filepath.Base(exact),
		},
		{name: "relative alias", path: filepath.Base(exact)},
		{
			name: "another session",
			path: tools.GetPlanFilePath("session-b", agentID),
		},
		{
			name: "another agent",
			path: tools.GetPlanFilePath(sessionID, "agent-b"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			allowed, _ := isExactPlanFileMutation(
				map[string]any{"file_path": test.path},
				sessionID,
				agentID,
			)
			if allowed {
				t.Fatalf("unsafe alias %q was allowed", test.path)
			}
		})
	}

	if allowed, reason := isExactPlanFileMutation(
		map[string]any{"file_path": exact},
		sessionID,
		agentID,
	); !allowed {
		t.Fatalf("exact plan file denied: %s", reason)
	}
	if _, err := os.Stat(plansDir); !os.IsNotExist(err) {
		t.Fatalf("exact plan admission created plans directory: %v", err)
	}

	target := filepath.Join(home, "target.md")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(exact), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, exact); err != nil {
		t.Fatal(err)
	}
	if allowed, _ := isExactPlanFileMutation(
		map[string]any{"file_path": exact},
		sessionID,
		agentID,
	); allowed {
		t.Fatal("symlink target was allowed")
	}

	realHome := p17H0RealTempDir(t)
	homeAlias := filepath.Join(p17H0RealTempDir(t), "home-alias")
	if err := os.Symlink(realHome, homeAlias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", homeAlias)
	aliasedPlan := tools.GetPlanFilePath("session", "")
	if allowed, _ := isExactPlanFileMutation(
		map[string]any{"file_path": aliasedPlan},
		"session",
		"",
	); allowed {
		t.Fatal("symlinked plan parent was allowed")
	}
}

func TestP17H0RuntimeAdmissionPrecedesHooksPermissionAndExecution(
	t *testing.T,
) {
	home := p17H0RealTempDir(t)
	t.Setenv("HOME", home)
	registry := tools.NewRegistry()
	for _, name := range []string{
		"Bash",
		"UnknownReadOnly",
		"EnterPlanMode",
		"ExitPlanMode",
		"Write",
		"Edit",
	} {
		registry.Register(tools.ToolImpl{
			Info:       &schema.ToolInfo{Name: name},
			IsReadOnly: true,
		})
	}
	var permissionChecks atomic.Int32
	var executions atomic.Int32
	params := QueryParams{
		ToolRegistry: registry,
		CanUseTool: func(
			context.Context,
			string,
			map[string]any,
			*ToolUseContext,
		) (bool, string) {
			permissionChecks.Add(1)
			return true, ""
		},
		ToolExecutor: func(
			context.Context,
			string,
			string,
		) (string, error) {
			executions.Add(1)
			return "executed", nil
		},
	}
	active := &ToolUseContext{
		SessionID: "session",
		AgentID:   "agent",
		PlanMode:  true,
		Options: &ToolUseOptions{
			PermissionMode: permission.ModePlan,
		},
	}
	for _, name := range []string{"Bash", "UnknownReadOnly", "EnterPlanMode"} {
		outcome := executeToolCall(
			context.Background(),
			params,
			nil,
			active,
			toolCallWithArgs("blocked-"+name, name, `{}`),
			nil,
		)
		if outcome == nil || outcome.Result == nil ||
			!strings.Contains(outcome.Result.Content, "not available") &&
				!strings.Contains(outcome.Result.Content, "unavailable") {
			t.Fatalf("%s outcome = %#v", name, outcome)
		}
	}
	for _, inconsistent := range []*ToolUseContext{
		{
			SessionID: "session",
			AgentID:   "agent",
			Options: &ToolUseOptions{
				PermissionMode: permission.ModePlan,
			},
		},
		{
			SessionID: "session",
			AgentID:   "agent",
			PlanMode:  true,
			Options: &ToolUseOptions{
				PermissionMode: permission.ModeDefault,
			},
		},
	} {
		outcome := executeToolCall(
			context.Background(),
			params,
			nil,
			inconsistent,
			toolCallWithArgs("blocked-inconsistent", "Bash", `{}`),
			nil,
		)
		if outcome == nil || outcome.Result == nil ||
			!strings.Contains(outcome.Result.Content, "not available") {
			t.Fatalf("inconsistent Plan state outcome = %#v", outcome)
		}
	}
	noRegistry := params
	noRegistry.ToolRegistry = nil
	outcome := executeToolCall(
		context.Background(),
		noRegistry,
		nil,
		active,
		toolCallWithArgs("blocked-no-registry", "Bash", `{}`),
		nil,
	)
	if outcome == nil || outcome.Result == nil ||
		!strings.Contains(outcome.Result.Content, "no tool registry") {
		t.Fatalf("registry-free Plan outcome = %#v", outcome)
	}
	inactive := &ToolUseContext{
		SessionID: "session",
		AgentID:   "agent",
		Options: &ToolUseOptions{
			PermissionMode: permission.ModeDefault,
		},
	}
	exit := executeToolCall(
		context.Background(),
		params,
		nil,
		inactive,
		toolCallWithArgs("exit-inactive", "ExitPlanMode", `{}`),
		nil,
	)
	if exit == nil || exit.Result == nil ||
		!strings.Contains(exit.Result.Content, "only while plan mode is active") {
		t.Fatalf("inactive ExitPlanMode outcome = %#v", exit)
	}
	if permissionChecks.Load() != 0 || executions.Load() != 0 {
		t.Fatalf(
			"blocked calls reached permission/execution = %d/%d",
			permissionChecks.Load(),
			executions.Load(),
		)
	}

	exact := tools.GetPlanFilePath(active.SessionID, active.AgentID)
	for _, name := range []string{"Write", "Edit"} {
		outcome := executeToolCall(
			context.Background(),
			params,
			nil,
			active,
			toolCallWithArgs(
				"exact-"+name,
				name,
				`{"file_path":`+p17H0JSONString(t, exact)+`}`,
			),
			nil,
		)
		if outcome == nil || outcome.Result == nil ||
			outcome.Result.Content != "executed" {
			t.Fatalf("exact %s outcome = %#v", name, outcome)
		}
	}
	if permissionChecks.Load() != 2 || executions.Load() != 2 {
		t.Fatalf(
			"exact calls permission/execution = %d/%d, want 2/2",
			permissionChecks.Load(),
			executions.Load(),
		)
	}
}

func TestP17H0WrappedPermissionCannotBypassPlanContainment(
	t *testing.T,
) {
	home := p17H0RealTempDir(t)
	t.Setenv("HOME", home)
	registry := tools.NewRegistry()
	for _, name := range []string{"Write", "Bash", "ExitPlanMode"} {
		registry.Register(tools.ToolImpl{
			Info: &schema.ToolInfo{Name: name},
		})
	}
	eng := &QueryEngine{
		config: QueryEngineConfig{
			SessionID:      "session",
			AgentID:        "agent",
			PermissionMode: permission.ModePlan,
			ToolRegistry:   registry,
		},
		toolRegistry:    registry,
		approvalTracker: permission.NewApprovalTracker(),
		permissionRules: permission.NewRulesEngine([]permission.PermissionRule{
			{
				ToolName: "*",
				Action:   permission.ActionAllow,
			},
		}),
	}
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
	toolCtx := &ToolUseContext{
		SessionID: "session",
		AgentID:   "agent",
		PlanMode:  true,
		Options: &ToolUseOptions{
			PermissionMode: permission.ModePlan,
		},
	}
	exact := tools.GetPlanFilePath("session", "agent")
	if allowed, reason := canUse(
		context.Background(),
		"Write",
		map[string]any{"file_path": exact},
		toolCtx,
	); !allowed {
		t.Fatalf("exact plan write denied: %s", reason)
	}
	for _, call := range []struct {
		name  string
		input map[string]any
	}{
		{name: "Bash", input: map[string]any{"command": "true"}},
		{name: "Write", input: map[string]any{"file_path": exact + "-evil"}},
	} {
		if allowed, _ := canUse(
			context.Background(),
			call.name,
			call.input,
			toolCtx,
		); allowed {
			t.Fatalf("%s containment bypassed by allow rule", call.name)
		}
	}
	if innerCalls.Load() != 0 {
		t.Fatalf("plan containment reached inner permission %d times", innerCalls.Load())
	}
}

func TestP17H0UpdatedInputCannotBypassPlanContainment(t *testing.T) {
	home := p17H0RealTempDir(t)
	t.Setenv("HOME", home)
	registry := tools.NewRegistry()
	registry.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{Name: "Write"},
	})
	active := &ToolUseContext{
		SessionID: "session",
		AgentID:   "agent",
		PlanMode:  true,
		Options: &ToolUseOptions{
			PermissionMode: permission.ModePlan,
		},
	}
	exact := tools.GetPlanFilePath(active.SessionID, active.AgentID)
	unsafe := exact + "-evil"
	call := toolCallWithArgs(
		"write",
		"Write",
		`{"file_path":`+p17H0JSONString(t, exact)+`}`,
	)
	var permissionChecks atomic.Int32
	var executions atomic.Int32
	executor := func(context.Context, string, string) (string, error) {
		executions.Add(1)
		return "executed", nil
	}

	t.Run("pre-tool hook", func(t *testing.T) {
		hookExecutor := hooks.NewExecutor()
		hookExecutor.RegisterPreTool(func(
			context.Context,
			string,
			string,
			map[string]any,
		) *hooks.PreToolHookResult {
			return &hooks.PreToolHookResult{
				UpdatedInput: map[string]any{"file_path": unsafe},
			}
		})
		outcome := executeToolCall(
			context.Background(),
			QueryParams{
				ToolRegistry: registry,
				CanUseTool: func(
					context.Context,
					string,
					map[string]any,
					*ToolUseContext,
				) (bool, string) {
					permissionChecks.Add(1)
					return true, ""
				},
				ToolExecutor: executor,
			},
			hookExecutor,
			active,
			call,
			nil,
		)
		if outcome == nil || outcome.Result == nil ||
			!strings.Contains(outcome.Result.Content, "exact session plan file") {
			t.Fatalf("hook-mutated outcome = %#v", outcome)
		}
		if permissionChecks.Load() != 0 || executions.Load() != 0 {
			t.Fatalf(
				"hook mutation reached permission/execution = %d/%d",
				permissionChecks.Load(),
				executions.Load(),
			)
		}
	})

	t.Run("permission callback", func(t *testing.T) {
		outcome := executeToolCall(
			context.Background(),
			QueryParams{
				ToolRegistry: registry,
				CanUseTool: func(
					ctx context.Context,
					_ string,
					_ map[string]any,
					_ *ToolUseContext,
				) (bool, string) {
					permissionChecks.Add(1)
					SetUpdatedInput(
						ctx,
						map[string]any{"file_path": unsafe},
					)
					return true, ""
				},
				ToolExecutor: executor,
			},
			nil,
			active,
			call,
			nil,
		)
		if outcome == nil || outcome.Result == nil ||
			!strings.Contains(outcome.Result.Content, "exact session plan file") {
			t.Fatalf("permission-mutated outcome = %#v", outcome)
		}
		if permissionChecks.Load() != 1 || executions.Load() != 0 {
			t.Fatalf(
				"permission mutation checks/execution = %d/%d",
				permissionChecks.Load(),
				executions.Load(),
			)
		}
	})

	t.Run("target swapped before execution", func(t *testing.T) {
		target := filepath.Join(home, "target.md")
		if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(exact), 0o700); err != nil {
			t.Fatal(err)
		}
		outcome := executeToolCall(
			context.Background(),
			QueryParams{
				ToolRegistry: registry,
				CanUseTool: func(
					context.Context,
					string,
					map[string]any,
					*ToolUseContext,
				) (bool, string) {
					permissionChecks.Add(1)
					if err := os.Symlink(target, exact); err != nil {
						t.Fatal(err)
					}
					return true, ""
				},
				ToolExecutor: executor,
			},
			nil,
			active,
			call,
			nil,
		)
		if outcome == nil || outcome.Result == nil ||
			!strings.Contains(outcome.Result.Content, "exact session plan file") {
			t.Fatalf("swapped-target outcome = %#v", outcome)
		}
		if permissionChecks.Load() != 2 || executions.Load() != 0 {
			t.Fatalf(
				"target swap checks/execution = %d/%d",
				permissionChecks.Load(),
				executions.Load(),
			)
		}
	})
}

func p17H0RealTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func p17H0JSONString(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
