# Remove Private ACP Session Migration Implementation Plan

> **Historical use:** Retained as completed implementation evidence; do not
> execute this checklist as current work.

**Status:** historical
**Completed:** 2026-08-07

> **Ownership:** test-first implementation and closeout steps for P48.5/G46;
> root migration queue state remains authoritative.

**Goal:** Remove the unsafe private ACP `_session/export` and `_session/import`
surface so both methods fail as unknown extensions without side effects.

**Architecture:** The ACP extension dispatcher retains negotiated Goal methods
and `_session/status`, but removes both private migration cases and every
exclusive token, checksum, handler, error, and test hook. Public ACP Session
restore/fork/delete and engine sanitized presentation export remain owned by
their existing APIs. A real dispatcher fixture proves MethodNotFound and zero
mutation.

**Tech Stack:** Go 1.26.5, ACP Go SDK v1 extension dispatcher, QueryEngine
Session lifecycle, white-box/wire tests, source scans, SDK verification,
migration queue, and Makefile gates.

## Global Constraints

- Execute only when P48.5 is `Ready` and P48.4 has completed.
- Close only G46. Do not remove public load, resume, fork, delete, Goal,
  `_session/status`, engine/session presentation export, or shared Session
  conflict codes used by retained operations.
- Both removed method names must take the ordinary MethodNotFound path. Do not
  retain aliases, feature flags, deprecation stubs, or token readers.
- Historical migration documents remain immutable evidence. Update only
  current architecture, active plans, verification, and history indexes.
- Prove zero engine construction, Session registration, and filesystem
  mutation for the removed dispatcher calls.
- Remove imports and test helpers only after source scans prove they are
  exclusive to the rejected surface.

---

## Task 1: Freeze the removal contract at the dispatcher seam

**Files:**

- Modify: `server/acp/agent_protocol_test.go`

**Interfaces:**

- Exercises: `Agent.HandleExtensionMethod` with the two literal private method
  names.
- Observes: returned `*acpsdk.RequestError`, `Agent.sessions`, and a temp
  project tree. A RED-only engine-construction sentinel is removed with the
  rejected production seam.

- [x] **Step 1: Add one table-driven MethodNotFound oracle**

Create `TestExtensionHandlerRemovedSessionMigrationMethodsReturnMethodNotFound`
for `_session/export` and `_session/import`. For each method, call the real
dispatcher with syntactically valid-looking params, assert SDK MethodNotFound,
and prove:

- the RED-only `createImportedEngineFn` sentinel exposes any recognized import
  path before the fix, then is deleted with that path;
- the active Session map is unchanged; and
- a sentinel project tree has no new or modified files.

- [x] **Step 2: Run focused red**

```bash
go test ./server/acp/ -run '^TestExtensionHandlerRemovedSessionMigrationMethodsReturnMethodNotFound$' -count=1
```

Expected: FAIL because the dispatcher currently recognizes both methods; use a
valid active export fixture only where needed to distinguish recognition from
MethodNotFound.

## Task 2: Delete only the rejected private surface

**Files:**

- Modify: `server/acp/streaming.go`
- Modify: `server/acp/agent.go`
- Modify: `server/acp/agent_protocol_test.go`
- Modify: `server/acp/agent_session_test.go`
- Modify: `server/acp/parity_test.go`

**Interfaces:**

- Removes: `SessionMigrationToken`, ACP `ExportSession`/`ImportSession`, token
  checksum helpers, migration-only params/handlers/errors, dispatcher cases,
  `createImportedEngineFn`, and exclusive tests.
- Retains: `CodeSessionConflict` and every retained operation that uses it.

- [x] **Step 1: Remove dispatcher recognition first**

Delete the `_session/export` and `_session/import` cases and handlers from
`HandleExtensionMethod`. Confirm the new MethodNotFound test goes green before
deleting implementation helpers, proving the observable boundary independently.

- [x] **Step 2: Remove exclusive production code and hooks**

Delete the token type, checksum/version logic, import/export methods,
`CodeMigrationFailed`, `NewMigrationFailedError`, import parameter/response
types, `ErrPrivateMediaMigrationUnsupported`, and `createImportedEngineFn` only
after `rg` proves no retained caller. If `NewSessionConflictError` is used only
by private migration, remove that constructor but retain
`CodeSessionConflict` and the normal ACP conflict helper.

- [x] **Step 3: Remove obsolete success tests, retain negative truth**

Delete tests whose only contract is private import/export success or import
serialization, including the import-registration delete barrier. Update the
protocol error table to remove migration-only constructors. Retain the new
MethodNotFound table plus status, Goal, load/resume/fork/delete, and generic
unknown-extension tests.

- [x] **Step 4: Run focused retained extension and Session tests**

```bash
go test ./server/acp/ -run '^(TestExtensionHandlerRemovedSessionMigrationMethodsReturnMethodNotFound|TestExtensionHandler_SessionStatus|TestExtensionHandler_UnknownMethod|TestACP_DeleteSession.*)$' -count=1
```

- [x] **Step 5: Prove no current production surface remains**

```bash
rg -n 'SessionMigrationToken|ImportSession|ExportSession|_session/(export|import)|CodeMigrationFailed|NewMigrationFailedError|createImportedEngineFn' server/acp --glob '*.go' --glob '!*_test.go'
rg -n '_session/(export|import)' server/acp --glob '*_test.go'
```

Expected: no production hits; the test scan retains only the two negative
dispatcher literals. `ExportSession` under `engine/session` is deliberately
outside this removal scope.

- [x] **Step 6: Commit the removal**

```bash
git add server/acp/agent.go server/acp/streaming.go server/acp/agent_protocol_test.go server/acp/agent_session_test.go
git commit -m "fix(acp): remove private session migration"
```

## Task 3: Remove current claims and close G46

**Files:**

- Modify: `docs/architecture/platform/acp-adapter.md`
- Modify: `docs/architecture/state/sessions.md`
- Create: `docs/migration/verification/p48-5-remove-private-session-migration.md`
- Modify: `docs/migration/verification/README.md`
- Create: `docs/migration/history/runtime/p48-5-remove-private-session-migration.md`
- Modify: `docs/migration/history/README.md`
- Modify: `docs/migration/REMAINING.md`
- Modify: `docs/migration/STATUS.md`
- Modify: `docs/migration/queue.yaml`
- Modify generated: `docs/migration/PLAN.md`
- Modify: `docs/migration/plans/p48-acp-boundary-remediation.md`
- Modify: `docs/migration/plans/README.md`
- Modify: `docs/superpowers/plans/README.md`
- Modify: `docs/superpowers/specs/2026-08-07-acp-boundary-remediation-design.md`
- Repair drifted source anchor: `docs/migration/plans/p46-model-failover-repair.md`

- [x] **Step 1: Update current fact owners only**

Remove current claims that the private methods are supported. Record the
MethodNotFound compatibility consequence, remove G46 and P48.5, and mark P48
complete only if no P48 queue row or unresolved gap remains. Do not rewrite
historical P23/P24 evidence.

- [x] **Step 2: Run ACP SDK, contract, and repository gates**

```bash
go test ./server/acp/ -run '^(TestExtensionHandlerRemovedSessionMigrationMethodsReturnMethodNotFound|TestExtensionHandler_SessionStatus|TestExtensionHandler_UnknownMethod|TestACP_DeleteSession.*)$' -count=1
./scripts/verify-p23-5-acp-sdk.sh
make test-contract
make fmt
make lint
make test
make build
make docs-check
go run ./scripts/migration_manifest.go check
git diff --check
```

- [x] **Step 3: Repeat the source scan after docs synchronization**

```bash
rg -n 'SessionMigrationToken|ImportSession|ExportSession|_session/(export|import)|CodeMigrationFailed|NewMigrationFailedError|createImportedEngineFn' server/acp --glob '*.go' --glob '!*_test.go'
rg -n 'Agent\.(ImportSession|ExportSession)|CodeMigrationFailed|NewMigrationFailedError|createImportedEngineFn' docs/architecture
```

Expected: no current ACP production or implementation-claim hits. Current
architecture may name the two removed wire methods only to state their
MethodNotFound compatibility consequence; historical matches outside this
scope do not invalidate closeout.

- [x] **Step 4: Commit closeout and open one atomic PR**

```bash
git add docs/architecture docs/migration
git commit -m "docs: close P48 ACP boundary remediation"
```

The PR must state the `reject` decision, MethodNotFound compatibility impact,
retained public Session surfaces, rollback boundary, local gate results, and
remote-CI state.
