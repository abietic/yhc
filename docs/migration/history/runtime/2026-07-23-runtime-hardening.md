# 2026-07-23 Runtime Hardening Closeout

**Status:** historical
**Completed:** 2026-07-23
**Last verified:** 2026-07-23

> **Ownership:** delivery boundaries, adoption decisions, compatibility impact,
> and verification evidence for the post-P13-P18 TUI startup, ProjectGraph
> interrupt admission, TodoWrite permission/state hardening, and iteration
> closeout-gate reliability

## Outcome

Three independently reviewable fixes closed the iteration without changing the
single ProjectGraph execution owner or adding an Eino/Eino-ext fork.

| Delivery | Problem closed | Decision | Result |
|---|---|---|---|
| [PR #73](https://github.com/abietic/eino-agent/pull/73) | `make run` could remain on `Initializing...` because bounded output hid the terminal descriptor Bubble Tea needs for its initial window-size event. | `project-native` | Renderer writes remain bounded by `TerminalOutput`; a descriptor-preserving adapter exposes the real terminal only for size and resize detection. |
| [PR #74](https://github.com/abietic/eino-agent/pull/74) | An unrelated Agent completion could be claimed as a new prompt while a durable ProjectGraph permission interrupt still owned continuation. | `project-native` | `ClaimNextRuntimeItem` admits only the exact permission decision during an active interrupt and leaves all other runtime items pending for later delivery. |
| [PR #75](https://github.com/abietic/eino-agent/pull/75) | TodoWrite unnecessarily opened a permission interaction and stored normal model calls under a shared global fallback. | `adapt` | TodoWrite is default-allowed after explicit rules, available in Plan Mode, and keyed by trusted runtime SessionID/AgentID without being classified as read-only. |

The closeout process also exposed two ordering faults in the required CI gate.
[PR #77](https://github.com/abietic/eino-agent/pull/77) closed them as one
`project-native` gate-determinism repair: the first Go setup is now the only
dependency-cache restore owner, while the minimum-version setup only selects
the `go.mod` toolchain; and the Unix PTY workflow observes monitor close plus
the restored-size frame before sending `Ctrl+C`. Production runtime behavior
and the minimum Go, lint, test, and build coverage remain unchanged.

## Preserved Invariants

- Bubble Tea still owns rendering and terminal mode transitions; every project
  frame still crosses one bounded writer and ordered restoration boundary.
- A durable Graph interrupt remains checkpoint/resume authority. Permission
  identity and current policy are revalidated before tool execution.
- Deferred Agent notifications, prompts, and hook wakeups are not dropped or
  converted into Graph decisions.
- TodoWrite still crosses tool selection, schema validation, repeated-call,
  Pre/Post hook, result, and transcript/event paths.
- Explicit TodoWrite `deny` and `ask` rules remain authoritative. Standalone
  MCP strict mode still rejects TodoWrite because it mutates state.
- Leader and child Todo lists remain process-local and isolated by the exact
  runtime Session/Agent pair; model input cannot choose another scope.

## Non-Goals

- No new Graph topology, Eino dependency, Eino/Eino-ext source change, durable
  schema, Agent lifecycle, or transport-specific queue owner was added.
- TaskCreate, TaskUpdate, and other task tools did not inherit TodoWrite's
  permission default.
- Todo state did not become durable session storage.
- Completed P13-P18 programs were not reopened or renumbered.
- CI reliability work did not relax a required gate, increase the PTY timeout,
  or skip the minimum-supported-Go test/build path.

## Evidence

Focused coverage included:

- the Unix PTY initial-size and restoration path, including race execution;
- exact ProjectGraph interrupt/resume with a deferred Agent completion, plus
  its race path;
- Todo default, `dontAsk`, Plan, explicit deny, and explicit ask admission;
- no Todo-created Graph interrupt on default paths;
- trusted leader/child Session/Agent state isolation and forged input
  rejection by non-use; and
- registry metadata proving TodoWrite is default-allowed but not read-only or
  permission-required.

The final code closeout passed `make fmt`, `make lint`, `make test` (4,452
tests), `make build`, `make lint-new`, `make docs-check`, migration manifest
validation, focused race tests, and `git diff --check`. PR #77 then passed the
same required remote formatting, lint, minimum-Go, test, and cross-platform
build gate without a second cache restore; the synchronized PTY test also
passed ten consecutive focused runs and three race runs locally.

## Current Owners

| Boundary | Current owner |
|---|---|
| Terminal descriptor and bounded output | [`runTUI` and `tuiProgramOutput`](../../../../cmd/yhc/cmd/root.go#L235), with PTY evidence in [`TestTUITerminalRestorationPTY`](../../../../cmd/yhc/cmd/terminal_lifecycle_unix_test.go#L39) |
| Graph-interrupt queue admission | [`QueryEngine.ClaimNextRuntimeItem`](../../../../engine/queued_input.go#L132), with deferred-delivery evidence in [`TestP138ProjectGraphInterruptResumeExecutesToolExactlyOnce`](../../../../engine/graph_hitl_test.go#L20) |
| Todo permission default | [`QueryEngine.wrapCanUseTool`](../../../../engine/engine.go#L2133) and [`defaultPermissionAllowedTools`](../../../../tools/registry.go#L915) |
| Todo runtime state scope | [`GetTodoListForAgent`](../../../../tools/todo_write.go#L49) and [`TodoWriteTool`](../../../../tools/todo_write.go#L194) |
| Required gate cache and PTY ordering | [CI workflow](../../../../.github/workflows/ci.yml) and [`TestTUIWorkflowPTY`](../../../../internal/tui/pty_workflow_unix_test.go#L25) |

Current behavioral documentation is in
[`terminal-lifecycle.md`](../../../architecture/tui/contracts/terminal-lifecycle.md),
[`permissions.md`](../../../architecture/capabilities/permissions.md), and
[`tool-registry.md`](../../../architecture/capabilities/tool-registry.md).
The next accepted work, if any, belongs in [`PLAN.md`](../../PLAN.md).
