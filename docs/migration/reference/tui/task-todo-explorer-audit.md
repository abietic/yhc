# Task and Todo Explorer Reference Audit

**Status:** reference-snapshot
**Snapshot:** 2026-07-27; Eino-Agent `b4b5cfb21132`, Claude Code Ripe
`4b9d30f79532`, Codex `66bd101fff6f`, OpenCode `411eff73f026`, Crush
`2af939d8e900`, Grok Build `a5727c596045`, and Pi `c55ae2faa5d8`
**Current-source addenda:** 2026-07-31; Eino-Agent `fe9625349d9`,
`9460656ee808`, `2c57115bd8da`, and `47f49b9a37b`

> **Ownership:** source comparison for the Task/Todo runtime facts and TUI
> experience needed by G33 and P31. Current Eino-Agent behavior belongs in
> [`tasks-and-agents.md`](../../../architecture/runtime/tasks-and-agents.md)
> and [`architecture/tui/README.md`](../../../architecture/tui/README.md);
> accepted implementation belongs in
> [`p31-task-todo-explorer.md`](../../plans/p31-task-todo-explorer.md).

## Result

The useful reference pattern is not a more decorative Task row. It is a
separation of three responsibilities:

1. a logical plan item records intended work, ordering, owner, and outcome;
2. an execution instance records a child Agent's identity, lineage, progress,
   result, error, and controls; and
3. a bounded UI projection joins them without becoming either state owner.

Current Eino-Agent has most execution facts and a partial shared selector, but
its plan facts are split between `tools.TaskManager` and a process-local
`TodoWrite` map. Ctrl+T, Ctrl+B, `/team`, the wide sidebar, and tool history
then expose different detail and control sets. The P31 design should converge
the state boundary before expanding the component.

The reader should be able to use this report to answer which reference
mechanisms support that conclusion and which mechanisms are deliberately
excluded. Refresh this report when one of the named snapshots changes or when
Eino-Agent replaces the owners linked above.

## Observable Question and Evidence Boundary

**Question:** Which engine-owned Task/Todo facts, lifecycle transitions, and
user controls must a TUI project during live execution and replay?

The audit inspected direct implementation, callers, and tests where present.
Existing comparison documents were navigation only. “Not found” means absent
from the named local snapshot after source search; it is not a statement about
a newer upstream revision.

The following are outside the evidence boundary:

- recovery that automatically takes over a live child process after a crash;
- arbitrary human editing of model-maintained plan items in the TUI;
- hosted dashboards, multi-process team coordination, or vendor services; and
- background shell convergence where Eino-Agent lacks one canonical
  QueryEngine task projection.

## What Eino-Agent Does at This Snapshot

| Fact | Verified owner and consequence |
|---|---|
| Structured task records | A root QueryEngine lineage injects one [`tools.TaskManager`](../../../../tools/task_store.go#L64) into TaskCreate/Get/List/Update/Stop/Output. Child engines share it; independent roots do not. Lifecycle events reach the runtime read model. |
| Todo checklist | [`TodoWriteTool`](../../../../tools/todo_write.go#L192) replaces a package-global list scoped by trusted Session and Agent IDs. All-complete clears the active list. It has no durable store or runtime selector input. |
| Agent execution | [`tools.AgentRunner`](../../../../tools/agent_runner.go) owns child identity, generation, progress, message, pause/resume/abort, terminal state, transcript, and retained/evicted inspection. |
| Shared read path | `QueryEngine.TaskAgentSnapshot` gave canonical runtime Agent/task rows precedence and excluded package-global fallback state. Todo was not a row. P31.5 later deleted this historical selector. |
| Ctrl+T | [`buildTaskPanelLines`](../../../../internal/tui/app.go#L5806) shows a bounded status summary. Its keys only scroll, refresh, and close. |
| Ctrl+B | [`BackgroundTasksPanel`](../../../../internal/tui/background_tasks.go#L25) adds Agent detail/transcript/control and local-task output/stop. It still reads local-task output through the root task manager. |
| Other views | `/team` and the wide sidebar consume the shared selector for smaller read-only projections. Task/Todo tool history renders the mutation call, not current board truth. |

Two reproduced mismatches matter to the design:

- a local task in `in_progress` is stoppable through `TaskStop`, while the
  Background Tasks UI currently enables its local-task stop path only for the
  literal `running` state; and
- completing an Agent execution and completing a Task/Todo plan item are
  unrelated mutations, yet the current UI offers no stable relation with which
  to explain that difference.

## Plan-Item Evidence

| Reference | Verified mechanism | Useful consequence | Boundary |
|---|---|---|---|
| Claude Code Ripe | `src/utils/tasks.ts` persists plan items with stable IDs, subject, description, `activeForm`, owner, dependencies, metadata, and `pending/in_progress/completed`. `TaskListV2` prioritizes recent completion, active work, unblocked pending work, then older completion and reports hidden counts. | A plan list needs stable identity, dependency-aware ordering, current activity, and honest truncation. | Its separate runtime type is also named `Task`; that naming collision should not be copied. |
| OpenCode | `SessionTodo.Service` in `packages/opencode/src/session/todo.ts` persists an ordered per-session list and publishes `todo.updated`. `session-todo-dock.tsx` shows done/total plus the active or next item before expansion. | A compact, continuously useful current-activity projection is more valuable than showing the last TodoWrite JSON. | Its Todo schema has no stable per-item ID in this snapshot, so replacement and rename identity are insufficient for Eino-Agent replay. |
| Crush | `internal/session/session.go` persists `Todos`; `internal/agent/tools/todos.go` validates replacement and derives just-started/just-completed metadata. `activeForm` is explicitly the running phrase rather than the durable title. | Preserve separate title and active activity text, and make transitions visible without rewriting the title. | Content-based comparison is ambiguous after rename or duplicates. |
| Grok Build | `todo/mod.rs` stores an ordered map keyed by Todo ID, separates replace from merge, and supports `pending/in_progress/completed/cancelled`. `todo_pane.rs` supplies search, selection, copy, terminal filtering, and progress whose denominator excludes cancelled items. | Stable IDs, explicit mutation mode, a cancelled terminal, and derived progress remove replacement/replay ambiguity. | The resource/ACP storage implementation is specific to that runtime; only its observable contract is relevant. |
| Pi | The coding-agent README states that Todo is not built in. The example extension rebuilds a branch-local list from persisted tool-result details and renders at most five compact rows before expansion. | Append-only session evidence can rebuild extension state, and compact views should be intentionally bounded. | This is an example extension, not production runtime authority. |

Claude's Task list and OpenCode's Todo dock answer the same user question at
different richness levels: “what remains and what is active?” Grok adds the
identity and mutation rules needed for deterministic replay. None of them
justifies using a plan item's `owner` label as an execution identity.

## Execution-Instance Evidence

| Reference | Verified mechanism | Useful consequence | Boundary |
|---|---|---|---|
| Claude Code Ripe | `src/Task.ts` gives background executions type-prefixed IDs, `pending/running/completed/failed/killed`, tool-use causation, times, output file/offset, and terminal guards. `AgentTool/UI.tsx` projects a bounded activity tail, tools, tokens, duration, result, and error. `BackgroundTasksDialog` separates list and detail. | Execution identity, progress, terminal reason, persisted output, and list/detail views belong together. | Plan Task and execution Task are two independent types; same-name unification would hide the distinction. |
| Codex | `tui/src/multi_agents.rs` renders spawn/send/wait/close/resume from typed thread/agent state. `rollout-trace/.../agents.rs` reduces spawn and result edges with parent/child thread identity; tests preserve failed/cancelled results even without a final assistant message. | Parent-child lineage and terminal notification are replay facts, not UI inference. A missing final message must retain an execution/thread anchor. | Codex `ThreadGoal` is one goal with budget/accounting, not a Todo breakdown. |
| OpenCode | `tool/task.ts` creates or reuses a child session, sets `parentID`, narrows permission, bounds depth, cancels the child on interrupt, and delivers completion/error back to the parent. Regression tests cover child navigation. | The execution row should open the exact child transcript and preserve its parent relationship. | Background-job restart takeover was not established. |
| Grok Build | `task/types.rs` exposes `Initializing`, `Running`, `Completed`, `Failed`, and `Cancelled` snapshots. Task output can query or wait, and cancellation distinguishes cancelled, already finished, and not found. | Controls need engine-provided capabilities and fenced outcomes; the TUI must not infer cancelability from a display string. | Snapshot persistence across a process crash was not established. |
| Pi | The subagent example supports single, parallel, and chain subprocesses, streamed updates, abort, and compact/expanded rendering. | Parallel progress needs a bounded aggregate plus per-child detail. | External subprocess examples lack the project runtime lineage and recovery guarantees required by a built-in contract. |

## Reference Source Index

| Reference | Direct source and focused evidence |
|---|---|
| Claude Code Ripe | `.reference/claude-code-ripe/src/utils/tasks.ts`, `src/Task.ts`, `src/components/TaskListV2.tsx`, `src/components/tasks/BackgroundTasksDialog.tsx`, and `src/tools/AgentTool/UI.tsx` |
| Codex | `.reference/codex/codex-rs/tui/src/multi_agents.rs`, `codex-rs/rollout-trace/src/reducer/tool/agents.rs`, and `agents_tests.rs` beside it |
| OpenCode | `.reference/opencode/packages/opencode/src/session/todo.ts`, `src/tool/task.ts`, `packages/app/src/pages/session/composer/session-todo-dock.tsx`, and `packages/app/e2e/regression/subagent-child-navigation.spec.ts` |
| Crush | `.reference/crush/internal/session/session.go`, `internal/agent/tools/todos.go`, and `internal/session/session_test.go` |
| Grok Build | `.reference/grok-build/crates/codegen/xai-grok-tools/src/implementations/grok_build/todo/mod.rs`, sibling `task/types.rs`, `task_output/mod.rs`, `kill_task/mod.rs`, and `xai-grok-pager/src/views/todo_pane.rs` |
| Pi | `.reference/pi/packages/coding-agent/README.md`, `examples/extensions/todo.ts`, and `examples/extensions/subagent/index.ts` |

## Decision Matrix

| Design question | Evidence-supported answer | Observable consequence |
|---|---|---|
| One state type for plan and execution? | No. Keep logical work and execution attempts distinct, then join them by an explicit optional relation. | A successful Agent does not silently complete a plan item; a failed attempt does not erase the intended work. |
| One presentation source? | Yes. Build one bounded engine selector over canonical plan and execution owners. | Ctrl+T, Ctrl+B, `/team`, the sidebar, and tool-history summaries cannot disagree about status or allowed actions. |
| How is Todo compatibility retained? | Treat legacy TodoWrite as a scoped adapter into the logical work board, with stable IDs and explicit replace/merge behavior. | Existing replacement calls remain valid; omitted or completed items can leave durable terminal evidence rather than disappear from replay. |
| How are controls exposed? | The engine returns allowed actions bound to snapshot revision and execution generation. | Replay-only, terminal, stale, or unowned rows cannot accidentally message, resume, pause, or cancel live work. |
| What is always visible? | A compact done/total, current activity, active-execution count, and attention marker; full detail remains on demand. | Long lists do not consume the chat viewport, while failures and blocked work are not silently truncated. |
| What must remain detailed? | Output, error, terminal reason, lineage, activity tail, and child transcript use bounded detail readers. | The compact row remains cheap without discarding diagnostic evidence. |

## Consequences and Exclusions

The accepted contract should introduce project vocabulary:

- **WorkBoard**: one root-session-lineage plan scope;
- **WorkItem**: one durable logical outcome on that board;
- **Execution**: one Agent generation that may be linked to a WorkItem; and
- **TaskExplorerSnapshot**: a bounded read model with engine-declared actions.

It should not:

- infer an execution link from matching title, owner label, or list position;
- auto-complete a WorkItem when an execution returns successfully;
- add a fourth TUI-owned task store or retain several panels with different
  control rules;
- adopt a full Codex app-server, Claude multi-process file-lock protocol, Grok
  resource framework, or Pi extension process model; or
- promise live child takeover after restart. Cold restore is read-only until
  an explicit supported continuation creates a new generation.

Unresolved implementation measurements are the exact live/archive bounds and
whether user-authored plan mutation belongs in a later TUI workflow. P31 keeps
plan mutation model/tool-owned initially and requires the existing G11
viewport/performance gates before setting final numeric bounds.

## 2026-07-30 Promotion Audit Addendum

At the promotion-audit snapshot, current source confirmed that P31.1a could
remain reversible only if the first slice observed, rather than replaced, the
split owners:

- the root QueryEngine lineage injects one `TaskManager`, while TodoWrite still
  uses the trusted Session/Agent-scoped compatibility map;
- Task and Todo mutations had no common post-commit observer;
- Task dependency updates append/deduplicate without graph validation, while
  non-`running` status strings pass through and the documented `deleted`
  status does not currently delete a record;
- Session view state demonstrated a private same-directory temp/write/sync/
  close/rename pattern, but no existing writer owned a WorkBoard record or its
  deletion; and
- the existing tests cover many individual branches but did not previously
  freeze the logical-work mutation/read scenarios that a P31.1a shadow
  observer can reach in one fixture.

The promotion baseline therefore added an exact scenario fixture and
freezes one QueryEngine-lineage `WorkBoardShadow`, a private bounded
Session-owned sidecar, post-legacy-mutation observation, deterministic writer
failure injection, and exact sidecar removal. Those constraints were then
implemented and closed by the
[`P31.1a delivery`](../../history/runtime/p31-1a-reversible-workboard-shadow.md).
At this audit snapshot, root PLAN had no `Ready` slice. P31.1b still needed an
independent audit before any limit could become an authoritative rejection.

## 2026-07-31 P31.1b Promotion Audit Addendum

At snapshot `c258806d8486ac069ad6c949ac4c71bc521f44ea`, P31.1a is
complete but P31.1b is not yet executable. The audit asked one question: can a
single PR replace the split Task/Todo owners without an unresolved
compatibility, Session-lifecycle, cutover, or downgrade decision?

Current source answers part of that question:

- [`tools/p31_promotion_compatibility_test.go`](../../../../tools/p31_promotion_compatibility_test.go)
  freezes exact legacy results for arbitrary Task status, unresolved
  dependencies, aliases, Todo scope, replacement, and clearing;
- [`engine/internal/workboard/types.go`](../../../../engine/internal/workboard/types.go)
  intentionally rejects those non-canonical status and graph shapes;
- [`engine/internal/workboard/shadow.go`](../../../../engine/internal/workboard/shadow.go)
  owns only the validation-only shadow suffix and atomic writer stages;
- [`engine/session_service.go`](../../../../engine/session_service.go) owns the
  engine-facing resume/fork facade, while
  [`engine/session/delete.go`](../../../../engine/session/delete.go) recognizes
  only the P31.1a shadow; and
- neither current Session metadata nor WorkBoard files establish a minimum
  reader or an authority commit.

The audit therefore rejected immediate promotion. Reusing the shadow record
would either reject behavior that is currently accepted or quietly change it,
and independently writing a board, backup, and marker without one commit point
could expose an empty board or two writable owners after a crash.

The detailed
[`P31.1b promotion freeze`](../../plans/p31-task-todo-explorer.md#p311b-promotion-audit-freeze)
resolves the design gap with a project-owned combination:

| Decision | Result |
|---|---|
| Preserve | Exact Task/Todo input, result/error text, scoped replacement, lifecycle events, and non-QueryEngine fallback |
| Adapt | Store arbitrary status and missing dependency strings in a typed compatibility payload while the canonical graph remains valid |
| Combine | Use one root-lineage adapter, SessionService lifecycle owner, versioned board/marker/backup files, and a marker-last cutover |
| Reject | Promoting the shadow suffix, truncating legacy state, automatic downgrade, implicit backup restore, and any dual-write transition |
| Defer | Explorer replay, TUI projection, Agent execution links and controls, and final deletion of non-QueryEngine fallbacks |

The freeze retains P31.1a's deterministic resource ceilings. No retained
production shadow population exists, so root promotion must explicitly accept
that an oversized pre-cutover snapshot fails before mutation rather than being
truncated. It must also accept that a marked Session has a forward-only
`workboard/v2` reader floor and that binaries older than P31.1b are unsafe for
later mutation.

This addendum and its detailed freeze remain planning evidence. They do not
create an authoritative record, compatibility marker, backup, reader, adapter,
recovery command, or production owner. Root subsequently accepted the exact
release consequence in the
[`P31.1b promotion decision`](../../plans/p31-task-todo-explorer.md#p311b-root-promotion-decision)
and selected only that implementation slice as `Ready`.

## 2026-07-31 P31.2 Promotion Audit Addendum

At snapshot `fe9625349d9bd215cefb02ad53c676d641b728fe`, P31.1b has
replaced split Task/Todo authority with the committed WorkBoard v2 record, but
the explorer/replay boundary remains queued. The audit asks whether replay
requires another durable event log and which current facts can safely enter a
canonical read model before presentation or control work.

Current source establishes four constraints:

- [`LogicalWorkAdapter`](../../../../engine/internal/workboard/adapter.go)
  serializes Task/Todo mutations, commits by BoardID and expected revision, and
  exposes a defensive lifecycle snapshot, but publishes no WorkBoard reducer
  event;
- [`RuntimeStateStore`](../../../../engine/runtime_state.go#L397) applies
  ordered QueryEvents through one admission lock and can replay a caller-owned
  slice, while its bounded event rings are process-local rather than Session
  artifacts;
- the historical `QueryEngine.TaskAgentSnapshot` joined runtime rows,
  AgentRunner display metadata, and legacy AppState compatibility rows for
  then-current TUI consumers; P31.5 later deleted that selector; and
- Session resume validates and activates WorkBoard authority separately from
  rebuilding replay-only Agent facts. It does not reload a durable runtime
  event stream.

The relevant reference evidence does not justify replacing these owners:

| Evidence | Decision | Consequence |
|---|---|---|
| Claude Code Ripe `TaskListV2` uses stable IDs, blocked-aware priority, recent terminal visibility, and hidden counts | `adapt` | Keep the user outcome, but use durable item revision rather than UI wall-clock observation for deterministic ordering |
| Codex rollout trace reduces typed thread/Agent edges and retains failed or cancelled delivery without inventing a final message | `adapt` | Preserve exact execution generation and lineage; represent missing targets explicitly |
| Codex append-only raw trace persists a separate sequenced event protocol | `defer` | P31.2 has no accepted crash-consistency, cleanup, reader-floor, or recovery contract for another artifact |
| OpenCode persists Todo rows transactionally and publishes an update event after the transaction | `reject` as owner | Its database/event-service ownership would duplicate the existing WorkBoard JSON authority |
| Grok exposes typed initializing/running/completed/failed/cancelled snapshots | `adapt` | Normalize presentation phases without treating a display state as a control capability |
| Pi rebuilds session runtime after explicit teardown/switch | `preserve` | Cold explorer restore replaces read state and never resumes historical execution |

The audit therefore recommends `combine`: use the committed WorkBoard record as
the cold logical-work snapshot, retain runtime events as bounded in-process
execution observations, and join them in one project-owned
`TaskExplorerSnapshot`. A synthetic bootstrap is a read-only reducer input,
not a durable event and not a board mutation.

The detailed
[`P31.2 promotion freeze`](../../plans/p31-task-todo-explorer.md#p312-promotion-audit-freeze)
sets exact row, text, diagnostic, page, ordering, identity, replay, and
compatibility boundaries. It also rejects inferred WorkItem/Agent relations:
only a typed `(BoardID, WorkItemID, AgentID, Generation)` input can represent a
link. P31.2 defines that read-model shape and deterministic diagnostics but
admits no production link; P31.4 still owns durable admission and every live
control.

At this audit snapshot, the addendum did not make P31.2 executable: root PLAN
still required an independent review and a separate acceptance of the
bootstrap/runtime-only recovery contract. Those later promotion and
implementation decisions are recorded in
[`p31-task-todo-explorer.md`](../../plans/p31-task-todo-explorer.md#p312-root-promotion-decision)
and
[`p31-2-canonical-explorer-snapshot.md`](../../history/runtime/p31-2-canonical-explorer-snapshot.md).

## 2026-07-31 P31.3 Promotion Audit Addendum

At snapshot `9460656ee8082465b9c75e2641a781c79f931e2a`, P31.2 has
delivered the canonical bounded selector, but presentation still consumes its
`TaskAgentSnapshot` compatibility adapter. The audit asks which TUI state may
move to the new selector without moving runtime authority or interpreting
current controls as engine-declared explorer capabilities.

Current source establishes the remaining split:

- [`QueryEngine.TaskExplorerSnapshot`](../../../../engine/task_explorer.go)
  already joins the authoritative WorkBoard projection with immutable runtime
  execution generations, but no production TUI caller consumes it;
- Ctrl+T, the activity tree, and the wide sidebar still derive independent
  views from `TaskAgentSnapshot`;
- Ctrl+B independently owns its list/detail presentation plus existing Agent
  and local-task compatibility controls;
- `/team` independently owns a read-only Agent list, thread navigation, and
  bounded detail/transcript peek; and
- Task/Todo tool history renders one recorded call and has no current-board
  query.

The relevant reference evidence supports one presentation without replacing
these owners:

| Evidence | Decision | Consequence |
|---|---|---|
| Claude Code Ripe separates plan rows from background execution detail and bounds both | `adapt` | Use one responsive explorer with logical-work and execution sections, not one mixed Task type |
| OpenCode keeps a compact done/total and current-activity projection continuously visible | `adapt` | Activity and sidebar summaries derive from the same bounded snapshot |
| Codex preserves immutable thread/Agent lineage and failed/cancelled evidence | `preserve` | Selection and refresh bind the exact execution generation; replay-only rows stay inspectable |
| Grok exposes search and textual terminal states but keeps cancellation semantics typed | `adapt` | Search and no-color meaning belong to presentation; controls do not |
| Existing Eino-Agent Ctrl+B and `/team` readers already expose bounded transcript/detail and explicit thread navigation | `preserve` | P31.3 reuses those readers and leaves every existing control provider outside the explorer action vocabulary |

The audit recommends `combine`: add one TUI-owned component whose only list
input is `TaskExplorerSnapshot`, route Ctrl+T, Ctrl+B, and `/team` through
explicit filters, and derive activity/sidebar summaries from the same input.
The component may own cursor, search, section, detail tab, scroll, and focus,
but no TaskManager, AgentRunner, WorkBoard, transcript, output page, control
provider, or durable state.

Ctrl+B is the critical compatibility fence. P31.3 may preserve its existing
controls only through the unchanged compatibility projection and providers.
An Agent control requires the selected explorer execution and current
compatibility row to match both Agent ID and generation; Agent ID alone cannot
make a retained generation live. Existing local-task output/stop remains a
separately labelled compatibility row and is never inferred from a WorkItem.
P31.3 must not convert those controls into `TaskExplorerAction`, offer them on
replay-only or retained generations, or infer availability from the
explorer's status text. P31.4 remains the sole owner of production
execution-link admission and revision/generation-fenced control.

The detailed
[`P31.3 promotion freeze`](../../plans/p31-task-todo-explorer.md#p313-promotion-audit-freeze)
sets the exact responsive, accessibility, search/focus, PTY, performance,
source-owner, compatibility, and rollback boundaries. At this audit snapshot,
the addendum did not make P31.3 executable: root PLAN still had to
independently accept that contract and select P31.3 as the sole `Ready` slice.
That later decision is now recorded in the
[`P31.3 root promotion decision`](../../plans/p31-task-todo-explorer.md#p313-root-promotion-decision);
the audit itself remains non-executable evidence.

## 2026-07-31 P31.4 Promotion Audit Addendum

At snapshot `2c57115bd8da2bab1f1a895ff8cedc60db0bb86b`, P31.3 has
made the bounded explorer selector visible, but linked launch and every
explorer mutation remain deliberately absent. The audit asks where one exact
execution generation can be durably related to a WorkItem before dispatch and
how a later TUI action can reach that generation without trusting a cached
status string or Agent ID alone.

Current source establishes a usable admission seam and four unresolved races:

- [`AgentRunner.launchAgent`](../../../../tools/agent_runner.go) reserves the
  initial identity and generation, calls
  `prepareAgentLaunchLocked`, and enters `ExecuteAgent` only after preparation
  succeeds. A terminal continuation increments the same Agent's generation and
  passes through the same preparation method.
- `prepareAgentLaunchLocked` currently publishes `RecordAgentLaunch` before
  `RecordAgentExecutionAdmission`. The latter durably pins child Session and
  model admission, but neither operation knows a WorkItem reference. A linked
  execution therefore needs a new pre-publication commit boundary; adding a
  WorkItem field to the existing later hook would expose a transient launch
  before its relation is durable.
- [`QueryEngine.TaskExplorerSnapshot`](../../../../engine/task_explorer.go)
  still passes no production links. Its typed fixture path already proves that
  `(BoardID, WorkItemID, AgentID, Generation)` is the minimum relation and
  reports missing or stale targets without inventing one.
- [`QueryEngine.SendAgentMessage`](../../../../engine/agent_control.go),
  `AbortAgent`, `PauseAgent`, and `ResumeAgent` accept only Agent ID. Their
  runner methods then consult mutable status text. A request selected from
  generation N can consequently reach N+1 after a continuation.
- [`BackgroundTasksPanel`](../../../../internal/tui/background_tasks.go)
  retains the P31.3 exact-generation compatibility fence, but its local-task
  stop path still receives `TaskManager` directly and decides availability
  from `"running"`. Neither path is an engine-declared explorer capability.

The adjacent tests already freeze the relevant current guarantees:
`TestSubAgentLaunchMetadataIsDurableBeforeFirstResponse` and
`TestSubAgentLaunchPersistenceFailureDoesNotStartExecutor` cover the existing
pre-executor seam; Agent runner and steering tests cover terminal, abort,
pause, and continuation behavior; P31.2 selector tests cover immutable
generation links; and P31.3 tests prove that Agent ID alone cannot make a
retained row live. P31.4 must compose these guarantees rather than replace
their owners.

The refreshed reference evidence supports the same separation:

| Evidence | Decision | Consequence |
|---|---|---|
| Claude Code Ripe holds task-list claim validation and ownership change under one exclusive lock and treats execution terminal states as irreversible | `adapt` | Link validation, exact-key reservation, and durable admission form one ordered boundary; WorkItem owner or status still cannot identify an execution |
| Codex records explicit parent/child thread edges and anchors failed or cancelled delivery to the child thread when no final assistant message exists | `preserve` | The immutable relation and terminal anchor survive missing output; no inferred conversation item or replacement edge is needed |
| OpenCode persists exact parent/child Session identity and connects parent interruption to child cancellation | `adapt` | Preserve exact child routing and cancellation propagation without adopting Session reuse as link ownership |
| Existing Eino-Agent launch persistence, runtime generations, transcripts, and terminal reducer | `preserve` | WorkBoard owns only the relation; AgentRunner remains the execution and terminal owner |
| Status-derived controls, AgentID-only dispatch, TUI-held `TaskManager`, detach/reassignment, and a second durable runtime event log | `reject` | Every side effect re-resolves a typed capability against current board identity and exact generation |

The audit recommends `combine`. A linked Session upgrades its authoritative
WorkBoard record to a strict version 3 reader floor and stores bounded immutable
execution links in that same record. This avoids a second relation sidecar and
lets the existing WorkBoard expected-revision commit own link durability,
fork, delete, compaction, and corruption behavior. The first linked admission
writes a prepared version 3 authority record before atomically raising the
existing marker to `workboard/v3`; either crash state fails closed for an older
reader. Unlinked Sessions and unlinked Agent launches remain on the version 2
path.

The detailed matrix distinguishes normal marker-v1/record-v2,
prepared marker-v1/record-v3, committed marker-v2/record-v3, and corrupt
marker-v2/record-v2 states. Marker write or reread uncertainty dispatches
nothing. Only the prepared pairing is automatically repairable, and its exact
generation remains terminal pre-dispatch unless existing durable child
evidence proves a later settled state.

The relation commit is ordered before runtime launch publication and
`ExecuteAgent`. A committed link whose child admission never became durable is
restored as a terminal pre-dispatch failure and never dispatches. A later
continuation reserves a new generation and appends a new link; it cannot
rewrite the selected terminal generation. Destructive WorkBoard backup recovery
is rejected after any link exists because replacing the board would erase the
historical relation. Fork creates a new BoardID and copies no source execution
links; the source retains them.

Link admission and WorkItem terminal mutation use the same WorkBoard authority
lock. A terminal mutation checks exact runner settlement and commits while that
lock remains held, so `continue` cannot append a new generation after the
completion guard but before the item commit. The lock order is WorkBoard then a
read-only runner settlement snapshot; runner reservation releases its mutex
before calling WorkBoard. Whole-Session deletion similarly blocks new
admission/control and rejects until every linked generation is durably settled.

Explorer actions use a project-owned typed request containing BoardID, displayed
board revision, exact execution key, action, request identity, and any bounded
payload. The engine re-resolves availability immediately before the side
effect. Board revision and exact generation are authorization fences.
`RuntimeRevision` remains a response-correlation and refresh hint: unrelated
progress must not make a safe cancel impossible, while current execution state
is still revalidated under the runner's generation lock. A stale board,
generation, capability, or selection returns a typed conflict and performs no
fallback action.

The detailed
[`P31.4 promotion freeze`](../../plans/p31-task-todo-explorer.md#p314-promotion-audit-freeze)
defines the record upgrade, launch ordering, capability request, completion
guard, crash windows, compatibility surface, rollback, and proof matrix. This
addendum does not upgrade a WorkBoard, admit a link, publish a capability,
change dispatch, or make P31.4 `Ready`. Root PLAN must independently accept the
`workboard/v3` release consequence and select the slice.

## 2026-07-31 P31.5 Promotion Audit Addendum

At snapshot `47f49b9a37bd70a301c6ff2c92dec1dc68e33cb1`, P31.4 has
delivered durable execution links and exact explorer controls. The closeout
question is no longer how to add another task feature. It is whether the old
owners can be deleted without breaking Task/Todo compatibility, sharing state
between entrypoints, or turning standalone MCP into a hidden Session runtime.

Current source establishes six remaining conflicts:

- QueryEngine construction still seeds WorkBoard compatibility from the
  package Todo map even though every supported Session already binds a
  `LogicalWorkAdapter`;
- unbound Task and Agent contexts still fall back to package-default managers,
  so a missing composition-root binding can silently select another runtime;
- `AppStateTaskStore` and `TaskAgentSnapshot` still form a second task
  projection between runtime events and Ctrl+B or `/team`;
- Ctrl+B still receives `TaskManager` and AgentID/status control providers,
  while thread-detail queued input bypasses the exact explorer action request;
- `/tasks` still formats the pre-WorkBoard runtime compatibility snapshot; and
- standalone MCP creates a unique non-Session scope but binds no explicit
  Task/Todo owner, so its calls reach package defaults.

The standalone choice is the critical compatibility decision. Excluding every
Task/Todo tool would avoid global state but unnecessarily break successful
tool schemas and result bytes. Binding standalone to a QueryEngine would add
transcripts, markers, execution links, and Session lifecycle that the server
never promised. A server-scoped in-memory Todo authority plus TaskManager
preserves useful local behavior without either error.

Reference evidence supports that boundary rather than a new owner:

| Evidence | Decision | Consequence |
|---|---|---|
| Claude Code Ripe keeps Todos and background Task rows in AppState | `reject` as target | It explains current compatibility, but copying AppState would preserve the second owner P31.5 must delete |
| OpenCode routes Todo reads and writes through an explicit Session ID | `adapt` | Every durable call must name the current QueryEngine Session; standalone uses an opaque server scope, never a Session |
| Codex reduces Agent results against exact retained thread/Agent identity | `adapt` | Human control stays bound to exact generation and failed or replay-only identity |
| Existing Eino-Agent WorkBoard v2/v3 and AgentRunner settlement | `preserve` | No new database, event log, relation file, or reader floor is required |
| Package defaults and status/AgentID TUI providers | `reject` | Missing owner is an error; presentation cannot select an execution owner |

The audit therefore recommends `combine`. One root QueryEngine lineage owns
one durable WorkBoard and one AgentRunner. `TaskManager` and Todo inputs are
adapters into that WorkBoard. One standalone MCP server owns one explicitly
injected, process-lifetime Task/Todo compatibility pair and no AgentRunner.
`RuntimeStateStore` plus `TaskExplorerSnapshot` remains the bounded read model
for every TUI task surface and `/tasks`.

This recommendation intentionally changes two compatibility edges:

1. exported or direct non-context calls no longer mutate a package singleton;
   they must receive an explicit owner or fail before mutation; and
2. `/tasks` changes from a runtime-compatibility listing to a labelled,
   read-only canonical WorkBoard/execution listing.

Successful Task/Todo tool calls with an explicit owner retain their exact
inputs and result bytes. Model-tool Agent compatibility remains engine-scoped;
human TUI controls use only exact board and generation identity. Plain,
headless, headless-goal, and ACP retain the durable WorkBoard but gain no new
interactive or transport mutation surface. Standalone retains local Task/Todo
operations, exposes no Agent execution/control tool, writes no WorkBoard
artifact, and cannot share state with another server.

Thread-detail queued input is part of that human-control boundary. The freeze
adds a distinct `cancel_input` action with an exact generation and stable
MessageID returned by send. Child drain and cancellation remove that ID under
the same generation message lock: cancellation wins before delivery, or the
losing request returns `input_not_pending` without recalling another message.
Stale board or generation identity never falls back to the current Agent.

The detailed
[`P31.5 promotion freeze`](../../plans/p31-task-todo-explorer.md#p315-promotion-audit-freeze)
fixes the no-global source gates, explicit standalone owner and allowlist,
entrypoint durability/control matrix, canonical projection deletion,
direct-call failure behavior, lifecycle regression matrix, and rollback. This
addendum performs none of those deletions and does not make P31.5 `Ready`.
Root PLAN must independently accept the Go compatibility consequence and the
no-global rollback before selecting the slice.

## Recommendation

**Recommendation: `combine`.** Preserve Eino-Agent's QueryEngine,
`AgentRunner`, runtime reducer, transcript readers, and existing typed controls;
combine Claude's plan/execution separation and bounded prioritization, Codex's
replayable lineage, OpenCode's compact current-work projection, Crush's
title/activity distinction, and Grok's stable Todo identity plus explicit
mutation/cancellation semantics behind the project-owned P31 WorkBoard,
Execution, and Task Explorer contracts.
