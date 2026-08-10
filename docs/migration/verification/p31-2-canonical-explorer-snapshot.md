# P31.2 Canonical Explorer Snapshot Verification

**Status:** verification
**Last verified:** 2026-07-31
**Scope:** P31.2 only

> **Ownership:** reproducible acceptance evidence for the process-local
> WorkBoard projection, immutable execution generations, deterministic bounded
> selector, cold bootstrap, compatibility adapter, failure isolation, and
> source-owner boundaries.

## Acceptance Evidence

| Boundary | Evidence |
|---|---|
| Projection authority | `ProjectionReducer` accepts an empty, identical, or newer validated full snapshot; rejects BoardID mismatch, revision regression, same-revision content mismatch, and 1,025-item input without replacement; and returns defensive record and diagnostic copies. |
| Publish linearization | Adapter tests cover exact next-revision reservation, durable commit before prepared swap, activation/recovery projection replacement, duplicate bootstrap, and independent prepared/active reducer ownership. |
| Post-commit uncertainty | Injected swap error and panic both return typed `committed_projection_uncertain` with `retry_safe=false`, retain the prior process projection, quarantine later mutations, preserve the committed durable revision, and repair only through a fresh adapter bootstrap. |
| Execution identity and replay | Ordered replay retains several immutable generations of one Agent with deterministic ordinals. Exact live attachment receives a new process-local ordinal; cold restore rows are ordinal-free and replay-only. Retained and hidden-generation lineage conflicts leave state unchanged. |
| Bounds and ordering | Tests cover 129 WorkItems, links, attention rows, and simultaneous live executions; 1,025 authoritative items; 128 diagnostics; 512-rune fields; 100-row terminal pages; hidden counts; dropped-event/eviction facts; and complete stable tie-breaks. |
| Explicit links and capabilities | Fixture-only exact links may relate several generations to one WorkItem. Missing and stale targets remain visible without inference or repair. Resolvable rows expose only `inspect`; production supplies no link input. |
| Defensive concurrency | Focused race tests cover concurrent WorkBoard commits, runtime lifecycle reduction/replay, selector reads, returned-map mutation, projection snapshots, and adapter activation. |
| Compatibility and scope | The old composition and new `TaskAgentSnapshot` adapter are deeply equal. Source gates show no TUI, ACP, Session-schema, Agent-admission, dispatch, production-link, or durable-event owner was added. |
| Review | Independent second-line review found one live-attach restore path that produced a row with neither an ordinal nor replay-only state. Both restore branches now assign a positive ordinal to live attachment and reserve ordinal-free rows for cold replay. Focused normal/race proof passed, and final re-review reported no findings. |

## Commands

```text
go test ./engine/internal/workboard -run 'TestProjection|TestLogicalWorkAdapter.*Projection' -count=1
go test ./engine -run 'Test(TaskExplorer|TaskAgentSnapshotCompatibility|RuntimeStateStore.*Execution)' -count=1
go test -race ./engine/internal/workboard -run 'TestProjection|TestLogicalWorkAdapter.*Projection' -count=1
go test -race ./engine -run 'Test(TaskExplorer|TaskAgentSnapshotCompatibility|RuntimeStateStore.*Execution)' -count=1
test -z "$(git diff --name-only origin/master -- engine/session server/acp internal/tui tools/agent.go tools/agent_runner.go)"
test "$(rg -l 'type WorkExecutionLink|\\[\\]WorkExecutionLink' engine --glob '*.go' | sort)" = $'engine/task_explorer.go\nengine/task_explorer_test.go'
make fmt
make lint
make lint-new
make test
make build
make docs-check
make docs-check-ci
go run ./scripts/migration_manifest.go sync
git diff --check
```

All commands passed. `make lint-new` reported zero issues and printed only an
unrelated processor warning for a previously removed temporary worktree. The
physical terminal-grid diagnostic remains the repository's existing opt-in
skip and is unrelated to this runtime slice. Independent re-review reported no
remaining findings. GitHub Actions billing/usage failures may be waived only
after the exact job annotation proves no runner started; they are never
described as green CI.
