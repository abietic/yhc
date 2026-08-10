# P48.5 Remove Private Session Migration Verification

**Status:** verification
**Last verified:** 2026-08-07

> **Ownership:** reproducible evidence that the ACP extension dispatcher
> rejects the former private Session-migration names without side effects

## Contract

`Agent.HandleExtensionMethod` recognizes only negotiated Goal methods and the
retained private `_session/status` extension. `_session/export` and
`_session/import` fall through to the pinned SDK's ordinary MethodNotFound
response with a nil result. No compatibility alias, engine construction,
Session registration, or filesystem mutation may occur.

The removal does not change public new, load, resume, fork, delete, or list;
Goal negotiation; `_session/status`; ordinary `CodeSessionConflict` handling;
or the separate sanitized `engine/session.ExportSession` presentation API.

## Test-First Evidence

The initial committed regression called the real dispatcher with a valid-
looking export request and a historically valid import token. Before the fix,
the export case returned the private migration failure path and the import case
reached the engine-construction seam. That RED result distinguished recognized
migration behavior from generic MethodNotFound.

After deletion,
`TestExtensionHandlerRemovedSessionMigrationMethodsReturnMethodNotFound`
asserts for both names:

- a nil result and `*acpsdk.RequestError` with `CodeMethodNotFound`;
- an identical `Agent.sessions` snapshot; and
- an identical recursive project-tree snapshot including entry type, mode,
  modification time, and regular-file bytes.

The migration-only construction hook was then removed with the production
surface, so the final test deliberately does not retain a long-lived test seam.
The final dispatcher has no branch before its MethodNotFound default, and the
production source scan below finds no token, import/export handler, migration
error, or engine-construction hook. RED reachability plus final source deletion
is the engine-construction exclusion proof.

## Source Inventory

The production scan is intentionally separate from the negative test and
current architecture. The test must retain both removed method literals, and
current architecture names them to state their MethodNotFound consequence.

```bash
rg -n 'SessionMigrationToken|ImportSession|ExportSession|_session/(export|import)|CodeMigrationFailed|NewMigrationFailedError|createImportedEngineFn' server/acp --glob '*.go' --glob '!*_test.go'
rg -n '_session/(export|import)' server/acp --glob '*_test.go'
rg -n 'Agent\.(ImportSession|ExportSession)|CodeMigrationFailed|NewMigrationFailedError|createImportedEngineFn' docs/architecture
```

The first and third commands return no hits. The second returns only the two
literal rows in the negative dispatcher test. `ExportSession` under
`engine/session` is deliberately outside the removal scope.

## Commands

```bash
go test ./server/acp/ -run '^(TestExtensionHandlerRemovedSessionMigrationMethodsReturnMethodNotFound|TestExtensionHandler_SessionStatus|TestExtensionHandler_UnknownMethod|TestACP_DeleteSession.*)$' -count=1
go test ./server/acp/ -count=1
go test -race ./server/acp/ -run '^(TestExtensionHandlerRemovedSessionMigrationMethodsReturnMethodNotFound|TestExtensionHandler_SessionStatus|TestExtensionHandler_UnknownMethod|TestP245cGoalExtensionsRequireNegotiationAndStrictSchema|TestACP_DeleteSession.*)$' -count=1
./scripts/verify-p23-5-acp-sdk.sh
make test-contract
make test-race
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

All commands pass on the closeout tree.

## Evidence Limits

The final negative test proves dispatcher behavior, Session-map stability, and
project-tree stability; source reachability proves that no engine-construction
path remains. It does not preserve a migration-only instrumentation hook merely
to count an unreachable call. Local tests do not claim live-client adoption,
remote CI, or ACP v2 compatibility. Those remain separate evidence classes.
