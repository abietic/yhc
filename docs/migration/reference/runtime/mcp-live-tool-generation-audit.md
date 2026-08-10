# MCP Live Tool Generation Audit

**Status:** reference-snapshot
**Snapshot:** 2026-07-31; Eino-Agent
`636dde0a5ef5038d6d1f0540a39fee5dd3fa563f`, Claude Code Ripe
`4b9d30f79532`, OpenCode `411eff73f026`, and Crush `2af939d8e900`

> **Ownership:** source-backed evidence for deciding how MCP connection loss,
> reconnect, and `tools/list_changed` change model-visible and executable tool
> generations. The accepted contract and execution order belong in
> [`p33-mcp-live-tool-generation.md`](../../plans/p33-mcp-live-tool-generation.md)
> and [`PLAN.md`](../../PLAN.md).

## Decision

Use `adapt`: extend Eino-Agent's existing ACP session-owned MCP publication
protocol to every QueryEngine-owned MCP manager. One complete contribution from
one exact connection generation is prepared and validated before an atomic
`Registry.ReplaceOwnedTools`; connection loss removes only that generation
through `Registry.RemoveOwnedTools`.

This decision does not copy a reference cache. The project registry already
owns stronger canonical identity, permission generation, collision, and
execution-lease semantics. OpenCode, Crush, and Claude provide supporting
evidence that refresh and failure replace or clear one server's complete
contribution rather than appending stale definitions.

## Reproduced G5 failure

Normal QueryEngine construction calls `InitMCPManager`, which connects each
configured server and then copies the discovered tools into the runtime
registry through `RegisterToolsInRegistry`. Those rows have no
`RegistrationOwner`, and the manager is not bound to the registry.

Later callbacks therefore change only `MCPToolManager.tools`:

- `tools/list_changed` can leave removed tools executable and omit new tools;
- unexpected close and `DisconnectServer` can leave a disconnected server's
  tools registered;
- `ReconnectServer` can replace manager inventory without replacing registry
  implementations.

`QueryEngine.modelVisibleTools` rebuilds model definitions from `Registry.List`,
not from the manager inventory. Turn-boundary refresh cannot repair this split
owner. The defect reaches project-configured MCP in TUI, Plain, ordinary
headless QueryEngine use, and the project-configured portion of ACP.

ACP client-supplied stdio MCP already uses the intended owner model:
`PrepareSessionMCPManager` assigns owner IDs, validates a complete candidate,
and publishes through `Registry.ReplaceOwnedTools`. Its list-change and close
paths replace or remove the exact owner contribution. The gap is the older
project-configured initialization path, not the registry primitive.

## A lease still needs one exact connection target

`Registry.AcquireExecution` captures a `ToolImpl` and holds the registry read
lock until `ToolExecutionLease.Execute` reaches the dispatch linearization
point. `Execute` then releases that lock before invoking the captured
implementation, allowing later registry mutation without waiting for the
network call to finish.

The current registered MCP implementation is not fully captured: it calls
`MCPToolManager.CallServerTool`, which looks up the server and tool again in
the manager. If a reconnect replaces the client after lease acquisition but
before invocation, the old authorized lease can call the new connection. That
cross-generation routing contradicts the registry lease's identity promise.

P33 must therefore bind each published MCP implementation to the exact client
and connection generation that supplied its schema. A pre-publication lease
may dispatch that captured target once; connection closure may still make the
network call fail. It must never reroute to a replacement client.

## Current publication and projection boundaries

| Boundary | Verified behavior | P33 consequence |
|---|---|---|
| Model definition | `QueryEngine.modelVisibleTools` reads `Registry.List`; `RefreshTools` applies after a completed round | A settled generation appears in the next model request, never an already submitted request |
| Permission | Permission descriptors bind requested name, canonical name, and registry generation | Replacement requires a fresh policy cycle; a stale settled permission fails before invocation |
| Dispatch | `AcquireExecution` verifies exact identity and generation, then captures `ToolImpl` | The captured implementation must also pin the connection generation |
| Dynamic publication | `ReplaceOwnedTools` validates the post-removal namespace and commits one generation | Publish one complete server contribution or nothing |
| Connection loss | `RemoveOwnedTools` removes exact owner rows without a global-generation precondition | Close must fail closed even when unrelated registry mutation won a race |
| ACP session MCP | `PrepareSessionMCPManager` uses owned atomic publication | Preserve and generalize this mechanism |
| Standalone MCP | `server/mcp` serves tools but owns no model-runtime consumer | Keep outside P33 |

## Comparative evidence

| Source | Useful behavior | Adoption consequence |
|---|---|---|
| Eino-Agent ACP session MCP | Complete validation, owner-scoped atomic replacement, exact-owner removal, and registry leases | Adapt this project-owned protocol to normal engine initialization |
| OpenCode | A notification-triggered relist replaces one server's complete cached definitions only after success; close clears client and definitions | Preserve the complete per-server replacement outcome, not its cache/event owner |
| Crush | Refresh replaces the complete server tool list; an error closes the session and clears tools | Preserve fail-closed cleanup, not its weaker generation model |
| Claude Code Ripe | Server update and close replace or clear that server's tools, commands, and resources | Preserve the contribution-level lifecycle outcome without importing React/AppState ownership |

Neither OpenCode nor Crush couples refresh to Eino-Agent's canonical permission
descriptor and execution lease. Claude's prefix-partitioned App state also
cannot own Go runtime dispatch. Copying any of those mechanisms would add a
second capability owner.

## Accepted lifecycle policy

1. One manager-owned connection generation contains an opaque owner token,
   exact client, validated tool inventory, and published registry generation.
2. Initial connect, notification refresh, manual reconnect, and close serialize
   through one manager lifecycle boundary. Network discovery runs outside
   state locks; publication revalidates the exact connection generation.
3. Refresh prepares the whole server candidate, then atomically replaces that
   owner's rows. No partial list is model-visible or executable.
4. Reconnect publishes a new owner and exact client target. A stale callback
   may remove only its old owner and cannot delete or overwrite the replacement.
5. Relist failure, invalid identity, collision, or compare-and-replace loss
   removes the affected old owner, clears only its manager inventory, records a
   bounded failure category, and closes that exact client.
6. Close removes registry rows before the manager can report the connection
   unavailable. New resolution and dispatch fail immediately; an already
   acquired lease may reach only its captured old client.
7. A successful registry commit precedes the matching healthy manager
   projection. The registry remains authoritative for model visibility,
   permission, and execution.

## Compatibility and exclusions

Valid configured servers keep the `mcp__server__tool` names, schemas,
capability metadata, permission flow, tolerant per-server startup failure, and
next-turn model refresh behavior. A server whose refreshed contribution
collides or becomes invalid now loses its previous executable rows instead of
silently retaining stale capabilities.

P33 adds no new transport, OAuth, prompt/resource projection, plugin MCP
activation, watcher, durable persistence, protocol field, or marketplace
behavior. It does not make standalone MCP a QueryEngine consumer and does not
change ACP descriptor admission.

## Evidence required before closeout

- normal `InitMCPManager` initial publication uses owned atomic rows;
- notification replacement adds and removes a complete server generation;
- unexpected close and explicit disconnect remove only the exact owner;
- reconnect rotates the connection target and a stale callback cannot remove
  the replacement;
- list, identity, collision, and registry-generation failures publish no
  partial rows and remove stale rows;
- a permission settled at generation N fails after replacement before
  invocation;
- a lease acquired before replacement invokes its captured old client at most
  once and never the replacement client;
- unrelated built-ins and other MCP servers survive every failure;
- TUI, Plain, ordinary headless, and ACP QueryEngine paths consume the same
  registry contract while standalone MCP remains excluded;
- focused race tests and all repository gates pass.

## Source anchors

| Boundary | Evidence |
|---|---|
| Normal project MCP initialization | [`InitMCPManager`](../../../../tools/mcp_tool.go) |
| Legacy split publication | [`MCPToolManager.RegisterToolsInRegistry`](../../../../tools/mcp_tool.go) |
| ACP owned setup and callbacks | [`PrepareSessionMCPManager`](../../../../tools/mcp_session_setup.go), [`MCPToolManager.refreshServerToolsOwned`](../../../../tools/mcp_session_setup.go) |
| Atomic owned registry generation | [`Registry.ReplaceOwnedTools`](../../../../tools/registry.go), [`Registry.RemoveOwnedTools`](../../../../tools/registry.go) |
| Permission-to-dispatch lease | [`Registry.AcquireExecution`](../../../../tools/registry.go), [`ToolExecutionLease.Execute`](../../../../tools/registry.go) |
| Model-visible projection | [`QueryEngine.modelVisibleTools`](../../../../engine/engine.go), [`runRoundLifecycle`](../../../../engine/round_lifecycle.go) |
| Current MCP behavior | [`mcp.md`](../../../architecture/capabilities/mcp.md) |

## Current replacement

P33.1 closed the reproduced split owner under the
[`P33 contract`](../../plans/p33-mcp-live-tool-generation.md). Current
implementation ownership is documented in
[`mcp.md`](../../../architecture/capabilities/mcp.md); delivery and regression
evidence is in
[`p33-1-mcp-live-tool-generation.md`](../../history/runtime/p33-1-mcp-live-tool-generation.md).
This snapshot remains historical comparative evidence, not current behavior.
