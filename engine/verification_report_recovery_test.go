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

func TestVerificationContradictoryReportRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, text, issue  string
		failed, unverified int
	}{
		{"incomplete-pass", strings.Replace(verificationPass, `"coverage_complete":true`, `"coverage_complete":false`, 1), "pass_incomplete_coverage", 0, 0},
		{"pass-with-missing", strings.TrimSuffix(verificationPass, "}") + `,"missing":["missing obligation"]}`, "pass_incomplete_coverage", 0, 0},
		{"pass-without-checks", `{"verdict":"PASS","coverage_complete":true,"checks":[]}`, "pass_incomplete_coverage", 0, 0},
		{"pass-with-failure", strings.Replace(verificationPass, `"status":"PASS"`, `"status":"FAIL"`, 1), "pass_contradicts_check", 1, 0},
		{"pass-with-unverified", strings.Replace(verificationPass, `"status":"PASS"`, `"status":"UNVERIFIED"`, 1), "pass_contradicts_check", 0, 1},
		{"pass-with-unverified-no-receipt", `{"verdict":"PASS","coverage_complete":true,"checks":[{"requirement":"original contract","expected":"ok","observed":"not executed","status":"UNVERIFIED"}]}`, "pass_contradicts_check", 0, 1},
		{"fail-without-counterexample", strings.Replace(verificationPass, `"verdict":"PASS"`, `"verdict":"FAIL"`, 1), "fail_without_counterexample", 0, 0},
		{"missing-pass-id", strings.Replace(verificationPass, `"tool_call_id":"verify-command"`, `"tool_call_id":""`, 1), "missing_tool_identity", 0, 1},
		{"missing-fail-id", strings.Replace(verificationFail, `"tool_call_id":"verify-command"`, `"tool_call_id":" "`, 1), "missing_tool_identity", 0, 1},
		{"contradiction-with-missing-id", strings.Replace(strings.Replace(verificationPass, `"coverage_complete":true`, `"coverage_complete":false`, 1), `"tool_call_id":"verify-command"`, `"tool_call_id":""`, 1), "missing_tool_identity", 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, allowance := range []int{0, 1} {
				usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 6})
				registry := tools.NewRegistry()
				tools.RegisterDefaults(registry)
				var saved verificationCursor
				executions, solverCalls := 0, 0
				events, terminal := collectEvents(t.Context(), QueryParams{
					Messages: []*schema.Message{schema.UserMessage("original contract")}, ToolRegistry: registry,
					RunUsage: usage, IndependentVerification: IndependentVerificationConfig{MaxTurns: 2, MaxRepairs: 3, MaxCoverageChecks: allowance},
					ChatModel: &canonicalScriptModel{responses: []canonicalModelResponse{
						verificationResponse("PRIVATE_SOLVER_HISTORY"), coverageRecoveryTool("verify-command"), verificationResponse(tc.text),
						coverageRecoveryTool("fresh-check"), verificationResponse(strings.ReplaceAll(verificationPass, "verify-command", "fresh-check")),
					}},
					ToolExecutor:             func(context.Context, string, string) (string, error) { executions++; return "fresh output", nil },
					commitVerificationCursor: func(c verificationCursor) error { saved = c; return nil },
					Deps: &QueryDeps{ProviderUsage: usage, CallModel: func(ctx context.Context, m model.BaseChatModel, messages []*schema.Message, system *schema.Message, infos []*schema.ToolInfo, opts execution.CallModelOptions) (*execution.CallModelResult, error) {
						if opts.QuerySource != "independent_verification" {
							solverCalls++
						}
						if opts.QuerySource == "independent_verification" && usage.Snapshot().ProviderCalls >= 3 {
							for _, msg := range messages {
								if strings.Contains(msg.Content, "PRIVATE_") || strings.Contains(msg.Content, "verify-command") {
									t.Error("recovery reused solver history or old receipt")
								}
							}
						}
						return execution.CallModel(ctx, m, messages, system, infos, opts)
					}},
				})
				wantPhase := "exhausted"
				if allowance == 1 {
					wantPhase = "completed"
				}
				if (terminal.Err == nil) != (allowance == 1) || saved.Phase != wantPhase || saved.Repairs != 0 || saved.CoverageChecks != allowance || solverCalls != 1 || executions != 1+allowance || usage.Snapshot().ProviderCalls != uint64(3+2*allowance) {
					t.Fatalf("recovery allowance=%d: terminal=%+v cursor=%+v solver=%d executions=%d usage=%+v", allowance, terminal, saved, solverCalls, executions, usage.Snapshot())
				}
				var summaries []IndependentVerificationSummary
				for _, event := range events {
					if event.AttachmentMessage != nil {
						if s, ok := event.AttachmentMessage.Extra["verification_summary"].(IndependentVerificationSummary); ok {
							summaries = append(summaries, s)
						}
					}
				}
				if len(summaries) != 1+allowance || summaries[0].Verdict != "PARTIAL" || summaries[0].Failed != tc.failed || summaries[0].Unverified != tc.unverified {
					t.Fatalf("invalid verdict escaped or check statuses changed: %+v", summaries)
				}
				encoded, _ := json.Marshal(summaries[0])
				if !strings.Contains(string(encoded), `"report_issue":"`+tc.issue+`"`) || strings.Contains(string(encoded), "PRIVATE_") {
					t.Fatalf("categorical report issue lost: %s", encoded)
				}
			}
		})
	}
}

func TestVerificationContradictoryReportBudgetResume(t *testing.T) {
	for _, tc := range []struct {
		name, report, command string
	}{
		{"contradiction", strings.Replace(verificationPass, `"coverage_complete":true`, `"coverage_complete":false`, 1), "independent check"},
		{"missing-identity", strings.Replace(verificationPass, `"tool_call_id":"verify-command"`, `"tool_call_id":""`, 1), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := IndependentVerificationConfig{MaxTurns: 2, MaxRepairs: 3, MaxCoverageChecks: 1, CoverageReview: true}
			registry := tools.NewRegistry()
			tools.RegisterDefaults(registry)
			usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 3})
			var saved verificationCursor
			executions := 0
			params := QueryParams{
				SessionID: "fixture", verificationWorkspace: "/fixture", IndependentVerification: config,
				Messages: []*schema.Message{schema.UserMessage("original contract")}, ToolRegistry: registry,
				RunUsage: usage, Deps: &QueryDeps{ProviderUsage: usage},
				ChatModel:                &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("done"), coverageRecoveryTool("verify-command"), verificationResponse(tc.report)}},
				ToolExecutor:             func(context.Context, string, string) (string, error) { executions++; return "fresh output", nil },
				commitVerificationCursor: func(c verificationCursor) error { saved = c; return nil },
			}
			_, terminal := collectEvents(t.Context(), params)
			if !errors.Is(terminal.Err, execution.ErrRunBudgetExceeded) || saved.Phase != "coverage" || saved.CoverageChecks != 0 || saved.Repairs != 0 || saved.Diagnostics == nil || saved.Diagnostics.Verdict != "PARTIAL" || saved.Diagnostics.CoverageComplete || saved.Diagnostics.Checks[0].Command != tc.command || saved.Diagnostics.Evidence != nil {
				t.Fatalf("budget pause lost validated diagnostics: terminal=%+v cursor=%+v", terminal, saved)
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
				t.Fatalf("resume reused old evidence or reset allowance: terminal=%+v executions=%d cursor=%+v", terminal, executions, saved)
			}
		})
	}
}

func TestVerificationContradictoryReportRejectsInvalidEvidence(t *testing.T) {
	bad := strings.Replace(verificationPass, `"coverage_complete":true`, `"coverage_complete":false`, 1)
	for _, tc := range []struct {
		name, text                          string
		unknownUsage, toolError, background bool
	}{
		{name: "syntax", text: bad[:len(bad)-1]},
		{name: "schema", text: strings.Replace(bad, `"checks":`, `"unknown_field":"PRIVATE_VALUE","checks":`, 1)},
		{name: "multiple-objects", text: bad + bad},
		{name: "invalid-status", text: strings.Replace(bad, `"status":"PASS"`, `"status":"BAD"`, 1)},
		{name: "missing-fields", text: strings.Replace(bad, `"expected":"ok"`, `"expected":""`, 1)},
		{name: "stale-id", text: strings.Replace(bad, "verify-command", "solver-receipt", 1)},
		{name: "stale-unverified-id", text: strings.Replace(strings.Replace(bad, "verify-command", "solver-receipt", 1), `"status":"PASS"`, `"status":"UNVERIFIED"`, 1)},
		{name: "wrong-unverified-command", text: strings.Replace(strings.Replace(bad, "independent check", "other command", 1), `"status":"PASS"`, `"status":"UNVERIFIED"`, 1)},
		{name: "unverified-command-without-id", text: strings.Replace(strings.Replace(bad, `"tool_call_id":"verify-command"`, `"tool_call_id":""`, 1), `"status":"PASS"`, `"status":"UNVERIFIED"`, 1)},
		{name: "wrong-command", text: strings.Replace(bad, "independent check", "other command", 1)},
		{name: "injected-evidence", text: strings.Replace(bad, `"checks":`, `"tool_evidence":{"id":"PRIVATE_VALUE"},"checks":`, 1)},
		{name: "injected-issue", text: strings.Replace(bad, `"checks":`, `"report_issue":"pass_incomplete_coverage","checks":`, 1)},
		{name: "oversized", text: strings.Repeat(" ", 65537) + bad},
		{name: "unknown-usage", text: bad, unknownUsage: true},
		{name: "tool-error", text: bad, toolError: true},
		{name: "background", text: bad, background: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 7})
			registry := tools.NewRegistry()
			tools.RegisterDefaults(registry)
			tool := coverageRecoveryTool("verify-command")
			if tc.background {
				tool.chunks[0].ToolCalls[0].Function.Arguments = `{"command":"independent check","run_in_background":true}`
			}
			response := verificationResponse(tc.text)
			if tc.unknownUsage {
				response.chunks[0].ResponseMeta = nil
			}
			var saved verificationCursor
			events, terminal := collectEvents(t.Context(), QueryParams{
				Messages: []*schema.Message{schema.UserMessage("original contract")}, ToolRegistry: registry,
				RunUsage: usage, Deps: &QueryDeps{ProviderUsage: usage},
				IndependentVerification: IndependentVerificationConfig{MaxTurns: 3, MaxRepairs: 3, MaxCoverageChecks: 1},
				ChatModel:               &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("done"), tool, response}},
				ToolExecutor: func(context.Context, string, string) (string, error) {
					if tc.toolError {
						return "", errors.New("fixture tool failure")
					}
					return "fresh output", nil
				},
				commitVerificationCursor: func(c verificationCursor) error { saved = c; return nil },
			})
			if !errors.Is(terminal.Err, ErrIndependentVerification) || saved.Phase != "check" || saved.Diagnostics != nil || saved.Repairs != 0 || saved.CoverageChecks != 0 || usage.Snapshot().ProviderCalls != 3 {
				t.Fatalf("invalid evidence recovered: reason=%s err=%v phase=%s calls=%d", terminal.Reason, terminal.Err, saved.Phase, usage.Snapshot().ProviderCalls)
			}
			if tc.unknownUsage && !errors.Is(terminal.Err, execution.ErrRunUsageUnknown) {
				t.Fatalf("usage stop lost: %v", terminal.Err)
			}
			for _, event := range events {
				if event.AttachmentMessage != nil && event.AttachmentMessage.Extra["attachment_kind"] == "independent_verification" {
					t.Fatal("invalid evidence published a diagnostic")
				}
			}
		})
	}
}

func TestVerificationContradictoryReportSupplementStops(t *testing.T) {
	bad := strings.Replace(verificationPass, `"coverage_complete":true`, `"coverage_complete":false`, 1)
	usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 7})
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	var saved verificationCursor
	_, terminal := collectEvents(t.Context(), QueryParams{
		Messages: []*schema.Message{schema.UserMessage("original contract")}, ToolRegistry: registry,
		RunUsage: usage, Deps: &QueryDeps{ProviderUsage: usage},
		IndependentVerification:  IndependentVerificationConfig{MaxTurns: 2, MaxRepairs: 3, MaxCoverageChecks: 1},
		ChatModel:                &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("done"), coverageRecoveryTool("verify-command"), verificationResponse(bad), coverageRecoveryTool("fresh-check"), verificationResponse(strings.ReplaceAll(bad, "verify-command", "fresh-check"))}},
		ToolExecutor:             func(context.Context, string, string) (string, error) { return "fresh output", nil },
		commitVerificationCursor: func(c verificationCursor) error { saved = c; return nil },
	})
	if !errors.Is(terminal.Err, ErrIndependentVerification) || saved.Phase != "exhausted" || saved.CoverageChecks != 1 || saved.Repairs != 0 || usage.Snapshot().ProviderCalls != 5 {
		t.Fatalf("repeated contradiction renewed allowance: err=%v phase=%s checks=%d calls=%d", terminal.Err, saved.Phase, saved.CoverageChecks, usage.Snapshot().ProviderCalls)
	}
}

func TestVerificationContradictoryReportInterruptedDoesNotRecover(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "provider-error", true: "cancel"}[canceled], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 7})
			registry := tools.NewRegistry()
			tools.RegisterDefaults(registry)
			response := verificationResponse(strings.Replace(verificationPass, `"coverage_complete":true`, `"coverage_complete":false`, 1))
			providerErr := errors.New("fixture provider failure")
			if !canceled {
				response = canonicalModelResponse{err: providerErr}
			}
			var saved verificationCursor
			events, terminal := collectEvents(ctx, QueryParams{
				Messages: []*schema.Message{schema.UserMessage("original contract")}, ToolRegistry: registry,
				RunUsage: usage, IndependentVerification: IndependentVerificationConfig{MaxTurns: 2, MaxRepairs: 3, MaxCoverageChecks: 1},
				ChatModel:                &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("done"), coverageRecoveryTool("verify-command"), response}},
				ToolExecutor:             func(context.Context, string, string) (string, error) { return "fresh output", nil },
				commitVerificationCursor: func(c verificationCursor) error { saved = c; return nil },
				Deps: &QueryDeps{ProviderUsage: usage, CallModel: func(ctx context.Context, m model.BaseChatModel, messages []*schema.Message, system *schema.Message, infos []*schema.ToolInfo, opts execution.CallModelOptions) (*execution.CallModelResult, error) {
					result, err := execution.CallModel(ctx, m, messages, system, infos, opts)
					if canceled && usage.Snapshot().ProviderCalls == 3 {
						cancel()
					}
					return result, err
				}},
			})
			wantErr := providerErr
			if canceled {
				wantErr = context.Canceled
			}
			if !errors.Is(terminal.Err, wantErr) || saved.Phase != "check" || saved.Diagnostics != nil || saved.CoverageChecks != 0 || saved.Repairs != 0 || usage.Snapshot().ProviderCalls != 3 {
				t.Fatalf("interruption authorized recovery: err=%v phase=%s calls=%d", terminal.Err, saved.Phase, usage.Snapshot().ProviderCalls)
			}
			for _, event := range events {
				if event.AttachmentMessage != nil && event.AttachmentMessage.Extra["attachment_kind"] == "independent_verification" {
					t.Fatal("interrupted report published a diagnostic")
				}
			}
		})
	}
}
