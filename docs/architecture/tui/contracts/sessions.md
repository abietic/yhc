# Session Discovery, Resume, and View-State Contract

**Status:** current

**Last verified:** 2026-10-26

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

## Captured changes and execution facts

[`WorkspaceChanges`](../../../../engine/workspace_revisions.go) owns a separate,
private `<session-id>.workspace-history/revisions.jsonl` journal under the active
transcript directory. The leader records Write/Edit before admission to the
actual executor and immediately observes the result, including a failed tool
that partially changed a file. Permission denial never captures a revision.
Other mutating tools are explicit coverage gaps; Git, Bash, child tools and
external edits are not reconstructed from conversation text.

Content capture accepts regular, non-symlink UTF-8 text up to 1 MiB per file;
`.git` and paths outside the exact workspace are rejected. The append-only log
is bounded to 32 MiB, 200 file records and 200 turn records, with private file
permissions, durable root identity and synced intents. Missing results, unsupported content, capacity
limits and persistence failures remain visible as incomplete coverage. They do
not change the executed tool's result. File data stays in this private journal,
not skill telemetry. Session deletion preflights this exact private directory,
rejects links or unknown entries before deleting the transcript, and retries
partial cleanup without requiring the transcript to remain. It removes private
records and content while retaining an empty 0700 directory: path-based removal
cannot atomically prove that another process has not substituted its name.
Fork and export
do not copy or expose these private bytes.

The canonical runtime emitter also records minimal start and terminal facts,
including retained tool counts. A successful Goal terminal pair uses the same
observer after canonical reduction. No model output or reasoning is stored in
these facts. A restart without a terminal leaves the turn incomplete; bounded
runtime tool rings cannot establish a complete action count.

The TUI-only `/changes` panel groups this history by turn, previews captured
content and selected hunks, and can add literal content to the draft. Its
snapshot is independent of current Git or workspace contents. `/recap` joins
these durable facts with current retained task and accepted-input projections.
Execution completion, cancelled/completed tasks and verification remain
separate: no independent verification receipt is currently recorded here.
Opening either view and replaying this journal dispatches no model or tool.
Session changes invalidate stale panels and callbacks by owner generation.

Source and regression boundaries: [`revisions.go`](../../../../engine/filehistory/revisions.go),
[`execution_summary.go`](../../../../engine/execution_summary.go),
[`workspace_revisions_test.go`](../../../../engine/workspace_revisions_test.go),
and [`workspace_history_pty_unix_test.go`](../../../../scripts/e2e/workspace_history_pty_unix_test.go).

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


## Guarded file restore

The leader TUI can restore a captured file or the captured file set before an
eligible turn. In `/changes`, **r** opens a current-disk-to-target preview;
Enter confirms that exact preview and then requests a separate native
allow-once permission. Opening the history view remains read-only. Recovery
never invokes a model, rewrites conversation messages, stages files or resets
Git. `/rewind` remains a removed-command tombstone.

[`PrepareWorkspaceRestore`](../../../../engine/workspace_restore.go) owns the
live preview, Session/thread/generation identity, policy revision and two-minute
expiry. The filesystem service also requires a non-serialized, one-use preview
admission tied to its current journal replay and observed file states. A saved
confirmation or permission revision cannot authorize a restart. Changes after
preview, including a switch between known before/after states, require a new
preview. Permission rules and execution write roots still apply; bypass,
acceptEdits and Auto cannot replace the fresh allow-once interaction.

Only new, complete capture boundaries can authorize restoration. The v2 journal
adds explicit turn-boundary proof and parent-directory existence; v1 history
remains reviewable and cannot acquire this proof through replay. Hooks, Bash,
direct terminal commands, MCP processes, children, custom permission callbacks,
unsupported file captures and unfinished later turns prevent a full-turn
restore. The same exclusions apply when selecting one file. No new directory,
link or hard-link ambiguity is restored. Capture-boundary persistence failure
must stop new admitted work when it would otherwise leave an older positive
proof unaware of a later writer. Direct terminal execution records its durable
coverage gap before launch and blocks if that cannot be persisted.

The native writer supports macOS and Linux. A plan contains at most 32 regular
UTF-8 paths, 8 MiB of combined original/target bytes and a 12 MiB encoded private
operation. It stores synced intent in the Session's 0600 `restore.json`, then
uses anchored no-follow access, exclusive sibling temporary files and
no-replace renames. Originals stay in intent-bound sibling backups until the
transition is verified. Per-path progress is synced before the next path;
unknown or conflicting bytes are preserved. Completed operations append file
receipts and remove only matching owned siblings. The operation is not a model
turn or a verification receipt. Other platforms retain read-only history.

An interrupted operation blocks ordinary model admission. Reopen `/changes`:
**t** previews retry and **b** previews rollback to the recorded pre-restore
states. Both require new confirmation and permission. There is no automatic
apply on restart. Cancellation stops at a file boundary and reports the durable
state rather than claiming an atomic multi-file undo. Directory locking
excludes concurrent journal writes; a stale process must explicitly reopen the
Session before recording again. Session deletion refuses unfinished recovery
or remaining sibling artifacts, then removes only the fixed private sidecars.

After any potentially started transition, the engine invalidates its affected
file-state cache and creates a private tool read-guard owner. The next main
request receives a factual reread notice; actual Write/Edit requires a new
Read. Another engine's compatibility map is unchanged. An external explicit
read-guard scope cannot bypass this fence: model admission refuses it for that
restored Session and advises a fresh Session. Conversation replay and model
context are not silently replaced with captured bytes.

Regressions: [`workspace_restore_test.go`](../../../../engine/workspace_restore_test.go),
[`restore_apply_test.go`](../../../../engine/filehistory/restore_apply_test.go),
[`history_restore_delete_test.go`](../../../../engine/session/history_restore_delete_test.go),
[`workspace_restore_ui_test.go`](../../../../internal/tui/workspace_restore_ui_test.go),
and [`TestWorkspaceHistoryRealCLIPTY`](../../../../scripts/e2e/workspace_history_pty_unix_test.go).


A prior uncaptured writer keeps this Session review-only for restoration: a
foreground shell can leave descendants after its command returns. Direct TUI
shell execution is bound to the engine's CWD, containment and shell lifecycle;
it never borrows a process-global shell. Existing shell sessions also prevent
positive capture proof. Start a fresh Session after stopping such work rather
than treating a later file-only turn as proof that earlier writers disappeared.

Pending recovery also blocks model-invoking commands, prompt expansion, workspace/external commands and Session forks before dispatch. Local inspection and explicit Session escape remain available.
