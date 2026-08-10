# P47.6 Snapshot Detail Structure

**Status:** historical
**Advanced gap:** G41 remains open for P47.7
**Completed:** 2026-08-07
**Adoption:** `combine`

> **Ownership:** completion evidence for defensive cached Task Explorer detail,
> row-kind `overview`/`activity` capability, independent detail navigation, and
> partial G41 closure. Current behavior belongs in the [Task and Agent runtime
> architecture](../../../architecture/runtime/tasks-and-agents.md) and the
> [TUI architecture](../../../architecture/tui/README.md).

## Outcome

P47.6 keeps `QueryEngine.TaskExplorerSnapshot` as the bounded runtime truth and
`TaskExplorerPanel.Refresh` as the only panel provider boundary. Refresh now
takes a defensive local copy of WorkItems, executions, links, attention,
diagnostics, every consumed nested slice, and hidden-count maps. Provider-owned
backing storage therefore cannot change a cached frame; only a later explicit
refresh can observe new provider values.

The selected WorkItem or exact execution exposes textual `overview` and
`activity` tabs. Overview contains only cached identity, lifecycle, ownership,
description, relation, replay, or predispatch facts supported by that row
kind. Activity contains only cached live-link, execution-key, tool/count,
observation, link, attention, and diagnostic facts. WorkItem links require
exact `(BoardID, WorkItemID)` and execution facts require exact
`(AgentID, Generation)`. Snapshot attention and diagnostics have no BoardID;
when the same WorkItemID exists on multiple boards, the panel fails closed and
does not attribute those boardless facts to either row.

Detail focus owns Left/Right and `h`/`l` tab switching plus vertical, page,
bound, and wheel scrolling. The tab and offset reset on an exact selection
change, while a same-selection refresh preserves them. Detail navigation never
moves the list cursor, selection, or offset. The sticky textual header and
bounded range indicator preserve current tab and scroll state without relying
on color. Rendering, tab switching, scrolling, mouse input, resize, and replay
call no snapshot/action provider, engine reader, transcript, filesystem, or
Git I/O and return no Bubble Tea I/O command.

## Compatibility

P47.1 action-intent correlation, P47.3 exact navigation, P47.4 mixed ordering,
P47.5 filter/focus/mouse containment, WorkBoard, AgentRunner, durable state,
replay, ACP, provider, permission, and wire contracts did not change. No
engine schema, engine API, async result, transcript reader, or output store was
added. Ctrl+B, `/team`, `/tasks`, activity, sidebar, and the provider-less
legacy TaskPanel retain their existing owners. P47.7 alone owns lazy exact
transcript, output, and lineage tabs, so G41 remains open.

## Proof And Review

Test-first evidence covers WorkItem and execution capabilities, exact and
unavailable activity, same-label and shared-thread generation collisions,
defensive mutation of every consumed backing store, provider/action counters,
reducer replay without Tea commands, independent keyboard and mouse scroll,
selection/tab/offset transitions, no-color frames from 40x20 through 180x30,
wide double projection, resize, and terminal lifecycle cleanup. Focused
P47.1/P47.3-P47.6 compatibility suites, race-selected tests, the complete TUI
package, and the real PTY tab/scroll/resize/close workflow passed.

Independent bounded review found one exact-association gap: boardless
WorkItem attention and diagnostics were initially matched only by WorkItemID.
The accepted finding added the duplicate cross-board ID fail-closed rule and a
deterministic negative oracle. The affected focused, race, and PTY suites then
passed.

The repository formatting, lint, test, build, pinned new-finding lint,
documentation, PTY, queue, migration-manifest, and diff gates passed on the
final caller worktree. PTY evidence proves terminal protocol parsing, resize,
close, and cleanup; it does not claim physical-font or pixel-layout inspection,
live-provider behavior, or remote CI. Protected-master PR integration remains
a separate gate.

## Rollback

A squash revert removes the panel-local clone, tab, pure projection, and
detail viewport state and returns Ctrl+T to the P47.5 mixed filter/focus view.
No durable migration is required. P47.1-P47.5 remain valid, but rollback
removes this portion of G41 closure and makes P47.7 inapplicable until the
predecessor contract is restored.
