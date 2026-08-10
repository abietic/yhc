# P20.R2 Plan Feedback Cursor

**Status:** historical
**Completed:** 2026-07-26
**Last verified:** 2026-07-26

> **Ownership:** completed delivery evidence for the visible Plan feedback
> cursor across color and no-color terminal modes. Current behavior belongs in
> [`editing.md`](../../../architecture/tui/contracts/editing.md) and
> [`accessibility.md`](../../../architecture/tui/contracts/accessibility.md).

## Outcome

P20.R2 completed the presentation half of the reopened G10 repair under the
existing `combine` decision. A focused Plan feedback editor now exposes one
unambiguous caret at empty input, text start, text middle, and end of line in
every supported terminal color mode without moving feedback or runtime state
into the renderer.

| Boundary | Delivered behavior |
|---|---|
| Color profiles | `DialogInputCursor` stores foreground/background in the pre-reversal form expected by Bubbles. The effective terminal cursor cell therefore uses the semantic brand background and input-surface foreground under Polar Night, Daybreak, Snowy, Aubergine, ANSI-256, and ANSI-16. |
| No color | `App` derives `ColorNone` from terminal capabilities and configures `PlanDialog` before final-frame ANSI stripping. A temporary textarea clone reserves one column and inserts one literal `▏` at the logical cursor, keeping the current character visible. |
| Focus and motion | Blur and blink-hidden frames omit the caret. Reduced motion keeps the same caret statically visible. Restoring visibility returns to the same logical cell. |
| State and geometry | Rendering does not mutate draft bytes, rune cursor, undo stack, focus, viewport, resize/theme state, external-editor snapshot, or runtime ownership. Compact, standard, wide, and tall layouts remain bounded. |

No startup-theme alias, G12 resolution behavior, Plan approval decision,
QueryEngine lifecycle, permission rule, persistence schema, or external-editor
contract changed.

## Acceptance Evidence

Final-cell terminal emulation decodes the actual rendered cell grid rather
than asserting ANSI presence. It covers all supported color profiles plus
no-color at empty/start/middle/end positions and verifies the effective
foreground/background after reverse-video semantics.

The deterministic matrix also proves:

- render-only no-color projection and unchanged authoritative textarea state;
- focused/blurred, blink-visible/hidden, and reduced-motion behavior;
- compact `40x12`, standard `80x24`, wide `132x30`, and tall `80x40`
  geometry across resize and live theme changes;
- exact valid UTF-8 state for CJK, combining, Indic, and ZWJ feedback;
- normalized golden output; and
- a real Unix PTY capture of final color and no-color App frames.

Focused normal and race tests, the complete repository gates, and an
independent accessibility/theme review passed:

```text
go test ./internal/tui -run 'TestP20R2|TestP202' -count=1
go test -race ./internal/tui -run 'TestP20R2|TestP202' -count=1
go test ./internal/tui -count=1
make fmt
make lint
make test
make build
make lint-new
make docs-check
go run ./scripts/migration_manifest.go check
git diff --check
```

## Compatibility And Rollback

The repair is presentation-only. Submitted feedback bytes, typed Plan
outcomes, engine settlement, entrypoint behavior, and persistent state remain
compatible.

Rollback removes the capability-selected textual projection and restores the
previous cursor style as one TUI change. It must not change feedback data or
Plan approval semantics. Because that rollback reintroduces an invisible
empty/end-of-line caret, it is acceptable only as an explicit G10 regression,
not as a silent fallback.

## Current Replacement

This record owns only P20.R2 delivery evidence. The consolidated corrected
matrix and G10 closure are recorded in
[`p20-r3-plan-interaction-closeout.md`](p20-r3-plan-interaction-closeout.md).
