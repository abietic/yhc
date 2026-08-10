# P29.4 Bounded Overload Failover

**Status:** verification
**Last verified:** 2026-08-02

> **Ownership:** reproducible acceptance evidence for the P29.4 logical-request
> coordinator, overload-only switching, shared budgets, immutable provider
> requests, attempt-scoped output, usage attribution, entrypoint commitment,
> and retained ProjectGraph tool ownership.

## Result

P29.4 maps the promoted `combine` contract to production behavior:

| Contract | Production evidence |
|---|---|
| One logical-request coordinator and no second fallback owner | `TestP294SingleFailoverOwnerSourceGate` |
| Primary-first ordered candidates, stable pre-dispatch skips, and no client construction during chain resolution | `TestP294CoordinatorSkipsCandidatesWithoutConsumingBudgets`, `TestP294CandidateSkipsAreOrderedAndNoCall`, `TestP294FailoverCandidateAdmissionCodesAreStableAndNoCall` |
| Same-route overload retry versus a new profile attempt | `TestP294OverloadRetriesThenSwitchesThroughOneCoordinator`, `TestCallModelWithRetry_Consecutive529ReturnsOverloadToCoordinator` |
| Only typed overload may switch; timeout, transport, auth, invalid input, policy, cancellation, deadline, primary construction, usage ambiguity, and unknown failures are terminal | `TestP294ClassifyModelFailureTaxonomy`, `TestP294OnlyOverloadCanSwitchProfiles`, `TestP294RouteConstructionAndUsageAmbiguityAreTerminal` |
| An unconstructable alternate is skipped before attempt/switch commitment; a later candidate may still start | `TestP294RouteConstructionAndUsageAmbiguityAreTerminal` |
| One provider-call, switch, and absolute-deadline budget across every route | `TestP294SharedProviderCallAndSwitchBudgetsDoNotReset`, `TestCallModelWithRetrySharesProviderCallBudget`, `TestCallModelWithRetryCancellationStopsWaitAndNewDispatch` |
| Messages, system prompt, and tool schemas rebuild from an immutable request snapshot for every dispatch | `TestP294SwitchReplaysImmutableInputWithThinkingCleanup` |
| Legacy and structured provider-bound reasoning/signatures plus message-level provider metadata are removed from the attempt-local clone before a different profile receives history; canonical public text/tool history and private source history remain unchanged | `TestP294SwitchReplaysImmutableInputWithThinkingCleanup`, `TestStripSignatureBlocksRemovesReasoningContent`, `TestStripSignatureBlocksRemovesStructuredReasoningWithoutLegacyText` |
| TUI exact-attempt retraction and conservative plain/headless/ACP/library commitment | `TestP294PartialOutputSwitchesOnlyForRetractableTUI`, `TestP294ZeroOutputSwitchesAcrossModelEntrypoints`, `TestP294RuntimeReducerRetractsOnlyTombstonedAttempt`, `TestP294AppRetractsExactAttemptBeforeProjectingNextAttempt` |
| Failed streamed tool output cannot commit a tool side effect | `TestP294FailedToolStreamHasNoCommittedToolSideEffect` plus the retained P26.1 source gate |
| Exact logical-request/attempt/retry usage attribution and ambiguity fail-closed | `TestP294GoalUsageKeepsExactAttemptAttribution`, `TestP294RouteConstructionAndUsageAmbiguityAreTerminal` |
| Legacy fallback compiles into the canonical bounded policy | `TestP294LegacyFallbackCompilesCanonicalCompatibilityBudget` |
| A new request starts from the current primary; no last-success stickiness or adaptive health exists | `TestP294NewRequestRestartsFromCurrentPrimary`, `TestP294SingleFailoverOwnerSourceGate` |
| Safe bounded all-routes terminal result | `TestP294AllRoutesTerminalSummaryIsBoundedAndRedacted` |
| Explicit lower retry ceilings remain authoritative | `TestP294ExplicitLowerRetryLimitRemainsLowerCeiling` |

The retry/fallback canonical trace fixture now records stable request, attempt,
profile, provider, API-model, route, retry, switch, call, phase, failure,
admission, and output-disposition facts. It deliberately omits latency and
failed output bytes. The Eino v0.9.12 failover wrapper remains rejected as an
owner; provider-specific Eino models stay as leaf dispatchers.

## Reproduction

Focused behavior and race checks:

```bash
go test ./engine -run '^TestP294' -count=1
go test ./engine/provider -run '^TestP294' -count=1
go test ./engine/execution -run \
  '^(TestP294|TestCallModelWithRetry)' -count=1
go test ./internal/tui -run '^TestP294' -count=1
go test -race ./engine ./engine/provider ./engine/execution ./internal/tui \
  -run 'P294' -count=1
```

Compatibility checks include the complete `engine`, `engine/provider`,
`engine/execution`, and `internal/tui` package suites. The P30.3 historical
media-projection fixture remains explicit evidence that attempt cloning
preserves typed runtime metadata and the separate bounded media-recovery owner.

Repository closeout:

```bash
make fmt
make lint
make lint-new
make test
make build
make docs-check
make docs-check-ci
go run ./scripts/migration_manifest.go check
git diff --check
```

## Security, Recovery, And Persistence Boundary

Only the trusted compiled portfolio and admitted current role binding can
supply a failover chain. Prompts, repository files, Agent definitions, model
output, runtime events, transcripts, and Session fields cannot add candidates
or change their order. Snapshots and events exclude account, endpoint,
credential, raw metadata, provider response body, and health state.

Attempt state is process-local. Failed output never enters canonical assistant
or tool history, but the existing provider-usage owner still settles or marks
that call ambiguous. No new Session migration or recovery owner is introduced.
P30.3 media recovery disables the generic coordinator for its separately
bounded selected-route and fallback sequence.

## Boundary

Passing this matrix proves deterministic overload-only portfolio failover. It
does not authorize switching on 429, transport, or timeout; Retry-After
cooldown; last-success stickiness; passive health; cost, quality, quota, or
latency scoring; background probes; new provider adapters; or standalone-MCP
model execution. The later
[`P29.5 Defer Decision`](../plans/p29-model-portfolio-routing.md#p295-defer-decision)
accepted none of those behaviors.
