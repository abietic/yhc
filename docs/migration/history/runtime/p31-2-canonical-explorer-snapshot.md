# P31.2 Canonical Explorer Snapshot

**Status:** historical
**Completed:** 2026-07-31

> **Ownership:** completion evidence for the process-local WorkBoard projection,
> immutable execution generations, bounded engine selector, cold bootstrap,
> current-view compatibility, and rollback boundary. P31.3 owns any Task
> Explorer presentation.

## Outcome

P31.2 completed the accepted `combine` slice without creating another durable
authority. WorkBoard v2 remains the only durable logical-work record.
`RuntimeStateStore` remains a process-local reducer. One engine-owned
`TaskExplorerSnapshot` defensively joins those owners into deterministic
WorkItem, execution, fixture-link, attention, diagnostic, revision, and hidden
count rows.

Cold construction, resume, fork activation, and restore staging bootstrap the
projection only from an already validated authoritative WorkBoard. They do not
write a board, emit a tool result or runtime event, enqueue input, call a model,
request permission, run Git, or start, resume, or message an Agent. Unmarked
legacy scope and standalone MCP retain the old compatibility selector and make
no replay or durability claim.

## Publish And Recovery Boundary

Each Task/Todo mutation builds and validates the next authority record, reserves
the exact current BoardID/revision transition in the projection reducer, commits
the durable next revision, swaps the prepared in-memory record, and only then
returns success. Reservation or validation failure precedes durable commit and
does not replace either owner.

The post-commit swap has no normal validation, capacity, or I/O branch. The
injected error and panic seams still fail closed: the adapter returns
`committed_projection_uncertain` with `retry_safe=false`, exposes no normal
success, and quarantines every later mutation in that process. The durable
WorkBoard remains authoritative, and a fresh adapter reconstructs the committed
revision before dispatch.

## Execution And Selector Boundary

The runtime reducer retains immutable execution generations by
`(AgentID, Generation)`, complete lineage, observation ordinal, and replay-only
state. It validates durable restore identity before mutation. Exact live
attachments receive a new process-local ordinal; cold restore rows remain
ordinal-free and replay-only. The primary
projection retains 128 generations, removes terminal before live rows, reports
cumulative eviction plus currently hidden live identities, and never rejects
the 129th admitted live execution.

WorkItems, executions, explicit fixture links, and attention each have a
128-row primary bound. Inline display fields are limited to 512 runes, and
terminal archive pages are limited to 100 rows. Ordering uses only accepted
revision, status/phase, attention, observation ordinal, stable order, and
identity tie-breaks; wall-clock time is not causal. Missing and stale links
remain visible, and no label, parent field, task text, timestamp, or transcript
can infer or repair a relation.

Every resolvable row exposes only `inspect`. Production creates no
WorkExecutionLink and changes no Agent admission, dispatch, TUI control, ACP
wire, or Session schema. `TaskAgentSnapshot` consumes the same canonical runtime
selection input while preserving current Ctrl+T, Ctrl+B, `/team`, sidebar,
output, stop, and shortcut behavior.

## Verification And Rollback

Deterministic replay, bootstrap conflict, next-revision publish, quarantine and
fresh repair, immutable lineage, generation retention, explicit stale/missing
links, 129/1,025/512/100 boundaries, defensive copies, concurrent reads/writes,
old-view equivalence, repository gates, and source/documentation gates pass.
The complete commands and review evidence are recorded in
[`p31-2-canonical-explorer-snapshot.md`](../../verification/p31-2-canonical-explorer-snapshot.md).

Rollback removes only the process-local reducer, publish hook, execution
generation projection, selector, and compatibility composition. It does not
rewrite WorkBoard data, lower the WorkBoard v2 reader floor, resurrect a legacy
Session writer, alter Agent generation identity, or remove existing TUI
controls. P31.3-P31.5 remain separately queued, and no successor became
`Ready`.
