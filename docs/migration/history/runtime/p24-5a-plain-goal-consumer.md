# P24.5a Plain Goal Consumer

**Created:** 2026-07-29
**Completed:** 2026-07-29
**Status:** historical
**Adoption:** `adapt`

> **Ownership:** completion evidence for P24.5a. Current Goal admission and
> Plain behavior belongs in
> [`query-engine.md`](../../../architecture/runtime/query-engine.md),
> [`commands.md`](../../../architecture/capabilities/commands.md), and
> [`interaction-modes-and-commands.md`](../../../guides/interaction-modes-and-commands.md).
> Executable order belongs in [`migration/PLAN.md`](../../PLAN.md).

## Outcome

Saved-root Plain now exposes the same off-by-default engine-owned `/goal`,
`get_goal`, and `update_goal` authority as TUI and automatically consumes the
existing dedicated Goal continuation. The transport adds no second Goal state,
queue, transition service, or generic runtime-item path.

The earlier Plain loop started one blocking `ReadString` goroutine per wait.
Cancellation or a Goal wake could therefore leave an abandoned reader able to
consume a later permission answer or user line. P24.5a replaces that ownership
with one process-lifetime broker and one idle driver.

## Input And Admission Contract

`plainInputBroker` is the only Plain `ReadString` caller. It emits ordered typed
line/EOF results to both idle admission and permission/Plan interaction. A
cancelled consumer leaves the same reader alive, so the next consumer receives
the pending line without another goroutine competing for stdin. A final
non-empty partial line is delivered once before EOF shutdown.

At an idle boundary the driver drains a completed line before observing the
coalesced Goal wake. After a wake it checks exact ProjectGraph permission
ownership, rechecks completed input and context cancellation, then calls only
`ClaimNextGoalContinuation`. A claimed cursor enters only through
`SubmitGoalContinuation`; generic subscription, claim, safe-point selection,
and `SubmitRuntimeItem` remain unchanged.

## Output And Shutdown Contract

One Plain writer owns prompts, command results, permission questions,
assistant/tool events, diagnostics, and Goal output. Automatic work prints a
bounded `[Goal continuation]` line followed by stable lifecycle facts. The
internal continuation prompt is never presented as user-authored input.

Normal EOF, `/exit`, and process-context cancellation call the existing
QueryEngine stop owner. An active Goal is checkpointed as paused and any
unadmitted continuation is permanently retired before exit. A pause
persistence failure is returned instead of starting another provider call.

## Capability Boundary

The command registry and Goal tool projection now admit only enabled saved-root
TUI or Plain engines. Plan, disabled, ephemeral, child/review, headless, ACP,
administration, and standalone MCP contexts remain excluded. The shipped
feature default stays off and no numeric token budget is introduced.

## Evidence

- [process-lifetime input broker](../../../../cmd/yhc/cmd/plain_input.go)
- [Plain idle/input/Goal driver and renderer](../../../../cmd/yhc/cmd/plain_repl.go)
- [interactive Goal capability gate](../../../../engine/goal_capability.go)
- [dedicated Goal claim and submission](../../../../engine/queued_input.go)
  and [submission boundary](../../../../engine/input_sources.go)
- [TUI/Plain command projection](../../../../engine/commands/registry.go)
- [broker, precedence, EOF, cancellation, and at-most-once tests](../../../../cmd/yhc/cmd/plain_input_test.go)
- [Plain integration and output tests](../../../../cmd/yhc/cmd/root_test.go)
- [real PTY continuation and EOF process test](../../../../cmd/yhc/cmd/plain_goal_pty_unix_test.go)
- [cross-entrypoint command/tool and generic-path exclusion tests](../../../../engine/goal_workflow_test.go)

Focused Plain/Goal tests and race variants pass. Repository closeout uses
`make fmt`, `make lint`, `make lint-new`, `make test`, `make build`,
`make docs-check`, `make docs-check-ci`, manifest validation, and
`git diff --check`.

## Rollback

Disable Plain Goal capability first, use the engine stop owner to pause active
Plain work, retire any unadmitted cursor, and verify no Goal item is
processing. Then revert the Plain broker/driver, renderer, command projection,
and tool-capability extension together. TUI Goal behavior, versioned Goal
state, provider usage, and the dedicated dormant item remain readable; no
rollback path reinterprets a Goal item as generic steering.

## Remaining Program

P24.5b explicit bounded headless execution, P24.5c negotiated ACP control, and
P24.6 measured default promotion remain separate queued slices.
