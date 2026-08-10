# P48.2 ACP Plan Tool-Call Identity Verification

**Status:** verification
**Last verified:** 2026-08-07

> **Ownership:** reproducible evidence that one engine tool-use identity spans
> ACP Plan start, every permission round, and terminal delivery without
> replacing Plan authorization identity

## Contract

For one `ExitPlanMode` invocation, the non-empty
`PermissionPromptRequest.ToolUseID` is the ACP `toolCallId` used by the
client-visible start, initial Plan choice, every Back retry or bypass
confirmation, and exactly one terminal update. Plan RequestID, revision,
reviewed digest, target, settlement, and the shared absolute interaction
deadline remain independent engine-owned facts.

A blank or whitespace-only Plan tool identity fails closed before the Plan
snapshot is read or any client permission request is made. Non-Plan permission
fallback and engine Plan policy are outside this compatibility repair.

## Deterministic Evidence

The production ProjectGraph fixture emits a model `ExitPlanMode` call, routes
its initial and resumed engine events through `Agent.streamEvent`, and records
the real ACP wire order. Approve, bypass, and reject require one start, one or
two permission requests with the same model ID, and one terminal update with
that ID in exact order.

Structured-target coverage keeps Plan RequestID distinct from the tool ID and
proves manual, accept-edits, bypass, Back-then-edits, reject, and unknown
responses reuse one transport identity. Existing failure fixtures preserve
one absolute deadline across the bypass round, parent cancellation, timeout,
transport failure, and typed Plan cancellation. The blank-ID regression uses
a missing Plan path and requires zero adapter and client permission calls, so
identity admission must precede snapshot I/O.

The focused race command exercises the same Plan approval and bypass cases.
The full ACP package, runtime contract/race packs, and official TypeScript ACP
SDK v1.3.0 subprocess harness cover the surrounding lifecycle and protocol
boundary.

## Commands

```bash
go test ./server/acp/ -run '^(TestACPProjectGraphPlanDecisionUsesProductionResolver|TestACPPlanApproval|TestACPPlanBypass)' -count=1
go test -race ./server/acp/ -run '^(TestACPProjectGraphPlanDecisionUsesProductionResolver|TestACPPlanApproval|TestACPPlanBypass)' -count=1
go test ./server/acp/ -count=1
make test-contract
make test-race
./scripts/verify-p23-5-acp-sdk.sh
make fmt
make lint
make test
make build
make lint-new
make docs-check
go run ./scripts/migration_queue check
go run ./scripts/migration_manifest.go check
git diff --check
```

All commands pass on the closeout tree.

## Evidence Limits

The local checks prove adapter behavior, deterministic event ordering, race
detector coverage, and real ACP v1 SDK wire compatibility. They do not claim a
remote-CI result, live network provider, ACP v2 behavior, or that ACP owns Plan
authorization. The repair does not change non-Plan missing-ID fallback or
introduce persistence, schema, or migration work.
