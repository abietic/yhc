# Model Portfolio, Role Routing, And Failover Audit

**Status:** reference-snapshot
**Snapshot:** 2026-07-27

> **Ownership:** source-backed evidence for named model portfolios, provider
> account isolation, capability metadata, agent-role routing, request failover,
> and the G31 adoption recommendation. Accepted execution order belongs in
> [`migration/PLAN.md`](../../PLAN.md).

The original portfolio comparison below remains the P29 program baseline. The
[`P29.3 production role-call addendum`](#p293-production-role-call-addendum)
updates the current-project side at snapshot
`a7d457e06c85f386392dcc932cea4da9024c7e05` after P29.2; it does not refresh or
make floating-upstream claims about the reference repositories. The
[`P29.4 production failover addendum`](#p294-production-failover-addendum)
rechecks the merged P29.3 owner at
`acb3cc99e5ec0c292eb2640174645b581e9a7742` and the already pinned Eino
v0.9.12 failover source. It makes no floating-upstream claim.

## Result

Eino-Agent already has a useful provider-neutral base: six provider-specific
Agentic adapters, deterministic field precedence, one eagerly initialized main
route, lazy cross-provider model switching, a built-in capability registry,
manual TUI/ACP model control, one canonical model-round/tool-commit boundary,
and one overload fallback. It does not yet have a model portfolio.

The missing boundary is not “a larger list of model strings.” A usable
portfolio must distinguish:

1. a credential-bearing provider account and endpoint;
2. a stable user-facing model profile;
3. the provider-local API model identifier;
4. verified or explicitly supplied capability metadata;
5. the agent role that may use the profile; and
6. a bounded, observable failover policy.

The current [`routeRegistry`](../../../../engine/provider/runtime.go) caches one
client per provider. Two OpenAI-compatible profiles with different endpoints or
credentials therefore cannot coexist safely in one runtime. The current
configuration merge also loses source provenance before provider resolution,
while project configuration may override `api_base_url`. Extending those two
mechanisms by adding more model strings would risk sending a credential to the
wrong endpoint.

The recommendation is `combine`:

- preserve Eino-Agent's provider-specific adapters, complete-stream-before-tool
  ordering, explicit model controls, redacted diagnostics, and legacy
  configuration precedence;
- adapt Codex's profile/provider split, project-local endpoint protections, and
  metadata-aware model transitions;
- adapt OpenCode's separation of public model identity, provider-local API ID,
  capabilities, limits, and provider request options;
- adapt Crush's explicit large/small preference idea into project-owned agent
  roles rather than copying its two-role constraint;
- preserve and generalize Claude Code Ripe's retry-versus-switch distinction,
  partial-attempt cleanup, and visible fallback event; and
- treat Eino v0.9.12 failover as a possible leaf mechanism only after it proves
  the project-owned event, transcript, tool, and recovery contract.

The accepted, queued design is
[`P29 Model Portfolio, Role Routing, And Failover`](../../plans/p29-model-portfolio-routing.md).
It does not describe current implementation and adds no `Ready` slice.

## Snapshot Boundary

| Source | Snapshot | Question answered |
|---|---|---|
| Eino-Agent | `ddaf0e4d646012b43206258e6bef18b1e87f33fc` | Current configuration, route identity, metadata, role selection, canonical model/tool ownership, retry/fallback, session, TUI, and ACP ownership |
| Claude Code Ripe | `4b9d30f7953273e567a18eb819f4eddd45fcc877` | Retry versus model switch, partial stream/tool cleanup, thinking compatibility, visible fallback, and agent/model capability schema |
| Codex | `66bd101fff6f0e7e05a594ec7bdb78b92f6b66d3` | Named profile/provider separation, project-local trust restrictions, model metadata, reasoning compatibility, and context-downshift compaction |
| Crush | `2af939d8e900f15edb5e78d766ff0b74dd4fe87e` | Provider catalog, provider-local model selection, large/small preferences, reasoning defaults, and recent-model persistence |
| OpenCode | `411eff73f026d4950c07947c4d983788cb615baa` | Provider/model/API-ID separation, capabilities, limits, cost metadata, credential-to-route lowering, and agent model assignment |
| Eino | pinned `v0.9.12` | Bounded failover attempts, failure predicate, original input, partial output, alternate model selection, and input transformation |

These are local source snapshots and the repository-pinned Eino module. This
report makes no claim about floating upstream behavior, live model prices, or
provider-advertised quotas.

## P29.3 Production Role-Call Addendum

**Current-project snapshot:** `a7d457e06c85f386392dcc932cea4da9024c7e05`

### P29.3 audit result

P29.2 made the root main route durable and fail-closed, but the compiled role
map still does not select production calls. The current runtime has one
authoritative main path and several unrelated side-model seams:

- root and child `QueryEngine` calls share the canonical ProjectGraph model
  round;
- Explore and Plan may use an injected `SubagentModel`, while a separate
  helper supplies a fixed Claude Haiku name that can disagree with the actual
  injected client;
- general Agents inherit the parent client, and the Agent tool/definition
  `model` strings do not currently choose a production client;
- tool-use summaries may use an injected `SummaryModel`, while authoritative
  compaction also consumes that same field under a different lifecycle;
- long-session memory/dream, WebFetch, permission classifier/explainer, and
  the P22 reviewer use separate owners and are not model-portfolio roles.

The P29.3 recommendation is `combine`: preserve the canonical call, tool,
permission, cancellation, recovery, and usage owners; adapt explicit role
selection and provider-specific reasoning options; and introduce one
provider-neutral role snapshot at the existing root/child/summary call
boundaries. Reject fixed provider model IDs and any route override sourced
from repository Agent definitions or model-generated Agent input.

### Complete production owner inventory

| Call class | Current selection owner | Actual call owner | P29.3 decision |
|---|---|---|---|
| Root `main` | P29.2 `QueryEngine` binding, resume admission, and pre-dispatch guard | `runProjectGraphQueryModelRound` -> canonical model round -> `execution.CallModel` | `preserve`; label and snapshot the same admitted main route |
| Explore Agent | `SubAgentExecutor` chooses injected `SubagentModel` only when present; fixed Haiku supplies the model name | Child `QueryEngine` canonical model round | `combine`; configured `explore` role first, trusted compatibility injection second, otherwise admitted main inheritance |
| Plan Agent | Same injected side-model branch as Explore; Plan tool filtering is separate | Child `QueryEngine` canonical model round | `combine`; configured `plan` role without changing Plan tools or permissions |
| Other Agent | Parent client/model; Agent input and definition model strings do not affect dispatch | Child `QueryEngine` canonical model round | `adapt` to configured `general`; keep repository/model-generated model strings non-authoritative |
| Tool-use summary | Optional `SummaryModel`; disabled unless the existing feature gate and model are present | `generateToolUseSummaryAsync` -> `SideQueryWithRetry` -> `CallModel` | `adapt` as the only initial `summary` call; keep it best-effort and non-authoritative |
| Auto/manual compaction | Existing `SummaryModel` or deterministic fallback plus compaction guards | `compact.RunLLMCompact` -> `SideQueryWithRetry` | `preserve` outside P29.3; it mutates authoritative history |
| Agent progress and away-summary services | Callback-only seams with no current engine production wiring | Supplied callback | `defer`; no invented recovery, usage, or routing claim |
| Long-session memory/dream | Root `ChatModel`, disabled while an unfinished Goal needs exact accounting | `callBackgroundProvider.Generate` | `preserve` outside P29.3; memory writes are not summaries |
| WebFetch and permission classifier/explainer | Caller/context side model | `SideQueryWithRetry` | `preserve` outside the fixed five roles |
| P22 reviewer | Explicit separate provider/model/credential composition | Reviewer-specific `Generate` | `preserve` as an independent authorization boundary |

The call inventory was bounded by all production `BaseChatModel.Stream` and
`Generate` sinks plus `execution.CallModel` and `SideQueryWithRetry` callers.
The only remaining direct production sinks are the long-session background
provider entry and the separately constructed P22 reviewer. Callback-only
services are listed as seams, not inferred as active routes.

### Current correctness and authority gaps

1. `model.SubagentModelFor` returns
   `claude-haiku-4-5-20251001`, but `SubagentModel` is an arbitrary
   `BaseChatModel`. Provider/model metadata can therefore disagree with the
   client that receives the call.
2. `PortfolioSnapshot.Roles` materializes inherited optional roles as the
   startup profile. It does not retain whether a role was explicitly bound,
   so an inherited role cannot follow a later P29.2 main switch correctly.
3. `RecordAgentExecutionAdmission` creates the child transcript before
   `ExecuteAgent`, but it currently records `opts.Model` rather than one
   capability-admitted role route and does not write a child model binding.
4. `AgentDefinition.Model`, Agent tool `model`, and
   `model.ResolveSubagentModel` are not on the production child dispatch path.
   Enabling them would also let project content or model output choose a
   credential destination, contrary to P29 source authority.
5. `SummaryModel` is shared by best-effort tool summaries and authoritative
   compaction. Replacing it wholesale with `summary` would silently move
   compaction ownership.
6. P29.2 reasoning admission is Claude-specific. `CallModel` writes Claude
   `output_config.effort`; the other pinned adapters do not consume that
   option, even when profile metadata declares a default.
7. Root prompt-media admission still uses the static default registry rather
   than the selected profile's effective metadata. A second role metadata
   table would let main and child calls disagree.

### Reference comparison for the role question

| Evidence | Useful mechanism | Adoption |
|---|---|---|
| Claude Code Ripe Agent execution passes an Agent/definition-resolved model into the child main loop | Resolve before child construction and retain child-loop ownership | `adapt` only for timing; reject Claude-only aliases and repository/model-generated route authority |
| Claude Code Ripe Explore and tool-summary paths prefer Haiku | A cheap explicit role can be valuable | `adapt` as user-owned profile binding; reject fixed provider/model identity |
| Codex thread options carry model and reasoning effort independently of provider wire format | Provider-neutral admitted intent reaches the runtime with cancellation | `adapt` into one immutable role-call snapshot |
| Crush separates large/small session choices and lowers reasoning through provider-specific request options | Role preference and adapter-owned lowering are separate concerns | `combine` with the five project roles; do not copy its coordinator |
| Eino-Agent P29.2 inventory carries selector, provider/API model, metadata provenance, route digest, and reasoning default | Existing safe material for role admission and durable child binding | `project-native`; extend the existing inventory/runtime rather than add a router |

### Frozen recommendation boundary

The role resolver receives a fixed role, the current admitted main binding,
and dynamic requirements. It returns a detached immutable snapshot containing
the selector/profile, resolved provider/API model, portfolio/route/metadata
identities, reasoning effort, and whether the result came from an explicit
user role, current-main inheritance, or a trusted compatibility injection.
Resolution does not construct a client.

Optional-role inheritance is dynamic: an absent role follows the current
P29.2 main binding at call admission, not the profile that happened to be
selected at startup. An explicit role uses only the user-owned compiled role
map. Trusted compatibility injection retains its narrower current precedence:
Explore/Plan prefer explicit role, then `SubagentModel`, then current main;
general prefers explicit role then current main; summary prefers explicit
role, then `SummaryModel`, then current main. Project Agent files, Agent tool
input, prompts, and transcripts cannot select another profile.

Named-profile role calls require authoritative `true` metadata for their
static role capabilities. Dynamic image/PDF and context requirements reuse the
same effective profile metadata and current P30 admission facts; unknown is
not support. Explicit legacy main use and trusted programmatic injection keep
their existing compatibility behavior but cannot authorize a different
automatic profile.

Child role selection completes before the existing durable execution
admission. A new child stores the raw Agent type separately, the fixed model
role, and the actual P29.2 `model_binding` v1 before `ExecuteAgent` can reach
the provider. Resume re-admits that exact binding; it never re-runs current
role policy to reinterpret a historical child. Old children without the
additive role identity retain their legacy inheritance path.

`summary` initially means only the root-only best-effort tool-use summary.
`EmitToolUseSummaries` must already admit the feature; child tool calls never
dispatch it. The role does not make summary output durable or route
auto/manual compaction, memory/dream, WebFetch, classifier/explainer, Agent
progress/away callbacks, or the P22 reviewer.

Provider-neutral reasoning is admitted against both effective profile
metadata and a pinned adapter table. Empty means provider default. Initial
exact lowering is:

| Adapter | Explicit effort values |
|---|---|
| Claude | `low`, `medium`, `high`, `xhigh`, `max` |
| OpenAI Responses | `none`, `minimal`, `low`, `medium`, `high`, `xhigh` |
| Ark Responses | `minimal`, `low`, `medium`, `high` |
| Gemini | `low`, `high` |
| DeepSeek and Qwen | none in P29.3; provider default only |

An unsupported value fails before provider dispatch. P29.3 does not translate
one level into another, turn Qwen's boolean thinking switch into a graded
effort, or send an untyped extra field. `/effort default` returns to the
selected profile default; a profile with no default returns to provider
default.

### Required evidence

Promotion requires deterministic tests for:

- explicit versus inherited role selection across a main switch;
- root/Explore/Plan/general/summary route identity and actual provider sink;
- unchanged Explore/Plan tools, permissions, ProjectGraph, file state, and
  parent cancellation;
- child admission-before-dispatch, exact binding persistence, compatible
  resume, and stale/unknown fail-closed recovery;
- unknown/false static capabilities plus image/PDF/context requirements with
  zero provider calls on rejection;
- trusted `SubagentModel`/`SummaryModel` compatibility without fixed Haiku
  identity;
- root-only tool-summary dispatch, including zero summary provider calls and
  usage admission from child tool calls;
- provider-specific reasoning options for Claude, OpenAI, Ark, and Gemini plus
  explicit rejection for unsupported adapters/levels;
- fixed-cardinality role and selected-profile usage attribution without double
  counting; and
- source gates proving compaction, memory/dream, WebFetch, permission helpers,
  and the P22 reviewer did not move under the role resolver.

This evidence is sufficient to promote P29.3 only. It does not implement or
authorize P29.4 failover, P29.5 measurement decisions, or adaptive health.

## P29.4 Production Failover Addendum

**Current snapshot:** `acb3cc99e5ec0c292eb2640174645b581e9a7742`

**Reference scope:** retained Claude Code Ripe
`4b9d30f7953273e567a18eb819f4eddd45fcc877` evidence and repository-pinned
Eino v0.9.12

**Decision:** `combine`

### Promotion question

After P29.3, can one implementation replace the current one-hop fallback
without taking tool, transcript, provider-usage, recovery, or entrypoint
ownership away from the canonical ProjectGraph model round?

Yes, if the project owns the logical request and all attempts. The review
freezes `overloaded` as the only route-switch class, one provider-call /
switch / absolute-deadline budget across routes, and an explicit
non-retractable-output watermark per entrypoint. It rejects the Eino failover
wrapper as the owner while keeping Eino provider models as leaves.

### Current production owner after P29.3

The current path is still:

```text
QueryEngine / ProjectGraph
  -> runCanonicalModelRound
     -> CallModelWithRetry
        -> CallModel
           -> routingChatModel
              -> selected provider adapter
     -> ProcessStream
     -> CompleteProviderUsage
  -> runCanonicalToolRound
```

[`runCanonicalModelRound`](../../../../engine/model_round.go) takes the P29.3
role/profile/provider identity, builds exact call options, owns the legacy
retry/switch loop, collects the complete stream through a deferred executor,
and settles provider usage before returning committed assistant/tool-call
state. [`runCanonicalToolRound`](../../../../engine/tool_round.go) remains the
only committed tool-call executor.

The retained P26.1 source gates and tests still reject model-round execution
callbacks and any production tool-dispatch owner outside the tool round.
P29.3 did not weaken that boundary. This closes the most important P29.4
prerequisite: a failed or truncated model attempt has no tool side effect to
undo.

### Current retry and one-hop fallback

[`CallModelWithRetry`](../../../../engine/execution/retry.go) currently:

- detects 429 and 529 through the legacy error classifier;
- retries the same model with bounded exponential backoff;
- returns `FallbackTriggeredError` after three consecutive 529 responses when
  one fallback string is configured;
- stops immediately for cancelled context or provider-usage terminal errors;
  and
- lets the environment change same-route retry count or unattended duration.

[`runCanonicalModelRound`](../../../../engine/model_round.go) recognizes that
sentinel before or after stream collection, then
[`handleFallbackRetry`](../../../../engine/query.go) emits a tombstone,
synthetic missing tool results, one warning, and retries with the fallback
model. The switch clears profile/provider/effort identity and protected
thinking cleanup is conditional on the old compatibility gate. A new
`CallModelWithRetry` invocation receives a new inner retry budget.

The current tests prove one switch, partial-history exclusion, warning, and
same-route retry. They do not prove ordered named profiles, shared budgets,
candidate admission, attempt identity, provider usage per attempt, or
entrypoint partial-output safety.

### Portfolio and usage facts now available

P29.1 already compiles named `failover_policies` into the immutable portfolio.
Validation accepts only `overloaded`, positive call/elapsed budgets, bounded
switches, existing enabled profiles, and no primary/duplicate alternate.
Legacy `fallback_model` is already representable as one main policy, but the
compiled placeholder is not the execution owner and its call budget is not
yet compatibility-correct.

P29.2 supplies stable profile, provider/API-model, route-identity, metadata,
context, and reasoning facts. P29.3 supplies the fixed role snapshot and
candidate capability admission. These facts are enough to reject a candidate
before route construction or dispatch.

[`ProviderUsageCall`](../../../../engine/execution/provider_usage.go) already
distinguishes a proven pre-dispatch release from a possibly dispatched
ambiguous failure. `ProviderUsageTerminalError` already stops retry even when
the underlying failure resembles a transient provider error. P29.4 must add
request/attempt/retry attribution and share one provider-call count; it must
not replace this fail-closed owner.

### Entrypoint commitment evidence

TUI, plain/headless, and ACP consume the same engine events but do not have the
same retraction semantics:

| Consumer | Current safe consequence |
|---|---|
| TUI | The engine emits a generic tombstone, but the current TUI reducer treats it as a no-op; partial visible output is not retractable until P29.4 adds exact attempt-owned removal. |
| Plain/headless | Printed assistant bytes cannot be withdrawn; switching is safe only before the first assistant output event. |
| ACP v1 | Assistant chunks have no protocol retraction; switching is safe only before the first assistant update is offered to the projector. |
| Library | The callback contract does not promise retraction; default to first-output commitment unless a trusted projector explicitly implements attempt tombstones. |
| Standalone MCP | It constructs no model runtime and remains outside P29. |

The current generic tombstone does not carry a logical request/attempt
identity, its TUI projection is a no-op, and the model round does not know a
projector's capability. P29.4 therefore needs one trusted projection-policy
input, an attempt-scoped watermark, and TUI reducer ownership that deletes
only the tombstoned attempt's visible output before projecting the next
attempt. Inferring retraction from model output or claiming that already
printed bytes disappeared would be false.

### Eino v0.9.12 mechanism evaluation

The pinned `adk.ModelFailoverConfig` and `FailoverContext` provide useful
mechanism evidence: bounded alternate calls, original input, last partial
output/error, a failure predicate, alternate model selection, and optional
input transformation.

Direct source recheck also confirms incompatible product ownership:

- the wrapper remembers a last-success model in agent execution context and
  tries it first on later calls;
- its stream path copies and consumes the stream to decide failover;
- its source explicitly permits failed-attempt events to have reached the
  client and delegates reset/deduplication to client handlers;
- its budget is a local failover-attempt count, separate from same-route retry,
  provider-call accounting, and the project's absolute deadline; and
- it cannot emit the project's attempt IDs, tombstone/watermark ordering,
  terminal summary, transcript decision, or Goal usage settlement.

Adopting the wrapper around the model round would create a second lifecycle
owner and silently introduce last-success route stickiness. Adopting it inside
`routingChatModel` would hide route switches below project events and usage.
Both are rejected. The implementation keeps ordinary Eino provider adapters
as leaves and uses a project-owned coordinator.

### Frozen taxonomy and attempt trace

Only `overloaded` may create a new profile attempt. Existing 429 behavior may
retry the same route inside shared budgets but cannot switch. Timeout,
transport, auth, config, invalid request, policy/content, context,
capability/modality/reasoning, conversion, tool protocol, permission,
cancellation, deadline, persistence, route-construction, usage-ambiguity,
local invariant, and unknown failures are terminal or pre-dispatch candidate
skips exactly as frozen in the
[`P29.4 Promotion Freeze`](../../plans/p29-model-portfolio-routing.md#p294-promotion-freeze).

P29.5 owns any later rate-limit, transport, Retry-After, or cooldown decision.

One request snapshots the portfolio revision, role, primary, alternates,
requirements, immutable cleaned input, projection policy, cancellation, and
budgets. Each admitted candidate receives a monotonic attempt identity; each
actual dispatch receives a retry index and provider-call identity. The trace
records bounded safe admission, dispatch, failure class, output disposition,
usage, and terminal facts. It is an event/test trace, not persisted
failed-output or a new Session recovery owner.

### Adoption matrix

| Question | Evidence | Decision |
|---|---|---|
| Complete-stream-before-tool | P26.1 owner tests and current model/tool rounds | `preserve` |
| Profile/role/candidate identity | P29.1-P29.3 portfolio, binding, and role snapshots | `project-native` |
| Retry versus switch cleanup | Current loop and Claude Code Ripe | `preserve` semantics, `adapt` identities and budgets |
| Failover mechanism | Eino typed context plus incompatible last-success/event ownership | `reject` wrapper owner; keep Eino provider leaves |
| Error classes | Current 429/529 behavior plus accepted P29 policy | `adapt`: switch only `overloaded` |
| Entrypoint retraction | TUI tombstone event with current reducer no-op; plain/ACP non-retractable output | `combine`: implement exact TUI attempt removal plus trusted policy and conservative watermark |
| Usage and recovery | Existing fail-closed provider calls; no in-flight attempt schema | `preserve` usage owner; no attempt persistence |
| Adaptive health | No bounded production observations | `defer` to P29.5 or later |

### Promotion result

The retained owner, available portfolio/role facts, frozen taxonomy, shared
budgets, entrypoint watermark, Eino rejection, implementation scope, proof
matrix, and rollback are sufficient to promote exactly P29.4. This addendum
does not implement failover and does not promote P29.5.

## Observable Question

How can a user configure several model/account combinations once, assign the
right model to each agent function, switch them predictably, and use ordered
fallbacks without:

- mixing credentials and endpoints;
- silently trusting repository-controlled routing definitions;
- treating unknown capability defaults as verified facts;
- replaying partial provider artifacts or tool calls into another model;
- multiplying retries and token spend without a shared bound; or
- making TUI, plain/headless, and ACP observe different active models?

A successful design must also preserve legacy startup configuration, existing
provider adapters, cancellation, session recovery, the ProjectGraph query
kernel, and the completed single model-round/tool-commit ownership boundary.

## Current Source Evidence

### Configuration describes one route and loses layer provenance

[`config.Config`](../../../../engine/config/config.go) owns `provider`, `model`,
`api_base_url`, `fallback_model`, and `model_aliases`. `LoadEffectiveConfig`
loads user and project settings, then `MergeConfigs` applies project fields over
user fields. Although a project-local path and loader exist, the effective
loader does not currently include `.claude/settings.local.json`.

`applyOverrides` has mixed merge semantics:

- non-empty model/provider/base URL/fallback fields replace lower values;
- model aliases and MCP server maps merge by key;
- several lists replace as a unit; and
- source provenance is no longer available after the merge.

The project layer may set `api_base_url`. Provider resolution can then combine
that endpoint with an API key selected from explicit input, generic
environment, provider-specific environment, configured input, or the
credential store. The current behavior is compatible, but it is not a safe
template for cross-layer account/profile assembly.

### Runtime client identity is only the provider

[`provider.Runtime`](../../../../engine/provider/runtime.go) resolves a complete
main `ResolvedConfig`, initializes it, and wraps it with `routingChatModel`.
`routeRegistry.models` is a `map[Provider]model.BaseChatModel`.

For another model on the same provider, `routeRegistry.resolveModel` copies the
main provider, API key, base URL, and max-token value. `routeRegistry.route`
then returns the existing client as soon as the provider key matches. There is
no identity for endpoint, credential reference, wire/adapter options, or
account.

[`TestRuntimeRoutesAliasesAcrossProvidersLazily`](../../../../engine/provider/runtime_test.go)
proves lazy initialization and one factory call per provider. It does not cover
two routes with the same provider and different endpoints or credentials. The
current type model cannot express that test case through named configuration.

### Built-in metadata is useful but is not configured inventory

[`model.ModelCapabilities`](../../../../engine/model/capabilities.go) records
context, output, media, thinking, tool, streaming, system-prompt, deprecation,
and token-cost fields. `GetCapabilities` uses exact, alias, and substring
matches, then returns an optimistic default for unknown models.
`KnownContextWindow` correctly avoids presenting that unknown-model default as
a diagnostic fact.

[`model.DefaultRegistry`](../../../../engine/model/registry.go) derives a static
TUI-facing catalog. `/model list` and ACP configuration options enumerate that
built-in catalog and filter it through the current resolver. They do not show a
user's configured endpoints/accounts as the authoritative inventory, and there
is no per-profile metadata override with field provenance.

### Agent roles have no configured routing owner

[`model.SubagentModelFor`](../../../../engine/model/capabilities.go) names a
fixed Haiku model for Explore and Plan when an optional `SubagentModel` is
supplied. [`SubAgentExecutor`](../../../../engine/subagent.go) otherwise uses
the parent model. Current production composition does not inject a distinct
`SubagentModel` or `SummaryModel`; those seams are primarily exercised by
focused tests and embedding paths.

There is therefore no shared configuration owner for main, Explore, Plan,
general Agent, or non-authoritative summary calls. Capability tier helpers also
allow unknown models through, which is acceptable for explicit legacy use but
is not sufficient evidence for automatic role assignment.

### Fallback is one overload-triggered model switch

[`execution.CallModelWithRetry`](../../../../engine/execution/retry.go) retries
429/529 responses on the same model. After three consecutive 529 responses, a
configured fallback causes `FallbackTriggeredError`.

[`runCanonicalModelRound`](../../../../engine/model_round.go) owns the switch
loop and calls
[`handleFallbackRetry`](../../../../engine/query.go), which then:

1. emits a tombstone;
2. yields synthetic missing tool results for attempt-local tool calls;
3. continues only after the deferred streaming executor has been discarded;
4. changes the main-loop model;
5. strips protected thinking signatures only under the current Anthropic
   compatibility gate; and
6. emits a visible `model_fallback` warning.

[`TestQueryRetriesWithFallbackModelOnFallbackTriggeredError`](../../../../engine/query_fallback_test.go)
proves one switch and warning.
[`TestQueryEngineFallbackRetryDropsPartialAssistantHistory`](../../../../engine/query_fallback_test.go)
proves discarded partial assistant content does not become persisted history.
There is no ordered chain, shared retry/switch budget, stable attempt identity,
role-specific policy, route health, or capability/context admission for the
fallback.

### Session state remembers a model string, not a durable profile

[`SessionMetadataFull`](../../../../engine/session/branch.go) persists `model`
and `provider`, but no logical profile ID, capability/limit digest, route
identity, reasoning default, role binding, or failover attempt lineage.
[`persistSessionCheckpoint`](../../../../engine/session_checkpoint.go) records
the current model string and derives provider from it.

[`ResumeSession`](../../../../engine/session/resume.go) rebuilds transcript and
session metadata. It does not re-resolve a profile or classify a changed
context window/capability set. A model/profile change between process runs can
therefore be neither accepted nor rejected through an explicit compatibility
contract.

### Supported entrypoints share construction but not portfolio projection

CLI TUI/plain/headless and ACP construct the same provider runtime and inject
it as `QueryEngine`'s `ModelResolver`. ACP's `sessionConfigOptions` and TUI
`/model` still project the static registry. The independent standalone MCP
server constructs no model runtime and is not an applicable portfolio
entrypoint.

This is a strong reuse seam: P29 can replace one inventory/resolution owner
without inventing a second query loop or entrypoint-specific router.

## Reference Evidence

### Codex protects credential destinations and separates profiles

`ConfigProfile` in
`.reference/codex/codex-rs/config/src/profile_toml.rs` selects a model, model
provider, service tier, and reasoning settings. The provider map points to
`ModelProviderInfo` in
`.reference/codex/codex-rs/model-provider-info/src/lib.rs`, which groups base
URL, credential environment variable, wire API, headers, retry limits, stream
timeout, and authentication behavior.

Codex explicitly treats repository configuration as a trust boundary.
`PROJECT_LOCAL_CONFIG_DENYLIST` and `sanitize_project_config` in
`.reference/codex/codex-rs/config/src/loader/mod.rs`
prevent project-local configuration from choosing base URLs, profiles, model
providers, or provider definitions; untrusted project layers can also be
disabled through `ProjectTrustContext`.

`ModelInfo` in
`.reference/codex/codex-rs/protocol/src/openai_models.rs` records context,
maximum context, compaction compatibility, reasoning levels, tools,
modalities, and other request capabilities. `TurnContext::with_model`
re-resolves metadata and changes unsupported reasoning effort to a supported
value/default. `maybe_run_previous_model_inline_compact` in
`.reference/codex/codex-rs/core/src/session/turn.rs` detects compaction
incompatibility or a smaller context model before sampling.

Selected consequence: adapt the trust restriction, profile/provider split, and
metadata drift check. Do not copy Codex's complete configuration system or
silently choose an arbitrary replacement reasoning effort.

### OpenCode separates configured identity from provider API identity

`ConfigProvider.Info` in
`.reference/opencode/packages/core/src/config/provider.ts` contains provider
request defaults and a model map. Each model may define a public name,
provider-local `api.id`, capabilities, variants, limits, cost, and request
settings. `ModelV2.parse` in
`.reference/opencode/packages/core/src/model.ts` keeps provider ID and model ID
separate.

`SessionRunnerModel.fromCatalogModel` in
`.reference/opencode/packages/core/src/session/runner/model.ts`
converts a catalog model and credential into an API route. It removes `apiKey`
from the HTTP body, applies endpoint/headers/limits, selects the correct
protocol, and forwards the provider-local API model ID.

Selected consequence: adapt the public-profile/provider-local-ID separation,
capability/limit model, and typed provider lowering. The audit found no
production general-purpose LLM failover policy to copy.

### Crush proves explicit role preference and model persistence

`SelectedModel` in `.reference/crush/internal/config/config.go` separates
provider ID and provider API model ID and carries reasoning, thinking,
max-token, sampling, and provider options. `ProviderConfig` in the same file
contains endpoint, provider type, API-key template, OAuth, headers, body
extensions, flat-rate state, and model catalog.

Crush stores `large` and `small` preferences. `resolveSelectedModels` in
`.reference/crush/internal/config/load.go`
validates them against the catalog and restores model defaults for token and
reasoning settings. `ConfigStore.UpdatePreferredModel` persists selection and
recent models together.

Selected consequence: adapt explicit, durable role preference and validation,
but generalize the role vocabulary. Crush's invalid-selection correction is a
configuration fallback, not request failover, and its raw `api_key` field is
not a security precedent.

### Claude Code Ripe has the strongest switch cleanup evidence

`withRetry` in
`.reference/claude-code-ripe/src/services/api/withRetry.ts` distinguishes
ordinary same-model retries from a `FallbackTriggeredError` after a bounded
consecutive-529 threshold.

`queryLoop` in `.reference/claude-code-ripe/src/query.ts` switches the model,
clears assistant/tool-result/tool-use buffers, discards and recreates the
streaming executor, changes the main-loop model, removes incompatible thinking
signatures, records telemetry, and emits a visible warning. The request is
submitted again with cleaned history. This is not “failover without prompt
replay.”

Selected consequence: preserve the retry/switch distinction and adapt the
cleanup into one logical-request/multiple-attempt contract. Replay is allowed
only before irreversible tool or non-retractable output commitment.

### Eino exposes a mechanism, not the product policy

Eino v0.9.12 is pinned in [`go.mod`](../../../../go.mod). Its
`adk.ModelFailoverConfig` provides bounded attempts, `ShouldFailover`,
`FailoverContext` with original input/last partial output/error, and
`GetFailoverModel` with optional input transformation. It also prefers the
last successful model on later calls.

Selected consequence: reuse is conditional. The project must retain failure
classification, attempt identity, event/tombstone ordering, tool safety,
transcript commitment, entrypoint projection, and session recovery. The
last-success preference is rejected for the first P29 failover slice because
it silently changes cost, cache affinity, and starting-route behavior.

## Adoption Matrix

| Product question | Evidence | Decision |
|---|---|---|
| Account, profile, and API model identity | Current provider-only cache; Codex profile/provider; OpenCode provider/model/API ID | `combine`: user-owned provider accounts plus stable model profiles and provider-local API IDs |
| Repository trust and credentials | Current project base-URL override; Codex denylist/trust | `adapt`: new credential-bearing definitions are user/runtime-owned; project config may select only a user-authorized profile and cannot redefine it |
| Capability and limit metadata | Current static table; OpenCode/Codex rich metadata | `combine`: built-in metadata plus explicit validated user overrides with per-field provenance |
| Agent-function routing | Current optional side-model seams; Crush roles; OpenCode agents | `adapt`: explicit `main`, `explore`, `plan`, `general`, and `summary` bindings |
| Retry versus route switch | Current/Claude bounded 529 switch | `preserve`: same-route retry remains distinct from a new model attempt |
| Failover chain and input transform | Claude cleanup; Eino typed failover context | `combine`: project-owned ordered chain and events; optionally reuse a proven Eino leaf |
| Sticky last-success and health | Eino last-success; no matching current contract | `defer`: always begin with the configured/session-selected primary until measured health policy is accepted |
| Automatic price/quota optimizer | Metadata exists; live quotas and stable prices are unverified | `reject` for initial slices: explicit roles/chains first, measurement before adaptive policy |

## Verified, Inferred, And Unresolved

| Classification | Finding |
|---|---|
| Verified | Current effective configuration represents one main model plus one fallback and loses user/project provenance after merge. |
| Verified | Current route clients are cached only by provider and same-provider model switches reuse the main endpoint and credential. |
| Verified | Current configured inventory, session metadata, and role routing have no stable profile identity. |
| Verified | Current and Claude fallback both replay a cleaned logical request; neither proves zero prompt replay. |
| Verified | OpenCode/Crush did not provide a production general-purpose request-failover policy in the audited scope. |
| Inferred | Users with multiple prepaid or rate-limited accounts benefit from explicit role and failover policies, but the repository has no usage baseline proving an optimal automatic selector. |
| Unresolved | Exact provider quota APIs, live prices, cache-hit economics, and acceptable per-role quality/latency thresholds. These require measurements, not static claims. |

## Compatibility Consequences

- Existing `provider`, `model`, `api_base_url`, `fallback_model`,
  `model_aliases`, CLI flags, and `PROV_*` variables remain compatibility
  inputs. They compile into an internal legacy portfolio rather than remaining
  a second runtime owner.
- The legacy compiler temporarily preserves project `api_base_url` precedence
  with a redacted warning. Named portfolio routes never inherit that value;
  removing the legacy override remains a separate breaking migration decision.
- New named accounts and profiles do not inherit credential or endpoint fields
  from project configuration.
- A custom unknown model remains explicitly selectable for legacy compatibility,
  but it is not eligible for automatic role assignment or failover until the
  required capabilities and limits are declared.
- Manual TUI/ACP model selection becomes profile-first. A selected profile ID,
  not a credential or endpoint, is the durable public identity.
- Failover may resend cleaned prompt/history as a new attempt. It may not
  duplicate tool side effects, preserve incompatible thinking artifacts, or
  silently truncate to fit a smaller context.
- Standalone MCP remains outside the model runtime.

## Rejected Shortcuts

- expanding `fallback_model` into a comma-separated list;
- caching clients by provider or provider/model only;
- placing raw API keys in settings, session metadata, diagnostics, or events;
- field-by-field merging of endpoint and credential-bearing account records
  across user and project layers;
- treating the optimistic unknown-model defaults as verified role/failover
  capabilities;
- hard-coding one cheap model for every Explore or Plan agent;
- letting the provider router decide when a logical request should switch;
- wrapping the entire runtime in Eino failover before event, tool, transcript,
  and entrypoint equivalence is proven;
- retrying every profile with a fresh full retry budget; or
- adding active health probes that spend tokens merely to test availability.

## Evidence Limits

This audit proves current ownership and reference mechanisms, not future model
quality or cost savings. No provider was called, no live quota was inspected,
and no pricing claim is made. The existing focused tests prove current
single-fallback cleanup and cross-provider routing; the same-provider
multi-account, profile recovery, role-admission, chain, and cross-entrypoint
attempt scenarios remain required implementation evidence.

Current implemented provider behavior remains owned by
[`architecture/platform/model-providers.md`](../../../architecture/platform/model-providers.md).
G31 remains in [`REMAINING.md`](../../REMAINING.md) until the accepted P29
outcome is fully delivered.

## Recommendation

Accept P29 under `combine`, but keep it queued and non-executable while P22.1a
is the sole `Ready` slice. Build one immutable portfolio snapshot from trusted
account definitions, stable model profiles, metadata, role bindings, and
ordered failover policies. Let `engine/provider` cache clients by a complete
non-secret route identity, let `QueryEngine` and its canonical model-round
boundary own role selection and failover attempts, and let every supported
entrypoint project the same logical profile. Completed P26.1 is a retained
prerequisite, not an outstanding dependency.

Start with deterministic configuration and explicit role/chain selection.
Measure per-profile usage, failures, and latency before adding passive cooldown
or any adaptive optimization. This uses the user's token pools deliberately
without turning cost, safety, or recovery into an opaque scheduler.
