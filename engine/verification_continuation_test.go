package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestVerificationContinuationStageAndFreshEvidence(t *testing.T) {
	for _, tc := range []struct {
		name       string
		firstLimit int64
		stale      bool
	}{
		{name: "before-check", firstLimit: 1},
		{name: "before-report", firstLimit: 2},
		{name: "old-receipt-rejected", firstLimit: 2, stale: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var saved []byte
			registry := tools.NewRegistry()
			tools.RegisterDefaults(registry)
			bash := func(id string) canonicalModelResponse {
				r := verificationResponse("")
				r.chunks[0].ToolCalls = []schema.ToolCall{{ID: id, Type: "function", Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"independent check"}`}}}
				return r
			}
			mdl := &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("solver claim"), bash("old-receipt")}}
			params := QueryParams{Messages: []*schema.Message{schema.UserMessage("ORIGINAL")}, SessionID: "fixture", verificationWorkspace: "/fixture", ChatModel: mdl, ToolRegistry: registry, IndependentVerification: IndependentVerificationConfig{MaxTurns: 2, MaxRepairs: 1}, ToolExecutor: func(context.Context, string, string) (string, error) { return "ok", nil }, commitVerificationCursor: func(c verificationCursor) error { saved, _ = json.Marshal(c); return nil }}
			usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: tc.firstLimit})
			params.RunUsage = usage
			_, terminal := collectEvents(t.Context(), params)
			if terminal.Err == nil || usage.Snapshot().ProviderCalls != uint64(tc.firstLimit) {
				t.Fatal("first invocation did not stop at the call boundary")
			}
			var cursor verificationCursor
			if json.Unmarshal(saved, &cursor) != nil || cursor.Phase != "check" || cursor.Repairs != 0 {
				t.Fatal("pending checker phase was lost")
			}
			report := strings.ReplaceAll(verificationPass, "verify-command", "new-receipt")
			if tc.stale {
				report = strings.ReplaceAll(report, "new-receipt", "old-receipt")
			}
			params.ChatModel = &canonicalScriptModel{responses: []canonicalModelResponse{bash("new-receipt"), verificationResponse(report)}}
			params.Messages = append(params.Messages, &schema.Message{Role: schema.User, Content: "CONTINUATION_CONTROL", Extra: map[string]any{"is_meta": true}})
			params.independentVerificationContinuation = true
			params.loadVerificationCursor = func() (*verificationCursor, error) {
				var c verificationCursor
				err := json.Unmarshal(saved, &c)
				return &c, err
			}
			usage, _ = execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 2})
			params.RunUsage = usage
			params.Deps = &QueryDeps{ProviderUsage: usage, CallModel: func(ctx context.Context, m model.BaseChatModel, messages []*schema.Message, system *schema.Message, infos []*schema.ToolInfo, opts execution.CallModelOptions) (*execution.CallModelResult, error) {
				if opts.QuerySource != "independent_verification" {
					t.Error("resume repeated a solver completion before the pending check")
				}
				for _, message := range messages {
					if strings.Contains(message.Content, "CONTINUATION_CONTROL") {
						t.Error("checker received control text")
					}
				}
				return execution.CallModel(ctx, m, messages, system, infos, opts)
			}}
			_, terminal = collectEvents(t.Context(), params)
			if (terminal.Err != nil) != tc.stale || usage.Snapshot().ProviderCalls != 2 {
				t.Fatalf("resume terminal=%+v", terminal)
			}
			if tc.stale && !errors.Is(terminal.Err, ErrIndependentVerification) {
				t.Fatal("old receipt was not rejected by gate")
			}
		})
	}
}

func TestVerificationCursorIdentityAndFailureClosed(t *testing.T) {
	cfg := IndependentVerificationConfig{MaxTurns: 2, MaxRepairs: 1}
	params := QueryParams{SessionID: "fixture", verificationWorkspace: "/fixture", Messages: []*schema.Message{schema.UserMessage("ORIGINAL")}, IndependentVerification: cfg}
	usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 2})
	params.RunUsage = usage
	params.Deps = defaultDeps()
	if err := prepareIndependentVerification(&params); err != nil {
		t.Fatal(err)
	}
	cursor := params.independentVerification.cursor
	for _, tc := range []struct {
		name   string
		change func(*verificationCursor)
	}{
		{"version", func(c *verificationCursor) { c.Version = 2 }},
		{"session", func(c *verificationCursor) { c.SessionID = "other" }},
		{"workspace", func(c *verificationCursor) { c.Workspace = "/other" }},
		{"requirements", func(c *verificationCursor) { c.Requirements = "changed" }},
		{"config", func(c *verificationCursor) { c.MaxRepairs = 2 }},
		{"repair-count", func(c *verificationCursor) { c.Repairs = 2 }},
		{"completed", func(c *verificationCursor) { c.Phase = "completed" }},
		{"exhausted", func(c *verificationCursor) { c.Phase = "exhausted" }},
		{"repair-without-diagnostics", func(c *verificationCursor) { c.Phase = "repair"; c.Repairs = 1 }},
		{"historical-pass", func(c *verificationCursor) {
			r, _ := parseIndependentVerificationReport(verificationPass)
			c.Diagnostics = &r
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := cursor
			tc.change(&candidate)
			if candidate.validate(&params) == nil {
				t.Fatal("invalid cursor accepted")
			}
		})
	}
	params.commitVerificationCursor = func(verificationCursor) error { return errors.New("durability failure") }
	mdl := &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("must not dispatch")}}
	params.ChatModel = mdl
	_, terminal := collectEvents(t.Context(), params)
	if terminal.Reason != TerminalPersistenceError || !errors.Is(terminal.Err, errVerificationCheckpoint) || usage.Snapshot().ProviderCalls != 0 {
		t.Fatalf("checkpoint failure dispatched: %+v", terminal)
	}
}

func TestVerificationTransitionPersistenceFailureStopsNextDispatch(t *testing.T) {
	for _, tc := range []struct {
		name      string
		failAt    int
		wantCalls uint64
	}{{"before-check", 2, 1}, {"before-repair", 3, 3}, {"before-pass-publication", 3, 3}} {
		t.Run(tc.name, func(t *testing.T) {
			usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 8})
			registry := tools.NewRegistry()
			tools.RegisterDefaults(registry)
			bash := verificationResponse("")
			bash.chunks[0].ToolCalls = []schema.ToolCall{{ID: "verify-command", Type: "function", Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"independent check"}`}}}
			report := verificationFail
			if tc.name == "before-pass-publication" {
				report = verificationPass
			}
			mdl := &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("done"), bash, verificationResponse(report), verificationResponse("must not dispatch")}}
			commits := 0
			_, terminal := collectEvents(t.Context(), QueryParams{Messages: []*schema.Message{schema.UserMessage("ORIGINAL")}, ChatModel: mdl, RunUsage: usage, ToolRegistry: registry, IndependentVerification: IndependentVerificationConfig{MaxTurns: 2, MaxRepairs: 1}, ToolExecutor: func(context.Context, string, string) (string, error) { return "evidence", nil }, commitVerificationCursor: func(verificationCursor) error {
				commits++
				if commits == tc.failAt {
					return errors.New("durability failure")
				}
				return nil
			}})
			if terminal.Reason != TerminalPersistenceError || usage.Snapshot().ProviderCalls != tc.wantCalls {
				t.Fatalf("terminal=%+v calls=%d", terminal, usage.Snapshot().ProviderCalls)
			}
		})
	}
}

func TestVerificationCursorSurvivesBeforeAttachmentPublication(t *testing.T) {
	for _, phase := range []string{"check", "repair"} {
		t.Run(phase, func(t *testing.T) {
			dir := t.TempDir()
			e := &QueryEngine{transcript: transcript.NewRecorder("fixture", dir)}
			usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 6})
			params := QueryParams{SessionID: "fixture", verificationWorkspace: "/fixture", Messages: []*schema.Message{schema.UserMessage("ORIGINAL")}, IndependentVerification: IndependentVerificationConfig{MaxTurns: 2, MaxRepairs: 1}, RunUsage: usage, Deps: defaultDeps()}
			if err := prepareIndependentVerification(&params); err != nil {
				t.Fatal(err)
			}
			cursor := params.independentVerification.cursor
			cursor.Phase = phase
			if phase == "repair" {
				cursor.Repairs = 1
				r, _ := parseIndependentVerificationReport(verificationFail)
				cursor.Diagnostics = &r
			}
			if err := e.commitVerificationCursor(cursor); err != nil {
				t.Fatal(err)
			}
			if err := e.transcript.Close(); err != nil {
				t.Fatal(err)
			}
			// Reopen the real transcript before any diagnostic attachment was published.
			restarted := &QueryEngine{transcript: transcript.NewRecorder("fixture", dir)}
			t.Cleanup(func() { _ = restarted.transcript.Close() })
			params.loadVerificationCursor = restarted.loadVerificationCursor
			params.commitVerificationCursor = restarted.commitVerificationCursor
			params.independentVerificationContinuation = true
			params.Messages = []*schema.Message{schema.AssistantMessage("compacted solver claims", nil)}
			bash := verificationResponse("")
			bash.chunks[0].ToolCalls = []schema.ToolCall{{ID: "verify-command", Type: "function", Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"independent check"}`}}}
			responses := []canonicalModelResponse{bash, verificationResponse(verificationPass)}
			if phase == "repair" {
				responses = append([]canonicalModelResponse{verificationResponse("repaired")}, responses...)
			}
			params.ChatModel = &canonicalScriptModel{responses: responses}
			registry := tools.NewRegistry()
			tools.RegisterDefaults(registry)
			params.ToolRegistry = registry
			params.ToolExecutor = func(context.Context, string, string) (string, error) { return "ok", nil }
			params.Deps = &QueryDeps{ProviderUsage: usage, CallModel: func(ctx context.Context, m model.BaseChatModel, messages []*schema.Message, system *schema.Message, infos []*schema.ToolInfo, opts execution.CallModelOptions) (*execution.CallModelResult, error) {
				if opts.QuerySource != "independent_verification" {
					text := ""
					for _, x := range messages {
						text += x.Content
					}
					if !strings.Contains(text, "ORIGINAL") || !strings.Contains(text, `"verdict":"FAIL"`) {
						t.Error("repair lost frozen input or unpublished diagnostics")
					}
				}
				return execution.CallModel(ctx, m, messages, system, infos, opts)
			}}
			_, terminal := collectEvents(t.Context(), params)
			if terminal.Err != nil {
				t.Fatal(terminal.Err)
			}
			saved, err := restarted.loadVerificationCursor()
			if err != nil || saved.Phase != "completed" {
				t.Fatalf("completion not durable: %v", err)
			}
			if saved.validate(&params) == nil {
				t.Fatal("stored completion authorized another resume")
			}
			if err := restarted.transcript.RecordMetadata(verificationCursorMetadataName, `{"version":`); err != nil {
				t.Fatal(err)
			}
			if _, err := restarted.loadVerificationCursor(); err == nil {
				t.Fatal("malformed newest cursor was ignored")
			}
		})
	}
}
