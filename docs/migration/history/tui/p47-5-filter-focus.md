# P47.5 Filter and Focus Navigation

**Status:** historical
**Advanced gap:** G41 remains open for P47.6-P47.7
**Completed:** 2026-08-07
**Adoption:** `project-native`

> **Ownership:** completion evidence for Task Explorer filter/search
> composition, explicit focus, truthful hints, mouse containment, and partial
> G41 closure. Current behavior belongs in the [Task and Agent runtime
> architecture](../../../architecture/runtime/tasks-and-agents.md) and the
> [TUI architecture](../../../architecture/tui/README.md).

## Outcome

P47.5 keeps the engine-owned `TaskExplorerSnapshot`, row ordering, hidden
facts, exact action declarations, and runtime revisions unchanged. The
`TaskExplorerPanel` now projects four local filters: `all`, `active`,
`attention`, and `terminal`. Search is an AND condition over the selected
filter. WorkItem and execution predicates consume only their canonical status,
phase, and row-level attention facts; they do not upgrade hidden or
snapshot-level evidence into selectable rows.

Controls, list, and detail are explicit focus regions. `/`, Enter, Esc, Tab,
and Shift+Tab form one deterministic state machine, while action input and
confirmation retain modal ownership. One private descriptor owns the canonical
panel keys and their hints. Execution-action hints state `disabled` from the
selected exact generation's engine declarations; filtering and focus never
infer eligibility.

Render publishes bounded controls, visible-row, and detail hit regions. Any
refresh, refilter, focus, selection, unavailable snapshot, or other stale frame
invalidates that geometry. App routes Task Explorer pointer input before chat;
clicks change only local focus, filter, search, or exact selection, and wheel
movement reuses the clamped keyboard path. No pointer event submits a runtime
action.

## Compatibility

P47.1 action-intent correlation, P47.3 exact navigation, P47.4 mixed ordering
and composite selection, WorkBoard, AgentRunner, transcript, replay, durable
state, ACP, provider, permission, and wire contracts did not change. Ctrl+B,
`/team`, and the provider-less legacy TaskPanel fallback retain their existing
owners and bindings. P47.6 still owns cached overview/activity structure;
P47.7 still owns lazy transcript/output/lineage I/O. G41 therefore remains
open.

## Proof And Review

Deterministic tests cover the complete filter truth table, filter/search
composition, unchanged source snapshot and hidden facts, exact selection
preservation and fallback, empty reset, forward/reverse focus, search and
prompt priority, truthful disabled hints, no-color output at 40x20 through
180x30, list/control/detail clicks, wheel parity, chat containment, and
unavailable or stale geometry rejection. Focused P47 compatibility suites,
their race-selected subset, the complete TUI package, and the real PTY
filter/search/focus/resize/lifecycle workflow passed.

Independent bounded review found one stale-geometry boundary: an unavailable
frame initially retained live control hit regions. The accepted finding drove
geometry invalidation on refresh and local projection changes plus a negative
unavailable/pre-render/stale-frame oracle. The affected normal/race/PTY suites
and pinned v2 new-finding lint then passed.

The repository formatting, lint, test, build, documentation, queue, migration
manifest, and diff gates passed on the final caller worktree. PTY evidence
proves terminal protocol parsing, resize, close, and cleanup; it does not claim
physical-font or pixel-layout inspection, live-provider behavior, or remote CI.
Those remain separate acceptance classes and the protected-master PR is a
separate integration gate.

## Rollback

A squash revert removes the panel-local filter/focus/geometry seam and returns
Ctrl+T to the P47.4 mixed list without data migration. P47.1-P47.4 remain
valid, but rollback removes this portion of G41 closure and makes P47.6
inapplicable until the predecessor contract is restored.
