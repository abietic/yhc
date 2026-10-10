# Budgets and Limits

**Status:** current
**Last verified:** 2026-10-11

> **Ownership:** current enforcement and wiring state of turn, invocation usage, token, task, USD, context, and model-output limits

The runtime does not have one global budget. It has several independent controls with different owners and wiring states. Treating them as interchangeable would overstate current enforcement.

## Control matrix

| Control | Source of truth | Current enforcement | Scope | Wiring status |
|---|---|---|---|---|
| Maximum turns | [`TurnTracker.Advance`](../../../engine/turn_tracker.go) and [canonical after-tool check](../../../engine/round_lifecycle.go) | Stops before the next disallowed turn and returns `TerminalMaxTurns` | One query invocation | active |
| Per-turn token budget | [`TokenBudget`](../../../engine/budget/token.go) and [`Check`](../../../engine/budget/token.go) | Can emit a continuation nudge or request completion | Tracker instance | partially wired: production does not record input/output usage into it |
| Invocation call/token limits | [`RunUsage.AdmitProviderUsage`](../../../engine/execution/run_usage.go) | Pre-dispatch call cap; known-token stop at next admission, unknown usage stops a token-limited run | Shared invocation collector, including children and helpers | active in `exec`; opt-in injection elsewhere |
| API task budget | [`TaskBudget`](../../../engine/params.go), [`CallModelOptions.TaskBudget`](../../../engine/execution/call.go) | Sends Claude beta header and `output_config.task_budget`; remaining is reduced after compaction | Injected engine configuration / Claude request | partially wired: no default CLI configuration |
| USD budget | [`USDBudget`](../../../engine/budget/usd.go) | `Exceeded` can compare accumulated cost against a cap | Tracker instance | disconnected from production query enforcement |
| Context window | [`GetEffectiveContextWindowSize`](../../../engine/compact/auto.go) | Drives warning, auto-compaction, error, and blocking thresholds | Selected model / conversation | active |
| Model maximum output | [`ModelCapabilities.MaxOutputTokens`](../../../engine/model/capabilities.go) | Capability metadata is available; execution forwards an explicit override only when one is supplied | Model call | partially wired as metadata, not an automatic cap |

## Maximum turns

`MaxTurns == 0` means unlimited; negative values are rejected when configuration is built and again by engine/query guards. CLI resolution is flag, then environment, then config in [`resolveMaxTurns`](../../../cmd/yhc/cmd/root.go). The actual boundary is enforced by [`TurnTracker.Advance`](../../../engine/turn_tracker.go) in canonical ProjectGraph reconciliation, which emits `EventMaxTurnsReached` and returns `TerminalMaxTurns`.

This applies to any surface that submits through `QueryEngine`, including TUI, plain REPL, headless, ACP, and sub-agents. It is a turn-count boundary, not a model-call, tool-call, wall-clock, or token boundary.

The explicit `goal run` process adds a separate required positive
`--max-continuations` count. That count bounds how many exact durable Goal
continuations one process invocation may submit; it does not replace
`MaxTurns`, an optional positive Goal token budget, or exact provider-usage
accounting. A nil Goal budget disables only the Goal token limiter: provider
and usage accounting still accumulate. Adding a positive cap later compares it
with already committed usage, and zero remains invalid. Reaching the process
count returns `continuation_limited` and exit
`1` while preserving the active durable Goal for a later explicit invocation.

## Per-turn token budget and effort levels

[`TokenBudget.SetBudgetLevel`](../../../engine/budget/token.go) maps `low`, `medium`, `high`, and `max` to 1k, 4k, 16k, and 64k tokens. The canonical Graph runtime checks a configured tracker, or creates a query-local 200k tracker, before continuing.

The important current gap is accounting: production code does not call [`RecordInput`](../../../engine/budget/token.go) or [`RecordOutput`](../../../engine/budget/token.go). Consequently, the default query-local tracker remains at zero and does not impose an effective token stop. [`QueryEngine.GetTokenBudget`](../../../engine/engine.go) also returns only an explicitly injected tracker, so effort controls in UI/commands cannot mutate the query-local fallback.

The input parser recognizes token-budget continuations, but that parsed delta is not consumed by the current production loop. Document this as a seam, not as supported enforcement.

The root TUI's optional post-turn prompt suggestion is a separate side query,
not another round in the completed canonical query. It therefore does not
consume that query's `TokenBudget` or API `TaskBudget`. Its own boundary allows
one provider dispatch and caps output at 64 tokens. Provider-reported tokens
are persisted through a content-free auxiliary usage record while the
active-context reading continues to describe the latest main-loop response.

## Invocation usage and per-call history

`RunUsage` is the root concurrency-safe observer for one invocation. `exec`
constructs it even with both limits disabled and seals admissions before writing
the terminal JSON summary. Other embedding surfaces must inject the collector;
this does not install a persistent TUI cost ledger. Call and token limits default
to zero (disabled). A token threshold can be exceeded by already admitted
concurrent calls. Unknown usage remains unknown; it stops the next admission
only when the token limit is enabled. Existing durable Goal accounting composes
with this observer and retains its stricter settlement policy and wire identity.

The additive `usage.call_ledger` contains version 1, a generated invocation
`segment_id`, admission-ordered `records`, and `dropped_records`. Each retained
record has a generated call UUID, UUID-only round/request/attempt lineage,
zero-based attempt/retry indices, bounded provider/model/source/role/effort
labels, invocation start offset, call lifetime, and one settlement state:

| State | Meaning | Tokens |
|---|---|---|
| `in_flight` | Admitted but not settled | null |
| `known` | Valid cumulative provider usage settled once | Numeric input, cached/uncached input, output, reasoning, total |
| `unknown` | Dispatch may have occurred but usage is missing or invalid | null |
| `released` | Proven not dispatched; reservation refunded once | null |

Denied admissions have no call record and increment `denied_calls` instead.
Completion order never reorders admissions. Snapshots copy records and token
objects under the same root mutex. Repeated settlement cannot refund a completed
call or charge it twice. The first 1,024 admissions are retained, including
released reservations; later admissions increment `dropped_records` without
changing accounting or retry behavior. `usage.complete` still describes total
usage coverage, independently of truncated call history or unknown model names.

`requested_model` is the configured call label and may be an alias/profile;
`provider` is the descriptor's configured provider or `unknown`. The project
Agentic bridge observes `resolved_model` only from typed DeepSeek/GLM response
metadata for Stream and Generate. Absent, invalid, conflicting, or unsupported
response identities stay `unknown`; requested labels never prove the responding
model. Observation is an optional capability forwarded through combined owners;
it does not alter the durable Goal interface or session transcript. Empty model
metadata cannot overwrite an observation, and observations after settlement are
ignored. Auxiliary side-query retries now forward their retry index as well.

The ledger stores no prompt, reasoning text, command, stdout, error body,
credential, endpoint, or arbitrary profile body. Model identifiers must match a
bounded identifier syntax; unrecognized classification labels become
`other`/`unknown`, and non-UUID lineage becomes `unknown`. Call lifetimes include
client work from admission to settlement and can overlap. They do not enumerate
hidden SDK HTTP retries, external MCP/hook provider work, or server thinking
latency. Cached and reasoning tokens remain subsets; `uncached_prompt_tokens`
is input minus cache, never an additional billed token class.

Each resumed process emits a fresh incremental ledger. Harbor retains these as
`usage.call_ledgers`, checks duplicate segment IDs, and reports
`call_ledger_complete` separately from token completeness. Missing/invalid or
truncated histories do not erase valid aggregate tokens. Older records have no
invented per-call history. A process kill can still prevent terminal collection;
this bounded invocation history is not a crash-safe journal. No native-currency
price lock, per-call invoice, monetary admission cap, or stage reservation is
implemented by this observer. The existing offline cost report remains an
explicit rate-card estimate.

Source owners: [`RunUsage`](../../../engine/execution/run_usage.go) owns
admission/settlement; [`RunUsageLedger`](../../../engine/execution/run_usage_ledger.go)
owns bounded projections and response observation;
[`GenerateWithUsage`](../../../engine/execution/generate_usage.go) and
[`CallModel`](../../../engine/execution/call.go) bind the response observer to one
call; the [Agentic bridge](../../../engine/provider/provider.go) publishes typed
response identities; [Harbor usage validation](../../../scripts/terminal_bench/usage.py)
owns incremental history aggregation. [Headless usage tests](../../../cmd/yhc/cmd/headless_usage_test.go)
exercise the supported CLI and fresh resume boundaries with local HTTP fixtures.

## API task budget

The engine carries `{Total, Remaining}` through [`QueryParams`](../../../engine/params.go) into [`execution.CallModel`](../../../engine/execution/call.go). For Claude requests, it adds the task-budget beta header and serializes `output_config.task_budget` in [`buildClaudeTaskBudgetExtraFields`](../../../engine/execution/call.go). After compaction, canonical round preparation reduces `Remaining` by the pre-compaction token count.

No standard CLI composition currently initializes `QueryEngineConfig.TaskBudget`. This control is therefore available to injected/API construction paths and is provider-specific at the wire layer; it is not a universal model budget.

## USD budget

[`USDBudget`](../../../engine/budget/usd.go) can accumulate default-rate or per-model usage and report whether a cap is exceeded. No production path constructs it, records model usage into it, or terminates ProjectGraph through it. `QueryEngineConfig.MaxBudgetUSD` is copied into tool-use options, but there is no current enforcing consumer.

Cost reporting and hard cost enforcement are therefore not equivalent. Until a production owner wires accounting and a terminal policy, USD budget is a dormant capability.

## Context-window pressure

[`GetEffectiveContextWindowSize`](../../../engine/compact/auto.go) uses the centralized model capability table, a legacy fallback map, and finally a 200k default. `CLAUDE_CODE_AUTO_COMPACT_WINDOW` may lower, but not raise, the effective window. Explicit `[1m]` and `[2m]` model suffixes are parsed by [`splitContextSuffix`](../../../engine/model/capabilities.go).

Current threshold buffers are defined in [`engine/compact/auto.go`](../../../engine/compact/auto.go):

- auto-compaction: effective window minus 13k;
- warning and error: effective window minus 20k;
- manual blocking limit: effective window minus 3k.

These thresholds govern conversation context pressure. They do not consume or update `TokenBudget` or `USDBudget`.

## Model capability limits

[`GetCapabilities`](../../../engine/model/capabilities.go) resolves exact names, aliases, provider-qualified substrings, and a default for unknown models. `ContextWindow` actively feeds compaction. `MaxOutputTokens` is descriptive unless execution receives `CallModelOptions.MaxOutputTokens`; [`CallModel`](../../../engine/execution/call.go) forwards only that explicit override to Eino.

Provider defaults, recovery overrides, and capability-table maxima must therefore be kept distinct in reviews.

## Change checklist

When changing a budget or limit, verify:

1. where accounting is recorded;
2. where the decision is evaluated;
3. which terminal/event is emitted;
4. which entrypoints construct the control;
5. whether resume, compaction, retry, and sub-agent paths preserve it;
6. whether the provider wire format actually supports it.
