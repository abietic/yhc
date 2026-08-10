# P30.1b Strict Legacy Image Admission

**Status:** historical
**Completed:** 2026-07-29
**Last verified:** 2026-07-29

> **Ownership:** completed P30.1b `project-native` decision, strict legacy
> image-admission envelope, provenance containment, compatibility consequence,
> verification, and rollback. Current behavior belongs in
> [`input-queue.md`](../../../architecture/runtime/input-queue.md) and
> [`model-providers.md`](../../../architecture/platform/model-providers.md);
> later ordered input and durable media work belongs in
> [`p30-cross-entrypoint-multimodal-input.md`](../../plans/p30-cross-entrypoint-multimodal-input.md).

## Outcome

Direct `SubmitMessageWithImages`, busy user-input queue admission, durable
runtime-input enqueue/recovery, and safe-point projection now share one strict
engine validator before hooks, turn/model events, transcript mutation, ledger
mutation, or dispatch. Direct submission first copies the caller image slice,
so later caller replacement cannot change the admitted turn.

The version-1 envelope accepts PNG, JPEG, WebP, and single-frame GIF with at
most 20 images, 5 MiB decoded bytes per image, 10 MiB decoded bytes per prompt,
and 25,000,000 pixels per image. Admission requires strict canonical base64, a
normalized supported MIME declaration, declared/detected MIME equality, exact
terminal structure, bounded config decode, complete decode, and overflow-safe
resource accounting.

Malformed, truncated, mismatched, animated, trailing-payload, unsupported, and
over-limit images fail with `UserImageValidationError`. The error exposes only
the zero-based image index and stable reason code.

## Decision And Compatibility

P30.1b used `project-native` within P30's accepted `combine` program. The
project-wide 5 MiB ceiling aligns direct callers with the existing TUI byte
boundary; the aggregate, count, pixel, content, and terminal checks close
resource and polyglot-payload gaps at the QueryEngine owner rather than relying
on a provider decoder.

Valid legacy callers retain the public `UserImage` shape and existing complete
text followed by caller-ordered images. MIME spelling is normalized. Invalid
input that structural checks previously accepted now fails before mutation.
`UserImage.Name` and `Path` remain source-compatible caller fields, but are
cleared before durable storage and are absent from Eino multipart `Extra`,
transcripts, provider input, and errors.

This slice adds no ordered public prompt type, route-capability resolver,
`MediaRef`, media store, durable schema, ACP/TUI rich capability, provider
failover, or recovery retry. P30.1a's first-`media_size` terminal rule is
unchanged.

## Decoder Dependency

Standard-library decoders own PNG, JPEG, and GIF. WebP uses
`golang.org/x/image/webp` from `golang.org/x/image v0.43.0`, the newest release
that preserves the repository's existing `golang.org/x/sync v0.21.0`
constraint. Module resolution upgrades indirect `golang.org/x/text` from
v0.30.0 to v0.39.0. Selecting x/image v0.44.0 would also force the unrelated
x/sync v0.22.0 upgrade, so it was rejected for this atomic slice.

## Verification

Closeout passed the frozen focused, race, repository, documentation, manifest,
and diff gates:

```text
go test ./engine -run 'TestUserImageAdmission|TestSubmitMessageWithImages|TestQueryEngineQueueRejectsInvalidUserImages|TestRuntimeInputCoordinator.*Image|TestP300FlattenedPrompt|TestP301a'
go test ./internal/tui -run 'TestBusyQueuePreservesRichPasteAndImageSnapshot'
go test -race -timeout=20m ./engine/...
make fmt
make lint
make lint-new
make test
make build
make docs-check
make docs-check-ci
go run ./scripts/migration_manifest.go check-ledger
go run ./scripts/migration_manifest.go check
git diff --check
```

The focused fixtures cover all four supported formats; canonical-base64 and
MIME rejection; content mismatch; truncation; exact trailing-payload rejection
for every supported format; animated GIF/WebP rejection; count, per-image,
aggregate, and pixel limits; direct and queued zero-mutation failure; copied
caller input; cleared durable provenance; recovery validation; retained
complete-text-then-images order; and unchanged P30.1a terminal behavior.
The repository suite passed 5,990 tests with only the opt-in physical-terminal
diagnostic skipped.

## Rollback And Next State

Rollback may replace the decoder implementation but must retain equivalent
strict validation and provenance redaction at every current image entrypoint.
It may not restore arbitrary MIME/base64 admission, caller path/name
projection, or strip-current-and-complete recovery.

P30.1c-P30.6 remain accepted but queued, and G32 remains open. No successor
became `Ready` automatically; root `PLAN.md` must select the next slice
separately.
