# ProjectGraph Post-Rebuild Revision Fence Implementation Plan

> **Historical use:** Retained as completed implementation evidence; do not
> execute this checklist as current work.

**Status:** historical
**Completed:** 2026-08-07
**Created:** 2026-08-07
**Source snapshot:** `origin/master` at
`de74294b29f40d19bfd0e37f09889bd6f8037d90`

> **Ownership:** test-first delivery plan for P50.1, the first repair accepted
> by the [Permission Runtime Remediation Design](../specs/2026-08-07-permission-remediation-design.md)

**Goal:** Prevent an external permission-policy mutation in the narrow window
between ProjectGraph's live-revision check and action rebuild from being
accepted as an expected batch revision advance.

**Architecture:** `projectGraphHITLExecution` remains the sole batch-chain
owner. It continues to serialize ordinary decisions with its existing mutex,
but now fences the rebuilt action against `currentPolicyRevision` before any
settlement or persistence. External policy writers remain independent and do
not acquire the batch lock.

**Tech Stack:** Go 1.26.5, QueryEngine ProjectGraph HITL, exact permission-rule
persistence, white-box deterministic barriers, Go race detector, migration
governance, and Makefile gates.

## Global Constraints

- Execute only after root `docs/migration/queue.yaml` admits P50.1 as its sole
  `Ready` slice. The accepted design and this plan do not self-promote.
- Start from then-current `origin/master` on one short-lived `codex/fix-*`
  branch; do not stack the repair on this documentation branch.
- Keep `QueryEngine` and `projectGraphHITLExecution` as the only permission and
  batch-chain owners. Do not add a global permission lock.
- Do not make `PersistPermissionRule`, configuration reload, ACP, or another
  policy writer acquire the ProjectGraph batch mutex.
- Preserve Plan approval's separate revision contract and all non-ProjectGraph
  permission paths.
- Use channels/barriers for interleavings. A sleep may enforce a deadline, but
  it is not proof that a race window was reached.
- Every denied interleaving must assert no new rule, no duplicate rule, and no
  tool dispatch.
- Preserve unrelated caller changes and stage only P50.1 files.

---

## Task 1: Reproduce the post-check/pre-rebuild race deterministically

**Files:**

- Modify: `engine/graph_hitl.go`
- Modify: `engine/graph_hitl_test.go`

**Interfaces:**

- Exercises: `resolveProjectGraphHITLPermission`,
  `projectGraphHITLExecution.currentPolicyRevision`, and
  `QueryEngine.PersistPermissionRule`.
- Adds one unexported test-only interleaving hook to the existing execution
  value; production construction leaves it nil.

- [x] **Step 1: Add the exact interleaving seam without changing behavior**

Add this field to `projectGraphHITLExecution`:

```go
afterLivePolicyCheckForTest func()
```

Invoke a local copy after the pre-check at `graph_hitl.go:888-899` succeeds and
before `buildPermissionActionDescriptor` runs:

```go
if hook := execution.afterLivePolicyCheckForTest; hook != nil {
    hook()
}
```

The hook runs while `execution.mu` is held. It must never receive the engine,
request, action, or policy content as arguments.

- [x] **Step 2: Add the failing external-mutation regression**

Create
`TestP501ProjectGraphRejectsPolicyMutationBetweenCheckAndRebuild`. Build two
exact `Read` decisions with `newProjectGraphHITLExecutionPermissionTest`, then
install a hook that persists a third exact rule once:

```go
execution := projectGraphHITLExecutionFromContext(ctx)
var once sync.Once
execution.afterLivePolicyCheckForTest = func() {
    once.Do(func() {
        if err := query.PersistPermissionRule("Read", outside.Input); err != nil {
            t.Fatal(err)
        }
    })
}
```

Require the first resumed decision to return
`project graph permission intent expired`, require the second decision to be
rejected from the now-invalid batch, and assert that only the externally
persisted rule exists.

- [x] **Step 3: Run the focused red test**

```bash
go test ./engine/ -run '^TestP501ProjectGraphRejectsPolicyMutationBetweenCheckAndRebuild$' -count=1
```

Expected: FAIL because the rebuilt action captures the external revision and
the current code treats it as the expected post-settlement revision.

## Task 2: Fence the rebuilt action before settlement

**Files:**

- Modify: `engine/graph_hitl.go`
- Modify: `engine/graph_hitl_test.go`

- [x] **Step 1: Add the minimal post-rebuild equality check**

Immediately after a successful rebuild and before
`settlePermissionInteraction`, implement:

```go
if initialAction.PolicySnapshotID != execution.currentPolicyRevision {
    execution.invalid = true
    result = PermissionInteractionResult{
        Decision: PermissionDeny,
        Message:  "project graph permission intent expired",
    }
} else {
    result = e.settlePermissionInteraction(
        initialAction,
        request.ToolContext,
        result,
    )
}
```

Keep the existing `postRevision` logic after this branch. Do not update
`currentPolicyRevision` from a mismatched rebuild.

- [x] **Step 2: Make the red test green and pin invalidation**

After the first rejection, inspect the execution under its mutex and require:

```go
if !execution.invalid {
    t.Fatal("externally advanced batch remained valid")
}
```

Call the second decision twice, including once after the first terminal result,
and require identical denial with no additional persistence.

- [x] **Step 3: Run focused green and race tests**

```bash
go test ./engine/ -run '^(TestP501|TestProjectGraphHITLExecution)' -count=1
go test -race ./engine/ -run '^(TestP501|TestProjectGraphHITLExecution)' -count=1
```

## Task 3: Prove the remaining settlement-chain boundaries

**Files:**

- Modify: `engine/graph_hitl_test.go`

- [x] **Step 1: Cover lock contention without sleeps**

Create `TestP501ProjectGraphConcurrentDecisionsShareOneRevisionChain`. Hold the
first decision at `afterLivePolicyCheckForTest`, start a second decision, and
prove with channels that the second cannot reach its hook until the first
releases the batch mutex. Run both the successful-chain and external-drift
variants. Require either two exact rules once each or zero batch-owned rules;
never accept a partial unexplained chain.

- [x] **Step 2: Cover persistence failure and late settlement**

Use a test-owned project root whose `.claude` path is a regular file so exact
rule persistence fails deterministically. Require the first decision to deny,
the active revision not to be advanced as a successful settlement, and a late
or repeated decision to dispatch nothing. Keep the production persistence API
unchanged; the filesystem fixture is the independent failure oracle for this
slice.

- [x] **Step 3: Cover cancellation at the public resume boundary**

Add table cases that cancel `SubmitRuntimeItem` before the first settlement and
after one successful settlement but before the next runtime item is admitted.
Assert terminal reason, exact rule set, batch invalidation or disposal, and zero
duplicate dispatch. Do not add cancellation checks inside a function that the
canonical kernel no longer calls after cancellation.

- [x] **Step 4: Stress the original two-Read regression**

```bash
go test ./engine/ -run '^(TestP501|TestProjectGraphHITLExecution)' -count=100 -timeout=180s
go test -race ./engine/ -run '^(TestP501|TestProjectGraphHITLExecution)' -count=20 -timeout=180s
go test ./server/acp/ -run '^TestACP_ProjectGraphSerializesDistinctExactAllowAlwaysDecisions$' -count=100
go test -race ./server/acp/ -run '^TestACP_ProjectGraphSerializesDistinctExactAllowAlwaysDecisions$' -count=20
```

## Task 4: Close P50.1 without changing sandbox or reviewer behavior

**Files:**

- Modify: `docs/migration/plans/p37-concurrent-exact-permission-settlement.md`
- Create: `docs/migration/verification/p50-1-project-graph-revision-fence.md`
- Modify: `docs/migration/verification/README.md`
- Create: `docs/migration/history/runtime/p50-1-project-graph-revision-fence.md`
- Modify: `docs/migration/history/README.md`
- Modify: `docs/migration/queue.yaml`
- Modify generated: `docs/migration/PLAN.md`
- Modify: `docs/migration/REMAINING.md`

- [x] **Step 1: Synchronize only delivered facts**

Record the post-rebuild fence, deterministic interleavings, exact rollback, and
local evidence. Do not claim that all external policy writers are serialized,
that P37 owns Plan approval, or that a sandbox exists. Remove only P50.1 from
the queue, then render the next state according to the one-Ready rule.

- [x] **Step 2: Run final repository gates**

```bash
make fmt
make lint
make test
make build
make docs-check
go run ./scripts/migration_manifest.go check
git diff --check
```

- [x] **Step 3: Commit and open one atomic repair PR**

```bash
git add engine/graph_hitl.go engine/graph_hitl_test.go docs/migration
git commit -m "fix(permission): fence ProjectGraph rebuilt revisions"
```

The PR must state `project-native`, the race window, unchanged external-writer
ownership, rollback, focused race evidence, full local gates, and remote-CI
state.
