# Platform Architecture

**Status:** current
**Last verified:** 2026-08-07

> **Ownership:** change-oriented index for composition roots, configuration, providers, onboarding, notifications, and services

Use this group for process composition, configuration, providers, transports, onboarding, notifications, and services whose lifecycle depends on an entrypoint.

## Change routes

| Change | Start here | Required cross-check |
|---|---|---|
| Settings precedence or filesystem locations | [configuration](configuration.md) | affected runtime/capability owner |
| Model aliases, credentials, routing, fallback | [model providers](model-providers.md) | [model and tool execution](../runtime/model-and-tool-execution.md), [budgets and limits](../runtime/budgets-and-limits.md) |
| TUI/plain/headless/ACP/MCP process composition | [entrypoints and transports](entrypoints-and-transports.md) | [runtime services](runtime-services.md), [query engine](../runtime/query-engine.md) |
| Goal exposure in TUI, Plain, dedicated headless, or ACP | [entrypoints and transports](entrypoints-and-transports.md) | [query engine](../runtime/query-engine.md), [ACP adapter](acp-adapter.md) |
| ACP negotiation, projection, replay, or client behavior | [ACP adapter](acp-adapter.md) | [entrypoints and transports](entrypoints-and-transports.md), [sessions](../state/sessions.md), [commands](../capabilities/commands.md) |
| First-run behavior | [onboarding](onboarding.md) | [configuration](configuration.md) |
| Desktop/terminal notifications | [notifications](notifications.md) | [TUI architecture](../tui/README.md), owning entrypoint |
| Background memory, shutdown, resume rebinding | [runtime services](runtime-services.md) | [sessions](../state/sessions.md), [memory directory](../state/memory-directory.md) |

## Composition rule

The platform layer constructs the runtime but does not redefine turn semantics. Check every lifecycle claim separately for TUI, plain REPL, headless, ACP, standalone MCP, and sub-agent compositions.
