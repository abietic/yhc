package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestVerificationCoverageReviewRejectsUnsupportedPass(t *testing.T) {
	config := IndependentVerificationConfig{MaxTurns: 2, CoverageReview: true}
	usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 4})
	tool := verificationResponse("")
	tool.chunks[0].ToolCalls = []schema.ToolCall{{ID: "verify-command", Type: "function", Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"independent check"}`}}}
	mdl := &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("done"), tool, verificationResponse(verificationPass), verificationResponse(`{"verdict":"INSUFFICIENT","missing":["The required intermediate state was not established."]}`)}}
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	var saved verificationCursor
	events, terminal := collectEvents(t.Context(), QueryParams{
		Messages: []*schema.Message{schema.UserMessage("original contract")}, ChatModel: mdl,
		ToolRegistry: registry, RunUsage: usage, Deps: &QueryDeps{ProviderUsage: usage},
		ToolExecutor:             func(context.Context, string, string) (string, error) { return "prerequisite_reached=false", nil },
		IndependentVerification:  config,
		commitVerificationCursor: func(c verificationCursor) error { saved = c; return nil },
	})
	if !errors.Is(terminal.Err, ErrIndependentVerification) || usage.Snapshot().ProviderCalls != 4 || saved.Phase != "exhausted" || saved.Diagnostics == nil || saved.Diagnostics.Verdict != "PARTIAL" {
		t.Fatalf("unsupported PASS completed: terminal=%+v calls=%d phase=%s", terminal, usage.Snapshot().ProviderCalls, saved.Phase)
	}
	if saved.Repairs != 0 || len(saved.Diagnostics.Missing) != 1 || saved.Diagnostics.Checks[0].Status != "PASS" {
		t.Fatalf("coverage review invented a counterexample or repair: %+v", saved)
	}
	for _, e := range events {
		if e.Type == EventAttachment && e.AttachmentMessage != nil && strings.Contains(e.AttachmentMessage.Content, `"verdict":"PASS"`) {
			t.Fatal("provisional PASS escaped before review")
		}
	}
}

func TestVerificationCoverageReviewAdmissionAndEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, review string
		enabled      bool
		limit        int64
		calls        uint64
		failed       bool
		toolReply    bool
	}{
		{name: "disabled", limit: 4, calls: 3},
		{name: "supported", enabled: true, limit: 4, calls: 4, review: `{"verdict":"SUPPORTED","missing":[]}`},
		{name: "malformed", enabled: true, limit: 4, calls: 4, review: `{"verdict":"SUPPORTED","checks":[]}`, failed: true},
		{name: "budget-before-review", enabled: true, limit: 3, calls: 3, failed: true},
		{name: "tools-denied", enabled: true, limit: 4, calls: 4, toolReply: true, failed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: tc.limit})
			tool := verificationResponse("")
			tool.chunks[0].ToolCalls = []schema.ToolCall{{ID: "verify-command", Type: "function", Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"independent check"}`}}}
			review := verificationResponse(tc.review)
			if tc.toolReply {
				review = tool
			}
			mdl := &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("PRIVATE_SOLVER_HISTORY"), tool, verificationResponse(verificationPass), review}}
			registry := tools.NewRegistry()
			tools.RegisterDefaults(registry)
			executions, reviews := 0, 0
			var saved verificationCursor
			events, terminal := collectEvents(t.Context(), QueryParams{
				Messages: []*schema.Message{schema.UserMessage("original contract")}, ChatModel: mdl,
				ToolRegistry: registry, RunUsage: usage,
				IndependentVerification:  IndependentVerificationConfig{MaxTurns: 2, CoverageReview: tc.enabled},
				ToolExecutor:             func(context.Context, string, string) (string, error) { executions++; return "BOUND_OUTPUT", nil },
				commitVerificationCursor: func(c verificationCursor) error { saved = c; return nil },
				Deps: &QueryDeps{ProviderUsage: usage, CallModel: func(ctx context.Context, m model.BaseChatModel, messages []*schema.Message, system *schema.Message, infos []*schema.ToolInfo, opts execution.CallModelOptions) (*execution.CallModelResult, error) {
					if opts.QuerySource == "independent_verification_coverage" {
						reviews++
						if opts.ToolChoice != "none" || opts.ForcedToolName != "" || len(infos) != 0 {
							t.Errorf("coverage reviewer exposed tools: choice=%s infos=%d", opts.ToolChoice, len(infos))
						}
						joined := ""
						foundOriginal := false
						for _, msg := range messages {
							joined += msg.Content
							if strings.Contains(msg.Content, "Provisional checker report") && msg.Role != schema.Assistant {
								t.Error("untrusted report promoted to user instruction")
							}
							foundOriginal = foundOriginal || (msg.Role == schema.User && msg.Content == "original contract")
						}
						if !foundOriginal || !strings.Contains(joined, "BOUND_OUTPUT") || !strings.Contains(joined, "independent check") || strings.Contains(joined, "PRIVATE_SOLVER_HISTORY") {
							t.Error("coverage reviewer lost original requirements or bound evidence, or inherited solver history")
						}
					}
					return execution.CallModel(ctx, m, messages, system, infos, opts)
				}},
			})
			if (terminal.Err != nil) != tc.failed || usage.Snapshot().ProviderCalls != tc.calls || executions != 1 {
				t.Fatalf("terminal=%+v calls=%d executions=%d", terminal, usage.Snapshot().ProviderCalls, executions)
			}
			if tc.failed && (saved.Phase != "check" || saved.Repairs != 0) {
				t.Fatalf("failed review authorized completion or repair: %+v", saved)
			}
			if tc.enabled && reviews == 0 {
				t.Fatal("review did not traverse the production kernel")
			}
			if tc.name == "supported" {
				found := false
				for _, event := range events {
					if event.Type == EventAttachment && event.AttachmentMessage != nil {
						summary, ok := event.AttachmentMessage.Extra["verification_summary"].(IndependentVerificationSummary)
						found = ok && summary.CoverageReviews == 1 && summary.CoverageVerdict == "SUPPORTED"
					}
				}
				if !found {
					t.Fatal("coverage review summary missing")
				}
				var calls uint64
				for _, route := range usage.Snapshot().Routes {
					if route.Source == "independent_verification_coverage" {
						calls += route.ProviderCalls
					}
				}
				if calls != 1 {
					t.Fatalf("review usage not independently attributed: %d", calls)
				}
			}
		})
	}
}

func TestVerificationCoverageReviewSchemaFailsClosed(t *testing.T) {
	for _, text := range []string{
		`{"verdict":"FAIL","missing":["gap"]}`,
		`{"verdict":"SUPPORTED","missing":["gap"]}`,
		`{"verdict":"INSUFFICIENT","missing":[]}`,
		`{"verdict":"INSUFFICIENT","missing":[" "]}`,
		`{"verdict":"SUPPORTED","tool_evidence":{}}`,
		`{"verdict":"SUPPORTED"} {"verdict":"SUPPORTED"}`,
		strings.Repeat("x", 16*1024+1),
	} {
		if _, err := parseVerificationCoverageReview(text); err == nil {
			t.Fatalf("invalid coverage review accepted")
		}
	}
}

func TestVerificationCoverageReviewResumeRequiresFreshChecks(t *testing.T) {
	config := IndependentVerificationConfig{MaxTurns: 2, CoverageReview: true}
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	tool := verificationResponse("")
	tool.chunks[0].ToolCalls = []schema.ToolCall{{ID: "verify-command", Type: "function", Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"independent check"}`}}}
	usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 3})
	var saved verificationCursor
	executions := 0
	params := QueryParams{
		SessionID: "fixture", verificationWorkspace: "/fixture", IndependentVerification: config,
		Messages: []*schema.Message{schema.UserMessage("original contract")}, ToolRegistry: registry,
		RunUsage: usage, Deps: &QueryDeps{ProviderUsage: usage},
		ChatModel:                &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("done"), tool, verificationResponse(verificationPass)}},
		ToolExecutor:             func(context.Context, string, string) (string, error) { executions++; return "fresh output", nil },
		commitVerificationCursor: func(c verificationCursor) error { saved = c; return nil },
	}
	_, terminal := collectEvents(t.Context(), params)
	if terminal.Err == nil || saved.Phase != "check" || saved.Repairs != 0 || executions != 1 {
		t.Fatalf("budget pause lost pending stage: terminal=%+v cursor=%+v", terminal, saved)
	}
	// Round-trip the exact runtime cursor, as a new process would.
	encoded, _ := json.Marshal(saved)
	var restored verificationCursor
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	usage, _ = execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 3})
	params.RunUsage = usage
	params.Deps = &QueryDeps{ProviderUsage: usage}
	params.independentVerificationContinuation = true
	params.loadVerificationCursor = func() (*verificationCursor, error) { return &restored, nil }
	params.ChatModel = &canonicalScriptModel{responses: []canonicalModelResponse{tool, verificationResponse(verificationPass), verificationResponse(`{"verdict":"SUPPORTED","missing":[]}`)}}
	_, terminal = collectEvents(t.Context(), params)
	if terminal.Err != nil || executions != 2 || saved.Phase != "completed" || usage.Snapshot().ProviderCalls != 3 {
		t.Fatalf("resume reused provisional PASS or failed: terminal=%+v executions=%d phase=%s", terminal, executions, saved.Phase)
	}
	params.IndependentVerification.CoverageReview = false
	if restored.validate(&params) == nil {
		t.Fatal("resume silently disabled the saved coverage policy")
	}
}

type coverageCancelModel struct {
	*canonicalScriptModel
	cancel context.CancelFunc
}

func (m *coverageCancelModel) Stream(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	review := m.callCount == 3
	m.mu.Unlock()
	if review {
		m.cancel()
	}
	return m.canonicalScriptModel.Stream(ctx, messages, opts...)
}

func TestVerificationCoverageReviewCancellationRetainsCheck(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 4})
	tool := verificationResponse("")
	tool.chunks[0].ToolCalls = []schema.ToolCall{{ID: "verify-command", Type: "function", Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"independent check"}`}}}
	mdl := &coverageCancelModel{canonicalScriptModel: &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("done"), tool, verificationResponse(verificationPass), {err: context.Canceled}}}, cancel: cancel}
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	var saved verificationCursor
	_, terminal := collectEvents(ctx, QueryParams{
		Messages: []*schema.Message{schema.UserMessage("original contract")}, ChatModel: mdl,
		ToolRegistry: registry, RunUsage: usage, Deps: &QueryDeps{ProviderUsage: usage},
		IndependentVerification:  IndependentVerificationConfig{MaxTurns: 2, CoverageReview: true},
		ToolExecutor:             func(context.Context, string, string) (string, error) { return "fresh evidence", nil },
		commitVerificationCursor: func(c verificationCursor) error { saved = c; return nil },
	})
	if !errors.Is(terminal.Err, context.Canceled) || terminal.Reason != TerminalAbortedStreaming || saved.Phase != "check" || saved.Repairs != 0 || mdl.callCount != 4 {
		t.Fatalf("cancellation lost pending check: terminal=%+v phase=%s calls=%d", terminal, saved.Phase, mdl.callCount)
	}
}

func TestVerificationCoverageReviewPreservesConfiguredFailover(t *testing.T) {
	var profiles []string
	params := p294FailoverParams("headless", p294Chain(), func(_ context.Context, _ model.BaseChatModel, _ []*schema.Message, _ *schema.Message, infos []*schema.ToolInfo, opts execution.CallModelOptions) (*execution.CallModelResult, error) {
		profiles = append(profiles, opts.Model)
		if opts.QuerySource != "independent_verification_coverage" || opts.ToolChoice != "none" || len(infos) != 0 {
			t.Error("failover lost review source or exposed tools")
		}
		if opts.Model == "primary" {
			return nil, errors.New("529 overloaded_error")
		}
		return &execution.CallModelResult{StreamReader: schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: `{"verdict":"SUPPORTED","missing":[]}`}}), Model: opts.Model}, nil
	})
	report, _ := parseIndependentVerificationReport(verificationPass)
	report.Evidence = map[string]string{"verify-command": "bound fixture output"}
	gate := &independentVerificationGate{requirements: "original contract"}
	reviewed, err := gate.reviewCoverage(t.Context(), params, report)
	if err != nil || reviewed.Verdict != "PASS" || len(profiles) < 2 || len(profiles) > p294Chain().MaxProviderCalls || profiles[len(profiles)-1] != "alternate" {
		t.Fatalf("configured failover not preserved: err=%v profiles=%v verdict=%s", err, profiles, reviewed.Verdict)
	}
	for _, profile := range profiles[:len(profiles)-1] {
		if profile != "primary" {
			t.Fatalf("unexpected retry route: %v", profiles)
		}
	}
}
