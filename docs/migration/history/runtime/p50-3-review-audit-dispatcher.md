# P50.3 Non-Blocking Reviewer-Audit Dispatcher

**Status:** historical
**Closed gaps:** G50
**Completed:** 2026-08-08
**Adoption:** `project-native`

> **Ownership:** completion record for P50.3/G50; current reviewer-audit
> behavior belongs in
> [`permissions.md`](../../../architecture/capabilities/permissions.md)

## Outcome

Reviewer-audit storage no longer executes on permission or reviewer producer
goroutines. Each configured QueryEngine owns one bounded single-writer queue;
producers attempt admission without waiting, and sink latency, error, panic, or
queue pressure cannot alter permission classification, prompt settlement,
reviewer events, grants, or tool dispatch.

Engine close stops reviewer producers before closing audit admission and waits
at most 250ms for the dispatcher. A sink that ignores cancellation may retain
the one writer goroutine blocked inside external code, but it cannot hold
QueryEngine close. Queue drops, sink failures, flush expiry, and
enqueue-after-close attempts have typed saturated in-memory counters. The
writer durably coalesces unreported deltas only when the sink can accept them;
reports mark retained diagnostics partial without inventing missing records.

## Compatibility And Rollback

Permission outcomes and event order are unchanged. Audit writes may now trail
the permission outcome, and process termination or bounded shutdown can lose
accepted-but-not-retained evidence. This is explicit measurement degradation,
not authorization degradation. The local store keeps its existing locking,
rotation, strict schema, retention, and deletion ownership.

A squash revert removes the dispatcher lifecycle and typed diagnostics as one
atomic rollback. No permission state, grant, rule, Session, transcript, or
remote schema migration is required. Restoring direct sink calls also restores
the synchronous-latency defect, so it is not a safe partial rollback.

## Evidence

Deterministic blocked-sink, full/closed queue, sink error/panic, diagnostic
retry, concurrent producer/close, cooperative cancellation, exactly-once
accepted delivery, structured prompt, permission cancellation, and bounded
engine-close fixtures cover the accepted contract. Strict schema, saturated
aggregation, partial-report, and provider-free CLI fixtures cover evidence
loss without raw payloads or reconstructed denominators.

Focused, race, repository, documentation, queue, manifest, and diff gates are
recorded in the
[verification record](../../verification/p50-3-review-audit-dispatcher.md).
Remote CI remains a separate merge gate.
