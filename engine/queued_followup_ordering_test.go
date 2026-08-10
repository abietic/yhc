package engine

import (
	"context"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type queuedFollowUpBarrierModel struct {
	firstStarted chan struct{}
	firstRelease chan struct{}

	mu     sync.Mutex
	inputs [][]*schema.Message
}

const (
	queuedFollowUpFirstAssistant  = "first-terminal-assistant-sentinel"
	queuedFollowUpSecondAssistant = "second-terminal-assistant-sentinel"
)

func (m *queuedFollowUpBarrierModel) Generate(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "unused-generate-sentinel"}, nil
}

func (m *queuedFollowUpBarrierModel) Stream(
	_ context.Context,
	input []*schema.Message,
	_ ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	m.inputs = append(m.inputs, append([]*schema.Message(nil), input...))
	call := len(m.inputs)
	m.mu.Unlock()
	if call == 1 {
		close(m.firstStarted)
		<-m.firstRelease
	}
	assistant := queuedFollowUpSecondAssistant
	if call == 1 {
		assistant = queuedFollowUpFirstAssistant
	}
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role: schema.Assistant, Content: assistant,
	}}), nil
}

func (m *queuedFollowUpBarrierModel) snapshot() [][]*schema.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	inputs := make([][]*schema.Message, len(m.inputs))
	for index := range m.inputs {
		inputs[index] = append([]*schema.Message(nil), m.inputs[index]...)
	}
	return inputs
}

func TestQueuedFollowUpStartsAfterPriorTerminalAndPersistsOnce(t *testing.T) {
	const firstPrompt = "first-user-sentinel"
	const followUpPrompt = "queued-follow-up-sentinel"
	model := &queuedFollowUpBarrierModel{
		firstStarted: make(chan struct{}),
		firstRelease: make(chan struct{}),
	}
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:     "queued-follow-up-ordering",
		ThreadID:      "queued-follow-up-ordering",
		CWD:           t.TempDir(),
		TranscriptDir: t.TempDir(),
		ChatModel:     model,
	})
	t.Cleanup(eng.Close)

	firstEvents, _ := eng.SubmitMessage(t.Context(), firstPrompt)
	<-model.firstStarted
	queued, err := eng.EnqueueUserInput(UserTurnInput{Prompt: followUpPrompt})
	if err != nil {
		t.Fatal(err)
	}
	close(model.firstRelease)
	firstTerminal := drainQueuedFollowUpTerminal(t, firstEvents)
	if firstTerminal.Reason != TerminalCompleted {
		t.Fatalf("first terminal = %#v", firstTerminal)
	}

	claimed, ok, err := eng.ClaimNextRuntimeItem()
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != queued.ID || claimed.Kind != RuntimeItemUserPrompt {
		t.Fatalf("claimed item = %#v, ok=%v", claimed, ok)
	}
	secondEvents, _ := eng.SubmitRuntimeItem(t.Context(), claimed)
	secondTerminal := drainQueuedFollowUpTerminal(t, secondEvents)
	if secondTerminal.Reason != TerminalCompleted {
		t.Fatalf("second terminal = %#v", secondTerminal)
	}

	loaded, err := eng.GetTranscript().LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	assertQueuedFollowUpTranscriptOrder(
		t,
		loaded.Messages,
		queuedFollowUpFirstAssistant,
		followUpPrompt,
		queuedFollowUpSecondAssistant,
	)
	inputs := model.snapshot()
	if len(inputs) != 2 {
		t.Fatalf("model inputs = %d, want 2", len(inputs))
	}
	if got := queuedFollowUpMessageCount(inputs[1], followUpPrompt); got != 1 {
		t.Fatalf("second model input follow-up count = %d, want 1: %#v", got, inputs[1])
	}
}

func drainQueuedFollowUpTerminal(t *testing.T, events <-chan QueryEvent) Terminal {
	t.Helper()
	var terminal *Terminal
	for event := range events {
		if event.Type == EventTerminal {
			terminal = event.TerminalInfo
		}
	}
	if terminal == nil {
		t.Fatal("missing terminal event")
	}
	return *terminal
}

func assertQueuedFollowUpTranscriptOrder(
	t *testing.T,
	messages []*schema.Message,
	firstAssistant string,
	followUpPrompt string,
	secondAssistant string,
) {
	t.Helper()
	firstAssistantIndex := -1
	firstAssistantCount := 0
	followUpIndex := -1
	followUpCount := 0
	secondAssistantIndex := -1
	secondAssistantCount := 0
	for index, message := range messages {
		if message == nil {
			continue
		}
		if message.Role == schema.Assistant && message.Content == firstAssistant {
			firstAssistantCount++
			firstAssistantIndex = index
		}
		if message.Role == schema.User && message.Content == followUpPrompt {
			followUpCount++
			followUpIndex = index
		}
		if message.Role == schema.Assistant && message.Content == secondAssistant {
			secondAssistantCount++
			secondAssistantIndex = index
		}
	}
	if firstAssistantCount != 1 ||
		followUpCount != 1 ||
		secondAssistantCount != 1 ||
		firstAssistantIndex < 0 ||
		followUpIndex <= firstAssistantIndex ||
		secondAssistantIndex <= followUpIndex {
		t.Fatalf(
			"transcript ordering first=%d/%d follow-up=%d/%d second=%d/%d messages=%#v",
			firstAssistantIndex,
			firstAssistantCount,
			followUpIndex,
			followUpCount,
			secondAssistantIndex,
			secondAssistantCount,
			messages,
		)
	}
}

func queuedFollowUpMessageCount(messages []*schema.Message, content string) int {
	count := 0
	for _, message := range messages {
		if message != nil && message.Role == schema.User && message.Content == content {
			count++
		}
	}
	return count
}
