# P50.1 ProjectGraph Rebuilt-Revision Fence

**Status:** historical
**Closed gaps:** G48
**Completed:** 2026-08-07
**Adoption:** `project-native`

> **Ownership:** completion record for P50.1/G48; current permission behavior
> belongs in [`permissions.md`](../../../architecture/capabilities/permissions.md)

## Outcome

ProjectGraph now rejects an external effective-policy revision introduced
after the batch's live check but before canonical action rebuild. Under the
existing batch mutex, the rebuilt action must still name the batch's current
revision before settlement can persist a grant. A mismatch invalidates the
batch and denies the current and remaining decisions with
`project graph permission intent expired`.

The existing post-settlement transition remains authoritative: only a
successful settled action that owns the observed post-policy revision advances
the chain. Plan approval keeps its original exact revision binding. External
settings, ACP, and configuration writers do not acquire the batch mutex, and no
global policy lock, rule schema, reviewer behavior, sandbox, or new dispatch
owner was added.

## Compatibility And Rollback

The observable compatibility change is narrow and fail-closed. An interleaving
that previously absorbed an unrelated policy revision and authorized the
current batch now rejects it. Ordinary distinct exact `allow_always` decisions
still advance through their own successful rule revisions, persistence failure
remains retryable without advancing the revision, and non-ProjectGraph paths
are unchanged.

A squash revert of the rebuilt-revision comparison and its interleaving tests
restores the old race window. No durable schema or rule migration is required;
the independently persisted external rule in the rejected interleaving remains
a valid ordinary rule.

## Evidence

A retained test-only hook first reproduced the exact post-check/pre-rebuild
window. The final regression proves mismatch invalidation and exact rule
absence. Channel and mutex evidence covers concurrent successful and drifted
chains without sleeps. A filesystem oracle covers repeated persistence
failure without revision advance. Public `SubmitRuntimeItem` cases cover
cancellation before settlement and after one settlement, checkpoint disposal,
late-item rejection, an empty decision queue, and zero tool dispatch.

Focused repetition, race, repository, documentation, queue, manifest, and diff
gates are recorded in the
[verification record](../../verification/p50-1-project-graph-revision-fence.md).
Remote CI remains a separate merge gate.
