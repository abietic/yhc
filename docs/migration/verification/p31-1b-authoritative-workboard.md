# P31.1b Authoritative WorkBoard Verification

**Status:** verification
**Last verified:** 2026-07-31
**Scope:** P31.1b only

> **Ownership:** reproducible acceptance evidence for WorkBoard v2 authority,
> exact Task/Todo compatibility, marker-last cutover, Session lifecycle,
> forward-only recovery, failure isolation, and source-owner boundaries.

## Acceptance Evidence

| Boundary | Evidence |
|---|---|
| Compatibility | P31.1a on/off fixtures remain exact. Adapter tests cover Task aliases, arbitrary statuses, unresolved dependency promotion, metadata/output, atomic stop events, Todo replacement, all-complete clearing, duplicate identity, and exact child partitions. A bare historical transcript directory remains readable in legacy mode, while its first cutover still requires private containment before creating any artifact. |
| Single owner | Root and child engines share one adapter-bound TaskManager; independent roots are isolated. Durable Session scope without an adapter fails before read/write. Direct tools and standalone MCP use only explicit opaque non-Session scope. |
| Cutover | Backup, v2 seed, and marker-last stages have deterministic encode/create/chmod/write/sync/close/rename/parent-sync/re-read/install seams. Marker absence remains legacy; marker visibility never restores a legacy writer. |
| Strict storage | Tests reject corrupt, unknown, mismatched, oversized, linked, non-regular, unsafe-mode, invalid-revision, invalid-compatibility, dependency-cycle, and replacement-race inputs under exact 0700/0600 containment. |
| Durability uncertainty | Post-rename failure restores the prior file or absence. Compensation failure persists 0400 quarantine for both pre-marker and marked states; rebuilt Store, adapter, and QueryEngine reject before a model call. |
| Session lifecycle | Tests cover marked and legacy resume, authoritative and legacy fork, child-specific backup recovery, active and partial deletion retry, compaction BoardID/revision stability, export exclusion, administration validation, and inert restore staging. |
| Recovery | Local recovery requires exact Session ID, BoardID, positive revision, and acknowledgement; it installs the immutable baseline under a fresh BoardID while retaining the marker and never restoring a legacy writer. |
| Concurrency | Focused race tests cover root/child Task/Todo mutation, cutover, lifecycle gates, independent engines, Task inspection, Session deletion, and adapter/store access. |
| Review | Independent review found and closed post-rename retry duplication, legacy child Todo omission, marked in-memory scope divergence, and cross-restart quarantine gaps. Final re-review reported no findings. |

## Commands

```text
go test ./engine/internal/workboard ./tools ./engine/session ./engine ./cmd/eino-agent/cmd -run 'Test(P311b|LogicalWorkAdapter|StoreCutover|StoreMarker|StoreVisible|StoreInspect|ArtifactStore|ExplicitNonSession|DeleteSession_WorkBoard|ChildTaskLifecycle|ConcurrentTaskInspection|ConcurrentEnginesOwn|SessionsRecoverWorkBoard|SessionsDelete)' -count=1 -timeout=8m
go test -race ./engine/internal/workboard ./tools ./engine/session ./engine -run 'Test(P311b|LogicalWorkAdapter|StoreCutover|StoreMarker|StoreVisible|StoreInspect|ArtifactStore|ExplicitNonSession|DeleteSession_WorkBoard|ChildTaskLifecycle|ConcurrentTaskInspection|ConcurrentEnginesOwn)' -count=1 -timeout=8m
make fmt
make lint
make lint-new
make test
make build
make docs-check
make docs-check-ci
go run ./scripts/migration_manifest.go sync
git diff --check
```

All commands passed, and manifest synchronization produced no further drift.
The physical terminal-grid diagnostic remains the repository's existing
opt-in skip and is unrelated to this runtime slice.
