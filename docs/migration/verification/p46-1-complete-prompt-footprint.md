# P46.1 Complete Prompt Footprint Verification

**Status:** verification
**Last verified:** 2026-08-06

> **Ownership:** reproducible evidence that generic overload failover admits
> candidates against the complete frozen input footprint before route
> construction or dispatch

## Contract

The production canonical model round supplies normalized messages after
user-context prepend, the cloned system prompt, and the cloned complete tool
list to one provider-neutral estimator before `ResolveFailoverChain`.
System-heavy and tool-heavy requests therefore skip an insufficient known
context window with `context_window` while preserving candidate order and all
P29.4 budgets.

The estimate is an input-fit heuristic. It is not a provider billing token
count and does not reserve output tokens.

## Deterministic Fixture

`TestP461CompletePromptFootprintSkipsSmallerContextCandidates` drives the
production query collector through `runCanonicalModelRound`. Its two cases
keep the user message below a literal 64-token alternate limit, then exceed
that limit independently with:

1. the system prompt; and
2. a tool definition whose serializable `ToolInfo.Extra` carries the dominant
   footprint.

The fixture observes the `RoleResolutionInput` delivered at the resolver seam.
It requires the smaller alternate to emit `candidate_skipped/context_window`,
never start, never enter `PrepareModel`, and never reach the provider call. A
4096-token alternate then starts as attempt/switch 1 and completes. Provider
call count remains unchanged across the skipped candidate.

## Commands

```bash
go test ./engine/ -run '^TestP461CompletePromptFootprintSkipsSmallerContextCandidates$' -count=1
go test ./engine/ -run '^(TestP461CompletePromptFootprintSkipsSmallerContextCandidates|TestP294)' -count=1
go test ./engine/provider/ -run '^(TestResolveFailoverChainIsDetachedOrderedAndAdmissionAware|TestP294FailoverCandidateAdmissionCodesAreStableAndNoCall)$' -count=1
go test -race ./engine/ -run '^(TestP461CompletePromptFootprintSkipsSmallerContextCandidates|TestP294)' -count=1
go test ./engine/ -count=1
make fmt
make lint
make test
make build
make docs-check
go run ./scripts/migration_manifest.go check
git diff --check
```

All commands pass on the closeout tree.

## Evidence Limits

The fixture uses literal provider-neutral context thresholds and an in-process
resolver/call seam. It proves request-fact construction, skip accounting,
ordering, and absence of route/provider calls; it does not claim exact remote
tokenization, billing parity, live-provider behavior, physical-terminal
behavior, or remote-CI availability.
