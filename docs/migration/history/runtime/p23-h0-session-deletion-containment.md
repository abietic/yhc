# P23.H0 Session Deletion Containment

**Status:** historical
**Closed gaps:** G15
**Completed:** 2026-07-26
**Last verified:** 2026-07-26

> **Ownership:** delivery evidence for contained engine and ACP session
> deletion. Current behavior belongs in
> [`acp-adapter.md`](../../../architecture/platform/acp-adapter.md); remaining
> ACP work and executable order belong in
> [`p23-acp-adapter-hardening.md`](../../plans/p23-acp-adapter-hardening.md)
> and [`PLAN.md`](../../PLAN.md).

## Decision

P23.H0 was delivered under the **`combine`** decision:

- preserve the existing opaque single-filename-component Session ID
  compatibility boundary instead of imposing UUID-only storage;
- preserve `engine/session.DeleteSession` as the transcript, lineage, and
  owned-sidecar service;
- replace ACP's direct path assembly and active-session destruction with
  lifecycle coordination plus delegation; and
- preserve the established non-transactional failure semantics after a valid
  deletion starts.

## Outcome

`DeleteSession` now resolves the configured transcript root and preflights the
transcript, `.tmp`, runtime-input, and ProjectGraph-checkpoint paths before
removing anything. Every present target must be a regular file under that
resolved root. Malformed IDs, escaping paths, final-component symlinks,
directories, and other non-owned targets fail without filesystem mutation.
Unrelated neighboring files are not part of the deletion set.

ACP serializes session creation, restore, fork, import, close, and deletion.
Delete rejects an active target without unregistering or closing it, holds the
registry boundary through the shared service call, and keeps the existing
idempotent success for a missing durable session.

No public protocol version, durable schema, transcript format, provider route,
or non-ACP session-administration command changed.

## Evidence

Focused engine tests prove:

- traversal, absolute, slash, and backslash IDs cannot remove the exact files
  the previous join would have addressed;
- transcript and each owned-sidecar symlink reject before mutation;
- directories and other non-regular transcript targets reject unchanged;
- a symlinked configured root resolves to its owned directory;
- valid deletion removes the transcript and all three sidecar classes while
  preserving an unrelated neighbor; and
- missing transcripts retain `os.ErrNotExist`, normal deletion, byte
  accounting, parent lineage, and bulk deletion behavior.

Focused ACP tests prove:

- a valid inactive deletion reaches the shared service and removes its complete
  owned set;
- an active target remains registered and its transcript/sidecar remain;
- new/load/resume/fork/import registration, close, and deletion share one
  lifecycle boundary;
- a fork cannot register a child after agent close, and import registration
  cannot cross an inactive-delete check;
- a traversal ID leaves the prior out-of-root victim and session map unchanged;
  and
- missing deletion remains idempotent.

Closeout passed:

```text
make fmt
make lint
make lint-new
make test
make build
make docs-check
go test -race ./server/acp
go run ./scripts/migration_manifest.go check-ledger
go run ./scripts/migration_manifest.go check
go run ./scripts/migration_scan -reference .reference/claude-code-ripe -json
git diff --check
```

The race detector initially exposed two pre-existing fixtures that spent one
shared operation deadline before their restore assertion. The unmodified base
reproduced both failures. Giving the independent restore operations their own
bounded deadline made the required full ACP race gate deterministic without
changing production behavior.

## Compatibility And Failure Boundary

Compatibility narrows unsafe deletion only. Session IDs remain filename
components rather than UUID-only values, and ACP still treats a missing
inactive session as already deleted. Clients must close an active session
before deletion.

After preflight succeeds, deletion remains intentionally non-transactional:
the transcript is removed first, `.tmp` cleanup is best-effort, and a later
runtime-input or ProjectGraph-sidecar error is returned without rollback.
Final-component symlinks are rejected. Portable standard-library code does not
claim protection against a separate same-authority local process replacing
the resolved store concurrently.

Rollback is one code-and-test unit. Disabling the advertised ACP delete
capability is the safe operational fallback; restoring the previous direct
handler would reopen G15.

## Current Source

| Boundary | Code reference | Why it matters |
|---|---|---|
| shared deletion owner | [`DeleteSession`](../../../../engine/session/delete.go) | Owns complete preflight, transcript removal, sidecars, byte accounting, and lineage |
| ACP lifecycle and delegation | [`Agent.UnstableDeleteSession`](../../../../server/acp/agent.go) | Rejects active targets and calls the shared owner under serialized lifecycle state |
| fork and import registration | [`Agent.UnstableForkSession`](../../../../server/acp/streaming.go) | Keeps child/restore registration inside the same ACP lifecycle boundary |
| engine negative matrix | [`delete_test.go`](../../../../engine/session/delete_test.go) | Proves containment, target ownership, zero-mutation rejection, and valid cleanup |
| ACP entrypoint matrix | [`agent_session_test.go`](../../../../server/acp/agent_session_test.go) | Proves active/inactive/missing behavior, import ordering, and registry safety |

## Next State

G15 is closed. P23.H1-P23.5 remain queued; none becomes executable until root
`PLAN.md` explicitly selects one next slice.
