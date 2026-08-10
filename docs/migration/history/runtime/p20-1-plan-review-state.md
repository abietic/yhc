# P20.1 Explicit Plan Review, Action, And Viewport State

**Status:** historical
**Completed:** 2026-07-25
**Last verified:** 2026-07-25

> **Ownership:** delivery boundary, adoption, compatibility, rollback, and
> verification evidence for Plan-dialog presentation state

## Outcome

The TUI Plan approval path now has one explicit presentation owner.
`PlanDialog` starts each request in Review and represents Review, Actions, and
Feedback as mutually exclusive focus states. The Plan body renders inside one
bounded rendered-line viewport; sticky actions and help remain outside it.

Input has deterministic meaning:

- Up/Down and Home/End target the active Review or Actions region;
- PageUp/PageDown always page the Plan, including while Feedback is active;
- wheel events scroll only inside the published Review rectangle;
- a primary Review click focuses Review; and
- a primary action click focuses and selects without submitting.

`App` retains the modal pointer boundary, so Plan input cannot reach the chat
underneath.

## Actions And Responsive Geometry

Actions are rebuilt from the request's exact `ReturnMode`. The previous mode
is first; AcceptEdits and Bypass appear only when distinct; typed Revise
remains last. Selecting an explicit bypass action retains P20.0 confirmation
semantics.

Compact 40x12 rendering drops subtitle and editor/path chrome before it can
hide a nonempty Review viewport, every sticky action, or the focus footer.
Standard, wide, and tall layouts retain the editor/path footer. All lines are
display-width bounded. Theme changes preserve focus, selection, and offset.
Resize preserves the same state and clamps offset only after the new rendered
height is known.

Visible text is project-owned and no longer names Claude.

## Adoption And Compatibility

P20.1 is a `combine` decision:

- `preserve` Bubble Tea, the App modal/input owner, P20.0 request/revision/
  reviewed-digest semantics, response-channel settlement, Ctrl+G command, and
  the existing feedback submission behavior;
- `adapt` Claude Code Ripe's separation of scrollable review and sticky
  actions;
- `adapt` Grok Build's explicit presentation focus; and
- use `project-native` rendered-line viewport bounds, coordinate hitboxes,
  action de-duplication, and compact layout.

There is no engine/runtime/persistence, Graph, Eino/Eino-ext, dependency, ACP,
plain, worktree, terminal lifecycle, or multiline feedback-editor change.
P20.2 and P20.3 retain those editor and terminal outcomes.

## Verification

Focused tests cover all return modes, unique action targets, Review/Actions/
Feedback key routing, page/line/Home/End behavior, coordinate-scoped wheel and
click handling, App-level pointer non-leakage, compact/standard/wide/tall
layouts in all focus states, theme and bidirectional resize preservation, and
reviewed no-color golden states. The full TUI package and scoped race tests
pass.

Repository-wide format, lint, test, build, new-lint, documentation, manifest,
and diff gates passed before merge.

## Rollback

Revert the explicit focus/viewport/geometry structs, dynamic action builder,
responsive render, focused tests/golden, and documentation as one unit. The
P20.0 typed approval and exact reviewed-byte contract remain valid and require
no schema or recovery rollback.
