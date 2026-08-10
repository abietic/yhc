package engine

import (
	"reflect"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/execution"
)

func TestP294RuntimeReducerRetractsOnlyTombstonedAttempt(t *testing.T) {
	store := NewRuntimeStateStore()
	now := time.Now()
	event := func(
		sequence uint64,
		eventType QueryEventType,
		attemptID string,
		content string,
	) QueryEvent {
		result := QueryEvent{
			RuntimeEventEnvelope: RuntimeEventEnvelope{
				SessionID: "session",
				ThreadID:  "thread",
				TurnID:    "turn",
				Sequence:  sequence,
				Timestamp: now.Add(time.Duration(sequence) * time.Millisecond),
			},
			Type: eventType,
			ModelAttempt: &ModelAttemptEvent{
				AttemptID: attemptID,
			},
		}
		if content != "" {
			result.Message = &schema.Message{
				Role: schema.Assistant, Content: content,
			}
		}
		if eventType == EventTombstone {
			result.TombstoneUUID = attemptID
		}
		return result
	}

	if err := store.Apply(event(
		1,
		EventAssistant,
		"attempt-1",
		"first partial",
	)); err != nil {
		t.Fatal(err)
	}
	before := store.Snapshot("thread").Threads["thread"]
	if before.LiveMessage == nil ||
		before.LiveMessage.ModelAttemptID != "attempt-1" {
		t.Fatalf("live attempt before tombstone = %#v", before.LiveMessage)
	}
	if err := store.Apply(event(
		2,
		EventTombstone,
		"attempt-1",
		"",
	)); err != nil {
		t.Fatal(err)
	}
	after := store.Snapshot("thread").Threads["thread"]
	if after.LiveMessage != nil || len(after.Messages) != 0 {
		t.Fatalf(
			"tombstoned runtime output remained: live=%#v messages=%#v",
			after.LiveMessage,
			after.Messages,
		)
	}
	if err := store.Apply(event(
		3,
		EventAssistant,
		"attempt-2",
		"second partial",
	)); err != nil {
		t.Fatal(err)
	}
	next := store.Snapshot("thread").Threads["thread"]
	if next.LiveMessage == nil ||
		next.LiveMessage.ModelAttemptID != "attempt-2" ||
		next.LiveMessage.Content != "second partial" {
		t.Fatalf("next runtime attempt = %#v", next.LiveMessage)
	}
}

func TestP462RuntimeReducerReplaysDiscardedAttemptAndExactTombstone(
	t *testing.T,
) {
	now := time.Now().UTC()
	events := []QueryEvent{
		p462RuntimeAttemptEvent(
			now,
			1,
			"attempt-1",
			0,
			ModelAttemptStarted,
			ModelAttemptOutputNeverStarted,
		),
		{
			RuntimeEventEnvelope: p462RuntimeEnvelope(now, 2),
			Type:                 EventAssistant,
			Message: &schema.Message{
				Role: schema.Assistant, Content: "first partial",
			},
			ModelAttempt: &ModelAttemptEvent{AttemptID: "attempt-1"},
		},
		p462RuntimeAttemptEvent(
			now,
			3,
			"attempt-1",
			0,
			ModelAttemptDiscarded,
			ModelAttemptOutputDiscarded,
		),
		{
			RuntimeEventEnvelope: p462RuntimeEnvelope(now, 4),
			Type:                 EventTombstone,
			TombstoneUUID:        "attempt-1",
			ModelAttempt: &ModelAttemptEvent{
				AttemptID: "attempt-1", AttemptIndex: 0,
			},
		},
		p462RuntimeAttemptEvent(
			now,
			5,
			"attempt-2",
			1,
			ModelAttemptStarted,
			ModelAttemptOutputNeverStarted,
		),
	}

	live := NewRuntimeStateStore()
	for _, event := range events {
		if err := live.Apply(event); err != nil {
			t.Fatal(err)
		}
	}
	liveSnapshot := live.Snapshot("thread").Threads["thread"]
	if liveSnapshot.LiveMessage != nil || len(liveSnapshot.Messages) != 0 {
		t.Fatalf("discarded attempt output remained: %#v", liveSnapshot)
	}
	phases := make([]ModelAttemptPhase, 0, 3)
	for _, event := range liveSnapshot.Events {
		if event.Type == EventModelAttempt {
			phases = append(phases, event.ModelPhase)
		}
	}
	if want := []ModelAttemptPhase{
		ModelAttemptStarted,
		ModelAttemptDiscarded,
		ModelAttemptStarted,
	}; !reflect.DeepEqual(phases, want) {
		t.Fatalf("attempt phases = %#v, want %#v", phases, want)
	}

	replayed := NewRuntimeStateStore()
	if err := replayed.Replay(events); err != nil {
		t.Fatal(err)
	}
	if replaySnapshot := replayed.Snapshot("thread").Threads["thread"]; !reflect.DeepEqual(replaySnapshot, liveSnapshot) {
		t.Fatalf(
			"replay mismatch:\nlive=%#v\nreplayed=%#v",
			liveSnapshot,
			replaySnapshot,
		)
	}
}

func p462RuntimeEnvelope(now time.Time, sequence uint64) RuntimeEventEnvelope {
	return RuntimeEventEnvelope{
		SessionID: "session",
		ThreadID:  "thread",
		TurnID:    "turn",
		Sequence:  sequence,
		Timestamp: now.Add(time.Duration(sequence) * time.Millisecond),
	}
}

func p462RuntimeAttemptEvent(
	now time.Time,
	sequence uint64,
	attemptID string,
	attemptIndex int,
	phase ModelAttemptPhase,
	disposition ModelAttemptOutputDisposition,
) QueryEvent {
	return QueryEvent{
		RuntimeEventEnvelope: p462RuntimeEnvelope(now, sequence),
		Type:                 EventModelAttempt,
		ModelAttempt: &ModelAttemptEvent{
			AttemptID:         attemptID,
			AttemptIndex:      attemptIndex,
			Profile:           "profile",
			Phase:             phase,
			FailureClass:      string(execution.ModelFailureOverloaded),
			OutputDisposition: disposition,
		},
	}
}
