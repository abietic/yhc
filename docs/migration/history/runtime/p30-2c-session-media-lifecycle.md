# P30.2c Session Media Lifecycle

**Status:** historical
**Completed:** 2026-07-30
**Last verified:** 2026-07-30

> **Ownership:** completed P30.2c `project-native` decision within P30's
> accepted `combine` program, ref-only lifecycle projections, rich Session
> branching, sanitized export, ACP private-migration rejection, manual media
> collection, verification, and rollback. Current transcript behavior belongs
> in [`transcripts.md`](../../../architecture/state/transcripts.md), Session
> behavior in [`sessions.md`](../../../architecture/state/sessions.md),
> recovery ordering in
> [`recovery.md`](../../../architecture/runtime/recovery.md), and ACP behavior
> in [`acp-adapter.md`](../../../architecture/platform/acp-adapter.md).

## Outcome

A saved rich Session can now be paged, listed, exported as a sanitized
presentation, and branched without exposing or unnecessarily materializing its
private image bytes. A branch owns newly minted refs and independently copied
ordinary files, shares no manifest or inode authority with the source, and
continues to resume after the source is deleted.

The exact active saved QueryEngine can manually collect media proven
unreachable from the complete physical transcript audit, every durable
runtime-input state, and every in-flight rich writer. Collection is not
automatic, offline, cross-Session, or multi-process. ACP private Session
migration rejects ref-dependent transcript or queue state instead of issuing a
token that cannot carry the private store.

## Decision And Compatibility

P30.2c used `project-native` within P30's accepted `combine` program. It kept
the existing owners:

- transcript owns physical records, revisions, active projection, and bounded
  paging;
- `engine/session` owns listing, branch, export, and delete orchestration;
- `RuntimeInputCoordinator` owns durable pending, processing, submitting, and
  recovery state;
- QueryEngine owns the active media-lifecycle gate and manual collection;
- MediaStore owns the private manifest and immutable blobs; and
- P23 ACP owns migration-token admission and registration order.

The slice added no second index, provider adapter, Session database, media
archive, automatic collector, rich TUI/ACP ingress, or historical omission
policy. Text-only and valid legacy-inline Sessions retain their behavior. The
low-level transcript-only branch helper still rejects rich refs because it
does not own a MediaStore.

## Ref-Only Presentation

Transcript paging preserves ref-backed prompt rows in source order under the
existing cursor, prefix, record, and page bounds. Trusted lifecycle consumers
can retain the typed prompt record and runtime-item identity. Agent inspection
projects only ordered text plus MIME, size, dimensions, detail, and kind.
Listing reports `none`, `refs`, `record_corrupt`, or `unknown` from bounded
record inspection.

Markdown export writes a stable image placeholder. JSON export writes an
ordered closed text/image parts union. Neither resolves a blob or exposes a
media ID, digest, path, URI, base64 value, byte payload, caller metadata, or
provider body. The presentation is deliberately not a restorable archive.
Malformed or unknown rich records fail before returning a partial page or
export.

## Branch Publication Order

The branch protocol is:

```text
freeze regular source object, revision, and active prefix
  -> validate every selected unique source ref and target path
  -> copy ordinary bytes into a same-parent private staging sidecar
  -> mint child IDs and rewrite only the selected prompt records
  -> sync and install the child sidecar
  -> sync and install the no-clobber child transcript
```

The transcript is the child visibility point and is published only after all
child refs are durable. A crash can retain an unreachable staging or final
sidecar; it cannot expose a child transcript with missing media. Exact
OperationID retry binds source Session, source revision, prefix count, child
identity, prompt structure, source-to-child ref bijection, and child
transcript identity. A mismatch fails closed.

Source transcript, manifest, blob contents, metadata, and mtimes remain
unchanged. The child uses newly minted IDs, shares no hard link or manifest
authority, and survives source deletion.

## Manual Reachability Collection

Rich transcript and queue writers hold a shared QueryEngine media-lifecycle
lease from store publication through their durable ref commit. Collection
takes the exclusive lease and accepts only the exact live saved Session owner.

Under that lease it snapshots:

- the exact regular transcript object and revision;
- every valid ref in every physical prompt record, including superseded
  lifecycle snapshots;
- coordinator revision and refs from every durable item state; and
- the current private manifest.

It revalidates transcript object/revision and coordinator revision immediately
before manifest mutation. MediaStore publishes the retained manifest before
removing any unreferenced blob or temporary file. Shared content remains while
any retained manifest entry reaches its digest. Pre-commit corruption,
cancellation, replacement, revision drift, or unsafe filesystem structure
changes nothing. After manifest commit, cleanup may conservatively retain
bytes but cannot return a cancellation error that implies the committed prune
was rolled back.

## Verification

Closeout ran the frozen focused suites, exact engine race gate, Windows
cross-compilation, repository gates, documentation/ledger gates, and diff
validation:

```text
go test ./engine/internal/mediastore -run 'Test.*Copy|Test.*Collect|Test.*Fault|Test.*Link'
go test ./engine/transcript -run 'TestP302c|TestLoadMessagePage|TestBranch|TestP302a|TestRuntimeItem'
go test ./engine/session -run 'TestP302c|TestBranch|TestExport|TestDelete|TestList'
go test ./engine -run 'TestP302c|TestAgentTranscriptPage|TestP302a|TestP302b|TestRuntimeInputCoordinator'
go test ./server/acp -run 'TestP302c|TestSessionMigration|Test.*Load|Test.*List'
go test -race -timeout=20m ./engine/...
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

Fixtures cover ref-backed cursor pages, malformed rich records, listing
states, sanitized export, branch source immutability, newly minted child refs,
no hard links, source deletion, exact retry, preexisting targets, staging
cleanup, corrupt source, live and shared-digest collection, pending/
processing/settled reachability, pre-commit faults and cancellation,
active-owner rejection, private ACP export/import rejection, concurrency, and
race execution.

## Rollback And Next State

Rollback first disables manual collection and restores conservative rejection
for ref-backed paging, branch, export, and private migration. It must retain
the ref-only readers, MediaStore, rich writer gate, P30.2a/P30.2b record
readers and writers, exact deletion, and every committed child store. A child
that was published successfully remains an independent Session and cannot be
relinked to its source.

P30.3-P30.6 remain accepted but queued, G32 remains open, and no successor
became `Ready` automatically. Root `PLAN.md` must promote one slice in a
separate iteration.
