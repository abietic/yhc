package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type captureInputModel struct {
	inputs [][]*schema.Message
	calls  int
}

func (m *captureInputModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *captureInputModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.calls++
	m.inputs = append(m.inputs, append([]*schema.Message(nil), input...))
	return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "after compact"}}), nil
}

func TestQueryAutoCompactUsesPostCompactMessages(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "2000")

	ctx := context.Background()
	model := &captureInputModel{}
	maxTurns := 4

	messages := []*schema.Message{
		{Role: schema.User, Content: strings.Repeat("older question ", 220)},
		{Role: schema.Assistant, Content: strings.Repeat("older answer ", 200)},
		{Role: schema.User, Content: "latest question"},
		{Role: schema.Assistant, Content: "latest answer"},
	}

	var events []QueryEvent
	terminal := Query(ctx, QueryParams{
		Messages:     messages,
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    model,
	}, func(evt QueryEvent) {
		events = append(events, evt)
	})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected completed terminal, got %q", terminal.Reason)
	}
	if model.calls < 1 || len(model.inputs) < 1 {
		t.Fatalf("expected at least one model call after auto-compact, got calls=%d inputs=%d", model.calls, len(model.inputs))
	}
	input := model.inputs[len(model.inputs)-1]
	if len(input) < 5 {
		t.Fatalf("expected compacted input with boundary/summary/tail, got %#v", input)
	}
	compactStart := -1
	for i, msg := range input {
		if msg != nil && msg.Extra != nil && msg.Extra["subtype"] == "compact_boundary" {
			compactStart = i
			break
		}
	}
	if compactStart == -1 {
		t.Fatalf("expected compact boundary somewhere in model input, got %#v", input)
	}
	if compactStart+3 >= len(input) {
		t.Fatalf("expected boundary/summary/tail subsequence in model input, got %#v", input)
	}
	if input[compactStart+1].Extra == nil || input[compactStart+1].Extra["subtype"] != "compact_summary" {
		t.Fatalf("expected summary after boundary in model input, got %#v", input[compactStart+1])
		return
	}
	if input[compactStart+2].Content != "latest question" || input[compactStart+3].Content != "latest answer" {
		t.Fatalf("expected preserved tail after summary, got %#v", input)
	}

	var compactBoundaryEvents []*schema.Message
	for _, evt := range events {
		if evt.Type == EventCompactBoundary && evt.CompactBoundaryMessage != nil {
			compactBoundaryEvents = append(compactBoundaryEvents, evt.CompactBoundaryMessage)
		}
	}
	if len(compactBoundaryEvents) < 2 {
		t.Fatalf("expected compact boundary events for emitted post-compact messages, got %#v", compactBoundaryEvents)
	}
	if compactBoundaryEvents[0].Extra == nil || compactBoundaryEvents[0].Extra["subtype"] != "compact_boundary" {
		t.Fatalf("expected first compact event to be boundary marker, got %#v", compactBoundaryEvents[0])
		return
	}
	if compactBoundaryEvents[1].Extra == nil || compactBoundaryEvents[1].Extra["subtype"] != "compact_summary" {
		t.Fatalf("expected second compact event to be summary, got %#v", compactBoundaryEvents[1])
		return
	}
}
