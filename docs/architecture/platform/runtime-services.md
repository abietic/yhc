# Runtime Services

**Status:** current
**Last verified:** 2026-08-19

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
runtime services. This is write protection only: P51.1 does not claim that
Guest processes cannot read the subtree. Permission mode cannot remove or
widen this denial.

On Darwin amd64/arm64, the default Guest binding is
`workspace-write`/`degraded` through the fixed
`/usr/bin/sandbox-exec` Seatbelt adapter. The real capability probe and launch
path prove declared reads/writes, network denial, root identity, descendant
confinement, process-group cleanup, wall time, and bounded retained output.
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
- prompt suggestion service (`NewPromptSuggestionService`);
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
  process-class matrix and Darwin root/profile resolution.
- [`Binding.Prepare`](../../../engine/containment/binding.go) validates the
  immutable policy, adapter generation, availability, and launch digest.
- [`darwinSeatbeltAdapter`](../../../engine/containment/seatbelt.go) owns the
  real capability probe and fixed-binary launch transform.
