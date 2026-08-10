# P46 Model Failover Contract Repair

**Status:** historical
**Created:** 2026-08-05
**Approved:** 2026-08-06
**Completed:** 2026-08-06

> **Ownership:** accepted model-failover repair contract and the promotion
> boundary between P46.1 and P46.2

## Decision

Preserve the completed P29.4 project-owned failover contract and repair two
verified implementation gaps without broadening routing policy. P46 uses the
existing configured portfolio, role resolver, canonical model round, bounded
attempt coordinator, and runtime-event vocabulary. It adds no adaptive health,
hidden scoring, provider-specific routing owner, durable attempt state, or
second fallback path.

The repair is split into two independently reviewable slices:

1. P46.1 freezes the complete provider-visible request footprint before
   candidate admission; and
2. P46.2 makes a switch-eligible failed attempt explicitly discarded and
   projects one safe fallback notice through each active entrypoint.

The adoption decision is `preserve`: current P29.4 safety, ordering, budgets,
and compatibility remain authoritative, while implementation and current
documentation are brought back to that accepted contract.

## User Problem And Approval Evidence

### Incomplete request footprint

At approval, `runCanonicalModelRound` froze normalized messages, the system
prompt, and tool schemas independently. The attempt coordinator derived
`RoleRequirements.PromptTokens` from messages alone. User context is already
prepended to those messages, but the system prompt and provider-visible tool
schemas are not included. A smaller-context alternate can therefore pass
pre-dispatch admission, consume a switch and provider call, then terminate as
`context_too_long` before a later usable alternate is considered.

The existing smaller-context test supplies `PromptTokens` directly to the role
resolver. It proves the resolver comparison, not the production request-fact
construction that undercounts the real request.

### Incomplete disposal and switch visibility

At approval, the coordinator tombstoned retractable output and emitted a
generic `failed` attempt whose output disposition could be `discarded`. It did
not emit the planned discarded phase. TUI ignored model-attempt facts except
for exact tombstones, while Plain, Headless, and ACP had no model-failover
projection. An overload recovery could therefore remove partial TUI output or
silently change profiles without one bounded user-visible notice.

## Approaches Considered

### Request context admission

| Approach | Decision | Reason |
|---|---|---|
| Freeze one provider-neutral request footprint from messages, system prompt, and tool schemas | Adopt | Uses the exact immutable inputs already owned by the canonical round and keeps candidate admission deterministic. |
| Add a fixed percentage or token safety margin to message tokens | Reject | Still guesses request shape, over-rejects small prompts, and cannot prove tool-heavy requests fit. |
| Add provider-specific tokenizers before candidate construction | Defer | Would initialize or couple admission to adapter-specific behavior, increase dependencies, and still not provide exact remote tokenization. |

### Attempt disposal and notice

| Approach | Decision | Reason |
|---|---|---|
| Extend `EventModelAttempt` with a `discarded` phase and render later `started` attempts | Adopt | Keeps one engine-owned event stream, preserves attempt identity, and lets adapters produce non-canonical notices. |
| Emit a generic attachment or assistant message | Reject | Risks transcript/model-history pollution and confuses routing diagnostics with user or model content. |
| Add a second failover-specific event type | Reject | Duplicates attempt lifecycle ownership and creates avoidable reducer and entrypoint drift. |

## Scope And Non-Goals

P46 changes only generic P29 overload failover. It does not change:

- same-route 429 or overload retry limits;
- overload as the only switch-eligible failure class;
- capability, modality, PDF, reasoning, or context skip taxonomy;
- configured candidate order, provider-call/switch/deadline budgets, or lazy
  route construction;
- complete-stream-before-tool commitment, provider usage settlement, or
  failed-output durability;
- P30.3's separate `1 + 1 + 1` media recovery;
- Session, transcript, prompt-record, or runtime-input schemas;
- manual model selection, resume/rebind behavior, or role policy;
- output-token reservation, provider billing tokenization, cost scoring,
  adaptive health, Retry-After cooldown, or background probes; or
- standalone MCP, which has no model runtime.

## Frozen Invariants

1. The canonical round remains the only owner that can assemble model-visible
   messages, system prompt, and tools.
2. Candidate admission is detached and performs no route construction,
   credential resolution, provider-usage admission, or dispatch.
3. A skipped candidate consumes no attempt, switch, provider call, or wait.
4. Every actual dispatch restores the same immutable request snapshot.
5. Only typed `overloaded` can switch profiles.
6. A constructable alternate is confirmed before a failed TUI projection is
   retracted or a switch is consumed.
7. Failed output and fallback notices never enter canonical assistant history
   or durable Session state.
8. Plain, headless, ACP, and default library consumers remain
   non-retractable after the first visible assistant output.
9. Reasoning intent is frozen for one logical request. An incompatible
   failover candidate is skipped; effort is never guessed, lowered, or cleared
   merely to make that candidate eligible.
10. Every projected diagnostic excludes account, endpoint, credential,
    headers, prompt content, raw provider responses, and secret-derived data.

## P46.1 Complete Prompt Footprint Admission

### Target contract

Before `ResolveFailoverChain`, the canonical round supplies immutable request
facts containing:

- normalized messages after user-context prepend;
- the exact cloned system prompt;
- the exact cloned provider-visible tool schemas; and
- modality, PDF, reasoning-history, and requested-effort requirements.

The request-footprint estimator remains provider-neutral and conservative:

- messages and system prompt use the existing compact message heuristic;
- each tool contributes its complete detached JSON representation already
  accepted by immutable request cloning, including serializable
  `ToolInfo.Extra`; only the resulting count may leave the estimator;
- arithmetic is overflow-safe and a footprint-construction failure terminates
  before candidate resolution; and
- the result is an input-fit estimate, not a billing count or output-token
  reservation.

Every candidate compares that same frozen input estimate with its authoritative
context-window metadata. An unknown or insufficient context window produces
the existing safe `context_window` candidate skip before route construction.

### Acceptance evidence

- A production-path test where messages fit but system prompt pushes an
  alternate over its context limit.
- A production-path test where messages fit but tool schemas push an alternate
  over its context limit.
- The skipped candidate initializes no route and consumes no attempt, switch,
  provider call, usage admission, or deadline wait.
- A later sufficiently large alternate can still start and complete.
- Existing media, reasoning, duplicate, retry, budget, immutable replay, and
  legacy fallback tests remain unchanged or become strictly more explicit.

### Rollback

Rollback removes the complete-footprint constructor and restores message-only
admission. It changes no configuration or durable state, but reopens G36 and
must not be described as preserving smaller-context pre-dispatch safety.

### Completion

P46.1 completed on 2026-08-06 and closed G36. The canonical round now derives
one overflow-safe input-fit estimate from the exact frozen messages, system
prompt, and complete tool list before resolving the failover chain. The
[verification record](../verification/p46-1-complete-prompt-footprint.md) and
[history record](../history/runtime/p46-1-complete-prompt-footprint.md) own its
reproducible and delivery evidence.

## P46.2 Observable Attempt Discard And Switch

### Engine event order

Once the next candidate is constructable and the current failure is confirmed
as switch-eligible, the engine emits:

1. `EventModelAttempt` for the current attempt with phase `discarded`, the
   typed failure class, and output disposition `never_started` or `discarded`;
2. an exact attempt tombstone only when retractable output was offered;
3. `EventModelAttempt` with phase `started` for the admitted next attempt; and
4. the next provider dispatch.

A switched attempt does not also emit the terminal `failed` phase. `failed`
remains the phase for attempts that cannot continue. Candidate skips retain
`candidate_skipped`; successful attempts retain `committed`.

### Entrypoint projection

| Entrypoint | Required projection |
|---|---|
| TUI | Remove only the tombstoned attempt, then show one bounded warning/status line when a `started` event has `attempt_index > 0`. |
| Plain | Write one safe fallback notice to stderr; stdout assistant/tool content is unchanged. |
| Headless | Write the same safe notice to stderr; structured result and assistant output are unchanged. |
| ACP | Emit one status update through the existing `_session/status` extension; never synthesize an assistant text chunk. |
| Library | Preserve the typed attempt events; no forced writer or presentation dependency. |
| Standalone MCP | No change; no model runtime. |

The notice identifies only the configured fallback profile and bounded switch
count. Because P29 permits switching only for overload, it may state that the
previous route was overloaded. It contains no API model, endpoint, account,
credential reference, raw error, or prompt data.

The next `started` event is projected synchronously as that notice before its
provider dispatch. Profile IDs are already normalized to the bounded
`[a-z][a-z0-9._-]{0,63}` portfolio form; adapters must not substitute a display
name, API model, or unvalidated provider response.

Attempt events remain process-local and are not replayed after restart. Runtime
state records their phase and identity; presentation does not become a second
state owner.

### Acceptance evidence

- Ordered engine events prove `discarded -> tombstone-if-needed -> started`
  before the next dispatch.
- Zero-output failover still emits discarded and one notice without a
  tombstone.
- Partial TUI output is removed exactly once; plain/headless/ACP never switch
  after visible output.
- TUI, plain, headless, ACP, and library fixtures observe the same attempt
  identity and do not receive duplicate notices.
- Canonical assistant history, transcript checkpoints, tool side effects, and
  structured headless output contain no fallback notice.
- Canonical trace and applicable contract/PTY tests pin the ordering and
  output-channel boundary.

### Rollback

Rollback removes adapter presentation of switched attempts and maps
`discarded` back to `failed + output_disposition`. It changes no durable state
but reopens G37 and loses the explicit disposal phase required by this
contract.

### Completion

P46.2 completed on 2026-08-06 and closed G37. Once the next overload candidate
is constructable, the current attempt emits `discarded`, an exact tombstone
follows only for retractable output, and the next `started` event precedes its
provider dispatch. TUI projects one warning, Plain and Headless write one safe
stderr notice, ACP emits one `_session/status` update, and library consumers
retain typed events without a forced writer. The
[verification record](../verification/p46-2-observable-failover.md) and
[history record](../history/runtime/p46-2-observable-failover.md) own the
reproducible and delivery evidence.

## Promotion Gate

The user approved this written contract on 2026-08-06. P46.1 and P46.2 closed
with focused evidence and repository gates; P46 has no active queue row.

Promotion does not authorize P29.5/P29.6 adaptive routing. Any future
measurement or health policy requires a new product intake with a bounded,
privacy-safe evidence owner and non-zero representative data.

## Source And Test Owners

| Boundary | Current owner | Required evidence |
|---|---|---|
| Immutable request assembly | [`runCanonicalModelRound`](../../../engine/model_round.go#L65) | Engine integration tests over exact messages/system/tools. |
| Failover requirements and attempt lifecycle | [`newModelAttemptCoordinator`](../../../engine/model_failover.go#L64) | Coordinator and query fallback event-order tests. |
| Candidate metadata admission | [`Runtime.ResolveFailoverChain`](../../../engine/provider/role_resolver.go#L197) | Provider role-resolver no-call skip tests. |
| Runtime attempt truth | [`ModelAttemptEvent`](../../../engine/events.go#L275) and [`RuntimeStateStore`](../../../engine/runtime_state.go#L379) | Runtime reducer/trace assertions. |
| TUI presentation | [`App.handleEngineEvent`](../../../internal/tui/app.go#L1956) | Exact-attempt removal and warning projection tests. |
| Plain projection | [`drivePlainQueryEvents`](../../../cmd/yhc/cmd/root.go#L461) | stdout/stderr separation test. |
| Headless projection | [`collectHeadlessEvents`](../../../cmd/yhc/cmd/headless.go#L212) | result/stderr separation test. |
| ACP projection | [`Agent.streamEvent`](../../../server/acp/agent.go#L1913) | status-update/no-assistant-chunk contract test. |

## Verification And Closeout

Each slice follows red-green-refactor, retains its own focused reproduction,
and runs the affected package tests. P46.1 requires the runtime/provider
contract pack. P46.2 additionally requires entrypoint contract and Unix PTY
coverage. Final closeout runs:

```bash
make fmt
make lint
make test
make build
make docs-check
go run ./scripts/migration_manifest.go check
git diff --check
```

Live-provider, physical-terminal, and remote-CI observations remain separate
from deterministic local acceptance. A CI usage-limit failure may be recorded
under the user's explicit exception, but it is never reported as green.
