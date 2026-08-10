# P38 Provider Reasoning Origin

**Status:** historical
**Slice:** P38.0 complete
**Adoption:** `adapt`
**Gap:** G34
**Completed:** 2026-08-02

> **Ownership:** historical implementation contract for same-origin private
> provider continuation. Comparative evidence is in
> [`provider-reasoning-origin-audit.md`](../reference/runtime/provider-reasoning-origin-audit.md).
> Current behavior belongs in
> [`model-providers.md`](../../architecture/platform/model-providers.md), and
> delivery evidence is in
> [`p38-0-provider-reasoning-origin.md`](../history/runtime/p38-0-provider-reasoning-origin.md).

## Problem

The durable assistant message retains Agentic OpenAI reasoning text and its
encrypted signature, but the next Responses request cannot prove that the
restored block came from the exact route and credential now selected. The leaf
adapter therefore treats it as non-self-generated and omits it. Public text and
tool history continue, but same-route private continuation is lost.

Blindly restoring the adapter's boolean marker would be worse. A caller,
legacy transcript, another provider, another account, or a rotated credential
could cause private provider data to cross a trust boundary. The required
outcome is useful same-origin continuation with conservative stripping for
every absent, stale, or mismatched identity.

## Accepted owner and compatibility

P38 uses `adapt`:

- preserve Eino-ext Agentic OpenAI's self-generated-only request conversion;
- preserve the existing provider router as the actual dispatch-route owner;
- preserve transcript and Session replay as the durable message owners;
- preserve QueryEngine's canonical round and alternate-attempt stripping;
- preserve ACP as a public-only projection; and
- add one private, typed origin sidecar plus one route-owned exact decision.

The user-visible compatibility change is narrow. A restored conversation sent
through the exact same origin may include its existing encrypted reasoning item
in the next Agentic OpenAI Responses request. Public assistant text, tool calls,
permission behavior, routing choice, failover order, transcript display, ACP
wire output, and non-Agentic providers remain unchanged.

## Scope

P38.0 is one implementation PR covering:

- `engine/provider`: actual route-origin construction, publication fencing,
  shared Generate/Stream preparation, and attempt-local adapter trust;
- `engine/transcript` and `engine/session`: optional private origin persistence,
  clone/reload validation, fork/recovery handling, and public exclusion;
- `engine` canonical model round: atomically associate a completed assistant
  message with the actual dispatched origin and pass restored private bindings
  into the next route decision;
- existing failover/recovery code: strip before every ineligible transport
  attempt without changing canonical history; and
- ACP/export tests: prove that no new private record reaches a public
  projection.

P38.0 does not add provider switching, new models, a generic metadata replay
framework, cross-provider reasoning translation, a public origin field, API
response storage, new compaction behavior, a second transcript, or an ACP
protocol extension. It does not make reasoning text public or treat a provider
summary as a user-visible answer.

## Durable origin schema

The implementation adds version 1 of one project-owned non-secret record:

| Field | Meaning | Rejection rule |
|---|---|---|
| `version` | Exact schema version, initially `1` | Missing, zero, unsupported, or malformed is legacy-unverified |
| `provider` | Canonical provider ID | Exact mismatch strips |
| `account_id` | Canonical configured account ID used to resolve the client | Profile label or missing account is not proof |
| `api_family` | Canonical adapter protocol, initially `openai-responses/v1` for the accepted path | Exact mismatch strips |
| `api_model` | Exact model string sent to the provider | Alias/profile equality is insufficient |
| `route_identity_digest` | Existing canonical non-secret client-route digest | Missing or mismatch is stale route evidence |
| `credential_origin_id` | Opaque rotation-sensitive ID from the credential resolver | Missing, changed, or unavailable strips |

The record is attached through an optional transcript-private sidecar written
inside the same physical record as the assistant message. Its binding key is
the conjunction of:

- persisted `transcript.MessageEntryIdentity` record version/ID and message
  index;
- the internally assigned assistant logical `message_id`; and
- a canonical digest of the exact persisted assistant message payload.

Logical `message_id` alone is not authority because it lives in
`schema.Message.Extra` and can be supplied or replaced independently. The
sidecar is not stored in message/content-block `Extra`, tool metadata, response
metadata, or an ACP-visible field. Duplicate IDs, an origin outside its
physical entry, an index/logical-ID/payload-digest mismatch, an assistant
without a valid logical ID, and one message with conflicting origins are
invalid for reuse and do not make the Session unreadable.

The binding has its own `assistant-origin-binding/v1` codec. Its payload digest
is lowercase hexadecimal SHA-256 over the complete `schema.Message` JSON value
after the internal logical ID is assigned and before the containing record is
written. The optional origin sidecar is outside those bytes. The implementation
must use one private versioned encoder, include every Message JSON field, sort
map keys deterministically, reject values it cannot encode canonically, and
freeze golden bytes for populated legacy and current fields. The digest is an
association/integrity check, not authentication and not credential identity;
it cannot replace the private origin fields or their resolver.

Legacy interactions have no sidecar and remain readable. Unknown or malformed
nested origin records follow the existing fail-closed optional-record pattern:
the enclosing transcript loads, the private record is not projected, and the
message is ineligible. No backfill or heuristic migration is permitted.

### Credential-origin contract

`credential_origin_id` is supplied by the credential-resolution owner. It must:

- remain stable across restart for the same locally resolved credential;
- change when the credential rotates, even when account and environment name
  remain the same;
- disclose neither the credential nor an unkeyed/raw credential hash;
- be unusable as an authentication value; and
- become unavailable after machine/key-store movement when equivalence cannot
  be proved.

A named credential store may use a persistent opaque record ID plus revision.
An environment or explicit secret source may use a keyed, installation-local
opaque fingerprint only when its key lifecycle is owned and tested. If the
resolver cannot meet this contract, it returns no origin ID and P38 strips the
private block. Provider name, environment-variable name, auth reference, or
profile ID alone never substitutes for this field.

## Route publication and dispatch identity

`routeRegistry` is the sole publication owner. It publishes immutable route
objects containing the actual model client, provider/account/API-family/route
identity, credential-origin ID, and a monotonically increasing publication.
The exact request API model is added when the attempt selects its model option.
A published object and its client are never mutated or retargeted; replacement
allocates a new object.

Every dispatch re-runs the applicable credential resolver even when the
current client cache has an entry. The resolver returns the secret for client
construction plus the opaque credential-origin ID. Under the registry's
existing serialization boundary, the registry publishes a new client and
increments the revision on:

- first construction;
- credential-origin change;
- account, route digest, endpoint/auth/adapter, or API-family change;
- explicit route invalidation/configuration reload; or
- client reconstruction after an owned failure.

A cache hit may reuse the immutable client only after current credential origin
has been resolved and matches. Missing credential-origin identity may still
use the provider for ordinary model calls, but the published route is marked
ineligible for private continuation. The resolver contract requires every
secret rotation to change the origin ID; a resolver that cannot meet that rule
cannot opt in.

`routingChatModel.prepareRoute` returns one immutable attempt-local snapshot
with the selected client, the same seven durable fields, the exact API-model
option, and `route_publication`. Generate and Stream share this helper.

Immediately before classic-to-Agentic conversion, the router reacquires the
registry boundary and compares the captured immutable object/revision with the
current publication. This check and issuance of an internal one-attempt trust
proof are the linearization point. If replacement won before that point, the
attempt strips private state. If the proof won first, later publication cannot
mutate or retarget the already captured client; the proof remains bound only to
that client and attempt. It cannot be reused by fallback or another call.

The trusted decision travels through an internal typed path owned by the
router and Agentic wrapper. It is not inferred from message payload and cannot
be supplied through `Message.Extra`, model options exposed to callers, response
metadata, transcript JSON, ACP input, or provider output.

## Lifecycle

### Successful response

1. The router freezes the actual account, API family, API model, route digest,
   credential origin, and publication together with the client.
2. Generate output or complete Stream aggregation produces the canonical
   assistant message. Partial/error-only output cannot mint a reusable origin.
3. The canonical model-round owner associates the exact assistant logical
   `message_id` with the frozen origin before durable record settlement.
4. Transcript persistence allocates the physical entry identity, computes the
   canonical assistant payload digest, and writes message plus optional
   origin binding in one record settlement. A crash that leaves either side
   absent is safe: later reuse rejects the incomplete pair.
5. The adapter's response-local `openai-generated` value is never copied into
   durable authority. The actual route snapshot, not the response, proves
   origin.

### Next request

1. Session/transcript restore validates the private sidecar and binds it to the
   exact assistant logical ID without placing it in the public message.
2. The actual route is selected for this attempt. Manual selection and bounded
   failover each produce their own dispatch snapshot.
3. For every assistant message with private reasoning/signature, the router
   compares all durable fields and verifies recovery consistency.
4. Immediately before leaf conversion, it rechecks route publication.
5. Exact matches receive an attempt-local trusted self-generated decision.
   The Agentic wrapper may then create the leaf adapter marker on the cloned
   Agentic message only.
6. Any rejection removes legacy reasoning, every structured reasoning part and
   signature, and message-level private metadata from the attempt-local clone
   before transport. Public text/tool parts and canonical history stay
   unchanged.

### Recovery, fork, compact, and export

- Normal restart validates physical entry identity, message index, logical ID,
  and payload digest before exposing a private association. Session fork or an
  exact record rewrite may allocate a new physical identity and atomically
  rebind the sidecar only while the logical ID, payload digest, and source
  origin remain exact; otherwise it drops the association.
- Import or machine movement does not prove credential equivalence. Missing
  local origin material rejects reuse.
- Compaction may retain an untouched assistant message and atomically rebind
  its matching sidecar or drop both. A synthesized/edited summary or any
  changed payload cannot inherit the source origin.
- Recovery that changes model binding, route digest, or origin association
  rejects reuse without rewriting the original transcript.
- ACP load/replay, Session export, diagnostics, TUI/Plain history, and task or
  Agent projections omit the sidecar and all private fields.

## Frozen decision and diagnostics

All seven durable fields must equal the current dispatch fields. Recovery must
be valid and the captured publication must still be current. This is the sole
positive path.

| Reason code | Condition |
|---|---|
| `origin_absent` | No origin sidecar is bound to the assistant logical ID |
| `origin_legacy_unverified` | Legacy, malformed, unsupported, duplicate, or conflicting origin record |
| `origin_provider_mismatch` | Canonical provider differs |
| `origin_account_mismatch` | Configured account differs, including account failover |
| `origin_api_family_mismatch` | Adapter protocol differs |
| `origin_api_model_mismatch` | Exact API model differs, including manual switch |
| `origin_credential_mismatch` | Credential origin changed or is unavailable |
| `origin_route_stale` | Route digest differs, publication is missing, or publication changed after capture |
| `origin_recovery_mismatch` | Recovered binding/message/origin association is inconsistent |

Diagnostics may emit only one of these codes plus bounded counts and the
Generate/Stream path label. They exclude account/profile/reference names,
provider endpoints, API model text, digests, credential-origin IDs, signatures,
reasoning, prompts, tool inputs, and transcript content.

## Frozen invariants

- Exact origin is conjunctive. Physical entry/index, logical ID, payload digest,
  and all origin fields must match; no single field, payload shape, or adapter
  marker authorizes reuse.
- The actual dispatch owner mints origin; model output and persisted metadata
  cannot mint it.
- Credential rotation and route publication races fail closed before
  transport.
- Each fallback attempt is evaluated independently. A primary-route allow
  cannot survive into an alternate route.
- Generate and Stream share the same route check, field comparison, marker
  injection, stripping, and diagnostic semantics.
- Canonical history is immutable per attempt. Stripping affects only the
  cloned request history.
- Public text and function-tool history are byte-for-byte compatible on allow
  and reject paths.
- ACP, export, TUI, Plain, child Agent, and task projections never expose
  origins, signatures, encrypted continuation, or the private marker.
- Legacy and malformed data preserve Session readability but never private
  reuse.

## P38.0 implementation slice

P38.0 is one observable behavior and one rollback boundary:

1. Add strict origin/sidecar validation, physical-entry/index plus logical-ID
   and payload-digest binding, clone semantics, atomic rebind rules, and
   recovery projection without backfill.
2. Extend named and legacy credential resolution with an opaque
   rotation-sensitive origin contract. Sources unable to prove origin remain
   ineligible.
3. Make `routeRegistry` re-resolve credential origin before every cache reuse,
   publish immutable client snapshots on every named change event, and return
   the exact attempt identity from shared Generate/Stream route preparation.
   Recheck and issue the client-bound one-attempt proof at the defined
   pre-conversion linearization point.
4. Record origin only for a successfully completed canonical assistant output
   from that dispatch.
5. At the route-to-Agentic leaf, compare the restored sidecar, recheck
   publication, and inject the self-generated marker only on exact success.
   All other paths strip private state before transport.
6. Add bounded redacted diagnostics and keep all public projections unchanged.

The implementation must not expose a feature flag that weakens the comparison.
If one supported credential source cannot supply safe origin identity, that
source keeps conservative stripping rather than blocking all model use.

## Required deterministic evidence

The promotion fixture
[`p38_0_characterization_test.go`](../../../engine/provider/p38_0_characterization_test.go)
freezes the target identity, physical/logical/payload binding, the sole positive
path, single-field mismatch reasons, immutable publication replacement,
credential rotation, pre-leaf rejection, redaction, caller-marker rejection,
and public-part preservation without enabling production reuse.

The implementation PR must additionally prove:

- Generate and Stream exact-route parity, including multi-chunk and
  multi-index reasoning/signature assembly interleaved with text and tool
  calls;
- exact same provider/account/API-family/API-model/route/credential as the only
  request containing the private reasoning item;
- one-field mismatch tests plus manual model change, primary-to-alternate
  failover, same-provider account failover, credential rotation, legacy
  restart, malformed sidecar, recovery mismatch, and compaction-summary cases;
- a deterministic route-publication race where a stale captured decision strips
  before the leaf model receives input;
- failed, cancelled, and partial streams cannot mint durable origin;
- crash/reload/fork keeps valid private associations and treats partial pairs
  as absent without making the Session unreadable;
- sidecar swapping across physical entries, indices, logical IDs, or equal-ID
  changed payloads rejects recovery; exact fork/rewrite rebinding is atomic;
- `assistant-origin-binding/v1` golden encoding covers every persisted Message
  field and rejects non-canonical values before minting an origin;
- the exact Eino-ext Agentic OpenAI request fixture contains encrypted reasoning
  only on allow, while public text/tool request items match on reject;
- ACP create/load/resume and Session export contain no origin, marker,
  reasoning text, or signature;
- focused race coverage for registry publication, Stream settlement,
  transcript recording, reload, and concurrent route attempts; and
- `make fmt`, `make lint`, `make lint-new`, `make test`, `make build`, docs
  checks, migration manifest/ledger checks, and `git diff --check` pass.

Tests must inspect the leaf request or captured Agentic input, not infer reuse
from a response. Real provider access is not required for deterministic
closure, and no fixture may persist a real credential, signature, or reasoning
payload.

## Historical promotion proof

P38.0 was promoted because:

- current source and the downstream adapter were re-audited at pinned
  snapshots;
- the sole-positive identity and every rejection category are executable in a
  test-only fixture that captures the target Agentic input, injects the marker
  only on exact origin, strips every private block on rejection, and compares
  public text/tool items;
- the fixture binds the origin to physical entry/index, logical ID, and payload
  digest, and models immutable route replacement so credential rotation between
  capture and pre-leaf recheck strips the old attempt;
- existing failover stripping and persisted-marker rejection close the two
  unsafe prerequisite paths;
- existing `TestP361ReplaySnapshotProjectsOrderedPublicTextOnly`,
  `TestP361ACPReplayProjectionEmitsOnlyOrderedPublicText`, and the P36
  reasoning-only replay fixtures prove the current ACP projection remains
  public-only;
- the durable sidecar, dispatch publication, conversion point, public
  exclusion, compatibility effect, and rollback boundary are frozen here; and
- no other queue row is `Ready`.

This promotion did not assert that same-origin production continuation worked.
That claim was established by the P38.0 implementation and closeout evidence.

## Documentation ownership

This file owns the historical accepted contract. The comparative audit owns
reference evidence. Root PLAN owns current queue state. Current provider
architecture owns delivered behavior, G34 is closed, and the history record
owns closeout evidence. A later change must not reopen cross-origin private
reuse by treating this historical contract as current route authority.

## Rollback

The schema is additive. Old readers ignore the private transcript sidecar; new
readers treat an absent sidecar as ineligible. Rollback removes route-origin
capture, attempt-local marker injection, and diagnostics, and returns to
unconditional conservative stripping. It may leave unknown optional sidecar
JSON on disk, but no rolled-back code may copy it into message metadata or
public output.

Rollback must retain the completed alternate-route stripping and the rule that
`messagesToAgentic` rejects persisted assistant message-level `Extra`. It must
not delete public text/tool history or rewrite existing transcripts merely to
remove optional origin records.
