# Runtime Services

**Status:** current
**Last verified:** 2026-10-24

> **Ownership:** entrypoint-specific background-service construction, rebinding, shutdown, and disconnected helpers

`engine/services` mixes an active engine-owned background-service group with helper APIs that currently have no production caller. Service availability is entrypoint-specific; package reachability is not lifecycle wiring.

## Entrypoint lifecycle matrix

| Surface | Constructs `QueryEngine` | `EnableLongSessionServices` | Engine-owned memory/extraction/dream | Close owner |
|---|---:|---:|---:|---|
| TUI | yes, [`runTUI`](../../../cmd/yhc/cmd/root.go) | true | active for main engine with a non-nil model | `defer engine.Close()` in entrypoint |
| Plain REPL | yes, [`runPlainREPL`](../../../cmd/yhc/cmd/root.go) | true | active for main engine with a non-nil model | `defer engine.Close()` in entrypoint |
| Headless | yes, [`runHeadless`](../../../cmd/yhc/cmd/headless.go) | false/default | not started | `defer engine.Close()` in entrypoint |
| Session administration | lightweight host, [`NewSessionAdministrationEngine`](../../../engine/session_administration.go) | false/default | not started | one command invocation closes the host |
| Diagnostic/extension administration | lightweight host, [`NewInspectionAdministrationEngine`](../../../engine/inspection_administration.go) | false/default | not started | one command invocation closes the host; no provider runtime, MCP connection, Graph, watcher, hook, skill, worktree recovery, or Agent replay is started |
| State migration | no engine; [`migrate-state`](../../../cmd/yhc/cmd/migrate_state.go) dispatches owner functions | n/a | not started | one command invocation; the cron owner performs only strict state inspection/import |
| ACP | yes, per ACP session in [`createEngine`](../../../server/acp/agent.go) | true | active for main session engines with a non-nil model | ACP agent/session shutdown closes engines |
| Standalone MCP server | no; [`Serve`](../../../server/mcp/server.go) builds a direct tool registry | n/a | n/a | stdio server lifecycle |
| Sub-agent | yes, with non-empty `AgentID` in [`SubAgentExecutor.ExecuteAgent`](../../../engine/subagent.go) | false/default | not started; `AgentID` is also an explicit guard | sub-agent executor defers `Close` |

At engine construction, services start only when all three conditions hold: a non-nil chat model, empty `AgentID`, and `EnableLongSessionServices`. See [`NewQueryEngine`](../../../engine/engine.go).

## Active engine-owned group

[`BackgroundServices`](../../../engine/services/background_services.go) owns three cooperating services:

- session-memory updates after tool-call thresholds;
- durable memory extraction at turn boundaries;
- auto-dream eligibility checks at turn boundaries.

[`RecordToolCall`](../../../engine/services/background_services.go) is invoked after tool execution. [`RecordTurn`](../../../engine/services/background_services.go) schedules extraction and dream work after the loop reaches a natural turn boundary. Each job type is coalesced: a pending request replaces/merges work while its single worker is running.

The service model function is adapted from the engine chat model in [`newEngineBackgroundServices`](../../../engine/engine.go). The shared memory store uses `<transcript-dir>/memory` with a larger limit when long-session services are enabled; ordinary engines use a per-session memory directory.

## Resume and shutdown

Resume changes session-scoped paths. [`rebindLongSessionServices`](../../../engine/engine.go) shuts down the old group with a two-second deadline, creates the correct memory store for the resumed session, and starts a replacement only if the same enablement guards still hold. Resume invokes this after rebinding permission state in [`engine.go`](../../../engine/engine.go).

[`QueryEngine.Close`](../../../engine/engine.go) is the resource owner for more than `engine/services`:

1. release permission registry ownership;
2. shut down background services with a two-second deadline;
3. cancel and join async shell hooks;
4. persist and close transcripts;
5. stop the settings watcher;
6. shut down an owned agent runner;
7. kill all persistent shells in an owned per-engine `tools.ShellManager`; and
8. disconnect an owned MCP manager.

The Bash family resolves this manager and the engine execution CWD from tool
context. A child engine closes its shells before `AgentRunner` asks the durable
worktree service to remove or retain the path, so no package-global shell is
left attached to an ephemeral worktree. Session resume closes existing
engine-owned shells before rebinding CWD.

Every active QueryEngine owns one immutable process-class binding matrix.
TUI, Plain, headless, headless Goal, and ACP resolve it before ordinary engine
activation; restore finalizes the Guest root before hooks, stdio MCP, or Bash
can spawn. Child Agents derive an equal-or-narrower Guest binding and run a new
adapter probe instead of copying the parent's proof.

The Darwin `workspace-write` Guest may write the ordinary workspace and
approved temporary roots, but an immutable denied-write root reserves
`<canonical-workspace>/.eino-agent` for host-owned transcript, WorkBoard, and
runtime services. Linux bubblewrap overlays existing denied roots read-only;
it fails closed when a workspace-local denied path crosses a symlink, but does
not reserve absent names against later creation. These are write protections,
not read secrecy. Permission mode cannot remove or widen them.

Linux's fixed runtime read roots retain system aliases such as `/bin` and
`/lib64` alongside their canonical targets, so the live namespace can resolve
the fixed Bash path and ELF interpreters. These aliases remain read-only;
extra user roots and toolchain/cache roots retain canonical path selection.

On Darwin amd64/arm64, the default Guest binding is
`workspace-write`/`degraded` through the fixed
`/usr/bin/sandbox-exec` Seatbelt adapter. On Linux amd64/arm64 it uses fixed
`/usr/bin/bwrap`, an empty mount root, user/PID/IPC/network namespaces,
capability removal, and a seccomp filter denying socket and io_uring setup.
Each adapter performs a real capability probe before the binding becomes
available. The launch path proves declared reads/writes, network denial, root
identity, descendant confinement, process-group cleanup, wall time, and
bounded retained output.

The Darwin policy allows exact `kern.hostname` and `hw.machine` sysctl reads,
providing the OS metadata required by `uname(3)` and Node's test runner.
This exposes the host name and machine type, not blanket sysctl access or
sysctl writes. It does not widen filesystem roots or network access. The
adapter capability generation changes with this policy; the executable
regressions are `TestSeatbeltProfileAllowsOnlyRuntimeSysctls` and
`TestSeatbeltDarwinIntegration` in
[`seatbelt_test.go`](../../../engine/containment/seatbelt_test.go), plus real
allowed/denied sysctl calls in `TestSeatbeltDarwinSysctlAllowlist` in
[`seatbelt_darwin_test.go`](../../../engine/containment/seatbelt_darwin_test.go).

`ShellManager` passes the current `os.Environ()` byte-for-byte, pins the exact
binding before persistent Bash starts, and rechecks both binding digest and
workspace device/inode before every start/command. A missing executable,
unsupported host, failed probe, or replaced root yields an unavailable Guest
binding and rejects Bash before spawn; it never retries ambient.

P51.2 exposes a detached value identity for the QueryEngine permission owner;
it does not expose the pinned binding or environment values. Proof-bound Auto
Bash revalidates the exact Guest identity before registry acquisition, and
ShellManager repeats root validation at the last boundary before a new process
start or persistent-shell stdin write. Any bound-identity drift returns
`sandbox_binding_expired` without executing or retrying ambient.

P51.3 deliberately does not let the Linux proof satisfy Default/Auto's
automatic Bash admission. Until absent control-plane names can be fenced,
Linux containment limits prompt-approved Guest execution only; the automatic
path remains Darwin-specific.

The Session-facing CWD retains the caller or restored metadata spelling for
hooks, permissions, transcripts, and user-visible state. The Guest policy owns
the separately canonicalized root. ShellManager accepts an operational alias
only when it resolves to that exact captured device/inode, then starts the
process at the canonical policy root.

Shell hooks and configured stdio MCP each receive their own explicit
`danger-full-access`/`disabled` ambient binding. The aggregate Guest state also
remains `degraded` because environment credentials and hard memory,
file-descriptor, and process-count limits are not isolated. Only explicit
user-owned configuration or `--sandbox danger-full-access` selects an ambient
Guest rollback, which emits a visible warning. Permission mode, project
configuration, hooks, tool input, and ACP clients cannot broaden the matrix.

Entrypoints that construct an engine must close it. Adding a goroutine without a corresponding owner, cancellation path, and join boundary is a lifecycle bug.

## Interactive prompt suggestions

The root TUI sets `EnablePromptSuggestions` from the merged
`prompt_suggestions` setting. `NewQueryEngine` constructs
[`PromptSuggestionService`](../../../engine/services/prompt_suggestion.go) only
for a non-nil model, empty `AgentID`, TUI or AppServer entrypoint, and non-administration,
non-restore-staging engine. Plain, headless, ACP, child, and administration
engines do not construct it.

After a successful completed turn, the App runs the service's synchronous
generation method inside a cancellable Bubble Tea command. The service applies
a 30-second deadline, requires at least two assistant message records, and
filters unsafe or implausible output. The request uses the current engine chat
model through the controlled side-query seam, preserving the admitted model,
provider, main role, profile, and Session identity. It sends
a bounded conversation snapshot with no tools, allows at most one provider
dispatch with a 64-token output cap, and is disabled in Plan mode, while
permission input is pending at dispatch admission, or while an unfinished Goal
requires exact provider accounting.

Historical user and assistant text keeps its native speaker role in the
suggestion request. Main-system text and tool results are sent as assistant-side
evidence rather than suggestion policy, user messages, or live tool calls.
Only the engine-owned outer role prefix is decoded, so role-looking text
inside a message remains content. A separate final user message asks for the
next-user draft, keeping the last assistant report out of the live request
position. Argument suggestions retain their dedicated input format.

The project-owned [`GetSuggestionPrompt`](../../../engine/services/prompt_suggestion.go)
asks the model to continue only an explicit, unfinished user request whose
prerequisites are met, preserving its action, target and scope. Completed
explanations or summaries, missing information and unresolved choices call for
abstention. Commit, push, edit and research are not inferred from task completion.
For an explicitly requested test after a fix, an edit or file summary does not
count as a test result: the original test remains pending until a post-fix result
is reported. A short draft may reuse the user's test name or scope without
inventing a literal command; actual reported blockers still call for abstention.
The prompt uses the existing `no suggestion` marker for abstention, which
`FilterSuggestion` hides as meta text. These are model instructions, not a
deterministic semantic authorization check; the filter still checks text shape
and meta replies rather than proving that a candidate matches user intent.

Prompt suggestions on an admitted DeepSeek route explicitly request
`reasoning.effort=none`: the provider's
[output limit includes reasoning tokens](https://api-docs.deepseek.com/api/create-response/),
so thinking can otherwise consume the entire short suggestion budget. This is
a per-call control; the main route's reasoning setting and argument-suggestion
effort remain unchanged. Other providers keep their admitted effort. The shared
auxiliary suggestion boundary records usage and discards known token-truncated
responses before text can become a candidate; it never retries to fill the gap.

[`GeneratePromptSuggestionResult`](../../../engine/prompt_suggestion.go) returns
one ephemeral [`PromptSuggestionResult`](../../../engine/services/prompt_suggestion_result.go).
The result separates `available`, `suppressed`, `empty`, `filtered`, `timed_out`,
`cancelled`, `failed`, and `stale`. Only `available` carries candidate text.
Suppression codes distinguish disabled or unavailable generation, missing or
early conversation, Plan mode, pending permission, an active Goal, and a
request still in flight. AppServer additionally classifies busy sessions.
Provider errors remain internal errors; clients receive fixed categories and
render their own allowlisted explanations. Invalid UTF-8 and control-bearing
suggestions are filtered. Empty/whitespace suggestion text differs from a
stream rejected by the existing side-query contract, which remains a failed
request. The compatibility `GeneratePromptSuggestion` API retains its
best-effort string return.

One service permits one generation at a time. Caller cancellation does not
release that slot until the provider returns, so repeated manual requests
cannot accumulate overlapping provider calls. TUI manual requests and the
existing Workbench button consume this same result. Neither explicit request
bypasses `prompt_suggestions`, minimum assistant records, permissions, Goal
admission, or the selected model/effort/output budget.

The service starts no resident goroutine and therefore adds no engine shutdown
join. App-owned cancellation and generation/thread/revision/query fences reject
late results. The request and generated text are not appended to the
conversation or transcript. A content-free auxiliary usage record is
persisted after a successful provider response, so provider-reported tokens
survive Session restore without replacing the latest main-loop context-window
fact. Transcript replacement paths preserve that auxiliary record and its
entry identity. Because this is a separate post-turn helper request, it does
not consume the completed query's API task budget or continuation token
tracker; the selected provider may still bill it. Set `prompt_suggestions` to
`false` to remove that request entirely. Speculative execution and the
service's legacy speculation API remain outside production wiring.

Desktop/Web uses this same generator behind an explicit **Suggest next step**
action, not an automatic post-turn request. `runServeApp` initializes enablement
from the merged preference. The authenticated
`POST /v1/sessions/{session_id}/prompt-suggestion` accepts only an empty object;
it does not attach saved sessions or accept client conversation, paths or routes.
The response retains `session_id` and `suggestion`, adding `status` and an
optional project-owned `reason`; provider error text is never returned.

[`session.promptSuggestion`](../../../server/appserver/prompt_suggestion.go)
admits one request for an owned idle session and checks the shared effective
preference before dispatch and before returning text. It releases the
turn-admission lock before calling the provider. Direct/queued turn admission,
execution-control changes and session shutdown cancel the request; shutdown
joins it before closing the engine. Cancellation is cooperative, and request
identity rejects late output. Re-enabling a startup-disabled session requires
a new session; disabling the shared preference suppresses requests in existing
sessions.

The renderer fences an ephemeral candidate to the session, draft edit, runtime
cursor, execution settings and request generation. Acceptance is one undoable
plain-text draft edit, never Send. Typing, switching sessions or dismissing the
candidate invalidates its UI projection; it does not undo provider cost or
promise immediate IPC cancellation. No suggestion text is published as a
durable event or stored until accepted into an ordinary draft. Evidence owners:
[`prompt_suggestion_test.go`](../../../server/appserver/prompt_suggestion_test.go)
and [`prompt_suggestion.test.mjs`](../../../desktop/test/prompt_suggestion.test.mjs).

## Other active service-like behavior

The TUI owns welcome-tip persistence and scheduling through [`NewPersistentTipHistory`](../../../internal/tui/welcome.go) and `NewTipScheduler`. This behavior is TUI-specific and is not part of `QueryEngine` background services.

`engine/cron` is reachable through the provider-free `migrate-state` command,
but no released entrypoint constructs or starts its `Scheduler`. New cron task
and scheduler-lock writes use private files under `<project>/.yhc`. The migration
owner strictly inspects `<project>/.eino-agent/scheduled_tasks.json` and its
optional lock without mutating either; import requires explicit stopped-producer
attestation, rejects a live or unprovable PID, and verifies a stable pinned
snapshot before no-replace promotion. This wiring is state continuity, not a
claim that durable scheduled execution is an active product service.

## Reachable but disconnected helpers

Current production callers do not construct or invoke these `engine/services` APIs:

- agent summarization (`StartAgentSummarization`);
- away summaries (`GenerateAwaySummary`);
- LSP service manager (`NewLSPServiceManager`);
- magic-docs manager and prompt suggester (`NewMagicDocsManager`, `NewPromptSuggester`).

Their package is reachable because active background services and TUI tips share `engine/services`. They must not be described as running services until an entrypoint owns their construction and shutdown.

## Change checklist

For any service addition or lifecycle change, document and test:

- constructing entrypoints and exclusion guards;
- engine/session/sub-agent ownership;
- cancellation propagation and bounded join behavior;
- event ordering relative to tool completion and turn completion;
- resume rebinding of session-scoped paths;
- behavior when the model is nil or construction fails;
- no goroutine or transport leak after `Close`.

## Additional code references

- [`QueryEngine.toolExecutor`](../../../engine/engine.go) binds the
  engine-owned CWD and shell manager.
- [`QueryEngine.Close`](../../../engine/engine.go) owns shell teardown.
- [`ShellManager.ExecuteAt`](../../../tools/bash_shell.go) sets only the
  shell process initial directory and never calls process-wide `os.Chdir`.
- [`ResolveExecutionBindings`](../../../engine/execution_policy.go) owns the
  process-class matrix and platform root/profile resolution.
- [`Binding.Prepare`](../../../engine/containment/binding.go) validates the
  immutable policy, adapter generation, availability, and launch digest.
- [`darwinSeatbeltAdapter`](../../../engine/containment/seatbelt.go) owns the
  real capability probe and fixed-binary launch transform.
- [`linuxBubblewrapAdapter`](../../../engine/containment/bubblewrap.go) owns the
  Linux real probe, strict mount projection, and fixed-binary launch transform.
