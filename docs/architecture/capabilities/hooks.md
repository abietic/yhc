# Hooks

**Status:** current
**Last verified:** 2026-08-07

> **Ownership:** `engine/hooks.Executor`, with canonical QueryEngine/ProjectGraph lifecycle integration

## Current Hook Lifecycle

The hook executor combines programmatic hooks and configured shell hooks for
prompt, tool, compact, session, turn, notification, agent, stop, and async
lifecycle events. QueryEngine owns the executor lifetime; `executeToolCall`
owns the tool-hook ordering.

## Tool lifecycle

- Pre-tool programmatic hooks run before configured shell hooks. Results are
  aggregated; updated input flows to later hooks and permission checks.
- Permission behavior uses `deny > ask > allow`. An allow is not authority to
  override a rule denial.
- Exit code `2` from a pre-tool shell hook is blocking. Other non-zero exits are
  surfaced as hook-failure attachments and do not execute as successful hook
  output.
- Post-tool hooks can replace the result, attach context, or prevent another
  model turn. Execution failures use the post-tool-failure branch.
- `continue: false` is represented as a stop/prevent-continuation decision and
  must survive through the query terminal decision.

## Shell and async lifecycle

Shell hooks use their configured timeout or `DefaultShellHookTimeout`. On Unix,
the child starts in a process group so cancellation/timeout can terminate
descendants; Windows uses the platform-specific process helper. The executor,
not a detached goroutine, owns async shell hooks and joins or cancels them on
shutdown. An async `asyncRewake` completion with exit code `2` can request a
later engine turn through the owning transport.

QueryEngine binds the hook executor to its immutable execution-policy digest
independently from Bash. Synchronous shell hooks receive that identity before
spawn; asynchronous hooks retain the snapshot captured at dispatch even after
the launching turn ends. The current adapter is explicitly `disabled` and
preserves ambient host authority, so this binding is not OS containment.

User-prompt hooks run before normal turn submission. A rejected prompt does not
enter ProjectGraph; an updated prompt and additional context become the
submitted input.

Registration deep-clones the shell-hook configuration under the executor lock.
Tool and prompt execution, plus read-only `/hooks` inspection, each consume a
detached snapshot of the exact executor-owned generation. Inspection therefore
does not re-read configuration files as an alternate runtime truth.

## Invariants and edge cases

- Tool order is pre-hook, permission, execution, post-hook. Changing this is an
  observable contract change.
- Hook timeout and parent cancellation must terminate descendants, not only the
  immediate shell process.
- Async results are drained at safe query boundaries; they do not publish into
  the model history concurrently.
- Stop hooks and long-session memory services are QueryEngine-owned and are not
  present in the independent MCP server.

## Code references

- [`Executor`](../../../engine/hooks/hooks.go)
- [`Executor.ExecutePreTool`](../../../engine/hooks/hooks.go)
- [`Executor.ExecutePostTool`](../../../engine/hooks/hooks.go)
- [`Executor.ExecuteUserPromptSubmit`](../../../engine/hooks/hooks.go)
- [`Executor.ShellHookSnapshot`](../../../engine/hooks/hooks.go)
- [`ExecuteShellHook`](../../../engine/hooks/shell.go)
- [Unix process-group termination](../../../engine/hooks/shell_process_unix.go)
- [`executeToolCall` hook ordering](../../../engine/tool_execution.go)
- [`RunStopHooks`](../../../engine/hooks/stop_hooks.go)

## Related tracking

Do not encode planned hook behavior as current. Use [`migration/PLAN.md`](../../migration/PLAN.md)
and [`migration/REMAINING.md`](../../migration/REMAINING.md) for unfinished depth work.
