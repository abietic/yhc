# P24.5b Bounded Headless Goal

**Created:** 2026-07-29
**Completed:** 2026-07-29
**Status:** historical
**Adoption:** entrypoint-local `combine` within P24 `adapt`

> **Ownership:** completion evidence for P24.5b. Current process behavior
> belongs in
> [`query-engine.md`](../../../architecture/runtime/query-engine.md),
> [`commands.md`](../../../architecture/capabilities/commands.md),
> [`sessions.md`](../../../architecture/state/sessions.md), and
> [`interaction-modes-and-commands.md`](../../../guides/interaction-modes-and-commands.md).
> Executable order belongs in [`migration/PLAN.md`](../../PLAN.md).

## Outcome

Automation now has one explicit bounded way to continue an existing saved
Goal:

```bash
eino-agent goal run \
  --resume SESSION_ID \
  --max-continuations 8 \
  --output-format json
```

The process accepts no prompt or stdin objective. It cannot create, edit,
pause, resume, clear, or change a Goal budget. Ordinary `exec` and compatibility
`-p` remain one-prompt, one-turn entrypoints.

## Runtime And Result Contract

The command installs the internal `headless-goal` composition identity only
for its engine. It resumes the exact saved Session, inspects detached
`GoalSnapshot` state, then claims and submits only
`RuntimeItemGoalContinuation` through the existing dedicated APIs. After each
submission it drains the complete canonical event stream and re-reads durable
Goal state. A completed query turn is never Goal success by itself.

The required positive continuation count bounds one process invocation. The
existing positive Goal token budget, provider-usage ledger, permission policy,
Plan exclusion, exact cursor validation, and cancellation checkpoint remain
engine-owned. During an exact admitted Goal turn, the model may see
`get_goal` and terminal `update_goal`; no slash command or model-created Goal
authority is added.

Text and JSON use one renderer-neutral result. JSON stdout contains one
versioned `goal_run` object with exact Session identity, full detached Goal
state, budget and usage coverage, continuation counters, final assistant
output, terminal reason, exit code, and a bounded redacted error. Diagnostics
stay on stderr. Durable `complete` alone exits `0`; valid non-complete halts
and failures exit `1`, usage exits `2`, and cancellation exits `130` only
after durable stop handling succeeds.

## Isolation And Evidence

- [bounded process driver and result schema](../../../../cmd/yhc/cmd/headless_goal.go)
- [CLI, durable cold-resume, status, failure, redaction, and text/JSON tests](../../../../cmd/yhc/cmd/headless_goal_test.go)
- [dedicated composition identity](../../../../engine/commands/registry.go)
- [saved-root Goal capability gate](../../../../engine/goal_capability.go)
- [exact claim and submission boundaries](../../../../engine/queued_input.go)
  and [submission owner](../../../../engine/input_sources.go)
- [cross-entrypoint capability tests](../../../../engine/goal_workflow_test.go)

The focused test uses a real persisted active Goal cursor: a new
`headless-goal` engine cold-resumes it, admits one exact continuation, exposes
`update_goal` only for that Goal turn, and commits durable completion. Focused
race tests cover the process driver plus existing cancellation and unsupported
entrypoint boundaries. Repository closeout uses `make fmt`, `make lint`,
`make lint-new`, `make test` (5,941 tests passed; one opt-in physical-terminal
diagnostic skipped), `make build`, `make docs-check`, `make docs-check-ci`,
manifest validation, and `git diff --check`; all required gates passed.

## Rollback

Disable the explicit headless Goal capability and verify no dedicated process
owns an admitted continuation. Then revert the `goal run` command, process
loop, result schema, and `headless-goal` composition identity together.
Versioned Goal state, provider usage, cursor recovery, TUI/Plain consumers,
ordinary headless behavior, and older readers remain unchanged.

## Remaining Program

P24.5c negotiated ACP Goal behavior and P24.6 measured default promotion remain
queued. No later P24 slice is executable until root `PLAN.md` promotes it.
