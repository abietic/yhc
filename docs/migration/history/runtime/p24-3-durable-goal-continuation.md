# P24.3 Durable Goal Continuation

**Created:** 2026-07-29
**Completed:** 2026-07-29
**Status:** historical
**Adoption:** `adapt`

> **Ownership:** completion evidence for P24.3. Current continuation, runtime
> input, Session, and transcript behavior belongs in
> [`query-engine.md`](../../../architecture/runtime/query-engine.md),
> [`input-queue.md`](../../../architecture/runtime/input-queue.md),
> [`sessions.md`](../../../architecture/state/sessions.md), and
> [`transcripts.md`](../../../architecture/state/transcripts.md). Executable
> order belongs in [`migration/PLAN.md`](../../PLAN.md).

## User Problem

P24.2b could prove exact Goal state, terminal identity, provider usage, and
remaining budget, but an eligible completed turn had no durable handoff to a
possible next turn. A process could not prove whether it had created, claimed,
delivered, rejected, or recovered a continuation without inventing another
queue or inferring work from prose.

P24.3 closes that durability boundary. It deliberately does not make Goal
continuation user-visible or production-reachable.

## Delivered Contract

Goal persistence advances to version 3 and may carry one positive-version
continuation cursor. Versions 1 and 2 migrate without fabricating a cursor and
preserve the existing continuation ordinal. The cursor and matching
`RuntimeItemGoalContinuation` bind:

- the saved root Session and thread;
- Goal schema, Goal, objective, and state revisions;
- predecessor Goal turn, completed terminal reason, sequence, and time;
- budget, consumed tokens, and usage-ledger revision;
- the next continuation ordinal;
- runtime-input revision; and
- deterministic item, checkpoint, and next-turn identities.

Eligible terminal aftercare writes the cursor in the complete Goal checkpoint
before idempotently enqueuing the item at `PriorityLater`. Checkpoint failure
creates neither value. Queue failure leaves one recoverable cursor and never
advances a second ordinal.

The item remains dormant in production. Generic idle and safe-point claims
exclude it, and enqueue or recovery sends no generic subscription signal.
Public runtime-item submission has no model prompt for the new kind. Current
TUI, Plain, headless, ACP, child/review, and standalone-MCP entrypoints
therefore cannot claim or wake for it. One unexported engine seam exists only
to prove admission and delivery behavior before P24.4 selects a production
consumer.

## Admission, Settlement, And Recovery

The private admission serializes with Plan and Goal controls. Immediately
before the item becomes a turn, it revalidates exact cursor payload, Goal and
objective, predecessor terminal, runtime revision, accounting, remaining
budget, permission interruption, cancellation, and newer user input.

Pause, edit, clear, budget change, cancellation, and explicit user input
permanently supersede a pending or claimed-but-unadmitted item. The Goal
checkpoint records `rejected` before the coordinator writes its typed
rejection and settles the item. A failed rejection checkpoint remains
retryable and may release the same item; a committed rejection never releases
to pending.

After admission, the system-generated continuation message commits the exact
runtime-item receipt to the transcript. That commit settles the coordinator
before provider entry, then the Goal cursor records `delivered`. Restart
combines the Goal cursor, runtime-input ledger, rejection, and transcript
coverage:

- pending or admitting identity recreates at most the same deterministic item;
- an unconfirmed processing item returns once to pending;
- a transcript-covered item is settled and the Goal is paused without
  redelivery;
- rejected and delivered dispositions never recover to pending; and
- conflicting payloads, stale revisions, corrupt or unknown versions, and
  unsupported scope fail closed before model or tool entry.

## Evidence

The implementation and focused fixtures are owned by:

- [Goal cursor identity, admission, rejection, receipt, and recovery](../../../../engine/goal_continuation.go);
- [runtime-item schema, dormant scheduling, rejection, and ledger recovery](../../../../engine/input_coordinator.go);
- [terminal aftercare and Goal turn admission](../../../../engine/goal_runtime.go);
- [version-3 Goal persistence and v1/v2 migration](../../../../engine/goal_persistence.go);
- [Session schema and defensive restore cloning](../../../../engine/session/branch.go);
- [restore-staging reconciliation](../../../../engine/restore_staging.go); and
- [P24.3 durability, reachability, crash-window, and concurrency tests](../../../../engine/goal_continuation_test.go).

Focused normal and race tests cover eligible and ineligible terminals,
deterministic enqueue and conflicts, generic claim/signal exclusion, queue and
checkpoint failure, processing and receipt recovery, durable rejection,
post-claim user-input supersession, rejection-checkpoint failure, control and
cancellation races, permission interruption, v2 migration, unknown runtime
kinds, and public submission exclusion.

Independent review found one P1: user input arriving after claim but before
admission could make the generic error path release the Goal item to pending
before a durable rejection. The corrected path propagates a typed permanent
disposition, checkpoints `rejected`, rejects and settles the coordinator item,
and suppresses release. A separate fixture proves that failure before the
rejection checkpoint remains retryable with exactly the original cursor.
Re-review found no P0, P1, or P2 finding.

## Compatibility, Rollback, And Remaining Scope

Older readers reject version-3 Goal metadata and unknown runtime-item kinds
instead of silently discarding continuation state or treating it as steering.
Rollback must first leave production claim disabled, durably pause every
active Goal, reject or cancel and settle every Goal item, checkpoint that
state, and verify the Goal-item ledger is empty. Only then may one squash
revert remove the schema and runtime kind.

P24.4 still owns the opt-in TUI workflow, Goal command and model-tool controls,
public feature flag, progress, and first user-visible automatic continuation.
Plain, headless, ACP, default promotion, and G21 closeout remain later slices.
