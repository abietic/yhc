package engine

import (
	"context"
	"testing"

	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestPlanModeToolTransitionsCurrentAndFutureState(t *testing.T) {
	reg := tools.NewRegistry()
	tools.RegisterDefaults(reg)
	eng := NewQueryEngine(QueryEngineConfig{CWD: t.TempDir(), PermissionMode: permission.ModeDefault})
	turnID := "turn-plan-transition"
	if _, err := eng.beginPlanTurn(turnID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.endPlanTurn(turnID) })
	toolCtx := &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeDefault}}
	executed := 0
	params := QueryParams{
		ToolRegistry: reg,
		TransitionPermissionMode: func(
			current *ToolUseContext,
			mode permission.Mode,
			requestID string,
		) (*ToolUseContext, func(), error) {
			return eng.transitionPermissionModeForTurn(
				turnID,
				nil,
				current,
				mode,
				requestID,
			)
		},
		CanUseTool: approvePlanExitForTest(
			func(context.Context, string, map[string]any, *ToolUseContext) (bool, string) {
				return true, ""
			},
			permission.ModeDefault,
		),
		ToolExecutor: func(context.Context, string, string) (string, error) {
			executed++
			return "ok", nil
		},
	}
	enter := executeToolCall(context.Background(), params, nil, toolCtx, toolCall("enter", "EnterPlanMode"), nil)
	if enter.ContextModifier == nil {
		t.Fatal("EnterPlanMode did not return a context modifier")
		return
	}
	var publish func()
	var err error
	toolCtx, publish, err = enter.ContextModifier(toolCtx)
	if err != nil {
		t.Fatalf("commit EnterPlanMode: %v", err)
	}
	if publish != nil {
		publish()
	}
	if !toolCtx.PlanMode || toolCtx.Options.PermissionMode != permission.ModePlan || eng.PermissionMode() != permission.ModePlan {
		t.Fatalf("enter transition not synchronized: ctx=%+v options=%q engine=%q", toolCtx, toolCtx.Options.PermissionMode, eng.PermissionMode())
	}

	blockedWrite := executeToolCall(context.Background(), params, nil, toolCtx, toolCallWithArgs("blocked", "Write", `{"file_path":"blocked.txt","content":"blocked"}`), nil)
	if blockedWrite == nil || blockedWrite.Result == nil || !blockedWrite.Result.Extra["is_error"].(bool) {
		t.Fatal("Write should be blocked while the active turn is in plan mode")
		return
	}

	exit := executeToolCall(context.Background(), params, nil, toolCtx, toolCall("exit", "ExitPlanMode"), nil)
	if exit.ContextModifier == nil {
		t.Fatal("approved ExitPlanMode did not return a context modifier")
		return
	}
	toolCtx, publish, err = exit.ContextModifier(toolCtx)
	if err != nil {
		t.Fatalf("commit ExitPlanMode: %v", err)
	}
	if publish != nil {
		publish()
	}
	if toolCtx.PlanMode || toolCtx.Options.PermissionMode != permission.ModeDefault || eng.PermissionMode() != permission.ModeDefault {
		t.Fatalf("exit transition not synchronized: ctx=%+v options=%q engine=%q", toolCtx, toolCtx.Options.PermissionMode, eng.PermissionMode())
	}

	executeToolCall(context.Background(), params, nil, toolCtx, toolCallWithArgs("write", "Write", `{"file_path":"write.txt","content":"ok"}`), nil)
	if executed != 3 {
		t.Fatalf("executed tools = %d, want EnterPlanMode, ExitPlanMode, and final Write", executed)
	}
}

func TestDeniedExitPlanModeKeepsPlanMode(t *testing.T) {
	reg := tools.NewRegistry()
	tools.RegisterDefaults(reg)
	eng := NewQueryEngine(QueryEngineConfig{CWD: t.TempDir(), PermissionMode: permission.ModePlan})
	turnID := "turn-denied-exit"
	if _, err := eng.beginPlanTurn(turnID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.endPlanTurn(turnID) })
	toolCtx := &ToolUseContext{PlanMode: true, Options: &ToolUseOptions{PermissionMode: permission.ModePlan}}
	params := QueryParams{
		ToolRegistry: reg,
		TransitionPermissionMode: func(
			current *ToolUseContext,
			mode permission.Mode,
			requestID string,
		) (*ToolUseContext, func(), error) {
			return eng.transitionPermissionModeForTurn(
				turnID,
				nil,
				current,
				mode,
				requestID,
			)
		},
		CanUseTool: func(context.Context, string, map[string]any, *ToolUseContext) (bool, string) {
			return false, "denied"
		},
		ToolExecutor: func(context.Context, string, string) (string, error) {
			t.Fatal("denied ExitPlanMode must not execute")
			return "", nil
		},
	}

	outcome := executeToolCall(context.Background(), params, nil, toolCtx, toolCall("exit", "ExitPlanMode"), nil)
	if outcome.ContextModifier != nil {
		t.Fatal("denied ExitPlanMode must not transition mode")
		return
	}
	if !toolCtx.PlanMode || eng.PermissionMode() != permission.ModePlan {
		t.Fatalf("denied exit changed mode: ctx=%v engine=%q", toolCtx.PlanMode, eng.PermissionMode())
	}
}

type planModeSequenceModel struct{ turn int }

func (m *planModeSequenceModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *planModeSequenceModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.turn++
	names := []string{"EnterPlanMode", "Write", "ExitPlanMode", "Write"}
	if m.turn > len(names) {
		return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "done"}}), nil
	}
	args := `{}`
	if names[m.turn-1] == "Write" {
		args = `{"file_path":"write.txt","content":"ok"}`
	}
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			ID:       names[m.turn-1],
			Type:     "function",
			Function: schema.FunctionCall{Name: names[m.turn-1], Arguments: args},
		}},
	}}), nil
}

func TestStreamingPlanModeTransitionsAffectSameQuery(t *testing.T) {
	reg := tools.NewRegistry()
	tools.RegisterDefaults(reg)
	toolCtx := &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeDefault}}
	mode := permission.ModeDefault
	var executed []string
	maxTurns := 6

	terminal := Query(context.Background(), QueryParams{
		Messages:       []*schema.Message{{Role: schema.User, Content: "plan then implement"}},
		SystemPrompt:   &schema.Message{Role: schema.System, Content: "test"},
		QuerySource:    QuerySourceSDK,
		MaxTurns:       &maxTurns,
		ChatModel:      &planModeSequenceModel{},
		ToolRegistry:   reg,
		ToolUseContext: toolCtx,
		CanUseTool: approvePlanExitForTest(
			func(context.Context, string, map[string]any, *ToolUseContext) (bool, string) {
				return true, ""
			},
			permission.ModeDefault,
		),
		TransitionPermissionMode: func(
			ctx *ToolUseContext,
			next permission.Mode,
			_ string,
		) (*ToolUseContext, func(), error) {
			mode = next
			applyPermissionModeToToolContext(ctx, next)
			return ctx, nil, nil
		},
		ToolExecutor: func(_ context.Context, name, _ string) (string, error) {
			executed = append(executed, name)
			return "ok", nil
		},
	}, func(QueryEvent) {})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("terminal = %q, want completed", terminal.Reason)
	}
	want := []string{"EnterPlanMode", "ExitPlanMode", "Write"}
	if len(executed) != len(want) {
		t.Fatalf("executed = %v, want %v", executed, want)
	}
	for i := range want {
		if executed[i] != want[i] {
			t.Fatalf("executed = %v, want %v", executed, want)
		}
	}
	if mode != permission.ModeDefault || toolCtx.PlanMode || toolCtx.Options.PermissionMode != permission.ModeDefault {
		t.Fatalf("final mode not synchronized: mode=%q ctx=%v options=%q", mode, toolCtx.PlanMode, toolCtx.Options.PermissionMode)
	}
}

func toolCall(id, name string) *schema.ToolCall {
	return toolCallWithArgs(id, name, `{}`)
}

func toolCallWithArgs(id, name, args string) *schema.ToolCall {
	return &schema.ToolCall{ID: id, Type: "function", Function: schema.FunctionCall{Name: name, Arguments: args}}
}

func approvePlanExitForTest(
	next CanUseToolFn,
	target permission.Mode,
) CanUseToolFn {
	return func(
		ctx context.Context,
		toolName string,
		input map[string]any,
		toolCtx *ToolUseContext,
	) (bool, string) {
		if toolName == "ExitPlanMode" {
			SetPlanApprovalDecision(ctx, &PlanApprovalDecision{
				RequestID:    ToolUseIDFromContext(ctx),
				PlanRevision: 1,
				Outcome:      PlanApprovalApprove,
				Confirmed:    true,
				TargetMode:   target,
				settled:      true,
			})
		}
		if next == nil {
			return true, ""
		}
		return next(ctx, toolName, input, toolCtx)
	}
}
