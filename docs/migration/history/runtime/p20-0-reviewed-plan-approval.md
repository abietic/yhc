# P20.0 Typed Reviewed-Plan Approval

**Status:** historical
**Completed:** 2026-07-25
**Last verified:** 2026-07-25

> **Ownership:** delivery boundary, adoption decision, compatibility effect,
> rollback, and verification evidence for reviewed Plan approval

## Outcome

`ExitPlanMode` approval now binds a typed user outcome to exact reviewed Plan
bytes. QueryEngine snapshots `InitialPlanDigest` when Active enters
AwaitingApproval. TUI, plain, and ACP render or reload the exact Plan file and
return `ReviewedPlanDigest`. Settlement re-reads the same path and permits
Approve only when the current bytes match the reviewed digest.

Both identities use `sha256:<lowercase hex>` over exact file bytes. Newline,
text, path, rendering, and metadata normalization are intentionally excluded.
Changed bytes, missing files, invalid digests, mismatched request/revision,
generic allow, stale owner, unconfirmed bypass, timeout, and cancellation all
return to Active Plan without executing Exit.

## Typed Outcome And Mode Contract

The canonical outcomes are:

- `Approve`, with reviewed digest, target mode, and bypass confirmation;
- `Revise`, with feedback and no generic denial-tracking authority; and
- `Cancel`, with no feedback or permission grant.

Existing `Approved` remains readable for one compatibility window. A legacy
approval without a reviewed digest is bound to the request's initial digest,
so it can approve only unchanged initial bytes.

Every known non-Plan `ReturnMode` is preserved through Plan entry, persistence,
replay, and settlement. The first adapter action uses that exact previous mode.
An idle user abandon restores it and cannot select a new implementation mode.
Active-turn and AwaitingApproval external mode changes fail closed. Bypass
still requires explicit confirmation.

## Projection And Recovery

- TUI `Show` and `ReloadPlan` retain the digest of the exact displayed bytes.
- Plain output renders the full reviewed Plan before returning a typed result.
- ACP sends the full Plan as tool-call text content and returns the same digest.
- ProjectGraph HITL invocation identity includes the initial digest, so a
  resume reconstructs the same request even if the file later changes.
- Runtime interaction replay and process-local live-request matching require
  the same digest.
- The version-1 persisted Plan record adds an optional initial-digest field.
  Old checkpoints remain readable; an old AwaitingApproval record without a
  valid live digest cold-normalizes to Active.

## Adoption And Compatibility

P20.0 is a `combine` decision:

- `preserve` P17's QueryEngine phase owner, exact path, request/revision,
  execution-after-tool-result ordering, cold normalization, and Graph HITL
  owner;
- `adapt` Grok Build's explicit approve/revise/cancel vocabulary and Codex's
  explicit mode-transition boundary; and
- use `project-native` exact reviewed-document identity and complete
  `ReturnMode` restoration across supported entrypoints.

The change adds fields without changing the Plan phase/checkpoint version.
There is no Eino/Eino-ext source or dependency change, Graph topology change,
worktree action, generic permission grant, or automatic implementation handoff.

## Verification

Focused tests cover exact-byte/newline identity, unchanged and stale review,
external edit plus reload, typed outcomes, legacy normalization, generic allow,
bypass confirmation, every non-Plan return mode, idle abandon, active and
AwaitingApproval exclusion, checkpoint/runtime/Graph identity, TUI reload,
plain rendering, and ACP content. The four affected packages and scoped race
tests pass.

Repository-wide formatting, lint, test, build, new-lint, documentation,
manifest, and diff gates passed before merge.

## Rollback

Revert the typed outcome/digest fields, adapter projections, settlement
comparison, full return-mode acceptance, and tests as one unit. Version-1
checkpoints remain compatible because the digest field is additive and
optional. Rollback must restore legacy bool normalization without weakening
the already-merged P20.H0 capability precedence or P17 phase containment.
