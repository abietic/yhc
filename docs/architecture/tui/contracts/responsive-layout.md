# Responsive Layout Contract

**Status:** current

**Last verified:** 2026-08-07

**Ownership:** [`calculateLayout`](../../../../internal/tui/layout.go) owns
base geometry; `App.View` owns composition; the dialog stack owns overlays.

## Modes

[`responsiveLayoutDimensions`](../../../../internal/tui/layout.go) selects:

| Mode | Condition | Result |
|---|---|---|
| `compact` | width below 80 or height below 24 | Main column only; hint band capped at seven rows; activity capped at two rows |
| `standard` | otherwise, without an eligible sidebar | Full-width main column |
| `wide` | at least 150x24, sidebar data exists, and main remains at least 100 columns | Main plus 32-42 column sidebar |

Below 40x12, `App.View` renders the window-too-small surface rather than normal
layout ([`App.View`](../../../../internal/tui/app.go)). At exactly 80x24,
the layout is standard unless the independent wide condition is met.

## Geometry

One layout pass partitions non-overlapping header, chat, activity, hint,
editor, status, sidebar, and full-screen overlay rectangles. The priority is:

1. status and visible editor;
2. at least three chat rows;
3. bounded activity;
4. hints when at least three rows fit.

The overlay rectangle always covers the full terminal. Base columns are
composed first; the active dialog is rendered last
([`App.View`](../../../../internal/tui/app.go)).

Main and sidebar strings are display-width truncated/padded and joined row by
row by [`joinLayoutColumns`](../../../../internal/tui/layout.go). Editor
wrapping, chat width, selection, and mouse geometry use main-column dimensions.

One immutable render environment carries the exact App-selected display-cell
profile through Markdown/table rendering, every chat row, sticky prompts,
vertical bands, main/sidebar fitting, wide-sidebar truncation, status-hook
alignment, and final-frame clipping. Rectangle-origin-aware operations receive
the owning X column, so tab expansion and padding use the same grid that draws
the row. Theme and real terminal-size changes advance independent generations,
and frozen-item/viewport caches require the exact environment and width before
reuse.

[`finalizeFrameGeometry`](../../../../internal/tui/app.go) closes SGR/OSC
state on each physical row and clips the complete composed frame to the
terminal width. Its package-private development/test diagnostic records the
selected profile policy and the first pre-clip overflow row; `App.View` remains
side-effect-free and exposes no persistent diagnostic chrome.

## Scroll Follow Boundary

Downward chat scrolling clamps at the bottom screenful computed by
[`ChatView.followOffset`](../../../../internal/tui/chat.go). Reaching that exact
offset immediately restores follow mode and clears the scroll-away snapshot;
the renderer must not expose a top-padded intermediate frame before returning
to the live bottom view. The regression contract is covered by
[`TestChatViewScrollDownClampsAtFollowOffset`](../../../../internal/tui/chat_scroll_test.go).

`ChatView` owns one follow/append-epoch/baseline state. The first effective
line/page/wheel/top/item departure snapshots the live append epoch once.
Mutating or grouping existing items, rendering, theme changes, reset,
truncation, and hydration do not create live unseen events. Durable or Agent
projection restore preserves away intent with an invalid baseline.

Every nonempty away state publishes one semantic pill model: `Jump to bottom`
for an invalid or zero baseline delta, otherwise `1 new message` or
`N new messages`, always with the follow action. The renderer places it on the
final chat row and `App` consumes the same cached profile-cell result for
primary-click hit testing. That result publishes one styled run, chat-relative
row, inclusive start/exclusive end cells, follow action, and exact
render-environment identity. Tabs expand from the selected centered origin,
and resize/theme/profile changes recompute only presentation. Modal, sidebar,
and full-screen expand ownership is resolved before that hitbox, so clicks
cannot leak through an overlay.

## Compact and Wide Behavior

Compact mode retains at most seven autocomplete/history rows. While
autocomplete is active, candidates precede queued-input previews inside that
bounded band so clipping cannot hide the selected item. Keyboard submission,
autocomplete selection, command parsing, Agent detail, command palette, and
thread switching remain available.

The `/team` monitor applies its own bounded dialog adaptation inside those base
modes. Below a 64-column dialog it uses one textual row per child and keeps
`Tab peek`, `Enter switch`, and close visible. Standard and wide dialogs add a
second bounded activity/outcome row. The peek reserves fixed metadata,
transcript, separator, and help regions, so an asynchronous transcript page
cannot move its `Enter switch`/return controls. All variants cap the dialog at
the viewport and use display-cell truncation.

The Plan dialog has its own bounded geometry inside the full-screen overlay.
At 40x12 it drops the descriptive subtitle and editor/path line before it can
hide the nonempty Review viewport, every sticky action, or the focus footer.
At 22 rows and above it restores descriptive chrome plus the editor/path
footer. Review and action rectangles are published from the exact rendered
rows; wheel events outside Review cannot change its viewport. Resize preserves
focus, action selection, and offset and clamps the latter only after the new
rendered-line height is known. Feedback uses a bordered one-row textarea in
compact layout, three rows in standard/wide layout, and five rows in tall
layout. Compact Feedback drops the top review separator before any action,
editor row, or effective-key footer can be hidden.

Plan, permission, MCP approval/settings, resume, and question receive the
exact App-selected render environment at construction, real resize, and theme
change. Their shared package-private modal geometry owner projects every final
row through that profile and publishes one outer rectangle. Plan and question
remain top-origin; permission bottom-aligns when it fits; Resume and MCP center
the complete final box after border and padding are rendered. Tabs are
measured from each candidate start cell. If a modal is taller than the
overlay, all placements keep the first overlay-height rows and drop the tail.
Plan's feedback editor remains a Bubbles-owned editing model, but its exact
rendered rows are projected once and then used both for the frame and the
`X=3` feedback hit rectangle. Other migrated modals remain keyboard-only.

The Agent create/edit wizard, Ctrl+B background/detail, and `/team`
monitor/peek receive that same exact environment at construction, real resize,
and theme change. Each clears and replaces one transient final-outer
`modalFrameGeometry` from the same centered projection returned to App; it is
not a runtime, cache, persistence, or App-owned rectangle. Their existing
40..64, 18..80, and 28..112 dialog allocations, row budgets, Team compact
threshold, Bubbles editing, Agent controls, read-only Team restriction, and
keyboard routing remain unchanged. Ctrl+T instead retains the full-screen
`layout.overlayRect` as its only rectangle and projects its existing task
ordering, scroll window, and status row top-first through the App-selected
profile. Panel-specific detail/transcript lines use profile-owned wrapping;
generic HistoryItem semantics remain unchanged while their final projection is
owned below.

Tool history, structured diff, inline ErrorMessage, expanded/raw conversation,
welcome, and notification final rows consume the exact App-selected profile.
Production tool and error history adapters preserve the full
`HistoryRenderContext.Environment`, so resize, theme, profile, and geometry
changes invalidate finished-item presentation without changing canonical
content, semantic rich/expanded/raw/transcript dispatch, parsers, line budgets,
status, selection, or raw control stripping.

The full-screen expanded/raw view projects highlighted rows and its status line
after semantic rendering while retaining its existing 120-column conversation
render cap, scrolling, selection, and search behavior. Welcome keeps its
compact, condensed-mascot, and full-bordered tiers, row budgets, text, and
lifecycle; rendering and mascot hit testing consume the same profile-projected
rows. Notification projection calls `Active()` once, retains TTL, eviction,
severity, newest-item, suffix, and fallback semantics, and changes only final
EGC-safe truncation, origin-aware tabs, and control balance. Picker/autocomplete
geometry and residual string-to-click-column helpers remain the G11.E4
boundary.

The wide sidebar appears only when
[`wideSidebarVisible`](../../../../internal/tui/responsive_sidebar.go) sees
canonical task/Agent rows. It consumes the same `TaskAgentSnapshot` used by
other task surfaces. It shows bounded textual status and summary only; detail,
transcript, controls, and runtime ownership remain elsewhere.

## Steady-frame performance

`ChatView.Render` remains O(viewport), not O(history). Follow-offset discovery
walks backward only until the visible row budget is filled; exact-width,
exact-environment completed items retain frozen render entries. A dirty steady
frame may assemble visible rows, but it may neither re-segment a completed
item nor render the full transcript.

The closeout fixture warms 10,000 native finished history items, proves only a
viewport-bounded subset enters `renderCache`, then dirties the frame and
requires the aggregate semantic render count to remain unchanged. Its portable
p95 budget is 20 ms. A separate wide-layout fixture projects 100 live sidebar
rows under a 50 ms p95 budget. Machine-specific ns/op and allocation values are
diagnostic baselines, not portable gates; they live in
[`tui-performance-baseline.md`](../../../migration/verification/tui-performance-baseline.md).

## Status diagnostics

The status band calls the bounded engine usage projection rather than loading a
full diagnostic snapshot on every frame. It displays context only when the
latest provider input usage and an authoritative model context window are both
known. The spinner displays cumulative provider-reported input/output usage
when non-zero. An automatic-compaction marker does not reuse the summarizer
request's usage as post-compact context or infer tokens freed from message
counts; current context remains unavailable until the next main-loop response.
Message-size token estimates, generic provider prices, and the former
price-derived warning dialog are absent; detailed state/source/freshness remains
available through `/status`, `/context`, and `/usage`.

## Invariants

1. Base rectangles do not overlap and fit the selected terminal size.
2. Modal/overlay ownership covers both columns.
3. Compact omission never removes the only route to an action.
4. Wide mode never leaves fewer than 100 columns for the main workflow.
5. Display-cell width, not byte length, governs final composition.
6. The status band does not turn missing provider usage or billing metadata
   into token or money facts.
7. A steady dirty frame does not re-segment frozen history or render all
   history items.
8. PTY byte/lifecycle capture cannot be promoted into a terminal/font
   physical-grid claim.

## Evidence

The focused matrix covers widths 40, 80, 120, and 180 with heights 20, 30,
and 50 in [`TestResponsiveLayoutSizeMatrix`](../../../../internal/tui/responsive_layout_test.go).
Rectangle and width invariants are covered in
[`TestLayoutRectanglesPartitionViewport`](../../../../internal/tui/layout_regions_test.go).
The Agent monitor matrix, fixed peek controls, no-color/reduced-motion
behavior, and PTY resize path are covered by
[`agent_monitor_test.go`](../../../../internal/tui/agent_monitor_test.go) and
[`pty_workflow_unix_test.go`](../../../../internal/tui/pty_workflow_unix_test.go).
The six-modal 40/80/120/180 profile matrix, final-outer-box centering,
top/bottom/overflow rules, environment projection, Plan render/hitbox sharing,
keyboard-only mouse isolation, Unicode/control fixtures, and AST ownership
guard are covered by
[`display_cell_g11e1_test.go`](../../../../internal/tui/display_cell_g11e1_test.go).
The Agent/task 40/80/120/180 profile matrix, exact environment/semantic
preservation, transient geometry reset, full-screen Task Panel bounds,
keyboard-only mouse isolation, focused race suite, and AST owner guard are
covered by
[`display_cell_g11e2_test.go`](../../../../internal/tui/display_cell_g11e2_test.go).
The content-projection 40/80/120/180 profile matrix spans every production tool
family, diff, inline error, expanded/raw, welcome, and notification rows across
ASCII, CJK, combining, Indic, variation-selector, ZWJ, flag, tab, ANSI, and OSC
fixtures. It also covers exact-environment cache invalidation, raw control
stripping, welcome mascot bounds, notification pruning, focused race behavior,
and the scoped Go-aware geometry-owner guard in
[`display_cell_g11e3_test.go`](../../../../internal/tui/display_cell_g11e3_test.go).
The final PTY union covers 32/40/48/72/80/120/150/180 columns. One real
alternate-screen session performs streaming resizes, theme/no-color
reprojection, primary SGR mouse pill clicks, repaint, and restoration while
retaining table, sticky-header, status, and live Agent evidence in
[`g11f2_terminal_closeout_unix_test.go`](../../../../internal/tui/g11f2_terminal_closeout_unix_test.go).
The structural 10K frozen-history proof and numeric 10K/100-row budgets are in
[`g11f2_performance_test.go`](../../../../internal/tui/g11f2_performance_test.go).
