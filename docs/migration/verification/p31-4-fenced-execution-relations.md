# P31.4 Fenced Execution Relations Verification

**Status:** verification
**Last verified:** 2026-07-31
**Scope:** P31.4 only

> **Ownership:** reproducible acceptance evidence for the forward-only
> WorkBoard v3 relation boundary, ordered Agent admission, exact-generation
> controls, settlement and Session lifecycle fencing, TUI request/result
> handling, and rollback.

## Acceptance Evidence

| Boundary | Evidence |
|---|---|
| Marker-last durability | The v2-to-v3 upgrade writes the bounded version-3 record before the version-2 `workboard/v3` marker. Authority/marker atomic-write stages, encode seams, marker reread, prepared repair, corrupt pairings, reader floor, and fresh reopen converge only to valid v2 or valid v3. |
| Ordered admission | Linked initial launch and continuation reserve an exact generation without carrying the runner mutex into WorkBoard, commit one immutable relation, publish launch, admit child metadata, install live state, and dispatch exactly once. Admission failure persists terminal pre-dispatch metadata and dispatches nothing. |
| Immutable relation | BoardID, WorkItem ID/revision, AgentID/generation, actor, parent lineage, tool cause, and UTC admission are validated and bounded. Duplicate exact input is idempotent; conflict, reassignment, overwrite, excess count, oversized fields, fork copy, and linked recovery fail closed. |
| Exact controls | Inspect/switch/send/pause/resume/cancel/continue are engine-declared from exact board and generation facts. Stale board/generation and forged unsupported requests do not mutate. Continuation appends N+1. Replay-only, unresolved, stale, and pre-dispatch rows expose no live mutation. |
| Settlement | Terminal WorkItem mutation requires every committed link to be durable-terminal or superseded. Reserved, live, cancellation-pending, corrupt, missing, and unresolved facts reject. Cancellation acceptance alone does not settle. |
| Session lifecycle | Active deletion first blocks runner, relation, action, and WorkBoard admission; then it verifies linked and parent-Session settlement under the WorkBoard lifecycle lock. Rejection reopens the gate, success retains closure, resume rebinds callbacks, fork removes links, and compaction revalidates exact version/identity/revision/relations. |
| TUI owner | Ctrl+T sends only exact engine action requests. Cancel confirms, send/continue collect payload, and switch/notices apply only after exact result identity. Source checks find no direct TUI `TaskManager` or `AgentRunner` mutation. |
| Compatibility | Unlinked WorkBoard v2 Sessions and Agent launches retain existing behavior and Task/Todo bytes. Plain/headless and ACP remain read-only degradation; standalone MCP stays isolated. |
| Review | Independent review found one replay-only continuation leak. The capability declaration and forged-request regression closed it; final re-review reported no findings. |

## Commands

```text
go test -race ./engine/internal/workboard/ -run 'TestExecutionLink' -count=1
go test ./tools/ -run 'TestAgentRunnerLinked|TestAgentRunnerExactGeneration' -count=1
go test -race ./engine/ -run 'TestP314' -count=1
go test ./internal/tui/ -run 'TestP314Explorer' -count=1
go test ./internal/tui/ -run '^TestTUIWorkflowPTY$' -count=1 -v
test -z "$(rg -n 'taskManager.*Stop|TaskManager.*Stop' internal/tui || true)"
git diff --check
make fmt
make lint
make lint-new
make test
make build
make docs-check
make docs-check-ci
go run ./scripts/migration_manifest.go check
```

All commands passed. GitHub Actions billing or usage failures may be waived
only after the exact job annotation proves that no runner started; they are
never described as green CI.
