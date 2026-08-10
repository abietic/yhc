# P47.4 Mixed Rows and Stable Selection

**Status:** historical
**Advanced gap:** G41 remains open for P47.5-P47.7
**Completed:** 2026-08-07
**Adoption:** `project-native`

> **Ownership:** completion evidence for the mixed Ctrl+T row projection,
> exact presentation selection, compatibility boundaries, and partial G41
> closure. Current behavior belongs in the [Task and Agent runtime
> architecture](../../../architecture/runtime/tasks-and-agents.md) and the
> [TUI architecture](../../../architecture/tui/README.md).

## Outcome

P47.4 keeps `QueryEngine.TaskExplorerSnapshot` as the ordered runtime read
model and deepens only the `TaskExplorerPanel` Module. Ctrl+T now opens one
mixed list: the engine-sorted WorkItems followed by the engine-sorted exact
execution generations. It invents no cross-kind relation or comparator.

Every mixed row begins with textual `WorkItem` or `Execution` kind, so narrow
and no-color frames do not depend on styling alone. One mutually exclusive
composite key owns selection: `(BoardID, WorkItemID)` for logical work or
`(AgentID, Generation)` for execution. Refresh and snapshot reorder restore
only that exact value. A missing row selects the clamped prior cursor; an empty
projection clears cursor, selection, detail, and offset; resize changes only
the visible offset.

## Compatibility

The engine snapshot, group ordering, action declaration, public API, durable
state, replay, WorkBoard, transcript, permission, ACP, and wire contracts did
not change. WorkItem rows still cannot dispatch execution controls. Exact
execution actions and switching retain the P47.1/P47.3 intent, declaration,
result-correlation, and navigation paths. Ctrl+B, `/team`, the Agent picker,
and the private logical-only/execution-only panel fixtures keep their existing
projections.

P47.5 still owns local filters, focus regions, mouse/key parity, and resolved
hints. P47.6/P47.7 still own snapshot tabs and lazy transcript/output/lineage
I/O. G41 therefore remains open.

## Proof And Review

Collision-heavy tests cover same-label WorkItems, same-thread executions, and
multiple generations; insertion, reorder, removal, empty state, and resize;
WorkItem action rejection and exact execution dispatch; and 40x20, 80x24,
120x30, and 180x30 no-color bounded frames with textual kind. Focused P47.4
tests, P47.1/P47.3 compatibility tests, their race-selected subset, and the
complete TUI package passed.

Independent bounded review reported no finding across mixed ordering, exact
identity, fallback, resize purity, action compatibility, and scope containment.
The repository formatting, lint, test, build, documentation, queue, migration
manifest, and diff gates passed on the final caller worktree. This slice
changes no terminal protocol or process lifecycle, so it claims no new PTY,
physical-terminal, live-provider, or real-repository acceptance. Remote CI
remains a separate protected-master PR gate.

## Rollback

A squash revert can route Ctrl+T back to the logical-only private projection
and remove the mixed labels and identity tightening without durable migration.
P47.1-P47.3 remain valid, but rollback removes the P47.4 portion of G41 closure.
