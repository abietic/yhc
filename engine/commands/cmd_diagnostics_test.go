package commands

import (
	"context"
	"strings"
	"testing"
	"time"

	enginediagnostics "github.com/abietic/yhc/engine/diagnostics"
)

type fixedDiagnosticsEngine struct {
	snapshot enginediagnostics.Snapshot
	err      error
}

func (e fixedDiagnosticsEngine) DiagnosticsSnapshot(context.Context) (enginediagnostics.Snapshot, error) {
	return e.snapshot, e.err
}

func TestDiagnosticCommandsRenderOneSourceDerivedSnapshot(t *testing.T) {
	snapshot := diagnosticCommandFixture()
	ctx := &CommandContext{
		Context: context.Background(),
		Engine:  fixedDiagnosticsEngine{snapshot: snapshot},
	}
	tests := []struct {
		name    string
		execute func(*CommandContext, string) (*CommandResult, error)
		wants   []string
	}{
		{name: "status", execute: executeStatus, wants: []string{"Session Status", "source=engine.session", "Details: /context  /usage  /config  /doctor"}},
		{name: "context", execute: executeContextCommand, wants: []string{"Context Diagnostics", "Current input tokens:", "source=provider-response-meta"}},
		{name: "usage", execute: executeUsage, wants: []string{"Persisted Provider Usage", "Responses with metadata:", "Money is omitted"}},
		{name: "config", execute: executeConfig, wants: []string{"Effective Configuration", "Credential:", "configured", "source=env:OPENAI_API_KEY"}},
		{name: "doctor", execute: executeDoctor, wants: []string{"[pass] runtime.engine", "[skipped] provider.connectivity"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := test.execute(ctx, "")
			if err != nil {
				t.Fatalf("execute error = %v", err)
			}
			for _, want := range test.wants {
				if !strings.Contains(result.Output, want) {
					t.Fatalf("output missing %q:\n%s", want, result.Output)
				}
			}
			for _, forbidden := range []string{"secret-value", "$0.", "estimated", "****"} {
				if strings.Contains(result.Output, forbidden) {
					t.Fatalf("output contains forbidden value %q:\n%s", forbidden, result.Output)
				}
			}
		})
	}
}

func TestDiagnosticCompatibilityAliasesAreRemoved(t *testing.T) {
	registry := NewRegistry()
	RegisterDefaults(registry)
	ctx := &CommandContext{Engine: fixedDiagnosticsEngine{snapshot: diagnosticCommandFixture()}}

	for _, input := range []string{"/stats", "/cost", "/settings"} {
		alias, err := registry.Dispatch(context.Background(), EntrypointHeadless, ctx, input)
		if err != nil {
			t.Fatalf("dispatch %s: %v", input, err)
		}
		if alias.Removed == nil || alias.Action != ActionNone {
			t.Fatalf("%s should be a tombstone: %#v", input, alias)
		}
	}
}

func TestLoginIsHiddenWithoutOwnedAuthenticationFlow(t *testing.T) {
	registry := NewRegistry()
	RegisterDefaults(registry)
	if got := registry.GetFor(EntrypointTUI, "login"); got != nil {
		t.Fatalf("login unexpectedly discoverable: %#v", got)
	}
	result, err := registry.Dispatch(context.Background(), EntrypointTUI, &CommandContext{}, "/login")
	if err != nil {
		t.Fatalf("hidden login dispatch error = %v", err)
	}
	if result.Removed == nil || result.Action != ActionNone {
		t.Fatalf("hidden login result = %#v", result)
	}
}

func diagnosticCommandFixture() enginediagnostics.Snapshot {
	observedAt := time.Date(2026, 7, 22, 2, 0, 0, 0, time.UTC)
	meta := func(source string) enginediagnostics.FieldMeta {
		return enginediagnostics.FieldMeta{
			State: enginediagnostics.StateKnown, Source: source, ObservedAt: observedAt,
		}
	}
	text := func(value, source string) enginediagnostics.StringField {
		return enginediagnostics.StringField{FieldMeta: meta(source), Value: value}
	}
	integer := func(value int, source string) enginediagnostics.IntField {
		return enginediagnostics.IntField{FieldMeta: meta(source), Value: value}
	}
	snapshot := enginediagnostics.Snapshot{ObservedAt: observedAt}
	snapshot.Status = enginediagnostics.StatusSnapshot{
		SessionID: text("session-1", "engine.session"), Model: text("gpt-4o", "provider-resolver"),
		Provider: text("agenticopenai", "env:PROV"), CWD: text("/workspace", "engine.cwd"),
		MessageCount: integer(4, "engine.active-messages"), ToolCount: integer(8, "engine.tool-registry"),
		Transcript: text("readable", "transcript-jsonl"), UsageCoverage: text("complete", "transcript-response-meta"),
		ContextPercent: integer(25, "provider-response-meta + model-capability-table"),
	}
	snapshot.Context = enginediagnostics.ContextSnapshot{
		Model: text("gpt-4o", "provider-resolver"), ContextWindowTokens: integer(128000, "model-capability-table"),
		CurrentInputTokens: integer(32000, "provider-response-meta"), UsagePercent: integer(25, "provider-response-meta"),
		TotalMessages: integer(4, "engine.active-messages"), UserMessages: integer(1, "engine.active-messages"),
		AssistantMessages: integer(1, "engine.active-messages"), ToolMessages: integer(1, "engine.active-messages"),
		SystemMessages: integer(1, "engine.active-messages"), ToolCalls: integer(1, "engine.active-messages"),
		CompactionBoundaries: integer(0, "transcript-lifecycle"),
		TransientContributors: enginediagnostics.StringField{FieldMeta: enginediagnostics.FieldMeta{
			State: enginediagnostics.StateUnavailable, Source: "model-call-assembly", ObservedAt: observedAt,
		}},
	}
	snapshot.Usage = enginediagnostics.UsageSnapshot{
		PromptTokens: integer(32000, "transcript-response-meta"), CompletionTokens: integer(1000, "transcript-response-meta"),
		TotalTokens: integer(33000, "transcript-response-meta"), ResponsesWithMetadata: integer(1, "transcript-response-meta"),
		ResponsesWithoutMetadata: integer(0, "transcript-response-meta"), Coverage: text("complete", "transcript-response-meta"),
	}
	snapshot.Config = enginediagnostics.ConfigSnapshot{
		Provider: text("agenticopenai", "env:PROV"), Model: text("gpt-4o", "env:PROV_MODEL"),
		CredentialConfigured: enginediagnostics.BoolField{FieldMeta: meta("env:OPENAI_API_KEY"), Value: true},
		Endpoint:             text("https://api.openai.com", "provider-default"), PermissionMode: text("default", "engine.permission-mode"),
		Precedence: text("field-specific", "provider-resolution-policy"),
	}
	snapshot.Doctor.Checks = []enginediagnostics.DoctorCheck{
		{ID: "runtime.engine", Outcome: enginediagnostics.CheckPass, FieldMeta: meta("query-engine"), Summary: "available"},
		{ID: "provider.connectivity", Outcome: enginediagnostics.CheckSkipped, FieldMeta: enginediagnostics.FieldMeta{
			State: enginediagnostics.StateUnavailable, Source: "read-only-diagnostics", ObservedAt: observedAt,
		}, Summary: "not tested"},
	}
	return snapshot
}
