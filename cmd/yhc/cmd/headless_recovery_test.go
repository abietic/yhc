package cmd

import (
	"bytes"
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

type headlessRecoveryModel struct {
	mu        sync.Mutex
	responses []*schema.Message
	callIndex int
}

func (m *headlessRecoveryModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return m.next(), nil
}

func (m *headlessRecoveryModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{m.next()}), nil
}

func (m *headlessRecoveryModel) next() *schema.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.callIndex >= len(m.responses) {
		return &schema.Message{Role: schema.Assistant, Content: "done"}
	}
	response := m.responses[m.callIndex]
	m.callIndex++
	return response
}

func (m *headlessRecoveryModel) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callIndex
}

func TestHeadlessRecoveryCascadeReportsCompactionAndAnswer(t *testing.T) {
	const answer = "recovered headless answer"
	chatModel := &headlessRecoveryModel{responses: []*schema.Message{
		headlessOverflowMessage("first 413"),
		headlessOverflowMessage("second 413"),
		{Role: schema.Assistant, Content: answer},
	}}
	dir := t.TempDir()
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:          "headless-recovery",
		TranscriptDir:      filepath.Join(dir, "transcripts"),
		CWD:                dir,
		CustomSystemPrompt: "test",
		Model:              "test-model",
		MaxTurns:           6,
		ChatModel:          chatModel,
	})
	t.Cleanup(eng.Close)
	eng.SetResumedMessages(recoveryHistory())

	events, _ := eng.SubmitMessage(context.Background(), "please answer")
	var stdout, stderr bytes.Buffer
	if err := consumeHeadlessEvents(&stdout, &stderr, events); err != nil {
		t.Fatal(err)
	}
	if chatModel.calls() != 3 {
		t.Fatalf("model calls = %d, want 3", chatModel.calls())
	}
	if strings.Count(stderr.String(), "--- context compacted ---") != 2 {
		t.Fatalf("missing headless compaction marker: %q", stderr.String())
	}
	if !strings.Contains(stdout.String(), answer) || !strings.HasSuffix(stdout.String(), "\n") {
		t.Fatalf("unexpected headless output: %q", stdout.String())
	}
}

func TestHeadlessProjectsTypedCommandResult(t *testing.T) {
	dir := t.TempDir()
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:     "headless-command-visibility",
		CWD:           dir,
		TranscriptDir: filepath.Join(dir, "transcripts"),
	})
	t.Cleanup(eng.Close)

	events, _ := eng.SubmitMessage(context.Background(), "/clear")
	var stdout, stderr bytes.Buffer
	if err := consumeHeadlessEvents(&stdout, &stderr, events); err != nil {
		t.Fatalf("consume command events: %v", err)
	}
	if !strings.Contains(stdout.String(), "Conversation already empty.") {
		t.Fatalf("headless did not project typed command result: %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("headless command emitted unexpected error output: %q", stderr.String())
	}
}

func headlessOverflowMessage(content string) *schema.Message {
	return &schema.Message{
		Role:    schema.Assistant,
		Content: content,
		Extra: map[string]any{
			"api_error":  true,
			"error_type": "413",
		},
	}
}

func recoveryHistory() []*schema.Message {
	history := make([]*schema.Message, 0, 8)
	for i := 0; i < 4; i++ {
		history = append(history,
			&schema.Message{Role: schema.User, Content: fmt.Sprintf("history user %d", i)},
			&schema.Message{Role: schema.Assistant, Content: fmt.Sprintf("history assistant %d", i)},
		)
	}
	return history
}
