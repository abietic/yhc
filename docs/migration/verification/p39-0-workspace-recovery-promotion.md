# P39.0 Workspace Recovery Promotion

**Status:** verification
**Snapshot:** `0b3885ab4725b1aa121bb3d19400afbf2b9df93b`
**Measured:** 2026-08-02
**Ownership:** test-only recovery reference model; no production writer or
`/rewind` handler

## Result

At this promotion snapshot, P39.0 became the sole `Ready` slice. A
disposable-workspace matrix proves that the accepted recovery contract can
restore the exact pre-turn
workspace state without treating Git HEAD as the user's state, overwriting a
later external edit, following a symlink outside the saved root, mutating the
Git index, or losing the boundary between completed and pending work after a
partial failure.

This evidence does not close G2. `/rewind` remains a non-executable tombstone,
and production still has no durable recovery schema, writer, preview, or apply
path. P39.0 is characterization and contract work only.

## Reproduce

Run the focused matrix once, repeat it to expose ordering or stale-state bugs,
then run it under the race detector:

```sh
go test ./engine/recovery -run '^TestP390WorkspaceRecoveryPromotionMatrix$' -count=1
go test ./engine/recovery -run '^TestP390WorkspaceRecoveryPromotionMatrix$' -count=10
go test -race ./engine/recovery -run '^TestP390WorkspaceRecoveryPromotionMatrix$' -count=1
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c ./engine/recovery -o build/p39-recovery-windows.test.exe
```

The promotion change also passes `make fmt` and `make lint-new`. The complete
iteration must still pass the repository's four final Makefile gates before it
can merge.

## Frozen Recovery Boundary

The matrix uses a versioned, test-only reference state machine to make the
contract observable:

- A snapshot is bound to its ID, Session ID, turn ID, canonical workspace root,
  and root identity. Each relative path records the before/after existence,
  exact bytes, digest, and mode.
- Apply is allowed only when the current path still equals the recorded
  post-mutation state. Any different bytes, mode, type, symlink, or workspace
  root identity produces a conflict and leaves the current path untouched.
- Paths are processed in deterministic lexical order. Delete and rename are
  represented as explicit before/after path states rather than inferred from
  Git or implemented with a repository reset.
- An operation durably distinguishes completed paths from pending paths. A
  retry fences already-applied work and can continue the exact remaining set
  after state serialization and reconstruction.
- User confirmation and the permission decision both precede mutation.
  Cancellation and partial failure return explicit item results.
- Recovery never stages, resets, or otherwise mutates the Git index.

Root TUI, Plain, headless, and ACP sessions are required to share this
Session/turn recovery contract when a future writer is accepted. Child agents
and standalone MCP are explicitly unsupported by P39.0; neither may inherit or
silently create root-session rollback authority.

## Limits And Next Contract

The matrix is not a production filesystem implementation. Its sequential
reference model and `os.Root` handle prove the observable rules in disposable
workspaces, but they do not prove crash `fsync`, platform-specific descriptor
semantics, bind-mount containment, or the absence of every check/use race. A
future writer requires descriptor-relative, root-fenced operations and must
name the durable schema version, migration owner, retention/deletion owner, and
crash-recovery protocol before acceptance.

The matrix derives its durable root identity from device/inode identity on
Darwin/Linux and volume/file-index identity from an opened non-reparse
directory handle on Windows. Cross-compilation proves the Windows test path is
buildable; it does not substitute for a Windows runtime or filesystem-semantics
acceptance run.

The subsequent P39.0 closeout froze the final
[project-owned contract](../plans/p39-workspace-recovery-contract.md) and
retained this matrix as its conformance oracle. It added no handler, automatic
restoration, or reference repository history format. Rollback keeps `/rewind`
unavailable and preserves the current Git/workspace recovery guidance.

## Decision And Evidence

The adoption decision is `project-native`: retain the project's Session,
transcript, command, permission, and workspace-root owners; use comparative
reference behavior only as evidence; and reject revival of the disconnected
`filehistory.FileTracker` or adoption of another product's history format.

Source-backed context:

- [P39 intake contract](../plans/p38-p45-next-product-intake.md#p39-workspace-recovery-and-rewind)
- [file-state recovery audit](../reference/runtime/file-state-checkpoint-recovery-audit.md)
- [command architecture](../../architecture/capabilities/commands.md)
