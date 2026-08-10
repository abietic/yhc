# P30.3 Media-Size Recovery

**Status:** historical
**Completed:** 2026-07-30
**Last verified:** 2026-07-30

> **Ownership:** completed P30.3 `project-native` decision within P30's
> accepted `combine` program, exact-turn historical projection, Recovery
> Profile v1, durable activation, rich fallback admission, verification, and
> rollback. Current behavior belongs in
> [`recovery.md`](../../../architecture/runtime/recovery.md),
> [`model-providers.md`](../../../architecture/platform/model-providers.md),
> [`compaction.md`](../../../architecture/runtime/compaction.md), and
> [`transcripts.md`](../../../architecture/state/transcripts.md).

## Outcome

One exact rich logical round can now recover from a provider `media_size`
result without removing media from the current question or rewriting canonical
media. Only recorder-proved historical image parts become visible ordered
markers. Current-turn raster images remain in canonical state and may receive
deterministic, strictly smaller, attempt-local derivatives in the provider-call
clone.

The sequence is bounded to the original selected-route call, one selected-route
recovery call, and one distinct exactly eligible fallback. A current-media
failure either obtains a normal rich response or ends with a redacted image
error; it cannot synthesize a text-only completion.

## Decision And Compatibility

P30.3 used `project-native` within P30's accepted `combine` program and retained
the existing owners:

- QueryEngine/ProjectGraph owns the logical round, counters, events, and
  terminal;
- transcript owns the fsynced active-context transition;
- prompt records and their exact materialized-message bindings own turn
  identity;
- MediaStore remains the only canonical ref/blob authority;
- selected-route prompt admission owns rich capability provenance and
  generation; and
- provider runtime owns model resolution, construction, and dispatch.

No second conversation store, provider adapter, model registry, durable
derivative cache, background retry, or general portfolio was added. Text-only
recovery and ordinary overload fallback keep their existing behavior. P30.1a
terminal-on-first-failure remains the rollback baseline.

## Exact Projection And Commit Order

Submission freezes the exact current `TurnID`, original rich message, ordered
part signature, and selected route generation. Historical eligibility requires
a recorder-owned prompt record with a valid different turn identity in exact
active order. Content equality, last position, legacy inline media, malformed
or duplicate records, and reordered projections cannot prove eligibility.

Each eligible historical image becomes exactly one bounded text marker at the
same part position. All other parts retain their order and bytes in the
canonical projection. Before that projection becomes active, a configured
recorder appends and fsyncs one `compact-boundary` with bounded counts and a
system recovery marker:

```text
build exact candidate
  -> append and fsync lifecycle boundary
  -> replace active messages
  -> emit compact boundary and attachment
  -> retry provider
```

A persistence failure makes no retry or recovery event. Cancellation before
commit leaves active state unchanged; cancellation after commit retains the
truthful projection and prevents later events and provider calls. Restart
selects the committed projection once without rehydrating old media or
duplicating the boundary.

## Recovery Profile V1 And Fallback

The version-1 image profile accepts JPEG, PNG, and static WebP. It never
upscales, bounds the long edge to 2048 and pixels to 4,194,304, uses
CatmullRom resampling, and encodes opaque output as JPEG quality 85 or
alpha-bearing output as best-compression PNG. Strict reinspection must succeed,
and output must be smaller than its canonical source and within the existing
blob ceiling. GIF and any unsupported or non-beneficial conversion is
ineligible.

Derivative bytes exist only in a deep provider-call clone. They receive no
durable ref or public identity and are cleared after the attempt. Prompt
records, runtime-input items, refs, manifests, blobs, and original message
objects remain unchanged.

After the selected recovery call, fallback requires a newly resolved distinct
provider/model, explicit `supported` current-modality capability with non-empty
source and current generation, fresh exact ordered prompt admission, and a
second route check immediately before dispatch. Generic overload fallback and
call-level fallback options are disabled during this sequence. Unsupported,
unknown, stale, reordered, text-only, or already attempted candidates make
zero alternate calls.

## Verification

Closeout ran and passed the frozen focused suites, full engine race gate,
Windows cross-compilation, repository gates, documentation/ledger gates, and
diff validation:

```text
go test ./engine/internal/mediaimage -run 'TestP303|TestRecoveryDerivative'
go test ./engine/recovery -run 'TestP303|TestMediaRecovery'
go test ./engine/transcript -run 'TestP303|TestLifecycleBoundary|TestPromptRecord'
go test ./engine -run 'TestP303|TestQueryMedia|TestPromptInput|TestRoute'
go test ./engine/provider -run 'TestP303|TestUserInput'
go test -race -timeout=20m ./engine/...
GOOS=windows GOARCH=amd64 go test -c ./engine/internal/mediaimage
GOOS=windows GOARCH=amd64 go test -c ./engine/recovery
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

Fixtures cover deterministic output and alpha preservation; strict profile
bounds and cancellation; exact current/historical separation; marker order and
redaction; resumed prompt-record bindings; one durable boundary before retry;
canonical ref/blob and source-message immutability; exact `1 + 1 + 1` calls and
one logical usage round; fresh fallback resolution and dispatch guard;
ineligible fallback with zero alternate calls; persistence failure; and
cancellation immediately after commit.

## Rollback And Next State

Rollback first disables the P30.3 projection and eligible fallback, returning
every `media_size` result to P30.1a's terminal image error. It retains exact
turn identity, redacted classification, lifecycle readers, prompt records,
MediaStore, and every boundary already committed. A committed historical
projection remains active and cannot be silently rehydrated or deleted.

P30.4-P30.6 remain accepted but queued, G32 remains open, and no successor
became `Ready` automatically. Root `PLAN.md` must promote one slice in a
separate iteration.
