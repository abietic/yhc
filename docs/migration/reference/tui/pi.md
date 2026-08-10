# Pi Agent Core and TUI Reference Report

**Status:** reference-snapshot
**Snapshot:** 2026-07-16; `.reference/pi` at `c6d8371521fc8357958bb21fd43552c15f46c7f4`
**Last verified:** 2026-07-17

> **Ownership:** source-backed evidence about Pi's production coding-agent
> runtime, reusable Agent SDK, session model, provider layer, and terminal UI;
> this report does not own Eino-Agent's current architecture or execution plan.

## Conclusion

Pi is most useful as a reference for a small imperative agent kernel with
explicit turn events, safe tool-call handling, a navigable session tree, and a
highly extensible terminal product. Its main architectural lesson is also a
warning: the reusable `AgentHarness` and the shipped coding-agent CLI are two
different orchestration paths. The current CLI does **not** execute through
`AgentHarness`.

The verified production path is:

```text
main -> AgentSessionRuntime -> InteractiveMode -> AgentSession
     -> Agent -> runAgentLoop -> ModelRuntime -> pi-ai Models -> provider
```

`AgentHarness -> Agent -> runAgentLoop` is a separate SDK path. It contains
valuable storage, hook, compaction, and tree-navigation ideas, but describing
those facilities as production CLI wiring overstates the current integration.

For Eino-Agent, the strongest outcome is `combine`: adapt Pi's turn and session
semantics where they improve observable behavior, retain Eino-Agent's current
runtime and permission ownership, and implement TUI/subagent behavior through
project-native contracts.

## Research Boundary

This report is a static source and test audit of the named local revision. It
does not claim upstream-latest behavior. No Pi test suite or interactive PTY
session was run. Quantitative claims such as package or provider counts are
intentionally omitted because they do not change the adoption decision.

The frozen question is:

> Which component owns turn execution and session lifecycle in the shipped Pi
> coding agent, what behavior becomes visible to the TUI, and which contracts
> are suitable for Eino-Agent?

Claims below use these labels:

- **Verified:** reached from current source or covered by focused tests.
- **Inference:** supported by source shape but not exercised in this audit.
- **Recommendation:** a project decision, not a Pi implementation fact.
- **Excluded:** outside the inspected source boundary.

## Production Wiring Versus SDK Wiring

### Shipped coding-agent path

```mermaid
sequenceDiagram
    participant Main as "coding-agent main"
    participant Runtime as "AgentSessionRuntime"
    participant UI as "InteractiveMode"
    participant Session as "AgentSession"
    participant Agent as "Agent"
    participant Loop as "runAgentLoop"
    participant Models as "pi-ai Models"

    Main->>Runtime: create cwd-bound session runtime
    Main->>UI: run interactive mode with runtime
    UI->>Session: prompt / steer / followUp / compact / branch
    Session->>Agent: configure and start one active run
    Agent->>Loop: run loop with queue pollers and tools
    Loop->>Models: stream model response through ModelRuntime
    Models-->>Loop: normalized assistant stream
    Loop-->>Session: ordered message and tool events
    Session-->>UI: coding-agent session events
```

**Verified:** `AgentSessionRuntime` owns replacement and rebinding of the
current `AgentSession` plus cwd-scoped services. `InteractiveMode` consumes the
runtime and calls its session. `AgentSession` constructs and subscribes to an
`Agent`; no production construction or invocation of `AgentHarness` was found.

Source anchors:

| Boundary | Pi source | Evidence |
|---|---|---|
| Composition root | `packages/coding-agent/src/main.ts:632-743,813-841` | Creates runtime and selects the interactive entrypoint. |
| Session lifecycle wrapper | `packages/coding-agent/src/core/agent-session-runtime.ts:74-216` | Owns current session and cwd-bound service replacement. |
| Product session | `packages/coding-agent/src/core/agent-session.ts:277-360` | Owns the coding-agent-specific `Agent` and event subscription. |
| UI consumer | `packages/coding-agent/src/modes/interactive/interactive-mode.ts:441-453` | Reads and drives the runtime session. |
| SDK construction | `packages/coding-agent/src/core/sdk.ts:289-385` | Builds the coding-agent session exposed to callers. |

### Reusable Agent SDK path

`packages/agent/src/harness/agent-harness.ts:157-826` defines an independent
`AgentHarness`. It assembles resources, tools, system prompt, provider hooks,
session writes, compaction, and tree navigation around the same low-level
`Agent` and `runAgentLoop`.

**Verified:** the harness provides a coherent alternative orchestration layer,
but it is outside the shipped coding-agent CLI path at this revision. Its
contracts must therefore be cited as SDK evidence, not current product
behavior.

This separation matters because the two paths differ in storage timing, hook
names, compaction control, and tree-leaf persistence.

## Turn Kernel

### Two-level loop

`packages/agent/src/agent-loop.ts:155-275` implements an imperative two-level
loop:

1. The inner loop streams one assistant response, executes tool calls, and
   checks queued steering messages before another model request.
2. The outer loop checks follow-up messages only when the agent would otherwise
   stop.
3. `error` and `aborted` stop reasons end the run immediately.
4. A `length` stop reason fails every emitted tool call instead of executing
   possibly truncated arguments.
5. `prepareNextTurn` may replace context, model, thinking level, and tools after
   a completed turn.

The truncation rule is the most important safety property: a syntactically
plausible but incomplete mutation or shell command is never treated as a valid
tool request. Focused coverage exists in
`packages/agent/test/agent-loop.test.ts:310-366,528-614,655-699`.

### Steering and follow-up are queue points, not preemption

`Agent.steer()` and `Agent.followUp()` enqueue messages in
`PendingMessageQueue`; the queue can return all messages or one at a time.

- **Steering:** inserted after the current assistant response and its tool calls
  finish, before the next model request.
- **Follow-up:** considered only when the current run has no remaining tool call
  or steering continuation.
- **Abort:** a separate operation that aborts the active `AbortController`.

Therefore Pi steering does not interrupt an in-flight model stream or tool
batch. Calling it an "interrupt" would create the wrong TUI and cancellation
contract. Evidence: `packages/agent/src/agent.ts:123-157,274-310,401-466` and
`packages/coding-agent/src/core/agent-session.ts:1323-1388`.

### Tool preparation and execution

`executeToolCalls` in `packages/agent/src/agent-loop.ts:413-754` separates tool
preparation from execution:

1. Locate the tool and transform arguments.
2. Validate the schema.
3. Run `beforeToolCall`; it may block, fail, or replace arguments.
4. Emit start events and retain executable thunks.
5. Execute the prepared calls.
6. Run `afterToolCall`; it may rewrite content, details, error state, or request
   termination.

The concurrency rule is deliberately coarse. If global mode is sequential, or
if any call in the batch names a tool with `executionMode: "sequential"`, the
whole batch is serial. Otherwise all calls are prepared in model order and then
executed with `Promise.all`; results are emitted in model order. This differs
from Eino-Agent's existing mixed-batch behavior and should not be copied
without a user-visible reason.

### Event ownership

`Agent` reduces each event into state and then awaits subscribed listeners in
registration order. Its public lifecycle covers agent, turn, message, and tool
start/update/end events. `agent_end` is the final protocol event, but the agent
does not become fully idle until listeners have settled.

This gives UI consumers a deterministic stream, while slow or reentrant
listeners can delay lifecycle completion. The harness adds `save_point` and
`settled`; the coding-agent extension runner has a different event vocabulary.
Those event families should not be described as one universal Pi protocol.

## Session And Durability

Pi contains two related but distinct tree-session implementations.

### Coding-agent `SessionManager`

The production `SessionManager` in
`packages/coding-agent/src/core/session-manager.ts:791-1409` stores append-only
JSONL entries linked by `id` and `parentId`. It maintains the selected `leafId`
in memory and reconstructs context by walking the root-to-leaf path.

Verified properties:

- Product entry kinds include messages, model/thinking changes, compaction,
  branch summaries, custom records, labels, and session metadata. There is no
  durable `leaf` entry in this union.
- `branch()` moves the in-memory leaf. The new branch becomes durable only when
  a subsequent child entry is appended.
- On reload, the last appended entry becomes the leaf.
- New session output is withheld until an assistant message exists, avoiding
  empty files for prompts that never receive a response.
- Compaction and branch-summary entries affect context reconstruction without
  destroying older branches.

The tree delivers useful non-destructive navigation, but the implicit leaf and
lazy first flush are recovery contracts, not storage trivia. A crash after
moving the leaf but before appending a child cannot persist that navigation.

### Harness session storage

The generic harness uses its own session abstraction and JSONL storage in
`packages/agent/src/harness/session/`. Its header and leaf pointer are persisted
through the harness storage API, and compaction/tree navigation are restricted
to idle state. Tree navigation can summarize the abandoned path before moving
to another node.

This is not equivalent to the production manager's lazy-flush behavior. Any
Eino-Agent tree-session design must choose one durable leaf rule explicitly
instead of combining the two descriptions.

## Extension And Trust Boundaries

Pi's coding-agent extension runner can register tools, providers, commands, UI
components, and lifecycle handlers. Handlers run in sequence; cancellable
session events can stop an operation, while extension exceptions are recorded
and processing continues. The harness separately exposes provider-payload and
tool hooks.

The capability surface is powerful, but its cost is large:

- payload mutation can violate provider normalization assumptions;
- UI actions and runtime hooks introduce reentrancy concerns;
- ordering becomes public behavior once several extensions compose;
- continuing after extension failure requires explicit partial-failure rules.

Pi does have a project-trust gate. It controls whether project-local settings,
extensions, skills, and package storage may be loaded from `.pi` and
`.agents/skills`; see `packages/coding-agent/src/core/trust-manager.ts:29-205`
and `packages/coding-agent/src/main.ts:603-678`.

**Verified exclusion:** no per-tool ask/allow/deny permission coordinator was
found in the inspected core path. Project trust protects loading executable
project resources; it is not a substitute for mutation or shell permissions.
The audit does not establish that Pi relies on external containerization.

## Provider Layer

The modern provider owner is `Models` in `packages/ai/src/models.ts:109-507`:

- provider adapters own stream conversion;
- central model resolution supplies auth, headers, base URL, and metadata;
- the normalized stream exposes text, thinking, tool-call, completion, error,
  and abort states;
- dynamic credential resolution supports expiring credentials at request time.

`packages/ai/src/compat.ts` explicitly labels the older global surface as
temporary compatibility. It is migration debt, not a target architecture.

The reusable design is provider-owned protocol adaptation plus centrally owned
credential/model resolution. Eino-Agent should not copy a wide compatibility
surface merely because it exists.

## TUI And Terminal

### Renderer

`TUI` in `packages/tui/src/tui.ts:295-835,1360-1445` is a custom component tree
and renderer, not a Bubble Tea model:

- render requests are coalesced and capped at a 16 ms minimum interval;
- output is compared with `previousLines`;
- the renderer rewrites the changed span and performs a full redraw when width,
  height, cursor, or shrink conditions require it;
- Kitty image identifiers and reserved rows are tracked for cleanup;
- overlays support anchored/percentage placement, z-order, focus capture, and
  focus restoration;
- a hardware cursor marker supports IME placement.

It is more precise to call this changed-span rendering than "only write changed
lines": clearing, shrinking, image cleanup, and terminal changes can force a
larger redraw.

### Terminal lifecycle

`ProcessTerminal` in `packages/tui/src/terminal.ts:99-249` owns raw mode,
bracketed paste, focus events, Kitty keyboard negotiation with fallback,
terminal queries, stdin draining, and progress OSC sequences.

Output ultimately calls `process.stdout.write` synchronously. Pi does not use a
dedicated frame-writer queue comparable to Grok Build. Its renderer reduces
bytes written, but it does not define an explicit slow-terminal backpressure
policy.

### Interactive composition

`InteractiveMode` composes the chat transcript, streaming assistant content,
pending tool state, editor, completion, status, widgets, and extension UI. Its
large coordination surface demonstrates why Eino-Agent should keep runtime
truth in reducer-ready events rather than let one view own execution semantics.

## Subagent Boundary

**Verified exclusion:** no first-class Task tool, child-session lifecycle,
join/wait protocol, or subagent runtime was found in Pi's coding-agent or agent
core at this snapshot. A third-party extension can build one, but that does not
provide a core contract for cancellation, permissions, persistence, or TUI
switching.

Pi is therefore not a subagent-runtime reference for Eino-Agent. Its event and
tree-session ideas can support subagent UX, but the parent/child semantics must
remain project-native.

## Distinctive Strengths And Hidden Costs

| Design | User value | Hidden ownership and cost |
|---|---|---|
| Small imperative loop | Easy to reason about turn and tool ordering. | Queue points, listener completion, and abort semantics become API contracts. |
| Fail-closed truncated tool calls | Prevents execution of incomplete arguments. | Provider stop reasons must normalize reliably. |
| Tree sessions | Non-destructive branch, fork, compaction, and navigation. | Leaf recovery, context reconstruction, migration, summary provenance, and UI navigation must agree. |
| Wide extensions | Product behavior can evolve without rebuilding the core. | Capability control, ordering, reentrancy, compatibility, and failure isolation expand sharply. |
| Custom changed-span TUI | Good control over overlays, images, IME, and slow redraws. | ANSI, cursor, resize, image, focus, and terminal cleanup become a maintained subsystem. |
| Provider-owned adapters | Keeps protocol quirks out of the agent loop. | Compatibility metadata and credential refresh need versioned tests. |

## Comparison With Eino-Agent

Eino-Agent's current owners are documented in
[`query-engine.md`](../../../architecture/runtime/query-engine.md),
[`model-and-tool-execution.md`](../../../architecture/runtime/model-and-tool-execution.md),
[`sessions.md`](../../../architecture/state/sessions.md), and
[`runtime-events.md`](../../../architecture/tui/contracts/runtime-events.md).

| Concern | Pi snapshot | Eino-Agent current direction | Consequence |
|---|---|---|---|
| Runtime authority | `Agent` plus `runAgentLoop`; product session wraps it. | `engine/query.go:queryLoop` remains the production authority. | Improve contracts in place; do not insert a harness layer solely for similarity. |
| Queue semantics | Steering after a full assistant/tool turn; follow-up at natural stop. | Input queue and child pause points are project-owned. | Adopt names only after mapping exact safe points and cancellation behavior. |
| Tool concurrency | One sequential tool serializes the whole batch. | Existing execution supports project-owned batching. | Preserve current behavior unless a focused trace proves a safety gap. |
| Session model | Append-only tree with implicit production leaf. | Current session/transcript contracts are linear and separately owned. | Tree migration requires schema, recovery, and replay design before implementation. |
| Events | Fine-grained loop events; SDK and product hooks differ. | Typed runtime events feed CLI/TUI projections. | Combine lifecycle coverage, not event names or extension authority. |
| Permissions | Project resource trust; no observed per-tool coordinator. | Permission coordination is an explicit engine boundary. | Preserve Eino-Agent's stronger permission ownership. |
| TUI | Custom renderer and component tree. | Bubble Tea reducer/projection. | Reuse requirements and tests, not renderer code or architecture. |
| Subagents | No first-class core runtime. | Tasks, agents, identity, cancellation, and progress already have owners. | Keep the subagent contract project-native. |

## Adoption Decisions

| Decision | Scope | Rationale and required proof |
|---|---|---|
| `adapt` | Turn kernel | Add explicit fail-closed truncated tool-call coverage and freeze queue-point semantics through golden traces for abort, steering, follow-up, serial, and parallel calls. |
| `combine` | Runtime events | Combine Pi's lifecycle granularity with Eino-Agent identity and terminal events; verify one ordered trace across CLI, TUI, and ACP. |
| `defer` | Tree session and branch summary | First specify durable leaf, crash recovery, fork, compaction provenance, transcript migration, and replay. |
| `adapt` | Provider boundary | Prefer provider-owned stream normalization and central credential resolution; do not target Pi's temporary global compatibility surface. |
| `reject` | Renderer migration | Do not replace Bubble Tea with `pi-tui`; translate changed-span, overlay-focus, IME, and terminal-cleanup behavior into project-native tests. |
| `project-native` | Subagent runtime | Pi supplies no complete parent/child contract; retain Eino-Agent's own identity, permission, cancellation, persistence, and projection model. |
| `preserve` | Permission authority | Keep Eino-Agent's explicit per-tool permission coordination while treating project-resource trust as a separate loading concern. |

**Overall verdict: `combine`.** Pi should influence observable turn, event,
provider, and session requirements without becoming a replacement runtime or
TUI architecture. This report does not schedule any PLAN item.

## Related Reports

- [`grok-build.md`](grok-build.md)
- [`claude-code-ripe.md`](claude-code-ripe.md)
- [`codex.md`](codex.md)
- [`crush.md`](crush.md)
- [`opencode.md`](opencode.md)
- [`eino-agent-2026-07-10.md`](eino-agent-2026-07-10.md)
- [`modern-coding-agent-synthesis.md`](modern-coding-agent-synthesis.md)
- [`subagent-runtime.md`](subagent-runtime.md)
