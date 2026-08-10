# Testing Strategy

**Status:** current
**Last verified:** 2026-08-09

> **Ownership:** test purposes, evidence boundaries, risk-pack selection, and
> rules for promoting a defect reproduction into durable regression coverage

## What Testing Must Prove

The project does not optimize for the largest test count. It optimizes for an
independent, reproducible answer to four questions:

1. Did the observable contract remain true?
2. Did the change preserve ordering, cancellation, permission, persistence,
   recovery, and cleanup invariants at the affected boundary?
3. Can a failure be reproduced with one bounded command and a clear oracle?
4. Does the evidence cover the real entrypoint and failure mode, rather than a
   helper that merely resembles them?

`make test` remains the ordinary full-suite gate. The focused targets below add
risk-specific evidence; none of them replaces the four repository gates in
[`verification.md`](verification.md).

## Evidence Layers

| Layer | Use it for | Required oracle | Main limitation |
|---|---|---|---|
| Unit and table tests | Pure transformations, validation, state transitions, and error mapping | Explicit inputs and outputs independent of the implementation helper | Does not prove composition-root wiring or process behavior |
| Property and fuzz tests | Parsers, Unicode, serialization round trips, merge algebra, and malformed input | Invariant or second decoder, plus a retained minimal corpus for every discovered defect | Only explores the executed target and budget |
| Package integration tests | Ownership across a few real collaborators | Externally visible result, side-effect journal, or typed terminal state | In-process fixtures do not prove restart or terminal behavior |
| Scenario and contract tests | Query loop, tool lifecycle, entrypoint projection, permissions, cancellation, and recovery | Ordered event trace plus final state and side effects | A shared production helper can create a same-source oracle |
| Golden tests | Stable wire projections, terminal frames, and event sequences | Reviewed fixture with an explicit update command | Brittle for timestamps, random IDs, provider text, or physical rendering |
| Replay and fault tests | Durable writes, crash points, resume, fork, and fail-closed recovery | Fresh-process result plus proof of no duplicate dispatch or partial activation | Mock persistence alone cannot prove filesystem/process failure behavior |
| Race tests | Exactly-once settlement and shared-state ownership | Deterministic barriers and the Go race detector | A green run covers only schedules and paths reached by the test |
| PTY tests | Terminal bytes, resize, input parsing, child-process exit, and terminal-mode restoration | Raw protocol bytes, process status, and cleanup state | Does not prove fonts, pixels, OS clipboard permission, or desktop focus |
| Benchmarks | Hot-path diagnosis and comparison against a named baseline | Stable fixture, sample count, environment, and budget | A single local number is not a regression gate |
| Hermetic repository evaluation | End-to-end task outcome, allowed writes, policy, recovery, turns, and cost | Frozen repository fixture and outcome/policy rubric | Scripted providers do not prove live-provider reliability |
| Live-provider or Computer Use checks | Provider canaries and UI-only desktop behavior | Redacted observation with an explicit human-visible boundary | Nondeterministic and unsuitable as the default CI oracle |

## Standard Go Methods First

Use the standard `testing` package unless another library supplies an oracle or
execution capability that the standard library cannot provide.

- Use [Go fuzzing](https://go.dev/doc/security/fuzz/) for malformed and
  property-oriented input. Run one fuzz target per invocation, keep a bounded
  smoke budget in routine work, and retain minimized regressions in the package
  corpus.
- Use the [Go race detector](https://go.dev/doc/articles/race_detector) on
  deterministic concurrency scenarios. Replace `time.Sleep` coordination with
  channels, barriers, wait groups, or test hooks before treating a repeated
  race run as evidence.
- Use [`testing/synctest`](https://pkg.go.dev/testing/synctest) for new pure-Go
  tests whose correctness depends on goroutine quiescence or virtual time. Do
  not use it for PTYs, real subprocesses, filesystem locks, or ACP stdio.
- Use `testing/quick` or a small hand-written generator only when its property is
  clearer than a fuzz target and its seed can be reported.

Do not add a BDD, assertion, mocking, snapshot, or property framework merely to
shorten syntax. A new dependency must demonstrate a missing capability,
deterministic diagnostics, maintenance ownership, and lower total complexity.

## Executable Risk Packs

The Makefile owns the stable commands and time budgets. Select the smallest pack
that reaches the changed risk boundary, then still run the ordinary repository
gates before closeout.

| Target | Boundary | When to run |
|---|---|---|
| `make test-contract` | Query loop, terminal events, replay snapshots, transcript replacement, ACP load/delivery | Runtime, session, persistence, projection, or ACP changes |
| `make test-race` | Permission, Goal continuation, restore staging, ACP replay and reconnect settlements | Shared mutable state, cancellation, exactly-once, or lifecycle changes |
| `make test-pty` | Plain/TUI real-process workflow and terminal restoration | TUI, terminal, signal, input, resize, or process-lifecycle changes on Unix |
| `make test-fuzz-smoke` | Command parsing and TUI Unicode/annotation round trips | Parser, Unicode, serialization, selection, or display-cell changes |
| `make test-risk` | Contract, race, and PTY packs | Broad runtime changes before final gates; fuzz remains separately budgeted |
| `make test-e2e` | Real built binary with a local scripted provider, plus engine, ACP, permission-race, and PTY seams | Runtime, tools, CLI, ACP, permission-race, or terminal changes selected by the risk plan |
| `make check-boundaries` | New repository-internal production/test import edges and new packages below flat roots | Every non-documentation plan; only new forbidden production edges or flat-root erosion fail |
| `make verify-deep` | Diff-selected, bounded fault, fuzz, E2E, and PTY discovery | Opt-in diagnosis when ordinary focused evidence is insufficient; stops at the first fail or required block |
| `make eval-baseline` | P43 public-headless hermetic repository outcome, write-policy, cleanup, usage, and redacted report | Evaluation-harness changes or an explicit opt-in product baseline check; never as a default correctness gate |

Override bounded budgets only when the reason is recorded, for example
`make test-fuzz-smoke TEST_FUZZ_TIME=30s`. A timeout increase is not a fix for a
missing synchronization point.

These targets are non-default. Promoting one into required CI needs measured
runtime and flake evidence; until then, report whether the applicable pack ran,
failed, or was inapplicable instead of implying that `make test` covered it.
`eval-baseline` uses the current `localized-write-fix/v1` scripted-provider
scenario and is intentionally outside `verify`, required CI, and release
builds. Its pass does not prove live-provider quality, recovery, non-headless
entrypoints, or OS-wide process/network containment. Report publication is
deliberately no-replace. For a repeated run, choose a fresh test-owned path,
for example:

```bash
make eval-baseline EVAL_REPORT=build/evaluation/p43-second-run.json
```

Do not delete or overwrite a report whose ownership is uncertain merely to
turn a collision into a pass.

`test-e2e` is deterministic product evidence, not a substitute for the other
packs. It invokes the built binary against a local scripted provider and checks
independent file hashes, Git status, and prior tool-result assertions. Its
Make target also executes the selected engine, ACP, permission-race, and PTY
seams. The real-binary pack owns six bounded scenarios: permission rejection,
read/edit/test execution, malformed tool input, streamed cancellation,
write-based session resume, and typed-overload fallback. Each claim is limited
to its scenario's independent oracle; a green run is not live-provider or
Computer Use evidence.

## Persisted Iteration Evidence

`make verify` remains the ordinary full-suite gate. Iteration evidence is an
additional, diff-bound record:

- `make verify-focused` runs the plan-selected focused checks and persists the
  exact result for that diff digest.
- `make verify-merge` accepts only focused evidence for the same digest. On a
  named, clean non-`master` topic branch with no merge or rebase in progress, it
  may explicitly move complete successful focused evidence from the otherwise
  identical pre-commit plan to the new head. It rejects any other plan change
  and never moves failed, blocked, or merge results. It then runs `fmt`,
  rebuilds the plan, and refuses stale focused evidence before running
  applicable merge checks. It promotes sequentially through `merge_verified`
  to `evidence_ready` only when every applicable gate is complete.
- Evidence and bounded per-target logs live under
  `build/iteration/<diff-digest>/`; a pass removes its temporary log.
- The first executed failure is immutable: a retry cannot overwrite it. A
  `blocked` placeholder with no execution result may be replaced when that
  target first actually runs.

`docs-check` is `not_applicable` in iteration evidence when `.reference` is
absent, while `docs-check-ci` still runs. `coverage.json` is an advisory
derived from the test profile; it never becomes a required gate or changes
`evidence_ready`.

Race, PTY, fuzz, live-provider, and Computer Use checks remain separate
evidence classes. They answer different questions and must not be inferred
from focused, merge, or hermetic E2E success.

Boundary checks compare global base/head import-edge sets while reporting
production and test-only edges separately. They reject only newly introduced
forbidden production edges and new package directories below configured flat
roots; existing diagnostics are not a growing exception baseline.

`make verify-deep` selects from `test-fault-injection`, `test-fuzz-deep`,
`test-e2e-deep`, and `test-pty-deep` using the diff's risks. A passing run
is emitted as diagnosis-only evidence and cannot change `evidence_ready`.
Only the first `fail` or required `blocked` result is persisted as the
strict `build/iteration/<diff-digest>/deep-intake.json`; retry cannot replace
it. Unsupported PTY platforms are `not_applicable` and do not hide later
targets.

## Escalation to PTY and Computer Use

Escalate only as far as the remaining claim requires:

```text
typed/runtime state -> package contract -> real process -> PTY bytes/modes
-> Computer Use only for remaining OS/window/pixel claim
```

Computer Use acceptance records the platform and app version, scenario ID,
visible claim, an authorized redacted screenshot, and cleanup result. It is
supplementary and cannot promote a failed or blocked structured gate. Risk
packs such as `make test-e2e` and `make test-pty` remain distinct from the
persisted `make verify-focused -> commit -> make verify-merge` lifecycle.

## Choosing Coverage by Changed Risk

| Changed boundary | Minimum added evidence |
|---|---|
| Query loop, model/tool lifecycle, compact, or cancellation | Contract scenario with ordered events, typed terminal reason, and side-effect assertions |
| Session, transcript, checkpoint, or durable catalog | Corruption and crash-point test, fresh-process resume, and no replay-with-dispatch |
| Permissions, hooks, queues, or ownership handoff | Happy, reject, timeout/cancel, and exactly-once race scenario |
| ACP, MCP, plain, headless, TUI, or child Agent wiring | Shared fixture across every applicable entrypoint; discovery alone is insufficient |
| Parser, Unicode, annotations, or wire encoding | Examples plus round-trip/malformed-input fuzz or property invariant |
| Terminal lifecycle or rendering | Deterministic reducer/golden coverage plus PTY; use Computer Use only for the remaining physical UI claim |
| Performance-sensitive path | Correctness test first, then named benchmark fixture and budget with environment recorded |

## Defect-to-Regression Loop

1. Freeze the symptom: entrypoint, version/tree identity, OS/terminal, expected
   and actual behavior, terminal reason, and a redacted structural trace.
2. Reduce it to one command and one oracle. Preserve a failing artifact only if
   it contains no prompt, credential, private transcript, or machine-specific
   path.
3. Classify the owner: runtime, entrypoint adapter, persistence/replay,
   presentation/terminal, provider, environment, or test oracle.
4. Add the smallest regression that fails for the causal reason. Prefer an
   independent expected trace or side-effect journal over production code
   generating its own expected value.
5. Apply the fix, rerun the reproduction, the matching risk pack, and ordinary
   repository gates. Repeat stress only where deterministic synchronization
   makes repetition meaningful.
6. Record residual boundaries explicitly: compile-only, Unix PTY, physical
   terminal, live provider, or unreproduced environment.

Use [`$defect-investigation`](../../.agents/skills/defect-investigation/SKILL.md)
for the reusable investigation flow, strict pre-sanitized session-shape
aggregation, E2E scenario catalog, and
[blind forward-test cases](../../.agents/skills/defect-investigation/references/forward-test-cases.md).
Historical specialized procedures remain indexed by
[`migration/verification/README.md`](../migration/verification/README.md); they
are evidence recipes, not a second test-strategy owner.

## Cleanup and Evidence Hygiene

- Create repository fixtures and transcripts under test-owned temporary
  directories; always close PTYs, child processes, servers, streams, and file
  descriptors.
- Default session inspection to structured metadata. Do not export or quote raw
  prompts, model responses, environment values, credentials, or transcripts.
- Report the first failing command and seed. Do not turn an unexplained retry
  into a pass or call a cross-build a runtime test.
- A test name, registry entry, package dependency, or coverage percentage is
  supporting evidence only. The observable contract and independent oracle
  decide whether the risk is covered.
