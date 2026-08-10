# Permission Coalescing Audit

**Status:** reference-snapshot
**Last verified:** 2026-07-14
**Result:** P9.2A-P9.2B complete at this snapshot

> **Ownership:** this report owns the source-backed P9.2 comparison and the
> resulting implemented contract. Current behavior belongs in
> [`permissions.md`](../../../architecture/capabilities/permissions.md), and
> closeout evidence belongs in
> [`migration/history/runtime/post-parity.md`](../../history/runtime/post-parity.md).

## Observable Question

After one permission request receives a positive durable decision, which
already-pending requests may be allowed without prompting again, and how does
each request still terminate exactly once across cancellation, late responses,
owner-thread attention, child completion, and all supported entrypoints?

The audit separates two identities that must not be conflated:

- **request identity:** one tool-use ID and one terminal resolution;
- **approval scope:** the command, path, resources, or canonical input that a
  session or persistent decision actually grants.

## Decision

P9.2 was completed as two ordered implementation slices:

1. **P9.2A canonical permission interaction lifecycle** unifies request
   registration, structured decisions, owner metadata, and exactly-once
   terminal settlement in the engine.
2. **P9.2B exact-scope positive coalescing** re-evaluates already-pending
   requests after a session or always grant and settles only requests that the
   newly committed grant now allows.

Coalescing was added to the engine coordinator, not the TUI attention store or
`PermissionPrompter`. The runtime store remains a read model rather than a
settlement registry.

## Implementation Outcome

- P9.2A moved request registration, structured decisions, grant commit,
  exactly-once settlement, and request/resolved events under one canonical
  project coordinator shared explicitly by parent/child engines and ACP
  sessions.
- P9.2B scans only after a committed session or always grant. Candidate engines
  re-evaluate their own current policy and invocation; each accepted candidate
  performs an independent atomic claim without recommitting or recursively
  scanning.
- Session scans require one root lineage, the source scoped key, and the
  candidate's actual shared tracker grant. Always scans require one canonical
  project, a fresh effective-rules allow, and a match against the persisted
  source rule.
- Plain, TUI, ACP, headless, real subagent, shutdown, late-response, and restart
  tests prove presentation and lifecycle boundaries. Pending requests remain
  non-durable and never replay as actionable work.
- Positive scanning is an intentional Eino-Agent extension adapted from
  OpenCode. Claude Code Ripe contributes the exactly-once model but contains no
  verified equivalent durable-positive scan, so the result is not described as
  Claude parity.

## Pre-Implementation Eino-Agent Evidence

| Concern | Verified current behavior | Consequence |
|---|---|---|
| Session scope | `engine/permission_scope.go:sessionApprovalKey` creates exact Bash commands, recursive read/search paths, exact write paths, or canonical-input fingerprints. | A session decision already has a parameter-scoped representation suitable for candidate re-evaluation. |
| Always scope | `permission.BuildRuleFromInvocation` writes a local settings rule; Bash uses a command-family wildcard and file tools use directory wildcards. | Always scope is intentionally broader than one input. Coalescing must evaluate candidates against the new rule, not compare raw input or session keys. |
| Callback owner | TUI `MakeCanUseToolFn` owns a response channel, applies session/always persistence itself, and reports callback request/resolved events. | The engine cannot atomically settle another callback-owned waiter today. |
| Prompter owner | Each `QueryEngine` constructs its own `PermissionPrompter`, keyed by tool-use ID. `PermissionPrompter.Resolve` and `InteractiveHandler` use separate channel/claim mechanisms. | There is no single first-winner claim shared by user, classifier, cancellation, and a future coalescer. |
| Runtime projection | `RuntimeStateStore` records unresolved interactions by owner thread and removes them on a matching resolution event. Parent and child engines share this store. | The projection can prove attention cleanup, but it must not become the mutable resolver registry. |
| Subagents | Child engines share the parent callback and runtime store but construct a child prompter and approval tracker. The parent wrapped callback supplies effective parent approval checks. | The canonical live coordinator must be explicitly shared across the root session lineage. |
| TUI | Callback events merge with local response handles by tool-use ID. Session approval is in-memory; always writes project-local settings. | TUI has the richest decision surface, but persistence and settlement currently happen outside engine ownership. |
| Plain REPL | `a=always` returns the reason string `session_approve`; no session approval or persistent rule is committed. It does not report callback permission events. | The label and actual durability disagree; P9.2A must make this entrypoint explicit rather than coalescing a non-existent grant. |
| ACP | The client receives allow-once/allow-always/reject options, but `allow_always` currently returns only `true`. It neither persists a rule nor reports callback request/resolved events. | ACP cannot participate in durable positive coalescing until structured decisions move into the engine lifecycle. |
| Headless | Non-yolo headless denies before creating an interactive waiter; yolo bypasses the prompt. | Headless has no pending requests to coalesce and must remain fail-closed outside bypass mode. |
| Restart | Pending requests live in memory. Runtime/view replay intersects only currently unresolved canonical IDs; a fresh process has none. | Pending permission requests must not be restored as actionable waiters after process restart. |

### Pre-Implementation Exactly-Once Gap

Before P9.2A, `permission.ResolveOnce` was used by speculative classifier handling, but user
responses reach the request channel through `PermissionPrompter.Resolve`
without claiming the same guard. A channel send is bounded, but it is not a
shared terminal-state transition: after one receiver drains the channel, a
late racer can still send before pending-map cleanup. The current caller
returns once, yet the system cannot prove one canonical winner or one emitted
terminal record across all racers.

P9.2A replaced this split claim with one atomic take/remove transition that
owns both waiter delivery and the per-request resolution event.

## Reference Matrix

| Reference | State and scope owner | Positive durable behavior | Cancellation and late response | Projection and replay | Recommendation |
|---|---|---|---|---|---|
| OpenCode | `PermissionV2.Service` owns a process-local pending map. Requests carry `action`, `resources`, and optional `save` resources; saved rules are project-scoped. | `always` first saves the source rule, then re-evaluates every pending request against its own configured rules plus saved rules. Each fully allowed request receives its own reply event and deferred settlement. `once` does not cascade. | Reply runs uninterruptibly, removes each map entry, and returns not-found for a late reply. Finalization fails and clears remaining waiters. | Asked/replied events add and remove each owner session request. Pending requests themselves are not durable replay state. | Adapt the per-candidate re-evaluation and per-request reply. Reject its blanket same-session rejection cascade. |
| Claude Code Ripe | Interactive permission owns one tool-use queue row and a `createResolveOnce` claim. Swarm permission owns pending/resolved files under a directory lock. | No verified durable-positive pending scan exists in the allowed reference source. | Local, bridge, channel, classifier, hook, and abort racers must claim before async work. Remote and swarm paths remove/transition the request before a late response can win. | Swarm requests are durable coordination records; ordinary local permission is live interaction state. | Reuse exactly-once and cancellation style. Do not claim coalescing parity where the behavioral specification has no such feature. |
| Codex | Active `TurnState` owns pending request-permission entries by call ID. Permission profiles encode requested environment and CWD scope. | Turn and session grants are recorded, but no pending scan was found. Granted profiles are intersected with the original request before recording. | Response and cancellation first remove the pending entry. A late response sees no entry and cannot resurrect it. | App-server guards project permission waiters into thread `WaitingOnApproval` state. | Adapt atomic remove and grant/request intersection. Do not import storage or app-server structure. |
| Crush | A permission service owns pending channels by request ID; persistent session keys include session, tool, action, and normalized path. | The winning `GrantPersistent` callback writes the grant and wakes one waiter. A global request mutex serializes prompts, so it does not implement multi-owner coalescing. | `Take` is the exactly-once winner; focused tests cover grant/deny races and losing persistent grants. Cancellation has weaker explicit resolution evidence. | Session grants are in memory; notifications are keyed by tool-call ID. | Reuse atomic take tests. Reject request serialization and its path-only scope for command tools. |

## Frozen Contract

### Ownership

- One process-local `PermissionCoordinator` owns live requests and terminal
  claims for one canonical project runtime. Canonical identity is the resolved
  project root plus the configuration scope used to load project rules.
- Every request records its root-session lineage and owner thread. Root and
  child engines in that lineage share the project coordinator, just as they
  share `RuntimeStateStore`.
- TUI and plain runtimes normally own one project coordinator. An ACP agent
  keeps a registry keyed by canonical project identity and reuses a coordinator
  only across live session engines with that identity. It releases an idle
  coordinator after the last engine unregisters; unregistering the last engine
  first cancels any remaining owned requests and makes them terminal.
- Session grants scan only one root-session lineage. Project-local always grants
  may scan all live requests in the same project coordinator, but never another
  project, process, or configuration scope.
- `RuntimeStateStore` remains a bounded read model. TUI stores presentation
  handles only and cannot authorize or settle another request directly.

### Decisions

Adapters return one structured terminal decision:

- `allow_once`
- `allow_session`
- `allow_always`
- `deny`
- `cancelled`
- `timed_out`

Only `allow_session` and `allow_always` may trigger candidate scanning. An
ordinary allow, deny, cancellation, timeout, classifier approval, hook approval,
or repeated-tool override never cascades.

### Scope And Candidate Evaluation

- Register the canonical invocation descriptor before presenting the adapter.
- Commit the durable grant before scanning candidates.
- Re-evaluate each candidate with its own tool/input, explicit deny/ask/allow
  rules, permission mode, project root, and the newly committed grant.
- Settle a candidate only when that exact request is now allowed. Same tool
  name, same session, similar path, or matching raw JSON is insufficient.
- Evaluate a candidate with its own complete normalized invocation; never copy
  the source request's raw input or session key onto it. A broader persisted
  rule may allow a different invocation only when effective rule evaluation
  allows that candidate. Deny and ask rules continue to outrank a durable
  allow.

### Terminal Semantics

- Atomic take/remove chooses the first winner for user response, classifier,
  hook, coalescer, cancellation, timeout, child shutdown, or engine shutdown.
- Every winning request emits its own `permission_resolved` event on its owner
  event stream before waiter delivery completes.
- A losing late response returns false/not-found and creates no persistence,
  event, attention row, or tool execution.
- Pending requests are not transcript/session replay state. Restart removes
  their actionability; existing resolved events remain diagnostic history.

### Entrypoints

| Entrypoint | Required P9.2 behavior |
|---|---|
| TUI | Adapter presents structured choices; engine commits grants, scans, settles, and emits events. Inactive owner attention disappears per resolved request. |
| Plain REPL | `always` must mean a real project-local durable grant; add an explicit session choice or remove misleading wording. Prompting remains serialized by stdin, but child waiters can be coalesced. |
| Headless | No waiter in non-yolo mode; deny with diagnostics. Yolo bypass creates no durable grant and no cascade. |
| ACP | Map ACP option IDs to structured decisions and persist `allow_always` before scanning. Use the engine tool-use ID as request identity; ACP transport IDs remain presentation metadata. Share live coordination only between engines with the same canonical project identity. |
| Subagent | Share the parent coordinator, preserve child thread/session owner identity, and limit session grants to the root lineage. Child completion must cancel any request it still owns. |

## Rejected Designs

- OpenCode-style blanket rejection of all requests in one session.
- Tool-name-only, same-session-only, or raw-input equality coalescing.
- A TUI `PermissionQueue` loop that sends allow responses to visually similar
  rows.
- Serializing all permission requests with one mutex as a substitute for a
  multi-owner lifecycle.
- Replaying an unresolved runtime snapshot as a live permission waiter after
  restart.
- Treating `allow_always` as durable when the adapter only returned `true`.

## Completed Slice Boundaries

### P9.2A Canonical Permission Interaction Lifecycle

**Completed:** 2026-07-14

- add a shared engine coordinator and structured decisions;
- migrate callback and prompter settlement to one atomic terminal claim;
- make request/resolved events adapter-independent;
- wire TUI, plain, ACP, headless, and child ownership explicitly;
- correct plain/ACP durable-decision behavior;
- prove user/classifier/cancel/timeout/late-response races and owner cleanup;
- prove same-project ACP reuse, cross-project isolation, coordinator release,
  and root-session lineage isolation.

P9.2A must not auto-resolve another pending request.

### P9.2B Exact-Scope Positive Coalescing

**Completed:** 2026-07-14

- scan only after a committed session/always grant;
- re-evaluate candidates against the effective grant and current deny/ask
  precedence;
- atomically settle every newly allowed candidate with its own event;
- prove cross-thread/child attention cleanup and domain boundaries;
- prove ordinary allow, rejection, cancellation, timeout, and repeated-tool
  decisions do not cascade.

## Primary Source Anchors

### Eino-Agent

- `engine/permission_interaction.go`: project coordinator, atomic settlement,
  durable-grant scan, and candidate claims
- `engine/permission_scope.go`: `permissionInvocation`, `sessionApprovalKey`
- `engine/permission/approvals.go`: `ApprovalKey`, `MatchesInvocation`, and
  root-lineage approval lookup
- `engine/permission/persist.go`: `BuildRuleFromInvocation`
- `engine/engine.go`: `wrapCanUseTool`, `permissionGrantEvaluator`,
  `ApproveForSession`, and `PersistPermissionRule`
- `engine/tool_execution.go`: canonical tool-use identity and permission event
  routing
- `engine/runtime_state.go`: unresolved interaction reducer
- `engine/subagent.go`: parent coordinator, root lineage, tracker, callback, and
  runtime wiring
- `internal/tui/app.go`, `internal/tui/thread_attention.go`: callback waiters and owner presentation
- `cmd/eino-agent/cmd/root.go`, `cmd/eino-agent/cmd/headless.go`: plain/headless adapters
- `server/acp/agent.go`: ACP permission adapter

### References

- OpenCode: `.reference/opencode/packages/core/src/permission.ts`
- OpenCode UI: `.reference/opencode/packages/app/src/context/global-sync/event-reducer.ts`
- Claude Code Ripe: `.reference/claude-code-ripe/src/hooks/toolPermission/handlers/interactiveHandler.ts`
- Claude Code Ripe swarm: `.reference/claude-code-ripe/src/utils/swarm/permissionSync.ts`
- Codex: `.reference/codex/codex-rs/core/src/session/mod.rs`
- Codex turn state: `.reference/codex/codex-rs/core/src/state/turn.rs`
- Crush: `.reference/crush/internal/permission/permission.go`
