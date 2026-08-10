# P20.2 Shared Plan Feedback Editor

**Status:** historical
**Completed:** 2026-07-25
**Last verified:** 2026-07-25

> **Ownership:** delivery boundary, adoption, compatibility, rollback, and
> verification evidence for the completed Plan feedback-editor slice

## Outcome

Plan approval now opens a visible multiline feedback editor that behaves like
the normal composer where their contracts overlap. Both use one bounded
Bubbles textarea construction, rune-cursor snapshot/restore, and max-100 undo
mechanism, while `PlanDialog` retains its own draft and undo stack.

Feedback consumes the configured chat submit, newline, and undo actions and
renders those effective bindings. Empty or whitespace-only submit returns to
Actions without settling. Esc preserves the draft, cursor, and undo stack.
Non-key textarea messages—including paste completion and cursor ticks—remain
inside the focused modal. A nonempty submit returns typed Revise feedback while
the engine remains in Active Plan.

## Adoption And Ownership

P20.2 was a `combine` decision:

- `preserve` P20.0 typed Revise settlement and P20.1 Review/Actions/Feedback
  focus, viewport, and modal input ownership;
- `adapt` the existing project composer textarea and undo behavior instead of
  maintaining a second character editor;
- `adapt` Grok Build's reuse of full prompt-widget state and explicit feedback
  focus;
- `adapt` Codex's separation between “stay in Plan” and implementation
  authority; and
- use `project-native` semantic dialog-input styles, effective-key hints,
  independent undo state, and pending-chord reset.

[`newBoundedTextarea`](../../../../internal/tui/text_editor.go#L17) owns shared
textarea construction and
[`textEditorSnapshot`](../../../../internal/tui/text_editor.go#L12) owns cursor
state. [`PlanDialog.handleFeedbackKey`](../../../../internal/tui/plan_dialog.go#L293)
owns modal edit/submit behavior.
[`Resolver.ResetPending`](../../../../internal/tui/keybindings/resolver.go#L67)
prevents an incomplete modal chord from leaking into the next focus owner.

## Presentation And Permission Boundaries

Six semantic styles cover input surface, idle/focused border, text,
placeholder, and cursor. They propagate through truecolor, ANSI-256, ANSI-16,
and no-color rendering; reduced motion uses a static feedback cursor. Compact,
standard/wide, and tall layouts use one, three, and five editor rows
respectively without hiding actions or the effective-key footer.

The editor remains rune-based like the current Bubbles v1 composer. Tests prove
valid UTF-8 and bounded rendering for CJK, combining, and ZWJ sequences; this
slice did not claim grapheme-atomic deletion or replace the pending G9
display-cell profile.

An engine regression proves typed Revise bypasses ordinary permission denial
history and counters while retaining the exact prior `ReturnMode`
([`TestP202PlanReviseBypassesGenericDenialAccounting`](../../../../engine/query_engine_permission_test.go#L304)).
No engine production path, Graph topology, persistence schema, Eino/Eino-ext
dependency, ACP/plain adapter, worktree behavior, or Plan phase changed.

## Exclusions

P20.3 remains the sole owner of:

- `VISUAL`/`EDITOR` command resolution and editor arguments;
- stable thread/request/revision/path callback identity;
- terminal alternate-screen, paste, focus, mouse, and repaint restoration;
- exact Plan viewport restoration after editor completion; and
- the mandatory fake-Vim PTY round trip.

Plan reload preservation in unit tests established the state substrate for
P20.3; it was not treated as terminal or PTY evidence.

## Verification

Focused tests covered multiline and Unicode input, word/Home/End movement,
delete/backspace, paste, undo, empty feedback, Esc, effective custom bindings,
theme/resize/reload preservation, supported palettes, no-color, reduced
motion, compact through tall geometry, semantic contrast, pending-chord reset,
and generic-denial isolation. Full `internal/tui` and `engine` package tests
and scoped race tests passed.

Repository-wide format, lint, test, build, new-lint, documentation, manifest,
and diff gates passed before merge.

## Rollback

Revert the shared textarea helper, Plan feedback textarea/undo state, key
resolver reset, semantic input styles, focused tests/golden, and documentation
as one unit. P20.0 typed settlement and P20.1 focus/viewport state remain valid;
P20.3 must not proceed until it has another state-preserving editor boundary.

Current behavior is owned by
[`architecture/tui/contracts/editing.md`](../../../architecture/tui/contracts/editing.md),
[`architecture/tui/contracts/responsive-layout.md`](../../../architecture/tui/contracts/responsive-layout.md),
and
[`architecture/capabilities/permissions.md`](../../../architecture/capabilities/permissions.md).
