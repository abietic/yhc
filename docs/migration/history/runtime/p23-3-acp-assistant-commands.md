# P23.3 ACP Assistant Identity and Command Snapshots

**Status:** historical
**Closed gaps:** G19
**Completed:** 2026-07-28
**Last verified:** 2026-07-28

> **Ownership:** delivery evidence for persisted logical assistant identity,
> exact canonical ACP assistant projection, and registry/context-owned command
> snapshots. Current behavior belongs in
> [`acp-adapter.md`](../../../architecture/platform/acp-adapter.md); remaining
> ACP work and executable order belong in
> [`p23-acp-adapter-hardening.md`](../../plans/p23-acp-adapter-hardening.md)
> and [`PLAN.md`](../../PLAN.md).

## Decision

P23.3 was delivered under the **`combine`** decision:

- preserve ACP v1, the pinned Go SDK, QueryEngine/transcript ownership,
  provider bytes, command dispatch, and physical `TranscriptEntryID`;
- make the engine yield boundary the only logical assistant identity and
  canonical-delta producer;
- retain `engine/commands.Registry` as the only command definition,
  visibility, ordering, snapshot, and dispatch owner; and
- keep ACP as a delivery-only projector with independent operational rollback
  for the optional message-ID extension and command notifications.

The slice did not add replay, legacy-ID repair, bounded session listing, stdio
MCP, rich media, ACP v2, provider text normalization, or another command or
session state owner.

## Outcome

`queryWithKernel` now wraps the production kernel with one concurrency-safe
assistant projection emitter. The first assistant event after each stream
request receives one UUID in internal `message_id` metadata before persistence
or entrypoint delivery. Tool-interleaved chunks reuse that identity.
Conversation-history merging retains only the logical ID, transcript
persistence carries it passively, and provider normalization strips it.

The emitter publishes exact non-empty canonical deltas. Final-only content is
emitted once, an equal final is suppressed, and a strict extension emits only
its suffix. Any other delta/final mismatch cancels the query and returns a
bounded diagnostic containing identity, lengths, SHA-256 digests, and event
ordinal without assistant bytes. `TranscriptEntryID` remains the separate
physical record identity.

ACP consumes only canonical assistant deltas. It maps the UUID to the pinned
SDK's optional unstable `messageId` while preserving exact content bytes. The
legacy assistant event no longer creates a second wire producer.
`EINO_AGENT_DISABLE_ACP_ASSISTANT_MESSAGE_IDS=1` omits only that optional
field.

The command registry now exposes one immutable SDK-neutral snapshot from the
same `ListForContext` and live `CommandContext` used by dispatch. Ordered rows
contain the canonical name, registered description, and optional usage or
`ArgDef` hint. SHA-256 of the canonical JSON rows is the only replacement
identity.

Each ACP session serializes command recomputation, comparison, delivery, and
digest commit. New, resume, load, and fork force an initial snapshot; config,
mode, and settled-prompt boundaries send only a changed projection. Failed
delivery never commits the digest. Initial failures use the existing
new-session deletion, restored-session close, or fork rollback owner;
post-commit failures retain engine state and retry at the next boundary.
`EINO_AGENT_DISABLE_ACP_COMMAND_UPDATES=1` disables only notifications.

## Evidence

Focused engine, registry, transcript, ACP boundary, SDK round-trip, raw-wire
golden, failure-settlement, and rollback tests passed:

```text
go test ./engine ./engine/messages ./engine/commands -count=1
go test ./server/acp -count=1
go test -race ./server/acp -count=1
```

The first full race run exposed concurrent tool-runtime events entering the new
emitter. A mutex was added at the shared yield boundary, a deterministic
concurrent regression test was added, the original failing multi-tool race test
passed in isolation, and the complete ACP race suite then passed.

Repository closeout passed:

```text
make fmt
make lint
make test
make build
make docs-check
go run ./scripts/migration_manifest.go check
go run ./scripts/migration_scan
git diff --check
```

Canonical engine trace goldens now include assistant projection kind,
message ID, and exact delta. The ACP wire golden freezes interleaved assistant
and tool notifications under one UUID and proves the legacy assistant event is
not emitted. Command fixtures prove complete-row digesting, hints, every
required protocol refresh boundary, successful and failed generation
settlement, initial cleanup, post-commit retention, and retry semantics.

## Compatibility and Rollback

Clients that understand the optional unstable `messageId` can group live
assistant chunks. Clients that ignore it still receive the same ordered bytes.
Command-aware clients now receive full replacement snapshots rather than
maintaining an out-of-band list. Dispatch behavior and non-ACP entrypoints are
unchanged.

The two environment switches are independent. Disabling assistant IDs
preserves content and command updates. Disabling command updates preserves
registry dispatch and assistant projection. Reverting the engine emitter alone
while leaving ACP's canonical assistant path active would suppress assistant
text, so code rollback must revert the producer and consumer together.

## Current Source

| Boundary | Code reference | Why it matters |
|---|---|---|
| assistant identity and final reconciliation | [`assistant_projection.go`](../../../../engine/assistant_projection.go) | Owns logical UUID, exact canonical deltas, mismatch failure, and concurrent serialization before persistence and adapters |
| production query wiring | [`query.go`](../../../../engine/query.go) | Places the emitter around the only production kernel boundary |
| passive durable merge and provider stripping | [`engine.go`](../../../../engine/engine.go) and [`normalize.go`](../../../../engine/messages/normalize.go) | Retains only logical identity in history and removes it from provider requests |
| command rows and digest | [`registry.go`](../../../../engine/commands/registry.go) | Reuses registry visibility and order to build the complete SDK-neutral snapshot |
| ACP assistant projector | [`tool_lifecycle.go`](../../../../server/acp/tool_lifecycle.go) | Maps canonical deltas to exact SDK chunks and the optional UUID field |
| command delivery settlement | [`command_discovery.go`](../../../../server/acp/command_discovery.go) | Owns session-local delivery serialization, digest commit, and new-session cleanup |
| session refresh boundaries | [`agent.go`](../../../../server/acp/agent.go) and [`streaming.go`](../../../../server/acp/streaming.go) | Forces setup snapshots and recomputes after committed protocol boundaries |
| exact wire fixture | [`p23-3-assistant-tool-lifecycle.golden.json`](../../../../server/acp/testdata/p23-3-assistant-tool-lifecycle.golden.json) | Freezes assistant identity, exact bytes, and interleaved tool projection |

## Next State

P23.3 closed G19 and left the live queue. P23.4 remains non-executable until
focused engine/session tests prove an immutable replay snapshot and a
non-persisting staging abort; P23.5 retains its separate stdio-MCP lifecycle
gate.
