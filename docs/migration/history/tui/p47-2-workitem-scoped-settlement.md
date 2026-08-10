# P47.2 WorkItem-Scoped Settlement

**Status:** historical
**Closed gaps:** G39
**Completed:** 2026-08-07
**Adoption:** `combine`

> **Ownership:** completion evidence for exact WorkItem terminal-settlement
> scope, preserved durable commit semantics, compatibility boundaries, and G39
> closure. Current behavior belongs in the
> [Task and Agent runtime architecture](../../../architecture/runtime/tasks-and-agents.md).

## Outcome

P47.2 keeps `LogicalWorkAdapter` as the sole durable logical-work mutation
owner and keeps `AgentRunner` as the execution-settlement owner. Under the
existing WorkBoard lock, the terminal guard now derives every WorkItem that the
current mutation moves from non-terminal to terminal, selects only immutable
execution links for those exact item IDs and the current BoardID, and queries
the existing settlement snapshot with that exact-generation union.

A settled WorkItem can therefore complete while another item has a live or
missing execution. A live, reserved, cancellation-pending, unresolved,
missing, duplicate, or otherwise unsettled execution still rejects the
terminal transition of its own linked WorkItem. Batch Todo replacement checks
every target item in the same mutation rather than accepting the first settled
target.

## Compatibility And Durability

The repair changes only link selection before the established settlement
oracle. Settlement result normalization, fail-closed errors, expected Board
and item revisions, the adapter mutex, projection reservation, marker-last
commit, mutation quarantine, and authority-v3 encoding remain unchanged.
Rejected transitions leave durable status, Board revision, and execution links
unchanged; successful transitions advance the Board revision once and retain
the immutable link history.

No public API, stored schema, migration, replay event, permission path, Task
Explorer action, TUI state, ACP wire behavior, Ctrl+B, `/team`, or `/tasks`
contract changed. P47.3 still owns exact generation-bound thread navigation,
and P47.4-P47.7 retain the deeper explorer presentation scope.

## Proof And Rollback

The adapter oracle covers A-settled/B-live and its inverse, multiple target
generations, reverse-order settlement results, missing facts,
cancellation-pending state, exact batch targets, invalid Board/item/revision
admission, and success/failure durable commit boundaries. The engine-wired
oracle runs one settled linked generation beside a real live unlinked
generation, proves the WorkBoard retains only the exact link, and completes the
target through `TaskManager.Stop`. Focused normal and race tests and the full
engine package test passed before repository closeout.

The repository formatting, lint, test, build, documentation, queue, migration
manifest, and diff gates passed on the final caller worktree.
Independent review found no actionable defect; a first review requested the
engine-wired unlinked-execution oracle, and the follow-up review confirmed that
the added coverage closed the evidence gap without a flake, deadlock, or race
finding.

A squash revert restores board-wide settlement scanning without data
migration, but reopens G39. Later P47 slices do not depend on the narrowed
selection for rollback safety; they consume only the resulting current
WorkBoard facts.
