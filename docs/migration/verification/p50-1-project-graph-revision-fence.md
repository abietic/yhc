# P50.1 ProjectGraph Rebuilt-Revision Fence Verification

**Status:** verification
**Last verified:** 2026-08-07

> **Ownership:** reproducible evidence that one ProjectGraph resume batch
> rejects policy drift introduced after its live check but before action
> rebuild, without adding a global policy lock

## Contract

Ordinary ProjectGraph decisions retain one immutable base policy revision and
one batch-local current revision. Under the existing batch mutex, QueryEngine
checks the live revision, rebuilds the canonical action, and now requires the
rebuilt action's `PolicySnapshotID` to equal the batch current revision before
settlement. A mismatch invalidates the batch and returns
`project graph permission intent expired`; neither the current nor a repeated
remaining decision persists its exact rule or reaches dispatch.

External settings, ACP, and configuration writers remain independent of the
batch mutex. Plan approval retains its separate immutable revision contract,
and non-ProjectGraph permission paths are unchanged.

## Test-First And Interleaving Evidence

The retained interleaving hook runs under the batch mutex after the live check
and before action rebuild. Before the production fence,
`TestP501ProjectGraphRejectsPolicyMutationBetweenCheckAndRebuild` failed because
the first decision returned allowed. With the fence, the hook persists one
unrelated exact Read rule and proves:

- the first decision and two repeated remaining resolutions deny with the
  exact expiry reason;
- the execution is invalidated; and
- only the external rule exists once.

`TestP501ProjectGraphConcurrentDecisionsShareOneRevisionChain` uses channels
and `sync.Mutex.TryLock`, not sleeps, to prove the first hook owns the batch
mutex. The successful variant persists both batch rules exactly once. The
external-drift variant admits only the external rule, invokes no second hook
after invalidation, and denies both batch decisions.

## Failure, Cancellation, And Late-Submit Evidence

`TestP501ProjectGraphPersistenceFailureDoesNotAdvanceRevision` makes `.claude`
a regular file. Both settlement attempts fail at the existing atomic settings
writer, the batch revision remains unchanged, and the sentinel file remains
byte-identical. This preserves the existing retryable failure behavior rather
than inventing sticky invalidation for a non-revision error.

`TestP501ProjectGraphCancelledResumeDoesNotDispatch` drives the public
`SubmitRuntimeItem` boundary for cancellation before the first settlement and
after one settlement has reached the second interrupt. In both cases an
already-cancelled context produces `aborted_streaming`, deletes the checkpoint,
persists no rule, leaves no queued decision, and executes zero tools. Reusing
the late claimed item then returns `persistence_error` because no active
interrupt remains and still executes zero tools.

## Commands

```bash
go test ./engine/ -run '^(TestP501|TestProjectGraphHITLExecution)' -count=1 -timeout=60s
go test -race ./engine/ -run '^(TestP501|TestProjectGraphHITLExecution)' -count=1 -timeout=60s
go test ./engine/ -run '^(TestP501|TestProjectGraphHITLExecution)' -count=100 -timeout=180s
go test -race ./engine/ -run '^(TestP501|TestProjectGraphHITLExecution)' -count=20 -timeout=180s
go test ./server/acp/ -run '^TestACP_ProjectGraphSerializesDistinctExactAllowAlwaysDecisions$' -count=100
go test -race ./server/acp/ -run '^TestACP_ProjectGraphSerializesDistinctExactAllowAlwaysDecisions$' -count=20
make fmt
make lint
make test
make build
make docs-check
go run ./scripts/migration_queue check
go run ./scripts/migration_manifest.go check
git diff --check
```

All listed commands pass on the final closeout tree. Remote CI remains a
separate merge gate.

## Evidence Limits

The fixtures prove process-local ProjectGraph settlement, settings persistence,
public resume cancellation, late-item rejection, exact rule contents, and race
safety. They do not serialize external policy writers, prove a cross-process
file lock, add OS containment, change reviewer authority, or establish Auto
Permission sandbox eligibility.
