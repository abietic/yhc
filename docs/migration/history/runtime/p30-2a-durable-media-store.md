# P30.2a Durable Media Store And Ref-Backed Transcript

**Status:** historical
**Completed:** 2026-07-29
**Last verified:** 2026-07-29

> **Ownership:** completed P30.2a `project-native` decision within P30's
> accepted `combine` program, Session-private image durability, ordered
> transcript prompt identity, immediate rich restart, containment, lifecycle
> rejection, verification, and rollback. Current format behavior belongs in
> [`transcripts.md`](../../../architecture/state/transcripts.md), Session
> orchestration belongs in
> [`sessions.md`](../../../architecture/state/sessions.md), and later queue,
> lifecycle, recovery, TUI, and ACP expansion remains in
> [`p30-cross-entrypoint-multimodal-input.md`](../../plans/p30-cross-entrypoint-multimodal-input.md).

## Outcome

One immediate ordered text/image turn submitted to a saved Session now commits
as private image bytes plus exactly one bounded version-1 `user-prompt`
record. Image bytes are published and synced before the opaque ref becomes
visible; the prompt record is appended and synced before the first user event
or provider call. It contains ordered text and image-ref parts, stable turn and
entry identity, detected MIME, decoded size, dimensions, and image detail. It
contains no bytes, base64, digest, path, URI, caller name, or caller metadata.

A fresh engine resume opens the owning MediaStore and revalidates every image
against the private digest and the same strict complete-raster predicate used
at initial admission. It materializes the exact text/image order only after all
refs resolve. Unknown schema, malformed parts, missing or corrupt blobs,
metadata mismatch, over-limit input, and cancellation fail before live message
mutation or provider entry. Lifecycle checkpoints and compatibility rewrites
preserve prompt records and entry IDs rather than serializing the materialized
Eino message inline.

## Decision And Compatibility

P30.2a used `project-native` within P30's accepted `combine` program. It keeps
QueryEngine as the immediate-turn owner, transcript as conversation-order
authority, P30.1b as strict image validator, P30.1c as ordered/capability
admission, Session services as lifecycle owners, and P25.1 as provider
lowering. It adds no second Session database, replay loop, queue, capability
table, or provider adapter.

Text-only writers and valid legacy-inline image transcripts keep their previous
behavior. Sessionless engines remain ephemeral and claim no restart
durability. The public engine API still accepts only untrusted text/bytes; no
public caller can mint or reuse a durable media ref. Runtime-input records,
TUI, ACP, Plain, headless, child-Agent, Goal, and command surfaces are
unchanged.

## Storage And Crash Ordering

Each saved transcript may own exactly:

```text
<transcript>.media/
  manifest.json
  blobs/sha256/<prefix>/<digest>
```

Directories use mode `0700` and files use mode `0600`. A bounded strict
manifest maps a cryptographically random opaque ID to the private digest,
size, MIME, dimensions, and creation kind. Root-anchored operations reject
links, non-regular components, unsafe modes, and root/subroot replacement.

Publication uses create-exclusive staging, bounded copy and validation,
file sync and close, no-clobber blob installation or exact existing-blob
verification, blob-directory sync, atomically replaced manifest, and
manifest-directory sync. Fixed striped in-process locking preserves concurrent
distinct writes without an unbounded lock registry. A caller input copy is
cleared after use. Deterministic seams cover every file operation, transcript
append/flush, and deletion mutation boundary. Failure yields no visible ref and
cleans staging state; a crash may retain only an unreachable immutable orphan.

## Lifecycle Boundary

`DeleteSession` preflights the inactive transcript, ordinary recognized
sidecars, and the complete exact MediaStore tree without following links. It
revalidates identities before the first mutation, removes the transcript
before private media, and rejects unexpected entries, links, unsafe modes,
entry-count overflow, or replacement races without deleting anything. An
interruption can leave unreachable media but never a surviving transcript with
missing bytes.

Until P30.2c, branch/fork rejects a selected prefix containing media refs before
child creation, export rejects before output creation, and bounded transcript
paging rejects before publishing rows. These guards preserve text-only and
legacy-inline behavior while preventing a lifecycle consumer from silently
dropping private reachability.

## Verification

Closeout uses the frozen focused, race, cross-platform, repository,
documentation, manifest, and diff gates:

```text
go test ./engine -run 'TestP302a|TestSubmitPromptInput|TestP301c|TestUserImageAdmission'
go test ./engine/transcript -run 'TestP302a|TestLoadFull|TestBranch'
go test ./engine/session -run 'TestP302a|TestDelete|TestExport'
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

The end-to-end fixture uses a legal image larger than five million bytes,
asserts a small transcript with no inline payload or digest, starts a fresh
engine, and proves exact text/image/text order and detail on the next provider
call. Separate fixtures prove lifecycle checkpoint and rewrite preservation;
corrupt/missing/unknown records fail before provider or runtime mutation;
unsafe sidecars cannot escape or write outside the store; every injected
publish/flush fault yields either no prompt or one fully resolvable prompt;
concurrent Store instances preserve every manifest row; and complete deletion,
branch/export/paging rejection, cancellation, and link-replacement races retain
their zero-partial-mutation contracts.

## Rollback And Next State

Rollback may stop new `user-prompt` writes and disable the immediate rich API
or route new input through the retained legacy writer. It must retain the
versioned reader, MediaStore, contained deletion, and branch/export/paging
guards for every record already written. Existing refs and blobs cannot be
removed while a transcript can reach them.

P30.2b-P30.6 remain accepted but queued, and G32 remains open. No successor
became `Ready` automatically; root `PLAN.md` must select the next slice
separately.
