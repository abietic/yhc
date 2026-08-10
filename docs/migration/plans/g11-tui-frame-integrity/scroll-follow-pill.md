# G11 Scroll Follow And Jump Pill

**Status:** historical
**Slice state:** complete
**Last verified:** 2026-07-26

> **Ownership:** G11.B `ChatView` follow-state transitions, unseen-baseline
> semantics, pill presentation model, hitbox contract, tests, and rollback

## Problem

Before G11.B, the pill used `scrollAwayCount > 0` both as an unseen-message
baseline and as proof that the user was away from the bottom. `ScrollToTop`
and equivalent restoration paths could set `follow=false` while leaving the
count zero. The renderer and App hitbox then both suppressed the only direct
jump action.

This is a presentation-state bug. Engine transcript, Agent/task runtime state,
and durable replay already provide the content; they must not own viewport
follow state.

## Implemented Result

G11.B replaces `follow` plus the item-count sentinel with one
`chatFollowState` value owned by `ChatView`. It records a saturating monotonic
live append epoch, snapshots the first effective departure once, and keeps
baseline validity independent from pill visibility. Empty, non-scrollable,
zero-height, zero/negative-distance, and invalid-target operations remain
following.

The exported `AppendHistoryItem` is explicitly live; projection code uses an
internal hydration entry, while legacy message-family reconstruction runs
under an explicit hydration intent. Tool grouping, mutation, finalize,
expansion, rendering, reset, and truncation never advance the epoch. Durable
and Agent projection restoration preserve away intent with an invalid
baseline, which renders `Jump to bottom` rather than hiding the action.

One `ChatView` pill model now publishes visibility, label, and follow action.
G11.D3 converts that model plus the chat rectangle and exact render environment
into one cached profile-owned styled run, final row, inclusive start/exclusive
end cells, and action. Rendering places the published run and `App` invokes
the same result's hit test.

## Target State Model

`ChatView` remains the only owner. The implementation may use an equivalent
representation, but it must distinguish:

| Fact | Meaning | Required transitions |
|---|---|---|
| `following` | viewport tracks the bottom screenful | reset/clear, explicit bottom, or downward clamp to exact follow offset |
| `appendEpoch` | monotonic presentation sequence for countable user-visible append events | advance on a real append; do not derive from current projection length |
| `unseenBaselineEpoch` | append epoch when the user first left follow | set once on follow→away; preserve while away; clear on follow |
| `baselineValid` | whether unseen count can be computed | independent of pill visibility |
| `pillVisible` | nonempty chat is outside follow | derived from follow state, never from baseline validity |

The existing fields may evolve into a small value object, but no parallel
boolean/count owner may remain. Tool grouping, expansion, theme/render changes,
projection replacement, and other operations that change `len(items)` without
appending a countable user-visible event do not advance `appendEpoch`. G11.A
freezes which ChatItem append families are countable; G11.B must not let
renderer implementation details decide that policy implicitly.

### G11.A append classification

A live top-level history append advances `appendEpoch` exactly once:

- a new user, assistant, thinking, tool, system, compact-boundary,
  interruption, compact-summary, or help item; and
- a new semantic `HistoryItem` delivered as live history.

Mutation of an existing assistant/thinking/tool item, finish/finalize,
grouping or ungrouping, expansion, theme/style/profile re-render, truncation,
reset, and cache replacement do not advance it. Transcript/session/Agent
projection hydration is not a live append; restoring away intent invalidates
the baseline and exposes a count-free jump action. The exported
`AppendHistoryItem` is the explicit live path; the internal hydration path is
named separately. Item type and `len(items)` never decide.

The G11.A current-state fixture also records that the old implementation does
not satisfy this classification: every length increase changes the derived
count, grouping can reduce it, truncation can retain a stale sentinel, and
durable restored-away state has no sentinel. See
[`g11-a-frame-integrity-characterization.md`](../../verification/g11-a-frame-integrity-characterization.md).

## Transition Contract

| Input | Follow result | Baseline result | Pill |
|---|---|---|---|
| first effective line/page/wheel up from bottom | away | snapshot current append epoch | `Jump to bottom` |
| effective `ScrollToTop` on scrollable content | away | snapshot if not already away | visible |
| item/search/thread jump | away | snapshot or preserve restored valid baseline | visible |
| countable user-visible append while away | away | unchanged; append epoch advances | positive unseen count |
| tool collapse/expand or style-only projection | unchanged | unchanged; never inferred from item length | unchanged |
| thread/Agent projection replacement preserving away intent | away | invalid | `Jump to bottom` |
| resize/theme/render | unchanged | unchanged | geometry recomputed only |
| downward scroll before exact follow offset | away | unchanged | visible |
| exact follow offset, explicit bottom, or pill click | following | cleared | hidden |
| truncate/reset/clear/new projection with follow intent | following | cleared | hidden |
| restored away projection with no valid baseline | away | invalid | `Jump to bottom` |
| empty or non-scrollable no-op scroll | unchanged | unchanged | unchanged |

Unseen count is `max(0, currentAppendEpoch-baselineAppendEpoch)` only when the
baseline is valid. Invalid or zero unseen count renders `Jump to bottom`; it
never hides the action.

## Shared Presentation Model

One helper owned by `ChatView` produces the semantic pill model:

```text
visible
label: Jump to bottom | 1 new message | N new messages
action: reset follow
```

One G11.D3 geometry helper converts that model plus the chat rectangle and
exact render-environment identity into:

```text
rendered run
start cell
end cell
row
```

`ChatView.Render` and `App.pillClickHits` consume the same cached result. App
routes the click but does not reconstruct count, label, style width, or
centered columns. Centering expands tabs from the published start cell.

## Invariants

1. `ChatView` is the sole follow/unseen owner.
2. Pill visibility depends only on nonempty-away state and overlay ownership.
3. Count cannot become negative after truncation, thread replacement, or
   restored projection.
4. First departure snapshots once; subsequent scrolls do not move the unseen
   boundary.
5. Projection length is not an unseen-message fact. Grouping, collapsing,
   expanding, or replacing items cannot create a false unseen count.
6. Entering follow through truncate/reset/clear also clears baseline validity;
   `follow=true` with a stale away baseline is invalid.
7. Render, hitbox, and accessibility label use one presentation model.
8. Sticky-header row allocation does not move the pill outside the published
   chat rectangle.
9. Modal, expand, sidebar routing, and Agent-detail ownership resolve before
   the background pill hitbox.
10. No engine event, transcript record, or persisted schema is introduced.
    Durable restore of `follow=false` therefore starts with an invalid baseline
    and a visible count-free jump action.

## G11.B Completed Tasks

1. Replace the sentinel interpretation of `scrollAwayCount` with one
   presentation-only state value containing follow, current append epoch,
   baseline append epoch, and explicit baseline validity.
2. Centralize follow→away and away→follow transitions; route `ScrollUp`,
   `ScrollToTop`, `ScrollToItem`, search jumps, and view-state restoration
   through them.
3. Route append, tool grouping/collapse, truncation, reset, clear, and thread/
   Agent projection replacement through explicit epoch-preserve, invalidate, or
   clear transitions. Do not use `len(items)` as the durable unseen fact.
4. Treat zero/negative scroll distances, invalid item targets, zero-height
   rectangles, empty content, and non-scrollable content as no-op transitions
   unless an explicit navigation action establishes a real away position.
5. Add the semantic pill presentation helper and remove duplicated label/count
   selection from `App`.
6. Keep geometry behind the then-current width adapter until G11.D3; do not
   introduce a third width method in G11.B.
7. Add transition-table tests and render/hitbox tests.
8. Update the current responsive-layout contract and tracker state.

## Acceptance Tests

- first line up, page up, wheel up, and top jump from follow;
- repeated scrolling while away preserves the original baseline;
- zero, one, and multiple unseen messages;
- grouping/collapse/expand does not change unseen count;
- truncation, reset, clear, and thread/Agent projection replacement clear or
  explicitly invalidate the baseline and cannot leave `follow=true` with stale
  away state;
- empty, non-scrollable, zero-distance, invalid-target, and zero-height actions
  do not manufacture away state;
- search/item jump and restored away state with invalid baseline still show
  `Jump to bottom`;
- exact follow offset, explicit bottom, and primary pill click restore follow;
- click outside the published row/columns misses;
- overlay/expand/sidebar routing does not leak clicks;
- 40/80/120/150/180 widths preserve a reachable textual action; and
- race tests remain unnecessary unless the implementation introduces
  cross-goroutine mutation, which this contract rejects.

## Completed G11.B And G11.D3 Result

G11.B closed after all follow transitions consumed one state helper and App
stopped reconstructing pill semantics. G11.D2 then closed the final-frame
profile boundary, and G11.D3 completed the shared profile-owned pill geometry.
Zero/one/many labels, accepted widths, the glyph/tab/control matrix, exact
click boundaries, sticky headers, cache invalidation, and existing routing
precedence are verified. G11.E1-G11.F2 have also completed, and G11 has left
the live queue. G11.C depended independently on G11.A's geometry evidence;
G11.B did not select or block the display-cell kernel.

## Rollback

Revert the state helper, transition routing, and shared pill model together.
The rollback target is the pre-G11 `ChatView` fields and tests. Do not retain
both new and legacy baseline interpretation or add an engine-owned fallback.

## Source Boundaries

| Boundary | Current source | Delivered behavior |
|---|---|---|
| line/page/wheel departure | [`ChatView.ScrollUp`](../../../../internal/tui/chat.go) | centralized leave-follow transition |
| top departure | [`ChatView.ScrollToTop`](../../../../internal/tui/chat.go) | same transition owner |
| item/search jump | [`ChatView.ScrollToItem`](../../../../internal/tui/chat.go) | same transition owner |
| downward clamp | [`ChatView.ScrollDown`](../../../../internal/tui/chat.go) | centralized restore-follow transition |
| projection truncation | [`ChatView.TruncateFrom`](../../../../internal/tui/chat.go) | clears follow baseline atomically |
| durable view restore | [`resetAndRestoreSessionViews`](../../../../internal/tui/session_view_state.go) | restored away state has an explicit invalid baseline |
| pill rendering | [`ChatView.Render`](../../../../internal/tui/chat.go) | consumes the shared cached profile-cell row/run |
| hit testing | [`App.pillClickHits`](../../../../internal/tui/app.go) | invokes the same geometry result's inclusive/exclusive hit test |
