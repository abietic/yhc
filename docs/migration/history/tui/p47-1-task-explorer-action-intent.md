# P47.1 Task Explorer Exact Pending Action Intent

**Status:** historical
**Closed gaps:** G38
**Completed:** 2026-08-07
**Adoption:** `combine`

> **Ownership:** completion evidence for immutable pending Task Explorer
> action identity, exact result correlation, compatibility boundaries, and G38
> closure. Current behavior belongs in the
> [TUI architecture](../../../architecture/tui/README.md).

## Outcome

P47.1 keeps `TaskExplorerPanel` as the sole owner of transient Ctrl+T action
state and keeps the engine provider as the only mutation authority. Starting
send, continue, or cancel confirmation now captures one immutable intent from
the displayed row and cached snapshot: request ID, exact BoardID and board
revision, correlation-only runtime revision, exact AgentID and generation,
queued-input MessageID, action, and textual target label. Mutable payload text
is stored separately and copied only when the user submits.

A refresh can replace rows, remove the original row, or move selection to an
equal-looking execution, but it cannot rewrite the pending intent. Submission
therefore sends the original identity to the engine for current-truth
reauthorization instead of rebuilding a request from the current selection.
The prompt renders the frozen textual target without provider or runtime I/O.

## Correlation And Compatibility

Before synchronous provider dispatch, the panel copies the submitted intent
locally and clears only that old pending value. If dispatch re-enters the panel
and starts a newer prompt, the older result cannot clear or rewrite it. Exact
result matching covers request, board, board revision, AgentID, generation,
MessageID, and action. A successful send uses the engine's established
`MessageID == RequestID` result rule; other actions preserve the frozen
request MessageID. Runtime revision remains correlation-only because the
engine returns current reauthorization truth rather than echoing it exactly.

Immediate inspect, switch, pause, and resume actions still use the existing
engine-declared availability and exact selected execution. No public API,
durable format, engine event, replay, permission, ACP, Ctrl+B, `/team`,
WorkBoard, or `AgentRunner` behavior changed. P47.2 owns WorkItem-scoped
settlement, P47.3 owns exact navigation activation, and P47.4-P47.7 retain the
deeper explorer presentation scope.

## Proof And Rollback

The table-driven regression exercises send, continue, and cancel across both
original-row removal and equal-label row reordering with changed board and
runtime revisions. It first reproduced retargeting to the new selection, then
proved every submitted field remains bound to the original intent. Separate
tests prove an older synchronous result cannot erase a newer prompt and that
repeated render calls are pure. The existing P31.4 confirmation contract now
also asserts the frozen target shown to the user. Independent review found one
send-result correlation mismatch against the real engine shape; an additional
red test reproduced the missing refresh before the action-specific rule closed
it.

Focused normal and race tests plus the repository formatting, lint, test,
build, documentation, migration-manifest, queue, and diff gates close the local
candidate. This presentation-state repair changes no terminal control-byte,
process-lifecycle, responsive-layout, durable, or live-provider boundary, so a
new PTY or physical-terminal claim is not required. Remote CI is reported
separately when unavailable because of quota exhaustion.

A squash revert restores selection-at-submit behavior without data migration,
but reopens G38. No later P47 or P48 slice is required for rollback.
