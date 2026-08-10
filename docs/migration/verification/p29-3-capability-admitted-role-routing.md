# P29.3 Capability-Admitted Agent-Role Routing

**Status:** verification
**Last verified:** 2026-07-30

> **Ownership:** reproducible acceptance evidence for P29.3 role authority,
> selected-profile admission, child durability/recovery, trusted side-model
> compatibility, root-only tool summaries, provider effort lowering, bounded
> usage identity, and explicit owner exclusions.

## Result

P29.3 maps the promoted contract to production behavior:

| Contract | Production evidence |
|---|---|
| Explicit optional-role presence and dynamic current-main inheritance | `TestCompilePortfolioRetainsExplicitRolePresence`, `TestRoleResolverExplicitAndDynamicMainInheritance`, `TestP293ChildRoleAdmissionRoutesExplicitAndDynamicMain` |
| Detached role snapshot and zero client construction during admission | `TestRoleResolverReturnsDetachedSnapshotWithoutRouteConstruction` |
| Actual shared-router sink across providers and same-route profiles | `TestRoleResolverSnapshotsReachSharedRuntimeAcrossProvidersAndProfiles` |
| Unknown/false static capability plus image, PDF, thinking-history, and context rejection | `TestRoleResolverStaticAndDynamicCapabilityAdmission`, `TestP293ChildContextAdmissionRejectsBeforeModelDispatch` |
| Named rich-prompt admission uses selected profile metadata, not a static fallback answer | `TestP293NamedPromptMediaUsesSelectedProfileMetadata` |
| Foreground/background child role and exact binding commit before dispatch | `TestP293ChildRoleAdmissionRoutesExplicitAndDynamicMain`, `TestP293BackgroundChildPersistsRoleBeforeDispatch` |
| Resume uses the persisted child binding rather than current role policy | `TestP293ChildResumeUsesPersistedBindingNotCurrentRolePolicy` |
| Partial P29.3 identity fails closed without transcript repair | `TestP293PartialDurableChildIdentityFailsClosed`, `TestP293ResumeRejectsUnknownOrMismatchedModelRole` |
| Old children retain legacy parent inheritance, then upgrade | `TestP293OldChildUpgradesThroughLegacyInheritanceWithoutPolicyRewrite` |
| Truthful `SubagentModel` compatibility and explicit-role precedence | `TestP293TrustedSubagentInjectionUsesTruthfulSelectorAndExplicitWins` |
| Root-only enabled tool summary, exact compatibility precedence, and bounded usage identity | `TestP293SummaryRolePrecedenceAndUsageAttribution` |
| Profile defaults, `/effort default`, incompatible-switch warning, and exact adapter admission | `TestP293ReasoningDefaultsResetAndIncompatibleSwitch`, `TestRoleResolverReasoningPrecedenceAndAdapterTable` |
| Typed OpenAI/Ark/Gemini lowering plus DeepSeek/Qwen and unsupported-level rejection before usage/provider entry | `TestCallModelLowersProviderReasoningEffortThroughTypedOptions`, `TestCallModelRejectsUnsupportedEffortBeforeProviderUse` |
| Fixed role/profile/applied-effort provider-usage attribution | `TestCallModelAttributesFixedRoleAndProfileToProviderUsage` |
| No fixed Haiku, failover, adaptive-health, compaction, memory/dream, WebFetch, permission, or reviewer owner expansion | `TestP293ExcludedModelOwnersRemainOutsideRoleRouting`, `TestP293RoleRoutingAddsNoFailoverOrAdaptiveHealthOwner` |

The existing ProjectGraph and AgentRunner suites remain the authority for
Explore/Plan tool filtering, permission decisions, foreground/background
stage, file-state/worktree scope, parent cancellation, terminal replay, and
owned close. P29.3 changes only the admitted model identity carried through
those owners.

## Reproduction

Focused behavior and provider/session/concurrency checks:

```bash
go test ./engine -run \
  '^(TestP293|TestP139bBackgroundAdmissionRejectsUnattributedMessageOnlyTranscript)' \
  -count=1
go test ./engine/provider -run '^TestRoleResolver' -count=1
go test ./engine/execution -run \
  'TestCallModel(LowersProviderReasoningEffortThroughTypedOptions|RejectsUnsupportedEffortBeforeProviderUse|AttributesFixedRoleAndProfileToProviderUsage)' \
  -count=1
go test -race ./engine/provider ./engine/session ./engine/execution ./engine \
  -run '^(TestP293|TestRoleResolver|TestConfiguredRuntime|TestCallModel(LowersProviderReasoningEffortThroughTypedOptions|RejectsUnsupportedEffortBeforeProviderUse|AttributesFixedRoleAndProfileToProviderUsage)|TestP292PersistedModelBinding|TestP292ConcurrentReasoningChangeInvalidatesPreparedModelCandidate|TestP139bBackgroundAdmissionRejectsUnattributedMessageOnlyTranscript)' \
  -count=1
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

The source gates walk production owners rather than relying on comments. They
reject role-routing symbols in compaction, memory/dream services, permission
helpers, WebFetch, and the provider reviewer; they also reject the retired
`SubagentModelFor` owner and failover/adaptive-health symbols in the P29.3 role
boundary.

## Security And Recovery Boundary

Only user-owned named role bindings, trusted composition-root injections, and
the current admitted main binding can select a role route. Agent definitions,
tool input, prompts, transcript fields, runtime events, and repository content
remain non-authoritative. Admission snapshots and persisted bindings exclude
account, endpoint, auth kind/reference/value, client, raw metadata, and route
health.

A new child cannot reach its executor before its original Agent identity,
fixed model role, and exact binding commit. A record with only part of that
P29.3 identity fails closed and is not repaired from current runner arguments.
An old record with neither role nor binding retains the legacy path and
upgrades only at a newly admitted execution.

## Boundary

Passing this matrix proves capability-admitted deterministic role selection
and exact provider effort lowering. It does not execute a failover policy,
change retry taxonomy, route authoritative compaction or background helpers,
add adaptive health, enable a summary feature, or create a standalone-MCP
portfolio runtime. Those boundaries remain owned by later P29 promotion.
