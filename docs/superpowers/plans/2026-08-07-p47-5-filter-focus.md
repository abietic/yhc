# P47.5 Filter and Focus Navigation Implementation Plan

> **Historical use:** Retained as completed implementation evidence; do not
> execute this checklist as current work.

**Status:** historical
**Created:** 2026-08-07
**Completed:** 2026-08-07

> **Ownership:** test-first execution plan for the accepted
> [`P47.5`](../../migration/plans/p47-task-explorer-remediation.md#p475-filter-and-focus-navigation)
> slice and partial G41 closure

**Goal:** Make the mixed Ctrl+T explorer locally filterable and explicitly
focusable with truthful text hints and keyboard/mouse parity, while preserving
the exact P47.1/P47.3/P47.4 runtime, action, and selection contracts.

**Architecture:** `TaskExplorerPanel` remains the Module and keeps one small
Interface for App key, mouse, refresh, and render calls. A panel-local filter
predicate composes with search over the cached row projection. One private
binding descriptor is the input-and-hint Seam. One render geometry snapshot
maps mouse input back to the same focus, selection, and scroll operations used
by keys. No engine, global keybinding schema, or detail-I/O Interface expands.

**Tech Stack:** Go 1.26.5, `TaskExplorerSnapshot`, Bubble Tea v2 key/mouse
messages, App layout geometry, PTY/vt terminal tests, standard Go tests, the
race detector, and repository Makefile gates.

## Frozen Contract

- Execute only P47.5; P47.6 overview/activity tabs and P47.7 lazy transcript,
  output, and lineage I/O remain out of scope.
- Adoption is `project-native`: filtering and focus are presentation-local and
  never rewrite the snapshot, engine ordering, `Hidden` facts, action
  declarations, runtime revisions, or exact execution identity.
- Filters cycle exactly `all -> active -> attention -> terminal -> all`.
  Search and the selected filter are a logical AND over the same cached rows.
- WorkItem `active` means exact `in_progress`; `pending`, non-canonical
  `blocked`, terminal, and unknown statuses are not inferred active.
  Execution `active` means `running`, `waiting_input`, or `paused`.
- WorkItem `terminal` means `completed`, `failed`, or `cancelled`. Execution
  `terminal` means `completed`, `failed`, or `cancelled`. `replay_only`,
  unknown, and hidden/evicted generations are not promoted to terminal rows.
- `attention` consumes only each row's existing `Attention` facts. Snapshot
  attention without a selectable row remains summary evidence and never
  creates or upgrades a row.
- Exact selection restoration, prior-cursor fallback, and empty-list clearing
  remain P47.4 behavior under filter changes, search, refresh, and resize.
- Normal focus has three explicit regions: search/filter controls, list, and
  detail. The panel opens on the list. `Tab` cycles controls -> list -> detail;
  `Shift+Tab` reverses. `/` enters search editing in controls. Search Enter
  applies and returns to the list; Esc leaves editing without clearing text.
- Enter on the list performs the current engine-declared inspect when present,
  opens current cached detail, and moves detail focus. Esc from controls or
  detail returns to the list; Esc from the list closes Ctrl+T.
- The canonical panel binding descriptor owns both resolution and hints for
  Enter, `x`, `s`, `p`, `c`, `n`, `r`, `f`, `/`, Tab, and Shift+Tab. The
  provider-less legacy fallback and its global `ContextTask` bindings remain
  unchanged and are not a second owner for the canonical panel.
- Action hints state disabled availability textually. Filters or focus never
  infer an action; exact execution actions still consume only engine
  `AllowedActions` and P47.1/P47.3 correlation.
- Render publishes bounded control, list-row, and detail hit regions. App
  routes TaskPanel mouse input before chat. Clicks change only local focus,
  exact selection, or filter/search state; wheel scroll uses the same clamped
  list movement as keys; no click submits a runtime action.
- Narrow, compact, standard, and wide no-color output exposes current filter,
  focus, search, local-hidden count, and resolved hints without color-only
  meaning. Real PTY evidence covers keyboard parsing, resize, and lifecycle;
  deterministic mouse tests cover pointer parity.
- Final code verification uses `make fmt`, `make lint`, `make test`, and
  `make build`; closeout also uses `make docs-check`, migration queue and
  manifest checks, and `git diff --check`.

## File Structure

| File | Responsibility in this change |
|---|---|
| `internal/tui/task_explorer_panel.go` | Own local filters, focus state, canonical binding/hint resolution, geometry, and mouse operations. |
| `internal/tui/app.go` | Route normalized TaskPanel mouse input before chat handling. |
| `internal/tui/p47_5_task_explorer_filter_focus_test.go` | Prove filter truth, search composition, focus, hints, actions, geometry, and mouse parity. |
| `internal/tui/pty_workflow_unix_test.go` | Prove real terminal keys, focus/filter text, narrow resize, close, and cleanup. |
| Migration, architecture, status, history, and plan owners | Close only P47.5's portion of G41, remove P47.5, and promote P47.6. |

### Task 1: Reproduce filter, focus, hint, and mouse gaps

**Files:**

- Add: `internal/tui/p47_5_task_explorer_filter_focus_test.go`
- Modify: `internal/tui/pty_workflow_unix_test.go`

- [x] **Step 1: Add the complete filter truth table**

Use mixed WorkItems and executions covering in-progress, pending, attention,
terminal, replay-only, unknown, and same-label/exact-generation collisions.
Drive `f` through the four filters and require exact group order, search AND
composition, unchanged source snapshot/Hidden facts, local-hidden text, exact
selection restoration, cursor fallback, and empty reset.

- [x] **Step 2: Add focus and one-owner hint tests**

Require list default focus, forward/reverse three-region cycles, `/` search
entry, Enter/Esc transitions, action-prompt priority, and textual current
filter/focus. Require unavailable WorkItem/replay actions to render `disabled`
and dispatch nothing, while an allowed execution retains its exact request.

- [x] **Step 3: Add App mouse parity tests**

Render once, then inject normalized clicks and wheel events at controls, list,
and detail coordinates. Require the same exact selection and clamped movement
as keys, local-only search/filter/focus updates, and zero chat selection/scroll
leakage or engine action dispatch.

- [x] **Step 4: Extend the real PTY smoke and verify red**

Extend the existing workflow through Ctrl+T, `f`, `/`, search text, Tab,
Shift+Tab, a 64x22 resize, Esc close, and terminal cleanup. Run:

```bash
go test ./internal/tui -run 'TestP475' -count=1
go test ./internal/tui -run '^TestTUIWorkflowPTY$' -count=1
```

Expected: FAIL because the current panel has no filter/focus text, Tab still
toggles detail, mouse falls through to chat, and `f` is ignored.

- [x] **Step 5: Commit the red regression**

```bash
git add internal/tui/p47_5_task_explorer_filter_focus_test.go \
  internal/tui/pty_workflow_unix_test.go
git commit -m "test(tui): reproduce Task Explorer filter and focus gaps"
```

### Task 2: Add local filter, focus, and canonical binding depth

**Files:**

- Modify: `internal/tui/task_explorer_panel.go`

- [x] **Step 1: Add private filter and focus values**

Keep zero values `all` and controls-safe, initialize Show on list focus, and
preserve local filter/search across refresh. Add pure row predicates using only
frozen WorkItem status, execution phase, and row attention facts.

- [x] **Step 2: Compose filter and search behind one projection Seam**

Apply filter AND query while retaining the P47.4 group order and exact
selection restoration. Compute local-hidden count separately from immutable
engine `Hidden` counts and surface a filter-aware empty state.

- [x] **Step 3: Resolve keys and hints from one descriptor**

Replace duplicated canonical panel literals with one command descriptor used
by `HandleKey` and responsive help. Preserve modal prompt ownership and exact
action capture. Add textual `disabled` state from engine declarations only.

- [x] **Step 4: Render explicit controls and focus**

Add a bounded no-color-safe controls row with `Focus`, selected filter, search,
and local-hidden facts. Make Enter, Esc, Tab, Shift+Tab, `/`, and `f` follow the
frozen state machine without adding detail tabs or I/O.

- [x] **Step 5: Run focused and race tests**

```bash
go test ./internal/tui -run 'TestP475|TestP474|TestP471|TestP473' -count=1
go test -race ./internal/tui -run 'TestP475|TestP474|TestP471|TestP473' -count=1
```

### Task 3: Publish geometry and consume TaskPanel mouse input

**Files:**

- Modify: `internal/tui/task_explorer_panel.go`
- Modify: `internal/tui/app.go`

- [x] **Step 1: Publish render-derived hit regions**

Record only the latest bounded control/filter/search, visible-list-row, and
detail rectangles. Geometry is presentation output, not persisted or engine
state. Clear it on unavailable or stale layouts.

- [x] **Step 2: Reuse keyboard operations for mouse behavior**

Click controls to focus search or select an exact filter, click a visible row
to set the same exact selection as keyboard movement, click detail to focus it,
and wheel the visible list through the existing clamped move path.

- [x] **Step 3: Route before chat and prove containment**

Convert App coordinates through `overlayRect`, hand TaskPanel events to the
panel, and consume them before sidebar/chat selection and scrolling. Repeat
focused mouse, dialog-stack, P47.4 geometry, and race tests.

### Task 4: Verify the PTY path and close P47.5

**Files:**

- Modify only architecture, migration, status, history, and plan owners that
  already own Task Explorer and G41 facts.

- [x] **Step 1: Run PTY and complete TUI verification**

```bash
go test ./internal/tui -run '^TestTUIWorkflowPTY$' -count=1
go test ./internal/tui -count=1
```

- [x] **Step 2: Request one bounded independent review**

Review filter truth, exact selection, prompt/focus priority, descriptor/hint
consistency, mouse-before-chat containment, render geometry, and absence of
P47.6/P47.7 scope. Apply only source-backed findings and repeat affected tests.

- [x] **Step 3: Synchronize owners and promote one slice**

Mark P47.5 complete, retain G41 as partial, remove P47.5 from `queue.yaml`,
promote only P47.6, render `PLAN.md`, add one historical record, and mark this
plan historical/executed.

- [x] **Step 4: Run documentation and repository gates**

```bash
make fmt
make lint
make test
make build
make docs-check
go run ./scripts/migration_queue check
go run ./scripts/migration_manifest.go check
git diff --check
```

- [x] **Step 5: Inspect and commit only P47.5 scope**

Stage only P47.5 files; leave `PROJECT_GUIDE.md` and `artifacts/` untouched.
The local commit records the user problem, `project-native` decision,
compatibility, rollback, PTY, and local evidence. Push, remote CI, squash
merge, and branch deletion remain separate protected-master integration gates.
