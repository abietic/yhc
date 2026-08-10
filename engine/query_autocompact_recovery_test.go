package engine

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/abietic/yhc/engine/transcript"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type autoCompactRestartCaptureModel struct {
	mu     sync.Mutex
	inputs [][]*schema.Message
}

func (*autoCompactRestartCaptureModel) Generate(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "unused-generate-sentinel"}, nil
}

func (m *autoCompactRestartCaptureModel) Stream(
	_ context.Context,
	input []*schema.Message,
	_ ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	m.inputs = append(m.inputs, autoCompactRestartCloneMessages(input))
	m.mu.Unlock()
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role: schema.Assistant, Content: "restart-model-answer-sentinel",
	}}), nil
}

func (m *autoCompactRestartCaptureModel) inputsSnapshot() [][]*schema.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	inputs := make([][]*schema.Message, len(m.inputs))
	for index := range m.inputs {
		inputs[index] = autoCompactRestartCloneMessages(m.inputs[index])
	}
	return inputs
}

func autoCompactRestartCloneMessages(messages []*schema.Message) []*schema.Message {
	cloned := make([]*schema.Message, len(messages))
	for index, message := range messages {
		cloned[index] = cloneMessage(message)
		if cloned[index] != nil {
			cloned[index].Extra = cloneMessageExtra(message.Extra)
		}
	}
	return cloned
}

type autoCompactRestartSummaryModel struct{}

func (*autoCompactRestartSummaryModel) Generate(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.Message, error) {
	return &schema.Message{
		Role:    schema.Assistant,
		Content: "<summary>durable-summary-sentinel</summary>",
	}, nil
}

func (m *autoCompactRestartSummaryModel) Stream(
	ctx context.Context,
	input []*schema.Message,
	opts ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func TestAutoCompactRestartUsesDurableBoundaryNotOriginalHistory(t *testing.T) {
	const sessionID = "auto-compact-restart-sentinel"
	const oldHistory = "raw-old-history-sentinel"
	const newestTail = "newest-tail-sentinel"
	const summary = "durable-summary-sentinel"
	const newPrompt = "fresh-restart-prompt-sentinel"
	t.Setenv("CLAUDE_AUTOCOMPACT_PCT_OVERRIDE", "1")

	root := t.TempDir()
	transcriptDir := t.TempDir()
	firstModel := &autoCompactRestartCaptureModel{}
	first := NewQueryEngine(QueryEngineConfig{
		SessionID:     sessionID,
		ThreadID:      sessionID,
		CWD:           root,
		TranscriptDir: transcriptDir,
		ChatModel:     firstModel,
		SummaryModel:  &autoCompactRestartSummaryModel{},
	})
	t.Cleanup(first.Close)
	first.SetResumedMessages([]*schema.Message{
		{Role: schema.User, Content: strings.Repeat(oldHistory+" ", 220)},
		{Role: schema.Assistant, Content: strings.Repeat("old-assistant-sentinel ", 200)},
		{Role: schema.User, Content: "tail-question-sentinel"},
		{Role: schema.Assistant, Content: newestTail},
	})
	firstEvents, firstAdmission := first.SubmitMessage(
		t.Context(),
		"first-turn-prompt-sentinel",
	)
	if firstAdmission.Err != nil {
		t.Fatalf("first submit admission: %v", firstAdmission.Err)
	}
	assertAutoCompactRestartTerminal(t, firstEvents)
	loaded, err := first.GetTranscript().LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	assertAutoCompactRestartDurableShape(t, loaded, oldHistory, summary, newestTail)
	first.Close()

	resumedModel := &autoCompactRestartCaptureModel{}
	resumed := NewQueryEngine(QueryEngineConfig{
		SessionID:     sessionID,
		ThreadID:      sessionID,
		CWD:           root,
		TranscriptDir: transcriptDir,
		ChatModel:     resumedModel,
	})
	t.Cleanup(resumed.Close)
	if _, err := resumed.ResumeSession(t.Context(), sessionID); err != nil {
		t.Fatalf("resume durable session: %v", err)
	}
	resumedEvents, resumedAdmission := resumed.SubmitMessage(t.Context(), newPrompt)
	if resumedAdmission.Err != nil {
		t.Fatalf("resumed submit admission: %v", resumedAdmission.Err)
	}
	assertAutoCompactRestartTerminal(t, resumedEvents)
	inputs := resumedModel.inputsSnapshot()
	if len(inputs) != 1 {
		t.Fatalf("resumed model calls = %d, want 1: %#v", len(inputs), inputs)
	}
	assertAutoCompactRestartModelInput(t, inputs[0], oldHistory, summary, newestTail, newPrompt)
}

func assertAutoCompactRestartTerminal(t *testing.T, events <-chan QueryEvent) {
	t.Helper()
	var terminal *Terminal
	for event := range events {
		if event.Type == EventTerminal {
			terminal = event.TerminalInfo
		}
	}
	if terminal == nil || terminal.Reason != TerminalCompleted {
		t.Fatalf("terminal = %#v", terminal)
	}
}

func assertAutoCompactRestartDurableShape(
	t *testing.T,
	loaded *transcript.LoadResult,
	oldHistory string,
	summary string,
	newestTail string,
) {
	t.Helper()
	if loaded == nil || len(loaded.Messages) < 3 {
		t.Fatalf("durable transcript = %#v", loaded)
	}
	var boundaryCount, summaryCount, tailCount, oldCount int
	for index, message := range loaded.Messages {
		if message == nil {
			continue
		}
		if message.Extra != nil && message.Extra["subtype"] == "compact_boundary" {
			boundaryCount++
			if index+1 >= len(loaded.Messages) ||
				loaded.Messages[index+1].Extra == nil ||
				loaded.Messages[index+1].Extra["subtype"] != "compact_summary" {
				t.Fatalf("boundary did not precede summary: %#v", loaded.Messages)
			}
		}
		if strings.Contains(message.Content, summary) {
			summaryCount++
		}
		oldCount += strings.Count(message.Content, oldHistory)
		if message.Content == newestTail {
			tailCount++
		}
	}
	if boundaryCount != 1 || summaryCount != 1 || tailCount != 1 || oldCount != 0 {
		t.Fatalf("durable shape boundary=%d summary=%d tail=%d old=%d messages=%#v", boundaryCount, summaryCount, tailCount, oldCount, loaded.Messages)
	}
}

func assertAutoCompactRestartModelInput(
	t *testing.T,
	input []*schema.Message,
	oldHistory string,
	summary string,
	newestTail string,
	newPrompt string,
) {
	t.Helper()
	if len(input) == 0 {
		t.Fatal("resumed provider input was empty")
	}
	var oldCount, summaryCount, tailCount, promptCount int
	for _, message := range input {
		if message == nil {
			continue
		}
		oldCount += strings.Count(message.Content, oldHistory)
		summaryCount += strings.Count(message.Content, summary)
		tailCount += strings.Count(message.Content, newestTail)
		promptCount += strings.Count(message.Content, newPrompt)
	}
	if oldCount != 0 || summaryCount != 1 || tailCount != 1 || promptCount != 1 {
		contents := make([]string, 0, len(input))
		for _, message := range input {
			if message != nil {
				contents = append(contents, string(message.Role)+":"+message.Content)
			}
		}
		t.Fatalf("resumed input old=%d summary=%d tail=%d prompt=%d messages=%#v", oldCount, summaryCount, tailCount, promptCount, contents)
	}
}
