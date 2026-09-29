package execution

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunDeadlineSurvivesDetachWithoutRestoringParentCancellation(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	deadline := time.Now().Add(time.Minute)
	run, cancelRun := WithRunDeadline(parent, deadline)
	defer cancelRun()
	detached, cancelDetached := BindRunDeadline(context.WithoutCancel(run))
	defer cancelDetached()
	cancelParent()
	if detached.Err() != nil {
		t.Fatal("restored ordinary parent cancellation across detach")
	}
	if got, ok := detached.Deadline(); !ok || !got.Equal(deadline) {
		t.Fatalf("deadline=%v %v", got, ok)
	}
	// A past cutoff is an independent deterministic oracle for enforcement.
	expired, cancelExpired := WithRunDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()
	rebound, cancelRebound := BindRunDeadline(context.WithoutCancel(expired))
	defer cancelRebound()
	if !errors.Is(rebound.Err(), context.DeadlineExceeded) {
		t.Fatalf("expired cutoff escaped detach: %v", rebound.Err())
	}
}

func TestRunDeadlineCannotExtendEarlierLimitAndDefaultIsOff(t *testing.T) {
	original := context.Background()
	off, cancel := WithRunDeadline(original, time.Time{})
	defer cancel()
	if off != original {
		t.Fatal("default changed context")
	}
	parent, cancelParent := context.WithTimeout(original, time.Minute)
	defer cancelParent()
	expected, _ := parent.Deadline()
	run, cancelRun := WithRunDeadline(parent, time.Now().Add(time.Hour))
	defer cancelRun()
	nested, cancelNested := WithRunDeadline(context.WithoutCancel(run), time.Now().Add(2*time.Hour))
	defer cancelNested()
	if got, ok := nested.Deadline(); !ok || !got.Equal(expected) {
		t.Fatalf("earlier cutoff extended: %v %v", got, ok)
	}
}
