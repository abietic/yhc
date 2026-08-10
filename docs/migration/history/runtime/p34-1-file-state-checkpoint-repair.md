# P34.1 File-State Checkpoint Repair

**Status:** historical
**Closed gaps:** G1
**Completed:** 2026-07-31

> **Ownership:** completion evidence for preserving successful file-tool
> semantics while repairing an incomplete incremental file-state transcript
> generation before the turn claims durable settlement.

## Outcome

P34.1 completed the accepted `preserve` contract and closed G1. Read, Edit, and
Write keep their existing side effects, model-visible results, hooks, and
permissions. A changed `FileStateCache` still attempts the same cumulative
`file-history-snapshot`, but the write error is no longer discarded.

The exact QueryEngine turn receives a monotonic repair signal after the
incremental recorder call returns. Ordinary message append and `Flush` cannot
clear it. The next safe checkpoint writes one complete `state-checkpoint`
containing current messages, replacements, file state, and cumulative provider
usage. Successful full repair clears the signal; failed or
durability-uncertain repair emits the existing terminal persistence error and
retains the engine-level checkpoint requirement.

## Ordering and Concurrency

QueryEngine remains the only append-versus-full-checkpoint owner. The tool path
updates the cache before attempting its snapshot and only reports
incompleteness; it never publishes a replacement active state. QueryEngine
copies its recorder or test writer while holding its state lock and releases
that lock before transcript I/O.

The production round owner settles all committed tool calls before publishing
their model-ordered result checkpoint decisions. Concurrent file completions
therefore set one turn-local atomic requirement, whether one or every
incremental append fails. One later successful incremental snapshot does not
erase an earlier failure, and the complete checkpoint contains the cumulative
cache generation.

## Recovery and Proof

Focused tests cover the unchanged successful append path for Read, Edit, and
Write; injected durability uncertainty; exact tool-result bytes and
single execution; one-failure/one-success and all-failure concurrent rounds;
partial JSONL suffix repair; complete-checkpoint failure; completed, hook-stop,
interrupt, max-turn, and model-error terminals; constructor restart; and
explicit Session resume. Reproducible commands and source gates are in
[`p34-1-file-state-checkpoint-repair.md`](../../verification/p34-1-file-state-checkpoint-repair.md).

An independent persistence/concurrency review found no production defect after
the repair. Its requested durability-uncertain, explicit-resume, and mixed
concurrent-outcome proofs were added before closeout.

## Compatibility and Rollback

Existing transcript schema, readers, append-only ordinary checkpoints,
lifecycle boundaries, entry identities, and compatibility rewrite APIs remain
unchanged. Standalone MCP still has no QueryEngine Session transcript, and
P34.1 does not add file contents, hashes, rollback versions, or `/rewind`.

A squash revert removes the typed handoff and turn-local repair signal as one
unit without rewriting existing transcripts. It restores the known G1
data-integrity risk and must reopen that gap.
