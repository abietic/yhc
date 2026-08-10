# P30.5a ACP Rich Ingress

**Status:** historical
**Completed:** 2026-07-30

> **Ownership:** completion evidence for live ACP ResourceLink, image, and
> embedded-resource prompt ingress. Current behavior belongs in
> [`acp-adapter.md`](../../../architecture/platform/acp-adapter.md),
> [`transcripts.md`](../../../architecture/state/transcripts.md), and
> [`sessions.md`](../../../architecture/state/sessions.md). Rich ACP
> load/replay remains owned by the queued P30.5b contract.

## Outcome

P30.5a completed a `project-native` slice inside P30's accepted `combine`
program. ACP v1 now advertises safe-raster image and embedded-context support
and converts every live Text, ResourceLink, image, embedded-text, and
embedded-blob block into one literal ordered engine prompt. Audio, unknown
unions, malformed required metadata, and unsupported selected routes fail
explicitly with bounded block-indexed errors before model entry.

Text-only input retains the existing ACP command-compatible path.
ResourceLink remains a deterministic no-fetch descriptor. Any prompt
containing a rich block uses `SubmitPromptInput`, so command-looking text is
literal and no adapter-owned flattening or separator can change protocol
order. Reserved `_meta` and image source URI do not become durable or
model-visible state.

## Durable And Lifecycle Boundary

The strict prompt-record codec now writes version 2 for ResourceLink,
embedded text/blob, and standard annotations while continuing to read
version-1 text/image records without rewriting them. Ordinary and
embedded-blob raster bytes remain in the Session-private MediaStore; records
and runtime-input ledgers contain opaque refs only.

Restart materialization, prompt paging, independent branching, sanitized
Markdown/JSON export, private-migration rejection, deletion, and reachability
collection understand every new kind. One embedded blob remains one logical
durable part even though its provider projection is an adjacent metadata-text
and image pair. P30.3 historical-media recovery follows that expansion without
changing canonical records or current-turn media.

P23.4b text/tool load remains advertised and unchanged. A load whose active
history contains a rich prompt still fails before its first update; no-replay
resume accepts new rich input without duplicating history. P30.5b owns rich
historical replay.

## Compatibility And Rollback

The slice preserves the ACP v1 wire, structural-validation precedence,
selected-route capability authority, Session prompt serialization, QueryEngine
ordering, and the P25.1 provider lowering boundary. Version-1 records remain
readable, and valid legacy text/image writers keep their existing schema.

Rollback first stops advertising image and embedded-context support and
returns explicit unsupported-input errors. Version-2 readers and lifecycle
support must remain so already committed Sessions stay resumable, branchable,
exportable, collectible, and deletable.

## Verification

Focused engine, transcript, Session, recovery, ACP, official TypeScript SDK
v1, selected-model, restart, no-replay resume, malformed-input, redaction, and
cross-platform compilation fixtures passed. The full repository formatting,
lint, test, build, documentation, migration-ledger, race, and diff gates were
also run before merge.

A real Zed 1.13.1 smoke used the exact candidate binary and an image-plus-text
prompt. The ordinary advertised-capability path created a new Session, invoked
the selected model, rendered the bounded response, and committed one ref-only
prompt. Current Zed prefers `session/load` when an agent advertises both load
and resume, so the resume half used a protocol-transparent test shim that hid
only `loadSession` in the initialize response and forwarded every other byte
to the same binary. Zed then displayed its explicit no-history “Resumed
Session” state, sent `session/resume`, and completed a second image-plus-text
prompt in the same Session. A method-only shim trace and the durable transcript
proved one resume and one live prompt, two user-prompt records, no replayed
history, two private media refs, and no inline base64 or source URI.
