# YHC User and Operator Guides

**Status:** current
**Last verified:** 2026-07-15

> **Ownership:** supported user and operator workflows for the current binary

These guides describe the behavior of the current binary. They are for running
and operating YHC; implementation design belongs in the
[architecture index](../architecture/README.md).

## Choose a task

| Goal | Guide |
|---|---|
| Build the binary and run a first prompt | [Getting started](getting-started.md) |
| Use the local Electron workbench | [Desktop workbench](desktop-workbench.md) |
| Select a provider or understand precedence | [Configuration and providers](configuration-and-providers.md) |
| Choose TUI, plain, headless, ACP, or MCP mode | [Interaction modes and commands](interaction-modes-and-commands.md) |
| Define safe unattended tool access | [Permissions and safety](permissions-and-safety.md) |
| Resume or inspect persisted conversations | [Sessions and transcripts](sessions-and-transcripts.md) |
| Add MCP servers, skills, or plugin commands | [Extensions](extensions-mcp-skills-plugins.md) |
| Diagnose a failed startup or missing feature | [Troubleshooting](troubleshooting.md) |

## Current boundaries

- `QueryEngine` and its public-Eino Compose ProjectGraph own conversation
  execution for TUI, plain, headless, and ACP entrypoints.
- `yhc serve mcp` is a separate tools-only stdio server. It does not
  construct `QueryEngine`.
- Headless mode has no interactive permission prompt. Explicit allow rules and
  other deterministic allow paths still apply; `-y` is the broad bypass.
- Runtime plugin bootstrap currently wires prompt commands only. Plugin-declared
  skills, hooks, and MCP servers have loader APIs but are not bootstrapped.
- `/rewind` is a non-discoverable removed-command tombstone; dispatch returns
  explicit guidance and never restores files.

These are current-state constraints, not statements about planned P13 work.

## Maintainer reference

| Boundary | Source |
|---|---|
| Cobra entrypoints | [`root.go`](../../cmd/yhc/cmd/root.go), [`serve.go`](../../cmd/yhc/cmd/serve.go) |
| Conversation authority | [`engine.go`](../../engine/engine.go), [`query_kernel_selection.go`](../../engine/query_kernel_selection.go), [`graph_query_kernel.go`](../../engine/graph_query_kernel.go) |
| Tool assembly | [`registry.go`](../../tools/registry.go), [`engine.go`](../../engine/engine.go) |
| Architecture | [Runtime](../architecture/runtime/README.md) |
