# P24.4 TUI Goal Workflow

**Created:** 2026-07-29
**Completed:** 2026-07-29
**Status:** historical
**Adoption:** `adapt`

> **Ownership:** completion evidence for P24.4. Current Goal execution,
> runtime-input, command, configuration, and TUI behavior belongs in
> [`query-engine.md`](../../../architecture/runtime/query-engine.md),
> [`input-queue.md`](../../../architecture/runtime/input-queue.md),
> [`commands.md`](../../../architecture/capabilities/commands.md),
> [`configuration.md`](../../../architecture/platform/configuration.md), and
> [`sessions.md`](../../../architecture/state/sessions.md). Executable order
> belongs in [`migration/PLAN.md`](../../PLAN.md).

## User Problem

P24.3 could persist and recover one exact Goal continuation, but every
production transport deliberately ignored it. A TUI user could not create,
inspect, edit, pause, resume, budget, or clear a Goal. The root model had no
bounded completion/blocker surface, and an idle TUI could not continue an
eligible saved Goal.

P24.4 closes the first user-visible transport boundary without making Goal a
generic queue item or enabling it for another entrypoint.

## Delivered Contract

Goal is an off-by-default saved-root TUI capability. Configuration merges
`goal.enabled` and `goal.default_token_budget` field by field, rejects a zero
budget, and ships no numeric default. Without an explicit positive user or
host budget, creation persists a paused draft and cannot wake automatically.

The compiled command registry adds one TUI-only, idle-only engine command:

- `/goal` reads the detached snapshot;
- `/goal <objective>` creates through the existing Goal transition owner;
- `/goal edit <objective>`, `pause`, `resume`, `clear`, and
  `budget <positive-tokens>` carry typed intent through the command executor;
  and
- no TUI path pre-applies Goal state.

The model tool pool adds `get_goal` and
`update_goal(status, reason, blocker_key)`. QueryEngine filters both out unless
the current request is an enabled saved-root TUI Goal turn. `get_goal` returns
a detached JSON projection. `update_goal` accepts only `complete` or
`blocked`; it cannot create, edit, pause, resume, clear, or change budget.
Three distinct Goal turns with the same normalized blocker key remain required
before `blocked`.

Completion remains engine-owned. A matching completion request becomes
`complete` only at a completed terminal with settled provider accounting, no
required wait or foreground child, no earlier queued user steering, and a
successful complete checkpoint. Goal terminal commit and busy-TUI user enqueue
share the existing provider boundary, so their order is explicit rather than
a check-then-act race.

## Dedicated TUI Continuation

`RuntimeInputCoordinator` keeps its generic signal and selectors unchanged.
It adds one coalesced Goal-only signal that also surfaces a recovered pending
item. QueryEngine exposes gated Goal claim and submission methods only when the
saved-root TUI capability is enabled. Generic subscription, claim, safe-point
selection, and `SubmitRuntimeItem` still reject Goal continuation.

The idle TUI waits on both signals. Ordinary runtime input is selected first;
only then may it claim the exact Goal item and submit through the existing
P24.3 admission, receipt, accounting, and settlement path. User input,
permission decisions, cancellation, Plan, and Goal controls retain their
existing priority and permanent-supersession behavior.

The status line reads `RuntimeStateStore` and projects bounded objective,
status, provider tokens, effective budget, coverage, reason, and the existing
engine-owned active time. Rendering owns no eligibility or transition.

## Capability And Rollback Boundary

Plain, headless, ACP, child/review, ephemeral, administration, disabled TUI,
and standalone MCP contexts expose no Goal command, model tool, subscription,
claim, or submission capability. Entering Plan and Goal activation remain
mutually exclusive.

Disabling the capability blocks new creation and claim, then recovery durably
pauses an active Goal and rejects or settles every unadmitted item without
rewriting a delivered disposition. Persisted Goal state and committed usage
remain readable; no Goal item is reinterpreted as generic steering. A squash
revert of P24.4 removes only the configuration, command/tool projection,
dedicated notification, TUI consumer, and progress surface. It does not remove
the P24.1-P24.3 durable schema or ledger.

## Evidence

- [Goal capability, dynamic tools, and safe-terminal completion](../../../../engine/goal_capability.go)
- [typed Goal command application](../../../../engine/goal_command.go)
- [model Goal tool execution](../../../../engine/goal_tool.go)
- [child prompt isolation](../../../../engine/subagent.go),
  [standalone MCP exclusion](../../../../server/mcp/server.go), and
  [generic tool-search exclusion](../../../../tools/tool_search.go)
- [dedicated coordinator notification and gated claim](../../../../engine/input_coordinator.go)
- [TUI scheduling and submission](../../../../internal/tui/queued_input.go)
- [reducer-owned TUI projection](../../../../internal/tui/app.go)
- [configuration merge and validation tests](../../../../engine/config/goal_test.go)
- [command, tool, completion, blocker, wake, kill-switch, and steering tests](../../../../engine/goal_workflow_test.go)
- [TUI wake/submission and reducer tests](../../../../internal/tui/goal_workflow_test.go)
- [real PTY command/projection/terminal-restoration test](../../../../internal/tui/goal_workflow_pty_unix_test.go)

Focused P24.4 engine/TUI tests and their race variants pass. Repository
closeout uses `make fmt`, `make lint`, `make lint-new`, `make test`,
`make build`, `make docs-check`, and `make docs-check-ci`; the closeout is not
complete unless every local gate passes.

## Remaining Program

P24.5a, P24.5b, P24.5c, and P24.6 remain separate queued slices. P24.4 does
not make Plain automatic, add a headless process-lifetime contract, negotiate
ACP Goal capability, or promote Goal to default-on.
