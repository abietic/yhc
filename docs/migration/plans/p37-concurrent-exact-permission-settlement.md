# P37 ProjectGraph Exact Permission Settlement Chain

**Status:** historical
**Accepted:** 2026-08-01
**Completed:** 2026-08-01
**Adoption:** `preserve`

> **Ownership:** completed contract for retaining distinct exact permission
> decisions in one ProjectGraph resume batch without accepting unexplained
> policy drift. Current behavior belongs in
> [`permissions.md`](../../architecture/capabilities/permissions.md); delivery
> evidence is in
> [`p37-1-project-graph-permission-settlement-chain.md`](../history/runtime/p37-1-project-graph-permission-settlement-chain.md).

## Outcome

P37.1 is complete. A ProjectGraph resume batch now settles ordinary permission
decisions through one revision chain. Every decision remains bound to the
batch's original policy revision and exact request/invocation identity; each
action is rebuilt under the batch lock after the prior decision has settled.
Only a successful settlement whose final action owns the observed post-policy
revision advances the chain.

This preserves the P22.1b exact-grant contract without weakening its final
dispatch fence. Distinct exact `allow_always` decisions in the same batch can
all reach the existing lossless settings writer. Any external or unexplained
policy revision expires the remaining decisions. Plan approval stays outside
the chain and retains its original exact revision binding.

## Reproduced Failure

The P36 candidate's full test workflow exposed an existing ACP ProjectGraph
fixture losing one of two distinct exact Read rules. Both requests were probed
under policy revision P0. During concurrent execution, the first
`allow_always` settlement persisted its rule and reloaded policy as P1. The
second request then compared its stale P0 action to P1 and failed before the
already-serialized settings read-merge-write owner.

The current invocation failed closed, but the user's exact durable decision
was silently discarded. A persistence lock could not repair the earlier
action-binding rejection, and allowing arbitrary snapshot drift would have
weakened authorization.

## Delivered Contract

[`projectGraphHITLExecution`](../../../engine/graph_hitl.go) owns one resume
batch:

1. It clones the durable decisions, requires a non-empty set, and requires all
   decisions to share one immutable base policy revision.
2. Request lookup retains exact request ID and invocation digest. Every
   decision must still name the immutable batch base revision.
3. Ordinary permission settlement runs under one batch mutex. Immediately
   before settlement, the live policy revision must equal the chain's current
   revision.
4. QueryEngine rebuilds the registered action inside that critical section and
   applies the existing complete settlement path. It does not reuse the
   pre-interrupt action descriptor.
5. The chain advances only when settlement succeeds, returns a settled action,
   and that action's `PolicySnapshotID` equals the post-settlement live
   revision.
6. An external or unexplained revision change invalidates the batch. Remaining
   decisions return `project graph permission intent expired`, persist
   nothing, and dispatch nothing.
7. Plan approvals use the original strict policy-revision lookup and never
   participate in ordinary grant chaining.

The chain proves only permission settlement order. Every allowed action still
passes the existing final descriptor rebuild, resolved-path checks, registry
generation and execution lease before dispatch. A later policy mutation may
therefore fail the current action closed even after its exact grant is durable
for retry.

## Authority And Entrypoints

The durable decision is targeted user intent, not approval by itself.
`ResolvePermissionInteraction` validates the active interrupt and enqueues one
versioned decision bound to interrupt ID, request ID, invocation digest, and
base policy revision. Supported transports claim the engine-owned permission
item before calling `SubmitRuntimeItem`; that public method does not itself
prove a prior claim. The resumed ProjectGraph and Compose interrupt state
revalidate the exact active identity before constructing the batch execution
owner.

TUI, Plain, and ACP present and submit decisions through the same ProjectGraph
owner. ACP's existing adapter remains a transport: it collects the user's
terminal decision, claims the engine-owned runtime item, and resumes the same
QueryEngine. Child execution retains its existing child QueryEngine and bubble
transport boundaries. Non-interactive headless cannot fabricate a decision,
and standalone MCP remains outside scope because it has neither QueryEngine
ProjectGraph nor this interaction owner.

## Scope And Non-goals

P37.1 changed only ProjectGraph permission resume execution and focused engine/
ACP tests. It added no rule schema, cross-process file lock, alternate policy
evaluator, prompt batching, permission UI, reviewer/classifier behavior, Plan
authority, standalone-MCP behavior, or P36 replay behavior.

It does not promise that every concurrently approved tool executes. Permission
settlement and durable exact-rule retention are chained; final dispatch remains
independently bound and can reject later drift. It also does not treat an
arbitrary policy update as part of the batch merely because it produced the
same revision shape.

## Verification

Focused proof covers:

- two distinct exact `allow_always` decisions advancing one batch chain;
- external exact-rule mutation expiring the later decision without persisting
  it;
- Plan-policy drift retaining the original strict behavior;
- the original ACP two-Read fixture retaining both exact rules once; and
- race validation across engine and ACP paths.

The original ACP regression passed 600 repetitions. The new chain, external
drift, Plan drift, and existing policy-expiry fixtures passed 100 repetitions,
and the focused engine/ACP set passed with the race detector. Repository and
documentation gates are recorded in the delivery evidence rather than owned
by this historical contract.

P37's focused fixtures did not inject cancellation or persistence failure
between two batch settlements, nor external mutation after the live check but
before action rebuild. P50.1 later closed that narrower current-action window
with a rebuilt-revision fence and added deterministic persistence, cancellation,
late-submit, repetition, and race evidence. P37's original settlement-chain
claim remains unchanged; the successor proof is recorded in the
[P50.1 closeout](../history/runtime/p50-1-project-graph-revision-fence.md).

## Compatibility And Rollback

Compatibility changes are narrow. Within one verified ProjectGraph resume
batch, a later exact decision is rebuilt on the revision produced by an earlier
successful settlement instead of being rejected solely because it retained
the batch base revision. Unexplained policy drift, Plan decisions, action
identity, persistence encoding, and final dispatch behavior remain fail-closed.

No durable schema changed. A squash revert restores stale pre-batch action
comparison and reopens the distinct exact-rule loss. Persisted exact rules
remain valid P22.1b rules and require no rollback migration.
