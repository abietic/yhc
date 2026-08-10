# P22 Enforcement And Promotion Readiness

**Status:** verification
**Last verified:** 2026-08-01

> **Ownership:** reproducible evidence for the P22.1c and P22.3a-P22.6
> planning decision. Current permission behavior belongs in the architecture
> and guide owners; execution order belongs in [`PLAN.md`](../PLAN.md); the
> detailed decision and future re-entry gate belong in
> [`p22-auto-permission-review.md`](../plans/p22-auto-permission-review.md#p22-remaining-slice-defer-decision).

## Outcome

P22.1c and P22.3a-P22.6 record `defer`. P22.H0-P22.2b already deliver the
deterministic action/policy boundary, exact settlement, an explicitly enabled
non-authoritative reviewer shadow, and a separately enabled bounded redacted
audit report. Current project-owned evidence cannot justify shell reviewer
eligibility, reviewer enforcement, capability expansion, default promotion,
or deletion of the legacy classifier.

No runtime, configuration, persistence, provider, permission, TUI, Plain,
headless, ACP, child, or standalone MCP behavior changes through this
decision. G14 remains reproduced but has no accepted successor. Missing
records are unavailable evidence, not zero latency, zero error, or zero safety
violations.

## Why No Implementation Slice Is Ready

The provider-free report observed on 2026-08-01 returned `status=no_data`:

| Required evidence | Current retained result | Consequence |
|---|---|---|
| Reviewer workload | 0 eligible actions, 0 reviewer attempts, and 0 terminal results | No real workload denominator exists. |
| Reviewer latency | `unavailable`, 0 samples | No p50/p95 or incremental-latency budget can be recorded. |
| Legacy comparison | `unavailable`, denominator 0 | No disagreement or false-allow rate can be inferred. |
| Direct-human comparison | `unavailable`, denominator 0 | Prompt reduction and human disagreement are unmeasured. |
| Versioned corpus | `unavailable`, denominator 0 | “Zero observed violations” is unproved because the required corpus denominator is absent. |
| Retained-data integrity | 0 valid, malformed, partial-tail, duplicate, orphan, incomplete, and unmatched records | The empty window is internally clean, but emptiness is not promotion evidence. |

[`ReviewAuditReport`](../../../engine/permission/review_audit_report.go) keeps
each denominator and unavailable state explicit. The shadow path in
[`permission_review.go`](../../../engine/permission_review.go) remains
advisory: failure, timeout, invalid output, or binding drift cannot authorize
or dispatch an action. Focused tests prove that deterministic safety contract;
they do not prove production usefulness or an acceptable enforcement budget.

P22.1c is also not independently justified. It would add a shell parser and
descriptor boundary for optional reviewer eligibility while enforcement itself
has no promotion evidence. It cannot create representative usage, and a partial
shell representation would enlarge the highest-risk authority surface while
G28 still records that allowed shell processes have ambient host authority.
Keeping incomplete or ambiguous shell actions human-required preserves the
current safety contract.

## Decision And Compatibility

The project keeps the P22 `combine` core and makes two explicit decisions:

1. **P22.1c:** `defer` the optional bounded-shell branch. No shell action gains
   reviewer eligibility.
2. **P22.3a-P22.6:** `defer` the enforcement, entrypoint/capability expansion,
   default-promotion, and old-owner-deletion chain. No successor is accepted.

QueryEngine remains the only permission authority. The separate reviewer and
audit sink remain explicit opt-ins and non-authoritative. Incomplete shell,
`Agent`/child, network, MCP/app/dynamic, and user-interaction actions remain
human-required in Auto absent exact user authority. The legacy classifier and
evaluator remain available; this decision deletes no compatibility or rollback
owner and closes neither G14 nor G28.

The project does not add traffic generation, provider probes, raw-content
capture, a second audit store, or broader shadow eligibility merely to make
the promotion report non-empty. Those mechanisms would add privacy, retention,
cost, and maintenance obligations without proving reduced prompt fatigue or a
safer user outcome.

## Reproduction

Inspect the current retained local window without constructing a provider:

```text
go run ./cmd/eino-agent permission-review-audit report --output-format json
```

The report must distinguish `no_data`, `retained_window`, and `partial`; an
unavailable denominator must never be interpreted as zero disagreement or zero
false allows. Verify the completed non-authoritative contract with:

```text
go test ./engine -run '^(TestPermissionReviewOffByDefaultAndChildDisabled|TestPermissionReviewShadowNeverChangesLegacyOutcomeOrBlocksIt|TestPermissionReviewTimeoutCancellationAndCrossDelivery|TestPermissionReviewFreshBindingRejectsRuntimeDrift)$' -count=1
go test ./engine/permission -run '^(TestBuildReviewAuditReportEvidenceStatus|TestReviewAuditStoreReportAndJSONMinimization)$' -count=1
```

These tests and an empty report reproduce the decision boundary; they cannot
manufacture the missing representative workload or promotion thresholds.

## Future Re-Entry Gate

A future proposal is new intake rather than a reopening of these slice IDs.
Before root `PLAN.md` may accept it, one reviewed evidence set must include:

1. a verified user problem showing that current deterministic policy plus
   human/fail-closed handling causes material prompt friction;
2. representative non-zero eligible, attempt, terminal, direct-human, and
   versioned-corpus denominators with explicit missing-data accounting;
3. approved p50/p95 reviewer and incremental-turn latency, unavailable/error,
   escalation, disagreement, false-allow, provider-cost, and prompt-reduction
   budgets;
4. zero hard-boundary and exact-action-binding violations across both the named
   security corpus and the measured cohort;
5. a privacy-reviewed bounded measurement owner with no raw inputs, host paths,
   credentials, transcript content, or routing authority;
6. an independent permission/security review and a rehearsed kill-switch
   rollback that returns every unresolved action to human/fail-closed handling;
   and
7. for shell eligibility specifically, a complete effect representation whose
   ambiguity, unsupported syntax, aliases, substitutions, redirections,
   wrappers, paths, and network effects all fail closed.

Private maintainer history, deterministic fixtures alone, absent data, or a
new telemetry mechanism without an independently verified user outcome cannot
satisfy this gate.
