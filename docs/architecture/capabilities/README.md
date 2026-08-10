# Capabilities Architecture

**Status:** current
**Last verified:** 2026-08-07

> **Ownership:** change-oriented index for tools, permissions, hooks, commands, skills, plugins, and MCP

Use this group when changing what the model or user can invoke. Capability discovery, model-visible projection, dispatch, and policy enforcement are separate stages; a declaration is not proof of runtime availability.

## Change routes

| Change | Start here | Required cross-check |
|---|---|---|
| Built-in tool set, precedence, model-visible filtering | [tool registry](tool-registry.md) | [model and tool execution](../runtime/model-and-tool-execution.md) |
| Allow/deny/ask behavior or approval lifetime | [permissions](permissions.md) | [tool registry](tool-registry.md), [TUI README](../tui/README.md) |
| Pre/post tool hooks or async hooks | [hooks](hooks.md) | [query engine](../runtime/query-engine.md), [runtime services](../platform/runtime-services.md) |
| Slash commands and command lifecycle | [commands](commands.md) | [query engine](../runtime/query-engine.md) |
| Skill discovery, loading, activation | [skills](skills.md) | [context assembly](../runtime/context-assembly.md), [prefetch](../runtime/prefetch.md) |
| Plugin roots and contributed features | [plugins](plugins.md) | the owning capability doc for each contribution |
| Agent-side MCP or standalone MCP server | [MCP](mcp.md) | [entrypoints and transports](../platform/entrypoints-and-transports.md) |

## Review rule

For every capability change, identify all four boundaries: declaration, resolution, model-visible projection, and execution. If one is missing, label the feature partially wired instead of inferring behavior from package reachability.
