# P24.2b Goal Provider Accounting

**Created:** 2026-07-29
**Completed:** 2026-07-29
**Status:** historical
**Adoption:** `adapt`

> **Ownership:** completion evidence for P24.2b. Current Goal provider
> accounting behavior belongs in
> [`query-engine.md`](../../../architecture/runtime/query-engine.md),
> [`sessions.md`](../../../architecture/state/sessions.md),
> [`transcripts.md`](../../../architecture/state/transcripts.md), and
> [`runtime-events.md`](../../../architecture/tui/contracts/runtime-events.md).
> Executable order belongs in [`migration/PLAN.md`](../../PLAN.md).

## User Problem

P24.2a could preserve completion intent but could not prove the Goal token
budget. Provider calls made by the root engine, retry and fallback paths,
compaction, model-backed helpers, or descendants could therefore bypass a
single authoritative limit. Treating transcript text or Session display usage
as accounting evidence would also undercount calls and make crash recovery
ambiguous.

P24.2b closes that accounting boundary. It does not add automatic
continuation, Goal tools or commands, new transports, runtime items, or UI
consumption.

## Delivered Contract

One QueryEngine-owned Goal accounting service now provides:

- a capacity-one root admission gate shared by the root and exact child
  generations;
- a durable pending admission written before every provider dispatch;
- one append-only, fsync-backed transcript usage record for each admitted
  provider call, followed by an aggregate Goal checkpoint;
- normalized charged tokens computed as
  `max(provider total, prompt + completion) - reported cached tokens`, with a
  cached deduction only when the provider supplies an explicit valid detail;
- exact child-generation capabilities that attribute usage to the root Goal
  without granting lifecycle mutation authority;
- aggregate recovery from the transcript ledger after a crash between ledger
  append and Goal checkpoint; and
- fail-closed behavior for missing usage, ambiguous dispatch, corrupt or
  oversized ledgers, uncertain durability, stale capabilities, and exhausted
  budgets.

The main model round, retry and fallback attempts, LLM compaction,
classification, permission explanation, tool-use summaries, and WebFetch
model extraction all use the same accounting boundary. Model-backed approval
review and queued long-session background work are suppressed while a Goal is
unfinished because those paths do not yet carry exact Goal authority. A
reader-writer dispatch boundary makes Goal publication wait for an already
started background call and makes queued work re-check Goal state immediately
before provider dispatch.

Session `UsageSummary` remains a display and compatibility projection; it is
not the Goal budget authority. The Goal usage record is a distinct transcript
entry with its own identity, schema, and replay rules.

## Restore And Compatibility

Goal persistence is version 2. The current reader migrates a supported
version-1 Goal with zero accounted usage and no pending admission, while an
older reader rejects version 2 rather than silently dropping accounting
state. Restore validates the aggregate against the exact transcript ledger,
repairs an interrupted post-ledger checkpoint, and refuses to resume when an
admitted call has no conclusive durable usage record.

The provider-side header remains an opaque per-call identifier. It is not used
as provider-neutral evidence unless the response or error path establishes
the exact call outcome and usage.

## Evidence

The implementation and focused fixtures are owned by:

- [provider-call admission and completion helpers](../../../../engine/execution/provider_usage.go);
- [Goal accounting service, recovery, and child capabilities](../../../../engine/goal_usage.go);
- [provider usage normalization](../../../../engine/goal_usage_normalize.go);
- [Goal persistence migration](../../../../engine/goal_persistence.go);
- [Goal transcript usage record and loader](../../../../engine/transcript/goal_usage.go);
- [main model-round accounting](../../../../engine/model_round.go);
- [compaction accounting](../../../../engine/compact/llm_compact.go);
- [WebFetch helper accounting](../../../../tools/webfetch.go); and
- [P24.2b accounting, recovery, and boundary fixtures](../../../../engine/goal_usage_test.go).

Focused tests cover exact-token normalization, root and child serialization,
budget exhaustion, ambiguous and missing usage, durability uncertainty,
checkpoint recovery, version-1 migration, corrupt and oversized ledgers,
fallback and compaction calls, direct model helpers, queued background work,
Goal activation during an in-flight background call, and race execution.

Two independent reviews first identified uncertain fsync failure and
unaccounted helper paths, then a queued-background dispatch race. The fixes
made uncertain durability fail closed, routed or suppressed every current
provider entrypoint, and added the shared publication/dispatch boundary. The
final independent review reported no P0, P1, or P2 finding. Final repository
gates are recorded in the root PLAN closeout entry.

## Rollback And Remaining Scope

One squash revert removes Goal provider accounting and restores the P24.2a
state-machine boundary. Because version-2 Goal records are intentionally
rejected by older readers, rollback must first confirm that no unfinished
version-2 Goal needs to be resumed or explicitly retire that Goal.

Automatic continuation, tool and command control, cross-entrypoint
projection, runtime-item integration, and UI display remain outside P24.2b.
Root `PLAN.md` must complete a new intake before any later P24 slice becomes
executable.
