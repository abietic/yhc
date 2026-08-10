# Model Context Protocol

**Status:** current
**Last verified:** 2026-08-08

> **Ownership:** Engine-scoped MCP client manager; independent `server/mcp` composition root

## Current MCP Boundaries

There are two distinct MCP roles:

1. The coding-agent runtime is an MCP **client**. QueryEngine owns or borrows an
   `MCPToolManager`, which loads configured servers, connects clients, discovers
   tools, and registers first-class `mcp__server__tool` entries in the runtime
   registry.
2. `yhc serve mcp` is an independent MCP **server**. It exposes directly
   executable registry tools over stdio.

These paths share tool implementations but not policy ownership.

## Client path

`engine/mcp.MCPClient` supports the configured SDK transports and owns
connect/disconnect, tool/resource/prompt operations, change notifications,
OAuth helpers, and optional reconnection. Every successful connection receives
an immutable client-local generation. A registered tool captures an
`MCPToolCallTarget` containing that exact SDK session and timeout; it never
looks up the manager's current client when it executes.

`tools.MCPToolManager` serializes publication-affecting lifecycle transitions.
Each live server has one manager-local generation, exact client, unique
registry owner, complete inventory, and bounded health projection.
`tools.Registry` is the sole owner of whether the corresponding
`mcp__server__tool` rows are model-visible, permission-resolvable, and
executable.

The manager also binds the explicit StdioMCP process-class identity before a
configured stdio client can start. Project configuration, ACP transactional
session descriptors, reload, restore, and explicit reconnect retain that
identity in the stdio transport before spawn. P51.1 deliberately keeps this
binding `danger-full-access`/`disabled` through the ambient-host adapter; the
Darwin Seatbelt Guest proof is never reused by MCP. Existing process-tree
ownership remains active, but no filesystem, network, credential, syscall, or
resource sandbox is claimed for configured stdio servers.

`engine/mcp.CanonicalEnvironmentKey` is the single environment-name identity
owner for stdio process overlay and ACP descriptor admission/fingerprinting.
Windows folds names to uppercase; non-Windows keeps exact spelling. Overlay
construction removes inherited semantic duplicates but emits each admitted
overlay entry with its original key spelling and exact value. ACP rejects a
semantic duplicate before manager or process construction and fingerprints
canonical names plus exact values without persisting either descriptor or
secret material.

Initial connection publishes one complete server contribution.
`tools/list_changed` keeps the connection owner and atomically replaces its
complete contribution, including a valid empty list. Manual reconnect prepares
a new client, generation, and owner before one atomic old-to-new replacement.
Disconnect and unexpected close remove only the exact owner. Listing,
identity, collision, or registry-generation failure removes that server's
stale rows and closes it instead of retaining a split manager/registry view.
Other servers and built-ins remain untouched.

Once registered, MCP tools use normal QueryEngine validation, repeated-call,
hook, permission, execution, and result paths. Permission settlement binds the
registry generation. A later replacement invalidates an unexecuted permission;
an already acquired registry lease dispatches its captured old target once or
fails if that connection closed, but cannot jump to the replacement client.
Registry mutation waits only until lease dispatch or cancellation, not for the
network call.

The manager also exposes a revisioned, detached inventory snapshot. `/mcp`
uses this exact read model to report sorted server/tool state, source, health,
and bounded failure categories. Failed servers remain inspectable even if no
live client was installed. `/mcp add`, `remove`, and `restart` are explicitly
unavailable before side effects because persisted configuration, manager
inventory, the runtime registry, the model-visible generation, and rollback
are not one transaction.

`yhc mcp list|get` uses the same snapshot type and text projection but
does not construct clients. Its inspection manager retains only configured
server names and enabled state, labels the snapshot source `configuration`,
and reports `configured` or `disabled` with `unprobed` health. Command, URL,
argument, environment, and header material is neither retained nor rendered.
An empty configured inventory remains distinguishable from an empty live
runtime inventory through the snapshot-level source.

The complete registry remains the dispatch inventory. Model-visible MCP tools
are a filtered projection assembled at safe query/turn boundaries; see
[`tool-registry.md`](tool-registry.md).

## Independent server path

`server/mcp.Serve` creates a fresh default registry and excludes hidden,
non-executable, and `IsPlanModeTransition` entries before registering tools
with the MCP SDK. The Plan exclusion is independent of names, so a custom
executable alias cannot fabricate an interactive Enter/Exit approval on a
surface with no QueryEngine. A registered call applies only the server-local
permission policy, pre/post hook interface, and direct execution.
`server/mcp.Serve` reads `MCP_PERMISSION_MODE` exactly once before registry
construction. Empty and exact lowercase `open` select the open typed policy;
exact lowercase `strict` selects the read-only typed policy. Every other value,
including case variants and surrounding whitespace, returns a safely quoted
configuration error before registry or transport startup. One immutable typed
value is captured by every tool closure for that `Serve` invocation.

It bypasses QueryEngine policy: no project permission rules, scoped approvals,
repeated-call guard, query hooks, recovery cascade, runtime reducer, transcript,
or model-visible pool. Treat it as a separate security boundary.

## Invariants and edge cases

- MCP names are normalized and prefixed before one owned main-registry
  publication.
- A failed configured server does not prevent other servers from initializing.
- Network connect/list/call work and registry mutation do not run while the
  manager state lock is held. One lifecycle mutex prevents refresh, reconnect,
  disconnect, and close from committing out of order.
- A late callback compares the exact client, manager generation, client
  generation, and owner before changing state. It cannot delete or fail a
  replacement generation.
- Registry hooks run synchronously after the manager lifecycle lock is
  released. A hook may inspect or close the manager without deadlocking, while
  callers of the public registry batch API retain synchronous hook delivery.
- Registry changes affect the next model tool assembly; they do not mutate a
  model request already in flight.
- Resource and prompt operations may still resolve the current manager client.
  Exact-generation pinning applies to registered tool execution.
- The SDK client's optional automatic reconnect remains a library capability
  and is not enabled by `MCPToolManager`. Production recovery uses the
  manager-owned explicit reconnect paths so every new SDK session receives a
  new manager generation and registry owner.
- Server strict mode relies on `ToolImpl.IsReadOnly`; incorrect metadata changes
  its security behavior.
- Open mode permits read and write tools; strict mode denies non-read-only
  tools before request access, hooks, timing, or execution. An impossible typed
policy follows the same fail-closed ordering with a distinct diagnostic.

The standalone allowlist is limited to Task/Todo lifecycle tools and has no
Bash, BashOutput, KillShell, or Agent launcher. A source test guards this
separate no-host-process composition root; adding such a tool requires policy
resolution before registration.
- Permission-mode parsing is byte-exact. Compatibility aliases, trimming, and
  case folding are deliberately absent so operator mistakes cannot broaden
  authority.
- Engine-scoped managers are preferred; the process-global manager is a
  compatibility fallback and must not leak session ownership.
- A configured stdio launch requires the exact StdioMCP binding captured by
  the manager and transport. A Guest or ShellHooks binding is a class mismatch,
  not additional authority.
- The package-global manager is not an inspection fallback. QueryEngine slash
  commands read only their engine-owned `RuntimeInspectionSnapshot`.
- Configuration inspection never calls `ConnectServer`, list operations, or a
  configured command. It must not claim runtime health or discovered tools.

## Code references

- [`MCPClient`](../../../engine/mcp/sdk_client.go)
- [`MCPClient.Connect`](../../../engine/mcp/sdk_client.go)
- [`MCPToolCallTarget`](../../../engine/mcp/sdk_client.go)
- [`CanonicalEnvironmentKey`](../../../engine/mcp/environment.go)
- [`stdioProcessTransport`](../../../engine/mcp/stdio_transport.go)
- [`MCPConfig` and `LoadMCPConfig`](../../../engine/mcp/config.go)
- [`MCPToolManager`](../../../tools/mcp_tool.go)
- [`MCPToolManager.InventorySnapshot`](../../../tools/mcp_tool.go)
- [`NewMCPInspectionManager`](../../../tools/mcp_tool.go)
- [`MCPToolManager.ConnectServer`](../../../tools/mcp_tool.go)
- [`MCPToolManager.ReconnectServer`](../../../tools/mcp_tool.go)
- [`MCPToolManager.refreshServerToolsGeneration`](../../../tools/mcp_session_setup.go)
- [`MCPToolManager.handleServerCloseGeneration`](../../../tools/mcp_session_setup.go)
- [`InitMCPManager`](../../../tools/mcp_tool.go)
- [`server/mcp.parseStandalonePermissionPolicy`](../../../server/mcp/server.go)
- [`server/mcp.Serve`](../../../server/mcp/server.go)
- [`server/mcp.standaloneMCPToolExposable`](../../../server/mcp/server.go)
- [`server/mcp.executeTool`](../../../server/mcp/server.go)

## Related tracking

Transport-specific parity evidence belongs in
[`migration/reference/`](../../migration/reference/README.md). Completed P33.1
delivery evidence is in
[`p33-1-mcp-live-tool-generation.md`](../../migration/history/runtime/p33-1-mcp-live-tool-generation.md).
