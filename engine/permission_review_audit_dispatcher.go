package engine

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/abietic/yhc/engine/permission"
)

const (
	permissionReviewAuditQueueCapacity = 128
	permissionReviewAuditCloseTimeout  = 250 * time.Millisecond
)

type reviewAuditDispatcherOptions struct {
	Capacity int
	Sink     permission.ReviewAuditSink
}

type reviewAuditDispatcherDiagnostics struct {
	EnqueueDrops      uint64
	SinkFailures      uint64
	FlushExpiry       uint64
	EnqueueAfterClose uint64
}

// reviewAuditDispatcher keeps durable reviewer evidence outside permission
// authority. Producers only attempt bounded-channel admission; one writer owns
// every sink call and contains sink failures and panics.
type reviewAuditDispatcher struct {
	sink   permission.ReviewAuditSink
	queue  chan permission.ReviewAuditRecord
	done   chan struct{}
	cancel context.CancelFunc

	mu     sync.Mutex
	closed bool

	expireOnce sync.Once

	enqueueDrops      atomic.Uint64
	sinkFailures      atomic.Uint64
	flushExpiry       atomic.Uint64
	enqueueAfterClose atomic.Uint64

	// Only the writer goroutine reads or mutates reported.
	reported reviewAuditDispatcherDiagnostics
}

func newReviewAuditDispatcher(
	options reviewAuditDispatcherOptions,
) *reviewAuditDispatcher {
	if options.Sink == nil {
		return nil
	}
	if options.Capacity <= 0 {
		options.Capacity = 1
	}
	writerCtx, cancel := context.WithCancel(context.Background())
	dispatcher := &reviewAuditDispatcher{
		sink:   options.Sink,
		queue:  make(chan permission.ReviewAuditRecord, options.Capacity),
		done:   make(chan struct{}),
		cancel: cancel,
	}
	go dispatcher.run(writerCtx)
	return dispatcher
}

func (d *reviewAuditDispatcher) Enqueue(
	record permission.ReviewAuditRecord,
) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		incrementReviewAuditCounter(&d.enqueueAfterClose)
		return
	}
	select {
	case d.queue <- record:
	default:
		incrementReviewAuditCounter(&d.enqueueDrops)
	}
}

func (d *reviewAuditDispatcher) Close(ctx context.Context) {
	if d == nil {
		return
	}
	d.mu.Lock()
	if !d.closed {
		d.closed = true
		close(d.queue)
	}
	done := d.done
	d.mu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-done:
		d.cancel()
		return
	default:
	}
	select {
	case <-done:
		d.cancel()
	case <-ctx.Done():
		select {
		case <-done:
			d.cancel()
			return
		default:
		}
		d.expireOnce.Do(func() {
			incrementReviewAuditCounter(&d.flushExpiry)
			d.cancel()
		})
	}
}

func (d *reviewAuditDispatcher) Diagnostics() reviewAuditDispatcherDiagnostics {
	if d == nil {
		return reviewAuditDispatcherDiagnostics{}
	}
	return reviewAuditDispatcherDiagnostics{
		EnqueueDrops:      d.enqueueDrops.Load(),
		SinkFailures:      d.sinkFailures.Load(),
		FlushExpiry:       d.flushExpiry.Load(),
		EnqueueAfterClose: d.enqueueAfterClose.Load(),
	}
}

func (d *reviewAuditDispatcher) run(ctx context.Context) {
	defer close(d.done)
	for {
		if ctx.Err() != nil {
			return
		}
		record, ok := <-d.queue
		if !ok {
			d.flushDiagnostics(ctx)
			return
		}
		if d.writeRecord(ctx, record) && ctx.Err() == nil {
			d.flushDiagnostics(ctx)
		}
	}
}

func (d *reviewAuditDispatcher) writeRecord(
	ctx context.Context,
	record permission.ReviewAuditRecord,
) (written bool) {
	defer func() {
		if recover() != nil {
			incrementReviewAuditCounter(&d.sinkFailures)
			written = false
		}
	}()
	if err := d.sink.Record(ctx, record); err != nil {
		incrementReviewAuditCounter(&d.sinkFailures)
		return false
	}
	return true
}

func (d *reviewAuditDispatcher) flushDiagnostics(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	// Freeze the snapshot before writing so a failed diagnostic write cannot
	// recursively generate another sink_failure record in this pass.
	snapshot := d.Diagnostics()
	d.flushDiagnostic(
		ctx,
		permission.ReviewAuditDiagnosticEnqueueDrop,
		snapshot.EnqueueDrops,
		&d.reported.EnqueueDrops,
	)
	d.flushDiagnostic(
		ctx,
		permission.ReviewAuditDiagnosticSinkFailure,
		snapshot.SinkFailures,
		&d.reported.SinkFailures,
	)
	d.flushDiagnostic(
		ctx,
		permission.ReviewAuditDiagnosticFlushExpiry,
		snapshot.FlushExpiry,
		&d.reported.FlushExpiry,
	)
	d.flushDiagnostic(
		ctx,
		permission.ReviewAuditDiagnosticAfterClose,
		snapshot.EnqueueAfterClose,
		&d.reported.EnqueueAfterClose,
	)
}

func (d *reviewAuditDispatcher) flushDiagnostic(
	ctx context.Context,
	code permission.ReviewAuditDispatcherDiagnostic,
	cumulative uint64,
	reported *uint64,
) {
	if ctx.Err() != nil || cumulative <= *reported {
		return
	}
	eventID, err := randomReviewOpaqueID(16)
	if err != nil {
		return
	}
	record := permission.ReviewAuditRecord{
		SchemaVersion:        permission.ReviewAuditSchemaVersion,
		EventID:              eventID,
		OccurredAt:           time.Now().UTC(),
		Kind:                 permission.ReviewAuditKindDispatcherDiagnostic,
		DispatcherDiagnostic: code,
		DiagnosticCount:      cumulative - *reported,
	}
	if d.writeRecord(ctx, record) {
		*reported = cumulative
	}
}

func incrementReviewAuditCounter(counter *atomic.Uint64) {
	for {
		current := counter.Load()
		if current == ^uint64(0) {
			return
		}
		if counter.CompareAndSwap(current, current+1) {
			return
		}
	}
}
