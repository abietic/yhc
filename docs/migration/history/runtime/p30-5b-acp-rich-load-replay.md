# P30.5b ACP Rich Load Replay

**Status:** historical
**Completed:** 2026-07-30

> **Ownership:** completion evidence for exact prompt-record-backed ACP rich
> `session/load`. Current behavior belongs in
> [`acp-adapter.md`](../../../architecture/platform/acp-adapter.md) and
> [`transcripts.md`](../../../architecture/state/transcripts.md). Provider-rich
> assistant replay remains G20 and is not claimed here.

## Outcome

P30.5b completed a `project-native` slice inside P30's accepted `combine`
program. ACP v1 load now replays version-1 text/image and version-2
Text/ResourceLink/image/embedded-text/embedded-blob user prompts in their exact
logical order before the load response. Every chunk from one durable prompt
shares one stable message ID. Embedded blobs remain one ACP Resource block;
they are never reconstructed from the provider-facing metadata-text plus image
pair.

The implementation adds no transcript reader and performs no reverse inference
from provider-shaped messages. `LoadFullContext` still validates and
materializes the exact prompt-record/ref binding. The immutable Session replay
snapshot clones a transport-neutral logical union from that binding, and the
existing P23.4b ACP projector alone maps it to SDK content blocks.

## Fidelity And Privacy Boundary

The durable prompt record selects the logical kind. A materialized
`schema.Message` is only the validated carrier for canonical base64 bytes and
must exactly match the record's provider projection. Shape drift, an unknown
record version or kind, a missing/corrupt blob, or unbound provider-rich
content fails before the first client update.

Replay preserves:

- exact text and block order;
- ResourceLink URI, name, optional metadata, and accepted standard
  annotations without opening or fetching the URI;
- safe-raster image base64, MIME, and accepted standard annotations;
- embedded text URI, optional MIME, text, and annotations as one Resource; and
- embedded blob URI, safe-raster MIME, base64, and annotations as one Resource.

Private refs, paths, media IDs and digests, source image URI, reserved `_meta`,
provider envelopes, and raw prompt records never cross the wire.

The pinned `github.com/coder/acp-go-sdk` v0.13.5 generated
`ContentBlock.MarshalJSON` omitted standard annotations and reserved metadata
for every content variant. The repository now uses a minimal local v0.13.5
fork whose only production delta restores those two generated fields for Text,
Image, Audio, ResourceLink, and Resource. P30 still discards incoming reserved
metadata before durable admission; the fork prevents the SDK from silently
dropping valid standard annotations and keeps generic ACP serialization
truthful until an upstream release contains the fix.

## Ordering And Failure Settlement

P23.4b remains the only load lifecycle owner:

1. Session validates one immutable full snapshot and every rich ref.
2. ACP builds the complete user/assistant/tool projection.
3. The existing unregistered, hook-free restore staging owner is prepared.
4. Replay, configuration, mode, and command snapshots are delivered in order.
5. Only then may staging commit, Session registration, hook startup, and the
   load response occur.

Pre-delivery validation failure leaves no client update, active Session, or
hook. A transport failure after a prior update can leave the disconnected
client with a partial local view, but server staging is aborted and no remote
rollback is claimed. Successful or failed replay does not rewrite transcript,
prompt-record, or MediaStore bytes. `session/resume` retains zero historical
conversation updates.

Provider-rich assistant content remains an explicit
`session.load.replay.richContent` failure before the first update. That G20
boundary is intentionally separate from exact prompt-record-backed user
replay.

## Verification

Focused prompt-record, Session snapshot, P23.4b/P30.5a/P30.5b ACP, invalid
durability, delivery-failure, ordering, mutation-isolation, and resume fixtures
passed. The official TypeScript SDK v1.3.0 subprocess harness replayed:

```text
text -> resource_link -> image -> resource(text) -> resource(blob) -> text
```

It verified one shared message ID, exact metadata and annotations, no source
image URI or `_meta`, and one logical Resource for the embedded blob. Focused
race tests passed for ACP, Session, and transcript packages; Windows ACP test
compilation and the repository gates also passed before merge.

A real Zed 1.13.1 smoke used the exact candidate binary, an isolated Zed data
directory, and a local deterministic provider. The live image-plus-text prompt
was durably committed before an intentional provider error produced a plain
assistant terminal.

After a complete Zed restart, the method trace recorded `initialize`, then
`session/load`, then user image and text chunks sharing `message-1`, followed
by assistant text under `message-2`, and only afterward the load response. No
`session/resume` occurred. Zed rendered the restored image attachment, exact
text, and assistant error in the reopened thread.

## Compatibility And Rollback

The slice preserves the ACP v1 wire, live rich admission, P23.4b text/tool and
assistant ordering, stable replay IDs, configuration/mode/command ordering,
no-replay resume, Session listing, branching, export restrictions, deletion,
selected-route admission, and provider lowering. Version-1 records remain
readable and are never rewritten.

Rollback stops advertising `loadSession` and rejects load as P23.4b permits.
P30.5a live rich support, every durable reader, and no-replay resume remain so
existing Sessions stay resumable, branchable, exportable, collectible, and
deletable.
