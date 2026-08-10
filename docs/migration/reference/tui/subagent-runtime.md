# TUI Sub-Agent Runtime Reference Research

**Status:** reference-snapshot
**Date:** 2026-07-10
**Scope:** `.reference/claude-code-ripe`, `.reference/crush`, `.reference/codex`, `.reference/opencode`, and current `eino-agent`

> **Ownership:** snapshot research that led to the completed sub-Agent/TUI
> program; current behavior lives in
> [`tasks-and-agents.md`](../../../architecture/runtime/tasks-and-agents.md) and
> [`architecture/tui/README.md`](../../../architecture/tui/README.md)

> **OpenCode audit (2026-07-12):** corrected the V1/V2 ownership boundary,
> ordered-array complexity, built-in agent modes, experimental background-job
> status, and child-session navigation against snapshot `9976269ab`.

> **Implementation follow-up (2026-07-10):** Tier 0.1 added the identified
> event/reducer foundation and Tier 0.2 closed the live-progress gap described
> below. This report retains the audited baseline; current implementation status
> is documented in
> [`architecture/runtime/tasks-and-agents.md`](../../../architecture/runtime/tasks-and-agents.md)
> and [`architecture/tui/README.md`](../../../architecture/tui/README.md).

> **Post-snapshot addendum (2026-07-17):** Pi and Grok Build were audited after
> this 2026-07-10 baseline. Their corrected source reports are
> [`pi.md`](pi.md) and [`grok-build.md`](grok-build.md). The addendum below
> records only the effect of that later evidence; it is not part of the
> original snapshot.

## Post-Snapshot Pi And Grok Build Evidence

The later audits preserve four conclusions relevant to the sub-Agent runtime:

- Pi's shipped coding-agent path is `AgentSessionRuntime -> AgentSession ->
  Agent`; its reusable `AgentHarness` is a separate SDK path. Session-tree and
  queue semantics are useful evidence, but must not be described as current
  CLI harness wiring.
- Pi steering and follow-up inputs are consumed at explicit safe boundaries.
  They support ordered interaction with a running child, not preemptive
  interruption of an in-flight model or tool call.
- Grok Build provides stronger evidence for an explicit child-session
  lifecycle, replayable child views, and one owner for session mutation. Its
  unbounded channels avoid producer suspension but do not provide
  backpressure, and its goal harness is opt-in rather than the default loop.
- Grok Build's terminal path supports a drain-before-restore invariant. The
  audit did not verify a general "only changed lines" rendering algorithm, so
  that mechanism is not an accepted project contract.

These findings refine future work without reopening the completed M0-M7
snapshot. The accepted project-owned contracts and execution boundaries live
in [`P14`](../../plans/p14-async-child-interaction.md) and
[`P15`](../../history/runtime/p15-terminal-output-resilience.md).

## Detailed Report Set

This file remains the focused research note for the first sub-Agent runtime
slice. The complete TUI audit and broader modernization decision are now split
into the following source-backed reports:

- [`claude-code-ripe.md`](claude-code-ripe.md) at local
  reference commit `4b9d30f79532`
- [`codex.md`](codex.md) at local reference commit
  `64bdeed9f7ad`
- [`crush.md`](crush.md) at local reference commit
  `d20e29ae7500`
- [`opencode.md`](opencode.md) at local reference commit
  `9976269ab`
- [`eino-agent-2026-07-10.md`](eino-agent-2026-07-10.md) at repository commit
  `630750541ea1`
- [`pi.md`](pi.md) at local reference commit `c6d8371521fc`
- [`grok-build.md`](grok-build.md) at local reference commit `b189869b7755`
- [`modern-coding-agent-synthesis.md`](modern-coding-agent-synthesis.md)
  for the comparison, target architecture, delivery milestones, and acceptance
  metrics

When this focused note and the detailed reports differ in scope, the detailed
reports own the full TUI assessment. The implementation checklist is maintained
in [`migration/history/tui/m0-m7-refinement-plan.md`](../../history/tui/m0-m7-refinement-plan.md).

## Purpose

This document records the reference-design findings that led to the completed
TUI M0-M7 and sub-agent runtime program. It is retained as research history,
not a current completion claim. Current implementation status belongs in the
subsystem tracker, and post-parity work belongs in
[`migration/PLAN.md`](../../PLAN.md).

The user-facing target is:

- Background sub-agents show useful live progress in the TUI.
- A user can inspect a sub-agent trace/transcript, not only a final summary.
- A user can switch attention to a sub-agent and send follow-up input.
- Parent, child, tool call, session, transcript, and output-file lineage remain
  understandable and durable enough for resume/debugging.

## `eino-agent` Baseline at the Snapshot

The Go port already has important foundations:

- `tools/agent_runner.go`
  - Tracks `RunningAgent`, status, messages, output file, pending messages,
    worktree fields, and bounded `AgentProgress`.
  - Supports background execution, terminal notifications, progress polling,
    `SendOrResumeAgentMessage`, and persisted metadata/transcripts for retained
    or evicted local agents.
- `engine/subagent.go`
  - Creates an isolated `QueryEngine` and runs the sub-agent query loop.
  - Collects final assistant output, turn count, tools used, and messages.
  - Currently does not update `AgentRunner` progress while events are streaming.
- `engine/query.go`
  - Drains agent notifications and progress events on the main thread at
    queue-drain boundaries.
  - Drains pending `SendMessage` payloads into sub-agent query loops at
    tool-round boundaries.
- `engine/app_state_tasks.go`
  - Provides a bounded engine-owned task/AppState snapshot for local tasks and
    local agents.
- `internal/tui/background_tasks.go` and `internal/tui/teams.go`
  - Render runtime task/agent snapshots.
  - Currently read `tools.RuntimeTaskSnapshotCurrent()` directly, not the
    engine-owned AppState snapshot.
  - Agent detail output is summary/metadata oriented, not transcript/trace
    oriented.
- `tools/agent_lifecycle.go` and `tools/agent_steering.go`
  - Define display state, progress stream, transcript access, pause/resume, and
    priority controls.
  - These are mostly test-covered primitives and are not yet wired into the
    live TUI/sub-agent execution path.

The main baseline conclusion: the data model exists in fragments, but there is
not yet one canonical, live, replayable agent thread/task state feeding the TUI.

## Reference: `claude-code-ripe`

Primary files inspected:

- `src/tasks/LocalAgentTask/LocalAgentTask.tsx`
- `src/components/tasks/BackgroundTasksDialog.tsx`
- `src/components/tasks/AsyncAgentDetailDialog.tsx`
- `src/state/AppStateStore.ts`
- `src/state/teammateViewHelpers.ts`

Design observations:

1. A local/background agent is modeled as task state, not just a goroutine.
   It carries `agentId`, selected agent/type, prompt, progress, result, messages,
   pending messages, `isBackgrounded`, retention flags, disk-loaded state, and
   output/transcript paths.

2. Progress is updated from observed messages while the agent is running.
   Tool activities, token counts, recent activity, and summary are all part of
   the task-facing progress state.

3. Foreground/background is a display and lifecycle concern.
   A foreground local agent can be backgrounded and later inspected. AppState
   tracks `foregroundedTaskId` and `viewingAgentTaskId`.

4. The TUI detail view is task-state driven.
   The background task dialog and async agent detail view show status, elapsed
   time, tokens/tools, recent progress, prompt/plan, result, and error state.

5. Retention matters for inspection.
   Viewing a local agent can retain its messages to avoid evicting the state the
   user is inspecting.

Applicable lesson for `eino-agent`:

- Do not start with a purely visual TUI rewrite. First make the agent runtime
  expose a live task state with progress, messages, output file, display mode,
  and retention semantics.

## Reference: `crush`

Primary files inspected:

- `internal/agent/agent_tool.go`
- `internal/backend/agent.go`
- `internal/ui/model/session.go`
- `internal/ui/chat/agent.go`

Design observations:

1. The agent tool naturally owns nested tool trace.
   `AgentToolMessageItem` can hold nested tool calls and render them under the
   parent agent tool call.

2. Backend dispatch is asynchronous.
   Message submission returns quickly; the workspace/session owns the run
   lifetime, and terminal/errors are delivered by notifications/completions.

3. UI session switching is explicit.
   The UI model can load/switch sessions and report the current session without
   blocking on backend execution.

Applicable lesson for `eino-agent`:

- Keep the global background panel, but also plan for nested sub-agent trace
  under the parent `Agent` tool call. This is the clearest way to understand
  "what the sub-agent did" from the main chat.

## Reference: `codex`

Primary files inspected:

- `codex-rs/tui/src/app/thread_events.rs`
- `codex-rs/tui/src/app/thread_routing.rs`
- `codex-rs/tui/src/app/session_lifecycle.rs`
- `codex-rs/tui/src/app/agent_navigation.rs`
- `codex-rs/tui/src/multi_agents.rs`
- `codex-rs/tui/src/bottom_pane/pending_thread_approvals.rs`
- `codex-rs/core/src/tools/handlers/multi_agents_v2/spawn.rs`
- `codex-rs/core/src/tools/handlers/multi_agents_v2/message_tool.rs`
- `codex-rs/core/src/tools/handlers/multi_agents_v2/list_agents.rs`
- `codex-rs/core/src/tools/handlers/multi_agents_v2/wait.rs`
- `codex-rs/core/src/tools/handlers/multi_agents_v2/interrupt_agent.rs`
- `codex-rs/core/src/agent/control.rs`
- `codex-rs/core/src/agent/control/spawn.rs`
- `codex-rs/core/src/agent/control/execution.rs`
- `codex-rs/core/src/context/subagent_notification.rs`
- `codex-rs/core/src/session_prefix.rs`
- `codex-rs/core/src/tools/tool_dispatch_trace.rs`
- `codex-rs/rollout-trace/src/model/session.rs`
- `codex-rs/rollout-trace/src/tool_dispatch.rs`
- `codex-rs/rollout-trace/src/reducer/tool/agents.rs`
- `codex-rs/core/tests/suite/subagent_notifications.rs`
- `codex-rs/core/tests/suite/agent_jobs.rs`

Design observations:

1. Root and child agents are both threads.
   The trace model uses `AgentThread` for both the root interactive session and
   spawned agents. Child origin records parent thread, spawn edge, task name, and
   role.

2. Agent addressing is path-based.
   `AgentPath` is the stable routing identity. Nicknames are presentation hints
   and must not be used as identity.

3. Multi-agent control is protocol-level, not only UI-level.
   `spawn_agent`, `send_message`, `list_agents`, `wait_agent`, and
   `interrupt_agent` are explicit tools with structured results and events.

4. Message delivery has two modes.
   `send_message` can queue only, while follow-up task delivery can trigger a
   turn. The inter-agent communication records author, target, message content,
   and source call ID.

5. Execution capacity is explicit.
   Sub-agent execution can be limited independently from root session operation.

6. Trace is append-only and reducer-backed.
   Tool dispatch emits start/end events. Multi-agent activity creates interaction
   edges between threads and tool calls. The reducer can later reconstruct
   threads, tool calls, messages, and delivery edges.

7. The TUI keeps a bounded event store per thread.
   Switching captures composer state, activates the target channel, rebuilds
   the chat from canonical turns plus buffered events, and replays only
   unresolved interactive requests. Closed Agent threads remain inspectable in
   stable first-seen order.

Applicable lesson for `eino-agent`:

- Short term: adopt the control-plane ideas with current Go primitives:
  stable agent ID/name routing, parent tool-use lineage, progress, transcript,
  send/resume, wait/interrupt/abort.
- Medium term: add an append-only trace slice with a reducer-like snapshot
  rather than stuffing all behavior into TUI structs.
- Do not immediately port Codex's full state DB or rollout trace. The smaller
  first step is a durable event vocabulary and an engine AppState reducer that
  can feed the TUI.

## Reference: `opencode`

Primary files inspected:

- `packages/core/src/background-job.ts`
- `packages/core/src/agent.ts`
- `packages/opencode/src/agent/agent.ts`
- `packages/opencode/src/agent/subagent-permissions.ts`
- `packages/opencode/src/tool/task.ts`
- `packages/opencode/src/session/prompt.ts`
- `packages/opencode/src/session/processor.ts`
- `packages/opencode/src/session/run-state.ts`
- `packages/opencode/src/session/tools.ts`
- `packages/opencode/src/permission/index.ts`
- `packages/opencode/src/question/index.ts`
- `packages/opencode/src/event-v2-bridge.ts`
- `packages/opencode/src/control-plane/workspace.ts`
- `packages/tui/src/context/event.ts`
- `packages/tui/src/context/sdk.tsx`
- `packages/tui/src/context/sync.tsx`
- `packages/tui/src/component/prompt/index.tsx`
- `packages/tui/src/routes/session/index.tsx`
- `packages/tui/src/routes/session/subagent-footer.tsx`
- `packages/tui/src/routes/session/permission.tsx`
- `packages/tui/src/routes/session/question.tsx`

Design observations:

1. Agents are configurations, not runtime objects.
   The default agent is `build`; `general` and `explore` are built-in
   subagents. `plan` is primary, while `compaction`, `title`, and `summary` are
   hidden primary agents. Custom `subagent`/`all` agents can be advertised to
   `task` when permission allows.

2. Subagents are child sessions with permission inheritance.
   `subagent-permissions.ts` propagates parent `deny` and
   `external_directory` restrictions while allowing the subagent's own
   capabilities.

3. Experimental background jobs use extend/promote/wait primitives.
   The `BackgroundJob` registry tracks process-local jobs with `Deferred`
   promises. Jobs can be extended or promoted, but model-visible background
   mode requires `OPENCODE_EXPERIMENTAL_BACKGROUND_SUBAGENTS`.

4. V2 event sourcing has a durable/live split.
   Durable events (`Text.Ended`, `Tool.Called`) are persisted to SQLite with
   optimistic concurrency. Live deltas (`Text.Delta`) are SSE fragments only.
   The current TUI still consumes the V1 message/part projection and ignores the
   V2 `sync` replication envelope, so the paths must not be described as fully
   converged.

5. Deferred-promise blocking for permissions and questions.
   `Permission.ask()` and `Question.ask()` return Effect values awaiting a
   `Deferred`. Tool execution suspends until the user replies, with cascading
   approval/rejection within the session.

6. Part-centric message rendering.
   Assistant messages are arrays of typed `Part`s (text, reasoning, tool,
   patch) dispatched via a registry. This avoids reparsing markdown and
   enables per-type streaming and rich tool widgets.

7. Bounded ordered reactive store with binary-search lookup.
   `SyncContext` uses ID-keyed records containing sorted arrays. Lookup is
   `O(log n)`, while insertion/removal remains `O(n)`; message hydration and
   live retention are bounded to 100 messages per session.

8. Child transcript navigation is first class.
   Parent task rows can open child sessions; commands cycle children and return
   to the parent, and `task_id` resumes a prior child. The top-level session
   picker filters children, so lineage navigation is the intended route.

Applicable lesson for `eino-agent`:

- Already satisfied: semantic typed history/tool renderers, structured
  questions, per-thread drafts, command routing, child transcript switching,
  follow-up/resume, and focus-aware cross-thread attention.
- Completed in P9.1: a bounded repeated-identical-tool confirmation guard uses
  canonical name/coerced-input hashing and focused false-positive tests.
- Completed in P9.2: the root/child lineage uses one
  engine-owned exactly-once permission lifecycle, then re-evaluate each pending
  request against a committed session/always grant and its own scope. Do not
  copy OpenCode's blanket same-session rejection cascade into a multi-thread
  runtime. See
  [`permission-coalescing.md`](../parity/permission-coalescing.md).
- Defer SQLite event sourcing and store rewrites until profiling or a measured
  multi-process recovery gap justifies them.
- Do not port the full Effect-TS runtime or OpenTUI renderer. The
  transferable concepts are typed lifecycle boundaries and scoped interaction
  semantics, not the exact runtime or store representation.

## Design Synthesis

The common pattern across the references:

1. A sub-agent is a routable runtime object.
   It has identity, status, messages, task prompt, progress, result, and parent
   lineage.

2. TUI views should be projections, not owners.
   Panels and detail views should read from engine/runtime state. They should not
   maintain the only copy of task/agent status.

3. Progress should be event-derived.
   Sub-agent progress should be updated while the sub-agent query loop consumes
   assistant/tool events, not inferred only after completion.

4. Message routing should be model-visible and UI-visible.
   The same underlying send/resume path should support the `SendMessage` tool and
   TUI follow-up input.

5. Trace should be append-only before it is beautiful.
   A compact event log is enough to unlock replay, nested rendering, detail
   panels, and session debugging.

## Historical `eino-agent` Direction

The P0-P2 direction below has been implemented and is retained as design
history. It is not the current execution queue.

### P0: Runtime Event Identity and Reducer

Before adding thread switching, extend the current event contract with stable
session/thread/turn/Agent identity, sequence, timestamp, and parent/causal
fields. Reduce those events into a bounded engine-owned per-thread snapshot.

Acceptance:

- The same leader-thread event sequence rebuilds an identical snapshot in a
  deterministic replay test.
- A child Agent receives its stable thread and parent tool-use identity before
  execution starts.
- Terminal and unresolved interactive state survive bounded live-event
  eviction through canonical transcript/request state.

### P0: Real-Time Sub-Agent Progress

Wire `engine/subagent.go` event consumption into `tools.AgentRunner.UpdateAgentProgress`:

- On stream/turn events, increment turn/activity state.
- On tool result events, update tool count, last tool, and recent activities.
- On assistant/tool summary availability, update bounded summary.
- Emit progress snapshots that existing `PollAgentProgress` can drain.

Acceptance:

- A running background agent appears with non-empty progress while still running.
- The TUI can show last tool/recent activity without waiting for completion.
- Existing `AgentRunner` progress tests remain valid; add a sub-agent executor
  progress test.

### P0: Engine AppState as TUI Source

Make `BackgroundTasksPanel` and `TeamsPanel` prefer engine-owned AppState:

- App periodically syncs `tools.RuntimeTaskSnapshotCurrent()` into
  `QueryEngine.SyncAppStateTasksFromRuntimeSnapshot`.
- Panels consume `AppStateSnapshot().Tasks` or a TUI adapter over that snapshot.
- Direct global runtime snapshot reads become fallback paths only.

Acceptance:

- `/tasks`, Ctrl+T, Ctrl+B, and `/team` derive from a consistent task row model.
- Runtime snapshot and engine event state converge deterministically.

### P1: Agent Detail Trace and Transcript View

Use existing transcript/message access instead of summary-only output:

- Overview: status, elapsed, tokens/tools, output file, worktree, pending count.
- Trace: recent tool activities and eventually nested tool calls.
- Messages: current agent transcript/messages.
- Output: final result or persisted output.

Acceptance:

- Opening a running agent detail view shows at least current messages and recent
  activities.
- Completed/failed agents expose final result/error plus transcript.

### P1: TUI Send/Resume Input

Expose a follow-up action from agent detail:

- Reuse `DefaultAgentRunner.SendOrResumeAgentMessage`.
- Append the user message locally for immediate visibility.
- If the agent is stopped/completed, resume in background with prior messages.

Acceptance:

- Sending to a running agent queues a message consumed at the next tool-round
  boundary.
- Sending to a retained/evicted completed agent starts the existing resume path.

### P1: Parent/Child Session Lineage

Persist and display:

- Parent session ID.
- Parent tool use ID.
- Agent ID/name/type.
- Output file and transcript file.
- Spawn mode/isolation/worktree.

Acceptance:

- Agent metadata exists shortly after launch, not only after completion.
- TUI detail can show parent tool/session lineage for running agents.

### P2: Foreground/Background and Steering

Wire existing primitives:

- `AgentDisplayState` becomes live state, not only tests.
- `AgentSteering.WaitIfPaused` is checked at safe query-loop/tool-round
  boundaries.
- TUI can pause/resume/abort and optionally focus an agent as the active detail
  target.

Acceptance:

- Pause/resume affects actual sub-agent execution.
- Foreground selection is represented in runtime state, not only panel cursor.

### P2: Append-Only Agent Trace

Introduce a small trace vocabulary before a full reducer:

- `agent_started`
- `agent_progress`
- `agent_message_queued`
- `agent_message_delivered`
- `agent_tool_started`
- `agent_tool_finished`
- `agent_completed`
- `agent_failed`
- `agent_aborted`

Acceptance:

- Trace events can rebuild a bounded per-agent snapshot in tests.
- Nested trace rendering can use parent tool-use IDs and agent IDs.

## Non-Goals for the First Slice

- Do not port Codex's full rollout trace database in one step.
- Do not replace Bubble Tea rendering.
- Do not redesign every task API before wiring the existing AgentRunner.
- Do not make TUI panels the source of truth.
