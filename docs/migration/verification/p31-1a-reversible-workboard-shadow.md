# P31.1a Reversible WorkBoard Shadow Verification

**Status:** verification
**Last verified:** 2026-07-30
**Scope:** P31.1a only

> **Ownership:** reproducible acceptance evidence for the P31.1a reversible
> WorkBoard comparison shadow, failure isolation, Session lifecycle exclusion,
> exact sidecar deletion, and rollback boundary.

## Acceptance Evidence

| Boundary | Evidence |
|---|---|
| Legacy compatibility | The P31 promotion Task/Todo fixtures execute with the observer disabled and enabled; exact results, errors, aliases, state, and trusted scopes remain unchanged. |
| Observer ordering | Tool tests prove only real accepted mutations observe, detached snapshots are used, and observer panic cannot change legacy success or state. |
| Domain and codec | WorkBoard tests cover strict round trip, detached decode, unknown fields/version, ownership mismatch, corruption, duplicate/self/missing/cyclic dependencies, invalid status, and every frozen budget. |
| Todo identity | Unique exact content/activity matches retain identity, ambiguous duplicates allocate new IDs, omissions become cancelled evidence, and trusted root/child partitions remain isolated. |
| Persistence | Tests inject `encode`, `mkdir`, `create_temp`, `chmod`, `write`, `sync`, `close`, and `rename`; each leaves the previous complete record or no record and no owned temp file. |
| Existing-record validation | Tests inject `read`, `decode`, `identity`, and `version`, reject corrupt or mismatched records, and prove no candidate state is restored. |
| Privacy and containment | The writer requires a private parent and mode-0600 regular target, refuses links and existing non-private directories without chmod, and bounds diagnostics. |
| Engine lifetime | Root/child sharing and independent-root isolation pass. Administration and restore staging never construct a shadow. Resume/fork/new activation clear the old binding, leave the source sidecar unchanged, and create no target sidecar. |
| Baseline and deletion | A shadow-unaware engine reopens the unchanged transcript while ignoring the removable sidecar. Delete removes only the exact regular sidecar and rejects links or appearance races before transcript mutation. |
| Concurrency | Focused race tests cover serialized concurrent observers, tool hooks, engine activation, and deletion boundaries. |

## Commands

```text
make fmt
make lint
make lint-new
make test
make build
make docs-check
make docs-check-ci
go run ./scripts/migration_manifest.go sync
go test -race ./engine/internal/workboard ./tools ./engine ./engine/session -run 'Test(Shadow|WorkBoard|P31Promotion|P311a|DeleteSession.*WorkBoard)' -count=1
```

All commands passed, and manifest synchronization produced no further drift.
The physical terminal-grid diagnostic remains the repository's existing opt-in
skip and is unrelated to this slice.
