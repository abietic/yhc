# P48.3 ACP String Raw Output Verification

**Status:** verification
**Last verified:** 2026-08-07

> **Ownership:** reproducible evidence that ACP exposes exact redacted
> tool-result text as string-valued `rawOutput` on both live and replay paths

## Contract

The live ACP lifecycle decodes the canonical engine JSON string to one Go
string. Session replay passes the validated durable tool-result
`message.Content` directly to the SDK update. Objects, arrays, numbers,
booleans, `null`, quoted-string-looking content, and empty text retain exact
string type and bytes on both paths.

This repair does not infer a typed result schema. It does not change transcript
content, rendered tool content, outcome status, update ordering, replay
validation, canonical redaction, or `rawInput` decoding.

## Deterministic Evidence

`TestACPToolRawOutputRemainsStringAcrossLiveAndReplay` drives each accepted
value through two independent production seams. The live side projects an
engine canonical start and terminal through the session lifecycle ledger. The
replay side writes a durable assistant/tool pair, builds the immutable replay
projection, and delivers it through the ACP connection. Each side is captured
through the SDK wire buffer; only the JSON-RPC envelope is decoded before the
test requires `rawOutput` to have dynamic type `string` and exact content.

The pre-fix RED run exposed object, array, null, number, boolean, and decoded
quoted-string values only on replay. The existing replay-order regression now
requires `{"ok":true}` as a Go string, while the full replay and lifecycle
suites retain tool pairing, content, status, ordering, malformed-input, and
delivery-failure behavior. Focused race and the official TypeScript ACP SDK
v1.3.0 subprocess harness cover the surrounding lifecycle and wire boundary.

## Commands

```bash
go test ./server/acp/ -run '^(TestP234bACPReplay|TestACPToolRawOutput|TestACPToolLifecycle)' -count=1
go test -race ./server/acp/ -run '^(TestP234bACPReplay|TestACPToolRawOutput|TestACPToolLifecycle)' -count=1
go test ./server/acp/ -count=1
./scripts/verify-p23-5-acp-sdk.sh
make test-contract
make test-race
make fmt
make lint
make test
make build
make lint-new
make docs-check
go run ./scripts/migration_queue check
go run ./scripts/migration_manifest.go check
git diff --check
```

All commands pass on the closeout tree.

## Evidence Limits

The local checks prove Go/SDK dynamic type, exact content, deterministic replay
behavior, and race-detector coverage. They do not claim that the text itself is
valid JSON or a structured tool result, a remote-CI result, ACP v2 behavior, a
live network provider, or physical rendering in a third-party client. Clients
that previously depended on replay-only non-string values observe the intended
compatibility correction.
