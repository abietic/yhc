# Compaction

**Status:** current
**Last verified:** 2026-11-09

> **Ownership:** canonical ProjectGraph preparation/reconciliation ordering;
> `engine/compact` transformations

## Current Compaction Path

Compaction is a set of bounded message transformations used before a model call
and during overflow recovery. `engine/compact` supplies the transformations;
the canonical round lifecycle decides their order, records boundaries, invokes
hooks, and installs the resulting message state. `QueryEngine` then appends one
durable lifecycle record for each completed automatic or manual compaction.
P30.3 media recovery reuses the same transcript lifecycle kind for one
historical-media active-context projection, but not the ordinary compaction
pipeline or its retry counters.

## Current pre-model order

1. Keep messages after the latest compact boundary.
2. Apply the general tool-result budget.
3. Resolve the preparation-time runtime model. Run snip only with at least 40
   messages and at that model's auto-compact threshold; emit a boundary when
   the transformation is material.
4. Run microcompaction: keep the existing idle-time clearing policy, then trim
   consumed history only at the preparation-time model's auto-compact threshold.
5. Apply staged context collapses at 80% of that model's auto-compact threshold.
6. Run pre-compact hooks and optional auto-compaction.
7. On auto-compaction, run post-compact hooks, build post-compact messages,
   clean up, and re-inject file/plan context.
8. Enforce the hard blocking limit, with one reactive-compaction attempt before
   surfacing a prompt-too-long terminal.

The execution-time content-replacement budget runs again immediately before the
model call because it is stateful and records newly persisted replacements.

## Invariants and edge cases

- The original input slice must not be mutated in place. A recovery stage counts
  as progress only when it returns a non-empty material transformation.
- Compact boundaries are semantic history markers; consumers and transcripts
  must observe them in the same order as the replacement messages.
- A completed compaction appends exactly one durable `compact-boundary`
  containing the complete active messages, replacements, and file-state
  projection. Earlier JSONL records remain audit history.
- A P30.3 historical-media projection appends and fsyncs one
  `compact-boundary` before active-state mutation, recovery events, or retry.
  Its bounded system marker distinguishes media recovery; it does not rewrite
  earlier ref-backed prompt records or make an attempt-local derivative
  durable.
- Tool-call/tool-result pairs must remain valid across truncation and grouping.
- User image, audio, video, and file blocks are replaced with bounded modality
  placeholders before summary-model calls; the original messages remain
  unchanged. Tool-result replacement and token transforms are likewise bounded,
  and repeated failure must terminate rather than loop indefinitely.
- Pre-compact cancellation skips that auto-compaction attempt. It does not
  silently disable hard-limit enforcement.
- The summary model is optional. Without a usable summary path, cheap
  transformations and terminal recovery still apply.
- Deterministic automatic compaction, reactive prompt-too-long recovery, and
  stateless overflow drain retain every real user request in the active history
  with its original role, text, and order. They also retain the recent tail,
  expanding it to include owners of retained tool results. Historical metadata
  is not pinned as a request, and retired history is not reloaded. Provider
  compatibility normalization may still combine adjacent user messages while
  retaining their text in order. Model-backed summaries keep their existing
  policy.
- These deterministic transformations decline when the result would not reduce
  estimated tokens. Large user requests therefore establish an irreducible
  context floor; hard-limit enforcement can terminate instead of dropping
  requirements to report recovery.
- Microcompaction does not normalize whitespace in message content or reasoning:
  indentation and spaces inside literals carry meaning. Explicit policies for
  long tool results, thinking blocks, and media remain bounded transformations.
- Standard microcompaction uses the same model context capacity and configured
  threshold overrides as auto-compaction. Below that threshold, it preserves
  old tool output and reasoning rather than rewriting cached prefixes every
  round. Under pressure, its savings target is the excess over that threshold;
  the latest unconsumed tool round and signed reasoning remain protected. The
  separate per-result budget, idle-time clearing, and overflow recovery still
  apply; preserving a prefix does not disable those bounds.
- Preparation resolves the runtime model, including existing Plan selection,
  before measuring pressure. A matching committed binding supplies its context
  capacity; another selected profile uses the side-effect-free inventory.
  Profile names are not treated as API model names. Snip, microcompaction,
  collapse, auto-compaction, and blocking checks share that detached capacity,
  with existing user overrides allowed to narrow it. Without authoritative
  metadata, known routes use their actual API model's capability lookup and
  unknown routes retain the conservative fallback. A profile's display name
  cannot supply API capabilities. Dispatch and compaction guards retain the original
  selector and re-evaluate the existing routing policy after transformations;
  pressure calculation does not create a client or pin a route.
- An API-name compatibility alias can reuse a valid committed binding only
  after selected inventory profiles take precedence. If strict lookup fails,
  a changed or unavailable namespace cannot establish that the alias has no
  same-name profile. Resume admission and clearing a context-only block also
  apply the user's narrowing override to authoritative route limits.
- Snip's message-count minimum alone cannot trigger history rewriting. Snip,
  microcompaction, and collapse all receive the preparation-time model and
  honor the existing context-window and threshold overrides. Unknown models
  retain the conservative capacity fallback. Explicit `Snip` and overflow
  drain remain available independently of the proactive pressure gate.

## Small example

If the prepared history crosses the auto-compact threshold, the round lifecycle
emits compact-boundary messages and continues from `BuildPostCompactMessages`
plus reinjected project context. After the round result returns, `QueryEngine`
appends one fsynced transcript `compact-boundary`. Restart therefore selects the
same compacted active context without deleting the older audit records.

For media-size recovery, restart likewise selects the committed projected
active context. It does not rehydrate omitted historical images from older
physical prompt records or repeat the boundary.

## Code references

- [Canonical round message preparation](../../../engine/round_lifecycle.go)
- [Durable automatic-compaction boundary](../../../engine/engine.go)
- [Manual-compaction command commit](../../../engine/command_executor.go)
- [`GetMessagesAfterCompactBoundary`](../../../engine/compact/boundary.go)
- [`ApplyToolResultBudget`](../../../engine/compact/budget.go)
- [`SnipCompactIfNeededForContextWindow`](../../../engine/compact/snip.go)
- [`MicrocompactForContextWindow`](../../../engine/compact/micro.go)
- [`ApplyCollapsesForContextWindow`](../../../engine/compact/collapse.go) owns
  the capacity-aware production collapse projection. The model-name helper
  entrypoints retain their existing fallback behavior.
- [`SanitizeMediaForCompaction`](../../../engine/compact/strip.go)
- [`AutoCompact`](../../../engine/compact/auto.go)
- [`splitDeterministicContext`](../../../engine/compact/requirements.go) owns
  request retention and complete recent tool groups.
- [Deterministic compaction restart test](../../../engine/query_compact_requirements_test.go)
  checks durable request retention through the next provider call.
- [`BuildPostCompactMessages`](../../../engine/compact/auto.go)
- [`ApplyPostCompact`](../../../engine/compact/post_compact.go)

## Related tracking

Recovery ordering is in [`recovery.md`](recovery.md). Reference and gap
tracking remain canonical in [`../reference/`](../../migration/reference) and
[`migration/REMAINING.md`](../../migration/REMAINING.md).
