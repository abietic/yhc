package appserver

import (
	"testing"
	"time"
)

func TestEventLogReplayAndGap(t *testing.T) {
	log := newEventLog(3)
	for i := 0; i < 5; i++ {
		event, ok := log.publish(WireEvent{
			ProtocolVersion: ProtocolVersion,
			Type:            "test",
			SessionID:       "session",
			Timestamp:       time.Now(),
			Data:            marshalEventData(map[string]int{"index": i}),
		})
		if !ok || event.ID != uint64(i+1) {
			t.Fatalf("publish %d = (%d, %v)", i, event.ID, ok)
		}
	}

	_, _, _, gap, err := log.subscribe(1)
	if err != errReplayGap {
		t.Fatalf("subscribe old cursor error = %v, want %v", err, errReplayGap)
	}
	if gap.Earliest != 3 || gap.Latest != 5 {
		t.Fatalf("gap = %+v, want earliest=3 latest=5", gap)
	}
	_, _, _, gap, err = log.subscribe(6)
	if err != errReplayGap || gap.Earliest != 3 || gap.Latest != 5 {
		t.Fatalf("future cursor gap = %+v, error = %v", gap, err)
	}

	replay, updates, unsubscribe, _, err := log.subscribe(2)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer unsubscribe()
	if len(replay) != 3 || replay[0].ID != 3 || replay[2].ID != 5 {
		t.Fatalf("replay = %+v", replay)
	}
	published, ok := log.publish(WireEvent{
		ProtocolVersion: ProtocolVersion,
		Type:            "live",
		SessionID:       "session",
		Timestamp:       time.Now(),
		Data:            marshalEventData(map[string]any{}),
	})
	if !ok {
		t.Fatal("live publish rejected")
	}
	select {
	case got := <-updates:
		if got.ID != published.ID {
			t.Fatalf("live id = %d, want %d", got.ID, published.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for live event")
	}
}

func TestEventLogDisconnectsSlowSubscriberForReplay(t *testing.T) {
	log := newEventLog(256)
	_, updates, _, _, err := log.subscribe(0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	for i := 0; i < 130; i++ {
		_, _ = log.publish(WireEvent{
			ProtocolVersion: ProtocolVersion,
			Type:            "progress",
			SessionID:       "session",
			Timestamp:       time.Now(),
			Data:            marshalEventData(map[string]int{"index": i}),
		})
	}
	count := 0
	for range updates {
		count++
	}
	if count != 128 {
		t.Fatalf("buffered updates = %d, want 128 before disconnect", count)
	}
	replay, _, unsubscribe, _, err := log.subscribe(0)
	if err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	defer unsubscribe()
	if len(replay) != 130 {
		t.Fatalf("replay count = %d, want 130", len(replay))
	}
}
