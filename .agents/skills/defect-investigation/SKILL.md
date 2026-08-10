---
name: defect-investigation
description: Reproduce, localize, fix, and verify YHC defects and regressions with source-backed evidence, minimal independent oracles, risk-matched Go tests, safe session inspection, and PTY or Computer Use end-to-end checks. Use for bugs, flaky failures, unknown regressions, issue triage, root-cause analysis, or fix verification. Diagnose without modifying code when the user asks only for analysis.
---

# Defect Investigation

Turn a symptom into a reproducible causal explanation and, when authorized, a
verified fix. Do not jump from a review comment, timeout, or historical report
straight to a patch.

Read [`docs/contributing/testing-strategy.md`](../../../docs/contributing/testing-strategy.md)
before choosing evidence. Use
[`references/e2e-scenarios.md`](references/e2e-scenarios.md) when the defect
needs a process, session, terminal, ACP, or desktop-level check.

## Telemetry admission

Apply `$skill-runtime` admission before investigation. Skip the run when the
task remains a short, local, read-only diagnosis with no Terra delegation or
final gate. Otherwise start an audit run immediately before the first trigger
with `skill=defect-investigation`, `kind=defect-investigation`, and the smallest
stable subsystem, issue, or reproduction scope.

When a run exists, record only applicable decision-bearing milestones:
`intake_frozen`,
`reproduction_finished`, `cause_localized`, `regression_added`,
`e2e_finished`, and `closeout_finished`. Use `not_reproduced`,
`oracle_ambiguous`, `environment_blocked`, `fix_not_authorized`,
`regression_failed`, or `logging_failed` as stable terminal categories. The
shared runtime owns data minimization, Terra accounting, and every exit's
finish.

## Freeze the Investigation Contract

Write down, in the working notes rather than a new repository document unless
the task requests one:

- observed symptom and exact entrypoint;
- expected observable behavior;
- current tree or binary identity, OS, terminal, and relevant configuration;
- the smallest known trigger and reproduction rate;
- destructive or private-data boundaries;
- whether the user authorized diagnosis only, a test, or an implementation.

Treat comments and historical claims as hypotheses. Separate facts already
observed, inferences still needing proof, and environment assumptions. If the
user requested diagnosis only, stop after causal evidence and a proposed
regression; do not edit production code.

## Trace the Real Owner

1. Inspect `git status --short` and preserve unrelated user changes.
2. When `.codegraph/` exists, use CodeGraph before broad search to locate the
   composition root, observable entrypoints, state owners, and call paths.
3. Read current source and focused tests before tracker prose. Use
   `PROJECT_DIRECTION.md` only when scope or reference adoption is involved.
4. Identify every boundary crossed by the symptom: runtime, tool/provider,
   permission, queue, persistence/replay, entrypoint adapter, terminal/UI, or
   environment.
5. Select one primary owner. Do not spread a speculative fix across layers to
   make the symptom disappear.

## Reproduce Before Repair

Reduce the symptom to one bounded command and one explicit oracle. Prefer this
order:

1. existing focused test;
2. a new failing unit or contract test using an independent expected value;
3. a temporary hermetic repository or process fixture;
4. a real PTY scenario for terminal bytes and process lifecycle;
5. Computer Use only for a remaining UI-only claim.

For public-headless repository outcome, contained Write, permission, cleanup,
usage, or report-publication symptoms, prefer the existing P43 harness through
`make eval-baseline EVAL_REPORT=<fresh-test-owned-path>` over creating a second
fixture. Its report is no-replace and its scripted provider does not prove live
providers, recovery, other entrypoints, or OS containment.

Capture the first failure, seed, typed terminal reason, event ordering, and
side-effect state needed to distinguish causes. Do not paste prompts, model
responses, credentials, cookies, environment dumps, or raw transcripts into
logs, issues, fixtures, or skill telemetry.

If the failure is timing-sensitive, replace sleep-based coordination with a
barrier, channel, wait group, virtual time, or deterministic hook before using
repetition as evidence. A retry that happens to pass is not a reproduction.

## Localize the Cause

For each plausible cause, state a prediction that would differ if it were
false. Use the smallest diagnostic that separates the hypotheses:

- ordered public events and typed terminal state for runtime ownership;
- a side-effect or durable-write journal for exactly-once and crash questions;
- a fresh process for resume, replay, catalog, and environment inheritance;
- raw PTY protocol bytes for terminal input/output and restoration;
- a second decoder, frozen fixture, or semantic invariant for parser and wire
  formats;
- focused `-race`, fuzz, or `testing/synctest` evidence where the testing
  strategy says the method is applicable.

Name the causal defect, the enabling condition, and the observable consequence.
Also name any nearby risk that was inspected and ruled out. Do not label a
package, line, or last changed commit as the root cause without the causal link.

## Repair and Preserve a Regression

When implementation is authorized:

1. Add the smallest test that fails for the localized cause, not merely for a
   copied error string.
2. Keep the observable contract, ordering, permission, cancellation,
   persistence, recovery, and cleanup invariants explicit.
3. Change the owning layer only. Update provenance comments when the project
   deliberately changes a reference-derived contract.
4. Rerun the minimal reproduction, then the matching Makefile risk pack from
   the testing strategy.
5. If the repair changes current behavior or an accepted evolution contract,
   update only its owning documentation.

Do not add an assertion, mocking, BDD, snapshot, or property framework unless
it supplies a missing capability and independent oracle that standard Go
testing cannot provide.

## Inspect Sessions Without Leaking Them

Start with structured administration output, normally:

```bash
yhc sessions list --output-format json
```

Restrict the project, fields, and count before reporting anything. Session
titles, paths, providers, and timestamps can still be private; summarize only
the structural facts required for the defect. Do not run `sessions export`,
print JSONL records, quote prompts/responses, or bulk-read the compatibility
transcript directory by default. Inspect raw content only with explicit user
authorization and a stated redaction plan.

When mining prior sessions for E2E cases, use only pre-sanitized categorical
records with `scripts/session_shape.py --input <sanitized.jsonl> --output json`.
It accepts exactly entrypoint, tool kind, event kind, terminal reason, and
lifecycle transition atoms; it never reads a transcript or emits source paths.
Convert repeated shapes into a sanitized scenario with setup, action, oracle,
cleanup, and privacy boundary; never turn one user's prompt into a committed
fixture.

## PTY and Computer Use Boundary

Escalate in this order:

```text
typed/runtime state -> package contract -> real process -> PTY bytes/modes
-> Computer Use only for remaining OS/window/pixel claim
```

Use a real PTY or existing PTY Go test for terminal byte protocols, alternate
screen modes, paste/focus/mouse modes, resize, signals, child exit, panic, and
restoration. Always close the PTY, terminate/wait for children, and restore
terminal state even when the test fails.

Use Computer Use only when the remaining claim depends on actual font fallback,
pixel clipping, OS clipboard permission, application focus, window management,
or another desktop integration that structured state and PTY bytes cannot
prove. Record the platform/app version, scenario ID, visible claim, authorized
redacted screenshot, and cleanup. Computer Use is supplementary evidence,
never the sole CI oracle, and cannot promote a failed or blocked structured
gate.

## Defect Intake and Close the Loop

Use [`templates/defect-intake.md`](templates/defect-intake.md) for the exact
intake. Do not add prompts, source dumps, transcripts, credentials, environment
dumps, or speculative fixes before localization.

Keep this causal loop intact:

```text
freeze symptom -> reproduce one independent oracle -> falsify hypotheses
-> identify one owner -> add the lowest stable red test -> repair minimally
-> make verify-focused -> commit -> make verify-merge -> evidence handoff
```

Name three evidence axes separately:

- the regression command and its applicable risk pack, such as
  `make test-e2e`, `make test-race`, or `make test-pty`;
- the persisted iteration lifecycle:
  `make verify-focused -> commit -> make verify-merge`; and
- optional diagnosis-only discovery through `make verify-deep`.

Never call a risk pack “merge evidence.” A risk pack may be selected inside
merge verification, but only `make verify-merge` can promote the exact
committed plan to `evidence_ready`.

Before declaring a fix complete, hand the regression and changed owner to
`$iteration-workflow` on the caller's current worktree. Report:

- confirmed symptom and root cause;
- changed owner and regression test;
- exact focused/risk-pack and repository-gate results;
- E2E entrypoint and cleanup result, if used;
- residual platform, provider, physical-terminal, or privacy boundary;
- whether unrelated user changes remain untouched.

If reproduction remains unavailable, return the highest-value next diagnostic
and what observation would decide it. Do not present a speculative patch as a
fix.
