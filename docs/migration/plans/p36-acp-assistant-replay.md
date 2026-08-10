# P36 ACP Provider-Rich Assistant Replay

**Status:** historical
**Accepted:** 2026-08-01
**Completed:** 2026-08-01
**Adoption:** `combine`

> **Ownership:** compatibility-retained completed contract for replaying the current
> provider-rich public assistant text through ACP v1 without exposing private
> reasoning or weakening load settlement. Completion evidence is in
> [`p36-1-acp-rich-assistant-replay.md`](../history/runtime/p36-1-acp-rich-assistant-replay.md).

## Problem

The Agentic provider path persists a complete assistant message containing
public `Content`, ordered text output parts, and optional reasoning parts with
provider-bound signatures. The Session replay snapshot preserves that
message, but ACP rejects every unbound `AssistantGenMultiContent` before the
first update. A normal provider-backed conversation therefore cannot be
reopened in a real ACP client even though its user-visible assistant output is
ordinary text.

The failure is safe but over-broad. Removing it without a closed projection
would risk duplicate text, hidden-reasoning disclosure, provider metadata
leakage, media capability drift, or partial replay before a later malformed
part is discovered. Source and protocol evidence is in
[`acp-assistant-replay-audit.md`](../reference/runtime/acp-assistant-replay-audit.md).

## P36.1 Atomic Slice

**Status:** `Complete`

P36.1 adds one immutable assistant presentation projection to the existing
Session replay snapshot. It accepts ordered text output parts and private
reasoning parts only when the text concatenation exactly equals
`Message.Content`. ACP emits the public text parts under the existing logical
assistant ID. The snapshot clone remains projection-only. The unregistered
staging QueryEngine independently reloads the original durable message,
including reasoning/signature, through the existing Session restore path.

Every private field is deliberately absent from ACP. Unsupported media,
unknown or malformed parts, and public-text disagreement fail the complete
projection before the first transport update. The existing load transaction,
capabilities, live text stream, transcript format, and no-replay Resume
contract do not change.

## Scope

P36.1 may change:

- `engine/session/replay_snapshot.go` for a closed immutable assistant replay
  part, strict output-part validation, exact public-text equality, and cloning;
- `server/acp/replay.go` for ordered assistant text-part mapping and removal of
  only the now-supported blanket rejection;
- focused Session/ACP/provider-shaped fixtures and failure injection;
- the official TypeScript ACP v1 real-program harness for provider-rich load;
- an isolated real Zed restart smoke using the exact candidate binary;
- current ACP/transcript architecture, `STATUS.md`, `REMAINING.md`, root
  `PLAN.md`, and one verification/history record at closeout.

The supported production entrypoint is ACP v1 `session/load`. Restored engine
context remains available to all existing Session continuation paths, but
P36.1 changes no other entrypoint's presentation.

## Non-Goals

P36.1 does not:

- add or rewrite transcript records, prompt records, media refs, message IDs,
  provider output, or active stream events;
- send reasoning text, signatures, encrypted continuation tokens,
  `MessageOutputPart.Extra`, message `Extra`, or streaming metadata to a
  client;
- use `agent_thought_chunk`, reserved `_meta`, annotations, or diagnostics as
  a private-data escape hatch;
- support assistant image, audio, video, resource, file, or remote-URL output;
- advertise audio, assistant-media, ACP v2, or a new protocol capability;
- fetch remote media, decode output base64, infer public content from private
  reasoning, or synthesize fallback text for unsupported parts;
- change live prompt ingestion, rich user replay, tool projection, command
  snapshots, Session listing, branching, export/import, or deletion; or
- permit cross-provider/model reuse of reasoning signatures.

## Frozen Invariants

### Authority and ordering

1. The transcript remains the durable message authority; no P36 writer or
   sidecar is added.
2. `LoadSessionReplaySnapshot` materializes and validates one immutable
   assistant presentation beside its unchanged projection clone. That clone
   is not a QueryEngine restore input, and ACP cannot reverse-infer
   presentation from provider fields independently.
3. `buildACPReplayProjection` validates every replay item and builds the
   complete ordered update list before the first transport write.
4. Public text parts retain persisted part order and exact bytes. Every part
   uses the same logical assistant message ID.
5. Assistant text precedes that message's tool calls; paired tool results and
   later messages retain current durable order.
6. Replay, configuration, mode, and command delivery still precede restore
   commit, registration, hooks, and the load response.

### Public presentation and private continuation

1. An accepted rich assistant message contains only text and reasoning output
   part types.
2. Each text part is a closed union: image, audio, video, and reasoning
   payloads are absent. Concatenating its exact bytes in order must equal
   `Message.Content`; mismatch is an unsupported-input error.
3. Each reasoning part has a non-nil reasoning payload. It contributes no ACP
   update, metadata, annotation, ID, diagnostic content, or public fallback.
4. Reasoning text, signature, output-part `Extra`, and message-level provider
   metadata remain in the durable message and the projection clone, but never
   in the public presentation. Runtime-only streaming metadata is not durable
   and has no replay projection.
5. A reasoning-only message may have an empty public projection. It does not
   produce an empty text chunk, but its tool calls and following messages
   retain their normal order.
6. Image, audio, video, unknown, nil, or structurally invalid parts fail before
   any replay update. No supported SDK union is treated as advertised product
   capability by type alone.
7. Diagnostics identify only the replay boundary, durable identity, bounded
   part index, and stable reason code; they never include content, base64,
   signature, provider metadata, transcript path, or raw JSON.

### Failure and lifecycle

1. Projection failure sends zero replay, config, mode, or command updates,
   starts no hooks, registers no active Session, and mutates no durable state.
2. Transport failure preserves the existing staging-abort behavior. A
   disconnected client may retain already delivered local updates; the server
   claims no remote rollback.
3. Successful load does not rewrite the transcript or provider-rich message.
4. `session/resume` continues to emit zero historical conversation updates.
5. The staging QueryEngine independently reloads the transcript. Loading then
   submitting a new prompt preserves the exact private reasoning/signature
   fields in restored model context under the current model binding.
6. Cancellation and concurrent load/close/delete retain the existing Session
   lifecycle serialization.

### Compatibility

- Plain assistant messages keep one unchanged text update.
- Prompt-record-backed rich user replay remains byte-, metadata-, and
  order-compatible.
- Tool identity, raw input/output, status, and pairing remain unchanged.
- Existing transcripts need no migration or backfill.
- A pre-P36 binary continues to fail safely on the same provider-rich Session.

## Deterministic Proof

Focused tests must cover:

- a transcript produced through the Agentic output conversion with multiple
  text parts interleaved with reasoning/signature parts;
- exact text-part order, bytes, and one logical message ID on ACP load;
- zero occurrence of reasoning text, signature, output/message metadata, and
  provider-private values in serialized ACP updates and errors;
- restored QueryEngine history retaining the complete private continuation
  fields after successful load and on the next model request;
- text mismatch, a text part carrying another typed payload, nil reasoning
  payload, unknown type, image, audio, and video failures before the first
  update;
- a reasoning-only assistant message with and without tool calls;
- unchanged ordinary assistant, rich-user, tool, delivery-failure, active
  conflict, no-replay Resume, and durable-mutation fixtures;
- the official TypeScript SDK v1 subprocess loading the provider-rich Session
  before the response and rendering the exact public text stream; and
- a complete isolated Zed restart with the exact candidate binary, method
  trace, restored public text, no reasoning/signature visibility, successful
  continuation, and no `session/resume` substitution.

Final verification requires:

```bash
make fmt
make lint
make lint-new
make test
make build
make docs-check
make docs-check-ci
go test -race ./engine/session ./server/acp -count=1 -timeout=20m
GOOS=windows GOARCH=amd64 go test -c -o /dev/null ./server/acp
./scripts/verify-p23-5-acp-sdk.sh
git diff --check
```

The official SDK and Zed evidence must use the exact candidate binary and an
isolated Session/provider fixture. A unit test that calls the projector
directly cannot substitute for either supported-entrypoint proof.

## Promotion and Rollback

P36.1 completed on 2026-08-01 and closed G20. No assistant-media, ACP v2, or
provider-origin successor was promoted by that closeout.

Rollback removes the assistant presentation projection and restores the
pre-P36 blanket `session.load.replay.richContent` failure. It rewrites no
durable data and remains safe for Sessions created while P36 is installed,
but reopens G20 because those provider-rich conversations again become
unloadable from ACP.
