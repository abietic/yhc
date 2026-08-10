package compact

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestBuildPostCompactMessagesOrdersBoundarySummaryKeepTail(t *testing.T) {
	boundary := &schema.Message{Role: schema.System, Extra: map[string]any{"subtype": "compact_boundary"}}
	summary := &schema.Message{Role: schema.System, Content: "summary", Extra: map[string]any{"subtype": "compact_summary"}}
	kept := &schema.Message{Role: schema.User, Content: "latest user"}
	attachment := &schema.Message{Role: schema.Tool, Content: "attachment"}
	hook := &schema.Message{Role: schema.User, Content: "hook result"}

	got := BuildPostCompactMessages(&AutoCompactResult{
		BoundaryMarker:  boundary,
		SummaryMessages: []*schema.Message{summary},
		MessagesToKeep:  []*schema.Message{kept},
		Attachments:     []*schema.Message{attachment},
		HookResults:     []*schema.Message{hook},
	})

	if len(got) != 5 {
		t.Fatalf("expected 5 post-compact messages, got %d", len(got))
	}
	if got[0] != boundary || got[1] != summary || got[2] != kept || got[3] != attachment || got[4] != hook {
		t.Fatalf("unexpected post-compact ordering: %#v", got)
	}
}

func TestAutoCompactCompactsWhenAboveThreshold(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "2000")

	messages := []*schema.Message{
		{Role: schema.User, Content: strings.Repeat("old context ", 200)},
		{Role: schema.Assistant, Content: strings.Repeat("assistant reply ", 180)},
		{Role: schema.User, Content: "latest question"},
		{Role: schema.Assistant, Content: "latest answer"},
	}
	tracking := &CompactTracking{}

	result, failures, updated := AutoCompact(messages, "sdk", tracking, 0, "", nil)
	if result == nil {
		t.Fatal("expected auto-compact result when above threshold")
		return
	}
	if failures != 0 {
		t.Fatalf("expected zero failures after successful compaction, got %d", failures)
	}
	if updated == nil || !updated.Compacted {
		t.Fatalf("expected tracking to mark compaction, got %#v", updated)
		return
	}
	post := BuildPostCompactMessages(result)
	if len(post) < 3 {
		t.Fatalf("expected boundary, summary, and preserved tail, got %#v", post)
	}
	if post[0].Extra == nil || post[0].Extra["subtype"] != "compact_boundary" {
		t.Fatalf("expected boundary marker first, got %#v", post[0])
		return
	}
	if post[1].Extra == nil || post[1].Extra["subtype"] != "compact_summary" {
		t.Fatalf("expected compact summary second, got %#v", post[1])
		return
	}
	if post[2].Content != "latest question" {
		t.Fatalf("expected preserved tail after summary, got %#v", post)
	}
	if result.PostCompactTokenCount >= result.PreCompactTokenCount {
		t.Fatalf("expected compaction to reduce estimated tokens, pre=%d post=%d", result.PreCompactTokenCount, result.PostCompactTokenCount)
	}
}

func TestAutoCompactSkipsCompactQuerySource(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "2000")

	messages := []*schema.Message{{Role: schema.User, Content: strings.Repeat("context ", 400)}}
	result, _, updated := AutoCompact(messages, "compact", &CompactTracking{}, 0, "", nil)
	if result != nil {
		t.Fatalf("expected no compaction for compact query source, got %#v", result)
		return
	}
	if updated == nil || updated.Compacted {
		t.Fatalf("expected tracking to remain uncompacted, got %#v", updated)
		return
	}
}

func TestAutoCompactCircuitBreakerSkipsAfterFailures(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "2000")

	messages := []*schema.Message{{Role: schema.User, Content: strings.Repeat("context ", 400)}}
	tracking := &CompactTracking{ConsecutiveFailures: 3}
	result, failures, updated := AutoCompact(messages, "sdk", tracking, 0, "", nil)
	if result != nil {
		t.Fatalf("expected circuit breaker to skip compaction, got %#v", result)
		return
	}
	if failures != 3 {
		t.Fatalf("expected failure count to be preserved, got %d", failures)
	}
	if updated == nil || updated.Compacted {
		t.Fatalf("expected tracking to remain uncompacted, got %#v", updated)
		return
	}
}

func TestAutoCompactUsesModelContextWindow(t *testing.T) {
	// With default window (200k), these messages (~600 tokens) won't trigger compaction.
	messages := []*schema.Message{
		{Role: schema.User, Content: strings.Repeat("word ", 500)},
		{Role: schema.Assistant, Content: "reply"},
	}
	tracking := &CompactTracking{}

	// Default 200k window: should NOT compact (tokens way below threshold)
	result, _, _ := AutoCompact(messages, "sdk", tracking, 0, "", nil)
	if result != nil {
		t.Fatal("expected no compaction with default 200k window for small messages")
		return
	}

	// Now use env override to simulate a small window where these messages DO exceed
	// the auto-compact threshold (window - 13000) but NOT the blocking limit (window - 3000).
	// Messages are ~650 tokens. Window=5000 → threshold=max(0, 5000-13000)=0 → triggers,
	// blocking_limit=5000-3000=2000 → 650 < 2000 so NOT blocking.
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "5000")
	tracking2 := &CompactTracking{}
	result2, _, updated2 := AutoCompact(messages, "sdk", tracking2, 0, "gpt-4", nil)
	if result2 == nil {
		t.Fatal("expected compaction when model window is small and messages exceed threshold")
		return
	}
	if !updated2.Compacted {
		t.Fatal("expected tracking to mark compaction")
	}
}

func TestGetEffectiveContextWindowSizeModelLookup(t *testing.T) {
	// Known model should return its specific window
	got := GetEffectiveContextWindowSize("gpt-4")
	if got != 8192 {
		t.Fatalf("expected 8192 for gpt-4, got %d", got)
	}

	// Case-insensitive
	got = GetEffectiveContextWindowSize("GPT-4-Turbo")
	if got != 128000 {
		t.Fatalf("expected 128000 for GPT-4-Turbo, got %d", got)
	}

	// DeepSeek V4 advertises a native provider-neutral 1M context window.
	got = GetEffectiveContextWindowSize("deepseek-v4-pro")
	if got != 1000000 {
		t.Fatalf("expected 1000000 for deepseek-v4-pro, got %d", got)
	}

	// Explicit context suffixes apply to any provider, not only Anthropic.
	got = GetEffectiveContextWindowSize("custom-provider-model[1m]")
	if got != 1000000 {
		t.Fatalf("expected explicit provider-neutral 1M window, got %d", got)
	}

	// Unknown model falls back to default
	got = GetEffectiveContextWindowSize("unknown-model-xyz")
	if got != defaultEffectiveContextWindow {
		t.Fatalf("expected default %d for unknown model, got %d", defaultEffectiveContextWindow, got)
	}

	// Empty model falls back to default
	got = GetEffectiveContextWindowSize("")
	if got != defaultEffectiveContextWindow {
		t.Fatalf("expected default %d for empty model, got %d", defaultEffectiveContextWindow, got)
	}

	// Env override caps the window
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "50000")
	got = GetEffectiveContextWindowSize("claude-sonnet-4-20250514")
	if got != 50000 {
		t.Fatalf("expected env override 50000, got %d", got)
	}
}

func TestP292TokenWarningUsesAuthoritativeContextWindow(t *testing.T) {
	t.Parallel()

	window := 24000
	state := CalculateTokenWarningStateForContextWindow(20000, "gpt-5", &window)
	if !state.IsAboveWarningThreshold ||
		!state.IsAboveAutoCompactThreshold ||
		state.IsAtBlockingLimit {
		t.Fatalf("warning state = %#v", state)
	}
	state = CalculateTokenWarningStateForContextWindow(21000, "gpt-5", &window)
	if !state.IsAtBlockingLimit {
		t.Fatalf("blocking state = %#v", state)
	}
}
