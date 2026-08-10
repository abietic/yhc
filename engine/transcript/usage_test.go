package transcript

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestLoadFullAggregatesUsageAcrossCumulativeLifecycleSnapshot(t *testing.T) {
	recorder := NewRecorder("usage-session", t.TempDir())
	first := &schema.Message{
		Role: schema.Assistant, Content: "first",
		ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
			PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12,
		}},
	}
	if err := recorder.RecordMessages([]*schema.Message{first}); err != nil {
		t.Fatalf("record first response: %v", err)
	}
	snapshot := UsageSummary{}
	snapshot.ObserveMessage(first)
	if err := recorder.RecordLifecycleBoundaryWithUsage(
		LifecycleCompact,
		[]*schema.Message{{Role: schema.System, Extra: map[string]any{"subtype": "compact_boundary"}}},
		nil,
		nil,
		snapshot,
		true,
	); err != nil {
		t.Fatalf("record usage boundary: %v", err)
	}
	second := &schema.Message{
		Role: schema.Assistant, Content: "second",
		ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
			PromptTokens: 20, CompletionTokens: 3, TotalTokens: 23,
		}},
	}
	if err := recorder.RecordMessages([]*schema.Message{second}); err != nil {
		t.Fatalf("record second response: %v", err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatalf("flush transcript: %v", err)
	}

	loaded, err := recorder.LoadFull()
	if err != nil {
		t.Fatalf("LoadFull: %v", err)
	}
	if loaded.Usage.PromptTokens != 30 || loaded.Usage.CompletionTokens != 5 || loaded.Usage.TotalTokens != 35 {
		t.Fatalf("usage = %#v", loaded.Usage)
	}
	if loaded.Usage.Version != UsageSummaryVersion || loaded.Usage.UnsupportedSnapshotVersion != 0 {
		t.Fatalf("usage schema = %#v", loaded.Usage)
	}
	if loaded.Usage.ResponsesWithMetadata != 2 || loaded.Usage.ResponsesWithoutMetadata != 0 {
		t.Fatalf("coverage = %#v", loaded.Usage)
	}
	if loaded.Usage.LastPromptTokens != 20 || !loaded.Usage.LastResponseHadUsageMetadata {
		t.Fatalf("last usage = %#v", loaded.Usage)
	}
	if loaded.Usage.CurrentContextPromptTokens != 20 || !loaded.Usage.CurrentContextUsageKnown {
		t.Fatalf("current context usage = %#v", loaded.Usage)
	}
}

func TestLoadFullPreservesUnsupportedUsageSnapshotCoverage(t *testing.T) {
	recorder := NewRecorder("future-usage", t.TempDir())
	line, err := json.Marshal(recordEntry{
		Kind:  string(LifecycleCompact),
		Usage: &UsageSummary{Version: 99, PromptTokens: 999},
	})
	if err != nil {
		t.Fatalf("marshal future snapshot: %v", err)
	}
	if err := os.WriteFile(recorder.Path(), append(line, '\n'), 0o600); err != nil {
		t.Fatalf("write future snapshot: %v", err)
	}
	if err := recorder.RecordMessages([]*schema.Message{{
		Role: schema.Assistant, Content: "later",
		ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
			PromptTokens: 5, CompletionTokens: 1, TotalTokens: 6,
		}},
	}}); err != nil {
		t.Fatalf("record later response: %v", err)
	}

	loaded, err := recorder.LoadFull()
	if err != nil {
		t.Fatalf("LoadFull: %v", err)
	}
	if loaded.Usage.Version != UsageSummaryVersion || loaded.Usage.UnsupportedSnapshotVersion != 99 {
		t.Fatalf("usage schema coverage = %#v", loaded.Usage)
	}
	if loaded.Usage.PromptTokens != 5 || loaded.Usage.TotalTokens != 6 || loaded.Usage.ResponsesWithMetadata != 1 {
		t.Fatalf("future snapshot values were interpreted or later usage was lost: %#v", loaded.Usage)
	}
}

func TestLoadFullTreatsUnversionedUsageSnapshotAsLegacyCoverage(t *testing.T) {
	recorder := NewRecorder("unversioned-usage", t.TempDir())
	line, err := json.Marshal(recordEntry{
		Kind:  string(LifecycleCompact),
		Usage: &UsageSummary{PromptTokens: 999},
	})
	if err != nil {
		t.Fatalf("marshal unversioned snapshot: %v", err)
	}
	if err := os.WriteFile(recorder.Path(), append(line, '\n'), 0o600); err != nil {
		t.Fatalf("write unversioned snapshot: %v", err)
	}

	loaded, err := recorder.LoadFull()
	if err != nil {
		t.Fatalf("LoadFull: %v", err)
	}
	if loaded.Usage.Version != UsageSummaryVersion || loaded.Usage.LegacyBoundariesWithoutUsage != 1 {
		t.Fatalf("unversioned coverage = %#v", loaded.Usage)
	}
	if loaded.Usage.PromptTokens != 0 || loaded.Usage.TotalTokens != 0 {
		t.Fatalf("unversioned token fields were interpreted: %#v", loaded.Usage)
	}
}

func TestLoadFullLabelsLegacyUsageBoundaryWithoutGuessing(t *testing.T) {
	recorder := NewRecorder("legacy-usage", t.TempDir())
	if err := recorder.RecordLifecycleBoundary(
		LifecycleCheckpoint,
		[]*schema.Message{{Role: schema.Assistant, Content: "legacy response"}},
		nil,
		nil,
	); err != nil {
		t.Fatalf("record legacy boundary: %v", err)
	}
	loaded, err := recorder.LoadFull()
	if err != nil {
		t.Fatalf("LoadFull: %v", err)
	}
	if loaded.Usage.LegacyBoundariesWithoutUsage != 1 {
		t.Fatalf("legacy usage coverage = %#v", loaded.Usage)
	}
	if loaded.Usage.TotalTokens != 0 || loaded.Usage.ResponsesWithMetadata != 0 {
		t.Fatalf("legacy boundary invented usage: %#v", loaded.Usage)
	}
}

func TestUsageSummaryDistinguishesKnownZeroFromMissingMetadata(t *testing.T) {
	summary := UsageSummary{}
	summary.ObserveMessage(&schema.Message{
		Role: schema.Assistant, Content: "zero",
		ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{}},
	})
	if summary.ResponsesWithMetadata != 1 || summary.ResponsesWithoutMetadata != 0 || summary.TotalTokens != 0 || !summary.CurrentContextUsageKnown {
		t.Fatalf("known zero = %#v", summary)
	}
	summary.ObserveMessage(&schema.Message{Role: schema.Assistant, Content: "missing"})
	if summary.ResponsesWithoutMetadata != 1 || summary.LastResponseHadUsageMetadata || summary.CurrentContextUsageKnown {
		t.Fatalf("missing metadata = %#v", summary)
	}
	summary.ObserveMessage(&schema.Message{Role: schema.Assistant})
	if summary.ResponsesWithoutMetadata != 2 {
		t.Fatalf("empty provider response coverage = %#v", summary)
	}
	summary.ObserveMessage(&schema.Message{
		Role: schema.Assistant, Content: "provider failed",
		Extra: map[string]any{"api_error": true},
	})
	if summary.ResponsesWithoutMetadata != 2 {
		t.Fatalf("synthetic API error changed provider coverage = %#v", summary)
	}
}

func TestLoadFullRetainsEmptyAssistantMissingUsageCoverage(t *testing.T) {
	recorder := NewRecorder("empty-assistant-usage", t.TempDir())
	if err := recorder.RecordMessages([]*schema.Message{{
		Role:         schema.Assistant,
		ResponseMeta: &schema.ResponseMeta{FinishReason: "max_tokens"},
	}}); err != nil {
		t.Fatalf("record empty assistant: %v", err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatalf("flush transcript: %v", err)
	}

	loaded, err := recorder.LoadFull()
	if err != nil {
		t.Fatalf("LoadFull: %v", err)
	}
	if loaded.Usage.ResponsesWithoutMetadata != 1 || loaded.Usage.CurrentContextUsageKnown {
		t.Fatalf("empty provider response coverage = %#v", loaded.Usage)
	}
}

func TestUsageSummaryInvalidatesCurrentContextAfterCompactionUsage(t *testing.T) {
	summary := UsageSummary{}
	summary.ObserveMessage(&schema.Message{
		Role: schema.Assistant, Content: "main response",
		ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
			PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110,
		}},
	})
	summary.ObserveMessage(&schema.Message{
		Role: schema.System,
		Extra: map[string]any{
			"subtype":        "compact_boundary",
			"usage_expected": true,
		},
		ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
			PromptTokens: 80, CompletionTokens: 8, TotalTokens: 88,
		}},
	})

	if summary.TotalTokens != 198 || summary.ResponsesWithMetadata != 2 {
		t.Fatalf("cumulative compaction usage = %#v", summary)
	}
	if summary.CurrentContextUsageKnown || summary.CurrentContextPromptTokens != 0 {
		t.Fatalf("compaction usage became active context = %#v", summary)
	}
}
