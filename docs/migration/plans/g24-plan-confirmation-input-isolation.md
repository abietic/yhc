# G24 Plan Confirmation Input Isolation

**Status:** historical
**Created:** 2026-07-28
**Last updated:** 2026-07-28

> **Ownership:** accepted Plan-dialog confirmation-input contract, the single
> G24.1 rollback boundary, promotion evidence, and deterministic acceptance
> gates. Root [`migration/PLAN.md`](../PLAN.md) alone owns executable order and
> slice state.

The pre-fix finding and initial repair contract are frozen in
[`recent-delivery-remediation-audit.md`](../reference/runtime/recent-delivery-remediation-audit.md#repair-contract-g24-confirmation-input-isolation).
Implemented behavior remains owned by
[`architecture/tui/README.md`](../../architecture/tui/README.md), and final
delivery evidence is
[`g24-plan-confirmation-input-isolation.md`](../history/tui/g24-plan-confirmation-input-isolation.md).

G24 completed under a `preserve` decision on 2026-07-28. G24.1 preserved the
completed P20 two-step typed bypass decision and made the visible confirmation
frame the exclusive owner of keyboard and pointer input.

## User Outcome

While the Plan bypass warning is visible, the user can choose only `No` or
`Yes`. Paging, wheel, pointer, editor, and hidden action input cannot change
the underlying review state or settle another action. `No` remains the
default; bypass occurs only after an explicit visible `Yes`.

## Reproduced Problem

At the promotion baseline, the production owner was
[`PlanDialog`](https://github.com/abietic/eino-agent/blob/3a35ad0534fa7d8141b6c7a87ac98907a42a3ff1/internal/tui/plan_dialog.go#L112).
Its
[`HandleKey`](https://github.com/abietic/eino-agent/blob/3a35ad0534fa7d8141b6c7a87ac98907a42a3ff1/internal/tui/plan_dialog.go#L288)
handled PageUp and PageDown before checking
`planFocusBypassConfirmation`, so paging could mutate the hidden review
viewport while the confirmation frame was visible.

[`HandleMouse`](https://github.com/abietic/eino-agent/blob/3a35ad0534fa7d8141b6c7a87ac98907a42a3ff1/internal/tui/plan_dialog.go#L474)
had no confirmation-first branch. Wheel input could scroll the review
rectangle, while primary clicks could focus the review or feedback editor or
select an underlying action through geometry published by the Plan frame. The
render path published review geometry before
[`renderBypassConfirmation`](https://github.com/abietic/eino-agent/blob/3a35ad0534fa7d8141b6c7a87ac98907a42a3ff1/internal/tui/plan_dialog.go#L939)
and published no dedicated `No` and `Yes` hitboxes.

The retained P20 tests prove that the warning is visible, `No` is the default,
`Yes` emits a typed confirmed result, and back navigation preserves selected
presentation state:
[`TestP20R1BypassConfirmationBackPreservesPresentationAndCancelIsExplicit`](../../../internal/tui/plan_dialog_state_test.go#L136)
and
[`TestP20R3BypassConfirmationIsVisibleAndRequiresDistinctChoice`](../../../internal/tui/plan_dialog_state_test.go#L175).
They do not prove that paging, wheel, stale hitboxes, or other non-confirmation
input are inert.

This is a presentation-state correctness and safety defect. It does not bypass
QueryEngine authorization directly: the existing typed Plan decision and
settlement owner remain authoritative.

## Decision

G24 uses `preserve`:

- preserve P20's explicit first bypass action followed by a distinct visible
  risk/`No`/`Yes` confirmation;
- preserve `No` as the initial selection, `Esc` and `No` as non-terminal return
  paths, and `Yes` as the only confirmed bypass result;
- preserve all underlying review offset, action selection, feedback draft,
  cursor, undo, and result state while confirmation is active; and
- close the incomplete input/geometry isolation without importing a new modal
  framework or changing permission policy.

Compatibility changes only for previously unintended input. Page, wheel,
review, feedback, action, editor, and text input become exact no-ops while the
confirmation is visible. Primary clicks on the visible `No` and `Yes` rows
become supported and consume geometry from that same rendered frame.

## Scope And Non-Goals

G24.1 owns only:

- confirmation-first routing in `internal/tui/plan_dialog.go`;
- confirmation-only geometry published with the returned Plan frame;
- focused state, mouse, normalized-frame, race, and retained P20 PTY proof; and
- closeout updates for the TUI owner, root migration ledger, gap inventory,
  contract index, status, and history.

G24.1 does not:

- change QueryEngine Plan authorization, permission modes, response
  settlement, reviewed bytes, or Plan file identity;
- change plain, headless, ACP, or standalone MCP behavior;
- change external-editor execution, feedback editing semantics, persistence,
  replay, transcript, or session schemas;
- add a reusable dialog framework or redesign other TUI modal input; or
- reopen completed P20 delivery history.

## State Owner And Frozen Transition

`PlanDialog` remains the sole presentation-state owner. Bubble Tea's existing
single `Update` path remains the only mutation owner; the slice adds no
goroutine, queue, durable record, or second renderer.

When `focus == planFocusBypassConfirmation`, both keyboard and mouse routing
must branch before every generic review, feedback, action, editor, page, or
wheel path:

| Input | Required transition |
|---|---|
| Up, Down, `k`, `j`, Tab, Shift+Tab | Toggle only the visible `No`/`Yes` selection. |
| Enter on `No` | Return to the prior safe action state with no result or permission settlement. |
| Enter on `Yes` | Emit exactly one typed confirmed bypass result through the existing response owner. |
| Esc | Return to the prior safe action state with no result or permission settlement. |
| Primary press inside visible `No` | Same transition as Enter on `No`. |
| Primary press inside visible `Yes` | Same transition as Enter on `Yes`. |
| PageUp, PageDown, Home, End, Ctrl+G, text/editor keys, wheel, release/motion, or any other click | Exact no-op. |

The prior safe state is the existing Plan action frame. `No` and `Esc` preserve
the pre-confirmation review offset, selected action, feedback draft, cursor,
undo history, and nil terminal result.

## Geometry Contract

The frame returned while confirmation is active publishes only:

- the outer modal rectangle; and
- two explicit current-frame hitboxes, ordered `No` then `Yes`.

Review, feedback, and underlying action rectangles must be zeroed before the
frame is returned. The visible `No`/`Yes` lines and their hitboxes must be
created from the same row construction and clipped by the same final modal
projection. A hitbox retained from an earlier Plan frame or earlier size may
not activate any control.

Rendering invalid dimensions continues to clear all published geometry.
Standard and compact layouts must preserve the same two-control semantics.

## Ordering, Identity, And Failure Invariants

- Confirmation routing runs before page, feedback, action, generic focus, and
  mouse-region routing.
- The rendered confirmation selection and hit-test selection are one
  `bypassConfirmYes` value from the same `PlanDialog` generation.
- `Yes` keeps the existing `PlanApprovalApprove`,
  `ModeBypassPermissions`, and `Confirmed=true` result. G24.1 must not invent a
  second result channel or rewrite `respondPlan`.
- Once the dialog closes, later input cannot emit a duplicate response.
- `Esc`, `No`, unrelated input, invalid geometry, and stale geometry produce no
  terminal result and no permission response.
- `ForceClose`, cancellation, response-channel behavior, and terminal
  restoration remain unchanged.
- No schema, checkpoint, replay, or durable-state migration exists.

## Atomic Slice

### G24.1 Confirmation-Only Input And Geometry

**State:** completed 2026-07-28

**Production allowlist:** `internal/tui/plan_dialog.go`.

**Focused test owner:** `internal/tui/plan_dialog_state_test.go`. Existing
mouse, final-frame, or PTY test files may change only when their current owner
is required to prove the frozen contract.

**Required behavior:**

1. route confirmation keyboard and pointer input before all other Plan paths;
2. publish two current-frame confirmation hitboxes and clear every hidden
   interactive rectangle;
3. reuse the existing safe-return and typed-confirmed-result transitions; and
4. preserve all underlying presentation state for every non-`Yes` path.

The slice is one TUI PR and one squash-revert rollback boundary.

## Deterministic Acceptance

Focused tests must prove:

- PageUp, PageDown, Home, End, Ctrl+G, text keys, and unrelated keys preserve
  review offset, focus, selected action, draft, cursor, undo, and nil result;
- wheel input plus clicks on stale review, action, and feedback coordinates are
  no-ops during confirmation;
- the confirmation frame publishes no review, action, or feedback hitbox and
  publishes exactly the visible `No` and `Yes` hitboxes;
- Up/Down/Tab/Shift+Tab alter only the visible selection;
- Enter and primary clicks activate the selected or clicked visible choice;
- `Esc` and `No` restore the safe action state without settlement;
- `Yes` emits exactly one typed confirmed bypass result and one existing
  permission response;
- invalid and resized compact/standard frames cannot retain actionable stale
  geometry; and
- the existing P20 confirmation state and physical-terminal scenarios remain
  green.

Required implementation verification:

```bash
go test ./internal/tui -run 'Test(G24|P20R1BypassConfirmation|P20R3BypassConfirmation)' -count=1
go test -race ./internal/tui -run 'Test(G24|P20R1BypassConfirmation|P20R3BypassConfirmation)' -count=1
make fmt
make lint
make test
make build
make lint-new
make docs-check
make docs-check-ci
git diff --check
```

The repository manifest and migration scanners must remain fully classified.
Normalized final-frame or PTY commands owned by the existing P20 harness must
also pass when they are separate from the focused Go test command.

## Promotion Evidence

G24.1 was executable because:

- the promotion-baseline production source deterministically reproduced key
  and pointer input reaching hidden state;
- `PlanDialog` is the single state and rendering owner;
- the P20 typed result, safe-return state, and compatibility surface are
  already frozen and tested;
- the current-source remediation audit records the same bounded repair; and
- the implementation changes no permission, persistence, replay, protocol, or
  non-TUI owner.

Root [`migration/PLAN.md`](../PLAN.md) promoted no second slice.

## Rollback And Closeout

Rollback is one squash revert. There is no data, schema, transcript, replay,
or cross-version compatibility work; the last safe owner remains
`PlanDialog`.

G24.1 closed with its live queue row removed, root `PLAN.md` returned to
intake, G24 removed from `REMAINING.md`, current TUI architecture and
`STATUS.md` synchronized, and final focused, race, PTY/frame, repository,
documentation, manifest, scanner, and review evidence recorded in
[`history/tui/`](../history/tui/g24-plan-confirmation-input-isolation.md).
P20 history remains unchanged.
