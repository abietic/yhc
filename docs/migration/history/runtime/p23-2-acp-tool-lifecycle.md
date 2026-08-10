# P23.2 ACP Tool Lifecycle Projection

**Status:** historical
**Closed gaps:** G18
**Completed:** 2026-07-27
**Last verified:** 2026-07-27

> **Ownership:** delivery evidence for the engine-owned canonical tool
> lifecycle producer and prompt-scoped ACP ledger. Current behavior belongs in
> [`acp-adapter.md`](../../../architecture/platform/acp-adapter.md); remaining
> ACP work and executable order belong in
> [`p23-acp-adapter-hardening.md`](../../plans/p23-acp-adapter-hardening.md)
> and [`PLAN.md`](../../PLAN.md).

## Decision

P23.2 was delivered under the **`combine`** decision:

- preserve ACP v1, the pinned Go SDK, QueryEngine/session ownership,
  `RuntimeEventEnvelope` ordering, permission semantics, and current
  cancellation/drain behavior;
- adapt the versioned P23.1 lifecycle envelope into one active engine producer
  and one prompt-scoped stateful ACP consumer;
- combine committed-call, effective-input, tool-progress, and normalized-result
  sources so the client receives one lossless lifecycle instead of inferring
  state from provider fragments; and
- delete the old assistant/result/progress inference path in the same rollback
  slice rather than retaining parallel lifecycle owners.

## Outcome

One engine lifecycle builder now publishes four already-redacted canonical
facts:

| Fact | Authoritative insertion point |
|---|---|
| `tool_start` | committed `schema.ToolCall` at `executeToolCall` entry, before repeated-tool or ordinary permission interaction |
| `tool_input` | final normalized and policy-settled input immediately before `ToolExecutor` |
| `tool_progress` | tool-scoped progress callback, represented as a complete replacement snapshot |
| `tool_terminal` | normalized `execution.ToolResult` after synthetic cancellation and context-modifier settlement |

Redaction happens before the canonical fact leaves QueryEngine. Credential
keys, provider secrets, PEM material, bearer credentials, and
credential-assignment forms are replaced recursively without including payload
bytes in builder diagnostics. ACP consumes the already-redacted fact and does
not become a second redaction or logging owner.

Each prompt owns one mutex-protected lifecycle ledger. The ledger uses the tool
invocation ID as identity, tolerates requested/canonical name aliases, and
serializes SDK tool-start delivery with the related permission request. Input
changes the call to in-progress with complete `rawInput`; progress replaces the
single visible content snapshot; terminal emits completed or failed with
normalized `rawOutput`. Duplicate canonical facts are de-duplicated, the same
ID remains isolated across sessions, and a synthetic terminal for a call whose
start never crossed the transport remains local only.

If a lifecycle notification cannot be delivered, the prompt is cancelled and
drained while the ledger settles the call locally. No false terminal is written
to the failed transport. Permission rejection and normalized execution failure
produce one failed terminal while the transport remains writable.

The ACP projector no longer infers tool lifecycle from assistant tool-call
fragments, legacy progress events, or legacy result events. Fragmented provider
JSON therefore cannot create repeated starts or malformed `rawInput`.

## Evidence

Focused lifecycle evidence passed:

```text
go test ./engine -count=1
go test ./server/acp -count=1
go test -race ./server/acp -count=1
```

Repository closeout passed:

```text
make fmt
make lint
make lint-new
make test
make build
make docs-check
go run ./scripts/migration_manifest.go check-ledger
go run ./scripts/migration_manifest.go check
go run ./scripts/migration_scan -reference .reference/claude-code-ripe -json
git diff --check
```

The engine wiring fixtures prove start-before-permission, final rewritten input
before dispatch, complete progress snapshots, and normalized completed/failed
terminal projection. ACP ledger and SDK-wire fixtures prove exactly-once
projection, alias de-duplication, session isolation, permission ordering,
rejection, delivery settlement, fragmented-provider isolation, and exact v1
notification bytes. Seven canonical trace goldens were updated to include the
new transport-neutral lifecycle facts.

## Compatibility And Rollback

P23.2 changes ACP v1 tool updates from provider-fragment inference to the
engine-owned lifecycle:

- start can precede input so permission always references a visible tool ID;
- `rawInput` appears only after final hook/permission/policy rewriting;
- engine failures end as failed rather than completed;
- progress is explicitly replacement-safe; and
- notification-delivery failure is returned and settles local state instead of
  being swallowed.

QueryEngine execution order, tool identity, permission decisions, tool-result
normalization, legacy non-ACP runtime events, assistant text bytes, ACP session
capabilities, commands, replay, listing, MCP, rich input, and ACP v2 remain
unchanged.

Rollback reverts the engine builder insertion points and ACP ledger/projector
as one unit while retaining the P23.1 SDK characterization and historical wire
fixture. It must not restore both projection owners concurrently.

## Current Source

| Boundary | Code reference | Why it matters |
|---|---|---|
| canonical union, validation, redaction, and builders | [`projection_lifecycle.go`](../../../../engine/projection_lifecycle.go) | Owns transport-neutral tool facts before they leave QueryEngine |
| committed start and final input wiring | [`tool_execution.go`](../../../../engine/tool_execution.go) | Publishes permission-visible identity and final effective dispatch input |
| progress and normalized terminal wiring | [`tool_round.go`](../../../../engine/tool_round.go) and [`query.go`](../../../../engine/query.go) | Publishes complete progress and post-settlement terminal truth |
| prompt-scoped lifecycle ledger | [`tool_lifecycle.go`](../../../../server/acp/tool_lifecycle.go) | Owns exactly-once ACP state transitions, SDK ordering, and delivery settlement |
| active ACP projector | [`Agent.streamEvent`](../../../../server/acp/agent.go) | Consumes only canonical tool lifecycle facts |
| engine wiring fixtures | [`tool_projection_wiring_test.go`](../../../../engine/tool_projection_wiring_test.go) | Proves authoritative insertion points and normalized terminal behavior |
| ledger fixtures | [`tool_lifecycle_test.go`](../../../../server/acp/tool_lifecycle_test.go) | Proves state transitions, alias identity, failure settlement, and session isolation |
| v1 wire fixture | [`p23-2-tool-lifecycle.golden.json`](../../../../server/acp/testdata/p23-2-tool-lifecycle.golden.json) | Freezes exact canonical client notifications |

## Next State

No slice is executable after P23.2. P23.3 remains gated on current-source
characterization of persisted logical-message identity, the complete projected
command digest, and protocol refresh triggers before root `PLAN.md` can promote
it.
