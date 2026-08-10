# Permission Runtime Remediation Design

**Status:** active-plan
**Accepted:** 2026-08-07
**Last verified:** 2026-08-07
**Source snapshot:** `origin/master` at
`de74294b29f40d19bfd0e37f09889bd6f8037d90`

> **Ownership:** reviewed design and independent delivery boundaries for three
> permission-runtime defects; current behavior remains owned by
> [`permissions.md`](../../architecture/capabilities/permissions.md), P22 by
> [`p22-auto-permission-review.md`](../../migration/plans/p22-auto-permission-review.md),
> and P37 by
> [`p37-concurrent-exact-permission-settlement.md`](../../migration/plans/p37-concurrent-exact-permission-settlement.md)

## Outcome

Repair three defects before host containment becomes an Auto Permission input:

1. reject ProjectGraph permission settlement when an external policy revision
   appears between the live-revision check and action rebuild;
2. calculate reviewer latency only from reviewer attempt-terminal pairs; and
3. remove durable reviewer-audit writes from the synchronous permission path.

The repairs have different state owners, tests, and rollback boundaries. They
therefore ship as three independently reviewable branches and pull requests.
This design does not enable reviewer enforcement, promote P22.3, or change the
migration queue.

## Repair 1: Fence ProjectGraph Settlement At The Rebuilt Revision

### Verified defect

[`resolveProjectGraphHITLPermission`](../../../engine/graph_hitl.go) checks the
live policy revision before rebuilding the action. An external policy mutation
can occur after that check. The rebuild then captures the external revision,
settlement succeeds against it, and the post-settlement comparison can mistake
that revision for an expected chain advance.

This violates P37's rule that an external or unexplained revision invalidates
the remaining batch. The existing batch mutex serializes decisions in one
ProjectGraph resume; it does not serialize unrelated policy mutations.

### Accepted repair

Keep the existing pre-check, rebuild the action inside the batch critical
section, and then require:

```go
initialAction.PolicySnapshotID == execution.currentPolicyRevision
```

If the equality fails:

- mark the execution invalid;
- return `project graph permission intent expired` for the current decision;
- persist no grant;
- dispatch no tool; and
- reject remaining ordinary decisions in the batch.

The existing settlement path continues to detect drift after rebuild and
during persistence. Do not make unrelated policy writers acquire the batch
mutex: that would introduce cross-owner lock ordering and deadlock risk.

The implementation may extract a small compare-and-set-shaped helper, but it
must not create a second permission-settlement owner.

### Deterministic proof

Tests use barriers, not sleeps, to inject:

- external policy mutation after the pre-check and before rebuild;
- cancellation before and after the first settlement;
- persistence failure for the first decision;
- two distinct decisions contending on the batch lock; and
- repeated or late settlement attempts.

Every fixture asserts revision state, exact persisted rules, zero duplicate
persistence, and zero duplicate dispatch. The original two-Read ACP regression
continues to pass at high repetition and under the race detector.

## Repair 2: Use Reviewer Attempts As The Latency Denominator

### Verified defect

[`BuildReviewAuditReport`](../../../engine/permission/review_audit_report.go)
adds every terminal record's latency to reviewer p50/p95. A terminal such as
`projection_unavailable` can exist without a reviewer attempt, causing latency
sample count to exceed reviewer-attempt count.

### Accepted repair

Reviewer latency includes a sample only when the same event has both:

```text
reviewer_attempt + terminal
```

Eligible-only, terminal-only, setup failure, and projection-unavailable events
remain visible through outcome and incomplete/unavailable diagnostics, but do
not enter reviewer latency. When no attempt-terminal pair exists, reviewer
latency is unavailable rather than zero.

Do not silently rename the contaminated metric. A separate setup-latency
metric may be proposed later with its own denominator and consumer need.

### Deterministic proof

Fixtures cover:

- completed, timeout, error, malformed, and unavailable attempt-terminal
  pairs;
- terminal-only projection failure;
- attempt without terminal;
- zero attempts; and
- stable p50/p95 ordering for an unsorted event set.

The report must expose three attempts and three latency samples when a fourth
eligible action terminates before reviewer launch.

## Repair 3: Queue Reviewer Audit Without Blocking Permission

### Verified defect

[`recordPermissionReviewAudit`](../../../engine/permission_review.go) calls the
configured sink synchronously with cancellation removed. The default store
uses cross-process locking and durable filesystem I/O; a custom sink may block
without bound. Error and panic containment does not prevent latency from
changing the legacy permission outcome.

### Accepted owner

QueryEngine owns one bounded audit dispatcher:

```go
type ReviewAuditDispatcher struct {
    queue chan permission.ReviewAuditRecord
    sink  permission.ReviewAuditSink
}
```

`Record` at the permission seam becomes a non-blocking enqueue:

- enqueue succeeds immediately; or
- a full/closed queue drops the event and increments a bounded typed counter.

One writer goroutine calls the sink. Sink error or panic records a bounded
diagnostic and continues or terminates according to a fixed dispatcher state;
it never feeds back into permission settlement. Queue entries and diagnostics
contain only the existing redacted audit schema.

QueryEngine shutdown closes admission, waits for a configured bounded flush
deadline, and then cancels the writer. A deadline expiry records incomplete
audit evidence and must not delay engine shutdown indefinitely.

### Metrics and failure semantics

The aggregate report exposes retained-window incompleteness through typed
counts for:

- enqueue drops;
- sink failures;
- shutdown-flush expiry; and
- records accepted after dispatcher closure.

These counts do not reconstruct missing events or claim complete denominators.

### Deterministic proof

A blocking sink fixture holds the writer while the test proves that classifier,
prompt, permission result, and QueryEngine cancellation still complete. Other
fixtures cover full queue, sink panic, sink error, concurrent producers,
bounded close, and exactly-once delivery for records actually accepted into
the queue.

## Cross-Repair Invariants

- QueryEngine remains the permission and dispatch owner.
- Reviewer shadow remains opt-in and non-authoritative.
- No reviewer result executes a tool, suppresses a prompt, persists a grant,
  or mutates the permission mode.
- Explicit deny, Plan admission, exact action binding, cancellation, and
  supported entrypoint behavior remain unchanged.
- Diagnostics contain no raw tool input, prompt, absolute path, environment
  value, credential, digest nonce, or reviewer payload.
- A green repository gate does not replace deterministic concurrency proof.

## Delivery Order And Rollback

| Order | Repair | Primary owner | Rollback boundary |
|---:|---|---|---|
| 1 | P50.1 ProjectGraph revision fence | `engine/graph_hitl.go` and focused engine/ACP tests | Remove the post-rebuild fence and its interleaving hook only |
| 2 | P50.2 Reviewer latency denominator | `engine/permission/review_audit_report.go` and report tests | Restore the old sample selection only |
| 3 | P50.3 Non-blocking audit dispatcher | QueryEngine reviewer-audit lifecycle and store/report tests | Restore direct sink calls and remove dispatcher lifecycle as one unit |

Each repair starts from then-current `origin/master`. This design does not
promote itself into `docs/migration/queue.yaml`; migration intake and the
one-Ready-slice rule remain separately owned.

## Verification Boundary

Each repair begins with a failing focused regression. Permission concurrency
changes run focused race tests. Every code delivery closes with:

```bash
make fmt
make lint
make test
make build
go run ./scripts/migration_manifest.go check
git diff --check
```

Remote CI, repeated local stress, race evidence, and real entrypoint acceptance
are reported separately.

## Non-Goals

- Reviewer enforcement or P22.3 promotion.
- A new semantic reviewer projection or shell parser.
- Host-process sandboxing or permission reduction; those belong to the
  separate Darwin containment design.
- Broad permission-module refactoring unrelated to the three defects.
