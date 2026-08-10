# P20.R3 Plan Interaction Closeout

**Status:** historical
**Closed gaps:** G10
**Completed:** 2026-07-26
**Last verified:** 2026-07-26

> **Ownership:** corrected final delivery evidence for the P20 Plan interaction
> program and G10 closure. Current behavior belongs in
> [`permissions.md`](../../../architecture/capabilities/permissions.md),
> [`editing.md`](../../../architecture/tui/contracts/editing.md), and
> [`terminal-lifecycle.md`](../../../architecture/tui/contracts/terminal-lifecycle.md).

## Outcome

P20.R3 closed G10 under the existing `combine` decision. QueryEngine remains
the sole Plan lifecycle, recovery, reviewed-byte, and settlement owner. TUI,
plain, and ACP remain presentation adapters that return typed intent; headless
and standalone MCP still expose no synthetic approval path.

The final audit found one real observable defect after P20.R1: `PlanDialog`
already had a `BypassConfirmation` state and two-action key handling, but
`Overlay` continued rendering the ordinary action list and its footer fell
through to Review. The required second action existed logically but was
invisible. The TUI now renders a distinct warning with explicit
“No, return to actions” and “Yes, bypass permissions” choices, defaults to No,
and emits `Confirmed=true` only after a second explicit Yes.

No engine state, Graph topology, durable schema, public protocol field,
generic permission rule, startup-theme mapping, or external-editor default
changed.

## Consolidated Acceptance Matrix

| Boundary | Final evidence |
|---|---|
| TUI intent and bypass | `TestP20R3BypassConfirmationIsVisibleAndRequiresDistinctChoice`, the P20.R1 state matrix, and `TestPermissionInteractionResultFailsClosedWithoutPlanTerminalResult` prove a visible default-No confirmation, explicit typed Approve/Revise/Cancel, and no generic-response or retained-feedback inference. |
| Plain and headless | `TestPlainPlanApprovalReturnsExactStructuredTarget`, `TestPlainPlanBypassBackAndWrongTokenLoop`, EOF/cancel coverage, and `TestHeadlessBypassCannotFabricatePlanApproval` prove exact reviewed targets, exact `BYPASS`, Back/cancel behavior, and fail-closed noninteractive execution. |
| ACP | Production-resolver and structured-target tests cover unique actions, fresh target/confirm/Back transport IDs, previous bypass mode, one absolute deadline, timeout, parent cancellation, delivery/transport failure, and zero settlement before the confirmed second round. |
| Engine settlement and recovery | `TestP200ApprovalBindsReviewedBytesAtSettlement`, typed-outcome/return-mode tests, P17.1 duplicate/wrong-owner tests, P17.2 cold/live recovery tests, and Graph HITL recovery tests cover stale bytes, request/revision/digest/target identity, exactly-once settlement, and fail-closed cold normalization. |
| External editor and terminal | `TestP203PlanEditorRoundTripPTY` performs two fake-Vim handoffs with resize, alternate-screen repaint, Review/Actions/Feedback routing, arrows, PageUp/PageDown, wheel/focus/mouse reacquisition, a visible feedback cursor, and the visible default-No bypass frame after terminal reacquisition. |
| Cursor and accessibility | P20.R2 final-cell, normalized-golden, color/no-color PTY, Unicode/layout, theme, focus/blink, and reduced-motion tests remain green on the final source. |
| Standalone MCP | `TestStandaloneMCPExcludesPlanModeTransitions` proves the surface registers no Plan transition implementation. |

Focused normal and race commands passed for `internal/tui`, `engine`,
`cmd/eino-agent/cmd`, and `server/acp`; the standalone MCP exclusion test also
passed. The real P20.3 PTY acceptance remains a normal Unix test because its
race-build counterpart explicitly skips Bubble Tea v1.3.10's
dependency-owned restore/resize race. Project-owned TUI state tests remain
race-enabled.

## Static Search Result

Production and test search found no immediate single-action bypass path.
The ACP test helper that selects `plan_bypass` locates and submits
`plan_bypass_confirm` through a second real `RequestPermission` round; matrix
tests assert the two requests and distinct transport identities. TUI
`permissionInteractionResult` consumes only an explicit `planResult` for Plan
requests and otherwise returns typed Cancel/deny. Plain requires the exact
second `BYPASS` token. No adapter derives Revise from feedback text.

## Repository Gates

The final source passed:

```text
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

The visible confirmation repair changes presentation only; submitted Plan
bytes, typed outcomes, engine settlement, recovery, and persistence remain
compatible. Rollback removes the confirmation renderer and footer as one TUI
change, but that reintroduces an invisible security-sensitive decision and is
therefore an explicit G10 regression. A safe emergency fallback is to omit the
bypass action, not restore an invisible or single-action approval.

## Current Replacement

This record owns P20.R3 delivery evidence. The compatibility-retained detailed
contract is
[`p20-plan-mode-interaction.md`](../../plans/p20-plan-mode-interaction.md).
G10 no longer appears in [`REMAINING.md`](../../REMAINING.md), and
[`PLAN.md`](../../PLAN.md) promotes G11.A.
