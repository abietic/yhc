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

const verificationPartial = `{"verdict":"PARTIAL","coverage_complete":false,"checks":[],"missing":["intermediate state not observed"]}`

func TestVerificationPartialDoesNotDispatchSolverRepair(t *testing.T) {
	usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 8})
	mdl := &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("done"), verificationResponse(verificationPartial), verificationResponse("blind repair")}}
	var saved verificationCursor
	_, terminal := collectEvents(context.Background(), QueryParams{
		Messages: []*schema.Message{schema.UserMessage("original contract")}, ChatModel: mdl, RunUsage: usage,
		IndependentVerification:  IndependentVerificationConfig{MaxTurns: 2, MaxRepairs: 1},
		commitVerificationCursor: func(c verificationCursor) error { saved = c; return nil },
	})
	if !errors.Is(terminal.Err, ErrIndependentVerification) || mdl.callCount != 2 || saved.Repairs != 0 || saved.Phase != "exhausted" {
		t.Fatalf("coverage gap triggered solver repair: terminal=%+v calls=%d repairs=%d phase=%s", terminal, mdl.callCount, saved.Repairs, saved.Phase)
	}
}

func stageCheck(id string) canonicalModelResponse {
	r := verificationResponse("")
	r.chunks[0].ToolCalls = []schema.ToolCall{{ID: id, Type: "function", Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"independent check"}`}}}
	return r
}

func TestVerificationCoverageStageRouting(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reports []string
		sources []string
		repairs int
		failed  bool
	}{
		{"partial-pass", []string{verificationPartial, verificationPass}, []string{"", "independent_verification", "independent_verification", "independent_verification"}, 0, false},
		{"partial-no-progress", []string{verificationPartial, verificationPartial}, []string{"", "independent_verification", "independent_verification"}, 0, true},
		{"counterexample-after-gap", []string{verificationPartial, verificationFail, "repaired", verificationPass}, []string{"", "independent_verification", "independent_verification", "independent_verification", "", "independent_verification", "independent_verification"}, 1, false},
		{"allowance-not-reset-by-repair", []string{verificationPartial, verificationFail, "repaired", verificationPartial}, []string{"", "independent_verification", "independent_verification", "independent_verification", "", "independent_verification"}, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mdl := &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("done")}}
			for _, report := range tc.reports {
				if report == verificationPass || report == verificationFail {
					mdl.responses = append(mdl.responses, stageCheck("verify-command"))
				}
				mdl.responses = append(mdl.responses, verificationResponse(report))
			}
			usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 12})
			registry := tools.NewRegistry()
			tools.RegisterDefaults(registry)
			var saved verificationCursor
			var sources []string
			params := QueryParams{
				Messages: []*schema.Message{schema.UserMessage("original contract")}, ChatModel: mdl, RunUsage: usage, ToolRegistry: registry,
				IndependentVerification:  IndependentVerificationConfig{MaxTurns: 2, MaxRepairs: 1, MaxCoverageChecks: 1},
				ToolExecutor:             func(context.Context, string, string) (string, error) { return "observed evidence", nil },
				commitVerificationCursor: func(c verificationCursor) error { saved = c; return nil },
				Deps: &QueryDeps{ProviderUsage: usage, CallModel: func(ctx context.Context, m model.BaseChatModel, msgs []*schema.Message, sys *schema.Message, infos []*schema.ToolInfo, opts execution.CallModelOptions) (*execution.CallModelResult, error) {
					sources = append(sources, opts.QuerySource)
					return execution.CallModel(ctx, m, msgs, sys, infos, opts)
				}},
			}
			events, terminal := collectEvents(t.Context(), params)
			if (terminal.Err != nil) != tc.failed || saved.Repairs != tc.repairs || saved.CoverageChecks != 1 || strings.Join(sources, ",") != strings.Join(tc.sources, ",") {
				t.Fatalf("routing: terminal=%+v repairs=%d coverage=%d sources=%v", terminal, saved.Repairs, saved.CoverageChecks, sources)
			}
			var summaries []IndependentVerificationSummary
			for _, event := range events {
				if event.AttachmentMessage != nil {
					if s, ok := event.AttachmentMessage.Extra["verification_summary"].(IndependentVerificationSummary); ok {
						summaries = append(summaries, s)
					}
				}
			}
			for i, s := range summaries {
				if s.Attempt != i+1 || (i > 0 && s.CoverageChecks != 1) {
					t.Fatalf("attempt or coverage accounting reset: %+v", summaries)
				}
			}
		})
	}
}

func TestVerificationCoverageStagePersistenceFailure(t *testing.T) {
	usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 8})
	mdl := &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("done"), verificationResponse(verificationPartial), verificationResponse("must not dispatch")}}
	_, terminal := collectEvents(t.Context(), QueryParams{
		Messages: []*schema.Message{schema.UserMessage("original")}, ChatModel: mdl, RunUsage: usage,
		IndependentVerification: IndependentVerificationConfig{MaxTurns: 2, MaxRepairs: 1, MaxCoverageChecks: 1},
		commitVerificationCursor: func(c verificationCursor) error {
			if c.Phase == "coverage" {
				return errors.New("fsync failed")
			}
			return nil
		},
	})
	if terminal.Reason != TerminalPersistenceError || mdl.callCount != 2 {
		t.Fatalf("checkpoint failure dispatched supplemental check: %+v calls=%d", terminal, mdl.callCount)
	}
}

func TestVerificationCoverageStageResumeAndFreshReceipts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit int64
		stale bool
	}{
		{"before-dispatch", 2, false},
		{"before-report", 3, false},
		{"malformed-after-dispatch", 4, false},
		{"old-receipt-rejected", 4, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := tools.NewRegistry()
			tools.RegisterDefaults(registry)
			usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: tc.limit})
			var saved verificationCursor
			params := QueryParams{
				SessionID: "fixture", verificationWorkspace: "/fixture", Messages: []*schema.Message{schema.UserMessage("original contract")}, RunUsage: usage, ToolRegistry: registry,
				IndependentVerification:  IndependentVerificationConfig{MaxTurns: 2, MaxRepairs: 1, MaxCoverageChecks: 1},
				ChatModel:                &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("done"), verificationResponse(verificationPartial), stageCheck("old-receipt"), verificationResponse("not valid JSON")}},
				ToolExecutor:             func(context.Context, string, string) (string, error) { return "observed", nil },
				commitVerificationCursor: func(c verificationCursor) error { saved = c; return nil },
			}
			_, terminal := collectEvents(t.Context(), params)
			if terminal.Err == nil || saved.Phase != "coverage" || saved.Repairs != 0 || saved.CoverageChecks != 0 || usage.Snapshot().ProviderCalls != uint64(tc.limit) {
				t.Fatalf("pending audit lost: %+v cursor=%+v", terminal, saved)
			}
			params.independentVerificationContinuation = true
			params.loadVerificationCursor = func() (*verificationCursor, error) { c := saved; return &c, nil }
			usage, _ = execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 2})
			params.RunUsage = usage
			params.Deps = &QueryDeps{ProviderUsage: usage}
			reportID := "new-receipt"
			if tc.stale {
				reportID = "old-receipt"
			}
			params.ChatModel = &canonicalScriptModel{responses: []canonicalModelResponse{stageCheck("new-receipt"), verificationResponse(strings.ReplaceAll(verificationPass, "verify-command", reportID))}}
			_, terminal = collectEvents(t.Context(), params)
			if tc.stale {
				if !errors.Is(terminal.Err, ErrIndependentVerification) || saved.Phase != "coverage" || saved.CoverageChecks != 0 || saved.Repairs != 0 {
					t.Fatalf("old receipt authorized completion or consumed allowance: %+v cursor=%+v", terminal, saved)
				}
				return
			}
			if terminal.Err != nil || saved.Phase != "completed" || saved.CoverageChecks != 1 || saved.Repairs != 0 {
				t.Fatalf("resume repeated solver or reset count: %+v cursor=%+v", terminal, saved)
			}
		})
	}
}

func TestVerificationCoverageCursorIdentityAndLegacy(t *testing.T) {
	usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 4})
	params := QueryParams{SessionID: "fixture", verificationWorkspace: "/fixture", Messages: []*schema.Message{schema.UserMessage("original")}, RunUsage: usage, Deps: defaultDeps(), IndependentVerification: IndependentVerificationConfig{MaxTurns: 2, MaxRepairs: 1}}
	if err := prepareIndependentVerification(&params); err != nil {
		t.Fatal(err)
	}
	legacy := params.independentVerification.cursor
	if err := legacy.validate(&params); err != nil {
		t.Fatal("zero-valued legacy cursor rejected", err)
	}
	legacy.Phase, legacy.Repairs = "repair", 1
	report, _ := parseIndependentVerificationReport(verificationPartial)
	legacy.Diagnostics = &report
	if err := legacy.validate(&params); err == nil {
		t.Fatal("legacy PARTIAL was authorized as a solver repair")
	}
	params.IndependentVerification.MaxCoverageChecks = 1
	if err := prepareIndependentVerification(&params); err != nil {
		t.Fatal(err)
	}
	valid := params.independentVerification.cursor
	valid.Phase, valid.Diagnostics = "coverage", &report
	if err := valid.validate(&params); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*verificationCursor)
	}{
		{"config", func(c *verificationCursor) { c.MaxCoverageChecks = 0 }},
		{"negative", func(c *verificationCursor) { c.CoverageChecks = -1 }},
		{"excess", func(c *verificationCursor) { c.CoverageChecks = 2 }},
		{"exhausted-pending", func(c *verificationCursor) { c.CoverageChecks = 1 }},
		{"missing-diagnostic", func(c *verificationCursor) { c.Diagnostics = nil }},
		{"fail-in-coverage", func(c *verificationCursor) {
			r, _ := parseIndependentVerificationReport(verificationFail)
			c.Diagnostics = &r
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := valid
			tc.change(&candidate)
			if candidate.validate(&params) == nil {
				t.Fatal("invalid supplemental cursor accepted")
			}
		})
	}
}

func TestVerificationCoverageStageCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 8})
	mdl := &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("done"), verificationResponse(verificationPartial), verificationResponse("must not dispatch")}}
	var saved verificationCursor
	_, terminal := collectEvents(ctx, QueryParams{
		Messages: []*schema.Message{schema.UserMessage("original")}, ChatModel: mdl, RunUsage: usage,
		IndependentVerification: IndependentVerificationConfig{MaxTurns: 2, MaxRepairs: 1, MaxCoverageChecks: 1},
		commitVerificationCursor: func(c verificationCursor) error {
			saved = c
			if c.Phase == "coverage" {
				cancel()
			}
			return nil
		},
	})
	if !errors.Is(terminal.Err, context.Canceled) || saved.Phase != "coverage" || saved.CoverageChecks != 0 || saved.Repairs != 0 || mdl.callCount != 2 {
		t.Fatalf("cancelled pending supplement dispatched or lost state: %+v cursor=%+v calls=%d", terminal, saved, mdl.callCount)
	}
}
