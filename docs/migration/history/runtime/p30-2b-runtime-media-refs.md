# P30.2b Runtime Media Refs

**Status:** historical
**Completed:** 2026-07-29
**Last verified:** 2026-07-29

> **Ownership:** completed P30.2b `project-native` decision within P30's
> accepted `combine` program, saved busy-queue media durability, exact
> queue/turn identity, claim-time admission, transcript reachability transfer,
> crash settlement, verification, and rollback. Current format behavior belongs
> in [`transcripts.md`](../../../architecture/state/transcripts.md), recovery
> behavior belongs in
> [`recovery.md`](../../../architecture/runtime/recovery.md), and later
> lifecycle, TUI, ACP, and historical-media work remains in
> [`p30-cross-entrypoint-multimodal-input.md`](../../plans/p30-cross-entrypoint-multimodal-input.md).

## Outcome

A rich user turn queued while a saved Session is busy now stores image bytes in
that Session's existing private MediaStore before it commits a bounded
runtime-input ledger item. The ledger contains one strict versioned prompt
record with ordered text and opaque image refs; it contains no image bytes,
base64, digest, path, URI, caller name, or caller metadata. Text-only and valid
legacy-inline runtime records remain readable without an eager conversion.

The queue item ID is the scheduling and settlement identity. The prompt record's
turn ID is the exact user-event and transcript turn identity. Claim resolves all
refs and rechecks the currently selected route before changing the item from
pending to processing. Submission authenticates the processing item, resolves
and admits it again, and uses the same refs for the transcript record. A
process-local lease prevents concurrent duplicate submission.

## Decision And Compatibility

P30.2b used `project-native` within P30's accepted `combine` program. It reuses
the P30.2a MediaStore and prompt-record schema, the existing runtime-input
coordinator, QueryEngine route admission, transcript authority, and P25.1
provider lowering. It does not add a second blob store, queue, replay loop,
capability table, or provider adapter.

Only the engine-bound queue writer can mint a ref-backed runtime envelope.
Direct callers cannot construct a valid bound ref item or clone one under a
different item ID. Existing text-only and legacy-inline records keep their
previous behavior. TUI ordering and advertisement, ACP and Plain/headless
entrypoints, child/Goal surfaces, branch/export/paging, and garbage collection
are unchanged.

## Commit, Claim, And Settlement

The durability order is:

```text
store blobs and synced manifest
  -> commit ref-only runtime-input ledger
  -> resolve refs and admit the current route
  -> mark the exact queue item processing
  -> append and sync the same-ref transcript prompt
  -> settle the exact queue item
```

Media publication failure creates no ledger item. Ledger replacement failure
creates no in-memory item and may retain only an unreachable immutable orphan.
Claim failure leaves the item pending. A pre-transcript route or admission
failure releases the exact item to pending. Once transcript append begins,
durability uncertainty does not release or resubmit the item in-process.

The transcript prompt carries a separate bounded `runtime_item_id` delivery
identity because ref-backed prompt records intentionally do not persist a
materialized Eino message. Restart recovery scans the complete append-only
audit. A processing item covered by a synced transcript prompt is removed;
an uncovered processing item returns to pending. The ledger settles only after
the transcript record is flushed, so a crash cannot silently lose a delivered
turn or inject a covered turn twice.

Cancellation, edit, hook rejection, and pre-ledger failures may leave
unreachable immutable media. P30.2c owns bounded reachability and garbage
collection rather than weakening this slice's store-before-ledger ordering.

## Verification

Closeout uses the frozen focused, race, cross-platform, repository,
documentation, manifest, and diff gates:

```text
go test ./engine -run 'TestP302b|TestRuntimeInputCoordinator|TestQueryEngineQueue|TestSubmitRuntimeItem|TestP302a'
go test ./engine/transcript -run 'TestP302a|TestRuntimeItem|TestLoadFull'
go test ./engine/session -run 'TestP302a|TestDelete|TestExport|TestBranch'
go test ./internal/tui -run 'TestQueue|TestP300ComposerImageDraftOrder'
go test -race -timeout=20m ./engine/... -count=1
GOOS=windows GOARCH=amd64 go test -c ./engine/internal/mediastore
GOOS=windows GOARCH=amd64 go test -c ./engine/transcript
GOOS=windows GOARCH=amd64 go test -c ./engine/session
GOOS=windows GOARCH=amd64 go test -c ./engine
make fmt
make lint
make test
make build
make lint-new
make docs-check
make docs-check-ci
go run ./scripts/migration_manifest.go check-ledger
go run ./scripts/migration_manifest.go check
git diff --check
```

The end-to-end fixture queues a legal multi-megabyte PNG, proves the ledger
stays below 16 KiB and contains only refs, starts a fresh engine, and verifies
the exact provider text/image order, the same transcript media ID, the exact
turn ID on every event, and an empty ledger after settlement. Separate fixtures
cover missing refs, unsafe roots, ledger replacement failure, route drift,
transcript-before-settlement transfer, covered and uncovered restart,
concurrent claim and submission, strict union decoding, and writer sealing.

## Rollback And Next State

Rollback may stop new ref-backed runtime-input writes and reject new rich queue
requests. It must retain the versioned reader, MediaStore, transcript delivery
identity, restart coverage, and lifecycle guards for records already written.
Existing refs and blobs cannot be removed while either the runtime ledger or
transcript can reach them.

P30.2c-P30.6 remain accepted but queued, and G32 remains open. No successor
became `Ready` automatically; root `PLAN.md` must select the next slice
separately.
