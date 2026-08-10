# P29.2 Shared Inventory, Manual Switching, And Durable Binding

**Status:** verification
**Last verified:** 2026-07-30

> **Ownership:** reproducible acceptance evidence for P29.2 inventory,
> selector, model-control transaction, Session binding, recovery guard,
> compaction, fork/export/listing, and entrypoint projection behavior.

## Result

P29.2 maps the promoted contract to production behavior:

| Contract | Production evidence |
|---|---|
| Detached, sorted, non-secret provider inventory | `TestP292InventorySnapshotIsDetachedSortedAndNonSecret` |
| Exact profile selectors and labelled legacy compatibility | `TestP292SelectorGrammarKeepsProfileAndLegacyPathsDistinct`, `TestP292LegacyInventoryLabelsItsDefaultSelector` |
| Canonical non-secret route digest | `TestRouteIdentityDigestUsesCanonicalNonSecretFields` |
| Validate and checkpoint before live model mutation | `TestP292ModelSwitchCommitsCheckpointBeforeLiveMutation`, `TestP292ModelSwitchFailureDoesNotMutateLiveBinding` |
| Process-local result without a recorder | `TestP292RecorderlessModelSwitchReportsProcessLocalCommit` |
| Uncertain checkpoint blocks switching, compaction, and provider dispatch until reload | `TestP292UncertainCheckpointBlocksAllProviderDispatch` |
| First, switched, and reasoning checkpoints persist the exact binding | `TestP292FirstAndSwitchedCheckpointsPersistExactBinding`, `TestP292ReasoningEffortUsesTheSameDurableBoundary` |
| Stale prepared candidates cannot win a concurrent model/reasoning race | `TestP292ConcurrentReasoningChangeInvalidatesPreparedModelCandidate` |
| Strict v1 validation and opaque invalid/unknown preservation | `TestP292PersistedModelBindingValidProjectionAndClone`, `TestP292PersistedModelBindingPreservesOpaqueNestedJSON`, `TestP292PersistedModelBindingRejectsUntrustedProjection` |
| Resume route, metadata, limit, and reasoning matrix | `TestP292ResumeAdmissionFailsClosedAndWarnsOnlyOnCompatibleDrift`, `TestP292NewSessionWithIncompatibleMetadataFailsClosed` |
| Context-only block clears only after a fitting durable compaction | `TestP292FailedCompactionKeepsContextOnlyDispatchBlock`, `TestP292DurableFittingCompactionClearsOnlyContextBlock`, `TestP292TokenWarningUsesAuthoritativeContextWindow` |
| Fork samples the latest binding; branch/list/export preserve opaque data but expose only safe state/kind/value | `TestP292ActiveForkSamplesLatestLiveBinding`, `TestP292BranchPreservesOpaqueBindingWithoutProjectingIt`, `TestP292ValidBindingListingAndExportsExposeOnlySafeProjection` |
| Commands, TUI picker, and ACP project the configured inventory | `TestP292ModelCommandUsesConfiguredSelectorsOnly`, `TestP292ModelPickerUsesOnlyConfiguredInventorySelectors`, `TestP292ACPOptionsUseConfiguredInventorySelectors` |
| Named startup is visible and legacy startup stays silent | `TestP291CLIConfiguredProfileReachesSharedRuntime`, `TestP292LegacyCLIStartupDoesNotClaimNamedProfile` |

Every provider attempt also passes the engine-owned binding guard. Identity
blocks cannot be bypassed by compaction; a context-only block can be cleared
only after the existing compaction owner durably checkpoints a fitting
history.

## Reproduction

Focused behavior and race checks:

```bash
go test ./engine/provider ./engine/session ./engine/compact ./engine/commands \
  ./engine ./internal/tui ./server/acp ./cmd/eino-agent/cmd \
  -run '^(TestP292|TestP291ACPCreateAndRestoreUseConfiguredProfile|TestP165aModelAndEffortCapabilitiesFailBeforeMutation)' \
  -count=1
go test -race ./engine/provider ./engine/session ./engine \
  -run '^(TestP292|TestConfiguredRuntime)' -count=1
```

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

The closeout secret scan covers changed runtime artifacts, serialized Session
and export shapes, captured diagnostics, and test output. The inventory and
user-facing binding projection contain no account ID, endpoint, auth
kind/reference/value, header, client, route health, digest, or opaque nested
payload.

## Boundary

Passing this matrix proves shared manual inventory and recoverable main-route
binding. It does not route Explore, Plan, general Agent, or summary roles,
lower new provider-neutral reasoning behavior, execute failover policies,
add hot reload, or create a standalone-MCP model runtime. Those boundaries
remain owned by later P29 slices, and G31 remains open.
