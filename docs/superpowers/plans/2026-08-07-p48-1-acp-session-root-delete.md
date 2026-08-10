# ACP Observed Session-Root Delete Implementation Plan

> **Historical use:** Retained as completed implementation evidence; do not
> execute this checklist as current work.

**Status:** historical
**Completed:** 2026-08-07

> **Ownership:** test-first implementation and closeout steps for P48.1/G42;
> root migration queue state remains authoritative.

**Goal:** Make ACP inactive Session deletion target the exact canonical project
root previously observed for that Session ID, while preserving safe
default-root idempotence for unknown IDs.

**Architecture:** `Agent` receives one synchronized, process-local locator. New,
load, resume, fork, and list record successful observations; close deliberately
retains them. Delete resolves the locator while `sessionLifecycleMu` owns the
lifecycle transition, delegates mutation to `engine/session`, and clears only
the exact observation after success. Conflicting roots fail closed without a
filesystem mutation.

**Tech Stack:** Go 1.26.5, ACP Go SDK v1, `engine/session`, white-box ACP
lifecycle tests, race detector, SDK wire verification, migration queue, and
Makefile gates.

## Global Constraints

- Execute only after root `docs/migration/queue.yaml` promotes P48.1 to
  `Ready`; written contract approval alone is not execution authority.
- Close only G42. Do not change ACP delete request shape, add a home-directory
  scan, or create a durable/global Session catalog.
- Use the engine's effective CWD after construction or restore; never trust a
  stale request CWD when a successful engine already owns the canonical root.
- Same Session ID plus two different canonical roots is permanently ambiguous
  for that agent process. Do not use last-writer-wins.
- `sessionLifecycleMu` remains the lifecycle lock and `engine/session` remains
  the only filesystem deletion owner.
- Preserve active-Session rejection, unsafe-ID containment, non-`ErrNotExist`
  failures, and default-CWD idempotence for IDs that were never observed.
- Preserve unrelated user changes in the caller checkout; work from a clean
  short-lived branch based on current `origin/master`.

---

## Task 1: Prove cross-root and ambiguous delete behavior

**Files:**

- Modify: `server/acp/agent_session_test.go`

**Interfaces:**

- Exercises: `Agent.NewSession`, `Agent.CloseSession`, `Agent.ListSessions`,
  and `Agent.UnstableDeleteSession` through their public ACP request types.
- Reuses: `transcript.NewRecorder`,
  `writeACPProjectGraphRootMetadata`, and existing delete fixtures.

- [x] **Step 1: Add the cross-CWD new/close/delete regression**

Create `TestACPDeleteSessionUsesObservedCrossCWDSessionRoot`. Configure the
agent with project A, call `NewSession` with project B, close the returned ID,
and delete it. Place an unrelated sentinel under A. Assert B's transcript and
owned sidecars are gone, the A sentinel is unchanged, and the delete response
is successful.

- [x] **Step 2: Add cold locator reconstruction through list**

Create `TestACPDeleteSessionRebuildsRootFromList`. Persist a valid Session in
project B, construct a fresh agent whose default is A, list with `Cwd: &B`, and
delete the listed ID. Assert B is deleted and A is untouched.

- [x] **Step 3: Add the duplicate-ID conflict oracle**

Create `TestACPDeleteSessionRejectsAmbiguousObservedRoot`. Persist the same safe
Session ID under projects B and C, list both roots through one agent, then
delete. Require a `*acpsdk.RequestError` with `CodeSessionConflict` and prove
both transcript trees are byte-for-byte unchanged.

- [x] **Step 4: Run the focused red tests**

```bash
go test ./server/acp/ -run '^TestACPDeleteSession(UsesObservedCrossCWDSessionRoot|RebuildsRootFromList|RejectsAmbiguousObservedRoot)$' -count=1
```

Expected: FAIL. The first two requests return success while project B remains;
the duplicate observation also false-succeeds against the default root.

## Task 2: Add one canonical process-local locator

**Files:**

- Create: `server/acp/session_roots.go`
- Create: `server/acp/session_roots_test.go`
- Modify: `server/acp/agent.go`
- Modify: `server/acp/streaming.go`
- Modify: `server/acp/agent_session_test.go`

**Interfaces:**

- Adds unexported `acpSessionRootLocator` to `Agent`.
- Reuses the existing `canonicalACPSessionDirectory` rule as the sole
  canonicalization owner.
- Produces a canonical CWD or an explicit ambiguous result; it never performs
  filesystem deletion itself.

- [x] **Step 1: Pin locator state transitions with unit tests**

Cover exact repeat observation, symlink/clean-path equivalence, different-root
ambiguity, concurrent remember/resolve, unknown-ID fallback, close-style
retention, and exact-entry forget. Keep path fixtures inside `t.TempDir()`.

- [x] **Step 2: Implement the minimal synchronized value**

Use an unexported value with one lock and one map:

```go
type acpSessionRootState struct {
	root      string
	ambiguous bool
}

type acpSessionRootLocator struct {
	mu    sync.Mutex
	roots map[acpsdk.SessionId]acpSessionRootState
}

func newACPSessionRootLocator() *acpSessionRootLocator
func (l *acpSessionRootLocator) remember(id acpsdk.SessionId, cwd string)
func (l *acpSessionRootLocator) resolve(id acpsdk.SessionId, fallback string) (root string, observed bool, ambiguous bool)
func (l *acpSessionRootLocator) forget(id acpsdk.SessionId, expectedRoot string)
```

Canonicalize both observations and fallback. `remember` may move exact to
ambiguous but never ambiguous back to exact. `forget` deletes only a
non-ambiguous entry matching `expectedRoot`; it never clears conflict evidence.

- [x] **Step 3: Initialize and record only successful lifecycle facts**

Initialize the locator in `NewAgent`. Add a nil-safe lazy accessor only if
existing white-box `Agent{}` tests require it; do not add package-global state.
Record:

- `NewSession`: `eng.GetCWD()` after command publication succeeds;
- `ResumeSession` and `LoadSession`: the restored engine CWD after commit and
  registry insertion;
- `UnstableForkSession`: the child engine CWD after command publication
  succeeds; and
- `ListSessions`: every row actually returned, after empty-CWD fallback.

Do not clear the locator in `CloseSession`, `Agent.Close`, or failed lifecycle
rollback.

- [x] **Step 4: Resolve before durable delete and clear after success**

While holding the existing lifecycle and active-registry locks, resolve the
Session root. Map `ambiguous` to a typed `CodeSessionConflict` error containing
only the Session ID. Pass `filepath.Join(root, ".eino-agent", "transcripts")`
to `session.DeleteSession`. On success or `os.ErrNotExist`, forget the exact
observation used. Preserve every other error.

- [x] **Step 5: Run focused green and race tests**

```bash
go test ./server/acp/ -run '^(TestACPDeleteSession|TestACP_DeleteSession|TestACPSessionRootLocator)' -count=1
go test -race ./server/acp/ -run '^(TestACPDeleteSession|TestACP_DeleteSession|TestACPSessionRootLocator)' -count=1
```

- [x] **Step 6: Commit the green behavior**

```bash
git add server/acp/agent.go server/acp/streaming.go server/acp/session_roots.go server/acp/session_roots_test.go server/acp/agent_session_test.go
git commit -m "fix(acp): delete sessions from observed roots"
```

## Task 3: Close G42 without overstating evidence

**Files:**

- Modify: `docs/architecture/platform/acp-adapter.md`
- Modify if its facts change: `docs/architecture/state/sessions.md`
- Create: `docs/migration/verification/p48-1-acp-session-root-delete.md`
- Modify: `docs/migration/verification/README.md`
- Create: `docs/migration/history/runtime/p48-1-acp-session-root-delete.md`
- Modify: `docs/migration/history/README.md`
- Modify: `docs/migration/REMAINING.md`
- Modify: `docs/migration/queue.yaml`
- Modify generated: `docs/migration/PLAN.md`
- Modify: `docs/migration/plans/p48-acp-boundary-remediation.md`

- [x] **Step 1: Synchronize only delivered fact owners**

Describe process-local correlation, ambiguity, default fallback, and the
cold-start list/load/resume boundary. Remove G42 and P48.1, render the queue,
and explicitly leave P48.2 queued unless root governance promotes it. Do not
claim durable global lookup or cold cross-project delete without observation.

- [x] **Step 2: Run ACP and repository closeout gates**

```bash
go test ./server/acp/ -run '^(TestACPDeleteSession|TestACP_DeleteSession|TestACPSessionRootLocator)' -count=1
go test -race ./server/acp/ -run '^(TestACPDeleteSession|TestACP_DeleteSession|TestACPSessionRootLocator)' -count=1
make test-contract
make test-race
./scripts/verify-p23-5-acp-sdk.sh
make fmt
make lint
make test
make build
make docs-check
go run ./scripts/migration_manifest.go check
git diff --check
```

- [x] **Step 3: Commit closeout and open one atomic PR**

```bash
git add docs/architecture docs/migration
git commit -m "docs: close P48.1 ACP session-root delete"
```

The PR must state `project-native`, default-root compatibility, ambiguity and
rollback behavior, local gate results, and remote-CI state. Squash-merge only
after repository policy permits it.
