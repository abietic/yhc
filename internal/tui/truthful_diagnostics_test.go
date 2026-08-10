package tui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/transcript"
)

func TestStatusAndSpinnerUseProviderUsageWithoutGenericPricing(t *testing.T) {
	root := t.TempDir()
	sessionID := "truthful-tui-usage"
	recorder := transcript.NewRecorder(sessionID, root)
	if err := recorder.RecordMessages([]*schema.Message{
		{Role: schema.User, Content: strings.Repeat("x", 400000)},
		{
			Role:    schema.Assistant,
			Content: "provider response",
			ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
				PromptTokens: 32000, CompletionTokens: 10000, TotalTokens: 42000,
			}},
		},
	}); err != nil {
		t.Fatalf("record transcript: %v", err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatalf("flush transcript: %v", err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}

	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:     sessionID,
		TranscriptDir: root,
		CWD:           filepath.Join(root, "workspace"),
		Model:         "gpt-4o",
	})
	defer eng.Close()
	app := newTestApp(180, 30)
	app.model = "gpt-4o"
	app.SetEngine(eng)

	status := stripANSIForTest(app.renderStatus())
	if !strings.Contains(status, "32.0k tokens (25%)") {
		t.Fatalf("status does not show exact provider context usage: %q", status)
	}
	if strings.Contains(status, "$") {
		t.Fatalf("status renders generic price estimate: %q", status)
	}
	if got := app.spinnerTokens(); got != "↑32k ↓10k" {
		t.Fatalf("spinner usage = %q, want exact provider totals", got)
	}
}

func TestStatusDoesNotEstimateContextFromMessageSize(t *testing.T) {
	root := t.TempDir()
	sessionID := "missing-tui-usage"
	recorder := transcript.NewRecorder(sessionID, root)
	if err := recorder.RecordMessages([]*schema.Message{
		{Role: schema.User, Content: strings.Repeat("x", 400000)},
		{Role: schema.Assistant, Content: strings.Repeat("y", 400000)},
	}); err != nil {
		t.Fatalf("record transcript: %v", err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatalf("flush transcript: %v", err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}

	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID: sessionID, TranscriptDir: root, CWD: root, Model: "gpt-4o",
	})
	defer eng.Close()
	app := newTestApp(180, 30)
	app.SetEngine(eng)

	status := stripANSIForTest(app.renderStatus())
	if strings.Contains(status, "tokens (") {
		t.Fatalf("status estimates context from message size: %q", status)
	}
	if got := app.spinnerTokens(); got != "" {
		t.Fatalf("spinner estimates missing provider usage: %q", got)
	}
}

func TestAutoCompactBoundaryDoesNotInventPostCompactUsage(t *testing.T) {
	root := t.TempDir()
	sessionID := "compact-boundary-usage"
	recorder := transcript.NewRecorder(sessionID, root)
	if err := recorder.RecordMessages([]*schema.Message{{
		Role: schema.Assistant, Content: "before compact",
		ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
			PromptTokens: 64000, CompletionTokens: 1000, TotalTokens: 65000,
		}},
	}}); err != nil {
		t.Fatalf("record usage: %v", err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatalf("flush usage: %v", err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("close usage: %v", err)
	}

	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID: sessionID, TranscriptDir: root, CWD: root, Model: "gpt-4o",
	})
	defer eng.Close()
	app := newTestApp(180, 30)
	app.SetEngine(eng)
	app.chat.AppendUser("one")
	app.chat.AppendSystem("two")

	app.handleEngineEvent(engine.QueryEvent{
		Type: engine.EventCompactBoundary,
		CompactBoundaryMessage: &schema.Message{
			Role:  schema.System,
			Extra: map[string]any{"subtype": "compact_boundary"},
		},
	})
	marker, ok := app.chat.items[len(app.chat.items)-1].(*CompactBoundaryMessage)
	if !ok {
		t.Fatalf("last item = %T, want compact boundary", app.chat.items[len(app.chat.items)-1])
	}
	if marker.messagesCompacted != 0 || marker.tokensBefore != 0 ||
		marker.tokensAfter != 0 || marker.contextPercent != 0 {
		t.Fatalf("auto compact rendered inferred metrics: %#v", marker)
	}
	rendered := stripANSIForTest(marker.Render(100, app.styles))
	for _, forbidden := range []string{"tokens freed", "context now", "messages removed"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("auto compact rendered %q without a source fact: %q", forbidden, rendered)
		}
	}
}
