package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/permission"
)

type reviewAuditDispatcherSinkFunc func(
	context.Context,
	permission.ReviewAuditRecord,
) error

func (f reviewAuditDispatcherSinkFunc) Record(
	ctx context.Context,
	record permission.ReviewAuditRecord,
) error {
	return f(ctx, record)
}

func TestReviewAuditDispatcherEnqueueNeverBlocks(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)

	var mu sync.Mutex
	var delivered []string
	dispatcher := newReviewAuditDispatcher(reviewAuditDispatcherOptions{
		Capacity: 1,
		Sink: reviewAuditDispatcherSinkFunc(func(
			_ context.Context,
			record permission.ReviewAuditRecord,
		) error {
			mu.Lock()
			delivered = append(delivered, record.EventID)
			first := len(delivered) == 1
			mu.Unlock()
			if first {
				close(entered)
				<-release
			}
			return nil
		}),
	})

	dispatcher.Enqueue(reviewAuditDispatcherRecord("first"))
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("writer did not enter blocking sink")
	}
	dispatcher.Enqueue(reviewAuditDispatcherRecord("queued"))

	done := make(chan struct{})
	go func() {
		dispatcher.Enqueue(reviewAuditDispatcherRecord("dropped"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("audit enqueue blocked behind sink")
	}
	if got := dispatcher.Diagnostics().EnqueueDrops; got != 1 {
		t.Fatalf("enqueue drops = %d, want 1", got)
	}

	unblock()
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	dispatcher.Close(closeCtx)

	mu.Lock()
	defer mu.Unlock()
	counts := make(map[string]int)
	for _, eventID := range delivered {
		counts[eventID]++
	}
	if counts["first"] != 1 || counts["queued"] != 1 || counts["dropped"] != 0 {
		t.Fatalf("delivered event counts = %+v", counts)
	}
}

func TestReviewAuditDispatcherContainsSinkFailureAndPanic(t *testing.T) {
	var mu sync.Mutex
	calls := make(map[string]int)
	diagnostics := make(map[permission.ReviewAuditDispatcherDiagnostic]uint64)
	dispatcher := newReviewAuditDispatcher(reviewAuditDispatcherOptions{
		Capacity: 8,
		Sink: reviewAuditDispatcherSinkFunc(func(
			_ context.Context,
			record permission.ReviewAuditRecord,
		) error {
			mu.Lock()
			calls[record.EventID]++
			if record.Kind == permission.ReviewAuditKindDispatcherDiagnostic {
				diagnostics[record.DispatcherDiagnostic] += record.DiagnosticCount
			}
			mu.Unlock()
			switch record.EventID {
			case "error":
				return errors.New("sink unavailable")
			case "panic":
				panic("sink panic")
			default:
				return nil
			}
		}),
	})
	dispatcher.Enqueue(reviewAuditDispatcherRecord("error"))
	dispatcher.Enqueue(reviewAuditDispatcherRecord("panic"))
	dispatcher.Enqueue(reviewAuditDispatcherRecord("success"))
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	dispatcher.Close(closeCtx)

	if got := dispatcher.Diagnostics().SinkFailures; got != 2 {
		t.Fatalf("sink failures = %d, want 2", got)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, eventID := range []string{"error", "panic", "success"} {
		if calls[eventID] != 1 {
			t.Fatalf("sink calls[%s] = %d, want 1", eventID, calls[eventID])
		}
	}
	if diagnostics[permission.ReviewAuditDiagnosticSinkFailure] != 2 {
		t.Fatalf("retained diagnostics = %+v, want sink_failure=2", diagnostics)
	}
}

func TestReviewAuditDispatcherRetriesDiagnosticDelta(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)

	var mu sync.Mutex
	failedDiagnostic := false
	diagnostics := make(map[permission.ReviewAuditDispatcherDiagnostic]uint64)
	dispatcher := newReviewAuditDispatcher(reviewAuditDispatcherOptions{
		Capacity: 1,
		Sink: reviewAuditDispatcherSinkFunc(func(
			_ context.Context,
			record permission.ReviewAuditRecord,
		) error {
			if record.EventID == "first" {
				close(entered)
				<-release
			}
			mu.Lock()
			defer mu.Unlock()
			if record.Kind == permission.ReviewAuditKindDispatcherDiagnostic {
				if !failedDiagnostic {
					failedDiagnostic = true
					return errors.New("transient diagnostic failure")
				}
				diagnostics[record.DispatcherDiagnostic] += record.DiagnosticCount
			}
			return nil
		}),
	})
	dispatcher.Enqueue(reviewAuditDispatcherRecord("first"))
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("writer did not enter blocking sink")
	}
	dispatcher.Enqueue(reviewAuditDispatcherRecord("queued"))
	dispatcher.Enqueue(reviewAuditDispatcherRecord("dropped"))
	unblock()

	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	dispatcher.Close(closeCtx)
	got := dispatcher.Diagnostics()
	if got.EnqueueDrops != 1 || got.SinkFailures != 1 {
		t.Fatalf("dispatcher diagnostics = %+v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if diagnostics[permission.ReviewAuditDiagnosticEnqueueDrop] != 1 ||
		diagnostics[permission.ReviewAuditDiagnosticSinkFailure] != 1 {
		t.Fatalf("retained diagnostics = %+v", diagnostics)
	}
}

func TestReviewAuditDispatcherConcurrentAcceptedExactlyOnce(t *testing.T) {
	const producers = 64
	var mu sync.Mutex
	calls := make(map[string]int, producers)
	dispatcher := newReviewAuditDispatcher(reviewAuditDispatcherOptions{
		Capacity: producers,
		Sink: reviewAuditDispatcherSinkFunc(func(
			_ context.Context,
			record permission.ReviewAuditRecord,
		) error {
			if record.Kind != permission.ReviewAuditKindDispatcherDiagnostic {
				mu.Lock()
				calls[record.EventID]++
				mu.Unlock()
			}
			return nil
		}),
	})
	start := make(chan struct{})
	var producersWG sync.WaitGroup
	for i := 0; i < producers; i++ {
		producersWG.Add(1)
		go func(index int) {
			defer producersWG.Done()
			<-start
			dispatcher.Enqueue(reviewAuditDispatcherRecord(fmt.Sprintf("event-%d", index)))
		}(i)
	}
	close(start)
	producersWG.Wait()
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	dispatcher.Close(closeCtx)
	if got := dispatcher.Diagnostics().EnqueueDrops; got != 0 {
		t.Fatalf("enqueue drops = %d, want 0", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != producers {
		t.Fatalf("delivered events = %d, want %d", len(calls), producers)
	}
	for eventID, count := range calls {
		if count != 1 {
			t.Fatalf("sink calls[%s] = %d, want 1", eventID, count)
		}
	}
}

func TestReviewAuditDispatcherConcurrentCloseClassifiesEveryEnqueue(t *testing.T) {
	const (
		producers = 32
		records   = 32
	)
	var delivered atomic.Uint64
	dispatcher := newReviewAuditDispatcher(reviewAuditDispatcherOptions{
		Capacity: 16,
		Sink: reviewAuditDispatcherSinkFunc(func(
			_ context.Context,
			record permission.ReviewAuditRecord,
		) error {
			if record.Kind != permission.ReviewAuditKindDispatcherDiagnostic {
				delivered.Add(1)
			}
			return nil
		}),
	})
	start := make(chan struct{})
	var producersWG sync.WaitGroup
	for producer := 0; producer < producers; producer++ {
		producersWG.Add(1)
		go func(producer int) {
			defer producersWG.Done()
			<-start
			for record := 0; record < records; record++ {
				dispatcher.Enqueue(reviewAuditDispatcherRecord(
					fmt.Sprintf("event-%d-%d", producer, record),
				))
			}
		}(producer)
	}
	closed := make(chan struct{})
	go func() {
		<-start
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		dispatcher.Close(closeCtx)
		close(closed)
	}()
	close(start)
	producersWG.Wait()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("concurrent close did not complete")
	}
	diagnostics := dispatcher.Diagnostics()
	classified := delivered.Load() +
		diagnostics.EnqueueDrops +
		diagnostics.EnqueueAfterClose
	if classified != producers*records {
		t.Fatalf(
			"classified enqueues = %d, want %d; diagnostics=%+v",
			classified,
			producers*records,
			diagnostics,
		)
	}
	if diagnostics.FlushExpiry != 0 {
		t.Fatalf("unexpected flush expiry: %+v", diagnostics)
	}
}

func TestReviewAuditDispatcherCloseIsBounded(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	dispatcher := newReviewAuditDispatcher(reviewAuditDispatcherOptions{
		Capacity: 1,
		Sink: reviewAuditDispatcherSinkFunc(func(
			_ context.Context,
			_ permission.ReviewAuditRecord,
		) error {
			close(entered)
			<-release
			return nil
		}),
	})
	dispatcher.Enqueue(reviewAuditDispatcherRecord("blocked"))
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("writer did not enter blocking sink")
	}

	closeCtx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	started := time.Now()
	dispatcher.Close(closeCtx)
	cancel()
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("dispatcher close elapsed = %s, want bounded return", elapsed)
	}
	if got := dispatcher.Diagnostics().FlushExpiry; got != 1 {
		t.Fatalf("flush expiry = %d, want 1", got)
	}
	dispatcher.Enqueue(reviewAuditDispatcherRecord("after-close"))
	if got := dispatcher.Diagnostics().EnqueueAfterClose; got != 1 {
		t.Fatalf("enqueue after close = %d, want 1", got)
	}

	unblock()
	joinedCtx, joinedCancel := context.WithTimeout(context.Background(), time.Second)
	defer joinedCancel()
	dispatcher.Close(joinedCtx)
}

func TestReviewAuditDispatcherCancellationStopsCooperativeSink(t *testing.T) {
	entered := make(chan struct{})
	dispatcher := newReviewAuditDispatcher(reviewAuditDispatcherOptions{
		Capacity: 1,
		Sink: reviewAuditDispatcherSinkFunc(func(
			ctx context.Context,
			_ permission.ReviewAuditRecord,
		) error {
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		}),
	})
	dispatcher.Enqueue(reviewAuditDispatcherRecord("cooperative"))
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("writer did not enter cooperative sink")
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	dispatcher.Close(closeCtx)
	cancel()
	joinedCtx, joinedCancel := context.WithTimeout(context.Background(), time.Second)
	defer joinedCancel()
	dispatcher.Close(joinedCtx)
	diagnostics := dispatcher.Diagnostics()
	if diagnostics.FlushExpiry != 1 || diagnostics.SinkFailures != 1 {
		t.Fatalf("dispatcher diagnostics = %+v", diagnostics)
	}
}

func reviewAuditDispatcherRecord(eventID string) permission.ReviewAuditRecord {
	return permission.ReviewAuditRecord{
		SchemaVersion:      permission.ReviewAuditSchemaVersion,
		EventID:            eventID,
		OccurredAt:         time.Now().UTC(),
		Kind:               permission.ReviewAuditKindEligible,
		CanonicalTool:      "TaskCreate",
		ActionKind:         "runtime_state",
		DeterministicClass: "review",
	}
}
