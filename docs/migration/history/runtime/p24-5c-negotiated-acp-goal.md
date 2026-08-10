# P24.5c Negotiated ACP Goal

**Created:** 2026-07-29
**Completed:** 2026-07-29
**Status:** historical
**Adoption:** `combine` within P24 `adapt`

> **Ownership:** completion evidence for P24.5c. Current engine behavior
> belongs in
> [`query-engine.md`](../../../architecture/runtime/query-engine.md), and the
> current transport contract belongs in
> [`acp-adapter.md`](../../../architecture/platform/acp-adapter.md).
> Executable order belongs in [`migration/PLAN.md`](../../PLAN.md).

## Outcome

ACP clients may opt into one private version-1 Goal protocol without changing
ordinary ACP:

- Initialize offer:
  `clientCapabilities._meta["eino-agent.goal"]={"versions":[1],"notifications":true}`;
- request methods: `_eino/goal/get`, `_eino/goal/control`, and
  `_eino/goal/continue`; and
- post-commit notification: `_eino/goal/updated`.

The first Initialize freezes the connection result. Missing, malformed,
unsupported, or late offers advertise nothing and receive MethodNotFound for
all Goal methods. Negotiation never enables the off-by-default Goal feature by
itself.

## Runtime Contract

New, Load, and Resume engines capture both the negotiated bit and effective
Goal configuration. The adapter accepts bounded strict JSON with exact
Session/request identity. Control carries typed create, edit, pause, resume,
budget, or clear intent plus optimistic Goal revision. QueryEngine
`ApplyGoalControl` serializes that intent with existing TUI/Plain controls and
delegates every durable transition to the existing Goal service. Same-state,
stale, duplicate, Plan-owned, unavailable, or in-flight requests return typed
conflict truth and publish no transition notification.

Continuation additionally binds Goal ID, Goal revision, objective revision,
and continuation ordinal. It acquires the Session's single prompt owner,
claims only through `ClaimNextGoalContinuation`, submits only through
`SubmitGoalContinuation`, and reuses the canonical ACP event and ProjectGraph
permission driver. The adapter drains the complete producer, then re-reads
durable Goal state and publishes one versioned result/notification. It never
calls `SubmitMessage`, originates `session/prompt`, polls, or widens generic
runtime-item claims.

Ordinary Prompt cancellation remains context-local. For a Goal continuation,
Cancel, request-context cancellation, delivery failure, and Session close
first cross the engine-owned durable stop boundary while exact Session
ownership is held, then cancel and join the event producer. A delivery-unknown
client may inspect current truth but cannot replay the old revision/cursor.

## Evidence

- [connection negotiation, Session ownership, cancellation, and shared event
  driver](../../../../server/acp/agent.go)
- [strict private protocol and detached projection](../../../../server/acp/goal_extension.go)
- [typed QueryEngine control adapter](../../../../engine/goal_control.go)
- [capability and exact-claim gates](../../../../engine/goal_capability.go)
  and [dedicated claim](../../../../engine/queued_input.go)
- [engine control and concurrency tests](../../../../engine/goal_control_test.go)
- [wire, construction, control, continuation, cancellation, and delivery
  tests](../../../../server/acp/goal_extension_test.go)

Focused engine and ACP tests plus their race variants pass. Repository
closeout uses `make fmt`, `make lint`, `make lint-new`, `make test`,
`make build`, `make docs-check`, `make docs-check-ci`, manifest validation,
and `git diff --check`.

## Rollback

Stop advertising `eino-agent.goal`, reject the three request methods, and
disable negotiated ACP Goal admission before removing the adapter. Existing
Goal state, usage, continuation cursors, TUI/Plain/headless consumers, old
readers, and ordinary ACP remain unchanged. A restored active Goal stays
durable and dormant until another supported consumer explicitly continues it.

## Remaining Program

P24.6 measured default promotion remains queued. It requires real
usage/cost/latency denominators and rollback evidence; P24.5c supplies no
default-on decision or numeric budget.
