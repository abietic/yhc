package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/schema"
)

const verificationUnboundCheck = `{"requirement":"other obligation","tool_call_id":"","command":"independent check","expected":"expected behavior","observed":"claimed behavior","status":"PASS"}`

func verificationReportWithUnboundCheck(text string) string {
	return strings.Replace(text, `"checks":[`, `"checks":[`+verificationUnboundCheck+`,`, 1)
}

func TestVerificationMissingIdentityPreservesBoundChecks(t *testing.T) {
	for _, bound := range []string{verificationPass, verificationFail} {
		usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 3})
		registry := tools.NewRegistry()
		tools.RegisterDefaults(registry)
		var saved verificationCursor
		_, terminal := collectEvents(t.Context(), QueryParams{
			Messages: []*schema.Message{schema.UserMessage("original contract")}, ToolRegistry: registry,
			RunUsage: usage, IndependentVerification: IndependentVerificationConfig{MaxTurns: 2, MaxRepairs: 3},
			ChatModel: &canonicalScriptModel{responses: []canonicalModelResponse{
				verificationResponse("done"), coverageRecoveryTool("verify-command"), verificationResponse(verificationReportWithUnboundCheck(bound)),
			}},
			ToolExecutor:             func(context.Context, string, string) (string, error) { return "fresh output", nil },
			commitVerificationCursor: func(c verificationCursor) error { saved = c; return nil },
		})
		if !errors.Is(terminal.Err, ErrIndependentVerification) || saved.Phase != "exhausted" || saved.Repairs != 0 || usage.Snapshot().ProviderCalls != 3 || saved.Diagnostics == nil {
			t.Fatalf("unbound claim authorized completion/repair: terminal=%+v phase=%s", terminal, saved.Phase)
		}
		checks := saved.Diagnostics.Checks
		var original independentVerificationReport
		if err := json.Unmarshal([]byte(bound), &original); err != nil {
			t.Fatal(err)
		}
		if len(checks) != 2 || checks[0].Status != "UNVERIFIED" || checks[0].ToolCallID != "" || checks[0].Command != "" || checks[1] != original.Checks[0] || saved.Diagnostics.Evidence != nil {
			t.Fatal("unbound command was guessed or bound check was altered")
		}
	}
}

func TestVerificationMissingIdentityCannotMaskInvalidEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, report                        string
		toolError, background, unknownUsage bool
	}{
		{name: "unknown-bound-id", report: strings.Replace(verificationPass, "verify-command", "unknown-id", 1)},
		{name: "unknown-unverified-id", report: strings.Replace(strings.Replace(verificationPass, "verify-command", "unknown-id", 1), `"status":"PASS"`, `"status":"UNVERIFIED"`, 1)},
		{name: "blank-unverified-id", report: strings.Replace(strings.Replace(verificationPass, `"tool_call_id":"verify-command"`, `"tool_call_id":" "`, 1), `"status":"PASS"`, `"status":"UNVERIFIED"`, 1)},
		{name: "unverified-command-only", report: strings.Replace(strings.Replace(verificationPass, `"tool_call_id":"verify-command",`, "", 1), `"status":"PASS"`, `"status":"UNVERIFIED"`, 1)},
		{name: "wrong-command", report: strings.Replace(verificationPass, "independent check", "other command", 1)},
		{name: "wrong-unverified-command", report: strings.Replace(strings.Replace(verificationPass, "independent check", "other command", 1), `"status":"PASS"`, `"status":"UNVERIFIED"`, 1)},
		{name: "missing-contract", report: strings.Replace(verificationPass, `"expected":"ok"`, `"expected":""`, 1)},
		{name: "invalid-status", report: strings.Replace(verificationPass, `"status":"PASS"`, `"status":"INVALID"`, 1)},
		{name: "syntax", report: verificationPass[:len(verificationPass)-1]},
		{name: "unknown-field", report: strings.Replace(verificationPass, `"checks":`, `"unknown":true,"checks":`, 1)},
		{name: "failed-tool", report: verificationPass, toolError: true},
		{name: "background-tool", report: verificationPass, background: true},
		{name: "unknown-usage", report: verificationPass, unknownUsage: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 7})
			registry := tools.NewRegistry()
			tools.RegisterDefaults(registry)
			tool := coverageRecoveryTool("verify-command")
			if tc.background {
				tool.chunks[0].ToolCalls[0].Function.Arguments = `{"command":"independent check","run_in_background":true}`
			}
			report := verificationResponse(verificationReportWithUnboundCheck(tc.report))
			if tc.unknownUsage {
				report.chunks[0].ResponseMeta = nil
			}
			var saved verificationCursor
			_, terminal := collectEvents(t.Context(), QueryParams{
				Messages: []*schema.Message{schema.UserMessage("original contract")}, ToolRegistry: registry,
				RunUsage: usage, IndependentVerification: IndependentVerificationConfig{MaxTurns: 3, MaxRepairs: 3, MaxCoverageChecks: 1},
				ChatModel: &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("done"), tool, report}},
				ToolExecutor: func(context.Context, string, string) (string, error) {
					if tc.toolError {
						return "", errors.New("fixture tool failure")
					}
					return "fresh output", nil
				},
				commitVerificationCursor: func(c verificationCursor) error { saved = c; return nil },
			})
			if !errors.Is(terminal.Err, ErrIndependentVerification) || saved.Phase != "check" || saved.Diagnostics != nil || saved.Repairs != 0 || saved.CoverageChecks != 0 || usage.Snapshot().ProviderCalls != 3 {
				t.Fatalf("missing identity masked invalid evidence: err=%v phase=%s calls=%d", terminal.Err, saved.Phase, usage.Snapshot().ProviderCalls)
			}
		})
	}
}

func TestVerificationMissingIdentitySupplementStops(t *testing.T) {
	unbound := strings.Replace(verificationPass, `"tool_call_id":"verify-command"`, `"tool_call_id":""`, 1)
	usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 7})
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	var saved verificationCursor
	_, terminal := collectEvents(t.Context(), QueryParams{
		Messages: []*schema.Message{schema.UserMessage("original contract")}, ToolRegistry: registry,
		RunUsage: usage, IndependentVerification: IndependentVerificationConfig{MaxTurns: 2, MaxRepairs: 3, MaxCoverageChecks: 1},
		ChatModel:                &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("done"), coverageRecoveryTool("verify-command"), verificationResponse(unbound), coverageRecoveryTool("fresh-check"), verificationResponse(unbound)}},
		ToolExecutor:             func(context.Context, string, string) (string, error) { return "fresh output", nil },
		commitVerificationCursor: func(c verificationCursor) error { saved = c; return nil },
	})
	if !errors.Is(terminal.Err, ErrIndependentVerification) || saved.Phase != "exhausted" || saved.Repairs != 0 || saved.CoverageChecks != 1 || usage.Snapshot().ProviderCalls != 5 {
		t.Fatal("repeated missing identities renewed the supplemental allowance")
	}
	encoded, _ := json.Marshal(saved.Diagnostics)
	if _, err := parseIndependentVerificationReport(string(encoded)); err != nil {
		t.Fatal(err)
	}
}
