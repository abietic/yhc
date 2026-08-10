package engine

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

type p233AssistantProjectionKernel struct {
	events   []QueryEvent
	terminal Terminal
}

func (p233AssistantProjectionKernel) kind() queryKernelKind {
	return queryKernelProjectGraph
}

func (k p233AssistantProjectionKernel) run(
	ctx context.Context,
	request queryKernelRequest,
) Terminal {
	for _, event := range k.events {
		if ctx.Err() != nil {
			break
		}
		request.yield(event)
	}
	return k.terminal
}

func TestP233AssistantProjectionExactBytesAndIdentity(t *testing.T) {
	t.Parallel()

	events, terminal := runP233AssistantProjection(t, []QueryEvent{
		{Type: EventStreamRequestStart},
		p233AssistantDelta("a"),
		{Type: EventToolProgress},
		p233AssistantDelta(" \n\nb"),
	})
	if terminal.Reason != TerminalCompleted || terminal.Err != nil {
		t.Fatalf("terminal = %#v", terminal)
	}
	assertP233AssistantProjection(t, events, "message-1", "a \n\nb")

	var stamped int
	for _, event := range events {
		if event.Type != EventAssistant || event.Message == nil {
			continue
		}
		stamped++
		if got := event.Message.Extra[assistantMessageIDExtraKey]; got != "message-1" {
			t.Fatalf("message_id = %#v", got)
		}
	}
	if stamped != 2 {
		t.Fatalf("stamped assistant events = %d, want 2", stamped)
	}
}

func TestP233AssistantProjectionFinalReconciliation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		events     []QueryEvent
		wantText   string
		wantLegacy int
	}{
		{
			name: "final only",
			events: []QueryEvent{
				{Type: EventStreamRequestStart},
				p233AssistantFinal("complete"),
			},
			wantText:   "complete",
			wantLegacy: 1,
		},
		{
			name: "equal final suppressed",
			events: []QueryEvent{
				{Type: EventStreamRequestStart},
				p233AssistantDelta("complete"),
				p233AssistantFinal("complete"),
			},
			wantText:   "complete",
			wantLegacy: 1,
		},
		{
			name: "prefix final emits suffix",
			events: []QueryEvent{
				{Type: EventStreamRequestStart},
				p233AssistantDelta("com"),
				p233AssistantFinal("complete"),
			},
			wantText:   "complete",
			wantLegacy: 2,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			events, terminal := runP233AssistantProjection(t, test.events)
			if terminal.Reason != TerminalCompleted || terminal.Err != nil {
				t.Fatalf("terminal = %#v", terminal)
			}
			assertP233AssistantProjection(t, events, "message-1", test.wantText)
			legacy := 0
			for _, event := range events {
				if event.Type == EventAssistant {
					legacy++
				}
			}
			if legacy != test.wantLegacy {
				t.Fatalf("legacy assistant events = %d, want %d", legacy, test.wantLegacy)
			}
		})
	}
}

func TestP233AssistantProjectionMismatchFailsClosed(t *testing.T) {
	t.Parallel()

	delivered := "private delivered bytes"
	final := "different private final bytes"
	events, terminal := runP233AssistantProjection(t, []QueryEvent{
		{Type: EventStreamRequestStart},
		p233AssistantDelta(delivered),
		p233AssistantFinal(final),
		{Type: EventToolProgress},
	})
	if terminal.Reason != TerminalModelError || terminal.Err == nil {
		t.Fatalf("terminal = %#v", terminal)
	}
	diagnostic := terminal.Err.Error()
	for _, secret := range []string{delivered, final} {
		if strings.Contains(diagnostic, secret) {
			t.Fatalf("diagnostic leaked assistant bytes: %q", diagnostic)
		}
	}
	deliveredDigest := sha256.Sum256([]byte(delivered))
	finalDigest := sha256.Sum256([]byte(final))
	for _, want := range []string{
		`message_id="message-1"`,
		fmt.Sprintf("delivered_bytes=%d", len(delivered)),
		fmt.Sprintf("delivered_sha256=%x", deliveredDigest),
		fmt.Sprintf("final_bytes=%d", len(final)),
		fmt.Sprintf("final_sha256=%x", finalDigest),
		"event_ordinal=3",
	} {
		if !strings.Contains(diagnostic, want) {
			t.Fatalf("diagnostic %q missing %q", diagnostic, want)
		}
	}
	for _, event := range events {
		if event.Type == EventToolProgress {
			t.Fatal("kernel continued after projection cancellation")
		}
	}
	assertP233AssistantProjection(t, events, "message-1", delivered)
}

func TestP233AssistantProjectionMissingIdentityFailsClosed(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		uuid func() string
	}{
		{name: "generator unavailable"},
		{name: "empty generated ID", uuid: func() string { return "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var events []QueryEvent
			emitter := newAssistantProjectionEmitter(
				nil,
				test.uuid,
				func(event QueryEvent) {
					events = append(events, event)
				},
			)
			emitter.Emit(QueryEvent{Type: EventStreamRequestStart})
			emitter.Emit(p233AssistantDelta("must not be delivered"))
			if emitter.Err() == nil {
				t.Fatal("missing logical identity did not fail projection")
			}
			if len(events) != 1 || events[0].Type != EventStreamRequestStart {
				t.Fatalf("events after identity failure = %#v", events)
			}
		})
	}
}

func TestP233ConversationHistoryRetainsOnlyLogicalMessageID(t *testing.T) {
	t.Parallel()

	history := newConversationHistory(nil)
	history.Observe(QueryEvent{
		Type: EventAssistant,
		Message: &schema.Message{
			Role:    schema.Assistant,
			Content: "a",
			Extra: map[string]any{
				assistantMessageIDExtraKey: "message-1",
				"provider_private":         "must-not-merge",
			},
		},
	})
	history.Observe(QueryEvent{
		Type: EventAssistant,
		Message: &schema.Message{
			Role:    schema.Assistant,
			Content: "b",
			Extra: map[string]any{
				assistantMessageIDExtraKey: "message-1",
			},
		},
	})
	messages := history.Messages()
	if len(messages) != 1 || messages[0].Content != "ab" {
		t.Fatalf("messages = %#v", messages)
	}
	if got := messages[0].Extra[assistantMessageIDExtraKey]; got != "message-1" {
		t.Fatalf("message_id = %#v", got)
	}
	if _, ok := messages[0].Extra["provider_private"]; ok {
		t.Fatalf("provider metadata leaked into durable merge: %#v", messages[0].Extra)
	}
}

func TestP233CanonicalAssistantFamily(t *testing.T) {
	t.Parallel()

	event := QueryEvent{
		Type: EventCanonicalProjection,
		CanonicalProjection: &CanonicalProjectionEvent{
			Version: CanonicalProjectionVersion,
			Kind:    CanonicalProjectionAssistantDelta,
			Assistant: &CanonicalAssistantPayload{
				MessageID: "message-1",
				Delta:     []byte("a"),
			},
		},
	}
	if got := event.Family(); got != RuntimeFamilyTurnMessage {
		t.Fatalf("family = %q, want %q", got, RuntimeFamilyTurnMessage)
	}
}

func TestP233AssistantProjectionSerializesConcurrentRuntimeEvents(t *testing.T) {
	t.Parallel()

	const toolEvents = 64
	var (
		yielded []QueryEvent
		wg      sync.WaitGroup
	)
	emitter := newAssistantProjectionEmitter(
		nil,
		func() string { return "message-1" },
		func(event QueryEvent) {
			yielded = append(yielded, event)
		},
	)
	emitter.Emit(QueryEvent{Type: EventStreamRequestStart})
	emitter.Emit(p233AssistantDelta("a"))
	for range toolEvents {
		wg.Add(1)
		go func() {
			defer wg.Done()
			emitter.Emit(QueryEvent{Type: EventToolProgress})
		}()
	}
	wg.Wait()
	emitter.Emit(p233AssistantDelta("b"))

	if err := emitter.Err(); err != nil {
		t.Fatal(err)
	}
	if got := emitter.ordinal; got != toolEvents+3 {
		t.Fatalf("event ordinal = %d, want %d", got, toolEvents+3)
	}
	if got := len(yielded); got != toolEvents+5 {
		t.Fatalf("yielded events = %d, want %d", got, toolEvents+5)
	}
	assertP233AssistantProjection(t, yielded, "message-1", "ab")
}

func TestP233AssistantMessageIDPersistsAcrossReload(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	transcriptDir := filepath.Join(dir, "transcripts")
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:     "p233-persisted-id",
		ThreadID:      "p233-persisted-id",
		TranscriptDir: transcriptDir,
		CWD:           dir,
		ChatModel:     &transcriptModel{},
	})
	t.Cleanup(eng.Close)
	events, _ := eng.SubmitMessage(t.Context(), "persist logical identity")
	for range events {
	}

	messages := eng.GetMessages()
	if len(messages) != 2 {
		t.Fatalf("live messages = %d, want 2", len(messages))
	}
	liveID, _ := messages[1].Extra[assistantMessageIDExtraKey].(string)
	if _, err := uuid.Parse(liveID); err != nil {
		t.Fatalf("live message ID %q is not a UUID: %v", liveID, err)
	}
	eng.Close()

	reloaded := NewQueryEngine(QueryEngineConfig{
		SessionID:     "p233-persisted-id",
		ThreadID:      "p233-persisted-id",
		TranscriptDir: transcriptDir,
		CWD:           dir,
	})
	t.Cleanup(reloaded.Close)
	reloadedMessages := reloaded.GetMessages()
	if len(reloadedMessages) != 2 {
		t.Fatalf("reloaded messages = %d, want 2", len(reloadedMessages))
	}
	if got := reloadedMessages[1].Extra[assistantMessageIDExtraKey]; got != liveID {
		t.Fatalf("reloaded message ID = %#v, want %q", got, liveID)
	}
}

func runP233AssistantProjection(
	t *testing.T,
	kernelEvents []QueryEvent,
) ([]QueryEvent, Terminal) {
	t.Helper()

	events := make([]QueryEvent, 0)
	terminal := queryWithKernel(
		t.Context(),
		QueryParams{
			Deps: &QueryDeps{
				UUID: func() string { return "message-1" },
			},
		},
		func(event QueryEvent) {
			events = append(events, event)
		},
		p233AssistantProjectionKernel{
			events: kernelEvents,
			terminal: Terminal{
				Reason:    TerminalCompleted,
				TurnCount: 7,
				MaxTurns:  9,
			},
		},
	)
	return events, terminal
}

func p233AssistantDelta(content string) QueryEvent {
	return QueryEvent{
		Type: EventAssistant,
		Message: &schema.Message{
			Role:    schema.Assistant,
			Content: content,
		},
	}
}

func p233AssistantFinal(content string) QueryEvent {
	return QueryEvent{
		Type: EventAssistant,
		AssistantMessage: &schema.Message{
			Role:    schema.Assistant,
			Content: content,
		},
	}
}

func assertP233AssistantProjection(
	t *testing.T,
	events []QueryEvent,
	wantMessageID string,
	wantText string,
) {
	t.Helper()

	var projected strings.Builder
	for _, event := range events {
		if event.Type != EventCanonicalProjection ||
			event.CanonicalProjection == nil ||
			event.CanonicalProjection.Kind != CanonicalProjectionAssistantDelta {
			continue
		}
		if err := event.CanonicalProjection.Validate(); err != nil {
			t.Fatalf("validate canonical assistant projection: %v", err)
		}
		if got := event.CanonicalProjection.Assistant.MessageID; got != wantMessageID {
			t.Fatalf("message ID = %q, want %q", got, wantMessageID)
		}
		projected.Write(event.CanonicalProjection.Assistant.Delta)
	}
	if got := projected.String(); got != wantText {
		t.Fatalf("projected text = %q, want %q", got, wantText)
	}
}
