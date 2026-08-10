# P47.7 Lazy Execution Detail Implementation Plan

> **Historical use:** Retained as completed implementation evidence; do not
> execute this checklist as current work.

**Status:** historical
**Created:** 2026-08-07
**Completed:** 2026-08-07

> **Ownership:** test-first execution plan for the accepted
> [`P47.7`](../../migration/plans/p47-task-explorer-remediation.md#p477-lazy-execution-detail)
> slice and final G41 closure

**Goal:** Add bounded, lazy `transcript`, `output`, and `lineage` tabs to an
exact Ctrl+T execution without performing I/O for cached `overview` or
`activity`, and without ever resolving a retained historical row against a
newer generation.

**Architecture:** `RuntimeStateStore` remains the execution identity owner and
`TaskExplorerPanel` remains the presentation owner. The existing
`AgentTranscriptPage` selector supplies exact, cursor-bound transcript pages.
Because `AgentDetailSnapshot(agentID)` can resolve a newer canonical generation,
the engine adds one narrow exact-execution detail reader that captures and
validates `(AgentID, Generation, SessionID, ThreadID)` before and after optional
bounded terminal-output I/O. Nonterminal generations never read a reused prior
terminal file. Bubble Tea commands carry immutable row, tab, request-generation,
and cursor identity; reducer application rejects any result that no longer
matches the open panel and selected tab.

**Tech Stack:** Go 1.26.5, engine runtime snapshots and transcript selectors,
Bubble Tea v2 commands/messages, existing agent detail renderers, PTY/vt terminal
tests, the race detector, and repository Makefile gates.

## Frozen Contract

- Execute only P47.7. Keep the P47 adoption decision `combine`: engine runtime
  truth and exact generation semantics are project-owned, while the bounded
  tab interaction reuses already verified TUI patterns.
- WorkItems retain only `overview` and `activity`. Exact executions expose, in
  order, `overview`, `activity`, `transcript`, `output`, and `lineage`.
  Left/Right and `h`/`l` move one supported tab; `Tab`/`Shift+Tab` remain focus
  navigation.
- `overview` and `activity` remain pure cached projections. Rendering, resize,
  scroll, focus changes, mouse input, and reducer replay never call an engine
  reader, filesystem, provider, tool, model, action provider, or Git.
- Entering a deep execution tab starts at most one lazy Bubble Tea command.
  Explicit `r` supersedes the active request. Transcript PgUp/Home at the top
  may request the next opaque cursor. Returning to a completed cached tab does
  not re-read until explicit refresh.
- Transcript uses `AgentTranscriptPage(AgentID, Generation, Cursor, Limit)` and
  retains its bounded limit and opaque cursor validation. No second transcript
  store, direct transcript file read, or AgentID-only fallback is introduced.
- Output and lineage use one narrow exact-execution reader. Its request requires
  `AgentID`, positive `Generation`, `SessionID`, and `ThreadID`. It validates the
  captured canonical runtime snapshot before and after any output read. Lineage
  does not read the output file; output uses the existing bounded tail helper
  only for a terminal current generation. No second output store or durable
  schema is introduced.
- The exact detail response contains only the captured runtime agent identity,
  bounded output when requested, truncation state, and a bounded load warning.
  It does not query AgentRunner messages, steering state, model/tool paths, or
  execution control.
- A retained historical row whose AgentID now names a newer generation is
  explicitly unavailable. Missing or mismatched session/thread identity also
  fails closed. Neither returned data nor warnings may contain facts from the
  newer generation.
- Every async request/result is correlated to exact selection, session, thread,
  generation, request generation, tab, and cursor. Selection movement, filter
  changes, panel close/reopen, tab changes, replacement generations, superseding
  refreshes, duplicates, and out-of-order completion invalidate old results.
- Bubble Tea command execution may finish after invalidation; cancellation is
  modeled as token invalidation and result rejection, not as guaranteed
  interruption of a synchronous reader.
- Applying lazy results never calls Task Explorer actions or changes the exact
  action/navigation target. P47.1 action intent, P47.3 navigation, and P47.4-6
  selection/filter/focus/mouse contracts remain unchanged.
- Deep bodies use the existing bounded transcript/output/lineage renderers,
  preserve independent detail scrolling, remain understandable without color,
  and fit narrow, compact, standard, and wide frames.
- Final code verification uses `make fmt`, `make lint`, `make test`, and
  `make build`; closeout also uses `make lint-new`, `make docs-check`, migration
  queue/manifest checks, `make test-pty`, focused race tests, and
  `git diff --check`.

## File Structure

| File | Responsibility in this change |
|---|---|
| `engine/agent_detail.go` | Expose a bounded exact-generation output/lineage read boundary and reject stale identity before output I/O. |
| `engine/agent_detail_test.go` | Prove live/current-terminal availability, historical fail-closed behavior, bounded output, and no lineage output read. |
| `internal/tui/task_explorer_lazy_detail.go` | Own private exact request/result envelopes and per-tab async state. |
| `internal/tui/task_explorer_panel.go` | Add execution-only tabs, lazy command triggers, paging, invalidation, and pure bounded projections. |
| `internal/tui/agent_transcript_page.go` | Add the Task Explorer surface while retaining the existing exact pager contract. |
| `internal/tui/app.go` and `internal/tui/thread_navigation.go` | Wire readers and route completed results only to the open Ctrl+T panel. |
| `internal/tui/p47_7_task_explorer_lazy_detail_test.go` | Prove capabilities, lazy dispatch, correlation, negative interleavings, purity, and structural frames. |
| `internal/tui/pty_workflow_unix_test.go` | Prove deep-tab inspection, paging/resize, close, and terminal restoration in a real PTY. |
| Migration, architecture, status, history, and plan owners | Close G41/P47, remove P47.7, and promote P48.1 as the sole Ready slice. |

### Task 1: Reproduce the exact-reader and lazy-detail gaps

**Files:**

- Modify: `engine/agent_detail_test.go`
- Add: `internal/tui/p47_7_task_explorer_lazy_detail_test.go`
- Modify: `internal/tui/pty_workflow_unix_test.go`

- [x] **Step 1: Add engine exact-identity RED tests**

Require exact live and current terminal reads, session/thread validation,
historical generation rejection before a newer output path can be observed,
bounded valid-UTF-8 output, and a lineage request that performs no output read.

- [x] **Step 2: Add execution-only capability and lazy-dispatch RED tests**

Require five execution tabs and two WorkItem tabs. Count snapshot, action,
transcript, and exact-detail providers. Repeated cached rendering and navigation
through `overview`/`activity` must dispatch nothing; entering each deep tab must
return one command with exact identity, tab, cursor, and bounded transcript
limit.

- [x] **Step 3: Add async negative-interleaving RED tests**

Cover stale selection, replacement generation, filter/refilter, panel close,
tab change, superseding refresh, duplicate result, and out-of-order completion.
Require no stale lines, scroll drift, action-provider call, or navigation target
mutation.

- [x] **Step 4: Add availability, bounds, and structural RED tests**

Cover live, current terminal, retained historical, missing reader, long content,
no-color output, transcript cursor paging, independent scroll, and 40/80/120/180
width structures.

- [x] **Step 5: Extend the real PTY smoke and verify red**

Drive Ctrl+T to an exact execution, focus detail, visit all deep tabs, resize,
return to cached tabs, close, and assert terminal cleanup. Run:

```bash
go test ./engine -run 'TestAgentExecutionDetail' -count=1
go test ./internal/tui -run 'TestP477' -count=1
go test ./internal/tui -run '^TestTUIWorkflowPTY$' -count=1
```

Expected: FAIL because the exact detail reader, execution-only tabs, async
messages, Task Explorer transcript surface, and PTY deep-tab flow do not exist.

- [x] **Step 6: Commit the red regression**

```bash
git add engine/agent_detail_test.go \
  internal/tui/p47_7_task_explorer_lazy_detail_test.go \
  internal/tui/pty_workflow_unix_test.go
git commit -m "test(tui): reproduce Task Explorer lazy detail gaps"
```

### Task 2: Add the engine-owned exact execution detail reader

**Files:**

- Modify: `engine/agent_detail.go`

- [x] **Step 1: Define the narrow request/result contract**

Add required exact agent/generation/session/thread request fields and an
`IncludeOutput` capability flag. Return captured runtime identity plus bounded
output facts; keep errors typed so a stale row differs from invalid input and
absence.

- [x] **Step 2: Validate identity before and after optional output I/O**

Capture agent/thread/revision under the runtime store read boundary, validate
all request identity and thread association, then use the existing bounded
output helper only for terminal output requests. Revalidate exact identity and
path after I/O. Never fall back by AgentID after a mismatch or expose a reused
prior-generation file while the current generation is nonterminal.

- [x] **Step 3: Run focused engine tests and race**

```bash
go test ./engine -run 'TestAgentExecutionDetail|TestAgentDetailSnapshot' -count=1
go test -race ./engine -run 'TestAgentExecutionDetail|TestAgentDetailSnapshot' -count=1
```

### Task 3: Add correlated lazy tabs to Ctrl+T

**Files:**

- Add: `internal/tui/task_explorer_lazy_detail.go`
- Modify: `internal/tui/task_explorer_panel.go`
- Modify: `internal/tui/agent_transcript_page.go`
- Modify: `internal/tui/app.go`
- Modify: `internal/tui/thread_navigation.go`

- [x] **Step 1: Add immutable private request/result state**

Bind per-tab state to exact selection/session/thread and monotonic request
generation. Store `tab` and `cursor` in every message. Implement begin,
supersede, invalidate, apply, and duplicate rejection without goroutine-owned
widget mutation.

- [x] **Step 2: Reuse the exact transcript pager**

Add a Task Explorer surface, tag requests as transcript-tab work, route results
only while Ctrl+T is open on the same exact row/tab, and preserve first/older
cursor behavior and page bounds.

- [x] **Step 3: Wire the exact output/lineage command**

Inject the engine exact-detail provider into the panel. Output requests set
`IncludeOutput`; lineage requests do not. Apply only exact matching results and
render explicit unavailable/loading/warning states.

- [x] **Step 4: Extend tab navigation without changing focus/actions**

Keep WorkItems clamped to two tabs and executions bounded to five. Return a
command only on a deep-tab lazy event, explicit reload, or transcript older-page
request. Invalidate in-flight work on semantic selection/tab/panel changes.

- [x] **Step 5: Preserve render purity and bounded layout**

Reuse current transcript/output/lineage builders from reducer-owned cached
results. Render performs no read or dispatch, and detail offsets clamp locally
across result arrival and resize.

- [x] **Step 6: Run focused compatibility and race tests**

```bash
go test ./internal/tui -run 'TestP477|TestP476|TestP475|TestP474|TestP471|TestP473' -count=1
go test -race ./internal/tui -run 'TestP477|TestP476|TestP475|TestP474|TestP471|TestP473' -count=1
```

### Task 4: Review, verify, and close P47.7

**Files:**

- Modify only source-backed migration/architecture/status/history owners.

- [x] **Step 1: Obtain a bounded independent review**

Review exact identity, pre-I/O validation, request invalidation, duplicate and
out-of-order rejection, render purity, cursor paging, action isolation, bounds,
and compatibility with P47.1-P47.6. Resolve every concrete finding before final
gates.

- [x] **Step 2: Run focused PTY and package verification**

```bash
go test ./engine -run 'TestAgentExecutionDetail|TestAgentDetailSnapshot|TestAgentTranscriptPage' -count=1
go test ./internal/tui -run 'TestP477|TestP476|TestP475|TestP474|TestP471|TestP473|TestTUIWorkflowPTY' -count=1
make test-pty
```

- [x] **Step 3: Run repository gates**

```bash
make fmt
make lint
make test
make build
make lint-new
make docs-check
go run ./scripts/migration_queue check
go run ./scripts/migration_manifest.go check
git diff --check
```

- [x] **Step 4: Close only delivered facts**

Mark P47.7 complete, close G41/P47, remove its queue row, promote P48.1 as the
sole `Ready` row, add one history record, update affected TUI/runtime ownership
and verification evidence, mark this plan historical, and re-run documentation
and queue checks.

- [x] **Step 5: Push, merge through green PR, and continue**

Push the topic branch, open one independently reviewable PR, wait for all
required checks, squash merge, delete the branch, and start P48.1 from fresh
`origin/master`.
