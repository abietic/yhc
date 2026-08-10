package engine

import (
	"context"
	"errors"
	"testing"
)

func runRepeatedToolCall(t *testing.T, guard *repeatedToolCallGuard, fingerprint string, overridden bool) repeatedToolDecision {
	t.Helper()
	ticket := guard.reserve()
	decision, _, err := ticket.await(context.Background(), fingerprint)
	if err != nil {
		t.Fatalf("await ticket: %v", err)
	}
	ticket.release(overridden)
	return decision
}

func TestRepeatedToolGuardThresholdDenialAndOverride(t *testing.T) {
	guard := newRepeatedToolCallGuard()
	fingerprint := repeatedToolCallFingerprint("Read", map[string]any{"file_path": "a"})
	for index, want := range []repeatedToolDecision{repeatedToolAllow, repeatedToolAllow, repeatedToolRequestOverride, repeatedToolBlock} {
		if got := runRepeatedToolCall(t, guard, fingerprint, false); got != want {
			t.Fatalf("call %d decision = %v, want %v", index+1, got, want)
		}
	}

	guard.reset()
	if got := runRepeatedToolCall(t, guard, fingerprint, false); got != repeatedToolAllow {
		t.Fatalf("after reset decision = %v, want allow", got)
	}
	_ = runRepeatedToolCall(t, guard, fingerprint, false)
	if got := runRepeatedToolCall(t, guard, fingerprint, true); got != repeatedToolRequestOverride {
		t.Fatalf("override decision = %v, want request", got)
	}
	if got := runRepeatedToolCall(t, guard, fingerprint, false); got != repeatedToolAllow {
		t.Fatalf("after override decision = %v, want allow", got)
	}
}

func TestRepeatedToolGuardDifferentFingerprintResetsStreak(t *testing.T) {
	guard := newRepeatedToolCallGuard()
	first := repeatedToolCallFingerprint("Read", map[string]any{"file_path": "a"})
	second := repeatedToolCallFingerprint("Read", map[string]any{"file_path": "b"})
	_ = runRepeatedToolCall(t, guard, first, false)
	_ = runRepeatedToolCall(t, guard, first, false)
	if got := runRepeatedToolCall(t, guard, second, false); got != repeatedToolAllow {
		t.Fatalf("different fingerprint decision = %v, want allow", got)
	}
	if got := runRepeatedToolCall(t, guard, first, false); got != repeatedToolAllow {
		t.Fatalf("old fingerprint after reset decision = %v, want allow", got)
	}
}

func TestRepeatedToolGuardReservationOrder(t *testing.T) {
	guard := newRepeatedToolCallGuard()
	fingerprint := repeatedToolCallFingerprint("Read", map[string]any{"file_path": "a"})
	tickets := []*repeatedToolCallTicket{guard.reserve(), guard.reserve(), guard.reserve()}

	type result struct {
		index    int
		decision repeatedToolDecision
		err      error
	}
	results := make(chan result, len(tickets))
	releases := make([]chan struct{}, len(tickets))
	for index, ticket := range tickets {
		releases[index] = make(chan struct{})
		go func(index int, ticket *repeatedToolCallTicket) {
			decision, _, err := ticket.await(context.Background(), fingerprint)
			results <- result{index: index, decision: decision, err: err}
			<-releases[index]
			ticket.release(false)
		}(index, ticket)
	}

	wants := []repeatedToolDecision{repeatedToolAllow, repeatedToolAllow, repeatedToolRequestOverride}
	for index, want := range wants {
		got := <-results
		if got.err != nil || got.index != index || got.decision != want {
			t.Fatalf("ticket %d result = %+v, want decision %v", index+1, got, want)
		}
		close(releases[index])
	}
}

func TestRepeatedToolGuardCancellationDoesNotBypassPredecessor(t *testing.T) {
	guard := newRepeatedToolCallGuard()
	fingerprint := repeatedToolCallFingerprint("Read", map[string]any{"file_path": "a"})
	_ = runRepeatedToolCall(t, guard, fingerprint, false)
	_ = runRepeatedToolCall(t, guard, fingerprint, false)

	first := guard.reserve()
	second := guard.reserve()
	third := guard.reserve()
	decision, _, err := first.await(context.Background(), fingerprint)
	if err != nil || decision != repeatedToolRequestOverride {
		t.Fatalf("first ticket = %v, %v; want override request", decision, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() {
		_, _, awaitErr := second.await(ctx, fingerprint)
		secondDone <- awaitErr
	}()
	thirdDone := make(chan repeatedToolDecision, 1)
	go func() {
		thirdDecision, _, _ := third.await(context.Background(), fingerprint)
		thirdDone <- thirdDecision
	}()
	cancel()
	if got := <-secondDone; !errors.Is(got, context.Canceled) {
		t.Fatalf("canceled ticket error = %v, want context canceled", got)
	}
	select {
	case got := <-thirdDone:
		t.Fatalf("successor bypassed unresolved predecessor with decision %v", got)
	default:
	}

	first.release(false)
	if got := <-thirdDone; got != repeatedToolBlock {
		t.Fatalf("successor decision = %v, want block", got)
	}
	third.release(false)
}

func TestRepeatedToolFingerprintIsCanonicalAndToolScoped(t *testing.T) {
	first := repeatedToolCallFingerprint("Read", map[string]any{"a": 1, "b": true})
	reordered := repeatedToolCallFingerprint("Read", map[string]any{"b": true, "a": 1})
	if first == "" || first != reordered {
		t.Fatalf("canonical fingerprints differ: %q != %q", first, reordered)
	}
	if first == repeatedToolCallFingerprint("Write", map[string]any{"a": 1, "b": true}) {
		t.Fatal("tool name was not included in fingerprint")
	}
	if first == repeatedToolCallFingerprint("Read", map[string]any{"a": 2, "b": true}) {
		t.Fatal("changed input produced the same fingerprint")
	}
}
