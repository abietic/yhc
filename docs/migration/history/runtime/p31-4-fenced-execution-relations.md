# P31.4 Fenced Execution Relations and Controls

**Status:** historical
**Completed:** 2026-07-31

> **Ownership:** completion evidence for immutable WorkItem/execution
> relations, ordered Agent admission, exact-generation controls, settlement
> and deletion fences, and the retained rollback floor. P31.5 owns old-owner
> deletion and cross-entrypoint closeout.

## Outcome

P31.4 completed the accepted `combine` slice. WorkBoard remains the durable
logical-work owner, `AgentRunner` remains the execution and settlement owner,
`RuntimeStateStore` remains a bounded projection, and the engine owns the only
Task Explorer action dispatcher.

Agent input may carry one optional typed WorkItem reference. The runner
validates and reserves an exact generation, releases its registry lock, and
asks the engine to commit a bounded immutable WorkExecutionLink. Only after
that commit may the launch event, child admission and metadata, live-runner
installation, and executor entry occur. Continuation appends generation N+1;
it cannot rewrite the predecessor relation. Unlinked input and existing
Task/Todo result bytes are unchanged.

## Durability And Settlement

The first committed link upgrades the authority record to version 3 before a
version-2 marker raises the minimum reader to `workboard/v3`. Marker absence or
the old marker retains valid v2 authority; a prepared v3 record is repaired
marker-last on reopen. The failure inventory covers every authority/marker
atomic-write stage plus encode and marker-reread seams. A linked Session never
downgrades during rollback.

Terminal WorkItem mutation holds the WorkBoard authority lock while reading a
WorkBoard-free runner settlement projection. Reserved, live,
cancellation-pending, or unresolved generations reject terminal mutation;
only durable terminal or superseded generations settle. Cancellation
acceptance is not settlement, and execution never auto-completes logical work.

## Controls And Session Lifecycle

The canonical snapshot relates exact BoardID/WorkItem and AgentID/generation
facts. The engine declares inspect, switch, send, pause, resume, cancel, and
continue capabilities and fences every request by exact board revision and
execution generation. Runtime revision is correlation only. Replay-only,
stale, unresolved, evicted, and pre-dispatch-failed rows cannot gain live
mutation; a forged request fails the same capability check.

Active Session deletion closes one shared admission gate before taking the
WorkBoard lifecycle lock. It rejects reservations, live or
cancellation-pending links, unsettled durable facts, and any parent-Session
execution that can still write. Rejection reopens admission; successful
transcript removal leaves it closed. Resume rebinds the same settlement and
admission callbacks, fork strips every relation, and linked v3 authority
rejects destructive backup recovery.

## Verification And Rollback

Focused ordering, exact-control, marker-last crash, recovery, continuation,
replay/pre-dispatch, terminal guard, active deletion, TUI confirmation/result,
race, and PTY lifecycle evidence passes. Independent second-line review found
and closed a replay-only continuation capability leak; final re-review
reported no findings. Reproducible commands and source-owner checks are in
[`p31-4-fenced-execution-relations.md`](../../verification/p31-4-fenced-execution-relations.md).

Rollback stops new linked admission and hides mutation controls, then waits
for admitted generations to settle. It retains the v3 reader, marker,
authority record, immutable relations, Agent APIs, and durable execution
evidence. It cannot detach, reassign, truncate, recover through the legacy
backup, or downgrade a linked Session. P31.5 remains queued, and no successor
became `Ready` at closeout.
