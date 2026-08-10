# P23.4b ACP Replay and Bounded Listing

**Status:** historical
**Closed gaps:** G16
**Completed:** 2026-07-28
**Last verified:** 2026-07-28

> **Ownership:** delivery evidence for truthful ACP v1 durable load replay and
> bounded durable-plus-active session listing. Current behavior belongs in
> [`acp-adapter.md`](../../../architecture/platform/acp-adapter.md) and
> [`sessions.md`](../../../architecture/state/sessions.md); remaining ACP work
> and execution order belong in
> [`p23-acp-adapter-hardening.md`](../../plans/p23-acp-adapter-hardening.md)
> and [`PLAN.md`](../../PLAN.md).

## Decision

P23.4b completed the P23 **`combine`** boundary:

- preserve ACP v1 load-before-response replay and opaque list cursors;
- consume the project-owned immutable snapshot, restore-staging, transcript,
  command-registry, and session-query owners rather than adding an ACP store;
- derive missing wire identity from a versioned privacy-safe UUIDv5 tuple while
  rejecting present non-UUID logical identity; and
- keep Resume no-replay, stdio MCP unsupported, rich replay gated by P30, and
  ACP v2 outside this slice.

No durable schema or migration was added.

## Outcome

ACP now advertises `loadSession`. `Agent.LoadSession` serializes with every ACP
session lifecycle transition, rejects an active target, reads one strict
`SessionReplaySnapshot`, prebuilds every portable replay update, and restores
one unregistered hook-free staging engine.

Replay preserves exact user and assistant text bytes. Modern fallback UUIDs use
session ID, persisted entry version/ID, and message index. Legacy fallback
UUIDs use session ID, physical record ordinal, and message index after revision
validation. Transcript path, timestamp, revision digest, payload digest,
content, and content hash are excluded. A legacy anonymous tool call uses
`<message-uuid>/tool/<index>` on the wire rather than its internal
revision-scoped pairing key. Tool starts carry canonical raw input, kind, and
locations; paired results carry exact rendered content, parsed raw output, and
completed or failed truth.

The adapter validates rich content, identity, canonical tool JSON, pairing,
uniqueness, and terminal outcome before the first update. It then delivers:

```text
durable replay -> config -> mode -> commands
  -> commit staging -> register -> start hooks -> response
```

There is no private status or other protocol message inside that required
trace. A replay/config/mode/command delivery failure aborts staging without a
checkpoint or transcript rewrite and leaves no active session or hook.
Concurrent close/delete/restore transitions cannot escape the existing
session-lifecycle mutex.

`Agent.ListSessions` now captures one immutable active-session overlay under
the registry lock and delegates to `engine/session.QuerySessions`. The selector
merges durable and active candidates, de-duplicates stable transcript
identity, applies one deterministic page and scan bound, and returns an opaque
cursor. Strict ACP cursors bind the canonical query plus durable and active
candidate generations; malformed, cross-query, or stale continuation fails
closed.

## Evidence

Focused replay, failure, ordering, cursor, and concurrency tests passed:

```text
go test ./engine/session ./server/acp -run 'TestP234b|TestQuerySessions' -count=1
go test -race ./server/acp -count=1
```

The SDK wire fixtures prove exact replay bytes, fixed modern and legacy UUIDv5
goldens, optional assistant-message-ID rollback, tool raw input/output and
completed/failed settlement, exactly five successful setup updates, and no
extra notification inside the load trace. Negative fixtures cover rich
content, non-UUID logical identity, trailing tool JSON, missing and active
sessions, replay/config/mode/command transport failure, zero pre-delivery
projection bytes, unchanged Resume, bounded page merge, query/durable/active
cursor invalidation, and Load/Close linearization.

Repository closeout passed:

```text
make fmt
make lint
make test
make build
make lint-new
make docs-check
go run ./scripts/migration_manifest.go check
go run ./scripts/migration_scan
git diff --check
```

An independent lifecycle/privacy review found one observable ordering defect:
the first draft inserted the private restored-status extension between replay
and config. The final implementation removed that Load-path notification,
added an exact full-wire trace, and passed re-review with no remaining finding.

## Compatibility and Rollback

Clients gain truthful durable load and bounded pagination. Already-active Load
now returns a typed conflict instead of reusing the active handle. Missing
durable sessions return typed not-found. Unsupported rich replay remains an
explicit pre-delivery error; stdio MCP input remains explicitly rejected, so
P23.5 and complete ACP v1 conformance remain open.

Rollback disables load advertisement and its handler while retaining a bounded
first page with explicit cursor rejection. The snapshot, staging, and selector
owners may remain because they are project-owned and require no durable data
rollback.

## Current Source

| Boundary | Code reference | Why it matters |
|---|---|---|
| strict replay identity | [`replay_snapshot.go`](../../../../engine/session/replay_snapshot.go) | Preserves physical ordinal and anonymous-call facts without changing transcript schema |
| ACP replay projection | [`replay.go`](../../../../server/acp/replay.go) | Prebuilds exact message/tool updates and privacy-safe UUIDs before delivery |
| staged Load ordering | [`Agent.LoadSession`](../../../../server/acp/agent.go) | Owns conflict, delivery, commit, registration, hook, and response order |
| bounded merged selector | [`QuerySessions`](../../../../engine/session/query.go) | Owns durable/active merge, bounds, generations, and opaque cursor validation |
| replay and load fixtures | [`replay_test.go`](../../../../server/acp/replay_test.go) | Proves wire goldens, failure cleanup, no-replay Resume, and lifecycle ordering |
| selector fixtures | [`query_test.go`](../../../../engine/session/query_test.go) | Proves page bounds, de-duplication, and stale-generation rejection |

## Next State

P23.4b left the live queue and closed G16. G17 now tracks only isolated
per-session stdio MCP setup; G20 still tracks official TypeScript and real
client interoperability. P23.5 retains a separate promotion gate, so no later
P23 slice becomes executable merely because replay and listing completed.
