# P39.0 Workspace Recovery Contract

**Status:** historical
**Completed:** 2026-08-02
**Adoption:** `project-native`
**Gap state:** G2 remains open

> **Ownership:** closeout evidence for the test-backed workspace recovery
> characterization contract. The frozen contract is
> [`p39-workspace-recovery-contract.md`](../../plans/p39-workspace-recovery-contract.md).
> This record does not claim a production writer or working `/rewind` command.

## Outcome

P39.0 replaces an ambiguous “add rewind” direction with one project-owned
recovery contract. A future service must bind exact private file authority to
Session, turn, canonical workspace root, durable root identity, complete
mutation-source coverage, and deterministic before/settled-after path states.
It must keep conversation transcripts, interaction provenance, permissions,
and workspace rollback as separate owners.

The completed slice deliberately rejects:

- the disconnected process-local `filehistory.FileTracker`;
- treating `FileStateCache` booleans or transcript replay as file content;
- Git HEAD/reset/checkout as the user's pre-turn workspace state;
- inferring complete agent mutations from a post-hoc diff; and
- importing a reference product's history schema.

P39.0 adds no production schema, file bytes, sidecar, writer, API, command, or
automatic recovery. `/rewind` remains a non-executable tombstone, so G2 stays in
`REMAINING.md`.

## Frozen Safety Boundary

The contract requires complete capture before a snapshot may be applied.
Typed root-authorized file mutations may eventually supply exact affected
paths. A turn containing arbitrary Bash, hook, MCP, child-process, editor, or
other unobserved writes is incomplete until a separate enforcing observation
boundary exists; partial restoration cannot be reported as full recovery.

A future preview is read-only. Apply requires the same snapshot and preview
generation, exact Session/turn/root identity, explicit user confirmation, a
fresh permission decision, and final root/path revalidation. Confirmation and
permission cannot synthesize each other, persist as an allow-always rule, or be
bypassed by Auto, `--yolo`, child authority, resume state, or protocol input.

Each current path must equal the recorded settled-after state before restore,
or already equal the captured before state. Every other state is a conflict
left untouched. The service cannot follow links, escape the root, choose older
bytes over an external edit, mutate the Git index, or use Git reset.

## Durability And Lifecycle Contract

P39.0 names logical `workspace-recovery-snapshot/v1` and
`workspace-recovery-operation/v1` records without creating their physical
storage. The snapshot covers Session/turn/root/completeness plus exact ordered
before/after existence, regular-file bytes/digest, and mode. The operation
covers confirmation/permission identity, deterministic per-path results, and
the durable completed/pending set.

After a crash, both identities must be reconstructed, the exact root reopened,
and only pending work reconsidered. A completed write whose journal bit was not
persisted is fenced by equality with the before state. Missing, corrupt,
unknown-version, incomplete, or root-mismatched records fail closed and never
auto-apply.

No authoritative legacy content record exists. A future writer must name the
physical private store, atomic publication and platform `fsync` behavior,
budgets/retention, migration owner, and exact Session-deletion cleanup before
acceptance. P39.0 changes no current Session deletion or export behavior.

## Entrypoints

Root TUI, Plain, ordinary/Goal headless, and ACP identities share the same
future Session/turn contract. Interactive apply still requires an accepted
surface-specific confirmation protocol. Ordinary one-shot headless execution
cannot auto-apply. Child Agents and standalone MCP are unsupported and receive
no root-Session recovery authority. Fork/export/replay do not copy or expose
private recovery content.

## Verification And Review

The merged disposable-workspace oracle covers exact clean/dirty/staged/
untracked/delete/rename restoration, Git-index isolation, external-edit
no-overwrite, link/root replacement, confirmation and permission ordering,
partial failure, cancellation, snapshot plus operation reconstruction,
already-applied fencing, and missing-root failure without panic.

Darwin/Linux focused repetitions and race tests pass. The Windows
volume/file-index identity implementation cross-compiles; a real Windows
filesystem acceptance run remains explicitly unproven. The reference model
also does not claim production crash `fsync`, bind-mount containment, arbitrary
process observation, or the absence of every filesystem check/use race.

Independent review found and drove four repairs before promotion merge:

1. reconstruct both snapshot and operation, not operation progress alone;
2. bind entrypoint disposition to production command/containment identities;
3. replace non-portable reflection with platform-specific durable root
   identity; and
4. check a missing root before identity derivation so restart fails closed
   instead of panicking.

Follow-up review found no remaining issue. Promotion PR #295 passed all local
Makefile gates and remote CI run 30729686748 before squash merge.

## Rollback And Future Work

Rollback removes only the characterization contract and test-only oracle. It
has no schema or user-data migration and leaves `/rewind` unavailable.

Any writer, retention policy, preview/apply API, command, or automatic recovery
requires a new accepted slice. P39.0 completion empties the current execution
queue; it does not promote a successor and does not close G2.
