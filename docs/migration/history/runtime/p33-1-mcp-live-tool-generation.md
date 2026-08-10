# P33.1 MCP Live Tool Generation

**Status:** historical
**Closed gaps:** G5
**Completed:** 2026-07-31

> **Ownership:** completion evidence for normal QueryEngine MCP atomic
> publication, exact-generation execution, lifecycle convergence, G5 closure,
> and the retained configuration-mutation boundary.

## Outcome

P33.1 completed the accepted `adapt` contract. Every normal
project-configured MCP connection now has one manager generation, one
client-local generation, one unique registry owner, and one complete tool
contribution. Initial connect publishes the complete contribution;
`tools/list_changed` replaces it as one batch, including a valid empty set;
manual reconnect prepares a new exact generation and atomically replaces the
retired owner. Explicit disconnect, unexpected close, invalid refresh,
collision, and registry-generation conflict remove only the affected
generation and fail closed.

The ACP combined setup retains its existing prepare-all, publish-once
transaction and owned process-tree cleanup. TUI, Plain, ordinary headless,
ACP, and inherited subagents continue to use registry-backed managers.
Standalone MCP remains a separate server boundary.

## Permission and Execution Identity

`engine/mcp.MCPClient` assigns an immutable generation to each successful SDK
connection. A registered MCP implementation captures an
`MCPToolCallTarget` containing that exact SDK session and timeout. It does not
look up whichever client the manager currently holds.

Registry permission settlement remains generation-bound. Replacement before
lease acquisition rejects the old permission generation. Replacement after
lease acquisition waits for dispatch or cancellation; a dispatched old lease
uses its captured target once or fails on the closed session, but cannot route
to the replacement client. Late refresh and close callbacks compare the exact
client, manager generation, client generation, and owner before changing
state.

## Concurrency and Review

Network connect, list, and call work does not hold the manager state lock.
One lifecycle lock serializes publication-affecting transitions, while
registry mutation remains atomic and exact-owner scoped. Registry hook
delivery is deferred until that lifecycle lock is released, preserving the
public registry API's synchronous hook behavior without allowing hook reentry
to deadlock the manager.

Independent concurrency review identified the original hook-reentry deadlock
and prompted the deferred-delivery correction. Deterministic reentry,
publication-before-close, refresh, reconnect, permission, execution-lease,
late-callback, collision, and multi-server regressions cover the repaired
boundary. Reproducible commands are in
[`p33-1-mcp-live-tool-generation.md`](../../verification/p33-1-mcp-live-tool-generation.md).

## Compatibility, Exclusions, and Rollback

Valid MCP names, schemas, capability metadata, permission prompts, and
next-model-round visibility remain compatible. The legacy
`RegisterToolsInRegistry` method remains as a deprecated source-compatible
atomic wrapper, but production runtime code no longer calls it.

The SDK's optional automatic reconnect remains a library-only capability:
`MCPToolManager` does not enable it. Production recovery uses explicit
manager-owned reconnect paths so each new SDK session receives a new manager
generation and registry owner. Live persisted configuration mutation, plugin
MCP activation, installation, marketplace, and trust policy remain excluded.

P33.1 adds no durable schema. A squash revert restores the old split-owner
behavior but reopens G5 and cross-generation dispatch ambiguity. Operational
rollback is to disable the affected configured server and restart the engine;
stale rows are never retained as a fallback.
