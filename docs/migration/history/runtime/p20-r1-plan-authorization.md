# P20.R1 Plan Authorization Repair

**Status:** historical
**Completed:** 2026-07-26
**Last verified:** 2026-07-26

> **Ownership:** completed delivery evidence for explicit TUI Plan intent,
> unique cross-entrypoint targets, and two-step bypass confirmation. Current
> behavior belongs in
> [`permissions.md`](../../../architecture/capabilities/permissions.md) and
> [`editing.md`](../../../architecture/tui/contracts/editing.md).

## Outcome

P20.R1 completed the authorization half of the reopened G10 repair under the
existing `combine` decision. QueryEngine remains the sole Plan lifecycle and
settlement owner; TUI, plain, and ACP only present the exact reviewed Plan and
return a typed terminal intent.

| Entry point | Delivered boundary |
|---|---|
| TUI | `PlanDialog` carries explicit Approve/Revise/Cancel intent. Bypass selection enters `BypassConfirmation`; only Yes sets `Confirmed=true`. No/Esc returns to Actions without losing selection, viewport, feedback draft, cursor, or undo. Generic responses and retained drafts cannot reconstruct intent. |
| Plain | One explicit loop exposes unique previous/accept-edits/bypass targets. Every bypass target requires the exact `BYPASS` token; negative input returns to targets, while EOF/cancellation/timeout cancels. |
| ACP | Target, confirmation, and Back-reissued requests each receive a fresh transport `ToolCallId`. All rounds share one absolute deadline and retain the original Plan request, revision, path, and reviewed digest. Timeout, parent cancellation, transport loss, unknown response, and incomplete confirmation fail closed. |

[`PlanApprovalTargetModes`](../../../../engine/permission_interaction.go#L140)
is the shared de-duplication helper. It grants no authority. Final engine
settlement still revalidates request identity, revision, exact current bytes,
reviewed digest, target, and bypass confirmation before issuing the
request-bound Exit capability.

## Preserved Invariants

- non-bypass decisions keep `Confirmed=false`;
- a bypass target cannot approve after one UI or protocol action;
- Plan outcomes create no session, project, or always-allow grant;
- no runtime decision item or Exit execution is produced before the second
  bypass confirmation;
- Back does not refresh the ACP timeout budget or change Plan identity; and
- stale bytes, unknown choices, input loss, cancellation, timeout, and adapter
  loss all preserve Plan and execute no tool.

## Verification

The closeout passed:

```text
go test ./engine -run 'Test.*PlanApprovalTarget|Test.*Plan.*Target'
go test ./internal/tui -run 'Test.*(Plan|ThreadAttention|PermissionInteraction)'
go test ./cmd/eino-agent/cmd -run 'Test.*Plain.*Plan'
go test ./server/acp -run 'TestACP.*Plan'
go test -race ./internal/tui ./server/acp -run 'Test.*Plan'
make fmt
make lint
make test
make build
make lint-new
make docs-check
go run ./scripts/migration_manifest.go check
git diff --check
```

Focused ACP evidence includes a previous-bypass second round, distinct
transport IDs, one non-refreshed deadline, near-budget exhaustion, parent
cancellation between rounds, transport failure, and zero Exit execution before
the confirmed runtime item resumes. An independent permission review found no
blocking defect.

## Compatibility And Rollback

The change adds no durable schema, Graph topology, public protocol field, or
generic permission behavior. The existing one-release read-only `Approved`
compatibility input remains unchanged. Rollback must revert the three adapter
interaction changes as one unit; if an entrypoint cannot preserve two-step
confirmation, its safe rollback is to omit bypass rather than restore a
single-action approval path.

## Current Replacement

Current implementation ownership is
[`permissions.md`](../../../architecture/capabilities/permissions.md) and
[`editing.md`](../../../architecture/tui/contracts/editing.md). The detailed
program contract remains
[`p20-plan-mode-interaction.md`](../../plans/p20-plan-mode-interaction.md).
The consolidated corrected closeout is
[`p20-r3-plan-interaction-closeout.md`](p20-r3-plan-interaction-closeout.md);
G10 is closed and [`PLAN.md`](../../PLAN.md) promotes G11.A.
