# P19.2.0 Revontuli Glyph Swap

**Status:** historical
**Completed:** 2026-07-24

> **Ownership:** delivery and closeout evidence for the P19.2.0 system,
> assistant, and model-status identity glyph replacement

## Outcome

P19.2.0 replaced the remaining borrowed chat and model-status identity marks
with the project-owned Revontuli mapping:

| Voice or surface | Delivered glyph | Semantic owner |
|---|---:|---|
| System and empty-help messages | `✧` | `SystemMessage` |
| Finalized and compatibility-streaming assistant messages | `✦` | `AssistantPrefix` |
| Status model identity | `✦` | `AssistantPrefix` |

The two weights are centralized as package constants. User-message,
tool-status, and spinner glyphs were not changed; the spinner remains owned by
the next P19.2.1 slice.

## Layout Correction

The filled Dingbats star exposed a disagreement between Lipgloss width and the
TUI's conservative emoji-aware terminal width. Status padding now measures both
segments with `emojiAwareWidth`; crowded and left-only fallback truncation
reduces its ANSI-aware bound until the rendered segment also fits that
measurement. This prefers a possible one-column under-fill on narrow-rendering
terminals over overflow on terminals that render the star as two columns.

## Evidence

- Focused renderer tests cover the exact outline/filled mapping, finalized and
  streaming assistant prefixes, semantic ANSI brand styling, continuation
  indentation, removal of the status hexagon, and bounded status width.
- App-layout, product-state, and Eno-welcome goldens record the normalized
  chat and status text.
- The complete `internal/tui` package and the repository formatting, lint,
  test, build, migration-manifest, documentation, and whitespace gates passed
  after the final source and documentation changes.

## Compatibility And Rollback

This `project-native` change deliberately replaces borrowed visual identity;
it does not change messages, runtime state, event ordering, timers, or terminal
lifecycle. Rollback is limited to the two identity constants and the
conservative status-width measurement.

Current ownership and remaining work live in
[`architecture/tui/README.md`](../../../architecture/tui/README.md),
[`migration/STATUS.md`](../../STATUS.md), and
[`migration/PLAN.md`](../../PLAN.md).
