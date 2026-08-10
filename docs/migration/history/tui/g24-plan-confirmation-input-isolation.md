# G24 Plan Confirmation Input Isolation

**Status:** historical
**Closed gaps:** G24
**Completed:** 2026-07-28
**Decision:** `preserve`

> **Ownership:** final G24.1 delivery evidence for Plan-dialog
> confirmation-first keyboard and pointer routing, current-frame-only No/Yes
> geometry, verification, compatibility, and rollback.

Current behavior belongs in
[`architecture/tui/README.md`](../../../architecture/tui/README.md). The
accepted historical contract remains in
[`g24-plan-confirmation-input-isolation.md`](../../plans/g24-plan-confirmation-input-isolation.md),
and the original finding remains time-scoped in the
[`recent-delivery-remediation-audit.md`](../../reference/runtime/recent-delivery-remediation-audit.md#repair-contract-g24-confirmation-input-isolation).

## Outcome

G24.1 closed the TUI presentation-state defect where PageUp/PageDown ran before
BypassConfirmation routing and mouse input could consume geometry from the
underlying Plan review, actions, or feedback editor.

`PlanDialog` remains the sole presentation owner. When bypass confirmation
owns focus:

- key routing runs before page, review, action, feedback, editor, and generic
  branches;
- Up/Down, `k`/`j`, Tab, and Shift+Tab change only the visible No/Yes
  selection;
- `Esc`, Enter on No, and a primary click on No return to Actions without a
  Plan result or permission response;
- Enter or a primary click on a current-frame Yes hitbox emits the existing
  typed confirmed `ModeBypassPermissions` result exactly once; and
- every other key, wheel event, pointer motion/release, and click is a no-op.

The frame publishes only its outer rectangle and two dedicated No/Yes
hitboxes. Review, action, and feedback rectangles are cleared. Entering a new
confirmation invalidates an earlier confirmation's hitboxes before the next
render, so No/Esc followed by re-entry cannot reuse an old Yes coordinate.
Invalid and clipped frames publish no actionable confirmation geometry.

## Scope And Compatibility

Production code changed only
[`plan_dialog.go`](../../../../internal/tui/plan_dialog.go). Focused state and
geometry proof changed only
[`plan_dialog_state_test.go`](../../../../internal/tui/plan_dialog_state_test.go).

The slice preserves P20's two-step typed bypass decision, default No, safe
No/Esc return, review offset, selected action, feedback draft/cursor/undo,
`ForceClose`, and exactly-once response owner. It changes compatibility only
for unintended input while confirmation is visible. QueryEngine authorization,
permission modes, reviewed bytes, persistence, replay, transcript, terminal
lifecycle, plain, headless, ACP, and standalone MCP did not change.

## Verification

| Boundary | Evidence |
|---|---|
| Keyboard isolation | `TestG241BypassConfirmationOwnsKeyboard` covers page, home/end, editor/text keys, all six No/Yes navigation keys, No, Esc, explicit Yes, retained presentation state, and exactly-once response. |
| Pointer and frame identity | `TestG241BypassConfirmationPublishesOnlyConfirmationHitboxes` covers pre-render review/action/feedback geometry, compact and standard frames, wheel/motion/release, non-overlapping stale rows, No/Yes clicks, No/Esc re-entry before render, and exactly-once response. |
| Invalid and resized geometry | `TestG241BypassConfirmationClipsAndClearsGeometryOnResize` proves invalid and too-short frames clear or clip No/Yes hitboxes and reject stale coordinates. |
| Compatibility | Retained P20.R1/P20.R3 confirmation tests and `TestP203PlanEditorRoundTripPTY` remain green. |
| Concurrency | The focused G24/P20 matrix passes under `go test -race`; no goroutine or shared runtime owner was added. |
| Repository | `make fmt`, `make lint`, `make test`, `make build`, `make lint-new`, `make docs-check`, `make docs-check-ci`, manifest validation, and `git diff --check` pass on the final source. |
| Review | The isolated Kimi patch passed its focused validation; Codex reviewed and extended it. Independent Terra review found and then verified closure of the cross-confirmation stale-Yes lifecycle window. |

## Rollback

One squash revert restores the earlier Plan-dialog presentation routing. There
is no schema, durable state, transcript, protocol, or cross-version rollback.
Reverting would reopen G24 but would not change the completed P20 typed
authorization path.
