# P28.H0 Standalone MCP Permission Policy

**Status:** historical
**Closed gaps:** G3
**Completed:** 2026-07-26
**Last verified:** 2026-07-26

> **Ownership:** delivery evidence for the standalone MCP permission boundary.
> Current behavior belongs in
> [`mcp.md`](../../../architecture/capabilities/mcp.md); executable order and
> remaining gaps belong in [`PLAN.md`](../../PLAN.md) and
> [`REMAINING.md`](../../REMAINING.md).

## Decision

P28.H0 was delivered under the **`adapt`** decision:

- preserve the separate `eino-agent serve mcp` composition root rather than
  routing it through QueryEngine;
- preserve exact empty/`open` compatibility and exact lowercase `strict`
  read-only behavior;
- replace the raw-string authorization check with one server-local closed typed
  policy parsed before registry or transport startup; and
- reject operator mistakes instead of normalizing, trimming, or treating them
  as open authority.

G4 plugin file authority and G5 live MCP registry synchronization remain
independent owners and were not included.

## Outcome

`server/mcp.Serve` reads `MCP_PERMISSION_MODE` once at entry. Empty or `open`
selects the typed open policy, `strict` selects the typed read-only policy, and
every other value returns a safely quoted configuration error before default
tool registration, hook construction, SDK server construction, or stdio
transport startup.

Every registered tool closure captures the same typed policy. Open permits
read and write tools. Strict permits read-only tools and preserves the existing
denial text for write tools. An impossible zero or unknown typed value denies
with a distinct diagnostic before request access, hook invocation, timing, or
execution.

The standalone CLI now reports that startup is beginning rather than claiming
the server is already started before configuration validation. QueryEngine,
client MCP, public configuration, CLI flags, tool metadata, schemas, and
persistence are unchanged.

## Evidence

Focused server tests prove:

- the parser accepts only empty, exact `open`, and exact `strict`;
- case variants, leading/trailing whitespace, control characters, and NUL fail
  with escaped diagnostics;
- invalid configuration wins over an already-cancelled context, while every
  valid mode reaches the transport and returns cancellation;
- the open/strict read-write matrix preserves allowed hook and executor order;
- strict and impossible-policy denial paths do not access a request, call
  hooks, or execute either executor form; and
- `ExecuteCtx` precedence and existing MCP integration behavior remain intact.

The command regression proves an invalid mode returns the configuration error,
emits only an accurate `starting` diagnostic, and never claims the server
started. Independent security review found this adapter diagnostic during the
first pass and accepted the focused correction with no remaining finding.

Closeout passed:

```text
make fmt
make lint
make lint-new
make test
make build
go test ./server/mcp -count=1
go test -race ./server/mcp -count=1
go test ./cmd/eino-agent/cmd -count=1
make docs-check
go run ./scripts/migration_manifest.go check-ledger
go run ./scripts/migration_manifest.go check
go run ./scripts/migration_scan
git diff --check
```

The repository test suite and all four release targets passed. Volatile
source, test, and scanner counts remain owned by
[`STATUS.md`](../../STATUS.md).

## Compatibility And Failure Boundary

The accepted values are byte-for-byte compatible. Empty and `open` retain the
previous permissive behavior; exact `strict` retains the previous read-only
admission and write-denial response. Values that previously failed open now
fail startup, which is the intentional security compatibility break.

Authorization still relies on `ToolImpl.IsReadOnly`; incorrect metadata can
misclassify a tool. Standalone MCP still bypasses QueryEngine rules, grants,
repeated-call admission, recovery, transcript, and model-visible registry
ownership. Those boundaries are explicit rather than repaired by P28.

Rollback is one server implementation, focused test, and CLI-diagnostic unit.
Reverting it restores the fail-open G3 behavior, so the safe operational
fallback is to run only with an exact accepted mode or disable the standalone
server until the unit is restored.

## Current Source

| Boundary | Code reference | Why it matters |
|---|---|---|
| startup parser and policy | [`server.go`](../../../../server/mcp/server.go) | Reads the environment once and constructs the closed typed policy before other server setup |
| tool authorization | [`executeTool`](../../../../server/mcp/server.go) | Denies strict writes and impossible policies before request access, hooks, or execution |
| standalone CLI diagnostic | [`serve_mcp.go`](../../../../cmd/yhc/cmd/serve_mcp.go) | Reports an attempt to start without falsely claiming readiness |
| policy matrix | [`server_test.go`](../../../../server/mcp/server_test.go) | Covers exact parsing, startup ordering, open/strict/invalid execution, hooks, and cancellation |
| command regression | [`serve_mcp_test.go`](../../../../cmd/yhc/cmd/serve_mcp_test.go) | Proves invalid configuration cannot emit a false started message |

## Next State

G3 is closed and P28 has no implicit later slice. The root execution queue
returns to intake with no `Ready` or `In progress` row. G4, G5, and the accepted
queued P22-P27 contracts retain their independent owners and require an
explicit future root-PLAN selection.
