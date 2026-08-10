# Non-Blocking Reviewer Audit Dispatcher Implementation Plan

> **Historical use:** Retained as completed implementation evidence; do not
> execute this checklist as current work.

**Status:** historical
**Completed:** 2026-08-08
**Queue state:** completed and removed
**Created:** 2026-08-07
**Source snapshot:** `origin/master` at
`de74294b29f40d19bfd0e37f09889bd6f8037d90`

> **Ownership:** test-first delivery plan for P50.3, the third repair accepted
> by the [Permission Runtime Remediation Design](../specs/2026-08-07-permission-remediation-design.md)

**Goal:** Ensure reviewer-audit storage latency, failure, panic, or shutdown
cannot block or alter permission classification, prompting, settlement, or
QueryEngine shutdown.

**Architecture:** Each QueryEngine with an audit sink owns one bounded
single-writer dispatcher. Permission paths only perform a non-blocking enqueue.
The writer serializes sink calls, coalesces typed diagnostic deltas, and is
closed after reviewer producers stop. Shutdown waits only for a bounded context;
an arbitrary sink that ignores cancellation may leak its worker goroutine but
can never hold QueryEngine close or permission authority.

**Tech Stack:** Go 1.26.5, bounded channels, atomics, context cancellation,
redacted JSONL audit store, typed retained-window diagnostics, race detector,
and Makefile gates.

## Global Constraints

- Execute only after root `docs/migration/queue.yaml` admits P50.3 as its sole
  `Ready` slice and P50.2 is terminal.
- The dispatcher is non-authoritative. No enqueue result, sink result, panic,
  counter, or close result may change a permission outcome.
- Keep raw tool input, prompt, path, environment, credential, nonce, and
  reviewer rationale out of the queue and diagnostics.
- Do not spawn one goroutine per record. Use one bounded queue and one writer.
- Producers never wait for capacity. Full or closed admission increments a
  typed counter and returns.
- Cancel permission reviewers before closing audit admission.
- A sink that ignores context cannot be forcibly stopped in Go. Bound the
  engine wait and report the incomplete local diagnostic; do not pretend it was
  durably written to the same blocked sink.
- Keep the default journal's cross-process lock and rotation ownership inside
  `permission.ReviewAuditStore`.

---

## Task 1: Define typed dispatcher diagnostics in the redacted audit schema

**Files:**

- Modify: `engine/permission/review_audit.go`
- Modify: `engine/permission/review_audit_test.go`
- Modify: `engine/permission/review_audit_report.go`
- Modify: `engine/permission/review_audit_report_test.go`

- [x] **Step 1: Add one diagnostic record kind and fixed codes**

Add:

```go
const ReviewAuditKindDispatcherDiagnostic ReviewAuditKind = "dispatcher_diagnostic"

type ReviewAuditDispatcherDiagnostic string

const (
    ReviewAuditDiagnosticEnqueueDrop ReviewAuditDispatcherDiagnostic = "enqueue_drop"
    ReviewAuditDiagnosticSinkFailure ReviewAuditDispatcherDiagnostic = "sink_failure"
    ReviewAuditDiagnosticFlushExpiry ReviewAuditDispatcherDiagnostic = "shutdown_flush_expiry"
    ReviewAuditDiagnosticAfterClose ReviewAuditDispatcherDiagnostic = "enqueue_after_close"
)
```

Extend `ReviewAuditRecord` with only:

```go
DispatcherDiagnostic ReviewAuditDispatcherDiagnostic `json:"dispatcher_diagnostic,omitempty"`
DiagnosticCount      uint64                          `json:"diagnostic_count,omitempty"`
```

Validation requires a known code, positive count, and none of the action,
reviewer, comparison, corpus, or recovery fields. Continue to require an opaque
event ID and timestamp so every retained line has the existing integrity shape.

- [x] **Step 2: Add report counters**

Extend `ReviewAuditDiagnostics` with:

```go
EnqueueDrops         uint64 `json:"enqueue_drops"`
SinkFailures         uint64 `json:"sink_failures"`
ShutdownFlushExpiry uint64 `json:"shutdown_flush_expiry"`
EnqueueAfterClose    uint64 `json:"enqueue_after_close"`
```

Handle diagnostic records before action grouping, sum counts with overflow
saturation, and mark the report `partial` when any counter is non-zero. A
diagnostic record must not increment eligible, attempt, terminal, orphan, or
comparison counts.

- [x] **Step 3: Add schema and aggregation tests**

Pin strict validation, JSON redaction, duplicate diagnostic deltas, saturating
addition, and `partial` report status. Require unknown codes and mixed payloads
to fail validation.

- [x] **Step 4: Run focused red/green as the schema lands**

```bash
go test ./engine/permission/ -run 'ReviewAudit.*(Dispatcher|Diagnostic)' -count=1
```

## Task 2: Implement one bounded single-writer dispatcher

**Files:**

- Create: `engine/permission_review_audit_dispatcher.go`
- Create: `engine/permission_review_audit_dispatcher_test.go`

**Interfaces:**

```go
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

func newReviewAuditDispatcher(reviewAuditDispatcherOptions) *reviewAuditDispatcher
func (d *reviewAuditDispatcher) Enqueue(permission.ReviewAuditRecord)
func (d *reviewAuditDispatcher) Close(context.Context)
func (d *reviewAuditDispatcher) Diagnostics() reviewAuditDispatcherDiagnostics
```

- [x] **Step 1: Add a blocking-sink red test**

Use a sink whose `Record` closes `entered` and waits on `release`. Fill the
writer and queue, then measure producer completion by a channel deadline:

```go
done := make(chan struct{})
go func() {
    dispatcher.Enqueue(record)
    close(done)
}()
select {
case <-done:
case <-time.After(time.Second):
    t.Fatal("audit enqueue blocked behind sink")
}
```

Require a full queue to increment `EnqueueDrops` and never call the sink for the
dropped record.

- [x] **Step 2: Implement synchronized close and non-blocking enqueue**

Use a mutex only to serialize channel close against sends:

```go
d.mu.Lock()
defer d.mu.Unlock()
if d.closed {
    d.enqueueAfterClose.Add(1)
    return
}
select {
case d.queue <- record:
default:
    d.enqueueDrops.Add(1)
}
```

`Close` marks closed and closes the queue once, then selects between writer
completion and `ctx.Done()`. On expiry it increments `FlushExpiry`, cancels the
writer context, and returns immediately without waiting again.

- [x] **Step 3: Contain sink panic and error per record**

Wrap exactly one `sink.Record` call in a helper that recovers panic and converts
both panic and error into `SinkFailures`. Continue with the next accepted record
unless the dispatcher context is cancelled. Never log the recovered value or
record payload.

- [x] **Step 4: Coalesce diagnostic deltas on the writer**

After successful action-record writes and during normal drain, attempt to write
one diagnostic record per non-zero unreported delta. Use a dispatcher-owned
opaque event ID. If a diagnostic write fails, retain the delta for a later
attempt; do not recursively emit a diagnostic for the failed diagnostic write
in the same loop. The in-memory `Diagnostics` snapshot remains authoritative
for failures the sink cannot retain.

- [x] **Step 5: Prove lifecycle and exactly-once behavior**

Cover full queue, closed queue, sink error, sink panic, concurrent producers,
bounded close, cancellation, diagnostic retry, and exactly-once delivery for
records actually accepted. Run:

```bash
go test ./engine/ -run '^TestReviewAuditDispatcher' -count=1
go test -race ./engine/ -run '^TestReviewAuditDispatcher' -count=20
```

## Task 3: Move QueryEngine audit calls off the permission path

**Files:**

- Modify: `engine/engine.go`
- Modify: `engine/permission_review.go`
- Modify: `engine/permission_review_test.go`

- [x] **Step 1: Add engine ownership and initialize only when enabled**

Add to `QueryEngine`:

```go
permissionReviewAudit *reviewAuditDispatcher
```

Construct it in `newQueryEngineWithOptions` only when
`config.ApprovalReviewAudit != nil`, using a fixed production capacity of 128.
Do not leave a second direct sink path.

- [x] **Step 2: Replace synchronous sink I/O with enqueue**

Keep schema defaults in `recordPermissionReviewAudit`, then call:

```go
if e == nil || e.permissionReviewAudit == nil {
    return
}
if record.SchemaVersion == 0 {
    record.SchemaVersion = permission.ReviewAuditSchemaVersion
}
record.OccurredAt = e.permissionReviewNow()
e.permissionReviewAudit.Enqueue(record)
```

Remove `context.WithoutCancel`, direct `sink.Record`, and the local panic
wrapper. The function may retain the context parameter temporarily for call-site
compatibility, but it must not derive audit execution lifetime from it.

- [x] **Step 3: Close after reviewer producers stop**

Immediately after `cancelPermissionReviews`, close the dispatcher with a
250-millisecond timeout:

```go
shutdownCtx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
e.permissionReviewAudit.Close(shutdownCtx)
cancel()
```

Nil is a no-op. Do not delay shell, hook, transcript, or service cleanup beyond
that bound.

- [x] **Step 4: Prove permission and close independence**

Add tests in which the sink blocks while:

- the legacy classifier allows and denies;
- a structured prompt settles;
- context cancellation completes; and
- `QueryEngine.Close` runs.

Require the same permission result and event order as a non-audited engine.
Release the blocking sink in test cleanup so tests do not leak intentionally.

- [x] **Step 5: Run focused engine and race tests**

```bash
go test ./engine/ -run 'PermissionReviewAudit|ReviewAuditDispatcher' -count=1
go test -race ./engine/ -run 'PermissionReviewAudit|ReviewAuditDispatcher' -count=20
```

## Task 4: Keep CLI reports truthful under evidence loss

**Files:**

- Modify: `cmd/eino-agent/cmd/permission_review_audit.go`
- Modify: `cmd/eino-agent/cmd/permission_review_audit_test.go`

- [x] **Step 1: Expose the new typed counters without prose reconstruction**

Retain JSON field names from `ReviewAuditDiagnostics`. Human text output may
print only code plus count. Do not infer missing eligible, attempt, terminal, or
comparison records from a drop count.

- [x] **Step 2: Add retained and blocked-sink fixtures**

Require retained dispatcher diagnostic deltas to make the report partial. For
a sink that never accepts the final diagnostic, assert only the in-memory
dispatcher snapshot; do not require the impossible durable write to that same
blocked sink.

- [x] **Step 3: Run package-width tests**

```bash
go test ./engine/permission/ ./engine/ ./cmd/eino-agent/cmd/ -run 'ReviewAudit|PermissionReviewAudit' -count=1
```

## Task 5: Close P50.3 without promoting reviewer enforcement

**Files:**

- Modify: `docs/architecture/capabilities/permissions.md`
- Create: `docs/migration/verification/p50-3-review-audit-dispatcher.md`
- Modify: `docs/migration/verification/README.md`
- Create: `docs/migration/history/runtime/p50-3-review-audit-dispatcher.md`
- Modify: `docs/migration/history/README.md`
- Modify: `docs/migration/queue.yaml`
- Modify generated: `docs/migration/PLAN.md`

- [x] **Step 1: Record the non-authoritative lifecycle boundary**

Describe enqueue admission, evidence loss, bounded close, and the unavoidable
blocked-sink durability limit. Keep P22.3 and reviewer enforcement deferred.
Remove only P50.3 from the queue and render the next legal state.

- [x] **Step 2: Run final repository gates**

```bash
make fmt
make lint
make test
make build
make docs-check
go run ./scripts/migration_manifest.go check
git diff --check
```

- [x] **Step 3: Commit and open one atomic dispatcher PR**

```bash
git add engine/permission/review_audit.go engine/permission/review_audit_test.go engine/permission/review_audit_report.go engine/permission/review_audit_report_test.go engine/permission_review_audit_dispatcher.go engine/permission_review_audit_dispatcher_test.go engine/engine.go engine/permission_review.go engine/permission_review_test.go cmd/eino-agent/cmd/permission_review_audit.go cmd/eino-agent/cmd/permission_review_audit_test.go docs/architecture/capabilities/permissions.md docs/migration
git commit -m "fix(permission): dispatch reviewer audit asynchronously"
```

The PR must separate child/reviewer completion, queue acceptance, durable sink
retention, local gates, remote CI, and real product acceptance.
