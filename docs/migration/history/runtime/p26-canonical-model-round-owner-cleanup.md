# P26 Canonical Model-Round Owner Cleanup

**Status:** historical
**Closed gaps:** G23
**Completed:** 2026-07-27
**Last verified:** 2026-07-27

> **Ownership:** completed P26.1 `project-native` decision, deleted duplicate
> ownership, compatibility evidence, and rollback boundary. Current behavior
> belongs in
> [`query-engine.md`](../../../architecture/runtime/query-engine.md) and
> [`model-and-tool-execution.md`](../../../architecture/runtime/model-and-tool-execution.md).

## Outcome

P26.1 removed the production-unreachable immediate tool executor from the
canonical model round. `runCanonicalModelRound` now has one job at the stream
boundary: consume and classify the complete provider stream with a deferred
collector. It cannot dispatch a committed tool.

Committed tool calls still cross cloned ProjectGraph state into
`runCanonicalToolRound`. That boundary remains the sole model-derived owner of
stable scheduling, repeated-call admission, permissions, hooks, progress,
cancellation, execution, and result settlement.

## Delivered Boundary

The cleanup made deferred execution structural rather than configurable:

| Removed owner or state | Current replacement |
|---|---|
| `canonicalModelRoundInput.deferToolExecution` and model-round `hookExecutor` plumbing | One unconditional stream collector with the model context and `DeferExecution: true` |
| Immediate executor callbacks and `canonicalModelRoundResult.streamingExecutor` | Committed calls cloned into Graph state and admitted only by `runCanonicalToolRound` |
| Fallback discard and abort drain for the immediate executor | Existing model fallback and abort terminal paths without unreachable executor cleanup |

`ProcessStream` still finishes terminal classification before any tool side
effect. Truncated, cancelled, withheld, malformed, failed, or abandoned
fallback streams therefore execute no tool. A committed call set still
preserves model order, safe parallel batches, serial barriers, call identity,
patched arguments, permission and hook ordering, Plan admission, durable HITL,
events, transcripts, terminals, and cancellation behavior.

## Evidence

The implementation and regression evidence are owned by:

- [`runCanonicalModelRound`](../../../../engine/model_round.go), the
  classification-only model boundary;
- [`runCanonicalToolRound`](../../../../engine/tool_round.go), the committed
  dispatch boundary;
- [`TestP26CanonicalModelRoundHasOnlyDeferredCollector`](../../../../engine/model_round_owner_test.go),
  which rejects forbidden model-round state, callbacks, and non-deferred
  collector configuration;
- [`TestP26ProjectGraphRemainsOnlyModelDerivedToolDispatchOwner`](../../../../engine/model_round_owner_test.go),
  which enumerates every production engine caller and requires committed
  dispatch to remain in the tool round;
- [`TestCanonicalProjectGraphQueryTrace`](../../../../engine/canonical_trace_fixture_test.go),
  which preserves the checked-in request, tool, event, state, transcript, and
  terminal traces;
- the streamed exactly-once and truncation rejection tests in
  [`query_streaming_execution_test.go`](../../../../engine/query_streaming_execution_test.go);
  and
- durable interrupt/resume and concurrent-invocation race coverage in
  [`graph_hitl_test.go`](../../../../engine/graph_hitl_test.go) and
  [`graph_query_kernel_test.go`](../../../../engine/graph_query_kernel_test.go).

Closeout also passed the repository formatting, lint, test, build, new-lint,
documentation, migration-ledger, scanner, race, and diff gates.

## Compatibility And Rollback

The change deletes an unreachable implementation; it changes no public API,
provider request, tool schema, dependency, durable session, transcript,
checkpoint, runtime item, permission rule, or entrypoint. TUI, plain, headless,
ACP, direct Query, and foreground/background child execution continue through
the same ProjectGraph lifecycle.

Rollback is the single P26.1 squash commit. Reverting it restores the obsolete
selectable branch and cleanup state without a data migration. The last safe
production behavior remains deferred-only, so rollback does not create a
supported immediate-execution mode.

## Current Replacements

- Current query ownership:
  [`query-engine.md`](../../../architecture/runtime/query-engine.md)
- Current model/tool ordering:
  [`model-and-tool-execution.md`](../../../architecture/runtime/model-and-tool-execution.md)
- Verified product state:
  [`STATUS.md`](../../STATUS.md)
- Future execution selection:
  [`PLAN.md`](../../PLAN.md)
