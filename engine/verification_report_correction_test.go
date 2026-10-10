package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func verificationCorrectionTool(id string) canonicalModelResponse {
	r := verificationResponse("")
	r.chunks[0].ToolCalls = []schema.ToolCall{{ID: id, Type: "function", Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"independent check"}`}}}
	return r
}

func TestVerificationFormatCorrectionUsesOnlyRemainingReportRound(t *testing.T) {
	bad := strings.Replace(verificationPass, `"status":"PASS"`, `"status":"UNKNOWN_STATUS"`, 1)
	usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 4})
	mdl := &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("done"), verificationCorrectionTool("verify-command"), verificationResponse(bad), verificationResponse(verificationPass)}}
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	executed, reporting := 0, 0
	events, terminal := collectEvents(t.Context(), QueryParams{
		Messages: []*schema.Message{schema.UserMessage("original contract")}, ChatModel: mdl,
		RunUsage: usage, ToolRegistry: registry, IndependentVerification: IndependentVerificationConfig{MaxTurns: 3},
		ToolExecutor: func(context.Context, string, string) (string, error) { executed++; return "observed evidence", nil },
		Deps: &QueryDeps{ProviderUsage: usage, CallModel: func(ctx context.Context, m model.BaseChatModel, messages []*schema.Message, system *schema.Message, infos []*schema.ToolInfo, opts execution.CallModelOptions) (*execution.CallModelResult, error) {
			if opts.QuerySource == "independent_verification" && opts.ToolChoice == "none" {
				reporting++
				text := ""
				for _, msg := range messages {
					text += msg.Content
				}
				if !strings.Contains(text, "original contract") || !strings.Contains(text, "verify-command") || !strings.Contains(text, "observed evidence") {
					t.Error("correction lost original requirements or current executable evidence")
				}
			}
			return execution.CallModel(ctx, m, messages, system, infos, opts)
		}},
	})
	if terminal.Err != nil || terminal.Reason != TerminalCompleted || executed != 1 || reporting != 1 || usage.Snapshot().ProviderCalls != 4 {
		t.Fatalf("format recovery failed: reason=%s error=%v executed=%d reporting=%d calls=%d", terminal.Reason, terminal.Err, executed, reporting, usage.Snapshot().ProviderCalls)
	}
	var summary *IndependentVerificationSummary
	for _, event := range events {
		if event.AttachmentMessage != nil {
			if value, ok := event.AttachmentMessage.Extra["verification_summary"].(IndependentVerificationSummary); ok {
				summary = &value
			}
		}
	}
	if summary == nil || summary.ReportCorrections != 1 || summary.FormatIssue != "check_status_enum" || summary.Verdict != "PASS" {
		t.Fatalf("missing categorical correction diagnostics: %+v", summary)
	}
}

func TestVerificationFormatCorrectionFailsClosed(t *testing.T) {
	bad := strings.Replace(verificationPass, `"status":"PASS"`, `"status":"UNKNOWN_STATUS"`, 1)
	for _, tc := range []struct {
		name         string
		turns        int
		budget       int64
		initial      string
		correction   canonicalModelResponse
		unknownUsage bool
		calls        uint64
	}{
		{name: "no-remaining-turn", turns: 2, budget: 8, initial: bad, correction: verificationResponse(verificationPass), calls: 3},
		{name: "one-correction-only", turns: 6, budget: 8, initial: bad, correction: verificationResponse(bad), calls: 4},
		{name: "budget-exhausted", turns: 3, budget: 3, initial: bad, correction: verificationResponse(verificationPass), calls: 3},
		{name: "unknown-usage", turns: 3, budget: 8, initial: bad, correction: verificationResponse(verificationPass), unknownUsage: true, calls: 3},
		{name: "unknown-initial-receipt", turns: 3, budget: 8, initial: strings.Replace(bad, "verify-command", "old-solver-id", 1), correction: verificationResponse(verificationPass), calls: 3},
		{name: "unknown-corrected-receipt", turns: 3, budget: 8, initial: bad, correction: verificationResponse(strings.Replace(verificationPass, "verify-command", "old-solver-id", 1)), calls: 4},
		{name: "contradiction", turns: 3, budget: 8, initial: strings.Replace(verificationPass, `"status":"PASS"`, `"status":"FAIL"`, 1), correction: verificationResponse(verificationPass), calls: 3},
		{name: "corrected-contradiction", turns: 3, budget: 8, initial: bad, correction: verificationResponse(strings.Replace(verificationPass, `"status":"PASS"`, `"status":"FAIL"`, 1)), calls: 4},
		{name: "correction-tools-denied", turns: 3, budget: 8, initial: bad, correction: verificationCorrectionTool("forbidden-correction-tool"), calls: 4},
		{name: "oversized", turns: 3, budget: 8, initial: strings.Repeat("x", 65537), correction: verificationResponse(verificationPass), calls: 3},
		{name: "runtime-evidence-spoof", turns: 3, budget: 8, initial: strings.Replace(verificationPass, `"checks":`, `"tool_evidence":{"fake":"value"},"checks":`, 1), correction: verificationResponse(verificationPass), calls: 3},
		{name: "invalid-status-masks-fail-without-witness", turns: 3, budget: 8, initial: strings.Replace(bad, `"verdict":"PASS"`, `"verdict":"FAIL"`, 1), correction: verificationResponse(verificationPass), calls: 3},
		{name: "invalid-status-masks-missing-coverage", turns: 3, budget: 8, initial: strings.Replace(bad, `"coverage_complete":true`, `"coverage_complete":false`, 1), correction: verificationResponse(verificationPass), calls: 3},
		{name: "correction-changes-valid-verdict", turns: 3, budget: 8, initial: strings.Replace(bad, `"verdict":"PASS"`, `"verdict":"PARTIAL"`, 1), correction: verificationResponse(verificationPass), calls: 4},
		{name: "correction-changes-coverage", turns: 3, budget: 8, initial: strings.Replace(strings.Replace(bad, `"verdict":"PASS"`, `"verdict":"UNKNOWN"`, 1), `"coverage_complete":true`, `"coverage_complete":false`, 1), correction: verificationResponse(verificationPass), calls: 4},
		{name: "correction-changes-expectations", turns: 3, budget: 8, initial: bad, correction: verificationResponse(strings.Replace(verificationPass, `"expected":"ok"`, `"expected":"different"`, 1)), calls: 4},
		{name: "json-schema-never-corrected", turns: 3, budget: 8, initial: strings.Replace(bad, `"checks":`, `"private_field":"value","checks":`, 1), correction: verificationResponse(verificationPass), calls: 3},
		{name: "json-syntax-never-corrected", turns: 3, budget: 8, initial: bad[:len(bad)-1], correction: verificationResponse(verificationPass), calls: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: tc.budget})
			initial := verificationResponse(tc.initial)
			if tc.unknownUsage {
				initial.chunks[0].ResponseMeta = nil
			}
			mdl := &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("done"), verificationCorrectionTool("verify-command"), initial, tc.correction, verificationResponse(verificationPass)}}
			registry := tools.NewRegistry()
			tools.RegisterDefaults(registry)
			executed := 0
			_, terminal := collectEvents(t.Context(), QueryParams{Messages: []*schema.Message{schema.UserMessage("original contract")}, ChatModel: mdl, RunUsage: usage, ToolRegistry: registry, IndependentVerification: IndependentVerificationConfig{MaxTurns: tc.turns}, Deps: &QueryDeps{ProviderUsage: usage}, ToolExecutor: func(context.Context, string, string) (string, error) { executed++; return "observed evidence", nil }})
			if terminal.Err == nil || !errors.Is(terminal.Err, ErrIndependentVerification) || usage.Snapshot().ProviderCalls != tc.calls || executed != 1 {
				t.Fatalf("boundary failure: reason=%s error=%v calls=%d executed=%d", terminal.Reason, terminal.Err, usage.Snapshot().ProviderCalls, executed)
			}
		})
	}
}

func TestVerificationCorrectionCancellation(t *testing.T) {
	for _, during := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-correction", true: "during-correction"}[during], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 4})
			bad := strings.Replace(verificationPass, `"status":"PASS"`, `"status":"UNKNOWN"`, 1)
			mdl := &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("done"), verificationCorrectionTool("verify-command"), verificationResponse(bad), verificationResponse(verificationPass)}}
			registry := tools.NewRegistry()
			tools.RegisterDefaults(registry)
			calls := 0
			_, terminal := collectEvents(ctx, QueryParams{Messages: []*schema.Message{schema.UserMessage("contract")}, ChatModel: mdl, RunUsage: usage, ToolRegistry: registry, IndependentVerification: IndependentVerificationConfig{MaxTurns: 3}, ToolExecutor: func(context.Context, string, string) (string, error) { return "evidence", nil }, Deps: &QueryDeps{ProviderUsage: usage, CallModel: func(ctx context.Context, m model.BaseChatModel, messages []*schema.Message, system *schema.Message, infos []*schema.ToolInfo, opts execution.CallModelOptions) (*execution.CallModelResult, error) {
				calls++
				if during && calls == 4 {
					cancel()
					return nil, ctx.Err()
				}
				result, err := execution.CallModel(ctx, m, messages, system, infos, opts)
				if !during && calls == 3 {
					cancel()
				}
				return result, err
			}}})
			want := 3
			if during {
				want = 4
			}
			if !errors.Is(terminal.Err, context.Canceled) || calls != want {
				t.Fatalf("cancellation did not stop correction: calls=%d error=%v", calls, terminal.Err)
			}
		})
	}
}

func TestVerificationFormatErrorRedactsModelValues(t *testing.T) {
	_, err := parseIndependentVerificationReport(`{"private_secret_field":"sensitive_value"}`)
	if err == nil || strings.Contains(err.Error(), "private_secret_field") || strings.Contains(err.Error(), "sensitive_value") {
		t.Fatalf("unbounded decoder diagnostic: %v", err)
	}
}

func TestVerificationCorrectedFailCanRepairAndRecheck(t *testing.T) {
	bad := strings.Replace(verificationFail, `"verdict":"FAIL"`, `"verdict":"UNKNOWN_VERDICT"`, 1)
	pass := strings.Replace(verificationPass, "verify-command", "verify-again", 1)
	usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 7})
	mdl := &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("done"), verificationCorrectionTool("verify-command"), verificationResponse(bad), verificationResponse(verificationFail), verificationResponse("repaired"), verificationCorrectionTool("verify-again"), verificationResponse(pass)}}
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	events, terminal := collectEvents(t.Context(), QueryParams{Messages: []*schema.Message{schema.UserMessage("original contract")}, ChatModel: mdl, RunUsage: usage, ToolRegistry: registry, IndependentVerification: IndependentVerificationConfig{MaxTurns: 3, MaxRepairs: 1}, Deps: &QueryDeps{ProviderUsage: usage}, ToolExecutor: func(context.Context, string, string) (string, error) { return "observed evidence", nil }})
	var verdicts []string
	for _, e := range events {
		if e.AttachmentMessage != nil && e.AttachmentMessage.Extra["attachment_kind"] == "independent_verification" {
			verdicts = append(verdicts, e.AttachmentMessage.Extra["verdict"].(string))
		}
	}
	if terminal.Err != nil || terminal.Reason != TerminalCompleted || strings.Join(verdicts, ",") != "FAIL,PASS" || usage.Snapshot().ProviderCalls != 7 {
		t.Fatalf("repair after correction failed: reason=%s error=%v verdicts=%v calls=%d", terminal.Reason, terminal.Err, verdicts, usage.Snapshot().ProviderCalls)
	}
}
