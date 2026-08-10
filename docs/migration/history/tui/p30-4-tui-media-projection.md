# P30.4 TUI Media Projection

**Status:** historical
**Completed:** 2026-07-30
**Last verified:** 2026-07-30

> **Ownership:** completed P30.4 `project-native` decision within P30's
> accepted `combine` program, including active-draft media ownership, ordered
> leader admission, ref-backed busy input, sanitized presentation, clipboard
> capture, verification, and rollback. Current behavior belongs in
> [`composer.md`](../../../architecture/tui/contracts/composer.md) and
> [`busy-queue.md`](../../../architecture/tui/contracts/busy-queue.md).

## Outcome

The Bubble Tea leader composer now emits the same literal text/image order at
the engine, durable queue, transcript, restart, and provider boundaries. Raw
image bytes have one TUI owner while the draft is mutable and one
Session-private MediaStore owner after durable acceptance. Queue previews,
chat rows, prompt history, rewrite, search, selection, undo metadata, and
runtime-state projection contain only bounded sanitized labels or descriptors.

Idle and busy submission use one typed asynchronous acceptance boundary. The
draft does not clear, history does not append, and running state does not begin
until the matching engine result proves exact admission or durable enqueue.
Rejection and cancellation retain the draft.

## Decision And Compatibility

P30.4 used `project-native` within P30's accepted `combine` program and retained
the existing owners:

- Bubble Tea App owns mutable active-draft text, ranges, revision, and
  presentation;
- one App draft-media table owns captured image bytes until submission;
- QueryEngine owns ordered admission and capability/provenance checks;
- Session-private MediaStore owns accepted durable bytes;
- `RuntimeInputCoordinator` owns queue scheduling, claim, settlement,
  cancellation, and edit serialization; and
- transcript prompt records own accepted turn order and ref identity.

Agent-thread, slash-command, shell, Plain, headless, ACP, standalone MCP,
child/review Agent, and SDK attachment behavior did not widen. Text-only
submission, large paste, context mentions, ordinary queue controls, transcript
compatibility, and P30.3 recovery remain compatible.

## Active Draft And Admission

Image composer elements contain ID, label, MIME descriptor, and rune range
only. Captured bytes enter one App-owned media record. Reachability spans the
active and inactive thread drafts plus undo projections; collection zeros and
removes unreachable bytes.

Path and clipboard loads are typed Bubble Tea commands bound to request ID,
leader thread, draft revision, and captured insertion anchor. Only one may be
pending. A thread switch, edit, replacement, cancellation, or later request
makes the old result stale; stale bytes are zeroed and cannot create an
element.

Submission validates unique IDs, exact labels, non-overlapping rune ranges,
supported element kinds, and matching media before one stable rune walk emits
ordered `UntrustedPromptInput` parts. Paste expands in place, images remain at
their placeholders, context labels remain visible, and context blocks append
to trailing text. Only the logical first and last text edges are trimmed.
Missing or inconsistent media rejects the whole snapshot, while image-only
input remains valid.

## Durable Queue And Sanitized Projection

Busy leader admission publishes image bytes and one ordered ref-backed prompt
record before the bounded queue commit can succeed. The TUI receives only the
accepted queue ID and sanitized ordered descriptors.

`/queue edit` is the single reverse handoff. Under the coordinator and media
lifecycle gates, the engine materializes the exact pending record, persists
removal, then returns detached ordered bytes. Failure leaves the item unchanged.
Claim, cancel, and edit serialize so only one wins. The TUI reconstructs the
draft after success and clears the detached copies.

Submitted rich history is deliberately non-restorable. Image rows render a
visible `image content not restored` marker; no prompt recall, rewrite, raw
selection, search, or thread projection can recreate an image element, private
ref, source path, or byte copy.

## Clipboard Boundary

Clipboard capture is injected into App and has deterministic Darwin, Linux,
and Windows backends. Every backend uses fixed command/argument forms, a
three-second context deadline, bounded stdout/stderr and file reads, and
unique mode-`0600` temporary files where required. Temporary files are removed
on every terminal path.

Format and filename are treated as hints. Oversized, malformed, SVG, or
unsupported results fail before draft mutation; QueryEngine still performs the
canonical decoded image inspection at admission.

## Verification

Closeout ran and passed the frozen focused suites, combined TUI/engine race
gate, Windows cross-compilation, repository gates, documentation/ledger gates,
and diff validation:

```text
go test ./internal/tui/attachments -run 'TestP304|TestClipboard'
go test ./internal/tui -run 'TestP304|TestComposer|TestQueuedInput|TestExternalEditor'
go test ./engine -run 'TestP304|TestPromptInput|TestQueuedUserInput|TestRuntimeInput'
go test ./engine/transcript -run 'TestP304|TestPromptRecord'
go test -race -timeout=20m ./internal/tui/... ./engine/...
GOOS=windows GOARCH=amd64 go test -c ./internal/tui/attachments
GOOS=windows GOARCH=amd64 go test -c ./internal/tui
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

Fixtures cover ordered text/image combinations, image-only and whitespace
edges, paste/context expansion, invalid or deleted ranges, path replacement,
malformed bytes, clipboard bounds, stale loads, undo and draft-media
collection, external editor/thread/model fences, admission cancellation and
failure, queue capacity/restart/persistence/edit/cancel/claim behavior,
sanitized history/rewrite/search/selection, and unchanged unsupported
entrypoints.

## Rollback And Next State

Rollback disables TUI image capture and rich submission first while preserving
ordinary text, paste, context, command, shell, Agent-thread, and queue controls.
Existing ref-backed pending items and committed rich turns remain valid engine
truth and may drain normally. `SubmitPromptInput`, MediaStore, prompt records,
queue settlement, and P30.3 recovery remain readable. Sanitized history does
not regain restorable image payload.

P30.5a-P30.6 remain accepted but queued, G32 remains open at ACP rich ingress
and rich load/replay, and no successor becomes `Ready` automatically. Root
`PLAN.md` must promote one slice in a separate iteration.
