# P47.7 Lazy Execution Detail

**Status:** historical
**Closed gaps:** G41
**Completed:** 2026-08-07
**Adoption:** `combine`

> **Ownership:** completion evidence for exact lazy Task Explorer transcript,
> output, and lineage detail and final P47/G41 closure. Current behavior belongs
> in the [Task and Agent runtime architecture](../../../architecture/runtime/tasks-and-agents.md)
> and the [TUI architecture](../../../architecture/tui/README.md).

## Outcome

P47.7 preserves `RuntimeStateStore` as execution identity owner and
`TaskExplorerPanel` as presentation owner. WorkItems retain cached `overview`
and `activity` only. Exact execution rows add bounded `transcript`, `output`,
and `lineage` tabs after those two cached tabs. Entering a deep tab, explicit
refresh, or transcript older-page request returns a Bubble Tea command; render,
resize, scroll, focus, cached-tab navigation, and reducer replay call no reader
or action provider.

Transcript reuses `QueryEngine.AgentTranscriptPage` with its generation-bound
page limit and opaque cursor. Output and lineage use the new narrow
`QueryEngine.AgentExecutionDetail` request, which requires Agent ID, positive
generation, Session ID, and thread ID. Lineage reads only captured runtime
metadata. Output reads only a terminal current generation through the existing
bounded tail helper, then revalidates exact identity and output path. A live
continuation therefore cannot expose the prior generation's reused terminal
file, and a replacement during output I/O fails closed without returning data.
Retained historical detail is explicit unavailable rather than resolved from a
newer canonical generation.

The panel owns private per-tab request generations and caches. Every request
and result carries exact row, Session, thread, generation, tab, and cursor
identity. Selection/filter replacement, tab change, explicit supersession,
generation replacement, duplicate or out-of-order completion, and panel close
invalidate old work. Accepted results are defensively copied and never change
P47.1 action intent or P47.3 navigation targets.

## Compatibility

P47.1-P47.6 action, navigation, mixed-order, selection, filter/focus, mouse,
and cached-detail contracts remain unchanged. Ctrl+B, `/team`, `/tasks`,
activity, sidebar, WorkBoard, AgentRunner control, replay, ACP, provider,
permission, and wire ownership do not move. `AgentDetailSnapshot` remains the
eager compatibility reader for existing consumers. No durable schema, second
transcript/output store, historical execution restore, or new mutation path is
introduced.

## Proof And Review

Test-first evidence covers exact request validation, current terminal output,
nonterminal reused-file isolation, bounded valid UTF-8 tails, lineage without
output I/O, replacement during a blocked read, retained historical
unavailability, five execution tabs versus two WorkItem tabs, lazy provider
counts, exact cursor paging, stale/duplicate/out-of-order/closed-panel
rejection, filter replacement, render purity, no-color 40/80/120/180-column
frames, and real PTY deep-tab/resize/return/close cleanup. Focused engine/TUI,
race-selected, full engine/TUI package, repository test, lint, build, and pinned
new-finding lint gates passed on the final code.

Independent bounded review found two exact-output risks. Continuation reuses
one Agent output path, so the initial reader could label the preceding terminal
file as a live newer generation; it also lacked a read-after-I/O identity
check. The accepted fixes skip nonterminal output and revalidate exact identity
and path after a terminal read. A third review hypothesis claimed refilter could
retain another row's cache; source inspection showed `restoreSelection` already
resets detail on identity change, and a new deterministic regression preserves
that fail-closed behavior without adding a duplicate owner.

Documentation, queue, manifest, PTY, and diff gates also passed on the final
caller worktree. PTY evidence proves emitted terminal protocol, resize, close,
and cleanup; it does not claim physical-font or pixel-layout inspection,
live-provider behavior, or remote CI. Protected-master PR integration remains a
separate gate.

## Rollback

A squash revert removes the exact reader, Task Explorer async state, three deep
execution tabs, and their tests, returning Ctrl+T to the P47.6 cached
overview/activity view. No durable migration or data rollback is required.
P47.1-P47.6 remain valid, but G41 reopens until an exact lazy-detail boundary is
restored.
