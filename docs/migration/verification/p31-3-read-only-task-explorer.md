# P31.3 Read-only Task Explorer Verification

**Status:** verification
**Last verified:** 2026-07-31
**Scope:** P31.3 only

> **Ownership:** reproducible acceptance evidence for the presentation-only
> explorer, responsive projections, exact-generation compatibility fence,
> bounded rendering, PTY lifecycle, and source-owner boundaries.

## Acceptance Evidence

| Boundary | Evidence |
|---|---|
| Canonical list truth | Engine-backed Ctrl+T, Ctrl+B, `/team`, activity, and sidebar rows use `TaskExplorerSnapshot`. Presentation filters and viewport clipping preserve selector order. Standalone fixtures retain isolated compatibility fallbacks. |
| Responsive projection | Deterministic fixtures cover 40/80/120/180 columns, representative heights below and at or above 24 rows, and empty, plan-only, execution-only, mixed, blocked, attention, failure, and replay-only snapshots. |
| Display and interaction | Display-cell tests cover CJK, combining marks, ZWJ emoji, long tokens, no-color, reduced-motion, search, focus, exact identity refresh, pre-refresh-index fallback, and bounded physical rows. |
| Generation fence | Ctrl+B detail, transcript, navigation, and control entrypoints require the exact current `(AgentID, Generation)`. Parent-chat trace identity is immutable after first observation; retained g1 and initially unresolved links cannot acquire current g2. |
| Compatibility | Existing Ctrl+B Agent controls and local-task output/stop remain on their prior providers. Local tasks are explicitly labelled compatibility-only. `/team` remains an all-sub-agent read-only execution view because no separate TeamID relation exists. |
| Historical evidence | Compact Task/Todo summaries remain bounded recorded deltas. Expanded, raw, and transcript projections preserve complete sanitized recorded input/result evidence. |
| Cost and lifecycle | The 100-row steady-frame test retains the G11 p95 method and verifies no render-time provider reads. The Unix PTY covers explorer open/search/focus, Ctrl+B, `/team`, resize, close, and terminal restoration. |
| Scope | Source checks reject engine, WorkBoard, Session, ACP, tool, Agent dispatch, production-link, non-`inspect` action, and old-owner deletion changes. |
| Review | Independent review found selector reordering, AgentID-only chat navigation, an unlabelled local compatibility row, and a retained-generation upgrade. Focused fixes and regressions closed each defect; final re-review reported no findings. |

## Commands

```text
go test ./internal/tui -run '^TestP313' -count=1
go test -race ./internal/tui -run 'TestP313|TestAgentToolTrace|TestAppSyncAgentTrace|TestP271AgentLink' -count=1
go test ./internal/tui -run '^TestTUIWorkflowPTY$' -count=1 -v
go test ./internal/tui -count=1
test -z "$(git diff --name-only origin/master -- engine tools server)"
test -z "$(git diff --name-only origin/master -- engine/task_explorer.go engine/internal/workboard engine/session server/acp tools/agent.go tools/agent_runner.go)"
test -z "$(git diff -U0 origin/master -- internal/tui | rg '^[+].*(WorkExecutionLink|DefaultTaskManager|RuntimeTaskSnapshotCurrent)' || true)"
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

All commands passed. `make test` ran 6,611 tests; the only skip was the
repository's explicit opt-in physical terminal/font diagnostic, for which
P31.3 makes no physical-grid claim. `make lint-new` reported zero issues and
printed only the known processor warning for a previously removed temporary
worktree. GitHub Actions billing or usage failures may be waived only after
the exact job annotation proves that no runner started; they are never
described as green CI.
