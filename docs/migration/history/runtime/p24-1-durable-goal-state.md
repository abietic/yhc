# P24.1 Durable Goal State

**Created:** 2026-07-28
**Completed:** 2026-07-28
**Status:** historical
**Adoption:** `adapt`

> **Ownership:** completion evidence for P24.1. Current Goal state and recovery
> behavior belongs in
> [`query-engine.md`](../../../architecture/runtime/query-engine.md),
> [`sessions.md`](../../../architecture/state/sessions.md), and
> [`recovery.md`](../../../architecture/runtime/recovery.md). Executable order
> belongs in [`migration/PLAN.md`](../../PLAN.md).

## User Problem

A saved root Session had no typed, versioned Goal record. Later cross-turn
continuation could therefore not bind objective, lifecycle revision, status,
budget placeholders, or recovery behavior to one durable engine-owned state.

P24.1 closes only that prerequisite. It adds no QueryEvent, model tool, slash
command, runtime item, automatic continuation, provider admission/accounting,
transport capability, or UI. A persisted Goal cannot start work.

## Delivered Contract

One saved root Session may own one unfinished Goal. The internal QueryEngine
service creates, edits, pauses, resumes, changes a budget, or clears it under
these rules:

- mutations serialize under `planMu -> goalMu -> QueryEngine.mu`;
- each mutation validates a detached candidate, persists and flushes the
  complete Session checkpoint, and only then publishes the new live revision;
- a failed checkpoint leaves the previous live Goal unchanged;
- active Goal and active or awaiting-approval Plan state are mutually
  exclusive without implicit pause, resume, exit, or approval;
- ephemeral, child, review, and administration-only engines cannot create or
  mutate a Goal, and a fork never inherits the source Goal;
- active requires a positive remaining token budget, while an omitted budget
  creates only a paused draft; and
- objective, reason, blocker key, status transition, revision, and blocker
  evidence constraints fail without mutation.

`SessionMetadataFull.goal_state` is an additive version-1 nested record. Older
readers ignore it and retain ordinary Session behavior. A supported cold
`active` record advances once to `paused` and checkpoints before activation
because P24.1 has no revision-bound continuation cursor. Paused, blocked,
usage-limited, and terminal records remain inert.

Unknown versions, malformed-but-valid nested JSON, and semantically corrupt
records leave the enclosing Session readable. The engine exposes no available
Goal, preserves the unavailable record across unrelated checkpoints, and
permits only explicit clear.

## Restore And Failure Semantics

Restore staging may need to persist both runtime-input recovery and the
Goal-bearing transcript checkpoint. They are separate monotonic durable
owners, not an atomic transaction. Before commit begins, abort is
mutation-free. After commit begins, staging is one-way and retry-only: a
partial failure cannot claim successful abort or publish the Session, and a
retry or the next process restore converges the remaining write.

ACP Load and inactive Resume validate and deliver their required output before
staging commit, register the Session only after commit completes, and close the
engine on delivery or commit failure. A failed command delivery therefore
leaves no live registration or process-local restore owner while the durable
paused Goal converges on retry.

## Evidence

The implementation and focused fixtures are owned by:

- [`goalService` and the internal Goal state machine](../../../../engine/goal_state.go);
- [persisted conversion, validation, and cold normalization](../../../../engine/goal_persistence.go);
- [bounded scalar and blocker-key validation](../../../../engine/goal_validation.go);
- [`SessionMetadataFull.goal_state` and fork isolation](../../../../engine/session/branch.go);
- [complete Plan/Goal/Session checkpoint sampling](../../../../engine/session_checkpoint.go);
- [restore-staging commit and retry ownership](../../../../engine/restore_staging.go); and
- [ACP failure cleanup and lifecycle fixtures](../../../../server/acp/replay_test.go).

Focused tests cover every transition and invalid-input no-op, Plan exclusion,
saved-root scope, concurrent mutations, checkpoint/live coherence, persistence
failure, old-reader behavior, fork isolation, cold active normalization,
terminal and limited states, unknown/corrupt/malformed records, explicit clear,
cross-owner partial commit plus retry, ACP delivery failure, and restart
convergence. Focused race tests and the repository, documentation, manifest,
scanner, and diff gates passed before merge.

Independent lifecycle review first found missing staged persistence for cold
normalization, then the false-abort risk across two durable owners, and finally
the ACP cleanup leak. The corrected retry-only commit state and close-on-
failure integration passed re-review with no remaining finding.

## Compatibility And Rollback

The nested Goal record is additive. A rollback binary ignores it and continues
ordinary explicit-turn Session behavior; it neither resumes nor deletes Goal
state. A newer binary treats unsupported or corrupt state as unavailable and
activation-free.

One squash revert removes the internal state/service, nested checkpoint field,
and restore integration without a transcript migration. It does not change
existing messages, runtime-input items, Plan state, permission authority,
provider routing, entrypoint discovery, or UI behavior.

P24.2a and every later P24 slice remain queued. Root `PLAN.md` must run a new
intake before any Goal event, read projection, accounting, continuation, tool,
command, transport, or UI becomes executable.
