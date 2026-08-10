# P47.1 Task Explorer Exact Pending Action Intent Implementation Plan

> **Historical use:** Retained as completed implementation evidence; do not
> execute this checklist as current work.

**Status:** historical
**Created:** 2026-08-07

> **Ownership:** test-first execution plan for the accepted
> [`P47.1`](../../migration/plans/p47-task-explorer-remediation.md#p471-exact-pending-action-intent)
> slice and G38 closure

**Goal:** Ensure Ctrl+T send, continue, and cancel confirmation submit the
exact execution and snapshot identity displayed when the pending action
started, even when refresh moves selection before submission.

**Architecture:** `TaskExplorerPanel` remains the sole owner of transient
presentation state. It captures one immutable action intent from the selected
execution and cached snapshot, collects payload separately, and submits the
frozen value through the existing engine action provider. The engine remains
the only action authority; no runtime, persistence, replay, or public API owner
moves.

**Tech Stack:** Go 1.26.5, Bubble Tea v2 key messages, existing
`TaskExplorerActionRequest`/`TaskExplorerActionResult` types, standard Go table
tests, and repository Makefile gates.

## Global Constraints

- Execute only P47.1 from current `origin/master`; do not implement P47.2 or
  later Task Explorer behavior.
- Adoption is `combine`: preserve P31 exact-generation/result-correlation
  behavior and add a project-owned immutable TUI intent.
- Test through `TaskExplorerPanel.Show`, `HandleKey`, `Refresh`, `Render`, and
  `SetActionProvider`; do not export a test-only API.
- Freeze request ID, BoardID/revision, correlation-only runtime revision,
  AgentID/generation, MessageID, action, and display label at action start.
- Payload remains separate mutable input and is copied only at submission.
- Refresh may replace rows and move selection, but cannot mutate or retarget
  the pending intent.
- The engine provider reauthorizes the frozen request from current truth. Do
  not infer availability from the post-refresh selection.
- Result handling compares against the submitted frozen intent and cannot
  clear or rewrite a newer pending intent.
- Rendering remains bounded and performs no provider or runtime I/O.
- No public API, durable format, event schema, ACP wire, Ctrl+B, `/team`,
  `/tasks`, WorkBoard, or `AgentRunner` behavior changes.
- Final code verification uses `make fmt`, `make lint`, `make test`, and
  `make build`; documentation closeout also uses `make docs-check`, the
  migration manifest check, and `git diff --check`.

---

## File Structure

| File | Responsibility in this change |
|---|---|
| `internal/tui/task_explorer_panel_test.go` | Prove pending send/continue/cancel cannot retarget across removal, reorder, revision change, or equal labels; retain exact result and render behavior. |
| `internal/tui/task_explorer_panel.go` | Own one immutable pending action intent, prompt mode, payload collection, exact submission, and result correlation. |
| `docs/architecture/tui/README.md` | Replace the current G38 warning with delivered behavior. |
| `docs/migration/REMAINING.md` | Remove closed gap G38 and retain G39-G41. |
| `docs/migration/STATUS.md` | Refresh current Task Explorer facts and dated repository counts. |
| `docs/migration/queue.yaml` and `PLAN.md` | Remove P47.1 and promote P47.2 as the sole next `Ready` slice. |
| `docs/migration/plans/p47-task-explorer-remediation.md` | Mark P47.1 complete and retain later scope. |
| `docs/migration/history/tui/p47-1-task-explorer-action-intent.md` and index | Record delivery, evidence, compatibility, and rollback. |
| `docs/superpowers/plans/README.md` | Index this plan and mark it executed at closeout. |

### Task 1: Freeze the regression at the public panel seam

**Files:**

- Modify: `internal/tui/task_explorer_panel_test.go`

**Interfaces:**

- Consumes: `NewTaskExplorerPanel`, `SetSnapshotProvider`,
  `SetActionProvider`, `Show`, `HandleKey`, `Refresh`, and `Render`.
- Produces: `TestP471PendingActionIntentCannotRetargetAcrossRefresh`; no
  production API.

- [x] **Step 1: Add one table-driven exact-intent regression**

Add the test after `TestP314ExplorerActionsConfirmPayloadAndResultIdentity`.
The action table is:

```go
actions := []struct {
	name    string
	action  engine.TaskExplorerAction
	start   tea.KeyPressMsg
	submit  tea.KeyPressMsg
	payload string
}{
	{
		name: "send", action: engine.TaskExplorerActionSend,
		start: tea.KeyPressMsg{Code: 's', Text: "s"},
		submit: tea.KeyPressMsg{Code: tea.KeyEnter}, payload: "send payload",
	},
	{
		name: "continue", action: engine.TaskExplorerActionContinue,
		start: tea.KeyPressMsg{Code: 'n', Text: "n"},
		submit: tea.KeyPressMsg{Code: tea.KeyEnter}, payload: "continue payload",
	},
	{
		name: "cancel", action: engine.TaskExplorerActionCancel,
		start: tea.KeyPressMsg{Code: 'c', Text: "c"},
		submit: tea.KeyPressMsg{Code: 'y', Text: "y"},
	},
}
```

For each action, run two refresh cases:

```go
refreshes := []struct {
	name    string
	updated func(engine.TaskExplorerExecution, engine.TaskExplorerExecution) []engine.TaskExplorerExecution
}{
	{
		name: "selected execution removed",
		updated: func(_, replacement engine.TaskExplorerExecution) []engine.TaskExplorerExecution {
			return []engine.TaskExplorerExecution{replacement}
		},
	},
	{
		name: "selected execution retained after reorder",
		updated: func(original, replacement engine.TaskExplorerExecution) []engine.TaskExplorerExecution {
			return []engine.TaskExplorerExecution{replacement, original}
		},
	},
}
```

Each subtest constructs `agent-a@g1` and `agent-b@g2` with the same `Name`,
selects A at board/runtime revisions `7/11`, starts the action, changes the
snapshot to revisions `8/12`, applies the refresh case, and submits. Record the
request through `SetActionProvider` and assert this literal oracle:

```go
if request.RequestID == "" || request.BoardID != "board" ||
	request.BoardRevision != 7 || request.RuntimeRevision != 11 ||
	request.AgentID != "agent-a" || request.Generation != 1 ||
	request.Action != actionCase.action || request.Payload != actionCase.payload {
	t.Fatalf("pending action retargeted: %+v", request)
}
```

Return a result that repeats request, board, Agent, generation, MessageID, and
action, but uses `RuntimeRevision: request.RuntimeRevision + 1`; this preserves
the existing correlation-only runtime-revision contract.

- [x] **Step 2: Run the regression and verify red**

Run:

```bash
go test ./internal/tui -run TestP471PendingActionIntentCannotRetargetAcrossRefresh -count=1
```

Expected: FAIL because removal submits `agent-b@g2` and both refresh variants
submit the refreshed revisions `8/12`.

- [x] **Step 3: Commit the red regression**

```bash
git add internal/tui/task_explorer_panel_test.go
git commit -m "test: reproduce Task Explorer action retargeting"
```

### Task 2: Capture and submit one immutable action intent

**Files:**

- Modify: `internal/tui/task_explorer_panel.go`
- Modify: `internal/tui/task_explorer_panel_test.go`

**Interfaces:**

- Produces: unexported `taskExplorerActionIntent`,
  `taskExplorerActionPrompt`, `captureActionIntent`, `clearActionIntent`, and
  `submitActionIntent`.
- Preserves: public panel methods and the engine action request/result API.

- [x] **Step 1: Add the intent and prompt types**

Place these next to `taskExplorerSelection`:

```go
type taskExplorerActionPrompt uint8

const (
	taskExplorerActionPromptNone taskExplorerActionPrompt = iota
	taskExplorerActionPromptInput
	taskExplorerActionPromptConfirm
)

type taskExplorerActionIntent struct {
	RequestID       string
	BoardID         string
	BoardRevision   uint64
	RuntimeRevision uint64
	AgentID         string
	Generation      int64
	MessageID       string
	Action          engine.TaskExplorerAction
	DisplayLabel    string
}
```

Replace `actionInput` and `confirm` with:

```go
actionIntent *taskExplorerActionIntent
actionPrompt taskExplorerActionPrompt
actionText   string
```

- [x] **Step 2: Capture exact identity once**

Add `captureActionIntent(action)` that calls `selectedExecution` and
`actionAllowed` once, generates `uuid.NewString()`, copies the cached board and
runtime revisions plus exact execution key, and sets:

```go
DisplayLabel: fmt.Sprintf(
	"%s@g%d",
	execution.Key.AgentID,
	execution.Key.Generation,
),
```

Add `clearActionIntent` to nil the pointer, reset prompt to
`taskExplorerActionPromptNone`, and clear payload. `Show` calls it; `Refresh`
does not.

- [x] **Step 3: Submit only from the captured value**

Add `submitActionIntent(intent, payload)` that builds this request without
reading current selection or current snapshot:

```go
request := engine.TaskExplorerActionRequest{
	RequestID:       intent.RequestID,
	BoardID:         intent.BoardID,
	BoardRevision:   intent.BoardRevision,
	RuntimeRevision: intent.RuntimeRevision,
	AgentID:         intent.AgentID,
	Generation:      intent.Generation,
	MessageID:       intent.MessageID,
	Action:          intent.Action,
	Payload:         payload,
}
```

Correlate the result against every frozen identity field except
`RuntimeRevision`, which remains correlation-only. Immediate actions keep
`submitAction(action, payload)`, but it now captures an intent and immediately
passes it to `submitActionIntent`.

- [x] **Step 4: Route input and confirmation through one pending value**

When `s`, `n`, or `c` starts, capture once and store the pointer with input or
confirmation mode. On Enter/y, copy the pointer locally, call
`clearActionIntent`, and then submit the local pointer. This ordering prevents
a synchronous provider result from clearing a newer pending intent.

Render these target-visible prompts:

```go
fmt.Sprintf("%s %s> %s", intent.Action, intent.DisplayLabel, p.actionText)
fmt.Sprintf("Confirm %s %s? y/N", intent.Action, intent.DisplayLabel)
```

Update `helpLine` to use `actionPrompt`.

- [x] **Step 5: Update the existing confirmation assertion**

Replace the private `panel.confirm` assertion with:

```go
frame := stripANSIForTest(panel.Render(80, 24))
if !strings.Contains(frame, "Confirm cancel child@g3? y/N") || len(requests) != 0 {
	t.Fatalf("cancel was not held for confirmation: %q %+v", frame, requests)
}
```

- [x] **Step 6: Run focused tests and verify green**

```bash
go test ./internal/tui -run 'TestP471PendingActionIntentCannotRetargetAcrossRefresh|TestP314ExplorerActionsConfirmPayloadAndResultIdentity' -count=1
```

Expected: PASS with original `agent-a@g1` and `7/11` identity retained.

- [x] **Step 7: Commit the implementation**

```bash
git add internal/tui/task_explorer_panel.go internal/tui/task_explorer_panel_test.go
git commit -m "fix: freeze Task Explorer pending action intent"
```

### Task 3: Prove newer-intent correlation and render purity

**Files:**

- Modify: `internal/tui/task_explorer_panel_test.go`

**Interfaces:**

- Consumes: the same public panel seam and providers.
- Produces: `TestP471ActionResultCannotClearNewerPendingIntent` and
  `TestP471PendingActionRenderIsPure`; no production API.

- [x] **Step 1: Add the newer-intent scenario**

Start send for A, refresh to B, then submit A. Inside the first provider call,
invoke `panel.HandleKey` with `n` to start continue for B before returning A's
exact result. Type and submit `next payload`, then assert two requests:

```go
want := []struct {
	agentID   string
	generation int64
	action    engine.TaskExplorerAction
	payload   string
}{
	{"agent-a", 1, engine.TaskExplorerActionSend, "first payload"},
	{"agent-b", 2, engine.TaskExplorerActionContinue, "next payload"},
}
```

Compare each recorded request to the corresponding literal row.

- [x] **Step 2: Add render-purity coverage**

Count snapshot and action provider calls. After `Show`, start send and render
twice. Assert snapshot calls remain one, action calls remain zero, and the
frame contains `agent-a@g1`.

- [x] **Step 3: Run focused and race validation**

```bash
go test ./internal/tui -run 'TestP471|TestP314ExplorerActionsConfirmPayloadAndResultIdentity' -count=1
go test -race ./internal/tui -run 'TestP471|TestP314ExplorerActionsConfirmPayloadAndResultIdentity' -count=1
```

Expected: PASS. No PTY pack is required because P47.1 changes neither terminal
bytes, geometry, input parsing, processes, nor terminal mode.

- [x] **Step 4: Commit compatibility evidence**

```bash
git add internal/tui/task_explorer_panel_test.go
git commit -m "test: cover Task Explorer intent correlation"
```

### Task 4: Close P47.1 and advance exactly one queue row

**Files:**

- Modify: `docs/architecture/tui/README.md`
- Modify: `docs/migration/REMAINING.md`
- Modify: `docs/migration/queue.yaml`
- Modify: `docs/migration/PLAN.md`
- Modify: `docs/migration/plans/p47-task-explorer-remediation.md`
- Create: `docs/migration/history/tui/p47-1-task-explorer-action-intent.md`
- Modify: `docs/migration/history/tui/README.md`
- Modify: `docs/superpowers/plans/README.md`
- Modify: this plan

**Interfaces:**

- Consumes: final source/test evidence and queue generator.
- Produces: current architecture truth, G38 closure, one history record, and
  P47.2 as the only `Ready` row.

- [x] **Step 1: Synchronize lifecycle owners**

Make these exact changes:

- architecture: replace G38 warning with frozen pending-intent behavior;
- `REMAINING.md`: remove G38 and retain G39-G41;
- `queue.yaml`: remove P47.1, set P47.2 `state: ready`, satisfy
  `p47-2-root-selected`, and leave P47.3 queued;
- P47 contract: mark P47.1 complete, correct the approved promotion narrative,
  and leave later behavior unimplemented;
- history: record outcome, compatibility, focused/race/repository evidence,
  residual physical-terminal/remote-CI boundaries, and rollback;
- plan indexes: link the new history/plan and mark this plan executed.

- [x] **Step 2: Regenerate and validate plan owners**

```bash
go run ./scripts/migration_queue render
make docs-check
git diff --check
```

Expected: P47.2 is the sole `Ready` row and every document is reachable.

- [x] **Step 3: Commit closeout facts**

```bash
git add docs/architecture/tui/README.md docs/migration/PLAN.md \
  docs/migration/REMAINING.md docs/migration/STATUS.md \
  docs/migration/queue.yaml \
  docs/migration/plans/p47-task-explorer-remediation.md \
  docs/migration/history/tui/p47-1-task-explorer-action-intent.md \
  docs/migration/history/tui/README.md docs/superpowers/plans/README.md \
  docs/superpowers/plans/2026-08-07-p47-1-task-explorer-action-intent.md
git commit -m "docs: close P47.1 action intent repair"
```

### Task 5: Independent review and repository closeout

**Files:**

- Review: every file changed by Tasks 1-4

**Interfaces:**

- Consumes: final branch diff against `origin/master`.
- Produces: reviewed, gate-complete, merge-ready P47.1 branch.

- [x] **Step 1: Request one bounded read-only Terra review**

Verify exact capture, stale refresh, result correlation, render purity, scope,
queue lifecycle, and test sensitivity. Apply only source-backed findings.

- [x] **Step 2: Re-run focused tests after the last edit**

```bash
go test ./internal/tui -run 'TestP471|TestP314ExplorerActionsConfirmPayloadAndResultIdentity' -count=1
go test -race ./internal/tui -run 'TestP471|TestP314ExplorerActionsConfirmPayloadAndResultIdentity' -count=1
```

- [x] **Step 3: Run every final gate**

```bash
make fmt
make lint
make test
make build
make docs-check
go run ./scripts/migration_manifest.go check
git diff --check
```

Expected: every local gate passes. Remote CI absence caused by exhausted usage
is reported separately.

- [x] **Step 4: Inspect and stage only P47.1 scope**

```bash
git status --short
git diff --stat origin/master...HEAD
git diff origin/master...HEAD -- internal/tui docs
```

Confirm no unrelated path is present. Commit any accepted review repair as
`fix: address P47.1 review findings`.

- [x] **Step 5: Push, open the PR, and squash merge**

Push `codex/fix/task-explorer-action-intent`. The PR description states the
G38 user problem, `combine` decision, scope, compatibility, rollback,
focused/race/repository evidence, and remote CI limitation. Merge only through
the protected `master` branch.
