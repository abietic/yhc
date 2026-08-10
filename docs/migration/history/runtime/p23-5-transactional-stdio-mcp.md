# P23.5 Transactional Stdio MCP

**Status:** historical
**Closed gaps:** G17
**Completed:** 2026-07-28
**Last verified:** 2026-07-28

> **Ownership:** delivery evidence for isolated per-session ACP v1 stdio MCP
> setup. Current behavior belongs in
> [`acp-adapter.md`](../../../architecture/platform/acp-adapter.md); remaining
> ACP provider/replay interoperability belongs in
> [`REMAINING.md`](../../REMAINING.md).

## Decision

P23.5 completed the P23 **`combine`** boundary:

- preserve ACP v1 client-supplied setup on new, load, and resume;
- retain QueryEngine, the flat tool registry, P28 permission policy, durable
  replay, and restore staging as the product owners;
- adapt verified process-group, Windows Job Object, atomic registry, and
  current Zed request-lifecycle patterns; and
- keep HTTP, SSE, ACP transport, ACP v2, durable descriptor storage, optional
  rich content, and provider replay expansion outside this slice.

## Outcome

ACP validates the complete stdio descriptor set before launch. Bounds cover
server count, argument and environment count, aggregate bytes, names,
commands, NUL bytes, environment grammar, source normalization, and optional
transport unions. Errors disclose only a stable input name, reason code, and
descriptor index.

One session-owned `MCPToolManager` combines project and client servers,
connects client descriptors with bounded concurrency, discovers the complete
tool set, collision-checks it, and publishes one registry generation. New
prepares before visibility. Load and inactive Resume pass the prepared manager
through the non-persisting restore staging owner; commit adopts the exact
manager and abort closes it. Active Resume preserves empty input, reuses an
exact setup, reconnects missing members transactionally, and rejects a
different fingerprint.

Every dynamic registry row has an opaque manager/server owner.
`tools/list_changed` replaces one server generation against a frozen global
generation or removes its complete old generation. Unexpected connection
close first closes the exact monitored SDK session and owned transport, then
removes stale rows. Active reconnect stages the prepared client under the
manager lock before checking liveness and publishing, so a pre-publication
exit removes the old owner generation and a post-check exit cannot lose its
identity-bound cleanup callback. Project-configured servers retain their
tolerant omission policy; client descriptors remain all-or-nothing. Tool
invocation still passes through the ordinary dynamic/network permission path
and registry execution lease.

The project-owned stdio transport executes one absolute command without a
shell, applies exact argv, resolved CWD, and a deterministic inherited
environment overlay, and owns the complete process tree. Darwin/Linux use a
dedicated process group. Windows uses suspended start, kill-on-close Job Object
assignment, then primary-thread resume. Close applies bounded stdin,
terminate, and kill settlement even if a descendant outlives the direct child.
Unsupported hosts reject before launch.

## Evidence

Focused and race fixtures cover:

- every static descriptor bound and normalized source collision;
- multi-server all-or-nothing connect, discovery, cancellation, timeout, and
  registry publication;
- exact CWD, argv, inherited environment overlay, secret-safe errors, and
  absence of descriptors from durable bytes;
- new, load, inactive resume, active exact reconnect, mismatch, delivery
  failure, close, and restore-abort ordering;
- discovered collisions, global-generation compare failure,
  `tools/list_changed`, unexpected exit, and execution-lease behavior; and
- direct-child plus descendant cleanup on Darwin/Linux.

The official `@agentclientprotocol/sdk@1.3.0` v1 subprocess harness negotiates
protocol version 1 and exercises new, close, load, resume, discovery,
invocation, exact process input, unexpected exit, exact reconnect, typed
failure, and process cleanup:

```text
./scripts/verify-p23-5-acp-sdk.sh
sdk=@agentclientprotocol/sdk@1.3.0
negotiatedProtocolVersion=1
launches=5
modelRequests=6
replayUpdates=4
resumeConversationUpdates=0
failureCode=-32602
```

A real Zed 1.12.1 smoke used an isolated trusted project and the production
custom-agent configuration. Zed forwarded the configured MCP descriptor on
session setup. The ACP-owned child received the project CWD, exact argument,
and environment overlay; the model saw
`mcp__p23-zed-mcp__echo`; two real calls returned `echo:zed-v1`. Invoking the
test server's shutdown removed the child and its MCP rows, and closing the Zed
project removed both Zed-native and ACP-owned process trees.

Reopening the saved Zed conversation reached the established P23.4b guard and
failed with `session.load.replay.richContent`: the Agentic OpenAI path had
persisted `assistant_output_multi_content`. No replay was silently stripped.
That is retained as a G20 provider-to-durable-replay gap, not claimed as a
P23.5 failure or widened into this transport slice.

## Repository Closeout

The final source state passed:

```text
make fmt
make lint
make test
make build
make lint-new
make docs-check
make docs-check-ci
go run ./scripts/migration_scan -reference .reference/claude-code-ripe
go test -race ./server/acp -count=1
GOOS=windows GOARCH=amd64 go test -c -o /dev/null ./engine/mcp
./scripts/verify-p23-5-acp-sdk.sh
git diff --check
```

`make test` completed 5,564 tests with only the opt-in physical-terminal
diagnostic skipped. The full ACP race suite completed after focused
Darwin/Linux process-tree, reconnect-publication, project-tolerance, registry,
and lifecycle race repetitions. `make lint-new` reported zero current issues;
its warning referenced a deleted temporary worktree rather than current
source. Both documentation checks validated 194 Markdown files and 2,796
local links; the manifest and ledger classified all 1,884 reference files.
An independent second-line review returned `ADMISSION: ACCEPT` after its two
initial lifecycle findings were repaired and retested.

## Compatibility and Rollback

Zed and other ACP v1 clients can now supply bounded stdio MCP servers on
new/load/resume. Existing project MCP configuration remains combined under the
same manager, registry, permission, and close owners. Optional transports and
malformed input still fail before mutation. No durable schema changed.

Rollback restores explicit non-empty-MCP rejection and removes the
session-setup manager path. The atomic registry owner API and process-tree
transport may remain only if no unadvertised client setup path can reach them.

## Current Source

| Boundary | Code reference | Why it matters |
|---|---|---|
| descriptor admission | [`mcp_setup.go`](../../../../server/acp/mcp_setup.go) | Owns validation, bounds, privacy-safe errors, fingerprint, and CWD projection |
| ACP lifecycle wiring | [`Agent.NewSession`, `Agent.LoadSession`, and `Agent.ResumeSession`](../../../../server/acp/agent.go) | Own setup-before-visibility/replay, active reuse/reconnect, and conflict behavior |
| manager transaction | [`mcp_session_setup.go`](../../../../tools/mcp_session_setup.go) | Owns prepare, collision, publication, reconnect, dynamic refresh, and failure settlement |
| registry generation | [`Registry.ReplaceOwnedTools`](../../../../tools/registry.go) | Publishes or removes complete owner generations without overwriting unrelated rows |
| restore adoption | [`PrepareRestoreSessionMCP`](../../../../engine/mcp_session_setup.go) | Binds the prepared manager to commit/abort ownership |
| process tree | [`stdio_transport.go`](../../../../engine/mcp/stdio_transport.go) | Owns exact launch input and bounded whole-tree cleanup |
| official SDK proof | [`verify-p23-5-acp-sdk.sh`](../../../../scripts/verify-p23-5-acp-sdk.sh) | Runs the pinned TypeScript v1 client against the real binary and stdio server |

## Next State

P23 has left the live execution queue. G17 is closed. G20 remains open only for
provider-to-engine byte provenance, portable provider-rich durable replay, and
real-client rendering across those content shapes. Root PLAN returns to intake;
no later accepted program becomes executable as a consequence of P23
completion.
