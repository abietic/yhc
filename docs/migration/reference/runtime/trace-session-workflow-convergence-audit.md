# Trace and Session Workflow Convergence Audit

**Status:** reference-snapshot
**Last verified:** 2026-07-14
**Result:** P10 complete where the recorded evidence required implementation

> **Ownership:** this report owns the source-backed P10 workflow evidence and
> the resulting product decision. Current behavior belongs in
> [`migration/STATUS.md`](../../STATUS.md), accepted future work belongs in
> [`migration/PLAN.md`](../../PLAN.md), and unresolved gaps belong in
> [`migration/REMAINING.md`](../../REMAINING.md).

## Observable Question

Can a user with a long leader transcript and several concurrent Agents locate
the Agent that needs attention, switch to a usable child transcript, resolve
the request, inspect terminal failure and evicted output, return to the leader,
and safely resume or fork after restart without leaving the TUI for raw files?

The audit treats the following as separate claims:

- **discovery:** the TUI identifies which Agent needs attention;
- **navigation:** a visible label can be searched and selected;
- **projection:** the selected transcript becomes useful without loading the
  whole durable history into the parent row;
- **terminal explanation:** status, failure, usage, result, and output source
  are available across the bounded parent row and full Agent detail;
- **recovery:** live attach, replay-only restore, process restart, and fork do
  not create false actionability or lose lineage.

## Decision

P10 found one observable navigation defect and no architecture-level trace or
session gap. The picker rendered the leader as `main` but searched only stored
catalog fields, so searching the visible label returned no rows. The bounded
fix indexes `agentThreadLabel`, and unit plus real PTY coverage now proves the
leader-child-leader loop.

No parent-output duplication, live LLM summary, storage rewrite, or new panel
was accepted. The existing parent trace remains a bounded progress and terminal
index; the child transcript and Agent detail remain the result and output
surfaces.

## Measured Workflow

The self-hosted Unix PTY uses a 300-row leader history, three Agent rows, an
inactive child permission, a running sibling, and a failed evicted child. It
executes the real Bubble Tea alternate-screen, mouse, paste, resize, approval,
thread picker, interruption, EOF, and terminal cleanup paths.

| User goal | Semantic actions | Observed elapsed | Result |
|---|---:|---:|---|
| Notice pending attention | 0 | already visible | Status shows `Input needed in @alpha scout`. |
| Locate owner and reach first usable transcript | 3: `/agent`, type `alpha scout`, Enter | 34.2-58.5 ms | Picker shows concurrent Agents and `!1`; child prompt/transcript appears before approval. |
| Approve and return to leader | 4: choose approval, `/agent`, type `main`, Enter | within the same PTY workflow | Approval resolves only the owner row; the visible leader label is searchable. |
| Locate failed evicted child and read transcript | 3: `/agent`, type `gamma reviewer`, Enter | 11.5-23.2 ms | Picker shows `failed` and `disk`; failure text is projected in the TUI. |

These wall-clock values are diagnostic observations on the current Darwin
arm64 development machine, not portable SLAs. The ordinary cross-machine gates
remain p95 budgets:

| In-process path | Observed p95 | Gate |
|---|---:|---:|
| 20-Agent picker attention search and overlay | 0.097-0.118 ms | `< 50 ms` |
| 256-message first usable Agent transcript | 0.606-0.683 ms | `< 100 ms` |
| cached 20-Agent thread switch and frame | 0.256-0.306 ms | `< 100 ms` |

## Scenario Evidence

| Scenario | Evidence | Outcome |
|---|---|---|
| Long leader trace | `TestTUIWorkflowPTY` starts with 300 chat rows and exercises resize, selection, paste, and navigation. | The status, picker, and child transcript remain usable. |
| Concurrent Agents | The PTY catalog contains waiting alpha, running beta, and failed/evicted gamma rows. | Status, mode, and attention remain distinguishable in one picker. |
| Inactive-thread approval | The alpha permission is enqueued while the leader remains active. | It contributes status attention, does not steal focus, and opens only after owner activation. |
| Failure and eviction | Gamma is terminal with `model_error`, evicted storage, bounded output, and durable paths. Engine detail tests also merge retained and evicted transcripts and read bounded output tails. | Failure and transcript are readable without raw-file inspection; detail retains storage and output-source metadata. |
| Process restart | `TestProcessRestartRestoresAgentReplayAndLineage` keeps an Agent blocked after its running launch snapshot is durable, records a stale request ID, then starts a separate process with a fresh runner and engine over the same disk state. | The stale running Agent restores aborted and replay-only with parent session/thread lineage, its launch transcript, no pending interaction, and no actionable request. |
| Resume and fork | Existing live-runner, disk-running, replay-only, selected-store, cross-CWD, missing-worktree, and fork tests were rerun. | Only a currently owned runner can live-attach. Disk state is non-actionable replay. Fork creates a new session with parent identity and leaves the source transcript unchanged. |

## Terminal Surface Assessment

The terminal workflow is intentionally split across two bounded levels:

| Question | Parent Agent row | Agent detail / thread projection | Assessment |
|---|---|---|---|
| Result | Shows progress summary and the detail affordance; it does not duplicate the full child result. | The final assistant answer remains in the projected Transcript; Output shows the bounded output tail. | Sufficient; no raw file required. |
| Failure | Shows failed status, terminal reason, and error. | Overview repeats terminal/error and records load warnings. | Sufficient. |
| Usage | Shows tool uses, total tokens, and duration. | Activity shows tool uses, tokens, runtime, and recent activity. | Sufficient. |
| Output source | Keeps the parent row compact. | Overview/Output/Lineage distinguish runtime-only, retained, or evicted storage and show transcript/output paths. | Sufficient. |

The audit does not reinterpret a progress summary as the final answer. A future
bounded result preview should be accepted only if user evidence shows that one
extra detail/thread action is a material workflow cost.

## Reference Comparison

| Runtime | Navigation identity | Terminal/output model | Resume/fork lesson | Applied decision |
|---|---|---|---|---|
| Claude Code Ripe | Teammate navigation selects background task/transcript identity. | `TaskStateBase` owns terminal status plus `outputFile`; `TaskOutput` tails file output and spills bounded pipe output to disk. | Output identity must survive UI lifecycle changes. | Keep durable transcript/output references behind bounded TUI projections. |
| Codex | Picker rows carry nickname, role, canonical agent path, running, and closed state; Alt-left/right provides direct cycling. | Parent history records spawn, interaction, wait, close, and resume outcomes with bounded previews. | Canonical thread identity and cheap switching matter more than copying its Ratatui architecture. | Keep stable Agent/thread IDs and both picker plus direct-cycle navigation. |
| Crush | Session records parent session, usage, cost, and summary identity; CLI resumes by explicit or latest session. | Usage belongs to durable session state. | Parent lineage and usage are useful durable metadata, but its session store is not an Agent attention specification. | Preserve lineage/usage fields without replacing current transcript storage. |
| Eino-Agent | `/agent`, searchable visible labels, status attention, and Alt-left/right select leader or child views. | Parent trace is bounded; detail combines canonical runtime state with retained/evicted transcript and output. | Live attach requires current runner ownership; restart restores replay-only; fork records parent session. | Retain current ownership model and close only reproduced workflow defects. |

## Rejected Work

- embedding the complete child result or output file in every parent tool row;
- using an LLM-generated live summary as runtime truth;
- restoring a disk `running` Agent as actionable after restart;
- replaying stale permission/question handles;
- replacing the current session/transcript store without a measured ownership
  or multi-process correctness failure;
- opening a visual redesign from latency numbers already far below budget.

## Primary Source Anchors

### Eino-Agent

- `internal/tui/agent_thread_picker.go`: visible labels, search, status/mode, and
  attention rows
- `internal/tui/thread_navigation.go`: transactional switch and transcript
  projection
- `internal/tui/pty_workflow_unix_test.go`: real PTY workflow and observed
  action/latency evidence
- `internal/tui/performance_bench_test.go`: p95 attention and transcript gates
- `internal/tui/agent_trace.go`, `internal/tui/agent_detail.go`: bounded parent
  and full terminal surfaces
- `engine/agent_detail.go`, `engine/thread_catalog.go`: retained/evicted output,
  replay mode, and lineage
- `engine/session_restore_test.go`, `engine/session_actions_test.go`: live,
  restart, resume, and fork evidence

### References

- `.reference/claude-code-ripe/src/Task.ts`
- `.reference/claude-code-ripe/src/utils/task/TaskOutput.ts`
- `.reference/claude-code-ripe/src/hooks/useBackgroundTaskNavigation.ts`
- `.reference/codex/codex-rs/tui/src/multi_agents.rs`
- `.reference/crush/internal/session/session.go`
- `.reference/crush/internal/cmd/root.go`
