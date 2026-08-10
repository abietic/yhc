package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"sync"

	"github.com/cloudwego/eino/schema"
)

const assistantMessageIDExtraKey = "message_id"

// assistantProjectionEmitter is the query-scoped owner of logical assistant
// message identity and exact-byte canonical deltas. It sits before transcript
// persistence and every entrypoint adapter.
type assistantProjectionEmitter struct {
	mu sync.Mutex

	cancel context.CancelFunc
	uuid   func() string
	yield  func(QueryEvent)

	messageID string
	delivered []byte
	err       error
	ordinal   uint64
}

func newAssistantProjectionEmitter(
	cancel context.CancelFunc,
	uuid func() string,
	yield func(QueryEvent),
) *assistantProjectionEmitter {
	if yield == nil {
		yield = func(QueryEvent) {}
	}
	return &assistantProjectionEmitter{
		cancel: cancel,
		uuid:   uuid,
		yield:  yield,
	}
}

func (e *assistantProjectionEmitter) Emit(event QueryEvent) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	e.ordinal++

	switch event.Type {
	case EventStreamRequestStart:
		e.messageID = ""
		e.delivered = nil
		e.yield(event)
	case EventAssistant:
		e.emitAssistant(event)
	default:
		e.yield(event)
	}
}

func (e *assistantProjectionEmitter) Err() error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.err
}

func (e *assistantProjectionEmitter) emitAssistant(event QueryEvent) {
	if e.err != nil {
		return
	}
	message, final := assistantProjectionMessage(event)
	if message == nil {
		e.yield(event)
		return
	}
	if e.messageID == "" {
		if e.uuid == nil {
			e.fail(fmt.Errorf(
				"engine: assistant projection message ID generator is unavailable at event ordinal %d",
				e.ordinal,
			))
			return
		}
		e.messageID = e.uuid()
		if e.messageID == "" {
			e.fail(fmt.Errorf(
				"engine: assistant projection message ID is empty at event ordinal %d",
				e.ordinal,
			))
			return
		}
	}

	stamped := cloneAssistantProjectionMessage(message, e.messageID)
	if final {
		e.emitFinal(event, stamped)
		return
	}

	event.Message = stamped
	e.yield(event)
	e.emitDelta(event.RuntimeEventEnvelope, []byte(stamped.Content))
	e.delivered = append(e.delivered, stamped.Content...)
}

func (e *assistantProjectionEmitter) emitFinal(
	event QueryEvent,
	message *schema.Message,
) {
	final := []byte(message.Content)
	switch {
	case len(e.delivered) == 0:
		event.AssistantMessage = message
		e.yield(event)
		e.emitDelta(event.RuntimeEventEnvelope, final)
		e.delivered = append(e.delivered, final...)
	case bytes.Equal(final, e.delivered):
		// The complete final fallback adds no new bytes or durable message.
	case bytes.HasPrefix(final, e.delivered):
		suffix := bytes.Clone(final[len(e.delivered):])
		if len(suffix) == 0 {
			return
		}
		// Preserve final metadata while exposing only bytes not already carried
		// by prior deltas. The history owner merges this event by message ID.
		message.Content = string(suffix)
		event.AssistantMessage = nil
		event.Message = message
		e.yield(event)
		e.emitDelta(event.RuntimeEventEnvelope, suffix)
		e.delivered = append(e.delivered, suffix...)
	default:
		deliveredDigest := sha256.Sum256(e.delivered)
		finalDigest := sha256.Sum256(final)
		e.fail(fmt.Errorf(
			"engine: assistant projection mismatch: message_id=%q delivered_bytes=%d delivered_sha256=%x final_bytes=%d final_sha256=%x event_ordinal=%d",
			boundedAssistantMessageID(e.messageID),
			len(e.delivered),
			deliveredDigest,
			len(final),
			finalDigest,
			e.ordinal,
		))
	}
}

func (e *assistantProjectionEmitter) emitDelta(
	envelope RuntimeEventEnvelope,
	delta []byte,
) {
	if len(delta) == 0 {
		return
	}
	e.yield(QueryEvent{
		RuntimeEventEnvelope: envelope,
		Type:                 EventCanonicalProjection,
		CanonicalProjection: &CanonicalProjectionEvent{
			Version: CanonicalProjectionVersion,
			Kind:    CanonicalProjectionAssistantDelta,
			Assistant: &CanonicalAssistantPayload{
				MessageID: e.messageID,
				Delta:     bytes.Clone(delta),
			},
		},
	})
}

func (e *assistantProjectionEmitter) fail(err error) {
	if e.err != nil {
		return
	}
	e.err = err
	if e.cancel != nil {
		e.cancel()
	}
}

func assistantProjectionMessage(
	event QueryEvent,
) (*schema.Message, bool) {
	if event.AssistantMessage != nil {
		return event.AssistantMessage, true
	}
	if event.Message != nil {
		return event.Message, false
	}
	return nil, false
}

func cloneAssistantProjectionMessage(
	message *schema.Message,
	messageID string,
) *schema.Message {
	cloned := *message
	cloned.Extra = cloneMessageExtra(message.Extra)
	if cloned.Extra == nil {
		cloned.Extra = make(map[string]any)
	}
	cloned.Extra[assistantMessageIDExtraKey] = messageID
	return &cloned
}

func boundedAssistantMessageID(messageID string) string {
	const maxBytes = 128
	if len(messageID) <= maxBytes {
		return messageID
	}
	return messageID[:maxBytes]
}
