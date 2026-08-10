# Iteration Quality S5C Measurement Readiness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `$skill-runtime` for
> privacy/admission checks and `$iteration-workflow` for test-first execution
> and committed evidence.

**Status:** active-plan
**Created:** 2026-08-09
**Plan state:** Ready after executed F0; C1 is the first measurement slice

> **Ownership:** local advisory measurement and conditional optimization
> sequence in the
> [S5 completion design](../specs/2026-08-09-iteration-quality-s5-completion-design.md)

**Goal:** Replace unmeasured claims about test and hook cost with repeatable
local p50/p95 evidence while adding no normal-hook latency, CI usage, network
dependency, or sensitive persistence.

**Architecture:** `scripts/iteration metrics` reads existing diff-bound
evidence into a bounded aggregate report. `hook-benchmark` creates a disposable
committed fixture and compares two otherwise identical shell adapters whose
final process is current `go run` or a once-built binary. Both commands are
opt-in and advisory; neither changes evidence or hook settings.

**Tech stack:** Go 1.26.5 standard library, existing evidence JSON, temporary
Git repositories, synthetic hook JSON, nearest-rank percentiles, and Make
wrappers. No telemetry backend or third-party benchmark package.

## Frozen invariants

- Metrics and benchmark output contain only allowlisted target/mode, outcome
  counts, sample count, and aggregate milliseconds.
- Never emit or persist prompt, response, source, transcript, argv, command
  output, environment, path, branch, commit, diff digest, session, or child ID.
- Read at most the newest 256 evidence files and require at least five executed
  durations before reporting percentiles.
- Blocked and not-applicable gates do not contribute duration.
- Metrics are read-only and cannot affect `evidence_ready`.
- The benchmark runs only when explicitly requested, uses synthetic input,
  removes its fixture, and is absent from hooks and CI.
- No hook optimization is accepted merely because a candidate is faster; it
  must also define binary freshness and failure fallback in a later slice.
- External security tooling and remote policy remain conditional, not green.
- Start only after `make change-evidence-ready` exists and is the shared
  closeout assertion.

## PR C1: aggregate existing gate evidence

### Task 1: add failing aggregate tests

Create `scripts/iteration/metrics_test.go`.

- [ ] Add table tests for no data, fewer than five durations, exact nearest-
  rank p50/p95, mixed outcomes, stable target/level ordering, and the 256-file
  bound.
- [ ] Add strict admission tests for unknown evidence fields, negative
  duration, symlink traversal, malformed digest directories, unreadable files,
  plan/directory identity mismatch, and target not expected by its plan/level.
- [ ] Assert output contains none of the privacy marker values placed in plan,
  path, branch, diff, log, and session-like fixture fields.
- [ ] Put a secret-shaped marker directly in `gate.target`; require the entire
  artifact to be rejected with a bounded generic diagnostic that does not echo
  the marker.

```bash
go test ./scripts/iteration -run '^TestMetrics' -count=1
```

Expected first result: metrics symbols or command are missing.

### Task 2: implement the read-only report

Create `scripts/iteration/metrics.go` and wire `metrics` in
`scripts/iteration/main.go`.

- [ ] Discover only validated evidence files below an explicit local root,
  sort by modification time with a stable path tie-break, and cap at 256.
- [ ] Load through stored-plan identity validation, then require every target
  to pass `safeTarget` and belong to `expectedTargets(plan, level)`. Generic
  evidence decoding alone is insufficient admission.
- [ ] Project only validated target, level, status, and duration into the
  aggregator; an invalid artifact contributes no partial counts.
- [ ] Count every valid outcome; calculate duration percentiles only from pass
  and fail results with positive duration.
- [ ] Emit versioned JSON and concise Markdown with states `no_data`,
  `insufficient_samples`, or `ready`.
- [ ] Add `make iteration-metrics` without adding it to `verify`, hooks, or CI.

```bash
go test ./scripts/iteration -run '^TestMetrics' -count=1
go run ./scripts/iteration metrics --root build/iteration --format json
make iteration-metrics
```

### Task 3: close C1

```bash
make fmt
make lint
make test
make build
```

- [ ] Commit only the metrics command, tests, and Make wrapper; produce current
  merge evidence. Report `insufficient_samples` as that state, not as a trend.
- [ ] Finish with `make change-evidence-ready` on the committed head.

## PR C2: benchmark the complete hook adapter path

### Task 1: freeze the comparison contract

Create `scripts/iteration/hook_benchmark_test.go`.

- [ ] Test run-count bounds, nearest-rank aggregation, deterministic mode
  ordering, cleanup after command failure, sanitized JSON output, and that both
  modes execute the same shell/`git rev-parse` adapter prefix.
- [ ] Use an injected process runner/clock for unit tests; do not sleep.

```bash
go test ./scripts/iteration -run '^TestHookBenchmark' -count=1
```

### Task 2: implement an explicit benchmark command

Wire `hook-benchmark --runs <5..100> --format json` into
`scripts/iteration/main.go`.

- [ ] Require a clean committed tracked tree because the fixture is created
  from `HEAD`; untracked content remains excluded and untouched.
- [ ] Create a temporary repository from the committed archive, initialize its
  local `origin/master`, and use a unique synthetic session per sample.
- [ ] Measure `wrapper_go_run` with the production wrapper. Build
  `scripts/iteration` once, then create a test-owned candidate wrapper whose
  bytes and commands are identical through shell startup and `git rev-parse`
  and whose only substitution is the final `exec` command.
- [ ] Measure `wrapper_prebuilt_binary` through that candidate wrapper. Reject
  the benchmark if the two wrappers differ anywhere except the frozen final
  process invocation; never compare wrapper execution with direct binary
  execution.
- [ ] Validate empty/bounded hook output as appropriate, aggregate p50/p95 in
  memory, remove the fixture, and print no per-sample data.
- [ ] Add `make iteration-hook-benchmark`; do not add it to any automatic target.

```bash
go test ./scripts/iteration -run '^TestHookBenchmark' -count=1
go run ./scripts/iteration hook-benchmark --runs 7 --format json
make iteration-hook-benchmark
```

### Task 3: close C2 without changing hook settings

```bash
make fmt
make lint
make test
make build
```

- [ ] Commit the benchmark independently from C1 and produce current merge
  evidence.
- [ ] Finish with `make change-evidence-ready` on the committed head.
- [ ] Record the comparison in the PR description. Do not claim token, cost, or
  elapsed-time savings outside the measured fixture.

## Conditional follow-up, not admitted by this plan

After C2, a separate proposal may replace `go run` with a cached binary only if
the local comparison shows material interactive cost and the proposal solves:

- source/config freshness after checkout or pull;
- Darwin/Linux/Windows installation and executable permissions;
- missing/stale/corrupt binary fallback;
- hooks-disabled operation; and
- zero change to privacy, evidence, or stop-continuation semantics.

Likewise, external vulnerability/secret/SAST gates require an admitted tool,
version, database/network policy, billing owner, false-positive policy, and
required/blocked semantics. Until then they remain deferred; S5C must not add a
placeholder green target.

## Completion

S5C is complete when both advisory commands are deterministic and privacy-safe,
their committed diffs pass `make change-evidence-ready`, and no automatic hook,
CI, or security policy changed. Optimization remains a measured follow-up
decision.
