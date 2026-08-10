# P43.0 Real-Repository Evaluation Baseline

**Status:** historical
**Execution state:** complete; G29 closed
**Last verified:** 2026-08-02
**Promotion snapshot:** `03faac2575a0de6e17c54d1c310cfd4eba081649`

> **Ownership:** the completed P43.0 harness, report, fixture, evidence, and
> rollback contract. Delivery evidence is in
> [`p43-0-real-repository-evaluation.md`](../history/runtime/p43-0-real-repository-evaluation.md).
> The promotion reproduction belongs in
> [`p43-0-real-repository-evaluation-promotion.md`](../verification/p43-0-real-repository-evaluation-promotion.md).
> This document does not make an evaluation score authoritative.

## Decision

P43.0 completed under `combine`:

- preserve canonical traces as protocol and kernel-compatibility evidence;
- adapt the disposable-repository, scripted-provider, and ordered-replay
  patterns verified in Codex, Crush, and OpenCode; and
- own the scenario schema, observable graders, redaction, isolation claims,
  and unsupported-entrypoint reporting in this project.

The first baseline is intentionally narrow. It proves that the current
`eino-agent exec` product path can make and grade one repository change without
a live provider, user credential, host checkout mutation, shell, or network
tool. It does not measure model quality, compare providers, enforce a release
gate, or claim OS process containment.

## User Problem And Scope

Package tests and canonical traces can stay green while a coding agent stops
solving repository tasks, violates a policy boundary, leaves unexpected files,
or consumes an unbounded number of turns. P43.0 adds one opt-in harness that
grades those observable outcomes around the existing executable. The harness
must remain outside QueryEngine and production scheduling.

P43.0 may add:

- a versioned scenario manifest and a pinned, dependency-free repository
  fixture;
- a local scripted provider that returns fixed public tool calls and usage;
- an external-process runner for the built `eino-agent` binary;
- deterministic outcome, policy, residual-state, usage, recovery-state, and
  artifact graders;
- one bounded, redacted JSON report schema; and
- an explicit local command that is not part of `verify`, CI required jobs, or
  release acceptance.

It must not change runtime selection, model routing, provider adapters,
permission decisions, Goal defaults, supported public entrypoints, Session
schema, production dispatch, or release policy.

## One Disposable Run

Every run follows one ordered lifecycle:

1. Read and strictly validate a committed scenario manifest. Reject unknown
   fields, duplicate paths, absolute paths, traversal, links, devices, and
   files beyond the declared count and byte limits.
2. Copy the pinned fixture through a root-anchored reader into a newly created
   private temporary directory. Initialize a clean Git snapshot and record its
   content-tree digest before starting the agent.
3. Create separate private `HOME`, XDG/config, artifact, and grader
   directories. Reserve the disposable repository's `.eino-agent` subtree for
   the public command's current Session metadata, inspect it separately from
   source changes, and remove it with the disposable repository. Start the
   scenario's loopback-only scripted provider. Clear inherited provider,
   proxy, cloud, SSH-agent, and project configuration inputs, then add only the
   manifest's allowlisted environment.
4. Execute the configured external `eino-agent` binary with the exact public
   `exec --output-format json` arguments, temporary repository as CWD, a
   bounded context deadline, and bounded stdout/stderr capture. The runner may
   terminate the whole owned process group on timeout; it must not import or
   construct QueryEngine.
5. Close the provider script, settle process output, and run graders outside
   the agent-visible repository. Inspect the pinned input, final repository,
   Git status, repository-local runtime metadata, allowed relative source
   changes, outside sentinels, process result, provider script, and bounded
   artifact buffers.
6. Canonicalize a report candidate without raw prompt, repository content,
   provider request, credential, or absolute path.
7. Remove the temporary repository, home, provider, and grader roots. Cleanup
   failure is itself a terminal harness failure and cannot receive a passing
   cleanup grade.
8. After both clean replays finish cleanup and their canonical grades match,
   write one report atomically with private permissions. A failed replay or
   cleanup publishes no passing report.

Cancellation and timeout stop admission of new provider responses, terminate
the owned process tree, settle a failed report, and still run residual and
cleanup checks. A partial or malformed headless JSON envelope is a harness
failure, not a task failure that can be scored as zero.

## Promotion Fixture: `localized-write-fix/v1`

The first scenario is a small real Git repository containing a Go module with
a missing `decorate` implementation used by `Greeting` and public compile/smoke coverage. A
grader-owned external test checks empty-input and non-ASCII behavior
without placing that test in the agent-visible repository.

The manifest freezes:

- one non-sensitive task and its digest;
- public entrypoint `headless.exec`;
- `acceptEdits` mode and exactly the `Write` tool;
- a loopback scripted OpenAI-compatible route with a fake credential;
- maximum provider calls, model turns, tool calls, wall time, captured bytes,
  repository files, and artifact bytes;
- exactly one allowed relative repository addition; and
- the public and hidden grader commands, their explicit 60-second per-command
  timeout, and expected results.

The provider first requests a `Write` to a test-owned sentinel outside the
workspace. The existing headless fallback must deny it. The provider then
requests the one contained implementation, which the existing `acceptEdits`
fast path may allow, and finally emits the terminal assistant result. This is a
policy/isolation probe, not a recommendation that normal models should attempt
an escape.

No `Read`, Bash, WebFetch, MCP, Agent, shell hook, plugin, or process-spawning
tool is model-visible. The fixture has no external dependencies. Two fresh
replays must produce byte-identical canonical reports and the same final tree
digest.

## Isolation Claim Boundary

P43.0 must state the mechanism and evidence for every axis. It cannot relabel a
permission prompt as an OS sandbox.

| Axis | Initial fixture claim |
|---|---|
| Host checkout | Enforced: the source fixture and caller checkout are read-only inputs; the agent CWD is a fresh private copy. |
| Workspace write | Enforced for the admitted surface: only `Write` is visible, `acceptEdits` allows the contained target, and the root-escape probe must be denied with its sentinel unchanged. |
| Host read | Unavailable to the model: no read/search/process/MCP tool is visible. Harness-owned grading reads only declared roots. |
| Network | No model-visible network/process tool. Provider transport is pinned to one harness-owned loopback endpoint. OS-wide network containment is not evaluated. |
| Credentials | Enforced for the scenario: inherited provider/cloud/proxy/SSH inputs are removed and the only key is a fixed fake sentinel scanned from artifacts. |
| Process/syscall/resource sandbox | `not_evaluated`: P42.0 has no enforcing adapter, and this fixture exposes no process tool. |
| TUI, Plain, ACP, standalone MCP | `not_evaluated`: these product entrypoints remain supported but are outside the first scenario. |

Any future scenario that exposes Bash, a process hook, MCP stdio, a child
process, or a network tool must supply and test an enforcing execution envelope
before it can report those axes as isolated. P43.0 does not close G28.

## Versioned Report Contract

The first reusable report is JSON with a closed schema and a stable kind such
as `eino-agent/evaluation-report`. It contains only bounded, canonical values:

- schema version, scenario ID/version, fixture/task/binary digests, and runner
  version;
- evaluated entrypoint plus an ordered map of `evaluated`, `not_evaluated`, or
  `unsupported` states with stable reason codes;
- terminal harness state distinct from task outcome;
- task outcome, public/hidden grader states, expected-change result, and final
  tree/residual digests;
- policy attempts, blocked attempts, violations, tool/provider retry and
  failure counts, and exact budget comparisons;
- recovery state, including `not_exercised` with a stable reason when the
  scenario does not contain a restart boundary;
- provider-reported token usage with `exact`, `partial`, or `unavailable`
  coverage; cost remains `unavailable` until an explicit pricing input exists;
- measured wall duration in a diagnostic field that is excluded from canonical
  grade equality; and
- the isolation matrix and cleanup result.

Reports must not contain raw prompts, repository bytes/diffs, request or
response bodies, tool input/output, environment values, API keys, absolute
paths, home-directory fragments, provider-private metadata, Session IDs, or
transcripts. Diagnostics use stable codes and bounded counts. Before writing,
scan the encoded report for the fixture's prompt, fake credential, absolute
roots, provider request sentinel, and repository-content sentinels.

The encoded report is capped at 64 KiB. Stdout and stderr are captured through
separate bounded buffers; truncation is explicit. An existing output path,
link, non-regular file, insecure parent, or replacement race fails before
publication.

## Implementation Boundary

P43.0 is one non-production harness PR:

1. add a standalone Go command under `scripts/evaluation` with strict manifest,
   lifecycle, subprocess, bounded-capture, canonical-report, and cleanup
   owners;
2. add `localized-write-fix/v1` under that command's `testdata`, including the
   pinned repository, task manifest, provider script, and external grader;
3. execute the built `eino-agent` binary through public `exec`, never an
   in-process engine or test-only production hook;
4. add deterministic unit/integration tests for two clean replays, root escape,
   redaction, bounds, malformed inputs, timeout/cancellation, cleanup, and
   unsupported/not-evaluated reporting;
5. add one explicit `make eval-baseline`-style local invocation that remains
   outside `verify`, required CI jobs, and release gates; and
6. update P43/G29 current documentation and add one closeout record. Canonical
   trace fixtures remain unchanged.

The implementation may split files inside `scripts/evaluation` for clarity,
but the command, fixture, schema, and invocation are one rollback boundary. It
must not add a general benchmark service, remote artifact upload, live-provider
recording, dashboard, production telemetry, or score threshold.

## Deterministic Proof Matrix

| Boundary | Required proof |
|---|---|
| Public product path | An external built binary runs `exec --output-format json`; a direct QueryEngine shortcut fails the source/behavior gate. |
| Clean replay | Two independent materializations start from the same tree digest and emit byte-identical canonical reports and final residual digests. |
| Outcome | Public smoke and external hidden regression pass only after the expected contained file is created. |
| Policy | Root-escape `Write` is denied, the outside sentinel is byte-identical, the contained `Write` succeeds once, and no other tool is exposed or executed. |
| Budgets | Exact provider/model/tool counts stay within positive manifest limits; timeout and captured/artifact byte limits fail visibly. |
| Usage and cost | Scripted provider totals are reported exactly; absent or malformed metadata becomes partial/unavailable; cost is unavailable without pricing input. |
| Recovery | Initial fixture reports `not_exercised`; schema tests reject missing recovery state and later restart scenarios must provide their own oracle. |
| Residual state | Product Git status and content digests contain only the declared relative addition; repository-local `.eino-agent` Session metadata is inspected and classified separately, while the source fixture, caller checkout, outside sentinel, config roots, and artifact roots remain outside the residual set. |
| Redaction | Prompt, fake key, temp roots, request/response/tool bodies, repo sentinels, Session identity, and provider-private fields cannot appear in JSON or diagnostics. |
| Isolation truth | Every axis has a mechanism and state; OS process/network containment and non-headless entrypoints remain `not_evaluated`, never silently pass. |
| Failure lifecycle | Bad manifest, provider drift, malformed JSON, grader failure, timeout, cancellation, report collision, and cleanup failure each produce one terminal harness failure without a passing task grade. |

Final closeout runs the focused evaluation tests, `make fmt`, `make lint`,
`make test`, `make build`, `make docs-check`, `make docs-check-ci`,
`make lint-new`, and `git diff --check`. The opt-in baseline command must also
run twice successfully from a clean checkout.

## Rollback

Rollback removes only `scripts/evaluation`, its fixture, the opt-in invocation,
P43.0 closeout documentation, and G29 closure. Production code, QueryEngine,
providers, permissions, Goal, Sessions, canonical traces, CI required gates,
and release behavior remain unchanged.

No durable migration or user data exists. Generated reports and temporary
repositories are disposable local artifacts. If rollback occurs, P43.0 returns
to `Queued` and G29 remains reproduced.

## Evidence And Owners

| Boundary | Evidence | Role |
|---|---|---|
| Current public entrypoint | [`headless.go`](../../../cmd/yhc/cmd/headless.go) | Owns the public one-shot command, machine-readable result, and headless permission fallback. |
| Contained edit policy | [`engine.go`](../../../engine/engine.go) and [`accept_edits.go`](../../../engine/permission/accept_edits.go) | Allow only contained Write/Edit before the non-interactive fallback; escaping paths remain denied. |
| Compatibility traces | [`canonical_trace_fixture_test.go`](../../../engine/canonical_trace_fixture_test.go) | Retains kernel/protocol compatibility and the deterministic double-run pattern; it is not the product-outcome grader. |
| Usage truth | [`usage.go`](../../../engine/transcript/usage.go) | Distinguishes provider-reported exact/partial/unavailable usage from invented estimates. |
| Promotion proof | [`p43_0_characterization_test.go`](../../../cmd/yhc/cmd/p43_0_characterization_test.go) | Proves the minimum public-path, disposable-root, policy, outcome, redaction, and replay contract without adding a reusable harness. |
| Original repair contract | [`recent-delivery-remediation-audit.md`](../reference/runtime/recent-delivery-remediation-audit.md#repair-contract-g29-real-repository-evaluation) | Defines the real-repository scenario classes and non-authoritative boundary. |
