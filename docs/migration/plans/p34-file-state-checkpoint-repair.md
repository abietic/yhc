# P34 File-State Checkpoint Repair

**Status:** historical
**Accepted:** 2026-07-31
**Completed:** 2026-07-31
**Adoption:** `preserve`

> **Ownership:** accepted atomic contract for preserving file-interaction state
> when its incremental transcript append fails after a successful file tool.
> Root [`PLAN.md`](../PLAN.md) alone owns live execution order.

## Outcome

P34.1 is complete. Read, Edit, and Write still update the cumulative
`FileStateCache` and attempt the existing append-only snapshot. A failed
incremental append now reports a turn-local, concurrency-safe repair
requirement to QueryEngine without changing the successful tool result or
repeating its side effect. The next safe checkpoint writes one complete
`state-checkpoint`; only successful full repair clears the requirement.

Deterministic engine, recorder, restart, explicit resume, terminal-path,
partial-line, and repeated race tests prove the frozen contract. Delivery and
reproduction evidence is in
[`p34-1-file-state-checkpoint-repair.md`](../history/runtime/p34-1-file-state-checkpoint-repair.md)
and
[`p34-1-file-state-checkpoint-repair.md`](../verification/p34-1-file-state-checkpoint-repair.md).

## Problem

A successful Read, Edit, or Write mutates the in-memory `FileStateCache`, then
`maybeRecordFileState` appends a cumulative `file-history-snapshot`.
`RecordFileHistorySnapshot` can return an encode, open, or write error, but the
caller discards it. A later ordinary message checkpoint can append and flush
successfully without knowing that the file snapshot never reached the
transcript.

Restart then restores the messages but loses the affected read/edit/write
provenance. This is a data-integrity mismatch on every supported
QueryEngine-backed entrypoint. The earlier G1 explanation blamed
`ReplaceWithReplacements`; current source disproves that mechanism because the
production checkpoint path does not call the compatibility rewrite API.

Comparative and current-source evidence is in
[`file-state-checkpoint-recovery-audit.md`](../reference/runtime/file-state-checkpoint-recovery-audit.md).

## P34.1 Atomic Slice

**Status:** `Complete`

P34.1 preserves the current transcript schema, append-only ordinary
checkpoint, complete lifecycle boundary, and QueryEngine checkpoint owner. It
adds one typed, turn-local handoff from incremental file-snapshot persistence
to that owner.

When an incremental snapshot append fails:

1. the successful tool result remains successful because the filesystem action
   has already occurred;
2. the exact turn marks its transcript projection incomplete through a
   concurrency-safe, monotonic repair signal;
3. no ordinary message append may clear that signal or claim the affected
   active state complete;
4. the next safe checkpoint writes a complete `state-checkpoint` containing
   current messages, content replacements, file state, and cumulative provider
   usage;
5. successful full repair clears the turn-local requirement; failed or
   durability-uncertain repair produces the existing terminal persistence
   failure and retains the engine-level checkpoint requirement.

The signal may be carried by a narrow QueryDeps callback or an equivalent
typed outcome, but it must reach the QueryEngine checkpoint decision without
changing model-visible tool-result bytes.

## Scope

P34.1 may change:

- `engine/tool_execution.go` for checked snapshot persistence;
- the narrow `QueryDeps` or canonical tool-round handoff needed to report an
  incomplete transcript generation;
- `engine/engine.go` for concurrency-safe turn-local repair selection and
  checkpoint settlement;
- focused transcript, engine, concurrency, and entrypoint tests;
- current transcript architecture, `STATUS.md`, `REMAINING.md`, root
  `PLAN.md`, and one verification/history record at closeout.

The behavior applies to TUI, Plain, ordinary headless, ACP, and parent or child
QueryEngines that execute Read, Edit, or Write with a transcript recorder.
Standalone MCP remains outside scope because it owns no QueryEngine Session
transcript.

## Non-Goals

P34.1 does not:

- persist file contents, hashes, permissions, or rollback versions;
- construct `filehistory.FileTracker` or enable `/rewind`;
- change Read, Edit, or Write permission, execution, result, or hook semantics;
- redesign `Replace`, `ReplaceWithReplacements`, `AtomicReplace`, Session
  branching, transcript schema, compaction, or media persistence;
- retry a failed side-effecting tool or report that successful tool as failed;
- add a background writer, second checkpoint owner, timer, queue, or durable
  sidecar.

## Frozen Invariants

### Ordering and authority

1. File tool execution succeeds before the cache is updated.
2. The cache update precedes the incremental snapshot attempt.
3. Snapshot failure marks repair before the tool result can reach the next
   transcript checkpoint decision.
4. All committed tool calls in a concurrent round settle before the
   model-ordered results are checkpointed; any one snapshot failure requires
   one complete repair.
5. QueryEngine remains the only owner that decides append versus full
   checkpoint. Tool execution may report incompleteness but cannot write or
   publish a replacement active state itself.

### Persistence and replay

1. A successful incremental snapshot plus later `Flush` retains current
   append-only behavior and emits no full checkpoint.
2. A failed incremental snapshot is never normalized into success merely
   because later message append or `Flush` succeeds.
3. Complete repair snapshots messages, replacements, file state, and usage
   from one safe-point generation and uses the existing fsync commit.
4. Partial-line repair remains recorder-owned. A failed append followed by a
   full repair cannot leave a malformed suffix selected as active state.
5. Restart after successful repair reconstructs every cumulative read/edit/
   write flag. Restart after failed repair remains authoritative and the live
   turn reports persistence failure instead of claiming completeness.

### Concurrency and failure

1. The repair signal is monotonic until a successful full checkpoint and is
   safe under concurrent file-tool completion.
2. No recorder callback acquires QueryEngine state locks while holding the
   recorder mutex.
3. No QueryEngine mutex is held across transcript I/O or sync.
4. Multiple snapshot failures coalesce into one full repair requirement.
5. Cancellation, interruption, hook stop, max turns, and terminal model errors
   still run the final checkpoint path and cannot bypass an outstanding repair.

### Compatibility

- Successful file tools keep their current model-visible result and side
  effects.
- Successful ordinary append checkpoints keep their existing JSONL shape and
  entry identities.
- Existing transcripts and readers need no migration.
- Compatibility rewrite APIs retain their documented explicit replacement
  semantics and are not used as a repair mechanism.

## Deterministic Proof

Focused tests must cover:

- successful Read, Edit, and Write snapshots followed by an ordinary append
  checkpoint and restart without a full boundary;
- one injected snapshot encode/write failure followed by a successful message
  append, proving the turn chooses a complete checkpoint before settlement;
- injected snapshot failure plus full-checkpoint failure, proving terminal
  persistence error and retained repair requirement;
- partial-line/durability-uncertain failure followed by recorder suffix repair
  and exactly one complete boundary;
- two or more concurrent file-tool completions with one or multiple snapshot
  failures, proving one repair with cumulative flags and no race;
- hook stop, interruption, max turns, and model-terminal paths with an
  outstanding repair;
- initial construction and explicit Session resume reconstructing repaired
  state;
- unchanged tool-result bytes and no tool re-execution;
- no production call from the repair path to compatibility rewrite APIs.

Final verification requires:

```bash
make fmt
make lint
make lint-new
make test
make build
make docs-check
make docs-check-ci
git diff --check
```

Focused iteration should include race repetition for the engine and transcript
packages and a deterministic injected writer failure rather than timing or
filesystem-pressure assumptions.

## Promotion and Rollback

P34.1 was promoted after the source audit reproduced the ignored incremental
failure and independent review rejected the earlier unconditional closure. It
is now historical and no longer owns live queue order.

Rollback removes the repair handoff and turn-local signal as one unit. It does
not rewrite existing transcripts or remove lifecycle records. The rollback
restores the current known risk that an incremental snapshot failure can be
lost behind a later successful ordinary message checkpoint; G1 must reopen if
that rollback occurs.
