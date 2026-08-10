# P31.3 Read-only Task Explorer

**Status:** historical
**Completed:** 2026-07-31

> **Ownership:** completion evidence for the presentation-only Task Explorer,
> exact-generation compatibility fence, responsive TUI projections, and
> rollback boundary. P31.4 owns durable execution-link admission and
> engine-declared mutation controls.

## Outcome

P31.3 completed the accepted `combine` slice without moving runtime or durable
authority. Ctrl+T now opens one responsive, read-only explorer whose only
production list input is `QueryEngine.TaskExplorerSnapshot`. Ctrl+B and
`/team` build their Agent rows from the same ordered execution section; the
activity tree and wide sidebar derive their summaries from that snapshot as
well.

The component owns only its cached snapshot, logical/execution filter, local
search, exact selection, cursor, detail level, scroll, and focus. Show and the
one-second refresh tick call the provider. Rendering filters and clips the
cached ordered rows without re-reading the engine or re-ranking selector
output.

## Compatibility And Identity Fence

Ctrl+B retains its existing detail, transcript, message, pause, resume, abort,
local-task output, and stop owners. An execution can reach those compatibility
paths only when its exact `(AgentID, Generation)` is also current in
`AppStateSnapshot`. Retained, replay-only, or otherwise noncurrent rows expose
read-only explorer facts instead.

Parent-chat Agent links also carry an exact immutable execution key. Their
identity is resolved once from the selector using the spawning tool identity;
a later selector refresh cannot upgrade a retained g1 link to current g2. A
trace that could not prove its identity on first observation remains
non-navigable. Existing local background tasks are visibly labelled
`Compatibility-only local task` and are never inferred from WorkItems.

`/team` preserves its existing all-sub-agent, read-only meaning while taking
only execution rows from the canonical selector. The runtime has no TeamID or
separate membership relation, so parent lineage is not reinterpreted as team
membership. Existing exact-current thread navigation remains read-only.

## Responsive And Historical Projection

The explorer has compact, standard, and wide layouts over the same facts. The
verified matrix covers 40, 80, 120, and 180 columns; heights below and at or
above 24 rows; empty, plan-only, execution-only, mixed, blocked, attention,
failure, and replay-only states; and CJK, combining marks, ZWJ emoji, long
tokens, no-color, reduced-motion, local search, focus, refresh, and exact
selection fallback.

Compact Task/Todo tool history remains recorded-event evidence. Expanded, raw,
and transcript views continue to show the complete sanitized recorded input
and result rather than substituting current WorkBoard state.

## Verification And Rollback

Focused generation, selection, display-cell, no-dispatch, performance, race,
golden, and real-program PTY tests pass. The PTY path opens and searches
Ctrl+T, changes focus, visits Ctrl+B and `/team`, resizes, closes, and verifies
terminal restoration. A 100-row fixture retains the G11 viewport-bounded p95
method and does not re-read the provider during steady rendering. Independent
second-line review found and closed selector reordering, AgentID-only
navigation, local-row labelling, and retained-generation upgrade defects; the
final re-review reported no findings.

The complete commands and source-owner checks are recorded in
[`p31-3-read-only-task-explorer.md`](../../verification/p31-3-read-only-task-explorer.md).

Rollback restores the previous `TaskAgentSnapshot` presentation routes. It
does not rewrite WorkBoard data, execution generations, Session state,
transcripts, controls, or the P31.1b reader floor. P31.4-P31.5 remain queued,
and no successor became `Ready` at closeout.
