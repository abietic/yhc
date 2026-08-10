# Recovery

**Status:** current
**Last verified:** 2026-08-07

> **Ownership:** canonical ProjectGraph lifecycle recovery transitions;
> `runCanonicalModelRound` model retry/fallback execution; `engine/recovery`
> bounded decisions; and QueryEngine restore staging plus Goal cold
> normalization for Session activation

## Current Recovery Cascades

Recovery converts model/API failures into explicit retry, transformation, or
terminal transitions. `runCanonicalModelRound` now owns production
same-route retry and the overload-only ordered profile coordinator for
ProjectGraph. The package provides classifiers and bounded cascades; canonical
after-model reconciliation owns prompt-too-long, media, and max-output-token
counters and decides when recovered messages replace round state. Media-size
recovery is bound to one exact current turn and one logical round. It may
commit one historical-media projection, prepare deterministic attempt-local
current-image derivatives, and use one freshly admitted rich fallback route.
It never strips current-turn media or rewrites canonical prompt records and
blobs.

P13.4 historically proved the same policy boundary against Eino v0.9.12 ADK
fixtures. P13.6b retired those construction and attempt adapters after the
project-owned staged Graph rollout demonstrated that production recovery needs only
`runCanonicalModelRound`, `CallModelWithRetry`, and `RecoveryManager`.

## Ordered cascades

### Overload retry and profile failover

`CallModelWithRetry` keeps 429 and bounded overload retry on the same profile.
After the overload ceiling, only the typed `overloaded` class returns to the
logical-request coordinator. The coordinator may start the next constructable
ordered profile under one provider-call, switch, and absolute-deadline budget.
All other failure classes are terminal or pre-dispatch skips.

Before resolving candidates, the coordinator admits the complete immutable
request footprint: normalized messages, system prompt, and serialized tool
schemas including `ToolInfo.Extra`. This is a conservative context-fit
estimate, not billing or an output reservation. Every dispatch restores those
immutable inputs and the exact reasoning intent; a route that cannot lower the
frozen effort is skipped before dispatch.

Failed output remains attempt-local; provider usage is still settled, and the
deferred model round cannot execute a tool. After a constructable alternate is
confirmed, the old attempt emits `discarded`, followed by an exact tombstone
only for offered retractable TUI output, then the next `started` before
dispatch. A switched attempt does not also emit `failed`. Plain/headless, ACP,
and default library projection cannot switch after visible assistant output.
The detailed attempt and entrypoint contract belongs to
[`model-providers.md`](../platform/model-providers.md#bounded-overload-failover).

P30.3 media recovery disables this generic coordinator because it owns a
separate exact `1 + 1 + 1` sequence. Legacy `fallback_model` otherwise compiles
into the same overload-only coordinator. There is no last-success stickiness,
transport switching, Retry-After cooldown, adaptive health, or background
probe.

### Prompt too long

1. Commit eligible staged collapses and retry.
2. Try reactive compaction once and retry only if it returns messages.
3. Surface `prompt_too_long`.

### Media size

1. The initial selected-route call uses the original canonical messages.
2. On `media_size`, validate the exact current-turn binding and recorder-owned
   prompt records. Replace only proved historical images with bounded
   in-position markers and prepare Recovery Profile v1 derivatives only in a
   deep provider-call clone.
3. If the candidate made no material change, terminate as `image_error`.
4. Before activating a historical projection, append and fsync one
   `compact-boundary`. Only then replace in-memory active messages, emit the
   compact/attachment events, and retry the selected route once.
5. If that call also returns `media_size`, resolve and freshly admit at most
   one distinct fallback route. The exact provider/model, capability source,
   generation, and current ordered modalities must still match immediately
   before the call.
6. Any persistence, identity, preparation, capability, cancellation, stale
   route, or exhaustion failure terminates with a bounded redacted result.

The sequence is bounded to `1 + 1 + 1` provider calls: original, selected-route
recovery, and one eligible fallback. Generic overload fallback is disabled
inside this sequence so it cannot bypass those counters or reuse admission
from another route.

Historical eligibility comes only from recorder-owned exact prompt-record
bindings with a different ordered `TurnID`. Legacy inline media, malformed or
duplicate records, current-turn mismatch, or reordered projection fails
closed. A historical image becomes exactly one visible marker at its original
part position. Text, tools, assistant output, non-image parts, and current-turn
parts keep their order and bytes in the canonical projection.

Recovery Profile v1 accepts JPEG, PNG, and static WebP, never upscales, bounds
the long edge to 2048 and pixels to 4,194,304, uses CatmullRom resampling, and
encodes opaque output as JPEG quality 85 or alpha output as best-compression
PNG. A result is used only when strict reinspection succeeds and it is smaller
than the canonical source. Derivatives receive no durable ref or public
identity and are cleared after each provider attempt.

P30.1a remains the rollback baseline: disable this bounded recovery and make
the first `media_size` terminal. The older all-image
`compact.TryReactiveCompact` transform remains non-production evidence and is
not a recovery policy owner.

### Ref-backed prompt restore

Resume and immutable replay open the source Session MediaStore and materialize
an ordered `user-prompt` only after every referenced blob passes exact
digest, size, MIME, dimensions, and strict raster validation. Unknown record
versions or part kinds, duplicate/invalid refs, missing/corrupt blobs,
metadata mismatch, over-limit content, and cancellation reject the whole load
before live engine mutation or provider entry. Recovery never substitutes a
placeholder, drops an image, or rewrites the durable record.

Lifecycle checkpoints and explicit transcript rewrites preserve the durable
prompt record and overlay its materialized message only in the live projection.
They do not serialize that Eino message back inline.

The saved runtime-input ledger is a separate durable owner, but P30.2b now
persists rich queued input as the same ref-backed prompt record. Recovery
resolves and admits every ref before changing the exact item from pending to
processing. After a crash, complete transcript delivery coverage removes a
processing item whose same-ref prompt was already appended and synced; an
uncovered item returns to pending. The transcript is flushed before ledger
settlement, and a separate bounded runtime-item delivery identity preserves
that decision without reintroducing materialized message or image bytes.

P30.6 removes the remaining new-write bypasses. Legacy inline queue JSON is
still decoded, sanitized, validated, and resumable, but generic enqueue rejects
that shape and every current rich writer commits a prompt record derived from
typed admission. A restart-plus-media-compaction fixture pins the same queued
prompt refs before claim, while attempt-local derivatives remain outside the
record and bounded by the existing recovery profile.

Inspection and presentation use a separate ref-only projection. Paging,
listing, branching preflight, and sanitized export can preserve typed prompt
order and metadata without opening a blob; they never weaken the strict
resolver used by resume or provider entry.

Media reclamation is manual and active-owner-only. Rich transcript and queue
writers share one QueryEngine media-lifecycle gate from store publication
through durable ref commit; collection takes the exclusive side. It snapshots
every physical transcript prompt ref, including superseded lifecycle
snapshots, plus refs from every coordinator state, then revalidates the exact
transcript object/revision and coordinator revision before pruning. The store
publishes the retained manifest before deleting an unreferenced blob, so
cancellation or a crash can retain an orphan but cannot make a live ref dangle.
No startup, timer, offline, or automatic collection path exists.

### Maximum output tokens

1. When enabled, raise the output cap to the bounded override once.
2. Add the continuation recovery message for a bounded number of retries.
3. Surface `model_error` after exhaustion.

`DetermineRecovery`, `ApplyRecovery`, and `EmergencyTruncate` provide a generic
conversation-recovery library. They have focused tests but no current
production composition-root caller; the live query path uses the three
category-specific `Try*Recovery` cascades above.

## Durable Goal cold recovery

P24.1 recovery never resumes Goal work. A supported saved-root Goal in a
terminal, paused, blocked, or usage-limited state restores inertly. A saved
`active` Goal has no revision-bound continuation cursor in this slice, so cold
restore advances its revision, changes it to `paused`, and durably checkpoints
that normalization before the Session becomes live. Child and review Sessions
discard a nested Goal record because those scopes cannot own one.

Unknown versions, malformed-but-valid nested Goal JSON, and semantically
corrupt records do not make the enclosing Session unreadable. The engine keeps
the Goal unavailable and preserves the record across unrelated checkpoints;
only an explicit clear may remove it.

Restore staging may need to commit both the runtime-input recovery ledger and
the transcript-backed Session checkpoint containing Goal normalization. These
are separate monotonic durable owners, not one atomic store. Once commit starts,
the staging owner enters a retry-only state: failure cannot report a successful
abort or publish the Session, and a retry or the next process restore converges
the remaining write before activation. ACP registers the Session only after
this commit and closes the engine on delivery or commit failure.

## Invariants and edge cases

- Recovery state is query-local and bounded by category. Media projection and
  fallback attempt bits cannot reopen later in the same query.
- Goal recovery is activation-free. A durable Goal record alone cannot enqueue
  input, call a model, emit a Goal event, or claim permission.
- Restore-staging abort remains mutation-free only before commit begins. Once
  either durable owner has committed, the state is one-way and retry-only.
- The original input remains immutable. A callback returning nil or an empty
  message list is not progress and must not trigger another retry.
- A media recovery boundary is the fsynced active-context commit point. Before
  it, cancellation changes no active state; after it, cancellation keeps the
  truthful projection and prevents later events or provider calls.
- Canonical prompt records, refs, manifests, blobs, runtime-input items, and
  original messages are immutable. A derivative exists only in the current
  provider-call clone.
- No current-media failure can publish a text-only substitute or synthesize
  `TerminalCompleted`.
- A ref-backed prompt restore is all-or-nothing and cancellation-aware; an
  unreadable ref cannot expose partial history or enter a provider call.
- A previous collapse-drain transition cannot run the same stage again.
- Context cancellation bypasses retry waits and terminates promptly.
- Fallback routing is provider-aware and handled around the model retry seam.
  Media fallback additionally requires a fresh exact route/capability binding;
  recovery never constructs a provider client directly.
- Raw provider errors are classified and replaced by secret-safe error strings
  before they reach retry, fallback, or terminal projection.
- Recovery visibility is branch-specific: compact boundaries, continuation
  attachments, and terminal events are projected when emitted by the live
  path. Maximum-output-cap escalation currently changes only query-local state;
  it has no recovery-specific public event.

## Code references

- [Prompt-too-long integration](../../../engine/round_lifecycle.go)
- [Media-size integration](../../../engine/round_lifecycle.go)
- [Maximum-output-token integration](../../../engine/round_lifecycle.go)
- [`TryPTLRecovery`](../../../engine/recovery/ptl.go)
- [`handleMediaSizeFailure`](../../../engine/media_recovery.go)
- [`recovery.BuildMediaCandidate`](../../../engine/recovery/media.go)
- [`mediaimage.DeriveForRecovery`](../../../engine/internal/mediaimage/recovery.go)
- [`TryMaxTokensRecovery`](../../../engine/recovery/max_tokens.go)
- [`DetermineRecovery`](../../../engine/recovery/conversation.go)
- [`ApplyRecovery`](../../../engine/recovery/conversation.go)
- [`EmergencyTruncate`](../../../engine/recovery/conversation.go)
- [`CallModelWithRetry`](../../../engine/execution/retry.go)
- [`runCanonicalModelRound`](../../../engine/model_round.go)
- [`newModelAttemptCoordinator`](../../../engine/model_failover.go)
- [`restorePersistedGoalState`](../../../engine/goal_persistence.go)
- [`CommitRestoreStaging`](../../../engine/restore_staging.go)
- [`persistSessionCheckpointMessagesLocked`](../../../engine/session_checkpoint.go)
- [`promptrecord.Materialize`](../../../engine/internal/promptrecord/record.go)
- [`mediastore.Store.Resolve`](../../../engine/internal/mediastore/store.go)
- [`Recorder.LoadRefProjection`](../../../engine/transcript/persist.go)
- [`QueryEngine.CollectSessionMedia`](../../../engine/media_lifecycle.go)
- [`mediastore.Store.Collect`](../../../engine/internal/mediastore/store.go)

## Related tracking

Compaction transforms are documented in [`compaction.md`](compaction.md).
Unresolved parity belongs in
[`migration/REMAINING.md`](../../migration/REMAINING.md).

The frozen contract and remaining rich-entrypoint work are owned by
[`P30 Cross-Entrypoint Multimodal Input`](../../migration/plans/p30-cross-entrypoint-multimodal-input.md).

The retired production strip-and-retry behavior and the retained
non-production transform are reproduced in
[`P30.0 multimodal characterization`](../../migration/verification/p30-0-multimodal-characterization.md).
The fail-closed replacement is recorded in
[`P30.1a terminal media safety`](../../migration/history/runtime/p30-1a-terminal-media-safety.md).
The restart-safe immediate prompt boundary is recorded in
[`P30.2a durable media store`](../../migration/history/runtime/p30-2a-durable-media-store.md).
The ref-only saved-queue and transcript-before-settlement boundary is recorded in
[`P30.2b runtime media refs`](../../migration/history/runtime/p30-2b-runtime-media-refs.md).
Ref-only lifecycle projections, independent branching, sanitized export,
private-migration rejection, and manual reachability collection are recorded
in [`P30.2c Session media lifecycle`](../../migration/history/runtime/p30-2c-session-media-lifecycle.md).
Exact historical projection, Recovery Profile v1, durable activation, and
fresh rich fallback admission are recorded in
[`P30.3 media-size recovery`](../../migration/history/runtime/p30-3-media-size-recovery.md).
The final writer/reader and restart-compaction evidence is recorded in
[`P30.6 multimodal program closeout`](../../migration/history/runtime/p30-6-multimodal-program-closeout.md).
