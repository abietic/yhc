# Worktree Lifecycle Audit

**Status:** reference-snapshot
**Snapshot:** 2026-07-18

> **Ownership:** source-backed comparison of worktree creation, owner binding,
> execution CWD, dirty handoff, cleanup, cancellation, persistence, and
> recovery; current execution order belongs in `PLAN.md`

## Observable Question

What project-owned contract should govern coding-Agent worktree creation,
owner/session binding, execution directory, cancellation, dirty state, terminal
handoff, cleanup, conflicts, process restart, and supported entrypoints?

The audit distinguishes:

- a normal workspace or configured CWD;
- recognition that the current directory is a linked Git worktree; and
- a managed worktree whose creation, owner, cleanup, and recovery are product
  responsibilities.

## Snapshots

| Repository | Revision |
|---|---|
| Eino-Agent | `026e6001ac24` |
| Claude Code Ripe | `4b9d30f79532` |
| Codex | `800715d20165` |
| Crush | `3446255daa02` |
| Grok Build | `b189869b7755` |
| OpenCode | `4a760b574349` |
| Pi | `c6d8371521fc` |

These are local research snapshots, not claims about current upstream heads.

## Current Eino-Agent Evidence

| Boundary | Verified behavior | Consequence |
|---|---|---|
| top-level tools | Enter/ExitWorktree use package-global state and `os.Chdir`. | Concurrent engines and ACP sessions share process cwd and state. |
| top-level lifecycle | raw Git commands create/reuse a branch and Exit optionally removes it. | No context cancellation, owner validation, session checkpoint, dirty handoff, or cleanup recovery. |
| Agent isolation | `AgentRunner` asks `WorktreeManager` for a path and passes it as explicit child CWD. | This is the safer existing product outcome worth preserving. |
| naming | manager path and branch derive from a sanitized display slug. | Distinct Agents with the same name can collide. |
| source state | worktree starts from committed HEAD. | Parent uncommitted changes are silently absent. |
| terminal cleanup | clean worktrees are removed; dirty/new-commit worktrees are retained and returned. | Useful fail-closed behavior exists, but retained handoff is only path/branch. |
| cleanup ownership | manager deletes its in-memory record before status/remove completes. | Failure cannot be retried through the same owner. |
| durability | Agent metadata stores path/branch; manager map does not survive restart. | Resume can display metadata but cannot reconstruct safe cleanup authority. |
| Agent input | `isolation` and explicit `cwd` may both be supplied. | Source and effective child CWD semantics are ambiguous. |

Current source anchors:

- [`tools/worktree.go`](../../../../tools/worktree.go#L14)
- [`WorktreeManager`](../../../../tools/worktree_manager.go#L126)
- [`WorktreeManager.CreateForAgent`](../../../../tools/worktree_manager.go#L191)
- [`WorktreeManager.CleanupForAgent`](../../../../tools/worktree_manager.go#L250)
- [`prepareAgentWorktree`](../../../../tools/agent_runner.go#L456)
- [`finalizeAgentWorktreeLocked`](../../../../tools/agent_runner.go#L1218)
- [`AgentTool`](../../../../tools/agent.go#L79)
- [`resolveResumedExecutionContext`](../../../../engine/session_restore.go#L78)

Focused Plan/Worktree tests passed across `engine`, `tools`, `internal/tui`,
and `server/acp`. The standalone tool test checks constructor names only.
Manager/Agent tests cover create, explicit child CWD, clean cleanup, and dirty
retention, but not process-global entrypoints, cancellation, collision, restart
ownership, or cleanup retry.

## Cross-Reference Matrix

| Reference | Managed creation | Owner/CWD | Dirty, cleanup, recovery | Verdict for this question |
|---|---|---|---|---|
| Claude Code Ripe | CLI, session tools, Agent isolation, hooks/Git | active session record; explicit worktree path; process and project CWD are synchronized in its single-session model | remove refuses dirty/unknown without explicit discard; transcript restore and stale cleanup preserve ownership | adapt ownership, dirty failure, resume, and fork rules; do not copy process `chdir` |
| Codex | no product-level managed Git worktree found | threads run in a CWD but do not own creation/cleanup | diff helpers are not an isolation lifecycle | no managed mechanism to adopt |
| Crush | no managed creation found | workspace owns a configured WorkingDir | detects worktree root for config containment only | adapt only the “recognition is not ownership” boundary |
| Grok Build | first-class task/session worktree builder | `isolation` and `cwd` are mutually exclusive; child receives worktree path | cancellation, dirty report/copy modes, cleanup, orphan recovery, and pool metadata | adapt cancellation, explicit binding, dirty report, and recovery; reject platform/backend breadth |
| OpenCode | independent Worktree Service | workspace/session binding is separate from creation; Ready/Failed events | force remove/reset exists; startup/bootstrap lifecycle is observable | adapt service/events; reject force removal without dirty confirmation |
| Pi | no managed creation found | recognizes linked-worktree `.git`/`commondir` for branch display | no owner, cleanup, handoff, or recovery | no managed mechanism to adopt |

## Important Reference Boundaries

### Claude Code Ripe

- `src/utils/worktree.ts` owns naming, creation, session records, cleanup, and
  stale-worktree rules.
- `src/tools/EnterWorktreeTool/EnterWorktreeTool.ts` and
  `src/tools/ExitWorktreeTool/ExitWorktreeTool.ts` provide session transitions
  and dirty confirmation.
- `src/utils/sessionRestore.ts` restores only an existing owned path and strips
  ownership from forks.

Claude's process `chdir` behavior assumes a different process/session model and
is not safe to copy into Eino-Agent's concurrent ACP/Agent runtime.

### Grok Build

- `xai-tool-types/src/task.rs` makes `isolation` and explicit `cwd` mutually
  exclusive.
- `xai-grok-shell/src/session/worktree.rs` binds worktree creation and resume.
- `xai-fast-worktree/src/api.rs` owns cancellation and cleanup.
- `xai-fast-worktree/src/worktree/mod.rs` returns base/dirty-copy reports.

Git/JJ, overlay, btrfs, and pool mechanisms exceed the accepted Eino-Agent
scope.

### OpenCode

- `packages/opencode/src/worktree/index.ts` owns create/bootstrap/remove/reset.
- `packages/opencode/src/schema/worktree-event.ts` distinguishes Ready and
  Failed.

Its force-remove/reset behavior is intentionally not an Eino-Agent target.

### Codex, Crush, And Pi

Codex has repository/diff/CWD behavior but no verified managed worktree
service. Crush resolves a worktree root to constrain config discovery. Pi
resolves linked-worktree Git metadata for branch watching. None supplies
creation, owner, dirty handoff, cleanup, or crash recovery.

## Findings

Verified:

1. `os.Chdir` and package-global worktree state are incompatible with
   multi-session/process concurrency.
2. A configured CWD or linked-worktree detector is not a managed isolation
   lifecycle.
3. The current Agent explicit-CWD path is salvageable and already provides user
   value.
4. Dirty status, base commit, owner identity, and cleanup state are lifecycle
   data, not presentation strings.
5. Cleanup must retain ownership until removal succeeds; uncertainty must keep
   the worktree.
6. Restart recovery cannot infer deletion authority from path or branch name.

Recommendation:

- disable the standalone global tools immediately;
- preserve and adapt Agent `isolation="worktree"` behind a context-aware
  durable service;
- reject ambiguous simultaneous isolation/cwd;
- make source dirty state explicit and fail closed by default;
- retain changed work with a diff-oriented handoff and no automatic merge; and
- restore only project-owned records, with safe cleanup retry.

Unresolved and deliberately deferred:

- leader-session Enter/ExitWorktree and its project-identity semantics;
- automatic copying of dirty source state;
- automatic commit, merge, cherry-pick, or patch application;
- worktree pools and non-Git backends.

## Adoption Decision

`combine`: combine complementary worktree-isolation behaviors behind one
Eino-Agent service. This decision is local to the worktree subsystem and does
not combine it with Plan Mode.

The completed implementation contract is
[`migration/plans/p18-worktree-lifecycle.md`](../../plans/p18-worktree-lifecycle.md).
Current implementation facts belong in architecture documents.
