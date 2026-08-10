# Runtime Architecture

**Status:** current
**Last verified:** 2026-08-07

> **Ownership:** change-oriented index for turn execution, context, recovery, queues, Agents, and limits

Use this group when changing turn semantics, model/tool execution, recovery,
compaction, or sub-agent behavior. A
[`QueryEngine`](../../../engine/engine.go) validates one durable
`project_graph/v1` pin, then invokes the shared canonical lifecycle through the
single ProjectGraph owner. Direct [`Query`](../../../engine/query.go) uses
the same compiled Graph. Historical Legacy and unpinned transcripts retain
diagnostic metadata but have no executor and fail closed on continuation. The
former imperative loop and fixture-only ADK adapters have been retired and are
not live fallbacks. The TUI and transports project the production runtime; they
do not own it.

## Change routes

| Change | Start here | Check next |
|---|---|---|
| Turn ordering, events, terminal reasons | [query engine](query-engine.md) | [typed errors](typed-errors.md), [input queue](input-queue.md) |
| Provider call options, streaming, tool dispatch | [model and tool execution](model-and-tool-execution.md) | [tool registry](../capabilities/tool-registry.md), [model providers](../platform/model-providers.md) |
| System/user context or attachments | [context assembly](context-assembly.md) | [prefetch](prefetch.md), [memory directory](../state/memory-directory.md) |
| Context pressure or summary boundaries | [compaction](compaction.md) | [budgets and limits](budgets-and-limits.md), [recovery](recovery.md) |
| Retry, failover, malformed streams | [recovery](recovery.md) | [model providers](../platform/model-providers.md), [typed errors](typed-errors.md) |
| Queued input or interrupt semantics | [input queue](input-queue.md) | [query engine](query-engine.md), [composer contract](../tui/contracts/composer.md) |
| Goal state, continuation, budgets, or Graph tools | [query engine](query-engine.md) | [input queue](input-queue.md), [entrypoints and transports](../platform/entrypoints-and-transports.md) |
| Prefetched memory or skills | [prefetch](prefetch.md) | [context assembly](context-assembly.md) |
| Task/sub-agent execution | [tasks and agents](tasks-and-agents.md) | [model and tool execution](model-and-tool-execution.md) |
| Max turns, token/task/USD/context limits | [budgets and limits](budgets-and-limits.md) | [compaction](compaction.md), [model providers](../platform/model-providers.md) |

## Current-state guardrails

- Event ordering and cancellation claims must be traced through the shared
  canonical lifecycle, the ProjectGraph traversal, and the current runtime
  event reducer.
- A package being in the command dependency closure does not prove that every API in it has a production caller.
- Entrypoint-owned services are documented under [platform runtime services](../platform/runtime-services.md), even when they feed the canonical lifecycle.
- ProjectGraph is the only production traversal. A Graph failure never replays
  through a retired kernel, and an incompatible historical Session fails before
  model or tool execution.
- The selected Graph depends only on the project-owned schedule and typed
  decision in `tool_schedule.go`; retired ADK construction, retry, resume, and
  checkpoint fixtures are not runtime dependencies.
