# P50 Permission Runtime Remediation

**Status:** historical
**Accepted:** 2026-08-07
**Completed:** 2026-08-08
**Adoption:** `project-native`

> **Ownership:** detailed accepted contract, dependency order, promotion
> evidence, and rollback boundaries for P50.1-P50.3. Mutable execution state
> belongs in [`queue.yaml`](../queue.yaml); task-level test-first steps belong
> in [`docs/superpowers/plans/`](../../superpowers/plans/README.md).

## Outcome

Repair three independently observable permission-runtime defects before any
host-containment proof becomes an Auto Permission input:

1. P50.1 rejects a ProjectGraph permission decision when an external policy
   revision appears after the live check but before the action rebuild.
2. P50.2 counts reviewer latency only for retained attempt-terminal pairs.
3. P50.3 removes durable reviewer-audit writes from the synchronous permission
   path through one bounded non-blocking dispatcher.

The dependency order was mandatory. P50.1 removed the authorization race,
P50.2 corrected the reviewer latency denominator, and P50.3 isolated audit
storage behind one bounded non-blocking dispatcher. Each slice shipped in one
branch and pull request with its own rollback boundary.

The reviewed design is
[`Permission Runtime Remediation`](../../superpowers/specs/2026-08-07-permission-remediation-design.md).

## Current Evidence

### ProjectGraph revision window

[`QueryEngine.resolveProjectGraphHITLPermission`](../../../engine/graph_hitl.go)
now checks the rebuilt action against the batch's current revision before
settlement. The retained
[`TestP501ProjectGraphRejectsPolicyMutationBetweenCheckAndRebuild`](../../../engine/graph_hitl_test.go)
injects an unrelated exact rule after the live check and proves the current and
remaining batch decisions deny without persisting their rules. Concurrency,
persistence-failure, public cancellation, late-submit, repetition, and race
fixtures close G48 without adding a global policy lock.

This is narrower than the completed P37 settlement-chain repair. P37 serializes
decisions within one resume batch; it intentionally does not serialize ACP,
settings reload, or other policy writers on that mutex.

### Reviewer latency denominator

[`permission.BuildReviewAuditReport`](../../../engine/permission/review_audit_report.go)
now appends latency only when the same retained event group contains both an
attempt and a terminal record. A terminal-only setup or projection failure
remains visible in outcome and lifecycle diagnostics but cannot increase the
reviewer latency sample count. The retained
[`TestBuildReviewAuditReportDecisionMetrics`](../../../engine/permission/review_audit_report_test.go)
fixture records three attempts and four terminal results, and expects three
latency samples.

### Reviewer-audit decision-path I/O

[`QueryEngine.recordPermissionReviewAudit`](../../../engine/permission_review.go)
now attempts only non-blocking admission to an engine-owned capacity-128
single-writer dispatcher. The retained
[`TestP503BlockingAuditSinkDoesNotBlockPermissionPath`](../../../engine/permission_review_test.go)
holds the first eligible-record write on a channel and proves legacy allow and
deny, reviewer launch and event order, structured prompt settlement, and
permission outcome advance without release. Separate fixtures cover bounded
engine close, queue pressure, sink error/panic, cancellation, concurrent close,
diagnostic retry, and accepted-record exactly-once delivery. This closes G50.

## Shared Invariants

- QueryEngine remains the only permission coordinator and dispatch owner.
- `projectGraphHITLExecution` remains the ordinary resume-batch revision owner.
- External policy writers do not acquire the ProjectGraph batch mutex.
- Plan approval keeps its separate immutable revision contract.
- Reviewer shadow remains optional, advisory, and unable to authorize tools,
  suppress prompts, persist grants, or change permission mode.
- No new diagnostic contains raw tool input, prompts, paths, environment
  values, credentials, digest nonces, or reviewer payloads.
- TUI, Plain, and ACP continue to submit user decisions to the same engine-owned
  ProjectGraph settlement path. Headless entrypoints cannot fabricate an
  interactive decision, and standalone MCP remains outside ProjectGraph.

## P50.1 ProjectGraph Post-Rebuild Revision Fence

**Status:** completed 2026-08-07

Delivery and reproducible commands are recorded in the
[history](../history/runtime/p50-1-project-graph-revision-fence.md) and
[verification](../verification/p50-1-project-graph-revision-fence.md) records.

### Accepted contract

After the existing live-revision check succeeds, rebuild the canonical action
inside the batch critical section and require:

```go
initialAction.PolicySnapshotID == execution.currentPolicyRevision
```

A mismatch invalidates the batch and returns
`project graph permission intent expired` before settlement or persistence.
The current and all remaining ordinary decisions persist no batch-owned rule
and dispatch no tool. The existing post-settlement transition check remains in
place and advances only through the exact revision owned by a successful
settled action.

### Delivered evidence

- a barrier injects external policy mutation after the live check and before
  rebuild;
- concurrent decisions prove batch-lock ordering without timing sleeps;
- persistence failure, cancellation, repeated settlement, and late settlement
  prove no duplicate rule or dispatch;
- the original two-Read regression remains green under repetition and race.

The executable steps are in the
[`P50.1 implementation plan`](../../superpowers/plans/2026-08-07-p50-1-project-graph-revision-fence.md).

### Non-goals and rollback

P50.1 adds no global policy lock, new rule schema, reviewer behavior, Plan
authority, or sandbox behavior. Reverting only the post-rebuild fence and its
test interleaving seam restores the old race window; persisted exact rules need
no migration.

## P50.2 Reviewer Attempt-Latency Denominator

**Status:** completed 2026-08-08

Delivery and reproducible commands are recorded in the
[history](../history/runtime/p50-2-reviewer-latency-denominator.md) and
[verification](../verification/p50-2-reviewer-latency-denominator.md) records.

### Accepted contract

One latency sample exists only when the same retained audit event contains both
`reviewer_attempt` and `terminal`. Terminal-only setup/projection failures stay
visible in outcome and lifecycle diagnostics but contribute no reviewer
latency. With zero pairs, latency evidence is unavailable rather than zero.

### Delivered evidence

Fixtures cover completed, timeout, reviewer-unavailable, invalid-result,
terminal-only, attempt-only, zero-attempt, and unsorted latency input. Outcome
counts remain unchanged while latency samples exactly equal retained
attempt-terminal pairs. CLI evidence proves two terminal outcomes can yield one
latency sample and that a zero-pair JSON report omits percentile fields.

The executable steps are in the
[`P50.2 implementation plan`](../../superpowers/plans/2026-08-07-p50-2-reviewer-latency-denominator.md).

### Non-goals and rollback

P50.2 does not rename the metric, add setup latency, change reviewer routing,
or alter a permission outcome. Reverting only sample admission restores the old
measurement semantics.

## P50.3 Non-Blocking Reviewer-Audit Dispatcher

**Status:** completed 2026-08-08

Delivery and reproducible commands are recorded in the
[history](../history/runtime/p50-3-review-audit-dispatcher.md) and
[verification](../verification/p50-3-review-audit-dispatcher.md) records.

### Accepted contract

Each QueryEngine with an audit sink owns one bounded single-writer dispatcher.
Permission paths perform a non-blocking enqueue; a full or closed queue drops
the redacted record and increments a typed bounded diagnostic. One writer owns
sink calls. Sink failure, panic, cancellation, or flush timeout never feeds
back into permission classification, prompting, settlement, or shutdown.

Engine close stops reviewer producers, closes admission, and waits only for a
bounded flush context. A sink that ignores cancellation may retain its worker
goroutine, but cannot hold permission handling or QueryEngine close.

### Required evidence

Deterministic fixtures cover a blocked sink, full queue, concurrent producers,
sink error, sink panic, enqueue after close, bounded shutdown, and exactly-once
delivery for records accepted by the queue. Reports expose retained-window
incompleteness without reconstructing missing records.

The executable steps are in the
[`P50.3 implementation plan`](../../superpowers/plans/2026-08-07-p50-3-review-audit-dispatcher.md).

### Non-goals and rollback

P50.3 adds no remote telemetry, approval ledger, reviewer enforcement, or one
goroutine per record. Reverting the dispatcher lifecycle and restoring direct
sink calls is one atomic rollback.

## Promotion And Execution Order

The written design and task plans are complete. P50.1-P50.3 close G48-G50.
Completion of P50 is not automatic promotion evidence for reviewer enforcement
or the separate host-containment program.

| Order | Slice | Intake disposition | Atomic owner |
|---:|---|---|---|
| 1 | P50.1 | Completed; G48 closed | ProjectGraph rebuilt-revision fence and focused tests |
| 2 | P50.2 | Completed; G49 closed | Reviewer report sample admission and report/CLI tests |
| 3 | P50.3 | Completed; G50 closed | QueryEngine audit dispatcher lifecycle and diagnostics |

For each selected slice:

1. begin from then-current `origin/master`;
2. run the focused red/green/race evidence named above;
3. update only current architecture, gap, queue, verification, and history
   owners whose facts changed;
4. remove only the completed queue row and render the next legal state; and
5. run all repository and migration gates before opening the pull request.

## Compatibility Consequences

The program intentionally strengthens fail-closed behavior in one narrow
ProjectGraph interleaving and corrects reviewer evidence/latency isolation. It
does not change ordinary permission order, exact user authority, Plan
approval, supported transport ownership, or model-visible tool behavior.
