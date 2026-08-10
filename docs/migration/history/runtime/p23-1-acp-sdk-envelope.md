# P23.1 ACP SDK Characterization And Lifecycle Envelope

**Status:** historical
**Completed:** 2026-07-27
**Last verified:** 2026-07-27

> **Ownership:** delivery evidence for the pinned ACP v1 SDK/wire
> characterization and inactive canonical lifecycle envelope. Current behavior
> belongs in
> [`acp-adapter.md`](../../../architecture/platform/acp-adapter.md); remaining
> ACP work and executable order belong in
> [`p23-acp-adapter-hardening.md`](../../plans/p23-acp-adapter-hardening.md)
> and [`PLAN.md`](../../PLAN.md).

## Decision

P23.1 was delivered under the **`combine`** decision:

- preserve ACP v1, `coder/acp-go-sdk v0.13.5`, QueryEngine/session owners,
  current client projection, prompt-scoped `session/cancel`, and the SDK's
  generic `$/cancel_request`;
- characterize the real generated client/server dispatcher instead of treating
  inactive streaming helpers as production evidence;
- add the smallest versioned engine-internal assistant/tool lifecycle envelope
  needed by later stateful projection; and
- keep that envelope inactive so characterization cannot silently repair or
  change current client output.

## Outcome

The production SDK connection now has focused fixtures for version negotiation,
wire errors, generic request cancellation, notification ordering, fragmented
and interleaved tool projection, exact assistant bytes, delivery failure, and
same-ID session isolation.

Both version-1 and version-2 initialize requests receive the v1 response shape.
The current error mapping is pinned as follows:

| Current path | JSON-RPC code |
|---|---:|
| unknown method | `-32601` |
| malformed parameters | `-32602` |
| project-owned unsupported input | `-32006` |
| ordinary handler error | `-32603` |
| cancelled request context | `-32800` |

The SDK-level golden intentionally records current defects: an incomplete JSON
prefix produces a raw-string tool start, the later complete object produces a
second start, progress updates replace one another, an engine
`is_error=true` result is marked completed, and tool-start delivery failure is
swallowed while assistant-text delivery failure is returned. P23.1 did not
normalize or fix any of those outputs.

`QueryEvent` now has an optional `CanonicalProjectionEvent`. Version 1 is a
closed union of:

- `assistant_delta`, with stable logical message ID and exact delta bytes;
- `tool_start`, with one call ID, tool name, and optional already-settled JSON
  input object;
- `tool_input`, with the final effective JSON input object when it becomes
  known after start;
- `tool_progress`, with one complete rendered snapshot, including an empty
  snapshot; and
- `tool_terminal`, with completed/failed outcome and optional valid JSON raw
  output.

Validation rejects version, union, identifier, irrelevant-field, outcome, and
JSON-object violations without interpolating payload bytes into errors.
`Clone` deep-copies all mutable byte slices. The embedded
`RuntimeEventEnvelope` remains the identity and ordering owner.

No production path populates this field and `Agent.streamEvent` does not read
it. The SDK golden includes a valid envelope on an existing assistant event and
remains byte-identical, proving the P23.1 no-output-change boundary.

## P23.2 Promotion Boundary

P23.2 was promoted only after its required production insertion points and
redaction owner assignment were frozen. This table is the accepted next-slice
contract; none of these producers exists in P23.1:

| Canonical fact | Frozen source |
|---|---|
| start and tool identity | committed call at `executeToolCall` entry, before repeated-tool or permission interaction |
| effective input | final normalized `currentInput`, encoded after hook/permission/policy rewrites and immediately before `ToolExecutor` |
| progress | tool-scoped progress callback normalized by the engine builder into a complete rendered snapshot |
| terminal outcome/output | normalized `execution.ToolResult` after cancellation synthesis and context-modifier settlement |
| redaction | P23.2-created engine lifecycle builder before the canonical event leaves QueryEngine; ACP is a projection consumer, not a second redaction or logging owner |

The inactive envelope is not accepted as a permanent parallel owner. P23.2
must activate one producer and one session-local ACP ledger, then delete the
old tool inference path in the same rollback slice.

## Evidence

Focused evidence passed:

```text
go test ./engine -run '^TestCanonicalProjectionLifecycle$' -count=1
go test ./server/acp -run '^TestP231' -count=1
```

Repository closeout passed:

```text
make fmt
make lint
make lint-new
make test
make build
make docs-check
go test -race ./engine -run '^TestCanonicalProjectionLifecycle$' -count=1
go test -race ./server/acp -count=1
go run ./scripts/migration_manifest.go check-ledger
go run ./scripts/migration_manifest.go check
go run ./scripts/migration_scan -reference .reference/claude-code-ripe -json
git diff --check
```

## Compatibility And Rollback

Current ACP clients receive byte-identical output and retain the same v1
capabilities, errors, cancellation, session behavior, and known stateless
projection defects. ACP v2, message IDs, command snapshots, durable replay,
bounded listing, stdio MCP, and rich media remain outside this slice.

Rollback removes `CanonicalProjectionEvent`, its optional `QueryEvent` field,
and its engine tests while retaining the SDK characterization suite and golden.
If the pinned SDK is upgraded, the version, dispatcher, cancellation, ordering,
and exact wire fixtures must be re-reviewed as a compatibility change rather
than mechanically regenerated.

## Current Source

| Boundary | Code reference | Why it matters |
|---|---|---|
| optional event carrier | [`QueryEvent`](../../../../engine/events.go) | Keeps the existing runtime envelope as identity and ordering owner |
| canonical union and validation | [`projection_lifecycle.go`](../../../../engine/projection_lifecycle.go) | Defines the inactive transport-neutral lifecycle facts |
| union and clone fixtures | [`projection_lifecycle_test.go`](../../../../engine/projection_lifecycle_test.go) | Proves closed variants, JSON object rules, empty progress, and deep-copy isolation |
| active ACP projector | [`Agent.streamEvent`](../../../../server/acp/agent.go) | Remains the unchanged current client-output owner |
| SDK and wire characterization | [`agent_sdk_characterization_test.go`](../../../../server/acp/agent_sdk_characterization_test.go) | Exercises the production connection, dispatcher, ordering, current projector, and delivery boundary |
| current wire golden | [`p23-1-current-projection.golden.json`](../../../../server/acp/testdata/p23-1-current-projection.golden.json) | Freezes exact current notification payloads without claiming target behavior |

## Next State

P23.2 is the sole root-plan `Ready` slice. It owns the production canonical tool
event builder, one session-local lifecycle ledger, start-before-permission,
exactly-one terminal settlement, complete raw input/output, failed status,
replacement-safe progress, delivery-failure handling, and deletion of the old
stateless tool inference path.
