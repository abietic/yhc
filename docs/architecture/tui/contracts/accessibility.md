# TUI Accessibility Contract

**Status:** current

**Last verified:** 2026-08-07

**Ownership:** terminal capability/config selects accessibility mode;
`internal/tui.App` applies final-frame and interaction behavior; renderers own
semantic text and width safety.

## No-Color

`NO_COLOR` or `TERM=dumb` selects `terminalcap.ColorNone`. Every `App.View`
return passes through [`finalizeView`](../../../../internal/tui/app.go),
which strips terminal control styling from the complete frame. OSC hyperlinks
degrade to their visible labels.

Raw history is independently control-free; it does not rely on final-frame
cleanup.

The focused Plan feedback editor receives the terminal color capability before
that cleanup. In `ColorNone`, it renders a width-reserved literal `▏` through
a temporary textarea clone, so final ANSI stripping cannot erase the only
caret distinction and the current character remains visible. This projection
does not mutate draft bytes, rune cursor, undo, focus, viewport, or runtime
state.

## Reduced Motion

Reduced motion is enabled by effective config, `YHC_REDUCED_MOTION`, or
`YHC_ACCESSIBILITY` at
[`runTUI`](../../../../cmd/yhc/cmd/root.go).

It suppresses non-essential cursor blink and visual animation updates. Runtime,
Agent, attention, permission-deadline, and background refresh polling continues
because those updates carry state rather than decoration. Elapsed time and
textual progress may still change. The Plan feedback textarea uses the same
visible caret cell statically under reduced motion while retaining text
editing and submission.

## Textual Semantics

Color and glyphs may reinforce state but cannot be the only carrier. Status,
tool/task/Agent rows, sidebar entries, waiting state, and raw/expanded mode all
include text. The generic tool renderer, for example, emits textual pending,
running, completed, or failed status
([`renderGenericHistoryHeader`](../../../../internal/tui/tool_history_renderer.go)).

## Raw History

The conversation expand view starts in expanded mode. Its `r` action toggles a
raw projection generated from canonical chat items. Raw mode is ANSI-free,
copy-friendly, searchable, and does not mutate conversation history
([`ChatView.RenderAllRaw`](../../../../internal/tui/chat.go)).

Session-transcript inspection from the resume picker is a separate immutable
projection and does not advertise the conversation raw/expanded toggle.

## Unicode and Width

Final layout composition uses the App-selected display-cell profile
([`fitLayoutColumnLine`](../../../../internal/tui/layout.go)); editor
wrapping uses grapheme-aware `uniseg` measurements
([`countVisualLines`](../../../../internal/tui/layout.go)). Acceptance covers CJK,
emoji/ZWJ, combining marks, and long unbroken content while preserving valid
UTF-8 and line bounds.

Rune ranges govern composer elements; byte bounds govern retained payloads.
Plan feedback uses the same rune-based Bubbles textarea boundary and renders
CJK, combining, and ZWJ sequences without invalid UTF-8 or frame overflow.
These units are intentionally distinct; neither composer nor Plan feedback
claims grapheme-atomic deletion.

Markdown table geometry has a narrower contract. The App-selected immutable
[`DisplayCellProfile`](../../../../internal/tui/display_cell.go) pins Unicode
17 / UAX #29 / `displaywidth v0.11`, ambiguous-width-narrow, and 7-bit
ANSI/OSC handling. Custom table layout consumes that exact profile for
measurement, wrap, truncate, padding, overflow, borders, and narrow fallback;
no Glamour post-render table repair remains.
Wrapped SGR and OSC 8 state closes before a
physical-line border and reopens only on the continuation. Every complete
top-level, stable-prefix, blockquote, list, and blockquote-list table derives
semantic inline runs from Goldmark. The active final streaming block instead
shows source-like literal rows until promotion or Finalize proves
completeness; invalid UTF-8 and C0/C1 controls are replaced and the same
profile hard-wraps each line. Only validated UTF-8 link destinations can open
OSC 8.

Renderer, stable/full fragment, frozen history-item, and viewport caches
include exact width plus the App-owned theme generation, geometry generation,
color profile, selected profile identity, and completeness where relevant.
Accessibility geometry therefore cannot reuse a stale profile or live/final
projection, including after thread restore or durable reset. Chat rows, sticky
prompts, bands, main/sidebar fitting, wide-sidebar content, status-hook output,
the complete final frame, and jump-pill render/hitbox geometry now use the same
profile. The pill publishes its final row and inclusive/exclusive cell bounds
once; rendering and primary-click routing consume that same result. Final
clipping and pill centering expand tabs from the owning rectangle origin,
never split an EGC, and close SGR/OSC state per physical row. The separately
named `terminalLayoutSafetyWidth` compatibility heuristic is deleted. A
type-aware Go AST gate spans the supported Linux amd64, Darwin amd64/arm64,
and Windows amd64 production builds and rejects unclassified direct width
selection, including chained methods, method values, and method expressions;
semantic source offsets, editor/library internals, and the single
profile-projected Lip Gloss fixed-size adapter remain explicit
owner/removal-condition exemptions.

The deterministic profile is a logical cell contract, not a claim about every
terminal/font pair. `/terminal` therefore reports `Terminal/font: not inferred`.
PTY fixtures can prove emitted bytes, resize/repaint behavior, click routing,
and terminal-mode restoration, but cannot observe glyph pixels or font
fallback. The separately labelled
[`TestG11F2PhysicalGridDiagnostic`](../../../../internal/tui/g11f2_terminal_closeout_unix_test.go)
is opt-in and opens the controlling terminal directly. It accepts a
cursor-position result only when the invocation names the terminal, terminal
version, font, and fallback; any result applies only to that observed
combination.

## Scope

This contract covers terminal-observable accessibility available in the
current Bubble Tea architecture. It does not claim screen-reader protocol
integration or terminal image rendering.

## Invariants

1. No-color output contains no styling/control escapes from the final frame.
2. Reduced motion suppresses decoration, not state delivery or deadlines.
3. Runtime lifecycle meaning remains available as text.
4. Raw history is control-free and non-mutating.
5. Final frames remain valid UTF-8 and bounded by display-cell width.
6. Focused no-color Plan feedback exposes one textual caret without changing
   the authoritative editor state.
7. Automated PTY output is never presented as terminal/font physical-grid
   evidence.

## Evidence

- no-color, reduced motion, textual status, raw, and Unicode matrix:
  [`TestNoColorFinalFrameContainsNoTerminalStyles`](../../../../internal/tui/accessibility_test.go)
- reduced-motion config merge:
  [`engine/config/accessibility_test.go`](../../../../engine/config/accessibility_test.go)
- width and wrapping:
  [`TestAppViewHonorsTerminalBounds`](../../../../internal/tui/layout_regions_test.go)
- Plan feedback final-cell color/no-color, reduced-motion, layout, and Unicode
  matrix:
  [`TestP20R2FeedbackCursorFinalCellsAcrossProfilesAndPositions`](../../../../internal/tui/plan_feedback_editor_test.go)
- render-only no-color state and real final-frame PTY evidence:
  [`TestP20R2NoColorFeedbackCaretIsRenderOnly`](../../../../internal/tui/plan_feedback_editor_test.go)
  and
  [`TestP20R2FeedbackCursorPTY`](../../../../internal/tui/plan_feedback_cursor_pty_unix_test.go)
- Markdown table display-cell profile, independent geometry, property, and
  fuzz evidence:
  [`display_cell_test.go`](../../../../internal/tui/display_cell_test.go)
- Goldmark semantic tables, code-span compatibility, terminal-control
  rejection, and narrow fallback:
  [`table_semantic_test.go`](../../../../internal/tui/table_semantic_test.go)
- streaming/final convergence, container projection, cache identity,
  sentinel fail-closed behavior, and all-theme narrow widths:
  [`g9d_streaming_table_test.go`](../../../../internal/tui/g9d_streaming_table_test.go)
- profile-owned final-frame width matrix, first-overflow diagnostics,
  origin-aware tabs, ANSI/OSC balance, no-color output, source-owner guard,
  and exact table/sidebar columns:
  [`display_cell_g11d2_test.go`](../../../../internal/tui/display_cell_g11d2_test.go)
- real-program resize/theme/no-color/mouse/restoration PTY evidence plus the
  separately labelled physical-grid diagnostic:
  [`g11f2_terminal_closeout_unix_test.go`](../../../../internal/tui/g11f2_terminal_closeout_unix_test.go)
