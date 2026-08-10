# P50.2 Reviewer Attempt-Latency Denominator Verification

**Status:** verification
**Last verified:** 2026-08-08

> **Ownership:** reproducible evidence that reviewer latency describes only
> retained audit events with both an attempt and a terminal result

## Contract

`BuildReviewAuditReport` keeps eligible, attempt, terminal, comparison, and
corpus denominators independent. One reviewer latency sample exists only when
the same retained event group contains both `reviewer_attempt` and
`terminal`. The terminal result may be completed, timed out,
reviewer-unavailable, or invalid; the attempt consumed measurable time in each
case.

A terminal-only setup or projection failure remains visible in outcome counts
and lifecycle diagnostics but contributes no reviewer latency. Attempt-only and
eligible-only groups also contribute no sample. With zero retained pairs,
latency remains `unavailable`, samples remain zero, and JSON omits `p50_ms`
and `p95_ms`.

## Test-First Evidence

Before the production admission change, the mixed lifecycle fixture reported
four samples with p95 40 from three attempts and four terminal results. The
terminal-only fixture independently reported one available latency sample.
Those failures pin the old denominator without depending on a model or clock.

The retained matrix then proves:

- completed, timeout, reviewer-unavailable, and invalid-result
  attempt-terminal pairs each contribute one sample;
- unsorted latencies `90, 10, 40, 20` produce nearest-rank p50 20 and p95 90;
- terminal-only projection failure, attempt-only, and eligible-only groups
  leave latency unavailable; and
- the original mixed report keeps four terminal outcomes while exposing only
  three latency samples, with p50 20 and p95 30.

## CLI Compatibility Evidence

The provider-free report command reads one completed attempt-terminal event and
one terminal-only projection failure from the real local audit store. Its
decoded report contains two terminal outcomes and one latency sample. A
separate terminal-only store produces unavailable latency and omits both JSON
percentile fields. Existing path-redaction, retention, and delete assertions
remain unchanged.

## Commands

```bash
go test ./engine/permission/ -run '^TestBuildReviewAuditReport' -count=1
go test ./engine/permission/ -run 'ReviewAudit' -count=1
go test ./engine/permission/ ./cmd/eino-agent/cmd/ -run 'ReviewAudit|PermissionReviewAudit' -count=1
go test -race ./engine/permission/ ./cmd/eino-agent/cmd/ -run 'ReviewAudit|PermissionReviewAudit' -count=1
make fmt
make lint
make test
make build
make docs-check
go run ./scripts/migration_queue check
go run ./scripts/migration_manifest.go check
git diff --check
```

All listed local commands pass on the final closeout tree. `make test`
completed 7,239 tests with three explicitly opt-in skips; the third-party ACP
SDK suite also passed. Remote CI remains a separate merge gate.

## Evidence Limits

This verifies retained-window measurement semantics, not reviewer quality,
representative latency, corpus coverage, or enforcement readiness. It does not
change reviewer routing, permission classification, prompting, grant
persistence, tool dispatch, audit retention, or the still-synchronous audit
sink lifecycle owned by prospective P50.3 intake.
