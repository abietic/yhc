# TUI Migration History

**Status:** historical
**Last verified:** 2026-08-07

> **Ownership:** this file indexes completed TUI modernization stages and their
> closeout evidence. It does not own current status, active order, or backlog.
> Current facts belong in [`migration/STATUS.md`](../../STATUS.md); accepted future
> work belongs in [`migration/PLAN.md`](../../PLAN.md); unresolved gaps belong in
> [`migration/REMAINING.md`](../../REMAINING.md).

## M0-M7 execution plan

[`m0-m7-refinement-plan.md`](m0-m7-refinement-plan.md) is the historical
executable checklist for the modern TUI track. It is retained as completion
evidence, not an active plan.

## M0-M7 completion report

[`m0-m7-completion-report.md`](m0-m7-completion-report.md) records the closeout
summary, delivered architecture, verification evidence, and commit decomposition
for M0-M7.

## P27 selection viewport geometry

[`p27-selection-viewport-geometry.md`](p27-selection-viewport-geometry.md)
records the completed P27 `combine` program: one final-frame chat projection,
same-render exact selectable semantics, content-bound interaction identity,
generation-fenced edge scrolling, and one typed truthful clipboard service
over the existing serialized `TerminalOutput`. It is the final delivery
evidence for G30 closure.

## P35 notification lifecycle

[`p35-1-tui-notification-lifecycle.md`](p35-1-tui-notification-lifecycle.md)
records the completed P35.1 `combine` repair: one bounded latest-three
engine-to-TUI mailbox, a sole pump that never holds its lock across
`Program.Send`, typed `App.Update` delivery, generation-fenced earliest idle
expiry, pure reads/rendering, ordered close/join, unchanged presentation and
non-TUI behavior, and G8 closure.

## P40 startup theme polarity

[`p40-1-startup-theme-polarity.md`](p40-1-startup-theme-polarity.md) records
the completed P40.1 `adapt` repair: two explicit startup-only compatibility
names preserve light/dark polarity, invalid environment/config values produce
bounded control-byte-safe typed warnings through `App.Update`, runtime
`/theme` remains unchanged, and G12 is closed.

## P41.1 fixed-size geometry owner

[`p41-1-fixed-size-geometry-owner.md`](p41-1-fixed-size-geometry-owner.md)
records the completed P41.1 `project-native` repair: one profile-owned
fixed-box projection returns exact rows and inner/outer rectangles, expands
tabs at the aligned content origin, preserves whole graphemes, emits padding
and borders itself, binds selection/modal placement to the same rows, removes
the two Lip Gloss fixed-size source exceptions, and closes G25.

## P41.2 bounded Markdown renderer pool

[`p41-2-bounded-markdown-renderer-pool.md`](p41-2-bounded-markdown-renderer-pool.md)
records the completed P41.2 `project-native` repair: one App-owned capacity-32
strict-LRU pool, atomic renderer/mutex entries, retained-pointer in-flight
eviction safety, private compatibility pools, exact-key preservation, and G26
closure.

## G24 Plan confirmation input isolation

[`g24-plan-confirmation-input-isolation.md`](g24-plan-confirmation-input-isolation.md)
records the completed G24.1 `preserve` repair: confirmation-first keyboard and
pointer routing, current-frame-only No/Yes hitboxes, stale geometry
invalidation across No/Esc re-entry and resize, retained presentation state,
and exactly-once typed bypass settlement.

## G27 result-bound command recency

[`g27-result-bound-command-recency.md`](g27-result-bound-command-recency.md)
records the completed G27.1 `preserve` repair: App-owned palette provenance,
exact engine `queryID` or local submission identity, result-bound exactly-once
Recent mutation, and fail-closed invalidation for non-success, stale,
superseded, replayed, manual, or duplicate outcomes.

## P31.3 read-only Task Explorer

[`p31-3-read-only-task-explorer.md`](p31-3-read-only-task-explorer.md)
records the completed P31.3 `combine` slice: one responsive selector-backed
explorer, exact immutable execution-generation entry fences, compatibility-only
local task labelling, shared activity/sidebar facts, responsive/display-cell
proof, viewport-bounded cost, PTY lifecycle, and rollback without runtime
authority changes.

## P47.1 exact pending action intent

[`p47-1-task-explorer-action-intent.md`](p47-1-task-explorer-action-intent.md)
records the completed P47.1 `combine` repair: send, continue, and cancel
confirmation capture one immutable execution-generation and snapshot intent;
refresh cannot retarget it, stale results cannot clear a newer prompt, the
engine retains current-truth authorization, and G38 is closed.

## P47.2 WorkItem-scoped settlement

[`p47-2-workitem-scoped-settlement.md`](p47-2-workitem-scoped-settlement.md)
records the completed P47.2 `combine` repair: terminal mutation selects only
links for the exact target WorkItems on the current Board, preserves
exact-generation fail-closed settlement and durable commit semantics, leaves
TUI and ACP ownership unchanged, and closes G39.

## P47.3 exact thread navigation

[`p47-3-exact-thread-navigation.md`](p47-3-exact-thread-navigation.md)
records the completed P47.3 `combine` repair: switch declaration, application,
Ctrl+T activation, bounded paging, and async result application share one exact
Session/thread/Agent/generation/mode target; stale or reused identities fail
visibly without rebinding, generic navigation remains unchanged, and G40 is
closed.

## P47.4 mixed rows and stable selection

[`p47-4-mixed-rows.md`](p47-4-mixed-rows.md) records the completed P47.4
`project-native` presentation seam: Ctrl+T joins engine-ordered WorkItems and
exact executions in one textually distinguished list, restores selection only
by exact composite identity, falls back deterministically when a row
disappears, preserves exact execution actions, and leaves G41 open for
P47.5-P47.7 filter/focus and bounded detail.

## P47.5 filter and focus navigation

[`p47-5-filter-focus.md`](p47-5-filter-focus.md) records the completed P47.5
`project-native` presentation seam: four exact local filters compose with
search, controls/list/detail focus and descriptor-owned hints remain truthful,
render-derived mouse geometry is stale-invalidated and consumed before chat,
runtime actions remain keyboard-explicit and engine-declared, and G41 stays
open for P47.6-P47.7 bounded snapshot and lazy execution detail.

## P47.6 snapshot detail structure

[`p47-6-snapshot-detail.md`](p47-6-snapshot-detail.md) records the completed
P47.6 `combine` seam: Ctrl+T defensively caches its bounded snapshot, projects
truthful WorkItem/execution `overview` and `activity` tabs, scrolls detail
independently, fails closed on ambiguous boardless facts, performs no detail
I/O, and leaves G41 open only for P47.7 lazy exact execution detail.

## P47.7 lazy execution detail

[`p47-7-lazy-execution-detail.md`](p47-7-lazy-execution-detail.md) records the
completed P47.7 `combine` seam: execution-only transcript/output/lineage tabs
load through exact generation-bound commands, reject stale async results,
isolate reused nonterminal output, revalidate terminal reads, preserve cached
render purity and existing actions, and close G41 plus the P47 program.

## Workstreams

[`workstreams.md`](workstreams.md) preserves the structural and modernization
workstream decomposition used to reach TUI parity and the modern multi-Agent
experience.

## P19 Revontuli slices

[`g11-b-scroll-follow-pill.md`](g11-b-scroll-follow-pill.md) records the
completed `ChatView` follow/append-epoch/baseline owner, semantic jump-pill
model, hydration and restoration rules, focused/race evidence, compatibility,
and rollback boundary.

[`g11-c-display-cell-profile-kernel.md`](g11-c-display-cell-profile-kernel.md)
records the immutable profile policy and derived identity, origin-aware
cluster operations, first-visible-scalar semantic presentation rule, App
constructor injection, terminal diagnostics, compatibility adapter, focused/
race evidence, and rollback boundary.

[`g11-d1-markdown-profile-projection.md`](g11-d1-markdown-profile-projection.md)
records the App-owned immutable render environment, independent theme/geometry
generations, active/inactive/restored/future/durable-reset projection,
production Markdown/table profile selection, exact renderer/stable/full/
frozen/viewport cache identity, focused/race evidence, compatibility, and
rollback boundary.

[`g11-d2-final-frame-geometry.md`](g11-d2-final-frame-geometry.md) records the
profile-owned chat/sticky/band/column/sidebar/status/final-frame boundary,
rectangle-origin-aware tabs, exact separator columns, first-overflow
diagnostics, control-balanced width matrix, source-owner guard, compatibility,
and rollback.

[`g11-d3-shared-pill-geometry.md`](g11-d3-shared-pill-geometry.md) records the
cached semantic-model/rectangle/environment pill projection, origin-aware
centering, shared rendered row and inclusive/exclusive hit bounds,
accepted-width/glyph/control/cache/routing evidence, compatibility, and
rollback.

[`g11-e1-modal-geometry.md`](g11-e1-modal-geometry.md) records exact App
environment projection into six production modal components, one
profile-owned final-row/outer-rectangle geometry helper, final-box centering,
Plan render/hitbox sharing, head-priority overflow, keyboard-only isolation,
the Unicode/control/width matrix, compatibility, and rollback.

[`g11-e2-agent-task-geometry.md`](g11-e2-agent-task-geometry.md) records exact
App environment projection into the Agent wizard, background/detail, and Team
monitor/peek; transient centered outer rectangles; the full-screen Task Panel
profile projection; panel-specific detail/transcript boundaries; Unicode/
control/width/race/source-owner evidence; compatibility; and rollback.

[`g11-e3-content-projection-geometry.md`](g11-e3-content-projection-geometry.md)
records exact App environment projection through tool history, structured
diff, inline error, expanded/raw, welcome, and notification final rows; the
shared content-geometry owner; welcome render/hit-bound sharing; preserved
history cache, raw, and notification lifecycle semantics; Unicode/control/
width/race/source-owner evidence; compatibility; and rollback.

[`g11-e4-picker-interaction-geometry.md`](g11-e4-picker-interaction-geometry.md)
records exact App environment projection through hints, search bars,
command/model/Agent pickers, Help/bypass/rewrite rows, active-thread labels,
and chat/expanded cell-to-source selection; explicit Resume/Theme no-op
inventory; Unicode/control/width/race/source-owner evidence; compatibility;
and rollback.

[`g11-f1-geometry-owner-deletion.md`](g11-f1-geometry-owner-deletion.md)
records deletion of the zero-caller geometry helpers and table-only
compatibility names/adapters, migration of residual compatibility render rows,
the production-wide classified Go AST gate, focused/race/repository evidence,
compatibility, and rollback.

[`g11-f2-terminal-program-closeout.md`](g11-f2-terminal-program-closeout.md)
records the test/docs-only G11 program closeout: one real-program PTY lifecycle
matrix, explicit terminal/font claim boundary, frozen-history structural proof,
portable steady-frame budgets, diagnostic benchmarks, compatibility, and
rollback.

[`2026-07-25-dirty-worktree-recovery.md`](2026-07-25-dirty-worktree-recovery.md)
records the path-by-path recovery of useful pre-reset TUI/runtime work onto
current owners, including click-to-bottom, history/hints, canonical active
time, confirmed Shift+Tab bypass, narrow-dialog safety, and restored G9 design
evidence.

[`g9-c-goldmark-semantic-tables.md`](g9-c-goldmark-semantic-tables.md)
records the Goldmark AST table-grammar boundary, structured inline runs,
same-byte code-span compatibility adapter, terminal-control rejection, and
the explicit G9.D nested/streaming convergence boundary.

[`g9-d-streaming-table-convergence.md`](g9-d-streaming-table-convergence.md)
records the complete/stable/final fragment state machine, nested
blockquote/list semantic-island projection, active-tail literal behavior,
profile/completeness cache identity, fail-closed sentinel boundary, and the
explicit G9.E repair-deletion handoff.

[`g9-e-table-repair-deletion.md`](g9-e-table-repair-deletion.md) records the
final post-render repair deletion, explicit non-table terminal-variability
guard, 32/48/72-column PTY geometry evidence, G9 closeout, compatibility, and
rollback boundary.

[`p19-2-0-glyph-swap.md`](p19-2-0-glyph-swap.md) records the project-owned
system/assistant/model-status glyph mapping, the status-width correction it
required, focused/golden evidence, compatibility boundary, and rollback for
the completed P19.2.0 slice.

[`p19-2-1-spinner-pulse.md`](p19-2-1-spinner-pulse.md) records the fixed-star
960ms breathing sequence, semantic truecolor/ANSI behavior, reduced-motion
boundary, renderer coverage, compatibility boundary, and rollback for the
completed P19.2.1 slice.

[`p19-2-2-aurora-shimmer.md`](p19-2-2-aurora-shimmer.md) records the shared
2.4-second verb phase, three-stop truecolor sequence, flat ANSI and
reduced-motion paths, semantic waiting/stalled colors, preserved pulse/timer
boundary, and rollback for the completed P19.2.2 slice.

[`p19-3-0-markdown-palette.md`](p19-3-0-markdown-palette.md) records the
explicit Markdown theme identity, semantic heading/code/quote/rule mapping,
nested-cache invalidation, ANSI-16 fallback, focused/golden/performance
evidence, compatibility boundary, and rollback for the completed P19.3.0
slice.

[`p19-3-1-tool-badges.md`](p19-3-1-tool-badges.md) records the shared semantic
tool-category mapping, neutral `element` surface, unknown-tool fallback,
truecolor/ANSI goldens, width and ownership boundary, and rollback for the
completed P19.3.1 slice.

[`p19-3-2-semantic-colors.md`](p19-3-2-semantic-colors.md) records the
theme-only static color-source boundary, semantic error/mode/running mappings,
dialog and syntax-color migration, truecolor/ANSI golden evidence, preserved
runtime ownership, and rollback for the completed P19.3.2 slice.

[`p19-3-3-composer-border.md`](p19-3-3-composer-border.md) records the
next-frame default/Plan/shell/bypass border mapping, shell precedence,
truecolor/ANSI golden and responsive-geometry evidence, unchanged layout
allocations, preserved runtime ownership, and rollback for the completed
P19.3.3 slice.

[`p19-3-4-user-message-panel.md`](p19-3-4-user-message-panel.md) records the
theme-owned user-message surface, repeated brand `▎` edge, sticky projection,
background-free ANSI fallback, wrap/raw/cache/scroll boundaries, focused and
golden evidence, and rollback for the completed P19.3.4 slice.

[`p19-3-5-welcome-wordmark.md`](p19-3-5-welcome-wordmark.md) records the
App-style-owned truecolor gradient, flat reduced-color/no-color fallback,
responsive geometry and runtime-restyling evidence, unchanged App allocations,
rollback, and final G6/G7/P19 program closeout.

## Implementation maps

The `implementation-maps/` directory retains the detailed mappings used during
M0-M7:

- [`migration/history/tui/implementation-maps/01-layout.md`](implementation-maps/01-layout.md)
- [`migration/history/tui/implementation-maps/02-messages.md`](implementation-maps/02-messages.md)
- [`migration/history/tui/implementation-maps/03-tools.md`](implementation-maps/03-tools.md)
- [`migration/history/tui/implementation-maps/04-input.md`](implementation-maps/04-input.md)
- [`migration/history/tui/implementation-maps/05-permissions.md`](implementation-maps/05-permissions.md)
- [`migration/history/tui/implementation-maps/06-spinner.md`](implementation-maps/06-spinner.md)
- [`migration/history/tui/implementation-maps/07-status.md`](implementation-maps/07-status.md)
- [`migration/history/tui/implementation-maps/08-sessions.md`](implementation-maps/08-sessions.md)
- [`migration/history/tui/implementation-maps/09-styling.md`](implementation-maps/09-styling.md)

## Note on subsystem pointer

This index replaces the old `subsystems/15-tui-subagent-runtime.md` history
pointer. That legacy path must not be read as a current subsystem owner; current
architecture and contracts are in
[`../../../architecture/tui/`](../../../architecture/tui/).

## Current replacements

For every historical area, the current replacements are:

- Runtime events, reducer, replay, selectors, and attention:
  [`architecture/tui/contracts/runtime-events.md`](../../../architecture/tui/contracts/runtime-events.md)
- Busy submission and pending input:
  [`architecture/tui/contracts/busy-queue.md`](../../../architecture/tui/contracts/busy-queue.md)
- Session discovery, resume, fork, and view sidecars:
  [`architecture/tui/contracts/sessions.md`](../../../architecture/tui/contracts/sessions.md)
- TUI architecture, state ownership, data flow, permissions, and rendering:
  [`architecture/tui/README.md`](../../../architecture/tui/README.md)
- Underlying task/agent lifecycle and engine runner ownership:
  [`architecture/runtime/tasks-and-agents.md`](../../../architecture/runtime/tasks-and-agents.md)
- Durable transcript and checkpoint ownership:
  [`architecture/state/transcripts.md`](../../../architecture/state/transcripts.md)
- Session storage and catalog ownership:
  [`architecture/state/sessions.md`](../../../architecture/state/sessions.md)

Historical numbers and milestones are closeout snapshots only; they do not
reflect current status or backlog.
