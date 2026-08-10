# P47.6 Snapshot Detail Structure Implementation Plan

> **Historical use:** Retained as completed implementation evidence; do not
> execute this checklist as current work.

**Status:** historical
**Created:** 2026-08-07
**Completed:** 2026-08-07

> **Ownership:** test-first execution plan for the accepted
> [`P47.6`](../../migration/plans/p47-task-explorer-remediation.md#p476-snapshot-detail-structure)
> slice and partial G41 closure

**Goal:** Give the mixed Ctrl+T explorer bounded, explicit `overview` and
`activity` detail tabs that consume only its cached snapshot, without pulling
P47.7 transcript, output, or lineage I/O forward.

**Architecture:** `TaskExplorerPanel` remains the Module and `Refresh` remains
its only snapshot-provider boundary. The panel takes a defensive local copy,
projects the selected exact row through two pure detail builders, and owns a
detail-only tab plus viewport offset. Keyboard and mouse operations mutate only
those presentation values. No engine API, Bubble Tea command, async result,
durable schema, or second runtime owner is added.

**Tech Stack:** Go 1.26.5, `engine.TaskExplorerSnapshot`, Bubble Tea v2
key/mouse messages, App layout geometry, PTY/vt terminal tests, standard Go
tests, the race detector, and repository Makefile gates.

## Frozen Contract

- Execute only P47.6. P47.7 lazy `transcript`, `output`, and `lineage` tabs,
  readers, requests, results, cursors, and correlation remain out of scope.
- The P47 adoption decision remains `combine`; this presentation seam is
  project-owned. Current engine snapshot ownership, P47.1 action identity,
  P47.3 navigation, and P47.4-P47.5 row/filter/focus/mouse contracts remain
  unchanged.
- The detail header names the selected row kind and exposes textual
  `overview` and `activity` tabs. Default and selection-change tab is
  `overview`; the current tab is understandable without color or glyph shape.
- While detail owns focus, Left/Right and `h`/`l` switch tabs and reset only
  the detail offset. `Tab` and `Shift+Tab` retain P47.5 focus cycling. List
  focus retains its existing navigation and exact-selection semantics.
- Detail focus gives Up/Down and `j`/`k`, PageUp/PageDown, Home/End, and wheel
  input to an independent `detailOffset`. List `offset`, cursor, exact
  selection, filter, search, action intent, and runtime revisions do not move.
- WorkItem overview may show only its cached identity, status, owner, revision,
  order, title/form/description, dependencies, attention, terminal reason, and
  result. Execution overview may show only its exact identity, phase/status,
  name/task/description, session/thread/parent/display-mode, replay, and
  predispatch facts.
- WorkItem activity may show `LinkedLive`, exact `ExecutionKeys`, links that
  match `(BoardID, WorkItemID)`, and snapshot attention/diagnostics explicitly
  associated with that item. With none, it says exactly
  `No cached execution activity for this WorkItem`.
- Execution activity may show cached `Activity`, last tool, tool/token counts,
  observation ordinal, exact-generation attention, and links that match
  `(AgentID, Generation)`. With none, it says exactly
  `No cached activity for this exact execution`.
- Global or partially identified attention, unrelated diagnostics, same-title
  WorkItems, same-thread executions, and other generations never become facts
  of the selected row. Transcript paths never imply that a file was read.
- `Refresh` deep-copies the outer snapshot slices, every nested WorkItem,
  execution, and link slice consumed by the panel, and hidden-count maps.
  Mutating provider-owned backing storage after refresh cannot mutate the
  cached frame. An explicit later refresh may observe the new provider value.
- Exact selection change or empty fallback resets `detailTab` and
  `detailOffset`. Refresh of the same exact selection preserves the tab and
  clamps presentation locally; render itself does not change the offset.
- Render, tab switch, detail scroll, mouse input, resize, and replayed key
  sequences call neither snapshot/action providers nor engine, transcript,
  filesystem, provider, or Git I/O. They return no Bubble Tea I/O command.
- Detail content and scroll indicators are bounded at narrow, compact,
  standard, and wide sizes. Wide geometry may project the same pure detail
  twice without changing state or dispatching work.
- Final code verification uses `make fmt`, `make lint`, `make test`, and
  `make build`; closeout also uses `make lint-new`, `make docs-check`, migration
  queue and manifest checks, `make test-pty`, and `git diff --check`.

## File Structure

| File | Responsibility in this change |
|---|---|
| `internal/tui/task_explorer_panel.go` | Own the defensive cached snapshot, detail tab/offset state, pure row projections, bounded rendering, keys, and detail wheel behavior. |
| `internal/tui/p47_6_task_explorer_snapshot_detail_test.go` | Prove row-kind capabilities, exact association, defensive copy, no dispatch, state transitions, scrolling, and structural frames. |
| `internal/tui/pty_workflow_unix_test.go` | Prove real terminal detail focus, tab switching, scrolling, resize, close, and cleanup. |
| Migration, architecture, status, history, and plan owners | Close only P47.6's portion of G41, remove P47.6, and promote P47.7. |

### Task 1: Reproduce the flat, mutable, non-scrollable detail gap

**Files:**

- Add: `internal/tui/p47_6_task_explorer_snapshot_detail_test.go`
- Modify: `internal/tui/pty_workflow_unix_test.go`

- [x] **Step 1: Add WorkItem and execution capability tests**

Use a mixed snapshot with same-label WorkItems, a shared-thread pair of
execution generations, related and unrelated links, attention, and
diagnostics. Require default `overview`, textual row kind and tab state,
Left/Right plus `h`/`l` switching, exact associated activity facts, and the two
frozen unavailable messages.

- [x] **Step 2: Add provider-purity and replay no-dispatch tests**

Count snapshot and action provider calls. After the one opening refresh,
repeat render, tab switches, detail scrolling, mouse wheel, and an identical
key replay through the panel and App reducer path. Require no additional
provider/action calls and no Tea command.

- [x] **Step 3: Add defensive-copy mutation tests**

After `Refresh`, mutate provider-owned WorkItem, execution, link, attention,
diagnostic, allowed-action, execution-key, and hidden-map backing storage.
Require the cached frame and exact row identity to remain unchanged. Then call
`Refresh` and require the new provider facts to appear.

- [x] **Step 4: Add bounded scroll and structural frame tests**

Use long exact related facts and narrow, compact, standard, and wide no-color
frames. Require a stable detail header, bounded width/height, independent list
and detail offsets, visible scroll position, correct Home/End/Page behavior,
selection preservation across resize, and deterministic WorkItem/execution x
overview/activity structural projections.

- [x] **Step 5: Extend the real PTY smoke and verify red**

Extend the current Ctrl+T flow through detail focus, activity tab, detail
scroll, 64x22 resize, overview restoration, close, and terminal cleanup. Run:

```bash
go test ./internal/tui -run 'TestP476' -count=1
go test ./internal/tui -run '^TestTUIWorkflowPTY$' -count=1
```

Expected: FAIL because the current detail is one flat projection, Left/Right
and detail scroll are ignored, provider-owned nested storage remains shared,
and the PTY has no explicit tab state.

- [x] **Step 6: Commit the red regression**

```bash
git add internal/tui/p47_6_task_explorer_snapshot_detail_test.go \
  internal/tui/pty_workflow_unix_test.go
git commit -m "test(tui): reproduce Task Explorer snapshot detail gaps"
```

### Task 2: Add the defensive cached detail projection

**Files:**

- Modify: `internal/tui/task_explorer_panel.go`

- [x] **Step 1: Deep-copy the provider snapshot at Refresh**

Add one private clone helper that copies all panel-consumed outer slices,
nested slices, and hidden maps. Keep `Refresh` as the only provider call and
retain P47.4 exact selection restoration over pointers into the panel copy.

- [x] **Step 2: Add private detail tab and offset values**

Add zero-value `overview`, `activity`, and an independent detail offset. Reset
them on Show, exact selection change, or empty state; preserve them for the
same exact selection across refresh. Never reuse the list offset.

- [x] **Step 3: Split detail into pure row-kind projections**

Replace the current mixed renderer with pure overview/activity builders and
exact related-fact selectors. Keep row-kind-specific empty activity text and
exclude unbound facts. Do not add an engine reader or inspect transcript paths.

- [x] **Step 4: Render a sticky textual tab header and bounded viewport**

Keep the tab header visible while the body scrolls. Use local effective
clamping for resize, a textual range indicator when truncated, and the same
side-effect-free builder for compact, inline, and wide frames.

- [x] **Step 5: Run focused and race tests**

```bash
go test ./internal/tui -run 'TestP476|TestP475|TestP474|TestP471|TestP473' -count=1
go test -race ./internal/tui -run 'TestP476|TestP475|TestP474|TestP471|TestP473' -count=1
```

### Task 3: Add detail-local keys and mouse scrolling

**Files:**

- Modify: `internal/tui/task_explorer_panel.go`

- [x] **Step 1: Extend the panel-owned binding and hint seam**

Declare previous/next detail-tab keys in the existing panel descriptor and
render their hints only while detail owns focus. Preserve Tab focus cycling,
action prompt priority, search ownership, and disabled engine-action hints.

- [x] **Step 2: Route navigation by current focus**

List focus keeps current selection movement. Detail focus routes vertical,
page, Home/End, and horizontal tab keys only to detail state. Selection and
runtime action identity remain unchanged.

- [x] **Step 3: Give detail wheel input the same local scroll operation**

When the latest render-derived detail rectangle owns a wheel event, focus the
detail and move only `detailOffset`. Retain P47.5 list-wheel parity and consume
all TaskPanel pointer input before chat.

- [x] **Step 4: Repeat focused, geometry, PTY, and package tests**

```bash
go test ./internal/tui -run 'TestP476|TestP475|TestTUIWorkflowPTY' -count=1
go test ./internal/tui -count=1
make test-pty
```

### Task 4: Review, verify, and close P47.6

**Files:**

- Modify only architecture, migration, status, history, and plan owners that
  already own Task Explorer and G41 facts.

- [x] **Step 1: Request one bounded independent review**

Review exact row association, clone completeness, render/provider purity,
selection/tab/offset state transitions, bounded layout, mouse containment,
and absence of P47.7 or engine scope. Apply only source-backed findings and
repeat affected tests.

- [x] **Step 2: Synchronize owners and promote one slice**

Mark P47.6 complete, retain G41 as partial, remove P47.6 from `queue.yaml`,
promote only P47.7, render `PLAN.md`, add one historical record, and mark this
plan historical/executed.

- [x] **Step 3: Run documentation and repository gates**

```bash
make fmt
make lint
make test
make build
make lint-new
make test-pty
make docs-check
go run ./scripts/migration_queue check
go run ./scripts/migration_manifest.go check
git diff --check
```

- [x] **Step 4: Inspect and commit only P47.6 scope**

Stage only P47.6 files; leave `PROJECT_GUIDE.md` and `artifacts/` untouched.
Push, remote CI, squash merge, and branch deletion remain separate
protected-master integration gates. Physical-terminal rendering remains a
separate non-CI boundary and cannot be inferred from PTY evidence.
