package cmd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/engine"
)

func TestP245aPlainInputBrokerOrdersLinesAndFinalPartialEOF(t *testing.T) {
	input := newPlainInputBroker(bufio.NewReader(strings.NewReader(
		"first\nsecond\nfinal",
	)))
	for index, want := range []plainInputResult{
		{line: "first\n"},
		{line: "second\n"},
		{line: "final", err: io.EOF},
	} {
		got := input.next(context.Background())
		if got.line != want.line || !errors.Is(got.err, want.err) {
			t.Fatalf("result %d = %#v, want %#v", index, got, want)
		}
	}
	if got := input.next(context.Background()); got.line != "" ||
		!errors.Is(got.err, io.EOF) {
		t.Fatalf("read after EOF = %#v", got)
	}
}

func TestP245aPlainInputBrokerCancellationKeepsSoleReader(t *testing.T) {
	reader, writer := io.Pipe()
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	input := newPlainInputBroker(bufio.NewReader(reader))

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := input.next(cancelledCtx); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("cancelled read = %#v", got)
	}

	next := make(chan plainInputResult, 1)
	go func() {
		next <- input.next(context.Background())
	}()
	if _, err := io.WriteString(writer, "retained for the next consumer\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-next:
		if got.line != "retained for the next consumer\n" || got.err != nil {
			t.Fatalf("next consumer result = %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("the process-lifetime reader did not deliver the retained line")
	}

	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if got := input.next(context.Background()); got.line != "" ||
		!errors.Is(got.err, io.EOF) {
		t.Fatalf("EOF after retained line = %#v", got)
	}
}

func TestP245aPlainIdleCompletedInputPrecedesGoalWake(t *testing.T) {
	results := make(chan plainInputResult, 1)
	results <- plainInputResult{line: "/goal pause\n"}
	input := &plainInputBroker{results: results}
	goalWake := make(chan struct{}, 1)
	goalWake <- struct{}{}

	selected := waitForPlainIdleWork(context.Background(), input, goalWake)
	if !selected.hasInput ||
		selected.goalWake ||
		selected.input.line != "/goal pause\n" {
		t.Fatalf("idle selection = %#v", selected)
	}
	select {
	case <-goalWake:
	default:
		t.Fatal("input precedence consumed the later Goal wake")
	}
}

func TestP245aPlainIdleContextCancellationDoesNotClaimGoal(t *testing.T) {
	results := make(chan plainInputResult)
	input := &plainInputBroker{results: results}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	selected := waitForPlainIdleWork(ctx, input, make(chan struct{}))
	if !selected.hasInput ||
		selected.goalWake ||
		!errors.Is(selected.input.err, context.Canceled) {
		t.Fatalf("cancelled idle selection = %#v", selected)
	}
}

func TestP245aPlainGoalWakeSubmitsOneClaimedCursorAtMostOnce(t *testing.T) {
	runtime := &fakePlainGoalContinuationRuntime{
		item: engine.RuntimeItem{
			ID:   "plain-goal-cursor-1",
			Kind: engine.RuntimeItemGoalContinuation,
		},
		snapshot: engine.GoalSnapshot{
			Objective: "finish\n  the Plain consumer",
		},
	}
	var output bytes.Buffer
	events, started, err := claimPlainGoalContinuation(
		context.Background(),
		runtime,
		&output,
	)
	if err != nil || !started {
		t.Fatalf("first Goal claim started=%v err=%v", started, err)
	}
	for range events {
	}
	if events, started, err = claimPlainGoalContinuation(
		context.Background(),
		runtime,
		&output,
	); err != nil || started || events != nil {
		t.Fatalf(
			"coalesced Goal claim events=%v started=%v err=%v",
			events,
			started,
			err,
		)
	}
	if runtime.claims != 2 || runtime.submissions != 1 {
		t.Fatalf(
			"Goal calls claims=%d submissions=%d",
			runtime.claims,
			runtime.submissions,
		)
	}
	if output.String() != "[Goal continuation] finish the Plain consumer\n" {
		t.Fatalf("Goal attribution = %q", output.String())
	}
}

type fakePlainGoalContinuationRuntime struct {
	item        engine.RuntimeItem
	snapshot    engine.GoalSnapshot
	claims      int
	submissions int
}

func (f *fakePlainGoalContinuationRuntime) ClaimNextGoalContinuation() (
	engine.RuntimeItem,
	bool,
	error,
) {
	f.claims++
	if f.claims != 1 {
		return engine.RuntimeItem{}, false, nil
	}
	return f.item, true, nil
}

func (f *fakePlainGoalContinuationRuntime) GoalSnapshot() (
	*engine.GoalSnapshot,
	bool,
) {
	snapshot := f.snapshot
	return &snapshot, true
}

func (f *fakePlainGoalContinuationRuntime) SubmitGoalContinuation(
	_ context.Context,
	_ engine.RuntimeItem,
) (<-chan engine.QueryEvent, engine.Terminal) {
	f.submissions++
	events := make(chan engine.QueryEvent)
	close(events)
	return events, engine.Terminal{Reason: engine.TerminalCompleted}
}
