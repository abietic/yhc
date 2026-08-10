# P24.6 Default-Promotion Readiness

**Status:** verification
**Last verified:** 2026-08-01

> **Ownership:** reproducible evidence for the P24.6 default-promotion
> decision. Current Goal behavior belongs in the architecture and guide
> owners; accepted execution order belongs in [`PLAN.md`](../PLAN.md); the
> detailed decision and re-entry gate belong in
> [`p24-durable-goal-lifecycle.md`](../plans/p24-durable-goal-lifecycle.md#p246-default-promotion-decision).

## Outcome

P24.6 records `defer`. P24.1-P24.5c already deliver a complete explicit opt-in
Goal lifecycle, but current project-owned evidence cannot justify enabling it
by default or shipping one numeric token budget. No runtime, configuration,
Session, transcript, provider, command, TUI, Plain, headless, or ACP behavior
changes through this decision.

Missing records are unavailable evidence, not zero cost, zero latency, or zero
failure. Deterministic safety tests remain required evidence for the opt-in
runtime; they are not representative user-adoption or affordability data.

## Why The Promotion Gate Fails

| Required evidence | Current project-owned evidence | Decision |
|---|---|---|
| Representative Goal usage denominator | Goal state and usage records exist only after explicit enablement and execution. The repository owns no bounded cohort report or representative non-zero session denominator. | Unavailable; do not infer adoption from missing or private operator data. |
| Provider-cost distribution | `GoalUsageRecord` stores provider-reported prompt, cached, completion, reasoning, total, and billable tokens. It stores no currency, rate, price-source revision, or cost snapshot. | Token accounting protects budgets but cannot prove monetary affordability. |
| Continuation-latency distribution | Goal state retains aggregate root active time and exact continuation identity. It does not own a report with defined enqueue-to-admission, admission-to-terminal, or end-to-end latency samples. | No p50/p95 or acceptable latency budget can be claimed. |
| Independent default-on lifecycle review | P24 slice reviews prove persistence, permission, accounting, recovery, cancellation, and entrypoint invariants under explicit opt-in. | They do not review a default-enabled cohort or select a default budget. |
| Rollback rehearsal | The current kill switch and drain ordering are specified and tested for opt-in behavior. No default-on rollout exists to rehearse against representative active Sessions. | Preserve the kill switch; do not claim a promotion rollback rehearsal. |
| Full default-on entrypoint matrix | TUI, Plain, bounded headless, and negotiated ACP have explicit capability gates; unsupported entrypoints fail closed. | Keep those gates. Do not widen discovery or activation by default. |

The relevant current owners are:

- [`config.DefaultConfig`](../../../engine/config/config.go), which disables
  Goal and supplies no default budget;
- [`QueryEngine.goalWorkflowEnabled`](../../../engine/goal_capability.go),
  which admits only explicitly enabled supported saved-root entrypoints; and
- [`transcript.GoalUsageRecord`](../../../engine/transcript/goal_usage.go),
  which is exact budget/recovery evidence rather than promotion telemetry.

## Decision And Compatibility

The adoption decision is `defer` for default-on promotion. The project keeps
the existing P24 `adapt` runtime and accepts no measurement-only implementation
slice. Adding a persistent audit owner solely to manufacture promotion data
would create privacy, retention, and maintenance obligations without an
independently verified user outcome. Such a future owner, if ever justified,
must remain non-authoritative and must not affect Goal recovery, permission,
budget, continuation, or terminal truth.

Compatibility is unchanged. Explicitly enabled saved-root TUI and Plain,
bounded `goal run`, and negotiated ACP continue to require a positive effective
budget. Disabled, ordinary headless, unnegotiated ACP, child/review,
ephemeral/administration, Plan, and standalone MCP paths gain no Goal
authority.

## Reproduction

Run the deterministic current-contract checks:

```text
go test ./engine/config -run '^TestP244GoalConfig' -count=1
go test ./engine/transcript -run '^TestP242bGoalUsageRecord' -count=1
go test ./engine -run '^(TestP242bNoGoalExposesNoProviderUsageCapability|TestP245aPlainUsesDedicatedGoalCapabilityWithoutGenericWidening)$' -count=1
go test ./server/acp -run '^TestP245cInitializeNegotiatesGoalCapabilityImmutably$' -count=1
```

Pass means the current default remains disabled, zero default budgets are
rejected, usage records retain exact token fields, unsupported execution lacks
Goal authority, and ACP requires immutable negotiation. These tests do not
manufacture the missing production denominators.

## Future Re-Entry Gate

A future default-promotion proposal is new intake. Before root `PLAN.md` can
accept it, evidence must include:

1. a named user problem that opt-in behavior cannot adequately solve;
2. a project-owned, privacy-reviewed, bounded report with representative
   non-zero sessions and explicit missing-data accounting;
3. immutable provider-price provenance or an explicit provider-neutral cost
   metric, plus defined continuation-latency boundaries and approved budgets;
4. zero duplicate, unauthorized, hard-boundary, accounting-coverage, and
   recovery violations across the named safety corpus and measured cohort;
5. independent lifecycle and security review; and
6. a rehearsed kill-switch rollback that pauses active Goals, settles pending
   continuation items, preserves committed usage, and starts no hidden work.

Private maintainer transcripts, deterministic fixtures alone, absent data, or
an unreviewed telemetry store cannot satisfy this gate.
