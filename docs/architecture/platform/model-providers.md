# Model Provider Runtime

**Status:** current
**Last verified:** 2026-08-24

> **Ownership:** `engine/provider.Runtime`, provider-specific adapters,
> credential loading in `engine/auth`, and model capability policy in
> `engine/model`

## Current provider runtime

The provider runtime separates source authority, deterministic route
resolution, and client creation. `NewConfiguredRuntime` first compiles user
and project layers into one non-secret portfolio snapshot. A selected named
profile or the internal `legacy.main` profile is then constructed eagerly;
other admitted routes remain lazy. CLI and ACP both inject this runtime as the
`QueryEngine` model resolver.

The runtime also exposes one detached, non-secret inventory of configured
profiles. QueryEngine overlays the active logical binding and dispatch guard;
commands, TUI, plain/headless startup, and ACP project that shared state.
Manual profile changes and supported reasoning changes use one
checkpoint-before-live-mutation transaction. Sessions persist an additive
binding v1 and re-admit it before provider dispatch after resume. Role
execution and overload-only failover now use the same inventory and binding
identities.

## Resolution contract

Legacy resolution records safe source metadata while keeping credential
values private. Precedence is field-specific:

| Field | High-to-low precedence |
|---|---|
| Provider | ranked candidates: explicit provider (rank 4), `PROV` (3), config (2); a provider-qualified or detected model carries its model source rank and can override a lower-ranked provider; if still unset, infer from the first provider-specific key, then the credential store |
| Model | explicit model (rank 4) → `PROV_MODEL` (3) → config (2) → alias expansion; a higher-ranked conflicting provider clears it to the selected-provider default |
| API key | explicit key → applicable `PROV_API_KEY` → provider-specific environment → config → credential store |
| Base URL | explicit URL → applicable `PROV_BASE_URL` → config → provider-specific environment → provider default |
| Max tokens | explicit value → config |
| Model aliases | configured aliases, then explicit aliases override by normalized name |

Generic `PROV_*` credential/URL values apply only when `PROV` is absent or
selects the route being resolved. Provider-qualified models and aliases
participate in conflict detection rather than forming one universal precedence
step.

Supported canonical adapters are Agentic Claude, OpenAI, Gemini, DeepSeek,
Qwen, and Ark. Public aliases normalize to those IDs.

## Trusted portfolio compilation

User settings are the only authority for `provider_accounts`,
`model_profiles`, metadata overrides, role bindings, failover definitions, and
the default `model_profile`. Project settings may select only a user profile
whose `project_selectable` flag is true. If a project file contains any
forbidden portfolio definition key, the loader removes the entire project
portfolio subset before typed decoding and emits one stable diagnostic naming
only the keys and source path. Unrelated project settings still merge
normally.

Portfolio account authentication is one of:

- `env`, naming the exact environment variable read during client
  construction;
- `credential`, naming an opaque `engine/auth` record; or
- `provider_default`, lowered to the existing concrete provider environment
  or credential-store source before cache lookup.

Plaintext `api_key`, arbitrary headers, unknown nested fields, unsafe URLs,
mixed profile/legacy selection in one source layer, and unauthorized project
selection fail validation. Account endpoints allow only absolute HTTP(S)
URLs without userinfo, query, or fragment and share one canonical form.

`PortfolioSnapshot` retains canonical accounts, profiles, per-field model
metadata provenance, effective role/failover definitions, a separate
explicit-role presence map, and a deterministic revision. The revision
excludes credentials, credential hashes, diagnostics, source paths,
timestamps, and map iteration order. Role execution consumes only explicit
optional-role presence. Each logical request resolves one detached failover
chain from the validated role policy without constructing alternate routes.

## Routing and fallback

- `NewConfiguredRuntime` is the shared CLI/ACP compiler/runtime boundary.
- `NewRuntime` remains the direct legacy compatibility constructor.
- `ResolveModel` resolves a model spec without network client creation.
- `PrepareModel` resolves and creates a route, used by CLI/ACP to fail fast for
  an explicitly configured legacy fallback.
- `routingChatModel` examines the per-call model option, resolves its provider,
  lazily obtains the client, rebinds current tool metadata, and forwards the
  provider-local model name.
- `RouteIdentity` contains provider, canonical endpoint, concrete auth
  kind/reference, and adapter digest. Profile ID, provider-local API model,
  resolved credential, and credential hash are excluded. Profiles sharing one
  route reuse a client while forwarding their own API model; endpoint,
  auth-reference, or adapter differences isolate clients.
- `runCanonicalModelRound` owns one logical-request coordinator. Same-route
  retry returns a typed overload result to that coordinator; the provider
  runtime owns candidate admission, construction, and route dispatch.
- `Runtime.InventorySnapshot` returns a detached, sorted view without
  constructing a client. Entries contain selectors, display/provider/API-model
  labels, effective metadata, reasoning default, and one-way route/metadata
  digests; account, endpoint, auth, client, and health state are absent.
- `Runtime.ResolveInventorySelector` gives exact normalized profile IDs
  precedence. Legacy resolution uses `legacy:<selector>` unless the runtime is
  itself legacy-bound; profile/legacy collisions therefore cannot silently
  switch namespaces.
- `Runtime.ResolveRoleCall` is side-effect-free. It returns one detached,
  non-secret snapshot for fixed `main`, `explore`, `plan`, `general`, or
  `summary` role identity, selected profile/provider/API model, revision and
  digests, known limits, admitted dynamic requirements, and exact applied
  effort without constructing a route.
- `QueryEngine.ChangeModel` resolves and validates a candidate before the
  active-turn lock, rechecks route generation and reasoning under that lock,
  durably checkpoints the binding, and only then mutates live state. A
  definite write failure leaves the prior binding and block unchanged. An
  uncertain write installs a process-local dispatch block that requires
  Session reload.
- P30.1c rich turns are admitted only for the selected main route. Because
  `SummaryModel` has no independent route/capability binding, proactive
  auto-compaction remains deterministic for that live turn instead of sending
  its ordered media to the summary route.
- P30.3 media recovery is separate from generic overload fallback. One exact
  logical round may retry the selected rich route once and then call at most
  one distinct fallback only after a fresh route resolution, `supported`
  capability result with non-empty provenance/current generation, and exact
  ordered current-modality admission. The engine freezes that binding and
  rechecks it immediately before the alternate call; generic fallback options
  are disabled during the recovery sequence.

The former fixture-only ADK provider wrapper was retired in P13.6b.
`runCanonicalModelRound`, its project-owned attempt coordinator,
`CallModelWithRetry`, and `routingChatModel` are the only query-kernel attempt,
retry, fallback, and route owners beneath the single live ProjectGraph kernel.

## Bounded overload failover

Every admitted model round freezes the current portfolio revision, fixed role,
primary, ordered alternates, dynamic requirements, projection policy,
cancellation, messages, system prompt, tool schemas, and shared budgets. A
new logical request always starts from the current primary. Same-route retry
keeps the same attempt and retry identity; an admitted different profile
creates the next monotonic attempt.

Before `ResolveFailoverChain`, the canonical round derives one provider-neutral
input-fit estimate from that immutable request. Normalized messages and the
system prompt use the existing compact message heuristic; a non-empty tool list
uses its complete detached JSON representation, including serializable
`ToolInfo.Extra`. Additions saturate at the platform integer maximum rather
than wrapping. The estimate is context admission only, not provider billing or
an output-token reservation.

Only typed `overloaded` may switch profiles. 429 remains same-route retry.
Transport, timeout, authentication, authorization, invalid request,
policy/content rejection, cancellation, deadline, context overflow,
conversion, protocol, persistence, primary route construction, ambiguous
usage, and unknown failures cannot switch. Capability, modality, reasoning, context,
duplicate/current-profile, and route-construction candidate rejection emits a
bounded skip before dispatch and consumes no provider call, switch, or attempt.

One provider-call count, switch count, and absolute deadline span all retries
and routes. Each actual dispatch consumes exactly one call only after tool
binding, effort lowering, usage admission, and immutable-request restoration
succeed. A constructable alternate is confirmed before visible TUI output is
retracted and before a switch is consumed.

Once that alternate is constructable, the failed attempt emits `discarded`
with typed overload and `never_started` or `discarded` output disposition. An
exact tombstone follows only for retractable TUI output; the admitted next
attempt then emits `started` before its provider dispatch. A switched attempt
does not also emit `failed`, which remains terminal for an attempt that cannot
continue.

Before a different profile receives history, the coordinator removes legacy
reasoning, structured reasoning/signature parts, and message-level provider
metadata from an attempt-local clone. Canonical public text/tool history and
the private source history remain unchanged.

Failed output remains attempt-local. Provider usage is still settled and
attributed to the logical request, attempt, profile, and retry; ambiguity is
terminal. The deferred model-round collector cannot execute a tool. Only a
completely classified successful attempt enters canonical assistant/tool
history.

TUI attaches assistant, thinking, and uncommitted tool projection to the exact
attempt, removes only a tombstoned attempt, and shows one bounded warning for a
later `started` attempt. Plain and Headless write the same safe notice only to
stderr; ACP uses `_session/status`; library callers retain typed events without
a forced writer. Notices contain only the normalized fallback profile and
switch count and never enter canonical assistant history, transcripts, or
structured headless output. Plain/headless, ACP, and default library consumers
remain non-retractable after visible assistant output. Attempt facts remain
process-local runtime events and add no Session schema. Standalone MCP has no
model runtime.

Legacy `fallback_model` compiles into this same policy with one alternate, one
switch, six provider calls, and a 45-second deadline. There is no hidden
last-success route, adaptive health, Retry-After cooldown, or background probe.
Eino v0.9.13 provider models remain dispatch leaves; its failover wrapper is
not an owner.

## Agent-role routing

The root call uses its admitted P29.2 `main` binding. Explore, Plan, and other
Agents map to `explore`, `plan`, and `general`. An explicitly configured
optional role wins; otherwise Explore/Plan may consume a truthful trusted
`SubagentModel` injection, and the remaining path dynamically inherits the
current durable main binding. Project Agent-definition text and model-produced
tool arguments are never route authority.

Child role resolution finishes after `AgentRunner` assigns exact identity and
worktree scope but before durable execution admission. The initial Session
writes the original Agent name/type, fixed `model_role`, and exact binding v1
before executor entry. Record and Execute share one process-local frozen call.
Resume re-admits the persisted binding and validates the fixed role instead of
re-running current role policy. An old child without role/binding retains
legacy parent inheritance and upgrades only on its next admitted execution.

Only an enabled root best-effort tool-use summary consumes `summary`. Child
summaries remain disabled. Auto/manual compaction, long-session memory and
dream, WebFetch, permission classifiers/explainers, callback progress
summaries, and the P22 reviewer retain their existing model owners. Role
routing changes no ProjectGraph, retry, stream/tool commitment, permission,
worktree, cancellation, transcript, or child lifecycle owner.

## Separate permission reviewer route

P22.2a adds an independent provider factory for the opt-in permission-review
shadow. [`NewApprovalReviewer`](../../../engine/provider/reviewer.go) requires
an explicit provider, model, and positive timeout. It creates a dedicated
client and one bounded generation call; it never reuses the actor
`provider.Runtime`, falls back to the actor model, or performs implicit
cross-provider routing.

The reviewer route deliberately ignores generic actor `PROV`,
`PROV_MODEL`, `PROV_API_KEY`, and `PROV_BASE_URL` values. An explicit reviewer
credential or the selected provider's own environment/credential source may
still construct that route. Initialization failures redact the exact configured
API key and base URL.

The factory accepts only the versioned, data-minimized permission-review
request and returns a strict bounded JSON result under one absolute deadline.
QueryEngine owns request/action/policy binding and every permission outcome;
the provider factory cannot grant, prompt, persist, or dispatch. CLI and ACP
construct this route only when their shadow flag is set. The independent
standalone MCP server constructs no reviewer.

## Provider-specific adapters

Provider adapters remain intentionally distinct. Even when endpoints are
OpenAI-compatible, credential variables, defaults, request metadata, thinking
fields, tool-call chunks, and response conversion differ. `agenticChatModel`
normalizes the Eino AgenticModel boundary without erasing provider construction
or metadata semantics.

DeepSeek uses the project-owned
[`agenticdeepseek`](../../../engine/provider/agenticdeepseek/model.go) Eino adapter. It
posts directly to DeepSeek's
[Responses API](https://api-docs.deepseek.com/zh-cn/guides/responses_api/) with project-owned request,
response, typed-error, and semantic-SSE types; it does not route through the
OpenAI SDK or the former Eino OpenAI Chat Completions ACL. Responses calls are
stateless, so the adapter sends the complete admitted history on every call and
does not use `previous_response_id` or `store`. A stream succeeds only after a
monotonically ordered `response.completed` or `response.incomplete` terminal
event. `response.failed`, malformed events, a truncated stream, or the legacy
`data: [DONE]` marker fail the attempt.

[`deepseek-v4-flash-vision-exp`](https://api-docs.deepseek.com/zh-cn/news/news260821/)
is the exact image-capable DeepSeek model. Following the official
[vision contract](https://api-docs.deepseek.com/zh-cn/guides/vision/), the
adapter preserves mixed text/image order and accepts HTTP(S), supported base64
data URLs, and DeepSeek Files API `file_id` references in user input; Responses
tool outputs may also carry images. Image input fails locally for every other
DeepSeek model instead of relying on the provider's placeholder or downgrade
behavior. Request size, per-image inline size, image count, URL scheme/length,
MIME type, detail, and file-ID shape are bounded before dispatch. The classic
YHC message bridge currently supplies URL and base64 images; direct Eino callers
can use the adapter's typed file-ID block constructors.

The same package exposes a separate project-owned DeepSeek
[`FilesClient`](../../../engine/provider/agenticdeepseek/files.go) for image
resource lifecycle. It appends `/files` to the validated API root and supports
bounded `user_data` upload, cursor listing, metadata retrieval, and deletion.
Uploads require the exact reader size, reject files above 64 MiB before
dispatch, and optionally set a creation-anchored lifetime from one hour to 30
days. This resource client shares typed, bounded, redacted API and transport
failures with the Responses adapter but does not become conversation or Session
state; callers remain responsible for deleting files they no longer need.

`engine/auth` supplies provider-default credentials and exact named
credentials only at client construction. `engine/model` owns model aliases,
context-window and deprecation metadata, profile override validation,
per-field provenance, and exact provider effort support. Provider routing and
the engine role resolver consume that policy but do not redefine it.

The project-owned `agenticChatModel` bridge also preserves terminal-only Eino
chunks. It extracts Claude stop reasons, OpenAI and Ark response
status/incomplete details, Gemini finish reasons, and DeepSeek/Qwen extension
finish reasons into the legacy `schema.ResponseMeta.FinishReason`. The query
execution layer classifies those raw values centrally; the bridge does not
choose tool disposition or alter provider routes.

## Model request capability layers

Model interaction has three separate owners. This follows the useful common
shape in the local Pi and DeepSeek Harness references without copying either
runtime wholesale:

1. QueryEngine and Session own model-neutral request intent, such as the
   canonical reasoning effort selected by the user. They do not construct SDK
   options or provider JSON.
2. Exact-model metadata owns which values may be offered. Built-in facts are
   derived only when the catalog model and selected adapter agree; a custom
   profile may override the field with explicit provenance. CLI and ACP read
   this admitted set instead of maintaining their own enums.
3. The adapter policy validates the canonical value and lowers it into a
   provider dialect immediately before dispatch. The resulting SDK option or
   wire fields are attempt-local and are recomputed for every failover
   candidate.

This split prevents a model catalog entry from promising a feature that its
selected adapter cannot serialize. It also prevents an adapter-wide feature
from being advertised for an exact model whose capability remains unknown.
Configuration compilation rejects explicit metadata or defaults that the
adapter cannot lower. Runtime admission intersects exact-model metadata with
the ordered adapter vocabulary once more, so model switches, Session resume,
roles, failover, `/effort`, and ACP use the same decision.

The durable semantic identifier accepts a bounded adapter-owned name rather
than one project-global feature enum. Adding a new value therefore requires an
adapter policy and authoritative model metadata, but not changes to Session,
commands, or ACP schemas. Arbitrary unvalidated provider JSON is deliberately
not a configuration surface: new model features should extend the typed
semantic request and adapter lowering layers while keeping credentials,
headers, retry, and failover authority outside model profiles.

## Classic-to-Agentic input bridge

The wider runtime currently owns classic `schema.Message` values.
`agenticChatModel` converts them to Eino `schema.AgenticMessage` values only at
the selected provider boundary. System, assistant, tool-result, reasoning,
tool-call, response-metadata, and provider-option conversion have focused
coverage.

The user-input bridge is lossless for the classic input shapes it accepts.
An empty `UserInputMultiContent` produces the existing single text block from
`Message.Content`. A non-empty multipart value becomes the sole ordered source,
so the bridge neither appends `Content` again nor duplicates a prompt created by
`newUserMessage`.

| Classic input part | Agentic content block | Preserved fields |
|---|---|---|
| Text | `UserInputText` | Exact text, including an empty string |
| Image | `UserInputImage` | Exactly one URL or base64 source, MIME type, and detail |
| Audio | `UserInputAudio` | Exactly one URL or base64 source and MIME type |
| Video | `UserInputVideo` | Exactly one URL or base64 source and MIME type |
| File, including PDF | `UserInputFile` | Exactly one URL or base64 source, MIME type, and optional name |

Every multipart part is converted in message and part order before request
options are normalized or the inner `AgenticModel` is called. Nil messages,
nil or mismatched typed payloads, unknown or unsupported user-part types,
ambiguous/missing media sources, and base64 media without a MIME type return
an `AgenticInputConversionError`. `Generate` and `Stream` make zero inner-model
calls for a rejected request, so local conversion failures cannot trigger a
provider retry or fallback with a reduced payload. Existing non-user
conversion remains unchanged.

Agentic OpenAI private continuation is narrower than general
classic-to-Agentic shape conversion. The public `Message.Extra` marker remains
untrusted. Instead, a successfully completed canonical assistant response may
receive one transcript-private `assistant-origin-binding/v1` sidecar. It binds
the physical entry/version, message index, internal logical assistant ID, and
canonical complete Message SHA-256 to the actual provider, account, API family,
API model, route digest, and opaque credential origin used by that dispatch.
Malformed, partial, legacy, or mismatched sidecars keep the Session readable
but are ineligible.

Every dispatch re-resolves its credential before cache reuse. The route
registry uses a monotonic resolution attempt and an account-level current
publication so an older credential, auth source, endpoint, or RouteIdentity
cannot be republished after a newer account resolution succeeds. Published
clients are immutable. Immediately before conversion, the router verifies the
captured identity publication and account publication, then issues an internal
client-bound one-attempt proof. Only a verified sidecar whose complete origin
matches that current dispatch receives a non-nil typed
`AgenticResponseMeta.OpenAIExtension` marker on the cloned Agentic message.
Caller or persisted `Message.Extra` cannot mint that marker. Every other
Agentic OpenAI path strips legacy and structured reasoning/signature state
before transport. Other providers keep their prior input behavior.

Origin persistence occurs only after complete Stream aggregation or successful
Generate output and before the canonical turn can claim durable settlement.
Failed, cancelled, partial, or persistence-failed output mints no origin. Exact
load, branch, rewrite, repair, and Resume paths rebind from a verified physical
source; summaries and changed payloads cannot inherit. The sidecar, origin
fields, marker, reasoning, and signature are excluded from durable public
entries, Session export, ACP New/Load/Resume wire output, TUI, Plain, task, and
Agent projections. Rejections expose only bounded Generate/Stream counts and a
stable reason code. Delivery evidence and rollback scope are in
[`P38.0 Provider Reasoning Origin`](../../migration/history/runtime/p38-0-provider-reasoning-origin.md).

The bridge deliberately excludes arbitrary `Message.Extra`,
`MessageInputPart.Extra`, deprecated media `Extra`, and project-local path
metadata from provider-visible blocks. Its formatted errors contain only
bounded indexes and a stable reason code; the typed error retains role and part
type for programmatic handling without formatting user-controlled values.

`SubmitMessageWithImages`, the source-compatible rich
`EnqueueUserInput` wrapper, and ordered `UntrustedPromptInput` admission share
one strict engine-owned image validation boundary. New generic
`RuntimeInputCoordinator` enqueue calls do not accept inline image payloads;
the coordinator reads that shape only for legacy restart compatibility. The
validator accepts at most 20
PNG/JPEG/WebP/single-frame GIF images, 5 MiB decoded bytes each, 10 MiB per
prompt, and 25,000,000 pixels each. Base64 must be canonical and whitespace
free; declared MIME must normalize to the detected format; format structure
must end exactly; and both bounded configuration and complete image decode
must succeed. SVG, animated GIF/WebP, HEIC/HEIF, TIFF, malformed, truncated,
mismatched, trailing-payload, and over-limit input fail with an index plus
stable redacted reason code.

Direct rejection occurs synchronously before prompt hooks, turn-start/model
events, transcript/history mutation, or model dispatch and returns
`TerminalImageError` plus one terminal event. Durable rejection occurs before
ledger mutation through typed admission, and recovery rejects invalid
persisted legacy image content rather than retrying it indefinitely. Direct
submission first copies the caller slice, so later mutation cannot change the
admitted request.

`UserImage.Name` and `UserImage.Path` remain source-compatible entrypoint
fields but are removed before durable queue storage and never enter Eino part
`Extra`, the transcript, provider input, or formatted errors. Once admitted,
`newUserMessage` preserves complete-text-then-images order, exact base64 bytes,
and normalized MIME without a silent skip.

`QueryEngine.SubmitPromptInput` is the separate version-1 immediate ordered
boundary. Callers construct an immutable closed union with
`NewPromptTextPart` and `NewPromptImagePart`; the engine preserves the exact
text/image interleaving and image detail. The direct API is literal and never
dispatches slash commands or parses `@model` overrides. It is not the durable
runtime-input shape.

For any ordered prompt containing an image, admission first reuses the strict
generic validator, then resolves the exact selected provider route through
`ModelResolver`. A named profile uses its own effective image metadata and
provenance; `unknown` is not support and cannot fall through to a static
registry answer. A legacy route retains the explicit
`PromptCapabilityResolver` path. Missing or incomplete route facts, absent
capability ownership, provider mismatch, `unsupported`, and `unknown` all fail
before the user-prompt Hook or durable/model-visible mutation.

Provider-facing admitted images live in a cryptographically random turn-local
media store. Opaque `MediaRef` values bind store generation, ordered part
identity, normalized MIME/detail, selected provider/model, engine route
generation, and capability source. For a saved Session, P30.2a additionally
copies the accepted bytes into the Session-private durable MediaStore and
flushes one ref-backed transcript prompt before event or provider entry. That
durable ref never becomes a caller-supplied or provider-facing identity. The
Hook sees only the concatenated text parts. Its non-identical rewrite is
accepted only for exactly one text part, so it cannot silently flatten or
reorder a rich prompt.

The binding is rechecked immediately before every rich model call. Model or
Plan phase changes, Session activation, and close advance the route
generation. A configured or provider-requested fallback cannot cross the
binding: the engine returns `TerminalPromptInputError` before publishing a
fallback transition or calling another route. Media stores zero decoded bytes
and invalidate refs on every terminal and close path.

P30.1c closeout evidence is retained in
[`Ordered Prompt And Selected-Route Admission`](../../migration/history/runtime/p30-1c-ordered-prompt-admission.md).
The additive durable immediate-turn boundary is retained in
[`P30.2a Durable Media Store`](../../migration/history/runtime/p30-2a-durable-media-store.md).
The final writer split and decode-only compatibility boundary are retained in
[`P30.6 Multimodal Program Closeout`](../../migration/history/runtime/p30-6-multimodal-program-closeout.md).

P25.1 delivered this bounded `adapt` decision without changing ProjectGraph,
classic message/transcript ownership, provider routing, retry policy, or a
durable schema. Closeout evidence is retained in
[`P25 Agentic Provider Input Fidelity`](../../migration/history/runtime/p25-agentic-provider-input-fidelity.md).

## Reasoning effort

Reasoning effort is a provider request capability, not the local continuation
`TokenBudget`. Admission requires the selected profile metadata to list the
exact value and the selected adapter to support exact lowering:

| Adapter | Explicit values | Lowering |
|---|---|---|
| Agentic Claude | `low`, `medium`, `high`, `xhigh`, `max` | `output_config.effort` |
| Agentic OpenAI Responses | `none`, `minimal`, `low`, `medium`, `high`, `xhigh` | typed Responses reasoning |
| Agentic Ark Responses | `minimal`, `low`, `medium`, `high` | typed Ark reasoning |
| Agentic Gemini | `low`, `high` | typed Gemini thinking level |
| Agentic DeepSeek V4 Pro/Flash/Vision Exp | `none`, `high`, `max` | typed DeepSeek Responses `reasoning.effort` |
| Agentic Qwen | none | provider default only |

For DeepSeek V4, all three explicit values are emitted unchanged as Responses
`reasoning.effort`; the old Chat Completions `thinking` and
`reasoning_effort` fields are never sent. Compatibility aliases such as `low`,
`medium`, or `xhigh` are rejected instead of silently mapped. Empty effort
emits no provider option. A profile default applies when the Session/call has
no override, and `/effort default` restores that profile default or provider
default.

An unsupported or unknown value fails before provider-usage admission and
dispatch; no level is guessed, clamped, or converted to a boolean. Manually
switching to or resuming an incompatible model clears the prior effort visibly
rather than guessing a replacement. The exact applied effort enters binding
v1, role snapshots, call options, and the bounded provider-usage descriptor.
One in-flight failover request instead preserves its frozen reasoning intent:
an alternate that cannot lower that exact effort is skipped before dispatch
rather than receiving a cleared or guessed value.

## Durable main-route binding and recovery

`SessionMetadataFull.model_binding` is an optional, independently versioned
record. Version 1 stores logical kind/value, resolved provider/API model,
portfolio revision, route-identity and metadata digests, known context/output
limits, and an applied reasoning effort. It stores no account, endpoint,
credential, header, client, or route-health state.

The nested decoder is strict for v1. Invalid and unknown-version valid JSON is
retained opaquely so automatic checkpoints and forks preserve it without
making the enclosing Session unreadable. Such records remain inert:
listing/export expose only `invalid` or `unsupported_version`, and resume
installs a fail-closed provider-dispatch block. Valid projections reveal only
state, kind, and logical value.

Resume resolves the current selector before activating live state. Missing
profiles and provider/API-model or route-identity drift require explicit
rebind. Compatible portfolio, metadata, or output-limit changes produce
bounded warnings; incompatible required metadata blocks dispatch. A smaller
known context installs a context-only block. The existing compaction owner may
clear that block only after it durably checkpoints a fitting history; identity
blocks never compact away. Unsupported persisted reasoning is cleared with a
warning rather than replaced by a guessed value.

Every provider attempt rechecks the active dispatch guard. Active forks sample
the latest live binding under the engine lock; durable branch/fork copies
opaque records exactly.

## Diagnostic read boundary

`QueryEngine.DiagnosticsSnapshot` calls the injected `ModelResolver` without
constructing a client or making a network request. It consumes the same
`ResolvedConfig` and `ResolutionSources` as execution, but retains only safe
facts: provider/model, per-field source, credential presence, and an endpoint
reduced to scheme plus host. Credential values, suffixes, URL userinfo, paths,
queries, and fragments do not enter the returned snapshot.

`model.KnownContextWindow` reports a limit only for an explicit context suffix,
an exact/alias table match, or the existing longest model-pattern match. The
unknown-model default used by execution heuristics is never presented as a
diagnostic fact. Provider connectivity remains a stable skipped doctor check;
the explicit startup preflight owns network/auth probing.

## Invariants and edge cases

- A fallback must not resolve to the same provider and model as the main route.
- Route creation is synchronized and cached per complete non-secret
  `RouteIdentity`. Tool bindings are cloned and rebound on the selected route.
- Named credentials are resolved only when a route needs construction; unused
  profiles do not read their environment or credential records.
- Project portfolio definitions never select an endpoint, credential
  destination, metadata override, role, or failover policy.
- Alternate-provider resolution clears generic provider environment values so
  the main provider cannot accidentally capture the fallback route.
- The permission reviewer is an explicit separate route: generic actor
  `PROV_*` values and actor fallback cannot select or credential it.
- Optional preflight runs before client creation for a route and must return
  actionable, redacted diagnostics.
- CLI TUI/plain/headless and ACP use the same runtime policy. The independent MCP
  server does not create a model runtime.
- Metadata-only terminal chunks must reach the query stream classifier; an
  adapter may not discard a truncation merely because it contains no content
  block.
- A non-empty user multipart value is authoritative at the Agentic boundary;
  `Message.Content` is not appended again.
- Invalid user multipart input fails before option normalization and the inner
  provider call; arbitrary project metadata is not forwarded.
- Direct and durable public-image admission use the same strict decoded
  format/resource predicate before model, transcript, or ledger mutation.
- Caller image names and local paths never enter durable or model-visible
  prompt metadata.
- Immediate ordered rich input requires an exact supported selected route and
  a live generation-bound media store before Hook or model entry.
- Rich input cannot fall back to a different route; text-only fallback remains
  unchanged.
- Delta versus cumulative streamed ToolCall arguments are provider contracts,
  not a heuristic. Provider adapters and `ProcessStream` must preserve the
  declared mode rather than silently corrupting JSON.
- Runtime model controls resolve before mutation and serialize against an
  active engine turn; an external control cannot silently replace the route
  owned by that turn.
- Every provider attempt passes the canonical model-binding dispatch guard.
  Identity blocks require an explicit rebind or reload; only a context-only
  block can enter compaction.
- Every logical failover request starts from its current primary. Candidate
  admission and construction skips consume no provider call, switch, or
  attempt; a new profile consumes one switch only after construction succeeds.
- Failed attempt output, reasoning, and uncommitted tool projection never
  enter canonical history. TUI retracts only the exact attempt; other
  entrypoints cannot switch after visible output commitment.
- Provider usage ambiguity, cancellation, deadline, and shared budget
  exhaustion are terminal and cannot open a new attempt.
- An uncertain model-binding checkpoint blocks provider dispatch, compaction,
  and another model switch until Session reload establishes the durable fact.
- Listing and export reveal only binding state, kind, and logical value; they
  never reveal binding digests or opaque nested JSON.

## Code references

| Boundary | Code reference | Why it matters |
|---|---|---|
| Legacy provider resolution | [`ResolveConfig`](../../../engine/provider/resolver.go) | Owns explicit, environment, settings, credential-store, and provider-default precedence for legacy routes. |
| Portfolio compilation | [`CompilePortfolio`](../../../engine/config/portfolio.go) | Validates account/profile authority, metadata, roles, failover policy, and the immutable non-secret snapshot. |
| Request capability policy | [`ResolveAdapterReasoningEffort`](../../../engine/model/reasoning_effort.go) | Separates canonical request intent, exact-model defaults, ordered adapter support, and provider wire dialects. |
| Request lowering | [`buildProviderEffortOption`](../../../engine/execution/call.go) | Produces the final typed provider SDK option immediately before provider admission and dispatch. |
| Named credentials | [`ResolveNamedCredential`](../../../engine/auth/auth.go) | Resolves opaque user-owned credential references only when a selected route is constructed. |
| Configured runtime | [`NewConfiguredRuntime`](../../../engine/provider/configured_runtime.go) | Joins source-aware configuration with the single shared CLI/ACP provider runtime. |
| Client isolation | [`NewRouteIdentity`](../../../engine/provider/route_identity.go) | Defines the complete non-secret identity used to isolate and reuse provider clients. |
| Role and failover admission | [`Runtime.ResolveFailoverChain`](../../../engine/provider/role_resolver.go) | Freezes the primary, ordered alternates, capability/context decisions, and shared limits without constructing routes. |
| Logical attempts | [`newModelAttemptCoordinator`](../../../engine/model_failover.go) | Owns complete-request candidate admission, candidate order, attempt identity, switch commitment, and bounded attempt events. |
| Canonical dispatch | [`runCanonicalModelRound`](../../../engine/model_round.go) | Freezes model-visible inputs and owns retry, switching, stream classification, and final commit. |
| Same-route retry | [`CallModelWithRetry`](../../../engine/execution/retry.go) | Keeps 429 and overload retry on one profile under the shared provider-call/deadline budget. |
| Durable model changes | [`QueryEngine.ChangeModel`](../../../engine/execution_controls.go) | Serializes admission and checkpoint commit before changing the live main binding. |
| Dynamic effort controls | [`QueryEngine.ReasoningEffortOptions`](../../../engine/execution_controls.go) | Projects the same exact model/adapter intersection to commands and ACP. |
| Safe diagnostics | [`QueryEngine.DiagnosticsSnapshot`](../../../engine/diagnostics.go) | Exposes bounded runtime facts without credentials, endpoints, or opaque binding data. |
| CLI composition | [`NewConfiguredRuntime` call](../../../cmd/yhc/cmd/root.go) | Proves the command runtime consumes the same compiled portfolio owner. |
| ACP composition | [`NewConfiguredRuntime` call](../../../server/acp/agent.go) | Proves ACP Session construction uses the shared provider/runtime policy. |

## Related tracking

Future provider/API choices belong in [`PLAN.md`](../../migration/PLAN.md);
current gaps and reference evidence belong in
[`REMAINING.md`](../../migration/REMAINING.md) and
[`migration/reference/`](../../migration/reference/README.md).
The ordered, durable, recovery-safe, and cross-entrypoint multimodal program is
retained as architecture history in
[`P30 Cross-Entrypoint Multimodal Input`](../../migration/plans/p30-cross-entrypoint-multimodal-input.md).
The current complete-footprint admission and observable failed-attempt disposal
were delivered by
[`P46.1`](../../migration/history/runtime/p46-1-complete-prompt-footprint.md)
and [`P46.2`](../../migration/history/runtime/p46-2-observable-failover.md).
