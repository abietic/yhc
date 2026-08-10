# P31.5 Old-Owner Closeout Verification

**Status:** verification
**Last verified:** 2026-07-31
**Scope:** P31.5 only

> **Ownership:** reproducible acceptance evidence for explicit Task/Todo/Agent
> owners, package and AppState fallback deletion, canonical task-facing
> projections, standalone isolation, exact queued-input cancellation,
> entrypoint labels, and the retained WorkBoard reader floor.

## Acceptance Evidence

| Boundary | Evidence |
|---|---|
| Explicit QueryEngine owners | Each root lineage owns one WorkBoard-backed TaskManager facade and AgentRunner. Every tool execution binds that manager, Todo authority, and runner; children share the same pointers. Construction reads no package Todo state. |
| Fail-closed direct calls | Unbound Task/Todo/Agent adapters return `ErrMissingToolOwner` and mutate nothing. Explicit non-Session callers bind their own TaskManager, Todo authority, and runner; successful Task/Todo result bytes remain exact. |
| Deleted compatibility owners | Production has no package Todo map, default TaskManager, default AgentRunner, `RuntimeTaskSnapshotCurrent`, AppState task store, `TaskAgentSnapshot`, or package task/Agent snapshot fallback. |
| Standalone MCP | Each `Serve` constructs one fresh ephemeral Todo authority and TaskManager. The exact allowlist contains TaskCreate/Get/List/Update/Stop/Output, combined Task, and TodoWrite only; two runtimes cannot observe one another and no Agent or Session artifact is reachable. |
| Canonical task surfaces | `/tasks`, Ctrl+T, Ctrl+B, `/team`, activity, sidebar, thread navigation/detail, queued input, and Agent eviction consume TaskExplorer rows. `/tasks` reports `durability=durable-session-workboard` and `control=read-only-command`. |
| Exact human control | The TUI has no TaskManager, AgentRunner, or ID/status mutation provider. Every mutation carries exact BoardID/revision and AgentID/generation through `ApplyTaskExplorerAction`; legacy QueryEngine-scoped Go/model-tool compatibility is not a TUI provider. |
| Message identity | Send uses the request ID as command UUID and returns it as MessageID. Retry within one generation does not enqueue twice. Cancel and child drain serialize on that generation's message lock and return `input_cancelled`, `input_not_pending`, or `stale_generation`. |
| Legacy read-only access | A Session without authoritative WorkBoard projection retains exact-generation transcript and navigation rows only. It receives no mutation action. |
| Lifecycle and rollback | Resume, fork, delete, compaction, v3 relations, settlement, eviction, and recovery retain P31.1b-P31.4 behavior. P31.5 adds no artifact or reader-floor transition and rollback cannot restore a package global or AppState owner. |
| Review | Independent review first challenged the retained exported ID-only Go compatibility methods. Production-call evidence plus a new static TUI source gate proved the frozen boundary deletes their TUI provider wiring, not the explicit QueryEngine API; re-review withdrew the finding and reported no findings. |

## Source Gates

```text
test -z "$(rg -n --glob '*.go' 'DefaultTaskManager|DefaultAgentRunner|RegisterAgentExecutor|RuntimeTaskSnapshotCurrent|GetBackgroundAgent|BackgroundAgentEntry|GetTodoItems|SetTodoItems|TaskAgentSnapshot|AppStateTask|SelectTaskAgentSnapshot|addAppStateTask' engine internal/tui server tools || true)"
test -z "$(rg -n --glob '*.go' --glob '!**/*_test.go' 'TaskManager|AgentRunner|SendAgentMessage\(|CancelAgentQueuedInput\(|\.AbortAgent\(|\.PauseAgent\(|\.ResumeAgent\(' internal/tui || true)"
go test ./internal/tui -run TestP315TUIHasNoLegacyTaskOrAgentMutationProvider -count=1
```

## Focused Commands

```text
go test ./tools -count=1
go test ./engine -count=1
go test ./internal/tui -count=1
go test ./server/mcp -count=1
go test -race ./tools ./server/mcp -count=1
go test ./tools -run 'TestDirectLogicalWorkToolsFailClosedWithoutOwner|TestEphemeralTodoAuthoritiesRemainIsolatedUnderConcurrency|TestAgentRunnerExactGenerationMessageRetryAndCancellation|TestAgentRunnerExactCancellationLinearizesWithDrain' -count=1
go test ./engine -run 'TestRootLineageSharesOneExplicitLogicalWorkRuntime|TestP315ExplorerMessageIdentityRetryAndCancellation|TestTaskExplorerUnavailableWorkBoardRetainsExactReadOnlyExecutions' -count=1
go test ./server/mcp -run 'TestStandaloneMCPExposesExactExplicitOwnerAllowlist|TestStandaloneMCPRuntimeIsFreshPerServeInvocation' -count=1
go test ./internal/tui -run 'TestP315BackgroundAndTeamUseExactGenerationActions|TestP315TUIHasNoLegacyTaskOrAgentMutationProvider|TestAgentThreadComposerSendsToChildWithoutInterruptingLeader' -count=1
```

## Repository Closeout

```text
make fmt
make lint
make lint-new
make test
make build
make docs-check
make docs-check-ci
go run ./scripts/migration_manifest.go check
git diff --check
```

All commands passed. GitHub Actions billing or usage failures may be waived
only after the exact job annotation proves that no runner started; they are
never described as green CI.
