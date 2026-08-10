# ACP String Raw Output Implementation Plan

> **Historical use:** Retained as completed implementation evidence; do not
> execute this checklist as current work.

**Status:** historical
**Completed:** 2026-08-07

> **Ownership:** test-first implementation and closeout steps for P48.3/G44;
> root migration queue state remains authoritative.

**Goal:** Preserve the exact redacted tool-result text as a string in ACP
`rawOutput` on both live and Session replay paths.

**Architecture:** The engine canonical terminal producer continues to encode
its string as JSON and the live ACP lifecycle continues to decode that canonical
payload. Replay stops interpreting transcript text as an ad hoc JSON schema and
passes `message.Content` directly to the SDK update. Wire tests compare dynamic
type and exact bytes across both paths.

**Tech Stack:** Go 1.26.5, ACP Go SDK v1, engine canonical tool projection,
Session replay snapshots, JSON wire fixtures, SDK verification, migration
queue, and Makefile gates.

## Global Constraints

- Execute only when P48.3 is `Ready` and P48.2 has completed.
- Close only G44. Do not change transcript bytes, engine tool-result schema,
  live redaction, tool content, tool status, ordering, or replay validation.
- JSON-looking text remains text. Do not add compatibility heuristics, an
  opt-in decoder, or a typed tool-result field.
- Empty output is the empty string, not `nil`.
- Malformed replay structure must still fail closed; only valid tool-result
  content interpretation changes.
- Preserve late complete `rawInput` and start/progress/terminal lifecycle.

---

## Task 1: Prove live/replay type parity at the wire

**Files:**

- Modify: `server/acp/replay_test.go`

**Interfaces:**

- Live seam: `acpToolLifecycleLedger.project` consuming one canonical terminal
  payload whose `RawOutput` is a JSON-encoded string.
- Replay seam: `buildACPReplayProjection` plus `deliverACPReplay` and the
  existing SDK wire buffer.

- [x] **Step 1: Replace the object-shaped replay expectation**

In `TestP234bACPReplayProjectionPreservesOrderBytesAndToolFacts`, require the
tool result `{"ok":true}` to appear as the exact Go string
`"{\"ok\":true}"`, not `map[string]any`.

- [x] **Step 2: Add a cross-path table contract**

Create `TestACPToolRawOutputRemainsStringAcrossLiveAndReplay` with these exact
values:

```go
[]string{
	`{"ok":true}`,
	`[1]`,
	`null`,
	`1`,
	`true`,
	`"quoted"`,
	``,
}
```

For each value, project a live completed tool call and a replayed durable tool
result. Marshal each SDK notification through the existing wire fixture,
unmarshal only the envelope, and assert `rawOutput` has dynamic type `string`
and exact content. Do not compare a helper to itself.

- [x] **Step 3: Run focused red**

```bash
go test ./server/acp/ -run '^(TestP234bACPReplayProjectionPreservesOrderBytesAndToolFacts|TestACPToolRawOutputRemainsStringAcrossLiveAndReplay)$' -count=1
```

Expected: FAIL for every valid JSON-shaped replay value because the current
decoder returns object, array, `json.Number`, bool, null, or decoded string.

## Task 2: Remove replay-only JSON shape inference

**Files:**

- Modify: `server/acp/replay.go`
- Modify: `server/acp/replay_test.go`

**Interfaces:**

- Changes only the argument passed to `acpsdk.WithUpdateRawOutput` for replay
  tool terminal updates.
- Removes unexported `decodeACPReplayRawOutput` after its final caller is gone.

- [x] **Step 1: Pass durable content directly**

Replace:

```go
acpsdk.WithUpdateRawOutput(decodeACPReplayRawOutput(message.Content))
```

with:

```go
acpsdk.WithUpdateRawOutput(message.Content)
```

Delete `decodeACPReplayRawOutput`. Keep `bytes`, `encoding/json`, and `io` if
they remain required by tool-input decoding or other replay validation; remove
only imports proven unused by the compiler.

- [x] **Step 2: Run focused replay and lifecycle green**

```bash
go test ./server/acp/ -run '^(TestP234bACPReplay|TestACPToolRawOutput|TestACPToolLifecycle)' -count=1
```

- [x] **Step 3: Verify the pinned SDK wire**

```bash
./scripts/verify-p23-5-acp-sdk.sh
make test-contract
```

- [x] **Step 4: Commit the green behavior**

```bash
git add server/acp/replay.go server/acp/replay_test.go
git commit -m "fix(acp): preserve string raw output on replay"
```

## Task 3: Close G44 and document the string contract

**Files:**

- Modify: `docs/architecture/platform/acp-adapter.md`
- Modify if its facts change: `docs/architecture/state/sessions.md`
- Create: `docs/migration/verification/p48-3-acp-string-raw-output.md`
- Modify: `docs/migration/verification/README.md`
- Create: `docs/migration/history/runtime/p48-3-acp-string-raw-output.md`
- Modify: `docs/migration/history/README.md`
- Modify: `docs/migration/REMAINING.md`
- Modify: `docs/migration/queue.yaml`
- Modify generated: `docs/migration/PLAN.md`
- Modify: `docs/migration/plans/p48-acp-boundary-remediation.md`

- [x] **Step 1: Update only delivered semantics**

State that ACP `rawOutput` is redacted text on live and replay paths and that
JSON-looking bytes have no implicit schema. Remove G44 and P48.3. Root
governance promotes P48.4 as the sole `Ready` slice. Do not claim the content
itself is valid JSON or a structured tool result.

- [x] **Step 2: Run final code and documentation gates**

```bash
go test ./server/acp/ -run '^(TestP234bACPReplay|TestACPToolRawOutput|TestACPToolLifecycle)' -count=1
./scripts/verify-p23-5-acp-sdk.sh
make test-contract
make fmt
make lint
make test
make build
make docs-check
go run ./scripts/migration_manifest.go check
git diff --check
```

- [x] **Step 3: Commit closeout and open one atomic PR**

```bash
git add docs/architecture docs/migration
git commit -m "docs: close P48.3 ACP string raw output"
```

The PR must state the `preserve` decision, exact compatibility change for
clients that previously observed replay-only non-string values, rollback,
local evidence, and remote-CI state.
