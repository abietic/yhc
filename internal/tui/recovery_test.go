package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/abietic/yhc/engine"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type tuiRecoveryModel struct {
	mu        sync.Mutex
	responses []*schema.Message
	callIndex int
}

func (m *tuiRecoveryModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return m.next(), nil
}

func (m *tuiRecoveryModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{m.next()}), nil
}

func (m *tuiRecoveryModel) next() *schema.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.callIndex >= len(m.responses) {
		return &schema.Message{Role: schema.Assistant, Content: "done"}
	}
	response := m.responses[m.callIndex]
	m.callIndex++
	return response
}

func (m *tuiRecoveryModel) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callIndex
}

func TestTUIRecoveryCascadeRendersCompactionAndAnswer(t *testing.T) {
	const answer = "recovered TUI answer"
	chatModel := &tuiRecoveryModel{responses: []*schema.Message{
		tuiOverflowMessage("first 413"),
		tuiOverflowMessage("second 413"),
		{Role: schema.Assistant, Content: answer},
	}}
	dir := t.TempDir()
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:          "tui-recovery",
		TranscriptDir:      filepath.Join(dir, "transcripts"),
		CWD:                dir,
		CustomSystemPrompt: "test",
		Model:              "test-model",
		MaxTurns:           6,
		ChatModel:          chatModel,
	})
	t.Cleanup(eng.Close)
	eng.SetResumedMessages(tuiRecoveryHistory())

	app := New(Config{Engine: eng})
	events, _ := eng.SubmitMessage(context.Background(), "please answer")
	for event := range events {
		app.handleEngineEvent(event)
	}

	if chatModel.calls() != 3 {
		t.Fatalf("model calls = %d, want 3", chatModel.calls())
	}
	var recoveryMarkers []string
	var sawAnswer bool
	for _, item := range app.chat.items {
		switch typed := item.(type) {
		case *CompactBoundaryMessage:
			recoveryMarkers = append(recoveryMarkers, typed.stats)
		case *AssistantMessage:
			if strings.Contains(typed.content, answer) {
				sawAnswer = true
			}
		}
	}
	wantMarkers := []string{
		"Context overflow, retrying staged collapse",
		"Context overflow, compacting history",
	}
	if fmt.Sprint(recoveryMarkers) != fmt.Sprint(wantMarkers) {
		t.Fatalf("TUI recovery markers = %#v, want %#v", recoveryMarkers, wantMarkers)
	}
	if !sawAnswer {
		t.Fatalf("TUI did not render recovered answer %q: %#v", answer, app.chat.items)
	}
}

func tuiOverflowMessage(content string) *schema.Message {
	return &schema.Message{
		Role:    schema.Assistant,
		Content: content,
		Extra: map[string]any{
			"api_error":  true,
			"error_type": "413",
		},
	}
}

func tuiRecoveryHistory() []*schema.Message {
	history := make([]*schema.Message, 0, 8)
	for i := 0; i < 4; i++ {
		history = append(history,
			&schema.Message{Role: schema.User, Content: fmt.Sprintf("history user %d", i)},
			&schema.Message{Role: schema.Assistant, Content: fmt.Sprintf("history assistant %d", i)},
		)
	}
	return history
}
