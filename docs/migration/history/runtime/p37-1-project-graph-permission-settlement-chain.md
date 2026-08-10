# P37.1 ProjectGraph Permission Settlement Chain

**Status:** historical
**Closed gaps:** G35
**Completed:** 2026-08-01

> **Ownership:** delivery evidence for chaining ordinary ProjectGraph
> permission settlements across exact grant-owned policy revisions while
> expiring unexplained drift.

## Outcome

P37.1 completed the `preserve` repair and closed G35. Two or more ordinary
permission decisions restored from one ProjectGraph interrupt batch now share
one execution owner. That owner requires a single immutable base policy
revision, serializes settlement, rebuilds each action after the prior decision,
and advances only through a successful settled action's observed policy
revision.

The repair retains every distinct exact `allow_always` rule that reaches the
batch from its durable targeted decision. It does not turn policy drift into
dispatch authority: an external revision expires the remaining batch, and
every allowed result still passes QueryEngine's existing final action and
registry-lease checks.

## Ordering And Authority

ProjectGraph first validates the active interrupt against the runtime decision's
version, interrupt ID, request ID, invocation digest, and original policy
revision. Collected decisions remain user intent rather than approval
authority. On execution:

1. the batch rejects empty or mixed-base decision sets;
2. each ordinary request matches its exact ID and invocation digest and still
   names the batch base revision;
3. the batch mutex compares the live policy revision with its current tracked
   revision;
4. QueryEngine rebuilds and settles the action inside that mutex;
5. only an allowed settled action whose snapshot equals the post-settlement
   revision advances the chain; and
6. unexplained drift invalidates every remaining decision.

Plan approval is intentionally excluded and continues to require the original
exact revision. Final resolved-path, policy, registry generation, and execution
lease validation remains outside and after this settlement chain, so a later
mutation can still deny the current invocation.

## Proof And Review

Focused validation passed:

```text
go test ./engine -run 'TestProjectGraphHITLExecution|TestP138ProjectGraphPlanApprovalPolicyDrift|TestP138ProjectGraphPolicyChangeExpiresPriorIntent' -count=100
go test ./server/acp -run '^TestACP_ProjectGraphSerializesDistinctExactAllowAlwaysDecisions$' -count=100
go test ./server/acp -run '^TestACP_ProjectGraphSerializesDistinctExactAllowAlwaysDecisions$' -count=600
go test -race ./engine ./server/acp -run 'TestProjectGraphHITLExecution|TestP138ProjectGraphPlanApprovalPolicyDrift|TestP138ProjectGraphPolicyChangeExpiresPriorIntent|TestACP_ProjectGraphSerializesDistinctExactAllowAlwaysDecisions' -count=1
```

The 600-run ACP stress retained both exact rules on every run. Independent
permission/concurrency review found no P0-P3 defect. It confirmed the
single-base batch, mutex-protected rebuild, successful post-revision advance,
external-drift expiry, and unchanged Plan binding. The review also noted that
the narrowly timed current settlement may return allowed before a newly
observed external revision invalidates later decisions; the existing final
dispatch binding remains the safety owner for that current action.

The focused matrix does not inject cancellation or persistence failure between
two batch settlements. Those paths retain the same observable boundary: a
failed settlement cannot advance the tracked revision, and any observed
unexplained revision expires later decisions.

Repository closeout additionally passed the Makefile formatting, lint,
new-finding lint, test, build, and documentation gates plus migration-manifest
and diff checks.

## Compatibility And Rollback

No persisted schema or exact-rule encoding changed. Existing checkpoints and
settings remain readable. The only compatibility change is that a later
ordinary decision in one verified batch may settle on the exact policy
revision produced by its predecessor instead of losing its durable rule.

A squash revert removes the batch revision chain and restores the prior stale
action comparison. It requires no data migration but reopens G35. Already
persisted exact rules remain valid and should not be removed.
