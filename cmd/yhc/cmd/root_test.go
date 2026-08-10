package cmd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/config"
	"github.com/abietic/yhc/engine/containment"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/internal/identity"
	"github.com/abietic/yhc/tools"
)

func TestSandboxFlagExplicitCaptureAndSelection(t *testing.T) {
	command := &cobra.Command{}
	flags := runtimeFlags{}
	bindRuntimeFlags(command.Flags(), &flags)
	if err := command.Flags().Set("sandbox", "danger-full-access"); err != nil {
		t.Fatal(err)
	}
	flags.captureExplicit(command)
	if !flags.sandboxSet || flags.sandbox != "danger-full-access" {
		t.Fatalf("sandbox flags = %#v", flags)
	}
	selection, err := resolveSandboxSelection(flags, &config.Config{Sandbox: &config.SandboxConfig{GuestProfile: "workspace-write"}})
	if err != nil || selection.GuestProfile != "danger-full-access" || selection.Source != config.SandboxSelectionCLI {
		t.Fatalf("sandbox selection = %#v, %v", selection, err)
	}
	engineSelection, err := resolveEngineSandboxSelection(flags, &config.Config{Sandbox: &config.SandboxConfig{GuestProfile: "workspace-write"}})
	if err != nil || engineSelection.Profile != containment.ProfileDangerFullAccess || engineSelection.Source != containment.SelectionCLI {
		t.Fatalf("engine sandbox selection = %#v, %v", engineSelection, err)
	}
}

func TestP49GoalCapabilityDefaultsEnabled(t *testing.T) {
	capability := goalCapabilityConfig(nil)
	if capability == nil || !capability.Enabled || capability.DefaultTokenBudget != nil {
		t.Fatalf("nil config Goal capability = %#v", capability)
	}

	disabled := false
	capability = goalCapabilityConfig(&config.Config{
		Goal: &config.GoalConfig{Enabled: &disabled},
	})
	if capability.Enabled {
		t.Fatalf("explicitly disabled Goal capability = %#v", capability)
	}
}

func TestPlainPermissionPromptUsesTruthfulGrantScopes(t *testing.T) {
	tests := []struct {
		input string
		want  engine.PermissionInteractionDecision
	}{
		{input: "y\n", want: engine.PermissionAllowOnce},
		{input: "s\n", want: engine.PermissionAllowSession},
		{input: "a\n", want: engine.PermissionAllowAlways},
		{input: "n\n", want: engine.PermissionDeny},
	}
	for _, test := range tests {
		var output bytes.Buffer
		prompt := makePlainPermissionPrompt(
			newPlainInputBroker(bufio.NewReader(strings.NewReader(test.input))),
			&output,
		)
		result := prompt(context.Background(), engine.PermissionPromptRequest{
			ToolName: "Bash",
			Input:    map[string]any{"command": "make test"},
		})
		if result.Decision != test.want {
			t.Fatalf("input %q decision = %q, want %q", test.input, result.Decision, test.want)
		}
		for _, label := range []string{"once", "session", "always", "deny"} {
			if !strings.Contains(output.String(), label) {
				t.Fatalf("prompt %q missing scope label %q", output.String(), label)
			}
		}
	}
}

func TestPlainPlanApprovalReturnsExactStructuredTarget(t *testing.T) {
	tests := []struct {
		input     string
		allowed   bool
		confirmed bool
		target    permission.Mode
		outcome   engine.PlanApprovalOutcome
		feedback  string
	}{
		{input: "m\n", allowed: true, target: permission.ModeDontAsk, outcome: engine.PlanApprovalApprove},
		{input: "e\n", allowed: true, target: permission.ModeAcceptEdits, outcome: engine.PlanApprovalApprove},
		{input: "b\nBYPASS\n", allowed: true, confirmed: true, target: permission.ModeBypassPermissions, outcome: engine.PlanApprovalApprove},
		{input: "r add rollback\n", target: permission.ModePlan, outcome: engine.PlanApprovalRevise, feedback: "add rollback"},
		{input: "n\n", target: permission.ModePlan, outcome: engine.PlanApprovalCancel},
	}
	for _, test := range tests {
		t.Run(strings.TrimSpace(test.input), func(t *testing.T) {
			planPath := filepath.Join(t.TempDir(), "plan.md")
			if err := os.WriteFile(
				planPath,
				[]byte("# Reviewed Plan"),
				0o600,
			); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			prompt := makePlainPermissionPrompt(
				newPlainInputBroker(bufio.NewReader(strings.NewReader(test.input))),
				&output,
			)
			result := prompt(context.Background(), engine.PermissionPromptRequest{
				ToolName: "ExitPlanMode",
				PlanApproval: &engine.PlanApprovalRequest{
					RequestID:        "plan-1",
					PlanRevision:     7,
					PlanFileIdentity: planPath,
					ReturnMode:       permission.ModeDontAsk,
				},
			})
			if result.PlanApproval == nil ||
				result.PlanApproval.RequestID != "plan-1" ||
				result.PlanApproval.PlanRevision != 7 ||
				result.PlanApproval.Approved ||
				result.PlanApproval.Confirmed != test.confirmed ||
				result.PlanApproval.TargetMode != test.target ||
				result.PlanApproval.Outcome != test.outcome ||
				result.PlanApproval.Feedback != test.feedback {
				t.Fatalf("input %q result = %#v", test.input, result)
			}
			wantDigest := ""
			if test.allowed {
				wantDigest = engine.PlanBytesDigest([]byte("# Reviewed Plan"))
			}
			if result.PlanApproval.ReviewedPlanDigest != wantDigest {
				t.Fatalf(
					"input %q reviewed digest = %q, want %q",
					test.input,
					result.PlanApproval.ReviewedPlanDigest,
					wantDigest,
				)
			}
			if test.allowed && result.Decision != engine.PermissionAllowOnce {
				t.Fatalf("input %q decision = %q", test.input, result.Decision)
			}
			if !test.allowed && result.Decision != engine.PermissionDeny {
				t.Fatalf("input %q decision = %q", test.input, result.Decision)
			}
			for _, label := range []string{"previous permissions", "auto-accept edits", "bypass permissions", "revise", "cancel"} {
				if !strings.Contains(output.String(), label) {
					t.Fatalf("prompt %q missing %q", output.String(), label)
				}
			}
			if !strings.Contains(output.String(), "# Reviewed Plan") {
				t.Fatalf("prompt did not render Plan bytes: %q", output.String())
			}
			if !strings.Contains(output.String(), "previous permissions (dontAsk)") {
				t.Fatalf("prompt did not expose exact return mode: %q", output.String())
			}
			if strings.Contains(output.String(), "always") || strings.Contains(output.String(), "session") {
				t.Fatalf("plan prompt exposed generic grant scopes: %q", output.String())
			}
		})
	}
}

func TestPlainPlanBypassBackAndWrongTokenLoop(t *testing.T) {
	planPath := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planPath, []byte("# Plan"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, input string
		mode, want  permission.Mode
		confirmed   bool
	}{
		{"back_then_edits", "b\nn\ne\n", permission.ModeDefault, permission.ModeAcceptEdits, false},
		{"wrong_then_confirm", "b\nwrong\nBYPASS\n", permission.ModeDefault, permission.ModeBypassPermissions, true},
		{"previous_bypass", "p\nBYPASS\n", permission.ModeBypassPermissions, permission.ModeBypassPermissions, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			got := readPlainPlanApproval(context.Background(), newPlainInputBroker(bufio.NewReader(strings.NewReader(test.input))), &output, &engine.PlanApprovalRequest{RequestID: "r", PlanRevision: 1, PlanFileIdentity: planPath, ReturnMode: test.mode})
			if got.Decision != engine.PermissionAllowOnce || got.PlanApproval == nil || got.PlanApproval.TargetMode != test.want || got.PlanApproval.Confirmed != test.confirmed {
				t.Fatalf("result = %#v", got)
			}
			if test.mode == permission.ModeBypassPermissions && strings.Contains(output.String(), "[b] bypass") {
				t.Fatalf("duplicate bypass target prompt: %q", output.String())
			}
		})
	}
}

func TestPlainPlanEOFEmitsTypedCancel(t *testing.T) {
	planPath := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planPath, []byte("# Plan"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := readPlainPlanApproval(context.Background(), newPlainInputBroker(bufio.NewReader(strings.NewReader(""))), io.Discard, &engine.PlanApprovalRequest{RequestID: "r", PlanRevision: 1, PlanFileIdentity: planPath})
	if got.Decision != engine.PermissionDeny || got.PlanApproval == nil || got.PlanApproval.Outcome != engine.PlanApprovalCancel {
		t.Fatalf("EOF result = %#v", got)
	}
}

func TestDrivePlainQueryEventsResolvesProjectGraphPlanApproval(t *testing.T) {
	eng, executions := newPlainPlanTestEngine(t, "plain-plan-live")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	prompt := makePlainPermissionPrompt(
		newPlainInputBroker(bufio.NewReader(strings.NewReader("p\n"))),
		&stdout,
	)
	events, _ := eng.SubmitMessage(context.Background(), "review Plan")
	if err := drivePlainQueryEvents(
		context.Background(),
		eng,
		prompt,
		&stdout,
		&stderr,
		events,
	); err != nil {
		t.Fatal(err)
	}
	if executions.Load() != 1 ||
		eng.PlanState().Phase != engine.PlanPhaseInactive ||
		eng.PermissionMode() != permission.ModeDefault ||
		eng.GetApprovalTracker().Count() != 0 {
		t.Fatalf(
			"plain Plan result executions=%d state=%#v mode=%q grants=%d",
			executions.Load(),
			eng.PlanState(),
			eng.PermissionMode(),
			eng.GetApprovalTracker().Count(),
		)
	}
	if !strings.Contains(stdout.String(), "# Plain Plan") ||
		!strings.Contains(stdout.String(), "[ExitPlanMode] exited") {
		t.Fatalf("plain output = %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "[session ended: completed]") {
		t.Fatalf("plain stderr = %q", stderr.String())
	}
}

func TestDrivePlainQueryEventsProjectsPermissionReviewStatus(t *testing.T) {
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:     "plain-review-status",
		TranscriptDir: t.TempDir(),
		CWD:           t.TempDir(),
	})
	defer eng.Close()
	events := make(chan engine.QueryEvent, 2)
	events <- engine.QueryEvent{
		Type: engine.EventPermissionReview,
		PermissionReview: &engine.PermissionReviewEvent{
			Phase:         engine.PermissionReviewUnavailable,
			CanonicalTool: "Write",
			ReasonCode:    "timeout",
		},
	}
	events <- engine.QueryEvent{
		Type: engine.EventTerminal,
		TerminalInfo: &engine.Terminal{
			Reason: engine.TerminalCompleted,
		},
	}
	close(events)

	var stderr bytes.Buffer
	if err := drivePlainQueryEvents(
		context.Background(),
		eng,
		nil,
		io.Discard,
		&stderr,
		events,
	); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(
		stderr.String(),
		"[permission review shadow unavailable: Write timeout]",
	) {
		t.Fatalf("plain permission review status = %q", stderr.String())
	}
}

func TestP462DrivePlainQueryEventsWritesFallbackNoticeOnlyToStderr(
	t *testing.T,
) {
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:     "plain-fallback",
		TranscriptDir: t.TempDir(),
		CWD:           t.TempDir(),
	})
	t.Cleanup(eng.Close)
	events := make(chan engine.QueryEvent, 5)
	events <- engine.QueryEvent{
		Type: engine.EventModelAttempt,
		ModelAttempt: &engine.ModelAttemptEvent{
			AttemptID: "primary", Profile: "primary",
			Phase: engine.ModelAttemptStarted,
		},
	}
	events <- engine.QueryEvent{
		Type: engine.EventModelAttempt,
		ModelAttempt: &engine.ModelAttemptEvent{
			AttemptID: "primary", AttemptIndex: 0, Profile: "primary",
			Phase: engine.ModelAttemptDiscarded, SwitchCount: 0,
		},
	}
	events <- engine.QueryEvent{
		Type: engine.EventModelAttempt,
		ModelAttempt: &engine.ModelAttemptEvent{
			AttemptID: "alternate", AttemptIndex: 1,
			Profile: "fallback.profile", Phase: engine.ModelAttemptStarted,
			SwitchCount: 1, APIModel: "secret-api-model",
		},
	}
	events <- engine.QueryEvent{
		Type:             engine.EventAssistant,
		AssistantMessage: &schema.Message{Content: "assistant output"},
	}
	events <- engine.QueryEvent{
		Type: engine.EventTerminal,
		TerminalInfo: &engine.Terminal{
			Reason: engine.TerminalCompleted,
		},
	}
	close(events)

	var stdout, stderr bytes.Buffer
	if err := drivePlainQueryEvents(
		context.Background(), eng, nil, &stdout, &stderr, events,
	); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "assistant output" {
		t.Fatalf("plain stdout = %q", stdout.String())
	}
	const notice = "Model fallback: profile fallback.profile after overload (switch 1)"
	if strings.Count(stderr.String(), notice) != 1 {
		t.Fatalf("plain fallback stderr = %q", stderr.String())
	}
	if strings.Contains(stdout.String(), notice) ||
		strings.Contains(stdout.String()+stderr.String(), "secret-api-model") {
		t.Fatalf("plain fallback crossed output boundary: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestP245aDrivePlainQueryEventsHasStableGoalAttribution(t *testing.T) {
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:     "plain-goal-output",
		ThreadID:      "plain-goal-output-thread",
		TranscriptDir: t.TempDir(),
		CWD:           t.TempDir(),
	})
	t.Cleanup(eng.Close)
	events := make(chan engine.QueryEvent, 3)
	events <- engine.QueryEvent{
		Type: engine.EventGoalLifecycle,
		GoalLifecycle: &engine.GoalLifecycleEvent{
			Phase: engine.GoalLifecycleTurnStarted,
			Goal: engine.GoalSnapshot{
				Status:              "active",
				Revision:            7,
				TokensUsed:          321,
				ContinuationOrdinal: 4,
			},
		},
	}
	events <- engine.QueryEvent{
		Type:             engine.EventAssistant,
		AssistantMessage: &schema.Message{Content: "continuation answer"},
	}
	events <- engine.QueryEvent{
		Type: engine.EventTerminal,
		TerminalInfo: &engine.Terminal{
			Reason: engine.TerminalCompleted,
		},
	}
	close(events)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := drivePlainQueryEvents(
		context.Background(),
		eng,
		nil,
		&stdout,
		&stderr,
		events,
	); err != nil {
		t.Fatal(err)
	}
	const wantStdout = "\n[Goal turn_started] status=active revision=7 tokens=321 continuation=4\ncontinuation answer"
	if stdout.String() != wantStdout {
		t.Fatalf("Plain Goal stdout = %q, want %q", stdout.String(), wantStdout)
	}
	if stderr.String() != "\n[session ended: completed]\n" {
		t.Fatalf("Plain Goal stderr = %q", stderr.String())
	}
}

func TestP245aPlainCompletedInputSupersedesGoalWakeBeforeClaim(t *testing.T) {
	budget := uint64(10_000)
	model := &headlessRecoveryModel{responses: []*schema.Message{{
		Role:    schema.Assistant,
		Content: "first Goal step",
		ResponseMeta: &schema.ResponseMeta{
			Usage: &schema.TokenUsage{
				PromptTokens:     5,
				CompletionTokens: 3,
				TotalTokens:      8,
			},
		},
	}}}
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	root := t.TempDir()
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:         "plain-goal-precedence",
		ThreadID:          "plain-goal-precedence-thread",
		CWD:               root,
		TranscriptDir:     filepath.Join(root, "transcripts"),
		CommandEntrypoint: commands.EntrypointPlain,
		GoalCapability: &engine.GoalCapabilityConfig{
			Enabled:            true,
			DefaultTokenBudget: &budget,
		},
		ChatModel:     model,
		ToolRegistry:  registry,
		ToolSelection: &tools.ToolSelection{},
		MaxTurns:      2,
	})
	t.Cleanup(eng.Close)
	input := newPlainInputBroker(bufio.NewReader(strings.NewReader(
		"/goal finish the Plain consumer\n/exit\n",
	)))
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	prompt := makePlainPermissionPrompt(input, &stdout)
	if err := drivePlainREPL(
		context.Background(),
		eng,
		eng.GetCommandRegistry(),
		input,
		prompt,
		&stdout,
		&stderr,
	); err != nil {
		t.Fatal(err)
	}
	if model.calls() != 1 {
		t.Fatalf("model calls = %d, want one user-admitted Goal turn", model.calls())
	}
	if strings.Contains(stdout.String(), "[Goal continuation]") {
		t.Fatalf("completed /exit lost precedence over Goal wake: %q", stdout.String())
	}
	goal, ok := eng.GoalSnapshot()
	if !ok ||
		goal.Status != "paused" ||
		goal.StatusReasonCode != "user-cancelled" {
		t.Fatalf("Goal after Plain exit = %#v, available=%v", goal, ok)
	}
	for _, item := range eng.RuntimeItems() {
		if item.Kind == engine.RuntimeItemGoalContinuation {
			t.Fatalf("Plain exit retained an unadmitted Goal item: %#v", item)
		}
	}
}

func TestP245aPlainEventuallyConsumesEligibleGoalWake(t *testing.T) {
	budget := uint64(10_000)
	modelCalls := make(chan int, 4)
	model := &signalingPlainGoalModel{
		headlessRecoveryModel: &headlessRecoveryModel{
			responses: []*schema.Message{
				{
					Role:    schema.Assistant,
					Content: "first Goal step",
					ResponseMeta: &schema.ResponseMeta{
						Usage: &schema.TokenUsage{TotalTokens: 8},
					},
				},
				{
					Role: schema.Assistant,
					ToolCalls: []schema.ToolCall{{
						ID:   "plain-goal-complete",
						Type: "function",
						Function: schema.FunctionCall{
							Name:      tools.UpdateGoalToolName,
							Arguments: `{"status":"complete"}`,
						},
					}},
					ResponseMeta: &schema.ResponseMeta{
						FinishReason: "tool_calls",
						Usage:        &schema.TokenUsage{TotalTokens: 4},
					},
				},
				{
					Role:    schema.Assistant,
					Content: "Goal complete",
					ResponseMeta: &schema.ResponseMeta{
						Usage: &schema.TokenUsage{TotalTokens: 3},
					},
				},
			},
		},
		calls: modelCalls,
	}
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	root := t.TempDir()
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:         "plain-goal-eventual",
		ThreadID:          "plain-goal-eventual-thread",
		CWD:               root,
		TranscriptDir:     filepath.Join(root, "transcripts"),
		CommandEntrypoint: commands.EntrypointPlain,
		GoalCapability: &engine.GoalCapabilityConfig{
			Enabled:            true,
			DefaultTokenBudget: &budget,
		},
		ChatModel:    model,
		ToolRegistry: registry,
		MaxTurns:     4,
	})
	t.Cleanup(eng.Close)
	reader, writer := io.Pipe()
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	input := newPlainInputBroker(bufio.NewReader(reader))
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	prompt := makePlainPermissionPrompt(input, &stdout)
	result := make(chan error, 1)
	go func() {
		result <- drivePlainREPL(
			context.Background(),
			eng,
			eng.GetCommandRegistry(),
			input,
			prompt,
			&stdout,
			&stderr,
		)
	}()
	if _, err := io.WriteString(
		writer,
		"/goal finish the automatic Plain continuation\n",
	); err != nil {
		t.Fatal(err)
	}
	for want := 1; want <= 3; want++ {
		select {
		case got := <-modelCalls:
			if got != want {
				t.Fatalf("model call signal = %d, want %d", got, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("model call %d was not reached", want)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Plain Goal driver did not terminate after EOF")
	}
	if count := strings.Count(stdout.String(), "[Goal continuation]"); count != 1 {
		t.Fatalf("Goal continuation attribution count = %d, output=%q", count, stdout.String())
	}
	goal, ok := eng.GoalSnapshot()
	if !ok || goal.Status != "complete" {
		t.Fatalf("completed Plain Goal = %#v, available=%v", goal, ok)
	}
	if items := eng.RuntimeItems(); len(items) != 0 {
		t.Fatalf("completed Plain Goal retained runtime items: %#v", items)
	}
}

func TestP245aPlainContextCancellationUsesStopOwner(t *testing.T) {
	root := t.TempDir()
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:         "plain-goal-context-cancel",
		ThreadID:          "plain-goal-context-cancel-thread",
		CWD:               root,
		TranscriptDir:     filepath.Join(root, "transcripts"),
		CommandEntrypoint: commands.EntrypointPlain,
		GoalCapability:    &engine.GoalCapabilityConfig{Enabled: true},
	})
	t.Cleanup(eng.Close)
	for _, command := range []string{
		"/goal finish cancellation closeout",
		"/goal budget 10000",
		"/goal resume",
	} {
		outcome, err := runPlainEngineCommand(context.Background(), eng, command)
		if err != nil || outcome.Status != engine.CommandResultSucceeded {
			t.Fatalf("prepare Plain Goal with %q: outcome=%#v err=%v", command, outcome, err)
		}
	}
	reader, writer := io.Pipe()
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	input := newPlainInputBroker(bufio.NewReader(reader))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := drivePlainREPL(
		ctx,
		eng,
		eng.GetCommandRegistry(),
		input,
		nil,
		io.Discard,
		io.Discard,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Plain cancellation error = %v", err)
	}
	goal, available := eng.GoalSnapshot()
	if !available ||
		goal.Status != "paused" ||
		goal.StatusReasonCode != "user-cancelled" {
		t.Fatalf("Plain cancellation did not pause Goal: %#v", goal)
	}
}

type signalingPlainGoalModel struct {
	*headlessRecoveryModel
	calls chan<- int
}

func (m *signalingPlainGoalModel) Generate(
	ctx context.Context,
	messages []*schema.Message,
	options ...model.Option,
) (*schema.Message, error) {
	response, err := m.headlessRecoveryModel.Generate(ctx, messages, options...)
	m.calls <- m.headlessRecoveryModel.calls()
	return response, err
}

func (m *signalingPlainGoalModel) Stream(
	ctx context.Context,
	messages []*schema.Message,
	options ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	response, err := m.headlessRecoveryModel.Stream(ctx, messages, options...)
	m.calls <- m.headlessRecoveryModel.calls()
	return response, err
}

func TestDrivePlainPendingProjectGraphPlanCancellation(t *testing.T) {
	eng, executions := newPlainPlanTestEngine(t, "plain-plan-pending")
	events, _ := eng.SubmitMessage(context.Background(), "review Plan")
	for range events {
	}
	if pending, ok := eng.PendingProjectGraphPermissionRequest(); !ok ||
		pending.PlanApproval == nil {
		t.Fatalf("pending plain Plan request = %#v, %v", pending, ok)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	prompt := makePlainPermissionPrompt(
		newPlainInputBroker(bufio.NewReader(strings.NewReader("n\n"))),
		&stdout,
	)
	if err := drivePlainPendingProjectGraphPermission(
		context.Background(),
		eng,
		prompt,
		&stdout,
		&stderr,
	); err != nil {
		t.Fatal(err)
	}
	if executions.Load() != 0 ||
		eng.PlanState().Phase != engine.PlanPhaseActive ||
		eng.PermissionMode() != permission.ModePlan ||
		eng.GetApprovalTracker().Count() != 0 {
		t.Fatalf(
			"plain pending cancel executions=%d state=%#v mode=%q grants=%d",
			executions.Load(),
			eng.PlanState(),
			eng.PermissionMode(),
			eng.GetApprovalTracker().Count(),
		)
	}
	if !strings.Contains(stdout.String(), "# Plain Plan") {
		t.Fatalf("plain pending prompt = %q", stdout.String())
	}
}

func newPlainPlanTestEngine(
	t *testing.T,
	sessionID string,
) (*engine.QueryEngine, *atomic.Int64) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	if err := tools.SavePlan(sessionID, "", "# Plain Plan\n"); err != nil {
		t.Fatal(err)
	}
	executions := &atomic.Int64{}
	registry := tools.NewRegistry()
	registry.Register(tools.ToolImpl{
		Info:                 &schema.ToolInfo{Name: "ExitPlanMode"},
		IsPlanModeTransition: true,
		Execute: func(string) (string, error) {
			executions.Add(1)
			return "exited", nil
		},
	})
	root := t.TempDir()
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:      sessionID,
		ThreadID:       sessionID + "-thread",
		TranscriptDir:  filepath.Join(root, "transcripts"),
		CWD:            root,
		PermissionMode: permission.ModePlan,
		ChatModel: &headlessRecoveryModel{responses: []*schema.Message{{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:   sessionID + "-exit",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "ExitPlanMode",
					Arguments: `{}`,
				},
			}},
		}}},
		ToolRegistry:  registry,
		ToolSelection: &tools.ToolSelection{Names: []string{"ExitPlanMode"}},
		MaxTurns:      2,
		PermissionPrompt: func(
			context.Context,
			engine.PermissionPromptRequest,
		) engine.PermissionInteractionResult {
			t.Fatal("ProjectGraph called the blocking plain Plan adapter")
			return engine.PermissionInteractionResult{
				Decision: engine.PermissionDeny,
			}
		},
	})
	t.Cleanup(eng.Close)
	return eng, executions
}

func TestPlainPlanApprovalContextCancellationEmitsTypedCancel(t *testing.T) {
	reader, input := io.Pipe()
	t.Cleanup(func() {
		_ = reader.Close()
		_ = input.Close()
	})
	planPath := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planPath, []byte("# Plan\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writer := newSignalingWriter()
	prompt := makePlainPermissionPrompt(
		newPlainInputBroker(bufio.NewReader(reader)),
		writer,
	)
	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan engine.PermissionInteractionResult, 1)
	go func() {
		resultCh <- prompt(ctx, engine.PermissionPromptRequest{
			ToolName: "ExitPlanMode",
			PlanApproval: &engine.PlanApprovalRequest{
				RequestID: "plain-plan-cancel", PlanRevision: 9,
				PlanFileIdentity: planPath,
			},
		})
	}()
	<-writer.firstWrite
	cancel()
	select {
	case result := <-resultCh:
		if result.Decision != engine.PermissionCancelled ||
			result.PlanApproval == nil ||
			result.PlanApproval.RequestID != "plain-plan-cancel" ||
			result.PlanApproval.PlanRevision != 9 ||
			result.PlanApproval.Outcome != engine.PlanApprovalCancel ||
			result.PlanApproval.Approved ||
			result.PlanApproval.TargetMode != permission.ModePlan {
			t.Fatalf("plain Plan cancellation = %#v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("plain Plan prompt did not observe context cancellation")
	}
}

func TestPlainPermissionPromptCancelsQueuedFollowerWithoutSecondPrompt(t *testing.T) {
	reader, input := io.Pipe()
	t.Cleanup(func() {
		_ = reader.Close()
		_ = input.Close()
	})
	writer := newSignalingWriter()
	prompt := makePlainPermissionPrompt(
		newPlainInputBroker(bufio.NewReader(reader)),
		writer,
	)
	sourceResult := make(chan engine.PermissionInteractionResult, 1)
	go func() {
		sourceResult <- prompt(context.Background(), engine.PermissionPromptRequest{
			ToolName: "Bash", Input: map[string]any{"command": "npm test"},
		})
	}()
	<-writer.firstWrite

	followerCtx, cancelFollower := context.WithCancel(context.Background())
	followerResult := make(chan engine.PermissionInteractionResult, 1)
	go func() {
		followerResult <- prompt(followerCtx, engine.PermissionPromptRequest{
			ToolName: "Bash", Input: map[string]any{"command": "npm run lint"},
		})
	}()
	cancelFollower()
	select {
	case result := <-followerResult:
		if result.Decision != engine.PermissionCancelled {
			t.Fatalf("follower result = %#v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued follower did not observe cancellation")
	}

	if _, err := io.WriteString(input, "a\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-sourceResult:
		if result.Decision != engine.PermissionAllowAlways {
			t.Fatalf("source result = %#v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("source prompt did not resolve")
	}
	if got := strings.Count(writer.String(), "Allow Bash"); got != 1 {
		t.Fatalf("prompt count = %d, output %q", got, writer.String())
	}
}

type signalingWriter struct {
	mu         sync.Mutex
	buffer     bytes.Buffer
	firstWrite chan struct{}
	once       sync.Once
}

func newSignalingWriter() *signalingWriter {
	return &signalingWriter{firstWrite: make(chan struct{})}
}

func (w *signalingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	written, err := w.buffer.Write(p)
	w.mu.Unlock()
	w.once.Do(func() { close(w.firstWrite) })
	return written, err
}

func (w *signalingWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}

func TestRuntimeEnvironmentCanonicalWinsLegacy(t *testing.T) {
	names := []identity.RuntimeEnvName{
		identity.RuntimeEnvAccessibility,
		identity.RuntimeEnvReducedMotion,
		identity.RuntimeEnvProviderPreflight,
		identity.RuntimeEnvSimple,
	}
	tests := []struct {
		name      string
		canonical *string
		legacy    *string
		want      bool
	}{
		{name: "canonical only", canonical: environmentValue("true"), want: true},
		{name: "legacy only", legacy: environmentValue("true"), want: true},
		{name: "both prefer canonical", canonical: environmentValue("on"), legacy: environmentValue("false"), want: true},
		{name: "present empty canonical blocks legacy", canonical: environmentValue(""), legacy: environmentValue("true")},
		{name: "invalid canonical blocks legacy", canonical: environmentValue("invalid"), legacy: environmentValue("true")},
		{name: "neither"},
	}
	for _, name := range names {
		t.Run(string(name), func(t *testing.T) {
			pair := name.Pair()
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					setOptionalEnvironment(t, pair.Canonical, test.canonical)
					setOptionalEnvironment(t, pair.Legacy, test.legacy)
					if got := envFlagEnabled(name); got != test.want {
						t.Fatalf("envFlagEnabled(%s) = %t, want %t", name, got, test.want)
					}
				})
			}
		})
	}
}

func environmentValue(value string) *string { return &value }

func setOptionalEnvironment(t *testing.T, name string, value *string) {
	t.Helper()
	old, present := os.LookupEnv(name)
	t.Cleanup(func() {
		if present {
			_ = os.Setenv(name, old)
			return
		}
		_ = os.Unsetenv(name)
	})
	if value == nil {
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := os.Setenv(name, *value); err != nil {
		t.Fatal(err)
	}
}

func TestToolsFlagPreservesExplicitEmptySelection(t *testing.T) {
	flags := pflag.NewFlagSet("tools", pflag.ContinueOnError)
	var values []string
	flags.StringSliceVar(&values, "tools", nil, "")
	if err := flags.Parse([]string{"--tools="}); err != nil {
		t.Fatal(err)
	}
	if !flags.Changed("tools") {
		t.Fatal("expected explicit empty --tools flag to be marked changed")
	}
	selection := tools.ParseToolSelection(values)
	if selection.Preset != "" || len(selection.Names) != 0 {
		t.Fatalf("explicit empty --tools parsed as %#v", selection)
	}
}

func TestPlainREPLHandleAction(t *testing.T) {
	tests := []struct {
		name       string
		action     commands.CommandAction
		data       map[string]any
		wantReturn string
	}{
		{"quit returns quit", commands.ActionQuit, nil, "quit"},
		{"prompt returns prompt", commands.ActionPrompt, nil, "prompt"},
		{"export returns empty", commands.ActionExport, nil, ""},
		{"toggle_vim returns empty", commands.ActionToggleVim, nil, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := &commands.CommandResult{
				Action: tt.action,
				Output: "test output",
				Data:   tt.data,
			}
			got := plainREPLHandleAction(result, io.Discard)
			if got != tt.wantReturn {
				t.Errorf("plainREPLHandleAction(%s) = %q, want %q", tt.action, got, tt.wantReturn)
			}
		})
	}
}

func TestPlainCommandRunsThroughEngine(t *testing.T) {
	registry := commands.NewRegistry()
	commands.RegisterDefaults(registry)

	tests := []struct {
		input string
		want  bool
	}{
		{input: "/clear", want: true},
		{input: "/reset", want: true},
		{input: "/new", want: true},
		{input: "/compact preserve decisions", want: true},
		{input: "/model test-model", want: true},
		{input: "/plan", want: true},
		{input: "/effort high", want: true},
		{input: "/add-dir /tmp", want: true},
		{input: "/fork branch-name", want: true},
		{input: "/sessions rename current session-name", want: true},
		{input: "/sessions list", want: true},
		{input: "/help", want: false},
		{input: "/history", want: false},
		{input: "/rename session-name", want: false},
		{input: "/export", want: false},
		{input: "/unknown", want: false},
	}
	for _, test := range tests {
		if got := plainCommandRunsThroughEngine(registry, test.input); got != test.want {
			t.Fatalf("plainCommandRunsThroughEngine(%q) = %v, want %v", test.input, got, test.want)
		}
	}
}

func TestRunPlainEngineCommandClearsCanonicalState(t *testing.T) {
	dir := t.TempDir()
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:     "plain-clear",
		CWD:           dir,
		TranscriptDir: filepath.Join(dir, "transcripts"),
	})
	t.Cleanup(eng.Close)
	eng.SetResumedMessages([]*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "hi"},
	})

	outcome, err := runPlainEngineCommand(context.Background(), eng, "/clear")
	if err != nil {
		t.Fatalf("run clear: %v", err)
	}
	if outcome.Output != "Cleared 2 message(s) (~19 tokens)." {
		t.Fatalf("clear output = %q", outcome.Output)
	}
	if got := len(eng.GetMessages()); got != 0 {
		t.Fatalf("message count after clear = %d", got)
	}
}

func TestRunPlainEngineCommandCompactsCanonicalState(t *testing.T) {
	const summary = "keep the accepted architecture decisions"
	chatModel := &headlessRecoveryModel{responses: []*schema.Message{
		{Role: schema.Assistant, Content: summary},
	}}
	dir := t.TempDir()
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:     "plain-compact",
		CWD:           dir,
		TranscriptDir: filepath.Join(dir, "transcripts"),
		ChatModel:     chatModel,
		Model:         "test-model",
	})
	t.Cleanup(eng.Close)
	eng.SetResumedMessages([]*schema.Message{
		{Role: schema.User, Content: "choose a graph design"},
		{Role: schema.Assistant, Content: "use the project-owned graph"},
		{Role: schema.User, Content: "preserve the decision"},
		{Role: schema.Assistant, Content: "accepted"},
	})

	outcome, err := runPlainEngineCommand(
		context.Background(),
		eng,
		"/compact preserve architecture decisions",
	)
	if err != nil {
		t.Fatalf("run compact: %v", err)
	}
	if chatModel.calls() != 1 {
		t.Fatalf("compact model calls = %d, want 1", chatModel.calls())
	}
	if !strings.Contains(outcome.Output, "Conversation compacted") ||
		!strings.Contains(outcome.Output, summary) {
		t.Fatalf("compact output = %q", outcome.Output)
	}
	messages := eng.GetMessages()
	if len(messages) < 2 ||
		messages[0].Extra["subtype"] != "compact_boundary" ||
		messages[1].Extra["subtype"] != "compact_summary" {
		t.Fatalf("canonical compacted messages = %#v", messages)
	}
}
