# P47.3 Exact Thread Navigation Target Implementation Plan

> **Historical use:** Retained as completed implementation evidence; do not
> execute this checklist as current work.

**Status:** historical
**Created:** 2026-08-07
**Completed:** 2026-08-07

> **Ownership:** test-first execution plan for the accepted
> [`P47.3`](../../migration/plans/p47-task-explorer-remediation.md#p473-exact-thread-navigation-target)
> slice and G40 closure

**Goal:** Make Ctrl+T switch availability, engine application, and TUI
activation consume one exact current child identity instead of rebinding a
`ThreadID` to another Agent generation.

**Architecture:** The engine resolves one typed
`TaskExplorerNavigationTarget` from the exact execution row, current Agent
generation, and exactly one matching runtime-thread catalog row. Ctrl+T keeps
that complete value, revalidates all five fields against current snapshot and
catalog facts, then binds the existing bounded transcript pager directly to
the target generation. Generic ID-based navigation remains unchanged for its
existing callers.

**Tech Stack:** Go 1.26.5, `QueryEngine.TaskExplorerSnapshot`,
`RuntimeThreadCatalogSnapshot`, `AgentTranscriptPage`, Bubble Tea v2 commands,
standard Go tests, the race detector, and repository Makefile gates.

## Frozen Contract

- Execute only P47.3; do not add mixed rows, filter/focus, or detail tabs from
  P47.4-P47.7.
- Adoption is `combine`: preserve the generation-bound transcript pager and
  generic navigation contracts, while adding a project-owned exact Ctrl+T
  target.
- Target identity is the exact execution row's child `SessionID`, `ThreadID`,
  `AgentID`, `Generation`, plus catalog-declared `Mode`.
- Ctrl+T switch is available only for an exact current generation with a
  readable transcript and one matching `live_attach` catalog row.
  Predispatch, replay-only, evicted, superseded, missing, duplicate,
  revision-mismatched, or unsupported targets remain inspectable where current
  behavior permits, but cannot switch.
- Catalog revision must equal the runtime revision of the snapshot used for
  resolution. A race fails closed instead of retrying against newer state.
- Generation is copied from the exact execution row and current Agent fact;
  the catalog does not gain or infer a generation field.
- Engine declaration and action application use the same pure resolver logic.
  A switch result carries the typed target rather than a standalone thread
  string.
- Ctrl+T compares all target fields against fresh snapshot and catalog facts,
  never calls the ID-only or first/latest-generation selector, and produces a
  visible failure without changing the active view.
- Successful activation switches once and schedules at most one existing
  bounded `AgentTranscriptPageRequest` for the exact generation. Resolver
  failure schedules no transcript, model, tool, Agent, permission, or Git
  work.
- `activateThreadByID*`, Ctrl+B, `/team`, the Agent picker, persistence,
  replay, permissions, ACP, WorkBoard, and public wire schemas keep their
  existing observable behavior.
- Final code verification uses `make fmt`, `make lint`, `make test`, and
  `make build`; closeout also uses `make docs-check`, migration queue and
  manifest checks, and `git diff --check`.

## File Structure

| File | Responsibility in this change |
|---|---|
| `engine/task_explorer.go` | Own typed target/result, sentinel failures, exact resolver, declaration, and application. |
| `engine/p47_3_task_explorer_navigation_test.go` | Prove exact current generation, catalog/revision/mode failures, and dispatch-free application. |
| `internal/tui/task_explorer_panel.go` | Retain and correlate the complete typed switch target. |
| `internal/tui/thread_navigation.go` | Revalidate and activate the exact Ctrl+T target through the existing bounded pager. |
| `internal/tui/app.go` | Consume the typed target, surface failure, and return the one transcript command. |
| `internal/tui/task_explorer_panel_test.go` | Prove malformed or stale action results never become navigation intents. |
| `internal/tui/agent_transcript_page_test.go` | Prove exact success, rebound-catalog failure, and single generation-bound request. |
| Migration and architecture owners | Close G40, remove P47.3, promote one next eligible slice, and retain historical evidence. |

### Task 1: Reproduce ID-only rebinding and missing transcript dispatch

**Files:**

- Modify: `internal/tui/agent_transcript_page_test.go`
- Modify: `internal/tui/task_explorer_panel_test.go`

- [x] **Step 1: Add a same-thread, different-generation Ctrl+T fixture**

Give the panel a selected generation 1 and return the current action result
for that exact key. Before App consumption, expose generation 2 under the same
ThreadID in the current snapshot/catalog.

- [x] **Step 2: Assert the exact negative and success oracles**

Require the rebound case to preserve the prior active view, show a visible
failure, and issue zero transcript requests. In a fresh exact fixture, require
one request for the selected generation and no first/latest fallback.

- [x] **Step 3: Run the focused test and verify red**

```bash
go test ./internal/tui -run 'TestP473' -count=1
```

Expected: FAIL because the panel retains only `ThreadID`, App re-resolves by
ID, directly switches the view, and returns no transcript command.

- [x] **Step 4: Commit the red regression**

```bash
git add internal/tui/agent_transcript_page_test.go \
  internal/tui/task_explorer_panel_test.go
git commit -m "test: reproduce Task Explorer navigation rebinding"
```

### Task 2: Resolve and return one exact engine target

**Files:**

- Modify: `engine/task_explorer.go`
- Add: `engine/p47_3_task_explorer_navigation_test.go`

- [x] **Step 1: Add the typed target and typed failure boundary**

Add the accepted five-field target and exported unavailable/stale sentinel
errors. Add a target pointer to `TaskExplorerActionResult`; retain the legacy
SessionID/ThreadID fields for source compatibility during this slice.

- [x] **Step 2: Add one pure resolver used by every engine entrypoint**

Resolve the exact row, require the same current Agent generation and lineage,
require equal runtime/catalog revisions, and require exactly one matching
`live_attach` catalog entry with a readable transcript. The public resolver,
action declaration, and `ApplyTaskExplorerAction(Switch)` call the same helper.

- [x] **Step 3: Prove missing, duplicate, mismatch, and unsupported facts**

Cover superseded same-thread generation, missing row/catalog, duplicate
ThreadID, SessionID/AgentID/mode mismatch, replay-only, evicted, predispatch,
missing transcript, and revision race. Application must return
`navigation_stale` or `navigation_unavailable` without runner or other work.

- [x] **Step 4: Run engine focused and race tests**

```bash
go test ./engine -run 'TestP473|TestP314' -count=1
go test -race ./engine -run 'TestP473' -count=1
```

### Task 3: Retain the complete target through Ctrl+T result correlation

**Files:**

- Modify: `internal/tui/task_explorer_panel.go`
- Modify: `internal/tui/task_explorer_panel_test.go`

- [x] **Step 1: Replace the standalone switch string**

Store one copied `TaskExplorerNavigationTarget`. Accept it only when the
request/result identity matches, the result is a successful switch with no
conflict, and target Agent/generation matches the frozen intent.

- [x] **Step 2: Reject malformed and late targets visibly**

Missing target, empty fields, unsupported mode, wrong Session/thread/Agent/
generation, or a late request result must leave no consumable target and keep
an explanatory notice.

- [x] **Step 3: Run focused panel tests**

```bash
go test ./internal/tui -run 'TestP473.*Target|TestP314ExplorerActionsConfirmPayloadAndResultIdentity|TestP471' -count=1
```

### Task 4: Activate the exact target and schedule one bounded page

**Files:**

- Modify: `internal/tui/thread_navigation.go`
- Modify: `internal/tui/app.go`
- Modify: `internal/tui/agent_transcript_page_test.go`

- [x] **Step 1: Add a Ctrl+T-only exact activation helper**

Read fresh snapshot/catalog facts once, compare all five target fields and the
catalog/runtime revision, then build `agentTranscriptSelection` directly from
the target. Do not call `threadNavigationEntry`, `activateThreadByID*`, or
`agentTranscriptSelection`.

- [x] **Step 2: Share only the already-resolved pager activation**

Factor the existing switch/bind/begin tail so generic navigation keeps its
current resolver while both paths reuse the same bounded pager. Perform every
exact validation before changing the active view.

- [x] **Step 3: Return the command and surface post-result races**

On success, close Ctrl+T, return at most one page command, and retain the exact
generation in its request. On failure, keep the prior view, keep Ctrl+T open,
show a notification/notice, and return no command.

- [x] **Step 4: Run focused and race TUI tests**

```bash
go test ./internal/tui -run 'TestP473|TestP142c' -count=1
go test -race ./internal/tui -run 'TestP473|TestP142c' -count=1
```

### Task 5: Verify compatibility and close P47.3

**Files:**

- Modify only the architecture, migration, history, and plan indexes that own
  P47.3/G40 facts.

- [x] **Step 1: Run compatibility and package tests**

```bash
go test ./engine -count=1
go test ./internal/tui -count=1
go test -race ./internal/tui -run 'TestP473|TestP142c|TestCompactModeKeepsPanelCommandsReachable|TestTUILocalCommandsUseNormalizedRegistryNames' -count=1
```

Ctrl+B, `/team`, picker, replay, reducer, and render behavior must remain on
their pre-P47.3 paths.

- [x] **Step 2: Request one bounded independent review**

Review exact resolution, stale-result correlation, activation-before-mutation
ordering, command count, generic navigation compatibility, and queue closure.
Apply only source-backed findings and repeat affected tests.

- [x] **Step 3: Synchronize owners and promote one slice**

Remove G40, mark P47.3 complete, remove it from `queue.yaml`, promote only the
next eligible row, render `PLAN.md`, add one historical record, and mark this
plan historical/executed.

- [x] **Step 4: Run documentation and repository gates**

```bash
go run ./scripts/migration_queue check
make docs-check
go run ./scripts/migration_manifest.go check
git diff --check
make fmt
make lint
make test
make build
```

- [x] **Step 5: Inspect and commit only P47.3 scope**

Stage only P47.3 files; leave `PROJECT_GUIDE.md` and `artifacts/` untouched.
The local commit records the user problem, `combine` decision, compatibility,
rollback, and local evidence. Push, remote CI, and squash merge remain separate
protected-master integration gates and are not preclaimed by this historical
execution plan.
