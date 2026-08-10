# Durable Goal Lifecycle Audit

**Status:** reference-snapshot
**Snapshot:** 2026-07-29; Eino-Agent `f37b9d6cb37e`, Codex
`66bd101fff6f`, Claude Code Ripe `4b9d30f79532`, OpenCode
`411eff73f026`, Crush `2af939d8e900`, Grok Build `a5727c596045`,
official ACP v1 plus `github.com/coder/acp-go-sdk` v0.13.5, and current
official Codex documentation
**Last verified:** 2026-07-29

> **Ownership:** source-backed evidence for persisted Goal state, automatic
> continuation, budget, recovery, and entrypoint behavior. This report does
> not own current Eino-Agent behavior or executable order.

## Conclusion

Codex Goal is not a task-list alias or an unbounded loop. It is a persisted
per-thread target whose `active` state may start another turn after the
previous turn reaches an idle boundary. The user controls objective changes,
pause, resume, clear, and budget policy; the model can inspect the Goal and
request only `complete` or `blocked`; the runtime owns accounting, recovery,
and automatic continuation.

Eino-Agent now also has exact root/descendant provider accounting, one
root-scoped provider-call admission gate, one deterministic crash-safe Goal
continuation cursor/item, off-by-default saved-root TUI and Plain consumers,
and one distinct bounded headless Goal process under the existing QueryEngine,
Session, transcript, and runtime-input owners. Generic claims and
subscriptions still exclude Goal items. ACP remains deliberately unsupported:
Initialize does not negotiate Goal, ACP engines fail the Goal capability gate,
and no Goal extension can inspect, control, or submit the durable cursor.

The recommendation is `adapt`: preserve Codex's useful persisted-target and
active-only continuation outcome through Eino-Agent's existing owners. Do not
copy Codex's SQLite/App Server structure or Grok Build's multi-role Goal
harness. For P24.5c specifically, use `combine`: official ACP capability and
private-extension rules plus exact Codex-style request/event identity around
the existing Eino-Agent owner, with ordinary ACP clients unchanged.

## Research Boundary

The observable question is:

> What project-owned state, ordering, recovery, budget, and entrypoint
> contracts are required for an explicit Goal to continue safely across turns
> and process resume?

The P24.5c promotion narrows the transport question to:

> How should an ACP client explicitly negotiate and drive one durable Goal
> without changing unsupported clients or duplicating continuation authority?

Claims use these labels:

- **Verified:** reached from current source, focused tests, or the named
  reference snapshot.
- **Inference:** a design consequence supported by the verified source shape.
- **Recommendation:** a future Eino-Agent contract.
- **Unresolved:** evidence or product policy that must be closed before the
  affected implementation slice is promoted.

The Codex source is a repository-local snapshot, not an upstream-current
claim. Current public product behavior was checked against
[Config basics](https://learn.chatgpt.com/docs/config-file/config-basic.md) and
[Long-running work](https://learn.chatgpt.com/docs/long-running-work.md).

## Current Eino-Agent Evidence

### Existing owners

| Boundary | Verified current owner | Consequence for Goal |
|---|---|---|
| Conversation lifecycle | [`QueryEngine`](../../../../engine/engine.go#L37) and its ProjectGraph turn path own root, TUI, plain, headless, ACP, and child execution. | Goal must be a `QueryEngine` capability, not a TUI loop. |
| Durable Goal state | [`goalService`](../../../../engine/goal_state.go) and [`persistSessionCheckpointMessages`](../../../../engine/session_checkpoint.go) serialize one versioned root Goal and flush the complete Session checkpoint before publication. | Provider admission and aggregate checkpoints must extend this owner rather than add a second Goal store. |
| Additive state schema | [`SessionMetadataFull`](../../../../engine/session/branch.go) carries presentation-free Goal state without callbacks or grants; unknown/corrupt versions preserve the Session but cannot activate. | Additional entrypoints consume the existing versioned state and must not add presentation authority or a second store. |
| Runtime input | [`RuntimeItem`](../../../../engine/input_coordinator.go#L173), [`ClaimNextRuntimeItem`](../../../../engine/queued_input.go#L134), and transcript-confirmed settlement provide durable enqueue, claim, release, and replay behavior. | Goal continuation is a typed item with dedicated TUI, Plain, and bounded headless consumers; P24.5c must preserve generic selection and settlement unchanged. |
| Live projection | [`GoalSnapshot`](../../../../engine/goal_runtime.go) and [`RuntimeStateStore.Apply`](../../../../engine/runtime_state.go) expose a detached ordered read model. | The reducer remains non-authoritative for accounting and recovery. |
| Identity and ordering | [`runtimeIdentitySnapshot`](../../../../engine/runtime_events.go) supplies Session, thread, Agent, parent, turn, sequence, causation, Goal/objective/root/Goal-turn identity, and positive child generation. | The Goal ledger and cursor already bind logical-round/provider-attempt identity; TUI projection cannot reconstruct or alter it. |
| Provider usage | The root Goal usage service admits one exact root/child-generation provider call, normalizes final provider usage into the transcript-backed Goal ledger, and applies aggregate budget state before clearing admission. | Session `UsageSummary` remains diagnostic; every transport may consume only the exact Goal ledger and coverage state. |

The runtime-input coordinator persists before it signals a subscriber, orders
explicit priority before FIFO, restores an interrupted processing item to
pending, and removes an item already proven delivered by transcript identity.
These are the correct primitives for continuation recovery. A bounded
`RuntimeStateStore` snapshot is not sufficient because it can evict old
threads and is not ordinarily replayed during session resume.

The current coordinator does not by itself close Goal admission races:
`Cancel` removes pending items only, while a claimed processing item returns to
pending after restart unless it is durably settled or transcript delivery is
proven. Therefore Goal control and final continuation admission need one
QueryEngine serialization boundary, and every permanently stale or superseded
claim needs a durable rejection/settlement disposition. A plain “reject” or
release would permit post-cancel execution or a restart claim/reject loop.

### Current entrypoint behavior

| Entrypoint | Verified behavior now | Goal implication |
|---|---|---|
| TUI | [`startNextGoalContinuation`](../../../../internal/tui/queued_input.go#L77) consumes the dedicated Goal signal and exact claim after ordinary input, while the generic runtime-item path remains separate. | P24.4 is the first supported opt-in consumer and proves the existing dedicated transport boundary. |
| Plain REPL | [`drivePlainREPL`](../../../../cmd/yhc/cmd/plain_repl.go) owns one process-lifetime stdin broker, gives completed input/permission precedence, and consumes only the dedicated Goal wake/claim/submission path. | P24.5a is complete; its single-reader transport owner is not reusable ACP protocol authority. |
| Headless | [`runHeadless`](../../../../cmd/yhc/cmd/headless.go#L103) remains one-shot, while [`driveHeadlessGoal`](../../../../cmd/yhc/cmd/headless_goal.go) owns the distinct bounded `goal run` process over an exact saved Session. | P24.5b is complete; ordinary `exec`/`-p` compatibility remains unchanged. |
| ACP | [`Agent.Initialize`](../../../../server/acp/agent.go) ignores client capabilities, [`Agent.Prompt`](../../../../server/acp/agent.go#L575) is client-request-driven, and [`HandleExtensionMethod`](../../../../server/acp/streaming.go) has no Goal method. | Goal control and continuation require a negotiated private protocol contract; the server must not fabricate background prompts. |
| Standalone MCP | It does not construct `QueryEngine` or a conversational thread. | Goal commands and tools do not apply. |

### Verified gap

`SessionMetadataFull` now has one version-3 Goal snapshot, the runtime reducer
has one lossless Goal event family, and `RuntimeItem` has a deterministic Goal
continuation variant. Eligible terminal aftercare checkpoints its immutable
cursor before enqueue, restart reconciles the same item, and internal final
admission revalidates exact Goal, accounting, budget, Plan, permission,
cancellation, and steering identity.

The production gap is now ACP-specific. TUI, Plain, and explicit bounded
headless Goal execution own dedicated consumers, while enqueue still does not
signal generic subscribers, generic claims skip the item, and public
runtime-item submission cannot dispatch it. ACP creates engines under
`commands.EntrypointACP`, which `goalWorkflowEnabled` rejects. Initialize
stores no client Goal offer, and the extension handler exposes no Goal schema
or method.

The smallest unsafe shortcut is to treat a pending Goal continuation as an
ordinary `session/prompt`. That request represents client-authored prompt
content and does not carry the immutable Goal cursor. Fabricating it would
create hidden input, bypass explicit client ownership, and make late delivery
or retries ambiguous.

The current controls must not be substituted:

- `TerminalCompleted` means one turn/invocation finished, not that a larger
  objective is complete.
- Session `UsageSummary` remains a diagnostic and cannot replace the exact
  Goal ledger, coverage state, or positive remaining-budget check;
- `GoalSnapshot` and reducer replay are read models, not admission or recovery
  authority;
- generic runtime-input signals and claims intentionally exclude Goal
  continuation; and
- a queued steering prompt is transport input, not a Goal state machine.

See
[`budgets-and-limits.md`](../../../architecture/runtime/budgets-and-limits.md)
for the current enforcement matrix and
[`sessions.md`](../../../architecture/tui/contracts/sessions.md) for the
resume-without-dispatch boundary.

## Plain Transport Evidence

Codex Goal provides the continuation policy but not a reusable Plain REPL
implementation. `ext/goal/src/runtime.rs:335-425` restores active state as idle,
holds one Goal-state permit across read/start, checks deferral and live thread
availability, and calls the normal idle-turn admission. This supports
active-only admission and one serialized final gate; it does not justify
copying Codex's App Server or SQLite owners.

Claude Code Ripe provides the relevant stdin lesson. Its
`src/hooks/usePasteHandler.ts:200-214` records that multiple stdin listeners
competed and dropped characters, so parsed input now has one owner. Its
structured print loop at `src/cli/print.ts:2808-2840` uses one input parser to
feed a queue concurrently with generation, and proactive work is scheduled
later at `:1828-1845` so pending stdin messages can run first. P24.5a should
adapt the ownership and priority rules, not the structured headless protocol
or proactive tick loop.

OpenCode's
`packages/core/src/session/run-coordinator.ts:1-96` and focused tests verify
that repeated wakes can coalesce and produce at most one successor after an
active drain. Eino-Agent already has a durable, identity-bound Goal item and
dedicated coalesced signal, so importing OpenCode's session runner would create
a second owner. Only the coalescing property is relevant.

P24.5a delivered the resulting `adapt` decision: one Plain-owned input/output
driver consumes the existing QueryEngine capability, keeps one blocking stdin
reader, gives completed user input and exact permission interaction
precedence, and calls only the dedicated Goal claim/submission APIs.

## ACP Transport Evidence

### Official protocol and current SDK

The official
[Agent Client Protocol](https://github.com/agentclientprotocol/agent-client-protocol)
keeps stable core behavior at protocol version 1. Initialize exchanges
capabilities; optional methods are available only when the peer advertises
support. Implementation-specific data belongs in `_meta`, and custom
extension method names begin with `_`.

The pinned Go SDK exposes `ClientCapabilities.Meta` and
`AgentCapabilities.Meta` as `map[string]any`. Its connection supports
`ExtensionMethodHandler`, `CallExtension`, and `NotifyExtension`, so P24.5c
needs no SDK fork or second JSON-RPC transport. Unknown extensions already map
to MethodNotFound.

This supports a versioned private capability and fail-closed method dispatch.
It does not define Goal state, continuation, budget, or event semantics; those
remain project-owned.

### Relevant reference mechanisms

Codex exposes typed `ThreadGoal` get/set/clear operations and Goal-updated
notifications. The protocol binds a thread and request, and the runtime/router
tracks exact turn and request identity so a late or foreign result can be
discarded. Its App Server, per-thread route registry, SQLite state, and process
lifecycle are not reusable owners in Eino-Agent.

OpenCode's ACP adapter implements standard Initialize, Session, Prompt, Cancel,
configuration, and command projection with no Goal extension. It is the
relevant compatibility baseline: a client that does not negotiate Goal must
continue to observe only the base protocol.

No negotiated ACP Goal surface was found in the selected Claude Code Ripe or
Crush snapshots. Their unrelated run loops and lifecycle events cannot justify
server-originated prompts or a second continuation scheduler.

### P24.5c recommendation

Use `combine` within the accepted P24 `adapt` program:

- negotiate `eino-agent.goal` version 1 through Initialize `_meta`;
- expose `_eino/goal/get`, `_eino/goal/control`, and
  `_eino/goal/continue` only after a compatible offer;
- emit `_eino/goal/updated` only after durable commit;
- bind every request/event to exact Session, request, Goal/objective revision,
  continuation ordinal, and Goal turn where applicable;
- map typed control intent to the existing `goalService`;
- submit work only through the existing exact Goal cursor; and
- leave absent, malformed, unsupported, disabled, and ordinary ACP paths
  unchanged.

Request IDs provide transport correlation, not durable authority. Expected
Goal revisions make control retries unable to repeat a mutation; the existing
cursor receipt/rejection lifecycle makes continuation retries unable to repeat
a provider turn. A delivery failure may hide a committed result from the
client, but it cannot roll back state or authorize re-execution.

## Codex Goal Contract

### User and model surfaces

The official product documentation describes Goals as persisted targets with
automatic continuation. `/goal <objective>` starts one; `/goal` views it; and
`edit`, `pause`, `resume`, and `clear` remain user controls. Goal execution
does not expand sandbox or approval authority.

The Codex snapshot supplies three model tools:

- `create_goal(objective, token_budget?)`;
- `get_goal()`; and
- `update_goal(status)` where status is only `complete` or `blocked`.

Their schemas and behavioral instructions are in
`.reference/codex/codex-rs/ext/goal/src/spec.rs:9-93`; dispatch and validation
are in `ext/goal/src/tool.rs:180-290,400-436`. Creation rejects an empty
objective, a non-positive budget, and replacement of an unfinished Goal.
Model creation is instructed to occur only after an explicit user or system
request.

### Durable state and continuation

| Concern | Verified Codex behavior |
|---|---|
| State | `active`, `paused`, `blocked`, `usage_limited`, `budget_limited`, and `complete` are persisted in `state/src/model/thread_goal.rs:12-115`. |
| Persistence | One Goal row records thread/Goal identity, objective, status, optional token budget, token/time usage, and timestamps in `state/goals_migrations/0001_thread_goals.sql`. |
| Eligibility | Only an `active` persisted Goal with feature/tool availability and no continuation deferral may start another idle turn. |
| Wake | `ext/goal/src/runtime.rs:359-425` calls the existing idle-turn admission with one continuation steering item; it does not recursively run a hidden model loop. |
| Resume | `ext/goal/src/runtime.rs:335-357` restores persisted state; the normal idle lifecycle later evaluates continuation. |
| Budget | `ext/goal/src/accounting.rs:313-336` accumulates non-cached input plus output usage and the state layer transitions to `budget_limited` after reported usage reaches the cap. |
| Failure | A non-retryable turn error blocks the Goal, and account usage limits produce `usage_limited`; neither path asks the model to expand authority or budget. |

TUI control is feature-gated and requires a saved session. The App Server
projects set/get/clear RPCs and change notifications. No evidence was found
for an independent non-interactive `codex goal` process command.

At this snapshot the Codex `Goals` feature is stable and default-enabled.
That is reference product policy, not evidence for Eino-Agent's first public
default or a safe token budget: the projects have different transport,
credential-cost, and rollout boundaries.

### Important qualification

The tool instruction says the model should request `blocked` only after the
same blocker persists for three Goal turns. The inspected runtime has no
durable blocker-key or distinct-turn counter enforcing that threshold.
Therefore the three-turn rule is a model contract in this snapshot, not a
runtime guarantee.

Codex's Goal budget is also post-accounting rather than a provider-admission
quota. `state/src/runtime/goals.rs:499-610` adds reported usage and then changes
status; its tests at `:1183-1251` continue charging already in-flight usage
after the Goal becomes `budget_limited`. No Goal-specific reservation or
remaining-budget check was found before provider entry. One admitted turn can
therefore overshoot, and the snapshot does not prove that child/subagent usage
is aggregated into the root thread's Goal. Those are explicit divergence and
coverage questions, not behavior Eino-Agent may claim by reference.

## Grok Build Boundary

The existing
[`Grok Build report`](../tui/grok-build.md#goal-harness) verifies a different,
heavier mechanism: optional planner, strategist, summarizer, classifier, and
skeptic roles around a Goal. It also persists role state and owns additional
model, budget, cancellation, and failure matrices.

That harness is not required to deliver Codex's narrower user outcome. Its
multi-role orchestration, actor rewrite, and automatic child wake-up remain
deferred. Reusing its visible continuation-message idea does not accept its
runtime structure.

## Evidence Matrix

| Concern | Codex snapshot | Eino-Agent now | Recommended project contract |
|---|---|---|---|
| Authority | Goal extension plus persisted thread state | `QueryEngine`, transcript/checkpoint, reducer, coordinator | Keep `QueryEngine` as the one transition and scheduling owner. |
| Persistence | SQLite Goal record | Version-3 JSONL Goal metadata plus runtime-input and transcript ledgers | Preserve the existing stores and positive-version fail-closed readers. |
| Continuation | Active-only idle turn with persisted deferral | Immutable item checkpoints before enqueue; TUI, Plain, and bounded headless consume dedicated claims while generic paths exclude it | Let negotiated ACP explicitly request the same exact claim; never synthesize a prompt or background loop. |
| Controls | User edits/pauses/resumes/clears; model completes/blocks | Command actions and Goal transitions are engine-owned and entrypoint-scoped | Add a typed negotiated ACP adapter to the same transition service; keep slash parsing and reducer mutation out. |
| Budget | Optional post-accounting threshold; admitted usage may overshoot | Exact root/child usage ledger and capacity-one durable admission gate enforce aggregate state | Require a positive effective budget for activation; ship no invented numeric default. |
| Blocked | Three-turn instruction, not enforced | Persisted blocker key and distinct Goal-turn guard enforce the threshold | Expose only the terminal request; keep runtime validation final. |
| Recovery | Resume reads Goal and later idle evaluation may wake | Exact cursor/item reconciliation, durable rejection, transcript receipt, and three explicit consumers are complete | ACP Resume stays side-effect free; only a later negotiated continue request may claim exact durable work. |
| Entrypoints | App, interactive CLI, IDE, App Server | TUI, Plain, and bounded headless Goal are supported; ACP is unsupported and ordinary headless stays one-shot | Add only negotiated ACP; unsupported clients and every other entrypoint remain unchanged. |

## Adoption And Compatibility

The recommendation is `adapt`.

Eino-Agent should adopt:

- one persisted Goal per saved root thread;
- explicit user controls and a model-visible read/terminal-status surface;
- active-only automatic continuation;
- per-Goal usage/time visibility and a post-accounting continuation stop;
- process-resume recovery from durable state; and
- unchanged permission, cancellation, and worktree boundaries.

Eino-Agent should deliberately diverge by:

- reusing `RuntimeInputCoordinator` instead of adding SQLite or an App Server;
- requiring an effective positive budget before first-release automatic
  continuation, because users supply provider credentials and bear cost;
- allowing at most one unaccounted Goal-bound provider call initially, so the
  disclosed threshold overshoot cannot multiply across root/child concurrency;
- durably recording that admission before provider entry and changing to
  `usage_limited` when recovery cannot match it to one exact usage record;
- enforcing any advertised blocker threshold in runtime state rather than
  presenting a prompt instruction as a guarantee;
- keeping Goal and Plan ownership mutually exclusive; and
- withholding model-created Goals until explicit authorization can be
  represented as typed request state.

Ordinary prompts, Plan Mode, permissions, standalone MCP, and child/review
control remain unchanged when the Goal feature is disabled. The accepted
future contract and ordered slices belong in
[`P24 Durable Goal Lifecycle`](../../plans/p24-durable-goal-lifecycle.md).

## Remaining Implementation And Promotion Evidence

- P24.5c must prove exact negotiation wire behavior, typed method bounds,
  optimistic identity conflicts, prompt/continue serialization, permission
  waits, cancellation/disconnect/close, restart, duplicate/late delivery, and
  unsupported-client compatibility before ACP Goal can ship.
- No measured provider/cost evidence justifies a positive shipped numeric
  default. Every current Goal transport therefore requires an explicit user or
  validated host budget and otherwise persists only a paused draft.
- A rollback with pending new runtime-item variants needs a drain rehearsal
  because an older binary must fail closed on an unknown item kind.
