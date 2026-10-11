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

// Exercise the production gate, not just its JSON decoder: a reasoning-only
// review must not strand a receipt-bound report or authorize solver repair.
func TestVerificationCoverageReviewRecovery(t *testing.T) {
	for _, tc := range []struct {
		name       string
		allowance  int
		secondPass bool
		wantCalls  uint64
		wantChecks int
		wantPhase  string
	}{
		{name: "fresh-checks-recover", allowance: 1, secondPass: true, wantCalls: 7, wantChecks: 1, wantPhase: "completed"},
		{name: "invalid-again-exhausts", allowance: 1, wantCalls: 7, wantChecks: 1, wantPhase: "exhausted"},
		{name: "default-no-supplement", wantCalls: 4, wantPhase: "exhausted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 9})
			registry := tools.NewRegistry()
			tools.RegisterDefaults(registry)
			firstTool := coverageRecoveryTool("verify-command")
			secondTool := coverageRecoveryTool("fresh-check")
			secondReport := strings.ReplaceAll(verificationPass, "verify-command", "fresh-check")
			secondReview := coverageReasoningOnly()
			if tc.secondPass {
				secondReview = verificationResponse(`{"verdict":"SUPPORTED","missing":[]}`)
			}
			mdl := &canonicalScriptModel{responses: []canonicalModelResponse{
				verificationResponse("PRIVATE_SOLVER_HISTORY"), firstTool, verificationResponse(verificationPass), coverageReasoningOnly(),
				secondTool, verificationResponse(secondReport), secondReview,
			}}
			var saved verificationCursor
			executions, solverCalls := 0, 0
			events, terminal := collectEvents(t.Context(), QueryParams{
				Messages: []*schema.Message{schema.UserMessage("original contract")}, ChatModel: mdl,
				ToolRegistry: registry, RunUsage: usage,
				IndependentVerification:  IndependentVerificationConfig{MaxTurns: 2, MaxRepairs: 3, CoverageReview: true, MaxCoverageChecks: tc.allowance},
				ToolExecutor:             func(context.Context, string, string) (string, error) { executions++; return "fresh output", nil },
				commitVerificationCursor: func(c verificationCursor) error { saved = c; return nil },
				Deps: &QueryDeps{ProviderUsage: usage, CallModel: func(ctx context.Context, m model.BaseChatModel, messages []*schema.Message, system *schema.Message, infos []*schema.ToolInfo, opts execution.CallModelOptions) (*execution.CallModelResult, error) {
					if opts.QuerySource != "independent_verification" && opts.QuerySource != "independent_verification_coverage" {
						solverCalls++
					}
					if opts.QuerySource == "independent_verification" && usage.Snapshot().ProviderCalls >= 4 {
						for _, msg := range messages {
							if strings.Contains(msg.Content, "PRIVATE_") || strings.Contains(msg.Content, "verify-command") {
								t.Error("supplement reused private history or stale receipt")
							}
						}
					}
					return execution.CallModel(ctx, m, messages, system, infos, opts)
				}},
			})
			if (terminal.Err == nil) != tc.secondPass || saved.Phase != tc.wantPhase || saved.CoverageChecks != tc.wantChecks || saved.Repairs != 0 || solverCalls != 1 || usage.Snapshot().ProviderCalls != tc.wantCalls {
				t.Fatalf("review recovery: terminal=%+v phase=%s checks=%d repairs=%d solver=%d calls=%d", terminal, saved.Phase, saved.CoverageChecks, saved.Repairs, solverCalls, usage.Snapshot().ProviderCalls)
			}
			if !tc.secondPass && !errors.Is(terminal.Err, ErrIndependentVerification) {
				t.Fatalf("invalid review completed: %+v", terminal)
			}
			if executions != 1+tc.wantChecks || !usage.Snapshot().Complete {
				t.Fatalf("fresh checks or usage lost: executions=%d snapshot=%+v", executions, usage.Snapshot())
			}
			var summaries []IndependentVerificationSummary
			for _, event := range events {
				if event.Type == EventAttachment && event.AttachmentMessage != nil {
					if s, ok := event.AttachmentMessage.Extra["verification_summary"].(IndependentVerificationSummary); ok {
						summaries = append(summaries, s)
					}
				}
			}
			if len(summaries) != 1+tc.wantChecks || summaries[0].Verdict != "PARTIAL" || summaries[0].CoverageReviews != 1 || summaries[0].CoverageVerdict != "UNAVAILABLE" || summaries[0].Failed != 0 {
				t.Fatalf("provisional PASS escaped or diagnostic lost: %+v", summaries)
			}
			encoded, _ := json.Marshal(summaries[0])
			if !strings.Contains(string(encoded), `"coverage_issue":"empty_output"`) || strings.Contains(string(encoded), "PRIVATE_") {
				t.Fatalf("bounded coverage issue missing: %s", encoded)
			}
		})
	}
}

func TestVerificationCoverageReviewRecoveryBudgetResume(t *testing.T) {
	config := IndependentVerificationConfig{MaxTurns: 2, MaxRepairs: 3, CoverageReview: true, MaxCoverageChecks: 1}
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 4})
	var saved verificationCursor
	executions := 0
	params := QueryParams{
		SessionID: "fixture", verificationWorkspace: "/fixture", IndependentVerification: config,
		Messages: []*schema.Message{schema.UserMessage("original contract")}, ToolRegistry: registry,
		RunUsage: usage, Deps: &QueryDeps{ProviderUsage: usage},
		ChatModel:                &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("done"), coverageRecoveryTool("verify-command"), verificationResponse(verificationPass), coverageReasoningOnly()}},
		ToolExecutor:             func(context.Context, string, string) (string, error) { executions++; return "fresh output", nil },
		commitVerificationCursor: func(c verificationCursor) error { saved = c; return nil },
	}
	_, terminal := collectEvents(t.Context(), params)
	if !errors.Is(terminal.Err, execution.ErrRunBudgetExceeded) || saved.Phase != "coverage" || saved.CoverageChecks != 0 || saved.Repairs != 0 || saved.Diagnostics == nil || saved.Diagnostics.Verdict != "PARTIAL" {
		t.Fatalf("budget pause did not retain recovery: terminal=%+v cursor=%+v", terminal, saved)
	}
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
	params.ChatModel = &canonicalScriptModel{responses: []canonicalModelResponse{coverageRecoveryTool("fresh-check"), verificationResponse(strings.ReplaceAll(verificationPass, "verify-command", "fresh-check")), verificationResponse(`{"verdict":"SUPPORTED","missing":[]}`)}}
	_, terminal = collectEvents(t.Context(), params)
	if terminal.Err != nil || executions != 2 || saved.Phase != "completed" || saved.CoverageChecks != 1 || saved.Repairs != 0 || usage.Snapshot().ProviderCalls != 3 {
		t.Fatalf("resume reused old PASS or reset allowance: terminal=%+v executions=%d cursor=%+v", terminal, executions, saved)
	}
}

func TestVerificationCoverageInvalidReviewIsOnlyMissingEvidence(t *testing.T) {
	for _, tc := range []struct{ name, text, issue string }{
		{"empty", " \n", "empty_output"},
		{"syntax", `{"verdict":`, "json_schema"},
		{"unknown-field", `{"verdict":"SUPPORTED","tool_evidence":{"injected":"PRIVATE_INVALID_CLAIM"}}`, "json_schema"},
		{"multiple", `{"verdict":"SUPPORTED"} {"verdict":"SUPPORTED"}`, "multiple_objects"},
		{"contradiction", `{"verdict":"SUPPORTED","missing":["PRIVATE_INVALID_CLAIM"]}`, "contradictory_verdict"},
		{"invented-fail", `{"verdict":"FAIL","missing":["PRIVATE_INVALID_CLAIM"]}`, "contradictory_verdict"},
		{"empty-gap", `{"verdict":"INSUFFICIENT","missing":[" "]}`, "empty_gap"},
		{"oversized", strings.Repeat("x", 16*1024+1), "size_limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 4})
			registry := tools.NewRegistry()
			tools.RegisterDefaults(registry)
			var saved verificationCursor
			events, terminal := collectEvents(t.Context(), QueryParams{
				Messages: []*schema.Message{schema.UserMessage("original contract")}, ToolRegistry: registry,
				RunUsage: usage, Deps: &QueryDeps{ProviderUsage: usage},
				ChatModel:                &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("done"), coverageRecoveryTool("verify-command"), verificationResponse(verificationPass), verificationResponse(tc.text)}},
				IndependentVerification:  IndependentVerificationConfig{MaxTurns: 2, MaxRepairs: 3, CoverageReview: true},
				ToolExecutor:             func(context.Context, string, string) (string, error) { return "fresh output", nil },
				commitVerificationCursor: func(c verificationCursor) error { saved = c; return nil },
			})
			if !errors.Is(terminal.Err, ErrIndependentVerification) || saved.Phase != "exhausted" || saved.Repairs != 0 || saved.Diagnostics == nil || saved.Diagnostics.Verdict != "PARTIAL" || saved.Diagnostics.CoverageComplete || saved.Diagnostics.Checks[0].Status != "PASS" || len(saved.Diagnostics.Missing) != 1 || strings.Contains(saved.Diagnostics.Missing[0], "PRIVATE_") {
				t.Fatalf("invalid review manufactured evidence or repair: terminal=%+v cursor=%+v", terminal, saved)
			}
			found := false
			for _, event := range events {
				if event.Type == EventAttachment && event.AttachmentMessage != nil {
					if s, ok := event.AttachmentMessage.Extra["verification_summary"].(IndependentVerificationSummary); ok {
						found = s.CoverageVerdict == "UNAVAILABLE" && s.CoverageIssue == tc.issue && s.Failed == 0
					}
				}
			}
			if !found {
				t.Fatal("categorical issue missing from outward summary")
			}
		})
	}
}

func TestVerificationCoverageUnknownUsageDoesNotRecover(t *testing.T) {
	for _, tc := range []struct{ name, text string }{
		{"empty", ""},
		{"supported", `{"verdict":"SUPPORTED","missing":[]}`},
		{"insufficient", `{"verdict":"INSUFFICIENT","missing":["missing original obligation"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) { testCoverageUnknownUsage(t, tc.text) })
	}
}

func testCoverageUnknownUsage(t *testing.T, text string) {
	t.Helper()
	usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 7})
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	review := verificationResponse(text)
	review.chunks[0].ResponseMeta = nil
	var saved verificationCursor
	events, terminal := collectEvents(t.Context(), QueryParams{
		Messages: []*schema.Message{schema.UserMessage("original contract")}, ToolRegistry: registry,
		RunUsage: usage, Deps: &QueryDeps{ProviderUsage: usage},
		ChatModel:                &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("done"), coverageRecoveryTool("verify-command"), verificationResponse(verificationPass), review}},
		IndependentVerification:  IndependentVerificationConfig{MaxTurns: 2, MaxRepairs: 3, CoverageReview: true, MaxCoverageChecks: 1},
		ToolExecutor:             func(context.Context, string, string) (string, error) { return "fresh output", nil },
		commitVerificationCursor: func(c verificationCursor) error { saved = c; return nil },
	})
	if !errors.Is(terminal.Err, execution.ErrRunUsageUnknown) || saved.Phase != "check" || saved.Repairs != 0 || saved.CoverageChecks != 0 || saved.Diagnostics != nil || usage.Snapshot().Complete || usage.Snapshot().ProviderCalls != 4 {
		t.Fatalf("unknown usage authorized recovery: terminal=%+v phase=%s calls=%d unknown=%d", terminal, saved.Phase, usage.Snapshot().ProviderCalls, usage.Snapshot().UnknownCalls)
	}
	for _, event := range events {
		if event.Type == EventAttachment && event.AttachmentMessage != nil && event.AttachmentMessage.Extra["attachment_kind"] == "independent_verification" {
			t.Fatal("unknown review usage published a verification result")
		}
	}
}

func TestVerificationCoverageProviderErrorDoesNotRecover(t *testing.T) {
	usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 7})
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	providerErr := errors.New("fixture coverage provider error")
	var saved verificationCursor
	events, terminal := collectEvents(t.Context(), QueryParams{
		Messages: []*schema.Message{schema.UserMessage("original contract")}, ToolRegistry: registry,
		RunUsage: usage, Deps: &QueryDeps{ProviderUsage: usage},
		ChatModel:                &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("done"), coverageRecoveryTool("verify-command"), verificationResponse(verificationPass), {err: providerErr}}},
		IndependentVerification:  IndependentVerificationConfig{MaxTurns: 2, MaxRepairs: 3, CoverageReview: true, MaxCoverageChecks: 1},
		ToolExecutor:             func(context.Context, string, string) (string, error) { return "fresh output", nil },
		commitVerificationCursor: func(c verificationCursor) error { saved = c; return nil },
	})
	if !errors.Is(terminal.Err, providerErr) || saved.Phase != "check" || saved.Repairs != 0 || saved.CoverageChecks != 0 || saved.Diagnostics != nil || usage.Snapshot().ProviderCalls != 4 {
		t.Fatalf("provider error authorized recovery: terminal=%+v cursor=%+v usage=%+v", terminal, saved, usage.Snapshot())
	}
	for _, event := range events {
		if event.Type == EventAttachment && event.AttachmentMessage != nil && event.AttachmentMessage.Extra["attachment_kind"] == "independent_verification" {
			t.Fatal("provider error published a verification result")
		}
	}
}

func coverageRecoveryTool(id string) canonicalModelResponse {
	response := verificationResponse("")
	response.chunks[0].ToolCalls = []schema.ToolCall{{ID: id, Type: "function", Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"independent check"}`}}}
	return response
}

func coverageReasoningOnly() canonicalModelResponse {
	response := verificationResponse("")
	response.chunks[0].ReasoningContent = "PRIVATE_REVIEW_REASONING"
	response.chunks[0].ResponseMeta.Usage.CompletionTokens = 16384
	return response
}
