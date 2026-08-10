# P20.4 Plan Entrypoint And Recovery Closeout

**Status:** historical
**Completed:** 2026-07-25
**Last verified:** 2026-07-25

> **Ownership:** delivery boundary, adoption, compatibility, rollback, and
> verification evidence for the completed Plan supported-entrypoint and
> recovery closeout

## Superseded Completion Note

A later current-source review on master `cb36859` found that this closeout
proved typed transport and engine settlement but not the frozen interaction
contract. TUI, plain, and ACP each turn one bypass target selection into
`Confirmed=true`; TUI also infers Revise from retained feedback. The tests
cited below passed but encoded the single-action TUI/ACP behavior. This file
remains a historical delivery record and no longer proves current G10
completion. Current gaps and accepted repair order are owned by
[`REMAINING.md`](../../REMAINING.md) and the reopened
[`P20 contract`](../../plans/p20-plan-mode-interaction.md).

## Outcome

Plan approval now has one observable contract across supported conversation
entrypoints. TUI, plain, and ACP present the same exact engine-owned request,
emit a typed outcome, enqueue one targeted permission decision, and resume the
same ProjectGraph turn. Headless fails closed when interaction is required.
The standalone MCP server exposes no Plan-transition implementation because it
has no QueryEngine phase or approval owner.

The plain REPL no longer skips `EventPermissionRequest`. It resolves a live
request inside the event driver, claims the decision `RuntimeItem`, and
continues that turn. After same-process resume, it handles an existing pending
request before accepting a new user prompt.

## Adoption And Ownership

P20.4 completed the P20 `combine` decision:

- `preserve` QueryEngine Plan phase, exact file/revision/digest identity,
  ProjectGraph StatefulInterrupt checkpoint, targeted `RuntimeItem`, and cold
  AwaitingApproval normalization;
- `adapt` the existing TUI, plain, and ACP presentations to emit only
  `Approve`, `Revise`, or `Cancel`;
- use `project-native` engine settlement as the only source of executable
  authority, with a process-local capability bound to the exact tool-use ID;
  and
- `reject` interactive Plan transitions on standalone MCP and synthetic
  approval in headless mode.

[`normalizePlanApprovalDecision`](../../../../engine/plan_state.go) owns
identity, target, confirmation, exact-file, and reviewed-byte validation.
[`planApprovalAllowsExit`](../../../../engine/tool_execution.go) consumes only
the settled, request-bound capability. Adapter-authored typed data is intent,
not authority.

## Entrypoint Matrix

| Entry point | Positive path | Negative or unavailable path |
|---|---|---|
| TUI | Owner-thread Plan dialog resolves the ProjectGraph request and resumes the exact decision item. | Cancel emits typed `Cancel`, remains Active, executes no Exit, and creates no grant. |
| Plain | Live event and pending-resume drivers render exact Plan bytes, resolve, claim, and continue. | Cancel and context cancellation emit typed `Cancel`; an unclaimable boundary terminates instead of accepting another prompt. |
| Headless | No interactive positive path exists. | Bypass cannot fabricate approval; required interaction reports fail closed and Plan remains Active. |
| ACP | Protocol options map to typed target modes and the production resolver resumes the Graph. | Cancel, timeout, missing connection, and client event-delivery failure produce typed `Cancel`; delivery failure settles the request once while the producer drains. |
| Standalone MCP | Not applicable. | Every `IsPlanModeTransition` implementation is excluded before registration. |

## Authorization And Compatibility

All current adapters stopped emitting `PlanApprovalDecision.Approved`.
Normalization accepts `Approved=true` only as a one-release legacy input,
limits it to unchanged initial bytes, clears it, and omits the field from
canonical JSON. This retains the promised rollback reader without maintaining
two runtime authorities.

A canonical `Outcome=Approve` still cannot execute directly. Settlement resets
any incoming process-local marker, validates the engine-owned request and
current Plan bytes, then issues a non-serialized capability. The final tool
gate requires the capability and the exact current tool-use ID. Replayed JSON,
raw adapter data, a different request, generic allow/session/always decisions,
and stale reviewed bytes fail closed.

Plan decisions are never positive-coalescing candidates. Approve, revise,
cancel, timeout, and adapter-loss tests all retain a zero generic-grant count.

## Recovery

The additive Plan checkpoint schema did not change. Cold
`AwaitingApproval` still clears the transient request, increments the revision,
returns to Active, and requires a fresh model Exit plus a new reviewed digest.
A same-process live checkpoint may reproject only its exact request. The
process-local execution capability is not serialized and is regenerated only
by settlement during the resumed tool invocation.

## Verification

Focused production-path tests cover:

- TUI ProjectGraph Plan approve/cancel through `handleEngineEvent`;
- plain live-event approval, pending-request cancellation, adapter context
  cancellation, and headless fail-closed behavior;
- ACP production resolver approve/cancel and typed one-shot delivery-failure
  settlement;
- standalone MCP Plan-transition exclusion;
- raw typed outcome rejection, exact request binding, legacy JSON input, stale
  bytes, and cold recovery; and
- zero approval grants on positive and negative Plan outcomes.

The affected engine, CLI, TUI, ACP, and MCP package suites, scoped race matrix,
and the real Plan-editor/TUI workflow PTY tests passed. The repository
format/lint/test/build, new-lint, documentation, manifest, and diff gates also
passed before merge.

## Rollback

Revert the plain event driver, request-bound settlement marker, adapter
mapping/filter changes, focused tests, and documentation as one unit. Keep the
additive typed request/outcome/digest fields and the legacy bool reader for the
remainder of its compatibility release. If the plain driver is reverted,
plain Plan approval must be declared unsupported and fail immediately; it must
not return to the old skipped-event behavior.

Current behavior is owned by
[`architecture/capabilities/permissions.md`](../../../architecture/capabilities/permissions.md),
[`architecture/platform/entrypoints-and-transports.md`](../../../architecture/platform/entrypoints-and-transports.md),
and
[`architecture/runtime/query-engine.md`](../../../architecture/runtime/query-engine.md).
