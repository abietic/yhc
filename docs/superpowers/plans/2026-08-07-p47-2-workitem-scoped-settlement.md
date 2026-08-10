# P47.2 WorkItem-Scoped Settlement Implementation Plan

> **Historical use:** Retained as completed implementation evidence; do not
> execute this checklist as current work.

**Status:** historical
**Created:** 2026-08-07
**Completed:** 2026-08-07

> **Ownership:** test-first execution plan for the accepted
> [`P47.2`](../../migration/plans/p47-task-explorer-remediation.md#p472-workitem-scoped-settlement)
> slice and G39 closure

**Goal:** Allow a WorkItem terminal transition when every execution linked to
that exact item is settled, regardless of live or missing executions linked to
other items on the same board.

**Architecture:** `LogicalWorkAdapter` remains the durable WorkBoard mutation
owner. Under its existing mutex, the terminal guard derives the set of
WorkItems that this mutation moves from non-terminal to terminal, filters the
current board's immutable execution links to those exact IDs, and passes only
their `(AgentID, Generation)` keys to the existing engine-owned settlement
snapshot. TUI, ACP, runner, persistence schema, and commit ordering do not
change.

**Tech Stack:** Go 1.26.5, existing `LogicalWorkAdapter`, `TaskManager`,
`ExecutionLink`, and `ExecutionSettlement` contracts, standard Go tests, race
detector, and repository Makefile gates.

## Global Constraints

- Execute only P47.2; do not implement P47.3 navigation or P47.4-P47.7
  presentation behavior.
- Adoption is `combine`: preserve the existing exact-generation runner
  settlement oracle and enforce the project-owned WorkItem identity boundary.
- Test through `TaskManager.Update`/`ReplaceTodos` and the bound
  `LogicalWorkAdapter`; do not export a test-only API.
- A mutation that terminalizes several WorkItems validates the union of links
  belonging to all of those target items, never an arbitrary first item.
- A live, reserved, cancel-pending, unresolved, missing, duplicate, or
  otherwise unsettled execution blocks only its own target WorkItem.
- Preserve expected Board revision, WorkItem revision, authority-v3 encoding,
  marker-last durable commit, projection reservation, and failure quarantine.
- Preserve the existing error strings and fail-closed behavior for missing or
  failing settlement snapshots and malformed settlement results.
- Do not change public APIs, persisted schemas, replay, permission, TUI, ACP,
  Ctrl+B, `/team`, `/tasks`, or Task Explorer action semantics.
- Final code verification uses `make fmt`, `make lint`, `make test`, and
  `make build`; closeout also uses `make docs-check`, migration queue and
  manifest checks, and `git diff --check`.

---

## File Structure

| File | Responsibility in this change |
|---|---|
| `engine/internal/workboard/execution_links_test.go` | Prove exact WorkItem scoping, inverse fail-closed behavior, multiple generations, missing facts, and durable commit boundaries. |
| `engine/internal/workboard/adapter.go` | Select only links owned by WorkItems terminalized by the current mutation before invoking the existing settlement oracle. |
| `docs/architecture/runtime/tasks-and-agents.md` | Describe delivered engine-owned settlement without changing TUI ownership. |
| `docs/migration/REMAINING.md` | Remove closed gap G39. |
| `docs/migration/STATUS.md` | Record current verified settlement behavior. |
| `docs/migration/queue.yaml` and `PLAN.md` | Remove P47.2 and promote exactly one next eligible slice. |
| `docs/migration/plans/p47-task-explorer-remediation.md` | Mark P47.2 complete while retaining later contracts. |
| `docs/migration/history/tui/p47-2-workitem-scoped-settlement.md` and index | Record outcome, evidence, compatibility, and rollback. |
| `docs/superpowers/plans/README.md` | Index this plan and mark it executed at closeout. |

### Task 1: Reproduce cross-WorkItem settlement leakage

**Files:**

- Modify: `engine/internal/workboard/execution_links_test.go`

**Interfaces:**

- Consumes: `BindLogicalWorkAdapter`,
  `LogicalWorkAdapter.AdmitExecutionLink`, `TaskManager.Update`, and
  `AdapterConfig.SettlementSnapshot`.
- Produces: `TestP472TerminalSettlementUsesExactWorkItemLinks`; no production
  API.

- [x] **Step 1: Add a two-item adapter fixture**

Create two compatibility Tasks before binding the adapter, force the existing
first mutation cutover, then admit one exact execution link for each item. The
fixture returns the manager, adapter, both Tasks, and their WorkItems. Every
admission reads the current Board revision and target WorkItem revision rather
than reusing a stale value:

```go
func p472AdmitLink(
	t *testing.T,
	adapter *LogicalWorkAdapter,
	item WorkItem,
	agentID string,
	generation uint64,
) {
	t.Helper()
	err := adapter.AdmitExecutionLink(AdmitExecutionLinkRequest{
		BoardID: adapter.record.BoardID,
		BoardRevision: adapter.record.Board.Revision,
		WorkItemID: item.ID,
		WorkItemRevision: item.Revision,
		AgentID: agentID,
		Generation: generation,
		ParentSessionID: "session",
		ParentThreadID: "thread",
		ParentAgentID: "parent",
		ParentToolUseID: "tool-" + agentID,
		AdmittedAt: time.Date(2026, 8, 7, 2, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("admit %s@g%d: %v", agentID, generation, err)
	}
}
```

- [x] **Step 2: Add the exact and inverse oracles**

For A-settled/B-live, make the callback fail the test unless it receives only
`[{AgentID: "agent-a", Generation: 1}]`, return A as settled, and require
`manager.Update(A, completed)` to succeed. In a fresh fixture, return A as
`Live: true` while B is settled and require the same update to fail.

- [x] **Step 3: Run the exact-item regression and verify red**

```bash
go test ./engine/internal/workboard -run TestP472TerminalSettlementUsesExactWorkItemLinks -count=1
```

Expected: FAIL because the current guard passes both A and B keys to the
settlement callback and blocks A on B.

- [x] **Step 4: Commit the red regression**

```bash
git add engine/internal/workboard/execution_links_test.go
git commit -m "test: reproduce cross-item settlement leakage"
```

### Task 2: Scope the guard to terminalized WorkItems

**Files:**

- Modify: `engine/internal/workboard/adapter.go`
- Modify: `engine/internal/workboard/execution_links_test.go`

**Interfaces:**

- Preserves: `LogicalWorkAdapter.guardTerminalLinksLocked(current, next)` and
  `AdapterConfig.SettlementSnapshot` signatures.
- Produces: an exact target-item filter before the existing settlement
  validation loop.

- [x] **Step 1: Select terminalized WorkItem identities**

Replace the board-wide `linked`/`terminalized` shortcut with a set containing
every ID that changes from non-terminal in `current` to terminal in `next`:

```go
terminalized := make(map[string]struct{})
for _, item := range next.Board.Items {
	prior, ok := before[item.ID]
	if ok && !isTerminalStatus(prior.Status) &&
		isTerminalStatus(item.Status) {
		terminalized[item.ID] = struct{}{}
	}
}
```

- [x] **Step 2: Build only exact current-board keys**

Iterate `current.ExecutionLinks` in durable order. Append a key only when
`link.BoardID == current.BoardID` and `link.WorkItemID` belongs to the
terminalized set. Return early when no matching links exist. Keep the existing
nil callback, callback error, duplicate, missing, and unsettled checks
unchanged.

- [x] **Step 3: Run the focused test and verify green**

```bash
go test ./engine/internal/workboard -run 'TestP472|TestAdapterExecutionLinkAdmissionAndTerminalSettlementGuard' -count=1
```

Expected: PASS; A queries only A's key, while live A still fails closed.

- [x] **Step 4: Commit the minimal implementation**

```bash
git add engine/internal/workboard/adapter.go engine/internal/workboard/execution_links_test.go
git commit -m "fix: scope WorkItem settlement links"
```

### Task 3: Prove generations, missing facts, and durable boundaries

**Files:**

- Modify: `engine/internal/workboard/execution_links_test.go`

**Interfaces:**

- Consumes: the same adapter seam and `Store.Inspect`.
- Produces: `TestP472TerminalSettlementPreservesFailClosedAndCommitSemantics`;
  no production API.

- [x] **Step 1: Add multiple-generation and missing-fact cases**

Use table cases where A has generations 1 and 2 and B has generation 1:

- both A generations settled and B live: A succeeds, and callback keys contain
  exactly A generations in durable link order;
- A generation 2 is live or cancel-pending: A fails;
- one A settlement is omitted: A fails;
- only B's fact is omitted: settled A succeeds because B is not queried.

- [x] **Step 2: Assert no partial commit on rejection**

Capture the pre-update Board revision, invoke the rejected A transition, and
inspect the authority store. Require the persisted revision and A status to
remain unchanged. For the successful case, require one Board revision advance,
A terminal status, and unchanged execution links after reloading the store.

- [x] **Step 3: Run focused and race validation**

```bash
go test ./engine/internal/workboard -run 'TestP472|TestAdapterExecutionLinkAdmissionAndTerminalSettlementGuard' -count=1
go test -race ./engine/internal/workboard -run 'TestP472|TestAdapterExecutionLinkAdmissionAndTerminalSettlementGuard' -count=1
```

Expected: PASS. No PTY, renderer-golden, ACP, or replay pack is required
because P47.2 changes no terminal bytes, presentation state, wire schema, or
replay path.

- [x] **Step 4: Commit the negative and durability evidence**

```bash
git add engine/internal/workboard/execution_links_test.go
git commit -m "test: harden WorkItem settlement boundaries"
```

### Task 4: Close P47.2 and promote one next slice

**Files:**

- Modify: the documentation owners listed in the File Structure table.

**Interfaces:**

- Consumes: current source, focused/race results, queue dependency and promotion
  rules, and repository documentation policy.
- Produces: one historical P47.2 record, G39 closure, and a queue with at most
  one `Ready` row.

- [x] **Step 1: Synchronize only changed fact owners**

Mark P47.2 complete, remove G39, update the current TUI architecture statement,
remove P47.2 from `queue.yaml`, and promote only a row whose dependency and
named gate are satisfied. Render the generated PLAN block. Mark this execution
plan historical and add its index entry.

- [x] **Step 2: Run focused documentation checks**

```bash
go run ./scripts/migration_queue render
go run ./scripts/migration_queue check
make docs-check
go run ./scripts/migration_manifest.go check
git diff --check
```

Expected: PASS with no queue drift, broken links, unowned closed gap, manifest
drift, or whitespace error.

- [x] **Step 3: Run the four final code gates**

```bash
make fmt
make lint
make test
make build
```

Expected: all four pass on the final caller worktree after the last edit.

- [x] **Step 4: Inspect and commit only P47.2 scope**

```bash
git status --short
git diff --check
git add engine/internal/workboard/adapter.go \
  engine/internal/workboard/execution_links_test.go \
  docs/architecture/runtime/tasks-and-agents.md \
  docs/migration/PLAN.md docs/migration/REMAINING.md \
  docs/migration/STATUS.md docs/migration/queue.yaml \
  docs/migration/plans/p47-task-explorer-remediation.md \
  docs/migration/history/tui/p47-2-workitem-scoped-settlement.md \
  docs/migration/history/tui/README.md \
  docs/superpowers/plans/2026-08-07-p47-2-workitem-scoped-settlement.md \
  docs/superpowers/plans/README.md
git commit -m "fix: close P47.2 WorkItem settlement"
```

Confirm `PROJECT_GUIDE.md` and `artifacts/` remain unstaged.
