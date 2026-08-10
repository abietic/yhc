# Session Discovery, Resume, and View-State Contract

**Status:** current

**Last verified:** 2026-08-10

**Ownership:** `engine/session` owns durable discovery and transcript loading;
`QueryEngine` owns execution-context restore; `ResumeDialog` owns picker state;
the TUI owns a safe presentation sidecar.

## Durable Sources

- Transcript JSONL is conversation and execution-checkpoint truth.
- `<session-id>.view.json` is presentation-only state.
- `~/.yhc/session-roots.json` is the writable discovery catalog of registered working and
  transcript roots; it contains no messages, prompts, credentials, or tool
  output.
- `~/.eino-agent/session-roots.json` contributes read-only discovery rows only
  when no exact catalog override is selected. Its transcripts are never
  opened for append by YHC.

Pending `RuntimeItem` entries are durable in the versioned runtime-input ledger
next to the transcript. Callback channels, approval decisions, structured
composer payloads, and preview rows are not durable session state. Resume
recovers processing entries as pending, removes transcript-delivered entries,
and drops stale stop controls without dispatching work.

## Bounded Discovery

[`SessionQuery`](../../../../engine/session/query.go) selects `cwd`,
`repository`, or `all` registered roots, plus sort/filter, page limit, scan
limit, and an opaque cursor. [`QuerySessions`](../../../../engine/session/query.go)
stats candidate JSONL files first, then incrementally reads light metadata until
the page fills or the scan bound is reached.

Defaults and hard limits are source-owned:

| Resource | Default | Maximum |
|---|---:|---:|
| Returned rows | 25 | 100 |
| Candidates scanned per page | 512 | 10,000 |

There is no exact filtered-total contract because that would require eagerly
reading every candidate. Cursors are bound to query fingerprint and stable sort
identity; a cursor from another scope/filter/sort is rejected.

Repository scope uses registered roots sharing Git common-directory identity.
It does not recursively scan the user's home directory.

Default CLI/ACP admission resolves only `<project>/.yhc/transcripts`. Existing
embedded engines may retain an explicit non-default transcript store, but the
exact default `<project>/.eino-agent/transcripts` path is always legacy. A row
from the legacy catalog remains marked `ReadOnly` and `NeedsImport`; relabeling
that legacy directory through a writable catalog cannot turn it into a
canonical resume source.

## Picker

[`ResumeDialog`](../../../../internal/tui/resume_dialog.go) owns loaded-row
deduplication, stable-key selection, query generation, stale async-result
rejection, preview state, and lazy continuation. Near-end navigation requests
another page. Search/filter changes start a new generation.

Preview reads recent bounded history; full transcript inspection is an explicit
action. Both are read-only and never dispatch a model or tool.

The picker offers:

- resume the exact selected transcript source;
- fork the complete selected message history into a new session, leaving the
  parent unchanged.

A legacy row is labelled `import required`. The first Enter opens a dedicated
confirmation view; only `Y` attests that the archived producer has stopped.
`N` or Escape returns to the picker without writing. After `Y`, the engine-owned
session service commits the recoverable bundle, re-admits the resulting
canonical row, and invokes the normal `ResumeInfo` path. Failure reopens the
picker with the error and leaves the legacy bundle unchanged.

[`applySessionPickerSelection`](../../../../internal/tui/app.go) persists
the old view state before invoking the engine action.

## Resume

Startup TUI, plain, headless, Goal, provider-free `sessions resume`, ACP Resume,
and ACP Load first run the read-only session admission boundary. For inactive
targets this happens before provider/model resolution and before recorder,
WorkBoard, approval, hook, MCP, or `QueryEngine` construction. A legacy target
returns the typed `legacy_session_import_required` projection with the session
ID only. Headless, provider-free administration, and ACP never import as a
side effect.

Canonical admission also checks any session-import evidence. If a journal or
project marker exists, the journal must be `catalog_committed`, the marker and
file manifest must match, and the exact root must be present in the canonical
catalog. Partial, colliding, or tampered bundles fail closed without recovery
writes. Explicit interactive confirmation or `migrate-state apply --owner
session --session ID --confirm-legacy-stopped` owns import; successful import
reruns admission rather than constructing a legacy-specific runtime.

[`QueryEngine.resumeSessionWithOptionsForTurn`](../../../../engine/engine.go)
loads and validates durable messages, then restores session/thread identity,
model, permission mode, working scope, result/transcript storage, replacement
state, file state, and retained Agent projections. Context-dependent registries
and settings are reloaded for the restored working directory.

The checkpoint includes an additive, independently versioned Plan record:
phase, exact plan-file identity, return-mode context, approval request
reference, and revision. It never stores a callback channel, terminal decision,
or grant. Resume validates the record before installing
`QueryEngine.PlanState`. Active restores directly. Cold AwaitingApproval clears
the request, increments the revision, normalizes to Active, and emits an
explicit warning. An unsupported or corrupt record preserves Plan containment
when the legacy permission mode was Plan.

The normalized cold state has no executable approval capability. A later
implementation requires a fresh model `ExitPlanMode`, a new request/revision,
and a newly reviewed digest; replayed typed data or the old request ID cannot
authorize Exit. Same-process TUI/plain/ACP continuation may resolve only an
exact live ProjectGraph interrupt retained by the current checkpoint owner.

The exact restored plan-file capability is not recomputed from the current
`HOME`. It reaches Plan Write/Edit admission, Enter/Exit tool execution,
compaction reinjection, and the next model-visible projection through the
engine-owned tool context.

Unavailable worktree or additional-directory paths degrade with explicit
warnings. Unknown persisted permission modes fail closed through parsing to a
supported mode. A checkpoint left `running` without an actionable in-process
callback becomes `interrupted`.

Persisted request IDs are diagnostic references. They first intersect the
unresolved reducer projection and then the original process-local
`PermissionCoordinator` owner for the final restored project. A Plan approval
additionally matches session, thread, request, revision, exact file, and return
mode. A fresh process therefore does not reopen an approval or question dialog
from disk. A same-process reconnect retains the one callback only when the
project coordinator is unchanged; crossing that boundary cancels the old
callback and cold-normalizes the durable Plan state.

Agent restore uses [`restoreSessionAgents`](../../../../engine/session_restore.go):
a still-running in-process Agent can be `live_attach`; durable-only Agent state
is restored as replay projection without controls or pending callbacks. A
validated durable-only Agent is also registered as an evicted reference so a
later explicit SendMessage may request continuation; resume itself never
launches it.

Worktree recovery is a separate resource boundary. Engine construction and
session resume enumerate only regular versioned records under the selected
project root and install a bounded recovery disposition through
`RuntimeStateStore.RestoreWorktreeSnapshots`. This path performs no Git,
model, tool, or Agent dispatch. Ready, Retained, and CleanupFailed records are
inspect-only; interrupted Creating/Removing records are recovery-pending;
missing paths or static project/path mismatches are unavailable diagnostics.

An explicit worktree Agent continuation subsequently proves the selected
session is the durable direct parent, reconstructs the complete owner from
Agent metadata, and performs fresh repository common-directory, path, branch,
status, and branch-HEAD admission before executor entry. An explicit cleanup
retry resolves the same owner inside QueryEngine and repeats clean/identity
checks at removal. Fork metadata clears `AgentIDs`, `WorktreePath`, and
`WorktreeBranch`, so a fork may see project recovery metadata but cannot
continue or delete the source Agent worktree.

## Replay Versus Resume

These operations have different contracts:

| Operation | Reconstructs | May dispatch work? |
|---|---|---|
| `RuntimeStateStore.Replay` | Bounded reducer state from ordered events | No |
| Agent thread replay/inspection | Selected runtime or durable transcript projection | No |
| Session resume | Durable conversation, execution context, Agent replay references, and worktree recovery metadata | Only after a future explicit user submission |

Resume does not rerun historical tools or model calls. It replaces the engine's
conversation history so a later `SubmitMessage` can continue with a new turn
([`resumeSessionWithOptionsForTurn`](../../../../engine/engine.go) and
[`SubmitMessage`](../../../../engine/engine.go)).

CLI/plain, TUI picker, SDK/headless, ACP Resume, and ACP Load all enter this
engine restore path. ACP reports the restored Plan phase through its status
extension; external ACP mode changes still use `SetPermissionMode` and lose an
active-turn race without mutation.

## View Sidecar

[`PersistedSessionViewState`](../../../../engine/session/view_state.go) is a
versioned, mode-0600, atomic sidecar. It retains at most 128 thread views and
64 Ki runes per plain draft.

Allowed fields are:

- active/owner thread identity and advisory attach mode;
- plain draft, cursor, and input mode;
- item/line scroll and follow state;
- selected Agent detail tab.

Excluded fields include structured elements, image/file/resource payloads,
queue previews, undo, active history search, selections, dialogs,
notifications, response channels, interaction requests, opaque transcript
cursors, in-flight page requests, and page caches.

[`resetAndRestoreSessionViews`](../../../../internal/tui/session_view_state.go)
starts from clean presentation state, intersects saved thread IDs with the
current runtime catalog, always treats the leader as live, and lets the catalog
override advisory Agent attach modes. Missing/corrupt sidecars produce a clean
view and warning; they do not invalidate the resumed conversation.

Foreground/background ProjectGraph child restore uses the same view sidecar
and selectors as existing Agent threads. Same-process running generations
attach live; cold terminal and Session-only orphan generations are replay-only;
an evicted runtime thread remains inspectable through its durable Agent
transcript. Switching among them first captures the old presentation and does
not dispatch or change runtime identity, generation, status, lineage, or
transcript state. Same-process switching retains the per-thread page cache and
scroll anchor; after process restart the sidecar restores only safe view state,
and the exact current child generation lazily requests a fresh bounded page.
The sidecar deliberately persists only the follow flag and offset, not the
presentation append epoch or unseen baseline. Restoring `follow=false`
therefore creates an away view with an invalid baseline and an immediately
visible count-free `Jump to bottom` action; hydration cannot manufacture an
unseen count.

## Invariants

1. Discovery work is bounded and cursor-based.
2. Preview, full inspection, reducer replay, and Agent replay are read-only.
3. Resume restores history/context but dispatches nothing until a future
   explicit submission.
4. Stale request IDs and disk-only Agents never regain live controls.
5. Pending input and rich/private presentation payloads are not durable.
6. A sidecar failure cannot make durable conversation history unresumable.
7. Cold Plan restore never revives an approval callback or historical grant;
   same-process projection requires the exact original live owner.
8. Plan recovery, compaction, Enter/Exit, and ProjectGraph consume one exact
   plan-file capability.
9. Durable view restoration cannot hide the only follow-recovery action or
   derive unseen count from hydrated projection length.

## Evidence

- bounded discovery: [`TestQuerySessionsScanCapBoundsSelectiveFilter`](../../../../engine/session/query_test.go)
- execution-context restore: [`TestResumeSessionInfoRestoresCrossCWDExecutionContext`](../../../../engine/session_actions_test.go)
- versioned Plan restore and cold normalization: [`plan_persistence_test.go`](../../../../engine/plan_persistence_test.go)
- ACP Resume/Load convergence: [`agent_session_test.go`](../../../../server/acp/agent_session_test.go)
- picker paging and stale rejection: [`TestResumePickerRapidSearchRejectsStaleGeneration`](../../../../internal/tui/resume_dialog_test.go)
- safe sidecar restore: [`TestPersistSessionViewStateKeepsOnlySafePresentationFields`](../../../../internal/tui/session_view_state_test.go)
- ProjectGraph child attach-mode and switch compatibility:
  [`TestP139dProjectGraphRestartProjectsReplayAndEvictedViewsWithoutDispatch`](../../../../internal/tui/project_graph_child_projection_test.go)
