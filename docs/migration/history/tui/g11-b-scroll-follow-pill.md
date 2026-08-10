# G11.B Scroll Follow And Jump Pill

**Status:** historical
**Completed:** 2026-07-26

> **Ownership:** G11.B delivery result, transition and hydration evidence,
> compatibility consequences, verification, and rollback boundary

## Outcome

The accepted `combine` slice replaces `ChatView.follow` plus
`scrollAwayCount` with one presentation-only value containing following state,
a saturating monotonic live append epoch, the first departure baseline, and
explicit baseline validity. `ChatView` is the sole owner; no engine event,
runtime reducer, transcript record, durable schema, permission, provider,
Eino, or replay contract changed.

Every effective line/page/wheel/top/item/search departure snapshots the
baseline once. Live top-level user, assistant, thinking, tool, system,
compact-boundary, interruption, compact-summary, help, and semantic history
appends advance the epoch once. Mutation, finalize, tool grouping, expansion,
theme/render/cache work, truncation, reset, and hydration do not advance it.
Empty, non-scrollable, zero-height, zero/negative-distance, and invalid-target
operations cannot manufacture an away state.

## Projection And Interaction Boundary

The exported `AppendHistoryItem` remains source-compatible and explicitly
means live history. Agent transcript projection uses a named internal hydration
entry; engine and Agent detail reconstruction execute under an explicit
hydration intent. Same-process thread switching preserves the `ChatView`
pointer and complete presentation state. Durable sidecar and Agent projection
replacement preserve away intent but deliberately invalidate the unseen
baseline, exposing a count-free `Jump to bottom` action.

One `ChatView` model publishes pill visibility, label, and follow action.
`ChatView.Render` and `App.pillClickHits` consume it, so App no longer
reconstructs count or label. Modal, expand, and sidebar routing still preempt
the background hitbox. Styled width and centered columns remain on the existing
Lip Gloss adapter until G11.D3 migrates geometry to the App-selected profile.

## Verification

Focused evidence covers line/page/wheel/top/item/search transitions, first
departure, zero/one/many counts, grouping, mutation, reset/truncation,
empty/non-scrollable/zero-height/no-op inputs, exact-bottom recovery,
40/80/120/150/180 widths, pill clicks, modal precedence, in-memory thread
switching, durable restoration, and Agent projection replacement:

```text
go test ./internal/tui -run '^TestG11B' -count=1
go test -race ./internal/tui -run '^TestG11B' -count=1
go test ./internal/tui -count=1
```

Repository closeout uses:

```text
make fmt
make lint
make test
make build
make lint-new
make docs-check
go run ./scripts/migration_manifest.go check
git diff --check
```

## Compatibility And Rollback

The only visible compatibility change is intentional: every nonempty away view
now retains a textual recovery action, and unseen labels count live append
events rather than current projection length. Grouping can no longer reduce a
count, and restored projections do not claim an invented count.

Rollback must revert the follow-state helper, explicit append/hydration paths,
semantic pill model, transition routing, and tests together. Never retain the
legacy sentinel beside the new epoch or add an engine-owned fallback. Current
architecture belongs in
[`architecture/tui/README.md`](../../../architecture/tui/README.md); future
geometry work belongs in [`migration/PLAN.md`](../../PLAN.md).
