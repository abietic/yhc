# P29.5 Observation Readiness

**Status:** verification
**Last verified:** 2026-08-01

> **Ownership:** readiness evidence for deciding whether retained local data can
> satisfy P29.5's profile/role/attempt baselines. This file does not own routing
> behavior, promote P29.5, or authorize adaptive health. The owning planning
> outcome is the
> [`P29.5 Defer Decision`](../plans/p29-model-portfolio-routing.md#p295-defer-decision).

## Result

P29.5 is not measurement-ready. Current production execution emits a safe,
process-local attempt event, but no retained owner preserves the complete
profile/role/attempt facts required to calculate retry amplification, recovered
requests, discarded tokens, latency percentiles, or role-specific quality.
Missing observations are unavailable evidence, not observed-zero failure or
cost rates.

A bounded one-machine structural check found ordinary cumulative provider-usage
snapshots but no durable, aggregate-capable record that joins the complete
model-attempt, failure, and latency dimensions for this repository. The bounded
runtime ring retains only a subset of those dimensions. That check opened owned
configuration and transcript files while projecting only field presence and
aggregate counts; it emitted and retained no prompt, response, credential,
endpoint, or transcript text. It is
therefore useful for diagnosing the missing measurement owner, but it is not a
content-free product surface or representative multi-profile evidence.

At readiness review, this result kept P29.5 blocked. The later owning decision
records `defer`, so `rate_limited`, `transport_unavailable`, Retry-After
cooldown, half-open behavior, and P29.6 remain outside executable scope.

## Evidence Boundary

| Required P29.5 fact | Current retained evidence | Readiness result |
|---|---|---|
| Profile, role, attempt, failure, retry, switch, call count, and latency | [`ModelAttemptEvent`](../../../engine/events.go#L275) carries these facts while the process is live. The event is not persisted or reconstructed after restart. | unavailable after the live turn |
| Bounded runtime replay/debug state | [`RuntimeEventRecord`](../../../engine/runtime_state.go#L281) retains only attempt ID, profile, phase, and failure class; [`runtimeEventRecord`](../../../engine/runtime_state.go#L1755) deliberately drops provider, model, route, retry, switch, call-count, latency, and output-disposition fields. The ring may evict older rows. | insufficient for the required aggregate |
| Prompt, completion, and cache tokens by attempt | [`UsageSummary`](../../../engine/transcript/usage.go#L10) persists cumulative prompt/completion totals and coverage only. It has no profile, role, attempt, failure, cache, or latency dimension. | insufficient and not joinable to attempt events |
| Exact attempt token attribution | [`GoalUsageRecord`](../../../engine/transcript/goal_usage.go#L21) has profile/attempt/cache attribution only for provider calls admitted by the opt-in Goal lifecycle. It does not cover ordinary TUI, plain, headless, ACP, or library turns. | entrypoint-specific; not a P29.5 denominator |
| Existing administration output | `config show` loads the active diagnostic snapshot, while `sessions list` reads transcript metadata regions to derive titles and session fields. Their JSON output does not expose the required profile/role/attempt/failover coverage. | neither a content-free scanner nor a P29.5 report |
| Quality and recovery budget | P29.4 deterministic fixtures prove routing correctness, not a retained real-workload quality or recovery distribution. | no promotion threshold can be derived |

The current runtime contract explicitly states that attempt events and
tombstones are process-local and are not reconstructed after restart; see
[`runtime-events.md`](../../architecture/tui/contracts/runtime-events.md).

## Reproduction

First verify the source-owned schemas and the deterministic P29.4 behavior:

```bash
go test ./engine -run '^TestP294' -count=1
go test ./engine/provider -run '^TestP294' -count=1
go test ./engine/execution -run '^(TestP294|TestCallModelWithRetry)' -count=1
go test ./internal/tui -run '^TestP294' -count=1
```

Do not use `config show`, `doctor`, or `sessions list` as a content-free P29.5
scanner. Their outward projection is redacted, but their current implementation
may load transcript content or metadata regions and still omits the required
profile/role/attempt coverage. Do not infer observations from session titles,
prompts, assistant text, provider response bodies, or credentials. The gate
passes only when a project-owned bounded report can prove complete denominators
and coverage without exposing those fields.

## Future Re-entry Rule

Adaptive health may return to accepted planning only when one reviewed evidence
set proves all of the following:

1. non-zero, explicitly scoped profile/role/attempt denominators from the
   production attempt owner;
2. prompt, completion, cache, discarded, retry, latency, terminal failure, and
   recovered-request coverage with missing metadata reported separately;
3. bounded cardinality and retention, stable redaction, no raw content or
   credential material, and no routing authority in the measurement path;
4. deterministic multi-provider fixtures plus an isolated real-repository
   scenario set;
5. explicit retry-amplification, discarded-token, recovery, latency, cost, and
   role-quality thresholds; and
6. a source-backed `adapt` or `defer` decision with no runtime routing diff.

If representative observations remain unavailable or their coverage is
insufficient, the correct P29.5 outcome is `defer`. That decision must be made
in a separately reviewed planning phase. The owning plan has now made that
decision explicitly; this readiness audit remains its evidence input rather
than the decision owner.

## Limitations

- The local observation is a machine-specific readiness check, not a portable
  performance baseline and not evidence about other installations. It opened
  local owned files while emitting only structural aggregates; no claim of
  content-free access is made.
- P29.4's canonical traces deliberately omit latency and failed output bytes;
  passing them cannot prove P29.5 budgets.
- Cumulative transcript usage can prove token accounting coverage, but cannot
  recover discarded-attempt attribution after the process exits.
- No live provider calls or background probes were added for this audit.
