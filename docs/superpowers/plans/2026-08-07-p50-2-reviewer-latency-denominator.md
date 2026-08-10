# Reviewer Attempt-Latency Denominator Implementation Plan

> **Historical use:** Retained as completed implementation evidence; do not
> execute this checklist as current work.

**Status:** historical
**Completed:** 2026-08-08
**Created:** 2026-08-07
**Source snapshot:** `origin/master` at
`de74294b29f40d19bfd0e37f09889bd6f8037d90`

> **Ownership:** test-first delivery plan for P50.2, the second repair accepted
> by the [Permission Runtime Remediation Design](../specs/2026-08-07-permission-remediation-design.md)

**Goal:** Make reviewer p50/p95 and sample count describe only events for which
the reviewer was actually attempted and a terminal result was retained.

**Architecture:** `BuildReviewAuditReport` keeps the current event grouping and
outcome diagnostics. It changes only latency sample admission from
`terminal != nil` to `attempt != nil && terminal != nil`; setup failures remain
visible as unavailable outcomes but never masquerade as reviewer duration.

**Tech Stack:** Go 1.26.5, typed redacted reviewer-audit records, nearest-rank
percentiles, JSON report compatibility, CLI report tests, and Makefile gates.

## Global Constraints

- Execute only after root `docs/migration/queue.yaml` admits P50.2 as its sole
  `Ready` slice and P50.1 has reached a terminal queue state.
- Keep eligible, attempt, terminal, comparison, and corpus denominators
  independent. Do not repair missing records or invent a zero latency.
- A terminal-only setup/projection failure remains an unavailable outcome and
  may affect lifecycle diagnostics; it contributes no reviewer-latency sample.
- An attempt-terminal pair contributes one sample whether the terminal status
  is completed or unavailable, because the reviewer attempt consumed time.
- Do not rename the metric, change percentile math, or add a setup-latency
  metric in this slice.
- Preserve the existing JSON field names and zero-value omission behavior.

---

## Task 1: Pin the correct latency denominator with red fixtures

**Files:**

- Modify: `engine/permission/review_audit_report_test.go`

- [x] **Step 1: Correct the mixed lifecycle fixture's expected result**

In `TestBuildReviewAuditReportDecisionMetrics`, retain event 4 as an eligible
plus terminal-only `projection_unavailable` result with `LatencyMS = 40`, but
change the latency oracle to:

```go
if report.Latency.Status != ReviewAuditEvidenceAvailable ||
    report.Latency.Samples != 3 ||
    report.Latency.P50MS != 20 ||
    report.Latency.P95MS != 30 {
    t.Fatalf("Latency = %+v, want 3 attempt-terminal samples p50=20 p95=30", report.Latency)
}
```

Keep outcome counts at four eligible actions, three attempts, and four terminal
results.

- [x] **Step 2: Add a denominator matrix**

Create `TestBuildReviewAuditReportLatencyRequiresAttemptTerminalPair` with
subtests for:

- completed attempt + terminal;
- timeout attempt + unavailable terminal;
- reviewer error attempt + unavailable terminal;
- malformed-output attempt + unavailable terminal;
- projection-unavailable terminal without attempt;
- attempt without terminal; and
- eligible-only/no-attempt data.

For a fixture containing four attempt-terminal pairs with unsorted latencies
`90, 10, 40, 20`, require `samples=4`, `p50=20`, and `p95=90`. For fixtures with
no pair, require `status=unavailable`, `samples=0`, and omitted percentile JSON.

- [x] **Step 3: Run the focused red test**

```bash
go test ./engine/permission/ -run '^TestBuildReviewAuditReport(DecisionMetrics|LatencyRequiresAttemptTerminalPair)$' -count=1
```

Expected: FAIL because the current loop appends every terminal latency.

## Task 2: Admit latency only for attempt-terminal pairs

**Files:**

- Modify: `engine/permission/review_audit_report.go`
- Modify: `engine/permission/review_audit_report_test.go`

- [x] **Step 1: Move the append behind the attempt check**

Replace the unconditional terminal append with this exact admission rule:

```go
report.Outcomes.TerminalResults++
if group.attempt != nil {
    latencies = append(latencies, group.terminal.LatencyMS)
}
```

Do not require `eligible` or `ReviewerStatus == "completed"` for the latency
sample. Existing orphan and incomplete diagnostics remain responsible for
invalid lifecycle shape.

- [x] **Step 2: Keep no-pair evidence unavailable**

Retain the existing finalization guard:

```go
if len(latencies) > 0 {
    // sort and calculate nearest-rank p50/p95
}
```

Do not set `available` with zero samples.

- [x] **Step 3: Run focused green and all permission report tests**

```bash
go test ./engine/permission/ -run '^TestBuildReviewAuditReport' -count=1
go test ./engine/permission/ -run 'ReviewAudit' -count=1
```

## Task 3: Prove report and CLI compatibility

**Files:**

- Modify: `cmd/eino-agent/cmd/permission_review_audit_test.go`

- [x] **Step 1: Add a JSON omission assertion for zero attempts**

Marshal a no-pair report and require:

```go
if strings.Contains(string(encoded), `"p50_ms"`) ||
    strings.Contains(string(encoded), `"p95_ms"`) {
    t.Fatalf("unavailable latency exposed zero percentiles: %s", encoded)
}
```

- [x] **Step 2: Exercise the command report path**

Create a temporary audit store with one attempt-terminal pair and one
terminal-only projection failure. Run the existing report command seam and
require two terminal outcomes but one latency sample. Do not weaken path,
retention, redaction, or delete tests.

- [x] **Step 3: Run package-width tests**

```bash
go test ./engine/permission/ ./cmd/eino-agent/cmd/ -run 'ReviewAudit|PermissionReviewAudit' -count=1
```

## Task 4: Close P50.2 as a measurement correction only

**Files:**

- Modify: `docs/architecture/capabilities/permissions.md`
- Create: `docs/migration/verification/p50-2-reviewer-latency-denominator.md`
- Modify: `docs/migration/verification/README.md`
- Create: `docs/migration/history/runtime/p50-2-reviewer-latency-denominator.md`
- Modify: `docs/migration/history/README.md`
- Modify: `docs/migration/queue.yaml`
- Modify generated: `docs/migration/PLAN.md`

- [x] **Step 1: Document the denominator without claiming reviewer quality**

State that latency is retained-window evidence over attempt-terminal pairs.
Keep setup failures in outcomes and keep reviewer enforcement disabled. Remove
only P50.2 from the queue and render the next legal state.

- [x] **Step 2: Run final repository gates**

```bash
make fmt
make lint
make test
make build
make docs-check
go run ./scripts/migration_manifest.go check
git diff --check
```

- [x] **Step 3: Commit and open one atomic metrics PR**

```bash
git add engine/permission/review_audit_report.go engine/permission/review_audit_report_test.go cmd/eino-agent/cmd/permission_review_audit_test.go docs/architecture/capabilities/permissions.md docs/migration
git commit -m "fix(permission): measure reviewer attempt latency"
```

The PR must state that this changes measurement semantics only, with no
permission, reviewer, prompt, or tool-dispatch effect.
