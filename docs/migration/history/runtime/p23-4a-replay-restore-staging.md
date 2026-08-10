# P23.4a Replay Snapshot and Restore Staging

**Status:** historical
**Completed:** 2026-07-28
**Last verified:** 2026-07-28

> **Ownership:** delivery evidence for the immutable session replay snapshot
> and non-persisting restore-staging lifecycle. Current behavior belongs in
> [`sessions.md`](../../../architecture/state/sessions.md) and
> [`transcripts.md`](../../../architecture/state/transcripts.md); remaining ACP
> work and executable order belong in
> [`p23-acp-adapter-hardening.md`](../../plans/p23-acp-adapter-hardening.md)
> and [`PLAN.md`](../../PLAN.md).

## Decision

P23.4a was delivered as a **`project-native`** prerequisite inside the accepted
P23 **`combine`** program:

- preserve the transcript recorder, lifecycle-boundary selection, ordinary
  QueryEngine resume/close persistence, and existing ACP behavior;
- add one revision-bound session replay value that retains physical and logical
  identity without opening a writer, repairing data, or mutating engine state;
- select the restore-staging lifecycle before construction or resume so abort
  cannot enter an ordinary persistence path; and
- defer runtime activation until commit rather than creating a second replay,
  permission, hook, MCP, input, or service owner.

The slice did not advertise ACP load, send replay notifications, paginate
session listing, implement stdio MCP, change the durable schema, adopt ACP v2,
or replace QueryEngine.

## Outcome

`LoadSessionReplaySnapshot` reads the same transcript and selects the same
final active lifecycle context as ordinary resume. Each ordered item retains
its exact `MessageEntryIdentity`, persisted logical assistant identity when
present, a deep-cloned message, and a validated tool outcome. Legacy anonymous
tool calls receive deterministic snapshot-local pairing only when exactly one
pending call makes the result unambiguous.

The loader returns no partial snapshot for cancellation, corruption, duplicate
physical/logical/tool identity, malformed roles, orphaned results, unknown
outcomes, unsettled calls, or ambiguous legacy pairing. `Items()` clones on
read, and the complete path leaves transcript bytes and directory contents
unchanged. `SessionService.ReplaySnapshot` exposes the same read-only owner to
future adapters.

`NewRestoreStagingQueryEngine` selects a staged lifecycle before construction.
Staged resume reconstructs the selected session without checkpointing or
registering it. It keeps runtime-input recovery in memory and defers shell-hook
registration, resumed-project MCP initialization, settings watching,
long-session services, Agent memory initialization, worktree recovery, and
model/command execution.

`CommitRestoreStaging` first commits deferred runtime-input recovery, activates
the resumed runtime once, and permanently restores ordinary close persistence.
`AbortRestoreStaging` is idempotent before commit, clears pending activation,
and closes owned resources without transcript checkpoint, close, or sync.
Aborting an ordinary or committed engine fails closed. `Close` races with
commit or abort through the same lifecycle lock: exactly one persistence
decision wins, and a pre-commit close is a non-persisting abort.

Ordinary and administration construction, resume ordering, checkpointing, and
close behavior remain unchanged. ACP does not consume either primitive yet.

## Evidence

Focused snapshot, staging, deferred-activation, durable-byte, cancellation,
ordinary-regression, and transition-race tests passed:

```text
go test ./engine/session -run '^TestP234aReplaySnapshot' -count=1
go test ./engine -run '^TestP234a' -count=1
go test -race ./engine/session -run '^TestP234aReplaySnapshot' -count=1
go test -race ./engine -run '^TestP234a' -count=1 -timeout=2m
go test ./engine/session -count=1
go test ./engine -count=1
```

Repository closeout passed:

```text
make fmt
make lint
make test
make build
make docs-check
go run ./scripts/migration_manifest.go check
go run ./scripts/migration_scan
git diff --check
```

The replay fixtures prove final-active-context equivalence, persisted and
legacy identity, clone isolation, deterministic pairing, exact success/failure
classification, strict rejection, and cancellation before and during
construction. Restore fixtures prove exact transcript and runtime-input bytes,
idempotent abort, one-way commit, blocked pre-commit runtime including `/new`,
deferred catalog/hook/MCP/watcher/service activation, ordinary persistence,
and `Commit`/`Abort`/`Close` race linearization.

## Compatibility and Rollback

No ACP client-visible capability or notification changed. Ordinary embedders
continue to use `NewQueryEngine`, `ResumeSession`, and persistent `Close`;
adapters must opt into `NewRestoreStagingQueryEngine` explicitly and must
commit before submitting runtime input.

Before P23.4b consumes these APIs, rollback may remove the replay snapshot,
session-service method, staging constructor/lifecycle, and deferred
runtime-input recovery as one unused prerequisite boundary. After consumption,
producer and consumer must roll back together so ACP cannot advertise load
without validation and non-persisting failure cleanup. No durable migration or
data rollback is required.

## Current Source

| Boundary | Code reference | Why it matters |
|---|---|---|
| immutable replay value and strict loader | [`replay_snapshot.go`](../../../../engine/session/replay_snapshot.go) | Selects the active context, retains durable identity, validates tool settlement, clones results, and never writes |
| session-service exposure | [`session_service.go`](../../../../engine/session_service.go) | Reuses the engine-owned session access boundary without activating a runtime |
| staged lifecycle and activation | [`restore_staging.go`](../../../../engine/restore_staging.go) | Owns commit/abort/close linearization and the one-way transition to ordinary persistence |
| construction and close boundary | [`engine.go`](../../../../engine/engine.go) | Selects staging before side effects, defers runtime dependencies, and preserves ordinary close behavior |
| deferred runtime-input recovery | [`input_coordinator.go`](../../../../engine/input_coordinator.go) | Reconstructs stale delivery state in memory and persists it only at commit |
| replay fixtures | [`replay_snapshot_test.go`](../../../../engine/session/replay_snapshot_test.go) | Proves identity, strict validation, clone isolation, cancellation, and exact read-only behavior |
| staging fixtures | [`restore_staging_test.go`](../../../../engine/restore_staging_test.go) | Proves persistence boundaries, deferred activation, ordinary regressions, and transition races |

## Next State

P23.4a left the live queue with no slice currently executable. Its completion
satisfies P23.4b's prerequisite, but root `PLAN.md` must promote P23.4b in a
separate intake iteration before ACP can consume the primitives. Until then
load remains unadvertised and G16 remains open. P23.5 retains its independent
stdio-MCP lifecycle gate.
