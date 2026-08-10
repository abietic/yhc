package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	enginediagnostics "github.com/abietic/yhc/engine/diagnostics"
	"github.com/abietic/yhc/engine/provider"
	"github.com/abietic/yhc/engine/transcript"
)

type diagnosticsUsageModel struct{}

func (diagnosticsUsageModel) Generate(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.Message, error) {
	return diagnosticsUsageResponse(), nil
}

func (diagnosticsUsageModel) Stream(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{diagnosticsUsageResponse()}), nil
}

func diagnosticsUsageResponse() *schema.Message {
	return &schema.Message{
		Role: schema.Assistant, Content: "done",
		ResponseMeta: &schema.ResponseMeta{
			FinishReason: "stop",
			Usage: &schema.TokenUsage{
				PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12,
			},
		},
	}
}

func TestDiagnosticsSnapshotObservesFinalizedStreamUsageImmediately(t *testing.T) {
	root := t.TempDir()
	engine := NewQueryEngine(QueryEngineConfig{
		SessionID: "live-usage", TranscriptDir: root, CWD: root,
		Model: "gpt-4o", ChatModel: diagnosticsUsageModel{},
	})
	defer engine.Close()

	events, _ := engine.SubmitMessage(context.Background(), "hello")
	drainEngineEvents(t, events)

	snapshot, err := engine.DiagnosticsSnapshot(context.Background())
	if err != nil {
		t.Fatalf("DiagnosticsSnapshot: %v", err)
	}
	if snapshot.Usage.TotalTokens.State != enginediagnostics.StateKnown ||
		snapshot.Usage.TotalTokens.Value != 12 {
		t.Fatalf("live cumulative usage = %#v", snapshot.Usage.TotalTokens)
	}
	if snapshot.Context.CurrentInputTokens.State != enginediagnostics.StateKnown ||
		snapshot.Context.CurrentInputTokens.Value != 10 {
		t.Fatalf("live context usage = %#v", snapshot.Context.CurrentInputTokens)
	}
}

func TestCompactionUsageDoesNotRecountPreservedAssistantResponses(t *testing.T) {
	engine := NewQueryEngine(QueryEngineConfig{CWD: t.TempDir()})
	defer engine.Close()
	engine.observeCompactBoundaryUsageMessage(&schema.Message{
		Role: schema.System,
		Extra: map[string]any{
			"subtype":        "compact_boundary",
			"usage_expected": true,
		},
		ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
			PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12,
		}},
	})
	engine.observeCompactBoundaryUsageMessage(&schema.Message{
		Role: schema.Assistant, Content: "preserved tail",
		ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
			PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120,
		}},
	})

	usage := engine.providerUsageSummary()
	if usage.TotalTokens != 12 || usage.ResponsesWithMetadata != 1 {
		t.Fatalf("preserved assistant usage was recounted: %#v", usage)
	}
	if usage.CurrentContextUsageKnown {
		t.Fatalf("compaction usage became active context: %#v", usage)
	}
}

func TestDiagnosticsSnapshotUsesPersistedUsageAndRedactsProviderSecrets(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	sessionID := "diagnostic-session"
	recorder := transcript.NewRecorder(sessionID, root)
	if err := recorder.RecordMessages([]*schema.Message{
		{Role: schema.User, Content: "hello"},
		{
			Role: schema.Assistant, Content: "world",
			ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
				PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120,
			}},
		},
	}); err != nil {
		t.Fatalf("record messages: %v", err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatalf("flush transcript: %v", err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}

	projectConfigDir := filepath.Join(root, ".claude")
	if err := os.MkdirAll(projectConfigDir, 0o755); err != nil {
		t.Fatalf("create project config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectConfigDir, "settings.json"), []byte(`{"model":"gpt-4o"}`), 0o600); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectConfigDir, "settings.local.json"), []byte(`{"broken"`), 0o600); err != nil {
		t.Fatalf("write local config: %v", err)
	}

	secret := "secret-value-that-must-never-appear"
	clock := func() time.Time { return time.Date(2026, 7, 22, 3, 4, 5, 0, time.UTC) }
	engine := NewQueryEngine(QueryEngineConfig{
		SessionID: sessionID, TranscriptDir: root, CWD: root, Model: "gpt-4o", Clock: clock,
		ModelResolver: ModelResolverFunc(func(string) (provider.ResolvedConfig, error) {
			return provider.ResolvedConfig{
				Config: provider.Config{
					Provider: provider.ProviderAgenticOpenAI,
					Model:    "gpt-4o",
					APIKey:   secret,
					BaseURL:  "https://user:" + secret + "@example.com/v1/" + secret + "?token=" + secret + "#" + secret,
				},
				Sources: provider.ResolutionSources{
					Provider: "env:PROV", Model: "env:PROV_MODEL", APIKey: "env:OPENAI_API_KEY", BaseURL: "env:OPENAI_BASE_URL",
				},
			}, nil
		}),
	})
	defer engine.Close()

	snapshot, err := engine.DiagnosticsSnapshot(context.Background())
	if err != nil {
		t.Fatalf("DiagnosticsSnapshot: %v", err)
	}
	if snapshot.ObservedAt != clock() {
		t.Fatalf("observed at = %s, want %s", snapshot.ObservedAt, clock())
	}
	if snapshot.Usage.TotalTokens.State != enginediagnostics.StateKnown || snapshot.Usage.TotalTokens.Value != 120 {
		t.Fatalf("total usage = %#v", snapshot.Usage.TotalTokens)
	}
	if snapshot.Context.CurrentInputTokens.State != enginediagnostics.StateKnown || snapshot.Context.CurrentInputTokens.Value != 100 {
		t.Fatalf("current input = %#v", snapshot.Context.CurrentInputTokens)
	}
	if snapshot.Context.ContextWindowTokens.State != enginediagnostics.StateKnown || snapshot.Context.ContextWindowTokens.Value != 128000 {
		t.Fatalf("context window = %#v", snapshot.Context.ContextWindowTokens)
	}
	if percent, tokens := engine.GetContextUsage(); percent != 0 || tokens != 100 {
		t.Fatalf("live context usage = %d%%/%d, want exact 0%%/100", percent, tokens)
	}
	if !snapshot.Config.CredentialConfigured.Value || snapshot.Config.CredentialConfigured.Source != "env:OPENAI_API_KEY" {
		t.Fatalf("credential diagnostic = %#v", snapshot.Config.CredentialConfigured)
	}
	if snapshot.Config.Endpoint.Value != "https://example.com" {
		t.Fatalf("redacted endpoint = %q", snapshot.Config.Endpoint.Value)
	}

	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	for _, forbidden := range []string{secret, "user:", "/v1/", "token="} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("snapshot leaks %q: %s", forbidden, encoded)
		}
	}

	ids := make([]string, 0, len(snapshot.Doctor.Checks))
	for _, check := range snapshot.Doctor.Checks {
		ids = append(ids, check.ID)
	}
	wantIDs := []string{
		"runtime.engine", "provider.route", "provider.credential", "session.transcript",
		"config.user", "config.project", "config.local", "runtime.tools",
		"runtime.permission-mode", "provider.connectivity",
	}
	if !reflect.DeepEqual(ids, wantIDs) {
		t.Fatalf("doctor IDs = %v, want %v", ids, wantIDs)
	}
	if check := snapshot.Doctor.Checks[6]; check.Outcome != enginediagnostics.CheckFail || check.FieldMeta.State != enginediagnostics.StateKnown {
		t.Fatalf("invalid local config check = %#v", check)
	}
	if check := snapshot.Doctor.Checks[len(snapshot.Doctor.Checks)-1]; check.Outcome != enginediagnostics.CheckSkipped || check.FieldMeta.State != enginediagnostics.StateUnavailable {
		t.Fatalf("connectivity check = %#v", check)
	}
}

func TestDiagnosticsSnapshotMarksPartialAndCorruptUsageStale(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	sessionID := "partial-session"
	recorder := transcript.NewRecorder(sessionID, root)
	if err := recorder.RecordMessages([]*schema.Message{
		{Role: schema.User, Content: "question"},
		{Role: schema.Assistant, Content: "first", ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}}},
		{Role: schema.Assistant, Content: "second without usage"},
	}); err != nil {
		t.Fatalf("record messages: %v", err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatalf("flush transcript: %v", err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}
	file, err := os.OpenFile(recorder.Path(), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open transcript: %v", err)
	}
	if _, err := file.WriteString("not-json\n"); err != nil {
		t.Fatalf("append corruption: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close corrupt transcript: %v", err)
	}

	engine := NewQueryEngine(QueryEngineConfig{
		SessionID: sessionID, TranscriptDir: root, CWD: root, Model: "custom-model",
		ModelResolver: ModelResolverFunc(func(string) (provider.ResolvedConfig, error) {
			return provider.ResolvedConfig{
				Config:  provider.Config{Provider: provider.ProviderAgenticOpenAI, Model: "custom-model"},
				Sources: provider.ResolutionSources{Provider: "explicit", Model: "explicit"},
			}, nil
		}),
	})
	defer engine.Close()

	snapshot, err := engine.DiagnosticsSnapshot(context.Background())
	if err != nil {
		t.Fatalf("DiagnosticsSnapshot: %v", err)
	}
	if snapshot.Status.Transcript.State != enginediagnostics.StateStale {
		t.Fatalf("transcript state = %s", snapshot.Status.Transcript.State)
	}
	if snapshot.Usage.TotalTokens.State != enginediagnostics.StateStale || snapshot.Usage.TotalTokens.Value != 12 {
		t.Fatalf("partial usage = %#v", snapshot.Usage.TotalTokens)
	}
	if snapshot.Usage.ResponsesWithoutMetadata.Value != 1 {
		t.Fatalf("missing responses = %#v", snapshot.Usage.ResponsesWithoutMetadata)
	}
	if snapshot.Context.CurrentInputTokens.State != enginediagnostics.StateUnavailable {
		t.Fatalf("latest input state = %#v", snapshot.Context.CurrentInputTokens)
	}
	if snapshot.Context.ContextWindowTokens.State != enginediagnostics.StateUnavailable {
		t.Fatalf("unknown context window = %#v", snapshot.Context.ContextWindowTokens)
	}
	if percent, tokens := engine.GetContextUsage(); percent != 0 || tokens != 0 {
		t.Fatalf("missing latest provider usage must not be estimated: %d%%/%d", percent, tokens)
	}
}

func TestDiagnosticsSnapshotHonorsCancellation(t *testing.T) {
	engine := NewQueryEngine(QueryEngineConfig{CWD: t.TempDir()})
	defer engine.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := engine.DiagnosticsSnapshot(ctx); err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestUsageDiagnosticsRejectUnsupportedDurableSnapshotVersion(t *testing.T) {
	observedAt := time.Date(2026, 7, 22, 6, 0, 0, 0, time.UTC)
	snapshot := enginediagnostics.Snapshot{}
	populateUsageDiagnostics(
		&snapshot,
		&transcript.LoadResult{Usage: transcript.UsageSummary{
			Version: transcript.UsageSummaryVersion, UnsupportedSnapshotVersion: 99,
		}},
		enginediagnostics.FieldMeta{
			State: enginediagnostics.StateKnown, Source: "transcript-jsonl", ObservedAt: observedAt,
		},
		nil,
		observedAt,
	)
	if snapshot.Usage.TotalTokens.State != enginediagnostics.StateUnavailable ||
		!strings.Contains(snapshot.Usage.TotalTokens.Detail, "unsupported snapshot version 99") {
		t.Fatalf("unsupported usage diagnostic = %#v", snapshot.Usage.TotalTokens)
	}
}

func TestDiagnosticsSnapshotKeepsStableDoctorIDsWhenProviderRouteFails(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	engine := NewQueryEngine(QueryEngineConfig{
		SessionID: "unresolved-provider",
		CWD:       root,
		Model:     "missing-model",
		ModelResolver: ModelResolverFunc(func(string) (provider.ResolvedConfig, error) {
			return provider.ResolvedConfig{}, errors.New("route unavailable")
		}),
	})
	defer engine.Close()

	snapshot, err := engine.DiagnosticsSnapshot(context.Background())
	if err != nil {
		t.Fatalf("DiagnosticsSnapshot: %v", err)
	}
	ids := make([]string, 0, len(snapshot.Doctor.Checks))
	for _, check := range snapshot.Doctor.Checks {
		ids = append(ids, check.ID)
	}
	wantIDs := []string{
		"runtime.engine", "provider.route", "provider.credential", "session.transcript",
		"config.user", "config.project", "config.local", "runtime.tools",
		"runtime.permission-mode", "provider.connectivity",
	}
	if !reflect.DeepEqual(ids, wantIDs) {
		t.Fatalf("doctor IDs = %v, want %v", ids, wantIDs)
	}
	credential := snapshot.Doctor.Checks[2]
	if credential.Outcome != enginediagnostics.CheckSkipped || credential.FieldMeta.State != enginediagnostics.StateUnavailable {
		t.Fatalf("credential check = %#v", credential)
	}
	assertDiagnosticFieldStates(t, reflect.ValueOf(snapshot), "snapshot")
}

func assertDiagnosticFieldStates(t *testing.T, value reflect.Value, path string) {
	t.Helper()
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return
		}
		assertDiagnosticFieldStates(t, value.Elem(), path)
		return
	}
	switch value.Kind() {
	case reflect.Struct:
		if state := value.FieldByName("State"); state.IsValid() && state.Type() == reflect.TypeOf(enginediagnostics.FieldState("")) {
			switch actual := enginediagnostics.FieldState(state.String()); actual {
			case enginediagnostics.StateKnown,
				enginediagnostics.StateUnavailable,
				enginediagnostics.StateStale,
				enginediagnostics.StateRefreshing:
			default:
				t.Fatalf("%s has invalid diagnostic state %q", path, actual)
			}
			return
		}
		for i := 0; i < value.NumField(); i++ {
			field := value.Type().Field(i)
			assertDiagnosticFieldStates(t, value.Field(i), path+"."+field.Name)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < value.Len(); i++ {
			assertDiagnosticFieldStates(t, value.Index(i), fmt.Sprintf("%s[%d]", path, i))
		}
	}
}
