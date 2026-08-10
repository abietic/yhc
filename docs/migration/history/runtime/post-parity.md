# Post-Parity Iteration History

**Status:** historical
**Last verified:** 2026-07-22

> **Ownership:** this document records completed post-parity iterations and
> their evidence. Current facts belong in [`STATUS.md`](../../STATUS.md), unresolved
> gaps in [`REMAINING.md`](../../REMAINING.md), and future work in
> [`PLAN.md`](../../PLAN.md).

## H0 Build Dependency Coverage

**Completed:** 2026-07-13

### Problem

`Makefile` binary prerequisites included Go files under `cmd`, `engine`, and
`tools`, but omitted production `internal` and `server` sources. Incremental
`make build` could therefore leave TUI or protocol-server changes out of existing
release artifacts.

### Resolution

- `SOURCES` covers `cmd`, `engine`, `internal`, `server`, and `tools`.
- All four release targets and the debug target continue to share that source
  dependency set without becoming always-rebuild targets.
- `scripts/build_dependencies_test.go` runs `make --question` against the real
  repository Makefile from isolated temporary fixtures.
- Each source root and each release/debug target is checked independently with
  explicit artifact times and no repository source timestamp mutation.

### Evidence

- `go test ./scripts -run '^TestBuildDependencies$' -count=1 -v`
- `make fmt`
- `make lint`
- `make test`: 3,720 tests pass
- `make build` and a forced four-target rebuild
- migration manifest, Markdown-link, and diff validation

### Non-Goals

H0 does not redesign provider build profiles, split binaries, remove dead
packages, or implement P9.1. Those remain independent decisions.

## P9.1 Repeated-Identical-Tool Circuit Breaker

**Completed:** 2026-07-13

### Problem

A valid tool call could repeat across model turns without a dedicated safety
boundary. Ordinary permission denial tracking answered a different question and
could not prevent repeated allowed side effects or runaway tool cost.

### Resolution

- Each `Query` owns an isolated repeated-call guard; child queries receive a new
  guard while inheriting only the interactive prompt adapter.
- The fingerprint hashes the registry-canonical tool name and parsed,
  schema-coerced input. Raw arguments are not retained or projected.
- Streaming and batch paths reserve non-blocking tickets in model order before
  concurrent goroutines evaluate admission.
- The third consecutive identical valid call stops before pre-tool hooks,
  ordinary permission checks, and execution.
- Different input, queued/new user input, or an explicit one-call override
  resets the streak. Tool success or failure alone does not.
- Cancellation transfers ticket release ownership without allowing a successor
  to bypass an unresolved predecessor.

### Interaction Contract

- TUI and ACP offer only `run once` or `stop and change strategy`; no session or
  always-allow rule can be created from this prompt.
- A one-call override still traverses the normal hook and permission chain.
- Headless and any entrypoint without a prompt adapter fail closed with a
  model-visible tool result, including permissive permission modes.
- Engine request/resolution events use `repeated_tool` identity and attempt
  metadata, remain owner-thread/replay visible, and contain no raw tool input.

### Evidence

- focused threshold, canonicalization, coercion, changed-input, override,
  denial, cancellation, queue reset, batch, streaming, partial-argument,
  runtime replay, TUI merge, and ACP option tests;
- repeated and race execution-path tests proving canceled tickets cannot bypass
  unresolved predecessors;
- package-level engine, TUI, ACP, and CLI regression tests;
- independent bounded review of concurrency, privacy, replay, subagent, TUI,
  headless, and ACP entrypoint behavior;
- repository Makefile, manifest, Markdown-link, and diff gates.

### Non-Goals

P9.1 does not add result-aware rolling-loop diagnostics, coalesce already
pending permission requests, persist guard state across user turns, or move
admission policy into the TUI.

## P9.2A Canonical Permission Interaction Lifecycle

**Completed:** 2026-07-14

### Problem

Interactive settlement was split across callback response handles, a
per-engine prompter, TUI-owned grant side effects, and entrypoint-specific
event behavior. Those owners could not provide one exactly-once winner across
user response, classifier/external resolution, cancellation, timeout, and
shutdown. Plain and ACP durable labels also did not match their effects.

### Resolution

- A process-local default registry reuses one `PermissionCoordinator` for
  independently constructed engines and canonical aliases of the configured
  project root, while isolating different roots. ACP injects the same ownership
  boundary explicitly. Canonicalization uses absolute paths plus symlink
  resolution; it deliberately does not discover an ancestor VCS root.
- Requests carry project, root-session, session, thread, Agent, and tool-use
  identity. One atomic take/remove claim owns grant commit, the ordered
  `permission_resolved` event, and waiter delivery; late responses cannot
  persist or emit a second terminal result.
- Structured decisions distinguish once, root-lineage session, project-local
  always, deny, cancellation, and timeout. Root/child engines share session
  approvals only through explicit lineage injection; separate root sessions do
  not share them.
- TUI, plain, and ACP only present choices and return decisions. The engine
  commits grants and emits lifecycle events. TUI ignores coordinator events for
  dialog creation to avoid duplicating its adapter-owned prompt; plain exposes
  truthful once/session/always labels; ACP persists allow-always under the
  actual session CWD.
- Headless keeps a legacy fail-closed or explicit bypass callback and installs
  no interactive prompt. Engine close cancels owned requests and waits for
  terminal settlement before the last registry entry is released.

### Evidence

- deterministic user/external, user/cancel, timeout, shutdown, late-response,
  registry alias/isolation/release, root-child grant, and non-coalescing tests;
- TUI structured-decision and duplicate-dialog tests, plain scope-label tests,
  and headless no-prompt tests;
- ACP actual-CWD registry tests plus persistent allow-always, tool-call identity,
  and request-before-resolution status ordering;
- focused engine, TUI, CLI, and ACP package tests plus race validation;
- repository Makefile, manifest, Markdown-link, and diff gates.

### Non-Goals

P9.2A did not scan or auto-resolve another pending request after a grant. That
historical boundary was closed independently by P9.2B below. Pending
interactions remain process-local and are never rehydrated as actionable
waiters.

## P9.2B Exact-Scope Positive Permission Coalescing

**Completed:** 2026-07-14

### Problem

Concurrent parent and Agent requests could remain separately blocked after one
request committed a session or project grant that already covered the others.
Resolving them by tool name, session, or visual queue position would reduce
prompts by silently broadening authority and would let UI state become a second
permission owner.

### Resolution

- After a winning `allow_session` or `allow_always` commit, the project
  coordinator snapshots live candidates and evaluates each through its owning
  engine outside the coordinator lock. Allow-once, deny, cancellation, timeout,
  classifier/hook approval, repeated-tool override, and bypass paths never
  trigger a scan.
- Session candidates must share the source root lineage, match the exact scoped
  invocation, and observe the committed grant in their own explicitly shared
  approval tracker. Always candidates must share the canonical project and be
  allowed by both the persisted source rule and their freshly reloaded complete
  rule set, preserving deny/ask precedence.
- Every candidate performs its own atomic terminal claim and emits its own
  owner-stream resolution with reason `coalesced`. It neither recommits the
  grant nor recursively scans. Shutdown, cancellation, and late adapter replies
  can win only through the same first-winner transition.
- Plain mode makes its prompt gate cancellable so a queued follower does not
  print another prompt after coalescing. TUI removes only the matching owner
  attention row, while ACP relies on the same engine lifecycle and persists the
  source rule before the scan.
- Pending requests remain process-local. Closing an engine cancels the request,
  prevents tool execution, and a reconstructed engine starts with no actionable
  waiter.

### Evidence

- positive and near-miss coverage for exact Bash commands, recursive read/search
  paths, exact write paths, canonical MCP inputs, persisted Bash families, and
  persisted write-directory rules;
- deny/ask precedence, canonical project aliases and isolation, root-lineage
  isolation, non-durable decisions, candidate shutdown, late replies, and
  single-grant persistence tests;
- a real `SubAgentExecutor` child execution test proving one parent session
  grant resumes the matching child tool call through shared lineage state;
- TUI owner-attention cleanup, plain queued-prompt cancellation, ACP concurrent
  allow-always execution, headless bypass, and process-restart non-replay tests;
- focused race suites, independent bounded review, repository Makefile gates,
  manifest and Markdown-link validation, and diff checks.

### Adaptation And Non-Goals

Positive pending-request scanning is a deliberate Eino-Agent product extension
adapted from OpenCode's per-candidate re-evaluation. Claude Code Ripe supplied
the exactly-once and cancellation model but has no verified equivalent durable
positive scan, so this is not claimed as Claude parity. The implementation does
not copy OpenCode's blanket rejection cascade, serialize all prompts behind one
coordinator mutex, persist pending waiters, or move authorization into the TUI.

## P10 Trace And Session Workflow Convergence Audit

**Completed:** 2026-07-14

### Question

Could a user with a long leader history and concurrent Agents locate an
inactive owner needing approval, reach a usable transcript, inspect failure and
evicted state, return to the leader, and safely recover after process restart
without inspecting raw files?

### Resolution

- A self-hosted Unix PTY now exercises a 300-row leader history, concurrent
  waiting/running/failed Agents, owner-scoped approval, searchable switching,
  failed evicted transcript projection, leader return, interruption, EOF, and
  terminal cleanup.
- P95 gates now measure 20-Agent attention search and cold 256-message first
  transcript projection in addition to cached switching.
- A separate process test writes real Agent/session state, starts a fresh test
  process, and proves replay-only restoration with parent lineage, transcript,
  terminal status, and no actionable interaction.
- The audit reproduced one bounded defect: the picker displayed the leader as
  `main` but did not search that derived label. Search now indexes the same
  label it renders, with unit and PTY acceptance coverage.
- The parent trace remains a bounded status/usage/error index. Final result,
  output source, retained/evicted storage, and lineage remain available through
  the projected child transcript and Agent detail; no tested workflow required
  raw-file inspection.

### Evidence

- repeated PTY workflow and terminal cleanup tests;
- 20-Agent attention and 256-message transcript p95 gates;
- retained/evicted Agent detail, terminal failure, runtime eviction, live
  resume, disk replay, cross-CWD resume, missing-worktree, and fork tests;
- cross-process restart replay and lineage test;
- source comparison against Claude Code Ripe task output, Codex Agent picker
  and collaboration history, and Crush session lineage/usage.

Full metrics, surface assessment, and source anchors are in
[`migration/reference/runtime/trace-session-workflow-convergence-audit.md`](../../reference/runtime/trace-session-workflow-convergence-audit.md).

## P11 Core-Surface Slimming Audit

**Completed:** 2026-07-14

### Question

Can the first unreachable-code cohort be accepted without removing a live
entrypoint, hidden dynamic path, supported external API, or truthful migration
evidence?

### Resolution

- A fresh `go list` reverse-import scan reproduced exactly 29 non-entrypoint
  zero-production-import packages: 11 under `engine/*` and 18 under
  `internal/tui/*`.
- Independent source review found no production/test importer, build tag,
  blank import, plugin, linkname, generate, embed, or CGo reachability path for
  the TUI cohort. Four release-target dependency closures also exclude it.
- The TUI cohort cannot be a module-external API because Go's `internal` rule
  blocks those imports. The engine cohort retains a separate public-API and
  behavior decision and was not accepted for deletion.
- The active CLI enters the root `internal/tui` package. The 18 subpackages are
  shadow scaffolds totaling 19 files and 3,602 production lines, with no tests.
- Manifest review found one structured target and 366 narrative notes naming
  the shadow packages. This is evidence debt that must be reconciled in the
  same deletion slice, not a reason to preserve a false architecture owner.

### Decision

P12 was accepted to delete only the disconnected TUI cohort, add deterministic
package-liveness reporting, revalidate affected manifest evidence, and prove
unchanged PTY/golden/performance/release behavior after deletion. No engine
package, public tool/command surface, runtime schema, or session format is in
scope.

Full package inventory, ledger counts, constraints, and acceptance metrics are
in
[`migration/reference/runtime/module-criticality-and-slimming.md`](../../reference/runtime/module-criticality-and-slimming.md).

## P12 Disconnected TUI Scaffold Removal

**Completed:** 2026-07-14

### Resolution

- Removed exactly the 18 accepted disconnected `internal/tui` packages: 19
  production files and 3,602 lines. No `engine/*` package was deleted.
- Extended `migration_scan` with deterministic `go list -json` reverse-import
  analysis covering production, same-package test, and external-test imports.
  The post-removal report distinguishes three legitimate `main` entrypoints
  from the 11 remaining zero-import non-entrypoint engine packages.
- Added a tested manifest rule that rejects retired paths in targets, tests, or
  notes. Corrected 341 false file-level port notes and reviewed the remaining
  26 affected claims; none had a live one-to-one owner and focused evidence, so
  they are explicit `excluded/not_started` entries rather than false done
  mappings.
- Forced four release builds produced byte-for-byte identical binaries and an
  unchanged CLI help hash. Every release dependency closure contains zero
  retired paths.
- Repeated PTY, current-output golden, and performance-budget tests passed;
  the full suite now passes 3,808 tests.

### Result

The live root TUI is unchanged, but its architecture has one fewer shadow
component/state model. Production source falls from 426 files/131,796 lines to
407 files/128,194 lines, while registered tools, commands, tests, runtime event
schema, session format, and binary distribution remain unchanged.

Detailed mapping decisions, measurements, and retained engine boundaries are in
[`migration/reference/runtime/module-criticality-and-slimming.md`](../../reference/runtime/module-criticality-and-slimming.md).

## P13.H0 Fail-Closed Streamed-Tool Commit

**Completed:** 2026-07-17

### Problem

`StreamingToolExecutor.AddTool` could release syntactically complete calls while
the model stream was still open. A later truncation terminal could therefore
arrive after a mutation, hook, or permission side effect had already begun.
The project Agentic-to-legacy bridge also discarded provider terminal metadata,
including metadata-only terminal chunks, so a classifier limited to
`ProcessStream` would not protect configured production providers.

### Resolution

- `StreamingToolExecutor` now owns one pending, committed, or rejected turn
  state. `AddTool` only accumulates while pending; only `ProcessStream` can
  release the complete set after shared terminal classification.
- The project-owned normalizer commits known successful outcomes and clean-EOF
  compatibility, rejects terminal or withheld `length`, `max_tokens`, and
  `max_output_tokens` in model order, and fails closed for unknown non-empty,
  typed or untyped withheld, or stream-error outcomes. Pre-commit cancellation
  rejects without execution; post-commit interruption keeps existing per-tool
  behavior.
- `agenticChatModel` preserves metadata-only terminal chunks and extracts raw
  terminal state from Claude, OpenAI, Gemini, Ark, DeepSeek, and Qwen Eino
  extensions. Provider clients, route selection, tool policy, scheduling,
  hooks, permissions, and storage did not change.
- Successful commits retain the existing stable safe/serial scheduler, Bash
  sibling cancellation, and model-result order.

### Evidence

- central classifier tables cover all accepted aliases, clean EOF, unknown
  values, stream failure, and context cancellation;
- fake streams prove multiple truncated mutation calls produce ordered error
  results with a zero side-effect counter, while successful commit executes
  once;
- provider bridge fixtures cover all six configured provider families and a
  metadata-only truncation terminal;
- production `Query` integration proves a truncated `Write` call is rejected,
  never dispatched, and followed by the next model turn, including the
  existing withheld max-output recovery path;
- focused execution/provider/query tests and focused race validation;
- independent concurrency and entrypoint review plus repository Makefile,
  manifest, Markdown-link, and diff gates.

### Adaptation And Non-Goals

This slice adapts Pi's truncate-without-execution safety rule to Eino-Agent's
existing stream, event, and tool-result contracts. It does not adopt Pi's loop,
upgrade Eino, add an ADK kernel, change provider routing, rewrite tool policy,
or introduce a feature flag or storage migration. At that closeout, P13.0
owned the next test-only compatibility baseline.

## P13.0 Canonical Behavioral Compatibility Suite

**Completed:** 2026-07-17

### Problem

Focused tests covered individual query-loop mechanisms but did not provide one
deterministic observable baseline for detecting extra model requests, changed
stream or tool projection, reordered queue/recovery events, altered message
state, or terminal drift during the staged Eino kernel migration.

### Resolution

- Added a test-only typed trace with model-request, stream, tool, event,
  state-boundary, and terminal records. The normalizer preserves semantic order
  while replacing generated identity, timestamps, caller temporary roots, and
  credential-like values.
- Added semantic diff categories for request count, tool order and outcome,
  event order, message state, terminal reason, and residual record payload.
- Added 11 reviewed golden traces covering no-tool completion, delta and
  cumulative streamed arguments, safe/serial scheduling, permission and hooks,
  retry/fallback, prompt-too-long recovery, main/child queue safe points,
  cancel/block outcomes, P13.H0 truncation, and `QueryEngine` entrypoint event
  identity.
- Golden replacement is available only through the explicit test flag
  `-update-canonical-traces`; ordinary test runs compare read-only fixtures.
- Production source, dependencies, public APIs, provider calls, permissions,
  storage, scheduling, and kernel selection did not change.

### Evidence

- `go test ./engine -run '^TestCanonicalTrace' -count=1`
- `go test ./engine -run '^TestCanonicalQueryCompatibilityTrace$' -count=2`
- focused engine race validation;
- repository Makefile, manifest, Markdown-link, and diff gates;
- independent review of determinism, concurrency, recovery, cancellation,
  sensitive-data normalization, and entrypoint coverage.

### Adaptation And Non-Goals

This slice uses a project-owned compatibility trace to combine existing
Eino-Agent observable contracts with the future Eino ADK migration gates. It
does not claim that any production query-loop logic moved to Eino. P13.1 owns
the dependency-only stable Eino upgrade, and trace changes during that slice
require an explicit compatibility decision rather than snapshot churn.

## P13.1 Stable Eino v0.9.12 Baseline

**Completed:** 2026-07-17

### Problem

The project still pinned Eino v0.9.0 while the accepted ADK migration target
depended on fixes released through v0.9.12. Beginning adapter work on the older
baseline would mix dependency drift with ownership changes and make a
compatibility regression difficult to localize.

### Resolution

- Upgraded only `github.com/cloudwego/eino` from v0.9.0 to v0.9.12. Eino's
  module-level Go version and dependency set are unchanged across those tags;
  the six configured Eino-ext provider modules remain at their existing
  versions and resolve the root v0.9.12 requirement through Go MVS.
- Audited every upstream patch. Material fixes cover ADK streamed metadata,
  resume streaming-mode restoration, sub-agent event role/forwarding,
  interrupt-aware and Agentic ReAct failover, message-ID copy-on-write,
  typed-nil checkpoint serialization, callback span ownership, ToolInfo JSON
  round trips, summarization, Agentic reasoning metadata, and gonja template
  hardening.
- Confirmed that the current production loop imports no Eino ADK, Runner, or
  TurnLoop package. Project-owned query, retry/recovery, queue, permission,
  hook, session, and checkpoint owners therefore remained unchanged.
- Kept the expected compatibility-code set empty. The direct production risk
  surface remains Eino `schema.Message`, streaming, tool schemas, the dormant
  compose graph helper, and Agentic provider conversion; focused and canonical
  evidence passed without a source change.

### Evidence

- all 11 P13.0 canonical golden hashes were identical before and after the
  upgrade under repeated execution;
- `go test ./engine/provider ./engine/execution` and the broader engine, tools,
  ACP, MCP, and CLI package tests passed;
- canonical race validation passed;
- repository Makefile, lint-new, manifest, Markdown-link, and diff gates passed;
- independent source-diff review found no required compatibility code and no
  production ADK reachability.

### Adoption Decision And Non-Goals

This dependency-only slice is `combine`: it accepts upstream correctness and
security fixes while preserving project-owned observable contracts. It did not
add middleware, construct a live ADK kernel, update golden behavior, alter
provider routing, or move model, tool, live-input, recovery, event, transcript,
session, or TUI ownership. P13.2 owns the next fixture-only compatibility layer.

## P13.2 ADK Compatibility Layer

**Completed:** 2026-07-17

### Problem

The accepted Eino convergence needed a real internal boundary before any
scheduler, recovery, tool-round, or canary work. Adding a public selector or a
second live loop at this stage would have made duplicate requests and side
effects possible while checkpoint, event, and rich tool-outcome contracts were
still unresolved.

### Resolution

- Added an unexported `queryKernel` interface. Production `Query` selects
  `legacyQueryKernel` unconditionally, and that implementation delegates to the
  existing `queryLoop`.
- Added an unexported deterministic fixture selector that constructs Eino
  v0.9.12 `ChatModelAgent` and `Runner`. The ADK kernel fails before
  `Runner.Run`; no `QueryEngineConfig`, CLI, or environment selector exists.
- Froze model-visible `ToolInfo` values by JSON round trip and made every Eino
  adapter return a fresh clone. Fixture adapters fail closed. A fake-only
  invocation proof preserves stable call ID and the complete canonical
  `toolExecutionOutcome` for later batch handling without reimplementing
  validation, permission, hooks, or execution order.
- Added project-owned provider-option, recovery-classification, tool-policy,
  call-identity, and event-projection handler seams. The event bridge maps
  attempt, message, stream, tool, interrupt, exit, and error events into the
  existing `QueryEvent` vocabulary through one incremental sink rather than
  buffering a completed stream; `turnEventEmitter` still owns identity,
  sequence, timestamp, reduction, and publication.
- Added a strict versioned and SHA-256-digested Session envelope around opaque
  Eino checkpoint bytes. Five typed runtime-item variants cover prompts, queued
  commands, steering, approval results, and task notifications. Approval data
  stores intent, invocation digest, and observed policy revision, never a
  permission grant.
- Kept the envelope codec disconnected from current transcript/session writes.
  Current Session metadata and transcript remain durable truth.

### Evidence

- `go test ./engine -run '^TestP132' -count=1`;
- `go test -race ./engine -run '^TestP132' -count=1`;
- all 11 P13.0 canonical traces remained exact;
- `make lint-new` reported no newly introduced findings;
- repository Makefile, manifest, Markdown-link, and diff gates passed;
- independent runtime-depth review covered production reachability, adapter
  ordering, checkpoint integrity, approval non-authority, and P13.3 deferrals.

### Adoption Decision And Non-Goals

This slice is `adapt`: it uses Eino ADK interfaces for construction, middleware,
events, tools, and opaque checkpoint compatibility while preserving every
project-owned production contract. It did not run an ADK Agent, write an Eino
checkpoint, select a kernel per session, move retry/recovery, replace
`executeToolCall`, change dynamic scheduling, transfer queue ownership, or
enable interrupt/resume. P13.3 owns scheduler and structured after-tool
feasibility; P13.4-P13.8 own the later live mechanics.

## P13.3 Scheduler And Continuation Proof

**Completed:** 2026-07-17

### Problem

Eino v0.9.12 `ToolsNode` offered only a global all-parallel/all-serial switch,
while the production query loop groups adjacent concurrency-safe calls and
uses every unsafe call as a serial barrier. Its after-tool hook also returned
only `error`, which could not distinguish a successful early return,
interrupt, and actual failure. Moving tool ownership without proving both
boundaries would risk reordered side effects, deadlocked waiters, ambiguous
resume, or sentinel-error control flow.

### Resolution

- Added a strict run-scoped schedule derived from complete model-ordered tool
  calls. Nil calls, missing/duplicate IDs, empty names, and concurrency
  classifier panics fail closed; only adjacent safe calls share a batch.
- Adapted Eino `ToolsNode` through per-call middleware. Each wrapper verifies
  call ID, tool name, and raw-argument digest against the frozen plan, waits on
  its batch gate, propagates stable call identity, and settles before the next
  batch opens. Eino retains model-ordered output collection.
- Made cancellation, endpoint errors, identity substitution, duplicate
  admission, and panic abort the run once and unblock every later waiter.
  Coordination uses no helper goroutine, sleep, polling loop, or global mutable
  state.
- Added a versioned plain-data checkpoint with round digest, ordered calls,
  batch classification, and model-ordered settled IDs. Decode rejects unknown
  fields, out-of-order settled IDs, and settlement beyond the first incomplete
  batch. A safe batch may retain a partial settled subset because siblings run
  concurrently. Restore creates new mutexes and gates, retains settled
  membership, and opens only the first incomplete batch. The fixture caller
  dispatches only unsettled calls; P13.5 must separately bind persisted output
  bodies into Eino's executed-tool resume state before a full original message
  can resume without re-execution.
- Added a typed complete-round decision that validates exact ordered outcomes
  and selects continue, successful return with
  `TerminalHookStopped`, or interrupt. It never maps those decisions through
  sentinel errors.
- Recorded the Eino capability boundary explicitly: v0.9.12's
  `WithAfterToolCallsHook` cannot consume a structured successful return or
  interrupt, and static `ReturnDirectly` cannot depend on the executed outcome.
  P13.5 must contribute or adopt a structured after-tool/runner decision seam
  before live tool ownership transfers.

### Evidence

- a real Eino `ToolsNode` fixture proves
  `safe + safe -> serial -> safe + safe`, sibling overlap, barriers, and
  model-ordered results;
- checkpoint round-trip and coordinator reconstruction prove a partially
  settled safe batch when the fixture dispatches only the remaining sibling;
- cancellation and panic fixtures prove later batches unblock and never
  execute;
- malformed schedule, identity substitution, duplicate admission, and
  classifier panic fixtures fail closed;
- complete-round continue/return/interrupt plus malformed-outcome tests pass;
- focused repeated and race tests pass;
- all 11 P13.0 canonical traces remain exact; and
- repository Makefile, lint-new, manifest, Markdown-link, and diff gates pass.

### Adoption Decision And Non-Goals

This slice is `adapt`: it reuses Eino's `ToolsNode`, per-call middleware, and
ordered result assembly while retaining project ownership of dynamic batch
planning, identity validation, checkpoint reconstruction, and continuation
semantics. It did not run an ADK Agent, connect canonical `executeToolCall`,
change production scheduling, persist an ADK checkpoint, add a kernel selector,
or move any model/tool/session owner. P13.4 owns deterministic model-attempt and
recovery mechanics. P13.5 reuses the scheduler only after its executed-result
resume bridge, structured decision bridge, and full canonical tool-round gates
pass.

## P13.4 Model Attempt And Recovery Proof

**Completed:** 2026-07-17

### Problem

Eino v0.9.12 can execute output-aware retry and model failover, but its default
backoff, retry limits, failover selection, error strings, and provider stream
assumptions do not match the project contract. Direct adoption would change
request counts, leak rejected partial output into history or checkpoint-visible
errors, pass a primary model option to a fallback provider, and duplicate
cumulative ToolCall arguments during Eino message concatenation.

### Resolution

- Added an explicit per-provider wrapper that binds the canonical route after
  inherited options, assigns one identity per underlying request, and sanitizes
  both call-time and mid-stream failures before Eino observes them.
- Added explicit delta and cumulative ToolCall argument modes. Cumulative state
  resets for each provider stream, requires a canonical stable call ID, emits
  only the new suffix, and fails closed on divergent snapshots without mutating
  provider-owned messages.
- Mapped the existing project backoff function into Eino
  `ModelRetryConfig`. A plain-data controller retains bounded/persistent retry
  state, consecutive 529 count, recovery usage, max-output escalation, attempt
  route/status, and terminal reason.
- Mapped three consecutive 529 failures to one lazy Eino failover while
  rejecting empty, missing, or same routes. The fallback provider wrapper
  overwrites inherited primary model options with its route.
- Mapped PTL/media recovery to persisted input-message rewrites and max-output
  recovery to one additional 64K option. Recovery eligibility, boundary count,
  and terminal classification remain project-owned.
- Extended the ADK event bridge so retry termination inside a streamed event
  produces attempt-aware tombstone semantics. Rejected attempts never enter the
  next provider input; raw provider errors never enter visible retry,
  attachment, terminal, or checkpoint strings.

### Evidence

- a real Eino Runner fixture proves exact
  `429 -> 529 -> partial 529 -> 529 -> fallback success` request count and route
  order, four rejected attempts, one accepted fallback, and no rejected partial
  assistant message in fallback history;
- focused fixtures prove project persistent elapsed caps, cancellation before a
  second request, same/missing fallback rejection, secret-safe error projection,
  and one model-fallback warning;
- PTL and media fixtures prove one persisted rewrite, one compact boundary, and
  project terminal reason after the second failure;
- max-output fixtures prove one 64K option escalation;
- cumulative/delta fixtures prove suffix conversion, repeat suppression,
  provider-message immutability, per-stream reset, and fail-closed identity or
  prefix divergence;
- P13.2/P13.3 focused tests and all 11 P13.0 canonical traces remain exact; and
- repository Makefile, lint-new, manifest, Markdown-link, diff, and independent
  runtime-depth review gates pass.

### Adoption Decision And Non-Goals

This slice is `adapt`: Eino owns the generic retry/failover execution skeleton,
while project code owns provider routing, policy, persistence-safe state,
recovery eligibility, event identity, and terminal meaning. It does not change
`queryLoop`, expose an ADK selector, execute a production model or tool through
ADK, persist a live ADK checkpoint, or promote the kernel. At this slice's
closeout, P13.5 still remained blocked on the executed-result
resume and structured after-tool decision SDK bridges frozen by P13.3.

## P13.5a Executed-Result Resume Bridge

**Completed:** 2026-07-17

### Problem

P13.3 could restore which calls had settled, but its schedule checkpoint did not
contain result bodies. Invoking a fresh Eino `ToolsNode` with the original
complete assistant message would therefore repeat settled side effects.
Eino v0.9.12 exports executed results as interrupt metadata but consumes them
through unexported resume state, so direct reuse would have required private
state or a fork.

### Resolution

- Added a strict versioned result checkpoint containing only round identity and
  ordered ordinal, call ID, tool name, and exact string content.
- Added a fixture-only bridge that freezes and validates the complete original
  assistant ToolCall set against the P13.3 schedule, reconstructs fresh Tool
  messages for settled results, and filters a cloned input to unresolved calls.
- Restored a fresh P13.3 coordinator and invoked one real public Eino
  `ToolsNode` for the filtered calls, then validated and merged persisted and
  fresh results in original model order.
- Made each bridge instance single-use before endpoint dispatch. Repeated or
  concurrent invocation fails closed, so one live object cannot rerun an
  unresolved side effect; later recovery requires a new validated checkpoint
  and bridge.
- Converted a first-task `ToolsNode` panic into a bridge error after the
  coordinator aborts and unblocks later batches. Cancellation and endpoint
  failure likewise return no complete round.

### Evidence

- a partially settled first safe batch executes only unresolved calls and
  returns every result in original model order;
- endpoint counters prove zero settled execution, exactly one unresolved
  execution, and no second dispatch under repeated or concurrent invocation;
- all-settled and none-settled fixtures prove the no-execution and full-node
  edges;
- strict JSON fixtures reject unknown/trailing data, wrong version/round,
  identity substitution, missing/extra/duplicate/reordered result entries, and
  mutated original input;
- cancellation, endpoint failure, and panic unblock later serial batches and
  return no complete result;
- focused race repetition, all 11 P13.0 canonical traces, repository Makefile,
  manifest, documentation, diff, and independent runtime review gates pass.

### Adoption Decision And Non-Goals

This slice is `adapt`: Eino retains public `ToolsNode` execution and ordered
collection, while the project owns strict persisted-result identity, filtering,
single-use recovery, and model-order merge. It did not alter the P13.3 schedule
wire format, Session durability, `queryLoop`, `QueryEngineConfig`, production
kernel selection, Eino source, or the full canonical tool outcome. P13.5b
remains the independent structured after-tool decision gate.

## P13.5c0 Typed Compose Graph Skeleton

**Completed:** 2026-07-17

### Problem

The exported `BuildQueryGraph` experiment had no production caller, discarded
the `Runnable` returned by `Compile`, treated user-visible turn count as a Graph
step budget, and branched after reading only the first model stream chunk. It
could not serve as a safe base for moving canonical model or tool rounds.

### Resolution

- Replaced the experiment with one internal fixture-only builder returning a
  compiled `compose.Runnable` with typed input, complete-round decisions, and
  terminal results.
- Made the explicit control path `prepare -> model -> tool -> prepare`, with
  ordinary complete-value branches for terminal, continue, successful return,
  and non-durable interrupt.
- Added fresh `WithGenLocalState` data per invocation. Local state contains only
  cloned plain data, round counters, decisions, and trace evidence; fixture
  functions remain immutable compiled-node dependencies.
- Deep-copied reference-backed input before a round hook can observe it and
  returned independently owned result slices.
- Reserved `WithMaxRunSteps` for infinite-loop protection without introducing a
  user-visible turn policy or Compose checkpoint.
- Kept `productionQueryKernel` fixed to Legacy and added no CLI, ACP, TUI,
  Session, transcript, provider, permission, or production selector surface.
- Deliberately removed the undocumented exported `BuildQueryGraph` and
  `QueryGraphConfig` experiment. They had no repository caller or supported
  entrypoint; external experimental importers must use `Query`/`QueryEngine`,
  and no public Graph-builder replacement is promised before cutover.

### Evidence

- typed fixtures prove no-tool terminal, tool-continue, tool-return, and
  non-durable interrupt paths with exact prepare/model/tool counts;
- return and interrupt reach no second prepare or model call;
- prepare, model, and tool failures stop later nodes and retain their error
  identity;
- a channel-controlled cancellation fixture stops before the tool node;
- a barrier-controlled ownership fixture proves caller mutation after input
  freeze cannot change the in-flight round, and result mutation cannot affect a
  later invocation;
- 32 concurrent invocations of one compiled Runnable pass under `-race` with
  independent run IDs, inputs, traces, counts, and terminal values;
- a bounded infinite loop terminates through the Graph step ceiling; and
- focused race, repository Makefile, manifest, documentation, diff, and
  independent runtime review gates pass.

### Adoption Decision And Non-Goals

This slice is `project-native`: public Eino Compose owns generic graph
compilation, cyclic scheduling, local-state lifecycle, typed branches, and the
run-step safety ceiling; project code owns node contracts and terminal meaning.
It does not bind a real model or tool, branch on a partial stream, create a
durable interrupt, change `QueryState`, select ProjectGraph in production, or
claim canonical trace parity. The source-compatibility break for the unsupported
exported experiment is intentional; keeping its raw-Graph signature would
preserve the owner this slice removes. P13.5c1 is the next accepted slice.

## P13.5c1 Canonical Model-Round Node

**Completed:** 2026-07-17

### Problem

P13.5c0's typed Graph still accepted an arbitrary fixture model function while
production `queryLoop` owned request preparation, provider options,
retry/fallback, stream normalization, terminal commit, and model-error
projection inline. Binding another implementation would create two model-round
owners. The plan also had to distinguish P13.4's fixture-only Eino
attempt/tombstone proof from the outer compact/recovery transitions intentionally
reserved for P13.5c3.

### Resolution

- Extracted one project-owned `runCanonicalModelRound` used by both Legacy
  `queryLoop` and the fixture-only project Graph adapter.
- Preserved the exact content-replacement budget, user-context and API message
  preparation, model-visible tool snapshot, provider options, retry/fallback
  route, complete-stream processing, outward events, and terminal mapping.
- Extended the existing terminal classifier result with
  `ToolCallsCommitted`. The Graph selects a tool branch only when the complete
  model round both needs follow-up and committed its call set.
- Forced the Graph model adapter to omit a streaming tool executor. A model node
  cannot run hooks, permissions, admission, or tool side effects; P13.5c2 owns
  that boundary.
- Propagated provider and stream failures as a typed Graph node error carrying
  the canonical Terminal and original error. Failures never become successful
  string terminals or enter Compose local state.
- Kept live models, contexts, callbacks, executors, and cancellation objects on
  the node stack. Compose local state receives only the existing plain-data
  decision and terminal value.
- Added a twelfth canonical compatibility trace for exact max-turn termination.

### Evidence

- focused Graph fixtures prove exact provider options and model-visible tools,
  no model-node tool execution, committed-call-only branching, fail-closed
  truncated/withheld calls, typed provider/stream failure preservation, exact
  three-primary-plus-one-fallback request order, and cancellation before the
  tool branch;
- Legacy and Graph share the same source boundary rather than duplicate its
  mechanics;
- all 12 canonical traces remain exact, including max-turn termination;
- focused engine, execution, and race suites pass; and
- repository Makefile, lint-new, manifest, documentation, diff, and independent
  runtime-depth review gates pass.

### Adoption Decision And Non-Goals

This slice is `combine`: public Eino Compose continues to own generic Graph
compilation and cyclic scheduling, while project code owns the observable
model-round contract and reuses existing Eino model/stream mechanics. It changes
no Eino source, fork, dependency, public API, entrypoint, session, transcript,
permission, or production kernel selector. P13.4's ADK retry/failover controller
and attempt tombstones remain fixture evidence because promoting them here
would change canonical outward retry events and introduce a second attempt
owner. Prompt-too-long, media, max-output, and compact recovery transitions
remain in `queryLoop` for P13.5c3. P13.5c2 is the next accepted slice.

## P13.5c2 Canonical Tool-Round Node

**Completed:** 2026-07-18

### Problem

P13.5c1 could hand a committed call set to the fixture Graph, but its tool node
was still an arbitrary function. Wiring Eino `ToolsNode` directly would reduce
the project result to string-only Tool messages and create a second owner for
stable batching, repeated-call admission, permissions, hooks, context
transition, attachments, file state, and sibling cancellation. Reimplementing
those contracts inside a Graph node would have the same dual-owner defect.

### Resolution

- Added `runCanonicalToolRound` as one project-owned node boundary. It validates
  the complete call set through `newADKToolSchedule`, retaining stable ID, tool
  name, argument digest, concurrency classification, and model order.
- Added `ExecuteCommittedToolCalls` as a narrow entry to the existing
  `StreamingToolExecutor`. It accepts only an already classified complete call
  set, commits once, and reuses the current adjacent-safe/serial-barrier
  scheduler, bounded concurrency, model-order collection, and Bash sibling
  cancellation.
- Routed every admitted call directly through canonical `executeToolCall`.
  Registry validation, plan and repeated-call guards, pre/post/failure hooks,
  QueryEngine permission coordination, input rewrites, execution, large-result
  offload, attachments, file snapshots, progress, and context transitions keep
  their existing owners.
- Retained rich `toolExecutionOutcome` values by stable call ID until
  `decideADKAfterTool` validates the complete round and selects continue,
  successful return, or interrupt. The Graph adapter projects only cloned Tool
  messages, terminal reason, and tagged branch data.
- Split terminal waiting from tool execution contexts. Invocation and
  cancellation-chain cancellation still reach every tool; AbortController
  cancellation rejects queued calls, cancels running `cancel` tools, and lets
  running `block` tools settle naturally. No live context owner enters Compose
  state. AbortController remains a non-durable typed interrupt; durable HITL
  remains P13.8.
- Froze model-committed ToolCalls and tool-result messages through JSON
  round-trip before storing or returning Graph state. Concurrent invocations of
  one compiled Runnable retain independent values, call IDs, and result data.

### Evidence

- focused fixtures prove dynamic registry/schema registration at the
  model-to-tool boundary, exactly-once preparation/execution, adjacent safe
  batches around serial barriers, and model-ordered events and Tool messages;
- repeated identical calls share the query-local guard across Graph rounds,
  with the third call blocked before permission and execution;
- matching concurrent calls pass through the existing project permission
  coordinator, commit one session grant, and resolve the follower through the
  canonical coalesced path;
- hook input rewrite, permission denial, post-hook return, EnterPlanMode
  context transition, task/progress projection, and canonical result envelope
  remain on `executeToolCall`;
- active Plan mode rejects Bash before permission/execution while the exact
  non-symlink session plan Write remains admitted through the same Graph tool
  node;
- Bash failure cancels the running safe sibling and prevents the queued serial
  sibling from executing, while still returning one result for every model
  call in model order;
- cooperative `cancel` tools observe Abort cancellation, cooperative `block`
  tools retain a runnable context and settle before the non-durable interrupt,
  invocation cancellation still returns the original context error, and
  concurrent Graph invocations pass race validation without state leakage; and
- focused engine/execution, repeated-run, race, repository Makefile, lint-new,
  manifest, documentation, diff, and independent runtime-depth review gates
  pass.

### Adoption Decision And Non-Goals

This slice is `combine`: public Eino Compose owns Graph compilation, cyclic
scheduling, branch routing, and per-invocation local state; project code owns
the tool policy/result contract and reuses its established scheduler and
executor. P13.3 `ToolsNode` middleware and P13.5a resume bridge remain
compatibility fixtures because their string-result boundary cannot carry the
rich live outcome without a second scheduler. No Eino source, fork, dependency,
public API, provider request, production selector, entrypoint, transcript,
checkpoint, or durable interrupt behavior changed. Production remains Legacy.
P13.5c2 closes the canonical tool-round boundary; current execution order and
the remaining P13.5c3 dependency gate live in `docs/migration/PLAN.md`.

## P15.0 Deterministic Terminal Stress Baseline

**Completed:** 2026-07-17

### Problem

The existing lifecycle suite proved normal, panic, suspend/resume, and PTY
restoration but did not establish what happens when the terminal writer itself
blocks or fails. Adopting Grok Build's writer thread without reproducing a
local failure would have introduced a second queue and teardown owner without
evidence.

### Resolution

- Added channel-controlled `io.Writer` barriers around the pinned Bubble Tea
  v1.3.10 program path. Graceful quit, model panic, and
  `ReleaseTerminal`/`RestoreTerminal` use the real renderer ordering.
- Added a Unix PTY subprocess whose reader pauses after startup, whose writer
  holds a sustained streaming/tool/Agent frame, and whose parent shell emits a
  sentinel only after normal teardown.
- Added resize-plus-rapid-invalidation and post-shutdown send probes. They
  distinguish one replacement frame plus event-loop backpressure from an
  unbounded queue and reject presentation after restore.
- Added a runtime-state probe that applies permission and terminal events
  before intentionally omitting the permission presentation frame. Terminal
  identity and unresolved attention remain in the engine snapshot.
- Kept production options, terminal writers, event ordering, and runtime state
  unchanged.

### Evidence

- graceful quit, panic cleanup, and terminal release remain blocked until the
  held writer returns; after release, every application marker precedes the
  alternate-screen restore;
- a returned writer error is invoked repeatedly but does not become a Bubble
  Tea program error or diagnostic;
- ten repeated race runs of all P15.0 command/PTY fixtures pass;
- focused runtime-state tests pass;
- the Unix PTY receives the complete progress frame and restoration suffix,
  then the parent shell emits `P150_SHELL_USABLE`; and
- repository Makefile, manifest, docs, diff, and independent lifecycle review
  gates pass.

Reproduction commands and evidence limits are in
[`terminal-output-resilience.md`](../../verification/terminal-output-resilience.md).

### Adoption Decision And Next Gate

This slice is `combine`: it adapts Grok Build's drain-before-restore invariant
and Pi's coalescing concern into project-native measurements while retaining
Bubble Tea as the normal-path production owner. The deterministic
blocked-writer result opens P15.1. P15.1 must choose the smallest bounded
mechanism that surfaces one writer failure, drains or abandons by deadline,
rejects late frames, and never creates two concurrent terminal writers. P14.3
remains blocked until the failing fixture reruns green.

## P15.1 Bounded Terminal Output Ownership

**Completed:** 2026-07-17

### Problem

The P15.0 production-equivalent fixture proved that Bubble Tea cannot cancel a
blocked output sink and ignores returned writer errors. Quit, panic cleanup,
and terminal release could therefore wait forever, while fallback restoration
had no proof that the last application writer had stopped.

### Resolution

- Added one project-owned `TerminalOutput` adapter beneath Bubble Tea. An
  unbuffered request channel and synchronous write acknowledgement permit one
  copied packet in flight and introduce no second renderer queue.
- Added typed 750 ms write, 1 s drain, and 250 ms platform-interrupt budgets.
  Close rejects later writes, reports every reached timeout category, and
  leaves `Stopped` false when the physical writer cannot be interrupted.
- Added Unix descriptor duplication with nonblocking netpoll deadlines and
  original-flag restoration. Added Windows handle duplication with a pinned
  writer thread and `CancelSynchronousIo`.
- Wired one failure signal to kill the Bubble Tea program. The entrypoint
  returns one terminal-output diagnostic and performs direct fallback cleanup
  only after the writer has stopped.
- Retained P15.0 direct Bubble Tea fixtures as dependency characterization and
  moved the completed detailed contract out of active plans.

### Evidence

- twenty repeated race runs cover ordered writes, concurrent callers,
  one-in-flight bounds, late-write rejection, first-failure retention, and
  typed write/drain/interrupt deadlines;
- ten repeated race runs cover the production fail-closed seam plus real PTY
  normal and panic restoration;
- Unix pipe saturation proves the duplicate writer deadline stops and restores
  the original descriptor's nonblocking state;
- Windows command/TUI and Linux TUI cross-compilation pass;
- five in-memory benchmark runs observe approximately 0.75 microseconds,
  442 bytes, and six allocations per packet on the development Apple M5 Pro;
  and
- repository Makefile, manifest, docs, diff, and independent lifecycle review
  gates pass.

Exact commands and categorical results are in
[`terminal-output-resilience.md`](../../verification/terminal-output-resilience.md).

### Adoption Decision And Remaining Gate

This slice is `combine`: it preserves Bubble Tea's renderer and mode lifecycle,
adapts Grok Build's drain-before-restore invariant, and implements a
project-native synchronous writer boundary. It rejects both a copied reference
writer thread and a second project frame queue. The P15 terminal gate is
closed. P14.3 is still blocked by P14.2c, and the overall execution queue is
blocked at the P13.5 Eino SDK capability gates.

## P17.H0 Fail-Closed Plan Admission

**Completed:** 2026-07-18

### Problem

Plan Mode hid no capabilities from the model, trusted per-tool `IsReadOnly`
metadata at execution, admitted Write/Edit by a lexical plans-directory prefix,
and allowed Exit to reach ordinary permission handling while inactive. An
explicit allow rule or a wrongly marked dynamic tool could therefore broaden
planning authority, and `allowedPrompts` output falsely described semantic
requests as granted permissions.

### Resolution

- Added one central Plan capability decision shared by model-visible assembly,
  QueryEngine permission wrapping, and canonical `executeToolCall` defense.
- Kept the dispatch registry complete while projecting only explicit
  exploration/clarification tools, exact-plan-file Write/Edit, and Exit during
  Active mode. Bash, Agent/background, ordinary mutators, unknown dynamic/MCP
  tools, and Enter fail closed.
- Replaced the directory-prefix exception with the exact absolute
  session/Agent plan-file identity and rejected lexical aliases, other
  identities, target symlinks, and symlinked parents.
- Made plan-path calculation side-effect free; admission no longer creates the
  plans directory before the exact Write/Edit executor runs.
- Rejected inactive Exit and all disallowed Active calls before repeated
  admission, hooks, permission presentation, or execution.
- Re-evaluated the same decision after pre-tool and permission input rewrites,
  preventing an initially valid plan write from being redirected before
  execution.
- Applied the same projection to leader and child QueryEngine construction and
  retained between-round refresh as the only model-surface update boundary.
- Reworded `allowedPrompts` as non-authoritative requested implementation
  capabilities; concrete actions still use the active permission policy.

### Evidence

- focused tests freeze deterministic default/Active pools, registry-free
  construction, projection/execution agreement, and fail-closed unknown
  read-only metadata;
- path fixtures cover prefix siblings, traversal and relative aliases, another
  session or Agent, target symlinks, and a symlinked parent;
- negative execution fixtures prove denied calls reach neither permission nor
  execution, including inconsistent Plan flag/permission snapshots;
- mutation fixtures prove pre-tool hooks, permission callbacks, and a target
  swapped to a symlink before execution cannot redirect an admitted plan write;
- exact Write/Edit and non-Plan tool-selection behavior remain available;
- focused engine, tools, ACP, and race suites pass; and
- repository Makefile, documentation, diff, and independent permission review
  gates pass.

### Adoption Decision And Remaining Gate

This slice is `combine`: it adapts the reference Plan capability outcomes into
one project-owned fail-closed policy and keeps runtime defense in depth. It
changes no Eino source, dependency, Graph selector, persistence schema, or TUI
approval protocol. It also does not replace generic Write/Edit with
descriptor-relative no-follow filesystem operations; the final same-user
check-to-open race remains outside this H0 threat boundary. P13.5c2 later
consumed this gate, and P17.0 later closed engine phase ownership. P17.1-P17.2
were split afterward; P17.1 now closes structured Exit settlement, while P17.2
still owns cold-resume convergence.

## P17.0 Engine-Owned Plan State

**Completed:** 2026-07-18

### Problem

P17.H0 made Plan capability admission fail closed, but permission mode,
active-turn `ToolUseContext`, external entrypoints, and TUI presentation still
formed separate mutable views. Enter/Exit context modifiers also ran at
different points in Legacy streaming, batch, shared model-round, and fixture
Graph tool paths. A cancelled or losing result could therefore make ownership,
event ordering, and next-round tool refresh ambiguous.

### Resolution

- Added one `QueryEngine`-owned `PlanState` with phase, exact plan-file
  identity, return-mode context, approval-request slot, and monotonic revision.
  P17.0 uses only Inactive and Active; AwaitingApproval remains reserved for
  P17.1.
- Serialized every tool and external transition behind one safe-turn boundary.
  A tool transition must own the current turn; an ACP, command, or TUI change
  while that turn is active fails without mutation and can retry afterward.
- Derived the active query's Plan flag, permission mode, plan path, execution
  policy, and model-visible refresh from the engine state. The next model call
  sees the new Enter/Exit surface only at the existing completed-round refresh
  boundary.
- Split transition commit from event publication. Legacy streaming, Legacy
  batch, the shared canonical model round, and the fixture Graph canonical tool
  round first accept the canonical tool result, then commit the context/state,
  yield the corresponding Tool result, and finally publish one typed Plan
  event. A transition validation failure converts that result to an error.
- Added a lossless `plan_state_transition` event with session, thread, turn,
  request, source, phase, plan identity, return mode, and revision. The runtime
  reducer stores a bounded Plan projection; replay reconstructs it without
  invoking a model, tool, permission callback, or transition.
- Routed `/mode`, TUI mode changes, and ACP session-mode changes through the
  same engine method. TUI event handling is projection-only for tool-originated
  changes.
- Rebuilt the in-memory Plan snapshot from the existing persisted permission
  mode on session resume. No new durable Plan record or approval callback
  revival was introduced; those remain P17.2.

### Evidence

- repeated Enter calls in one model result serialize: the first executes and
  commits, the second fails admission, and only one typed transition follows
  its matching Tool result;
- the next real model binding replaces Enter with Exit only after the committed
  transition;
- failed execution, permission denial, cooperative cancellation,
  non-cooperative success returned after cancellation, and an external change
  losing an active-turn race leave phase, revision, permission mode, and event
  sequence unchanged;
- the fixture Graph canonical tool node consumes the same engine transition,
  preserves result-before-event order, and updates the same reducer projection;
- direct reducer replay reconstructs the Plan projection without dispatch;
- existing persisted `permission_mode=plan` restores one Active engine snapshot
  with the exact resumed session/Agent plan identity;
- focused engine, execution, TUI, ACP, canonical-trace, and race suites pass;
  and
- repository Makefile, lint-new, manifest, documentation, diff, and independent
  runtime-depth review gates pass.

### Adoption Decision And Remaining Gate

This slice is `combine`: Eino Compose retains generic Graph scheduling while
the project owns Plan lifecycle semantics, canonical tool-result acceptance,
runtime reduction, and entrypoint behavior. Both production Legacy and the
fixture Graph consume that project boundary. No Eino source, fork, dependency,
production selector, durable schema, or structured approval protocol changed.
P17.1 subsequently closed typed Exit settlement. P17.2 owns persistence, cold
AwaitingApproval normalization, and cross-entrypoint restart evidence before
P13.5c3 can close the complete fixture kernel.

## P17.1 Structured Exit Approval

**Completed:** 2026-07-18

### Problem

P17.0 made Plan phase authoritative but Exit still used the generic permission
shape. TUI choices collapsed to one allow value, plain and ACP exposed generic
grant scopes, and no typed request/revision value connected approval to the
eventual canonical Exit result. A generic allow, cancellation race, or hidden
dialog could therefore be mistaken for authority to leave Plan Mode or expand
permissions.

### Resolution

- Added immutable `PlanApprovalRequest` and terminal `PlanApprovalDecision`
  values carrying exact request/revision identity, plan file, return-mode
  context, approve/reject, confirmation, selected target, and feedback.
- Added the AwaitingApproval transition. Presentation begins only after Active
  records the request; the coordinator's one terminal claim always returns the
  phase to Active before publishing its typed resolution.
- Kept Exit outside generic positive grants. Rules, hook allow, yolo and
  auto-allow modes, persisted approvals, permission coalescing, and semantic
  `allowedPrompts` cannot settle a Plan request. `acceptEdits` and
  `bypassPermissions` require explicit confirmation.
- Passed the validated decision through an engine-owned context side channel
  to canonical tool execution. The chosen target mode commits only if Exit
  executes successfully and its Tool result wins the same Legacy/fixture Graph
  commit boundary introduced in P17.0.
- Added a synchronous scheduler interruption callback so cancellation settles
  the coordinator before a cancel-behavior tool receives its synthetic result,
  including noncooperative executors that return late success.
- Mapped exact choices in TUI, plain, and ACP. The TUI reads the engine-provided
  plan identity, retains rejection feedback, and rejects the live request
  exactly once when switching away from its owner thread.
- Kept `allowedPrompts` only as deprecated display metadata and removed the
  remaining wording that implied a runtime grant.

### Evidence

- approve-to-default, accept-edits, and bypass choices produce distinct final
  modes only after the matching Tool result;
- generic allow, unconfirmed expansion, missing structured providers, reject,
  feedback, timeout, cancellation, engine Close, wrong owner, duplicate
  response, and owner-thread switch remain Active and perform no Exit side
  effect;
- cancellation settlement precedes the synthetic interrupted result under
  repeated race execution, even when the executor ignores cancellation;
- lossless request, resolution, and Plan transition events retain exact
  identity and reconstruct the bounded runtime projection without dispatch;
- production Legacy and the fixture Graph pass the same structured approval,
  tool-result ordering, and target-mode assertions; and
- focused engine, execution, tools, TUI, plain, ACP, Graph, race, repository
  Makefile, lint-new, documentation, diff, and independent runtime/TUI review
  gates pass.

### Adoption Decision And Remaining Gate

This slice is `combine`: Eino Compose still owns only generic fixture Graph
scheduling. The project owns approval identity, authorization, cancellation,
entrypoint behavior, and canonical target-mode commit. Production remains
Legacy, and no Eino source, fork, dependency, selector, or durable Session
schema changed. P17.2 owns versioned persistence, cold AwaitingApproval
normalization, and restart evidence before P13.5c3.

## P17.2 Persistence, Replay, And Entrypoint Convergence

**Completed:** 2026-07-18

### Problem

P17.1 owned the live approval lifecycle, but a session checkpoint still stored
only permission mode and diagnostic request IDs. Resume rebuilt a fresh
Active/Inactive state, losing exact phase, revision, return-mode context, and
the original plan-file identity. ACP Resume and Load created an engine without
calling its restore API. A changed `HOME` could also make Write/compaction use
the persisted path while Enter/Exit recomputed another path.

### Resolution

- Added an additive versioned `plan_state` record containing only phase, exact
  plan-file identity, return mode, request reference, and revision.
- Sampled Plan state and permission mode under one `planMu -> mu` checkpoint
  order. No callback, decision, response channel, or permission grant is
  serialized.
- Added validation and cold normalization. Active restores directly.
  AwaitingApproval without an exact live owner clears its request, increments
  revision, returns to Active, and emits a recovery warning. Unsupported or
  corrupt records preserve Plan containment when legacy metadata was Plan.
- Tightened actionability to require both an unresolved reducer request and the
  original process-local coordinator owner for the final restored project. A
  live Plan request additionally matches session, thread, request, revision,
  exact file, and return mode. Crossing a project/coordinator boundary cancels
  the old callback and cold-normalizes the durable state.
- Rebuilt the bounded runtime Plan projection directly, without a synthetic
  event or dispatch. TUI runtime reconciliation carries the typed Plan identity
  only for an unresolved live request.
- Carried the validated exact Plan-file identity through canonical tool
  execution. Write/Edit admission, Enter/Exit, compaction, production Legacy,
  and the fixture Graph now consume the same capability across `HOME` changes.
- Routed ACP Resume and Load through `QueryEngine.ResumeSession` and projected
  the resulting phase/warning count through the existing ACP status extension.

### Evidence

- cold Active preserves exact path, return mode, and revision without model or
  tool dispatch;
- cold AwaitingApproval becomes Active with a new revision, cannot consume the
  prior decision, and checkpoints the normalized record;
- unsupported versions and unsafe identities fail closed without expanding
  permissions;
- an exact same-process, same-project reducer/coordinator request remains
  singular, while stale, cross-project, or owner-mismatched references are
  diagnostic only;
- restored compaction and canonical tool execution use the persisted exact
  path, and the fixture Graph executes the same admitted capability;
- TUI replay reconstructs the exact typed request, and ACP Resume/Load restore
  actual messages plus normalized Plan state; and
- focused engine, tools, TUI, ACP, Graph, compaction, replay, and race suites
  plus repository closeout gates pass.

### Adoption Decision And Next Gate

This slice is `combine`: Eino Compose remains the fixture Graph mechanic, while
the project owns persistence schema, approval authority, recovery, exact file
capability, and entrypoint semantics. Production remains Legacy. No Eino
source, fork, dependency, or production selector changed. P13.5c3 is unblocked.

## P13.5c3 Complete Project Graph Inner Kernel

**Completed:** 2026-07-18

### Problem

P13.5c2 proved the canonical model and tool nodes, but `queryLoop` still held
the only executable copy of between-round compact/recovery, stop-hook, queue
safe-point, reinjection, max-turn, and terminal behavior. The fixture Graph
therefore could not run the canonical entrypoint suite as one complete inner
kernel. Its deferred model path also lacked Legacy's exact rejected-tool result
for a truncated uncommitted turn.

### Resolution

- Extracted canonical preparation, after-model, and after-tool lifecycle
  functions and one per-invocation live runtime constructor. Production
  `queryLoop` now traverses those functions instead of retaining private policy
  copies.
- Extended the project Graph to
  `freeze → prepare → model → reconcile → tool/reconcile → finalize`.
  Preparation and reconciliation return typed branch decisions; hidden
  imperative operations remain inside their owning boundary.
- Kept hooks, registries, queues, budgets, prefetch, recovery, cancellation,
  transcript, callbacks, and rich outcomes in an invocation-local runtime
  carried by context. Compose local state remains cloned plain data.
- Added deferred stream classification. It retains the same complete commit or
  rejected-truncation decision and exact model-ordered rejection results while
  leaving every committed side effect to the Graph tool node.
- Added terminal finalization that cancels the live round and flushes the
  transcript before returning the canonical Terminal. `Query` still owns
  post-terminal command-lifecycle completion.
- Added an unexported fixture context selector so the real `QueryEngine`
  entrypoint trace can run ProjectGraph. Production selection remains fixed to
  Legacy and no public config, CLI, or environment selector was added.
- Assigned P13.6b to remove ADK construction, attempt, normalizer, result-resume,
  and checkpoint evidence. The live schedule and typed decision currently
  co-located in `adk_scheduler.go` must first move under project-owned names.

### Evidence

- all 12 canonical direct-query and QueryEngine entrypoint traces match Legacy
  without golden updates;
- the truncated-tool fixture retains its exact rejected result, follow-up
  request, event order, and zero tool executions;
- focused topology tests prove prepare/model/reconcile/tool/finalize routing,
  exactly-once finalization, failure closure, and terminal cancellation;
- one compiled Runnable isolates 32 concurrent live query runtimes under the
  race detector;
- Legacy and ProjectGraph both complete a 40-tool-round unlimited fixture whose
  Graph path exceeds the former 128-step default; the shared Runnable now uses
  Eino's `math.MaxInt` ceiling so project max-turn, recovery, cancellation, and
  terminal policy remain authoritative; and
- the complete `engine/...` test tree, all four repository Makefile gates,
  `lint-new`, migration manifest, documentation links, diff hygiene, and an
  independent runtime review pass after the shared lifecycle extraction.

### Adoption Decision And Next Gate

This slice is `combine`: public Eino `compose.Graph` owns compilation, cyclic
node scheduling, branching, and per-invocation local state; project code owns
provider, compact/recovery, queue, model/tool, terminal, transcript, and event
contracts. It changes no Eino source or dependency and does not call
`ChatModelAgent`, `Runner`, or `ToolsNode` in the selected Graph. Production
remains Legacy. P13.6a owns the new-session canary, followed by P13.6b adapter
retirement.

## P13.6a ProjectGraph New-Session Canary

**Completed:** 2026-07-18

### Problem

P13.5c3 proved the complete ProjectGraph inner lifecycle only through a
deterministic selector. Production `QueryEngine` sessions still had no
single-selection, persistence, entrypoint, or rollback boundary. A direct
cutover or live fallback could have duplicated a model request or a
side-effecting tool call, while switching an existing session would make its
durable transcript ambiguous across restarts.

### Resolution

- Added an internal, default-off process rollout with ordered `no_tools`,
  `read_only`, and non-MCP `local_tools` cohorts. No public, CLI, or
  `QueryEngineConfig` kernel selector was added.
- Compiled one process-shared ProjectGraph Runnable. Each invocation still
  creates fresh canonical live state and plain Compose local state.
- Selected exactly one `legacy/v1` or `project_graph/v1` kernel for every new
  Session and persisted the version, cohort, and incompatibility diagnostic in
  existing Session metadata. Pre-canary transcripts remain Legacy.
- Restored the stored kernel before resume mutation. Unknown versions and
  invalid Graph metadata fail before model execution or transcript rewrite.
- Revalidated a Graph session's model-visible tool cohort at every model-round
  boundary, including after the between-round tool refresh. Cohort drift fails
  before the next provider request and never changes the session to Legacy.
- Routed normal QueryEngine turns through the pinned kernel while leaving
  direct `Query` fixed to Legacy. Graph failure returns its original terminal
  and is never replayed through Legacy.
- Bound ACP engine Session/Thread identity to the ACP Session ID so the same
  durable record owns protocol identity and kernel metadata. TUI, plain,
  headless, ACP, and child Agents all continue to construct `QueryEngine`
  rather than owning an entrypoint-specific selector.

### Evidence

- focused stage tables prove fail-closed no-tool, read-only, local-tool, MCP,
  missing-contract, invalid-stage, and default-off selection;
- new, pre-canary, persisted Legacy, persisted Graph, explicit resume, unknown
  version, and transcript-byte fixtures prove one durable version and
  fail-before-mutation restore;
- no-tool, read-only, and local mutator runs prove exact provider and tool
  counts through the real ProjectGraph kernel;
- a real Explore child and an ACP NewSession/Prompt path persist ProjectGraph
  metadata with their exact Session/Thread/Agent identities;
- a forced Graph failure records one Graph invocation, zero Legacy/provider
  replays, and one terminal; dynamic registry mutation before a turn and after
  one local tool round both fail before the next model request without
  switching kernels;
- the P13.2 reflection guard still proves that `QueryEngineConfig` exposes no
  kernel or ADK selector;
- focused race, complete `engine/...` plus ACP regression, all repository
  Makefile gates, `lint-new`, manifest, documentation, diff, and independent
  persistence/runtime review pass.

### Adoption Decision And Next Gate

This slice is `project-native`: public Eino `compose.Graph` owns generic graph
compilation and traversal, while Eino-Agent owns rollout policy, session
identity, compatibility cohorts, persistence, failure semantics, and rollback.
It changes no Eino source or dependency and adds no ADK fallback. P13.6b is now
ready to extract the live project schedule/decision from ADK-named evidence and
delete the remaining unused ADK construction, attempt, normalizer, resume, and
checkpoint adapters.

## P13.6b ADK Compatibility Retirement

**Completed:** 2026-07-18

### Problem

P13.2-P13.5a left a second, fixture-only ADK construction path after P13.6a
proved that eligible production sessions could run the project-owned Compose
Graph with no ADK fallback. Keeping both owners would preserve dead retry,
normalization, result-resume, checkpoint, and `ToolsNode` middleware concepts
inside the query kernel and obscure which schedule and continuation contract
was actually live.

### Resolution

- Deleted seven ADK-only production files and five associated test files:
  construction/event/tool adapters, model-attempt and provider wrappers,
  stream normalization, result resume, checkpoint codecs, and the `ToolsNode`
  batch coordinator.
- Extracted the live stable-batch plan, schedule digest, call/name/argument
  identity, rich outcome, and typed complete-round decision into
  `tool_schedule.go`.
- Renamed every surviving ADK-prefixed schedule/decision symbol and error under
  project ownership, including Graph, lifecycle, Plan cancellation, and
  canonical tool-round consumers.
- Removed `queryKernelADK` without adding another selector, fallback owner,
  persistence format, model request, or tool execution path.
- Left the Eino v0.9.12 dependency and public `compose.Graph` usage unchanged.

### Evidence

- the engine tree contains no `adk_*` Go file, ADK import, or ADK-prefixed Go
  owner;
- focused P13.5/P13.6, canonical, ProjectGraph, new schedule/decision, and race
  tests pass without golden changes;
- the complete `engine/...` and ACP test trees pass after the deletion;
- all four repository Makefile gates, documentation/manifest validation, and
  diff hygiene pass; and
- source scanning records six fewer production files and 2,581 fewer
  production lines, plus four fewer test files and 2,598 fewer test lines.

### Adoption Decision And Next Gate

This slice is `project-native`: Eino Compose remains the generic graph
mechanism, while Eino-Agent owns stable tool scheduling, typed continuation,
recovery, persistence, and rollback. P13.7 remains accepted but waits for the
P16 command-owner convergence through P16.5d before replacing the live input
coordinator.

## P16.H0 Unowned Credential Deletion Containment

**Completed:** 2026-07-18

### Problem

The visible `/logout` command deleted non-directory files below project and
user `.claude/credentials` and `.claude/auth` directories even though
Eino-Agent owns neither those stores nor a provider authentication lifecycle.
The TUI then projected the returned action as a successful logout. An empty,
known-provider, unknown-provider, or cancelled invocation could therefore
cross a destructive ownership boundary.

### Resolution

- Removed both credential-deletion helpers and every filesystem read/delete
  path from the command instead of guarding them behind a flag.
- Retained `/logout` for one compatibility window as non-mutating guidance.
  It identifies environment/configuration/provider ownership, returns
  `ActionNone`, and never reports a successful logout.
- Kept provider-name normalization only to show relevant environment-variable
  names. Unknown providers return the same explicit non-mutation boundary.
- Reclassified the two Claude logout mappings as exclusions because the
  project has no credential store or auth lifecycle to preserve.

### Evidence

- isolated project and user `.claude/credentials` and `.claude/auth` fixtures
  remain byte-identical after empty, known-provider, alias, and unknown-provider
  invocations;
- an already-cancelled `CommandDispatcher` invocation also leaves every fixture
  byte-identical and returns only the informational result;
- `cmd_logout.go` contains no credential-tree discovery or delete call; and
- focused command tests, all repository Makefile gates, documentation and
  manifest checks, diff hygiene, and independent security review pass.

### Adoption Decision And Next Gate

This slice is `project-native`: a command may not mutate storage the project
does not own merely to preserve reference naming. P16.0 is now ready to repair
plain/parser correctness before P16.1 removes `/logout` from default discovery.

## P16.0 Plain And Parser Correctness

**Completed:** 2026-07-18

### Problem

The plain REPL printed local success for `/clear` before calling
`SetResumedMessages(nil)`, which intentionally changed nothing, and never
applied `/compact`. It also read the wrong fork result key. `/undo` truncated
inside the command and again inside plain despite having no durable reversible
turn history. Production `Registry.Dispatch` bypassed the existing quote-aware
tokenizer and command validation, so multi-word permission rules could be
split and persisted incorrectly.

### Resolution

- Routed plain clear/compact, including clear aliases, directly through the
  existing `QueryEngine` command path and rendered its terminal result. The
  engine and transcript remain the single mutation owner.
- Switched plain fork handling to the canonical `new_session_id` result.
- Made `/undo` return explicit unavailable with `ActionNone` before inspecting
  or mutating live messages. Durable undo remains dependency-gated.
- Made production registry dispatch use the shared quote-aware tokenizer,
  populate `RawInput` and `Args`, and invoke `ValidateArgs` before execution.
  Permission add/remove consumes parsed arguments rather than re-splitting
  quoted input.
- Kept the command registry and action enum intact. Headless and ACP command
  text projection remains an explicit P16.3 gap rather than being hidden by
  this correctness slice.

### Evidence

- focused registry tests prove exact quoted argument preservation and
  validation-before-execution;
- an isolated permission fixture persists `Bash(rm -rf *)` as one project rule;
- undo tests prove the unavailable result and zero message mutation;
- plain tests prove canonical clear and LLM compact state replacement plus the
  fork key contract;
- existing TUI clear/compact tests and new headless/ACP visibility
  characterization cover all supported entrypoint projections; and
- all repository Makefile gates, migration manifest validation,
  documentation checks, and diff hygiene pass.

### Adoption Decision And Next Gate

This slice is `combine`: it preserves the project-owned QueryEngine state and
transcript owner while reusing the existing shared tokenizer and validation
contract. It changes no Eino source, dependency, Graph topology, kernel
selection, or durable schema. P16.1 is now ready to remove false discovery and
the semantically wrong `/new -> /clear` alias.

## P16.1 Truthful Visibility And Alias Cleanup

**Completed:** 2026-07-18

### Problem

Default help still advertised commands whose effects were absent, stale,
unsafe, dependency-gated, process-local, or limited to the TUI. The TUI also
pre-applied hidden `/mode` and `/rewrite` behavior before registry dispatch.
Most seriously for session semantics, `/new` remained an alias for `/clear`,
so a name that implies creating a fresh session erased the active
conversation instead.

### Resolution

- Removed `/new` from the `/clear` aliases while retaining `/reset`.
- Froze default discovery at 46 visible and 20 hidden built-ins.
- Hid `/plugin`, `/bug`, `/undo`, `/rewrite`, `/branch`, `/rewind`,
  `/color`, `/fast`, `/tag`, `/share`, `/release-notes`,
  `/terminal-setup`, `/mode`, `/bypass`, `/team`, and `/queue`; retained
  `/logout`, `/env`, `/output-style`, and `/session` as hidden.
- Replaced hidden unsupported handlers at registration with one explicit
  compatibility result carrying `ActionNone`, so direct canonical or alias
  dispatch cannot silently reach the old effect.
- Removed the TUI's pre-registry `/mode` and `/rewrite` paths. `/team` and
  `/queue` remain explicit TUI-local interactions rather than universal
  runtime commands.

### Evidence

- one exact snapshot test freezes the 46 visible names, 20 hidden names, and
  every built-in alias;
- direct canonical and alias dispatch tests prove compatibility text and no
  action for every hidden unsupported command, while `/logout` remains
  informational;
- plain entrypoint coverage proves `/new` cannot enter the engine-owned clear
  path;
- TUI regression tests prove hidden `/mode` cannot change engine mode and
  hidden `/rewrite` cannot open the selector before dispatch; and
- focused cross-entrypoint tests, all repository Makefile gates,
  documentation/manifest checks, diff hygiene, and independent review pass.

### Adoption Decision And Next Gate

This slice is `combine`: it preserves the project registry and TUI-local
interactions, while enforcing the accepted project-owned visibility contract
through the current registration boundary. It changes no Eino dependency,
Graph topology, kernel selector, action schema, or durable state. P16.2 is now
ready to replace the post-registration compatibility layer and legacy string
executors with one typed, entrypoint-aware command contract.

## P16.2 Canonical Command Contract

**Completed:** 2026-07-18

### Problem

Production entrypoints called `Registry.Dispatch`, while a richer
`CommandDispatcher` existed only in tests. Command records did not declare
entrypoint scope, typed availability, dependency, side-effect, result shape,
compatibility, or a cancellation-aware handler. Help, completion, TUI-local
interception, plugin installation, and dispatch therefore inferred different
views of the same registered names.

### Resolution

- After entrypoint ownership classification, made `Registry.Dispatch` the only
  strict parse, validation, availability, cancellation, and execution boundary
  and deleted the test-only dispatcher.
- Added explicit command kind, entrypoint set, availability and reason,
  dependency, side-effect, result kind, compatibility, argument schema, and
  `context.Context`-aware execution metadata.
- Adapted existing string-based inner executors during canonical registration
  without retaining a second live handler shape in registry records.
- Normalized canonical names and aliases, rejected built-in, alias, case-only,
  and plugin collisions before installation, and returned cloned lookup and
  discovery snapshots.
- Registered `/search`, `/team`, and `/queue` as TUI-only records. TUI help,
  completion, palette, and dispatch now consume the same filtered snapshot.
- Bound TUI, plain, headless, and ACP composition roots to explicit
  entrypoints. Headless and ACP intentionally expose only the four currently
  engine-owned slash commands.

### Evidence

- one exact all-command and alias matrix covers TUI, plain, headless, ACP, and
  CLI-administration entrypoints;
- focused tests prove context propagation, cancellation-before-handler,
  malformed-quote rejection, validation-before-execution, normalized collision
  rejection, immutable lookup snapshots, and atomic plugin reload;
- parser fuzz seeds cover empty, quoted, escaped, and malformed inputs;
- focused command/plugin/engine/TUI/plain/ACP tests and command/TUI race tests
  pass; and
- all repository Makefile gates, documentation/manifest validation, and diff
  hygiene pass.

### Adoption Decision And Next Gate

This slice is `combine`: it retains project-owned command semantics and current
runtime effects while combining explicit entrypoint/availability metadata with
one strict production dispatch contract. It changes no Eino dependency,
Compose Graph topology, kernel selector, action schema, or durable state.
P16.3 is now ready to replace legacy `Action`/`Data` application switches with
one typed engine/service owner and consistent headless/ACP projection.

## P16.3 Single Action Owner And Typed Projection

**Completed:** 2026-07-20

### Problem

The canonical registry still returned mutation-bearing `Action`/`Data`
results to several entrypoint-specific switches. A supported command could
therefore be applied by different owners, while headless and ACP silently
discarded ordinary command output. Session-changing commands could also drift
their result envelope to the newly activated identity. Finally, allowing a
command to mutate during an active model turn would let the reducer reject its
later event after the side effect had already happened.

### Resolution

- Declared each command's execution owner and rejected entrypoint-owned
  commands before handler execution in the engine path, including hidden
  records.
- Converted fork, rename, permission-rule, and plugin-reload handlers to pure
  intents and installed one `QueryEngine` executor for engine-owned
  runtime/session mutations.
- Added validated required/optional string and boolean payload accessors so a
  malformed intent fails before mutation.
- Added one losslessly delivered `EventCommandResult` with typed
  succeeded/failed/unsupported status, action, output, error, and follow-up
  prompt; TUI, plain, headless, and ACP now project the same outcome without
  reapplying it.
- Froze submit-time event identity so resume/fork result and terminal events
  close the source turn even when the engine activates another session for the
  next turn.
- Admitted command and model turns through the same Plan boundary. Rejected
  concurrent commands remain unsequenced caller feedback and cannot mutate or
  enter the reducer. `/plan` and mode commands publish the Plan transition,
  command result, and terminal through one contiguous replayable stream.
- Retained bounded typed command fields in runtime records so replay
  distinguishes succeeded, unsupported, and failed outcomes.

### Evidence

- exact-once tests cover dispatch/application, invalid typed payloads, fork
  creation and source identity, rename metadata, permission persistence, and
  deferred history rejection;
- owner-admission tests prove supported and hidden entrypoint-owned handlers
  are never called through the engine;
- a blocking model test proves concurrent `/clear` and `/plan` perform no
  mutation, enter no reducer event, and introduce no runtime sequence gap;
- Plan command replay starts with sequence one and reconstructs the transition,
  typed result, and terminal without redispatch;
- focused engine/command/TUI/plain/headless/ACP tests and focused race tests
  pass; and
- all repository Makefile gates, documentation/manifest validation, and diff
  hygiene pass.

### Adoption Decision And Next Gate

This slice is `combine`: it preserves the project-owned registry, QueryEngine,
Plan boundary, session/transcript services, and renderer contracts while
combining them behind one typed executor/event boundary. It changes no Eino
dependency, Compose Graph topology, kernel selection, or durable schema.
P16.4a can now converge the core session lifecycle. P16.4b still owns
crash-atomic fork create/metadata/switch compensation; P16.3 proves only one
live owner and one child creation.

## P16.4a1 Durable Core Session Lifecycle

**Completed:** 2026-07-20

### Problem

The engine rewrote the entire transcript at ordinary checkpoints. `/clear`
replaced the file with an empty state, `/compact` replaced it with only the
summary state, and an empty new session had no durable commit marker.
Consequently, audit history could be erased, a committed empty session was not
resumable, and restart behavior depended on the last rewrite rather than an
explicit lifecycle transition. ACP slash resume could also change the engine
identity without atomically remapping the ACP host's external session key.

### Resolution

- Added backward-compatible `session-start`, `reset-boundary`,
  `compact-boundary`, and exceptional `state-checkpoint` JSONL records.
  `LoadFull` now replays one active projection while retaining earlier records
  as audit history.
- Changed ordinary production checkpoints to append only newly durable
  messages. A failed write does not advance the durable cursor; a later safe
  point writes a full repair checkpoint, and an unrepaired final failure
  surfaces as `persistence_error`.
- Lifecycle commits sync both the JSONL file and its parent directory on first
  creation before the corresponding live mutation. Post-write sync failures
  are typed as indeterminate rather than misreported as definite rollback;
  incomplete JSONL suffixes are truncated before checkpoint repair, and an
  uncertain automatic compact commit is never retried as a second compact
  boundary.
- Added canonical `/new`: complete metadata and a durable empty-session marker
  commit before the engine activates a fresh identity through the same resume
  service. A pre-commit persistence failure leaves the source identity and
  context unchanged.
- Made `/clear` append one reset boundary before clearing live messages,
  replacement state, and file-state cache.
- Made manual and automatic compaction append exactly one compact boundary
  containing the complete active messages, replacements, and file state.
- Restored lifecycle-selected messages, metadata, replacement state, file
  state, query-kernel pin, execution context, and persisted Agent projections
  through one resume owner. Committed empty sessions are now resumable.
- Kept ACP protocol load/resume support, but excluded identity-changing slash
  `/new` and `/resume` from ACP registry admission until its external handle
  map has an atomic remap contract.

### Evidence

- transcript tests prove raw audit retention, lifecycle-selected active replay,
  auxiliary-state restoration, and malformed-record isolation;
- engine tests prove true-new source preservation, known pre-commit
  persistence-failure containment, transient incremental-write repair,
  unrepaired terminal failure, query-kernel re-evaluation and pinning,
  reset/compact restart determinism, one automatic compact record, and linear
  ordinary append;
- TUI tests prove a new identity rebinds the leader without deleting the source
  transcript;
- ACP tests prove identity-changing slash commands reject before mutation while
  protocol resume remains available;
- focused engine/session/transcript/command/TUI tests and focused race tests
  pass; and
- all repository Makefile gates, documentation/manifest validation, and diff
  hygiene pass.

### Adoption Decision And Next Gate

This slice is `combine`: it preserves the project-owned `QueryEngine`, session
catalog, metadata, query-kernel pin, entrypoint, and recovery contracts while
adapting append-only lifecycle records into the existing JSONL authority. It
changes no Eino dependency, Compose Graph topology, or model/tool execution
owner. P16.4a2 is ready to consolidate `/sessions`, direct resume, and the TUI
picker behind one session service. P16.4b remains blocked on that surface and
still owns crash-atomic fork creation, lineage, and switch compensation.

## P16.4a2 Consolidated Session Service

**Completed:** 2026-07-20
**Decision:** `combine`

### Problem

Session behavior still had multiple observable owners after the durable
lifecycle landed. Direct and startup resume used engine helpers, the
no-argument TUI `/resume` picker carried its own query/selection path, rename
wrote through a direct recorder helper, and TUI export rendered live UI
messages into a workspace file. `/history`, `/rename`, and `/export` also
remained separate command names without a canonical grouped surface. A catalog
could list the same CWD through multiple transcript roots, but an action that
accepted only a session ID could lose the selected source.

### Resolution

- added an engine-owned `SessionService` for bounded query, exact-source
  resume, rename, and durable Markdown export;
- added canonical `/sessions` list/search/resume/rename/export grammar with
  typed intents and retained `/history`, `/rename`, and `/export` only as hidden
  exact compatibility shortcuts through P16.7b;
- routed direct/startup resume and the TUI picker through the same facade. A
  selected row retains transcript directory/path, while a direct ID is resolved
  across registered same-CWD roots and duplicate IDs fail before mutation;
- removed the TUI-local export renderer. Export now flushes the active
  transcript, renders the persisted authority, fsyncs a same-directory temp
  file, and atomically replaces only a regular target. Windows uses
  replace-existing plus write-through; directories and symlinks fail closed;
- kept ACP slash `/sessions`, `/new`, and `/resume` unavailable before action
  because the protocol session map still has no atomic identity remap. Explicit
  ACP session protocol operations remain supported; and
- changed no Eino dependency, Compose Graph topology, query-kernel selection,
  append-only lifecycle schema, or fork behavior.

### Evidence

- command tests cover grammar, availability, hidden-shortcut equivalence, and
  ACP pre-action rejection;
- service and executor tests cover bounded search, exact-source
  list-to-rename/export/resume, duplicate-ID rejection, durable metadata,
  active persisted export, overwrite cleanup, directory/symlink refusal,
  replacement failure preservation, and cancellation before mutation;
- TUI/plain/headless tests prove service projection without a second exporter,
  and ACP tests prove slash rejection preserves identity and messages; and
- focused engine/command/TUI/ACP tests, focused race tests, all repository
  Makefile gates, documentation/manifest validation, and diff hygiene pass.

### Adoption Decision And Next Gate

This slice is `combine`: it preserves project-owned persistence, permission,
runtime-event, entrypoint, and TUI projection contracts while adapting a
grouped sessions command and one facade. It deliberately does not modify Eino:
session catalog, transcript durability, entrypoint identity, and workspace
file replacement are project-owned product contracts rather than model/Graph
orchestration. P16.4b is now ready and owns crash-atomic fork creation, lineage
metadata, child activation, and rollback.

## P16.4b Durable Fork Lifecycle

**Completed:** 2026-07-20
**Decision:** `combine`

### Problem

Fork had one live executor after P16.3 but no single persistence transaction.
The child file became visible before branch name and other metadata were
appended, current and picker forks used separate paths, and ACP created an
empty registered session before copying only in-memory messages. A persistence
or restore failure could therefore expose a partial child, lose execution
context across restart, or leave an external handle whose durable lineage did
not match its in-memory state.

### Resolution

- Extended the engine-owned `SessionService` with one create/activate boundary
  used by direct fork, TUI-selected fork, and ACP protocol fork.
- Bound one deterministic child ID to source locator plus operation ID. A
  retry reads the matching committed operation, while an existing target from
  another operation is never overwritten.
- Adapted the source execution checkpoint into complete child metadata:
  model/provider, permission mode, ProjectGraph kernel pin, Plan state, CWD,
  and additional directories survive. The Plan capability is rebound to the
  child-owned file identity and an in-flight approval normalizes to Active
  without callback authority. Child identity, timestamps, and lineage are new;
  pending callbacks, Agent runtime identities/revision, and worktree ownership
  are cleared.
- Added `Recorder.BranchWithState`, which writes active messages,
  replacements, file snapshots, parent/branch metadata, operation marker,
  branch name, and full child metadata into one unique same-directory temp
  file. File sync precedes a platform no-clobber install and parent-directory
  sync.
- Classified pre-install failure as definite no-child and post-install sync
  failure as durability-uncertain. The source transcript is only flushed and
  never rewritten.
- Activated TUI/plain/headless children only after durable creation using a
  cancellation-independent restore boundary. Restore failure compensates only
  a regular child whose operation marker, parent marker, and full
  session/parent lineage all match the create result.
- Kept ACP slash `/fork` unavailable before handler execution. Explicit ACP
  Fork serializes against source prompting, leaves the source handle
  unchanged, restores the durable child, and only then registers and starts
  the child handle/runtime.

### Evidence

- Transcript tests cover complete child composition, source byte preservation,
  no-clobber existing targets, pre-commit sync failure, post-commit directory
  uncertainty, and inspectable retry state.
- Session and service tests cover full metadata adaptation, copied replacement
  and file state, operation idempotency, conflicting-operation refusal,
  restart restore, stale live/durable boundary rejection, activation rollback,
  lineage-tamper refusal, and cancellation after commit.
- Command and TUI tests prove one child/switch and picker failure retention.
  ACP tests prove CWD rejection before registration, durable lineage and cold
  resume, unchanged source identity, slash rejection, and restore-failure
  transcript compensation before handle registration.
- Focused engine/session/transcript/TUI/ACP tests, focused race tests, Windows
  cross-compilation, independent persistence/concurrency review, repository
  Makefile gates, documentation/manifest validation, and diff hygiene pass.

### Adoption Decision And Non-Goals

This slice is `combine`: it preserves project-owned session, transcript,
query-kernel, permission, Plan, entrypoint, runtime-event, and TUI contracts
while adapting one durable lineage operation behind a shared service. It
changes no Eino dependency, Compose Graph topology, kernel selection rule,
public Eino schema, or lifecycle record kind. `/branch`, `/undo`, `/redo`,
`/rewrite`, `/rewind`, archive/delete, worktree ownership recovery, and an ACP
slash-handle remap remain separate gated work. Fork does not copy the source
session's external Plan Markdown file as a second persistence artifact; it
preserves Plan phase and return context in child metadata, rebinds the child to
its own Plan path, and leaves cross-file atomic artifact bundling to a separate
design.

## P18.H0 Process-Global Worktree Tool Containment

**Completed:** 2026-07-20
**Decision:** `combine`

### Problem

Default EnterWorktree and ExitWorktree shared package-global state and called
`os.Chdir`, so one model call could change filesystem context for every
concurrent QueryEngine, ACP session, and child. Removing them only from the
model-visible pool would not stop old transcript calls, standalone MCP
exposure, direct QueryEngine dispatch, or exported-constructor use.

### Resolution

- Removed both names from `RegisterDefaults`, the single registry source used
  by CLI/TUI/plain/headless, ACP create/resume, standalone MCP, leader, and
  child execution.
- Reserved both primary names and aliases in `Registry.Register`, preventing
  custom re-registration from recreating a model-visible or executable
  surface.
- Filtered caller-supplied reserved schemas from the final model-visible
  projection, including registry-free SDK configuration.
- Added one stable unavailable-name decision to canonical tool admission and
  the QueryEngine executor before JSON parsing, hooks, permissions,
  repeated-call admission, or tool execution.
- Replaced the old exported constructors with source-compatible unavailable
  stubs and deleted all Git commands, package-global worktree state, and
  `os.Chdir` behavior from that path.
- Preserved `Agent(isolation="worktree")`, which continues through the separate
  manager and explicit child CWD without changing the parent/process CWD.
- Updated the TUI renderer inventory and current tool count from 41 to 39.

### Evidence

- Registry tests prove default absence, primary/alias reserved-name refusal,
  registry-free projection containment, and retained Agent registration.
- Compatibility-stub tests prove explicit failure and unchanged process CWD.
- Engine tests replay both historical names with malformed input and prove
  unavailable settlement before the configured executor.
- Existing Agent worktree tests retain explicit child CWD, clean cleanup,
  changed-work retention, and error cleanup behavior.
- Focused tools/engine/TUI/ACP/MCP tests and the corresponding race suite pass;
  repository Makefile, documentation/manifest, and diff gates close the slice.

### Adoption Decision And Non-Goals

This slice is `combine`: it rejects unsafe leader-session process switching,
preserves source compatibility through non-executable stubs, and preserves the
existing Agent-isolation outcome for P18.0. It does not implement a durable
worktree service, change manager naming/cleanup/recovery, copy dirty source
state, add automatic integration, modify Plan Mode, or change Eino, Compose
Graph, or public Eino schemas.

## P18.0 Context-Aware Durable Worktree Lifecycle

**Completed:** 2026-07-20
**Decision:** `combine`

### Problem

The remaining Agent worktree manager held its mutex across uncancellable Git,
derived path and branch from a display name, reused an existing branch after a
failed create, stored ownership only in memory, and deleted its record before
cleanup. It could therefore collide across Agents, report no recoverable state
after timeout/process failure, remove an unrelated path/ref, or lose cleanup
retry authority. The runtime also had no structured worktree state projection.

### Resolution

- Added an engine-owned `engine/worktree.Service` under every QueryEngine,
  rooted at the stable project `.eino-agent/worktrees/v1` directory. The
  service is deliberately not bound to `AgentRunner` until P18.1.
- Added opaque worktree identity and a versioned durable record containing
  Agent/session/thread owner, canonical Git common-directory identity,
  repository root, managed path and branch, base commit, state, revision,
  bounded dirty-report slots, timestamps, and categorized error diagnostics.
- Persisted every record revision through a synced same-directory temp and
  atomic replace. Creating is durable before Git; cancellation, timeout,
  collision, Git, and validation failures become inspectable Failed records.
- Added context-aware explicit-directory Git operations under a bounded service
  deadline. Creation rejects existing path/ref and mismatched project/source
  common directories before mutation, then validates target root, common
  directory, HEAD, branch, and base before Ready.
- Added record-scoped operation leases and a short store commit lock. Different
  records reach Git concurrently; neither an AgentRunner nor registry mutex is
  involved.
- Added fail-closed non-force removal with fresh identity, ignored/untracked
  status, commit-count, and branch/base checks. CleanupFailed preserves retry
  metadata. If a commit races the final check and native Git removal, the
  service recreates the owned branch at the original path, verifies its
  identity and advanced HEAD, and records CleanupFailed/dirty.
- Added lossless `worktree_lifecycle` events and
  `RuntimeSnapshot.Worktrees`. A durable commit precedes reducer acceptance,
  malformed state/revision edges mutate nothing, and replay performs no Git,
  model, tool, or Agent dispatch.

### Evidence

- focused service tests cover path/ref collision before Git, distinct
  concurrent identity, cancellation, timeout, unrelated-record concurrency,
  dirty retention, cleanup failure/retry, restore failure metadata, atomic
  store/version handling, and event-sink ordering;
- real Git process tests cover create/remove and inject a commit inside the
  final-check/remove window, proving that the original path and advanced commit
  remain available;
- engine runtime tests cover construction, owner/session/thread lineage,
  ordered Ready/Removed projection, revision-gap rejection without partial
  mutation, and deterministic side-effect-free replay;
- focused race tests, Windows/Linux cross-compilation, and an independent
  persistence/concurrency review pass; and
- repository Makefile gates, documentation/manifest validation, and final diff
  hygiene pass.

### Adoption Decision And Next Gate

This slice is `combine`: it adapts proven owner binding, cancellation, dirty
retention, explicit service, and durable lifecycle patterns behind a
project-owned Go boundary. It does not change Eino, Compose Graph topology,
query-kernel selection, Plan Mode, or public Eino schemas because Git
ownership, crash records, cleanup, and runtime projection are product
side-effect contracts rather than model-round orchestration. P18.1 is now ready
to reject ambiguous `cwd + isolation`, bind Ready metadata to child launch,
populate source/result dirty reports, and route terminal cleanup/handoff
through this service. P18.2 subsequently closed restart discovery and fork
authority stripping while deferring policy-less automatic orphan pruning.

## P18.1 Agent Binding And Explicit Handoff

**Completed:** 2026-07-20
**Decision:** `combine`

### Problem

P18.0 had established a durable lifecycle owner, but the production Agent path
still used a second process-local manager. Dirty source changes were silently
omitted, retained results carried only path/branch, filesystem tools and Bash
could fall back to process CWD, project skills and permission roots were not
worktree-scoped, and persistent shells or transcripts could outlive/dirty the
wrong path.

### Resolution

- Replaced the AgentRunner manager path with a narrow binding to its parent
  QueryEngine's `engine/worktree.Service` and effective CWD. Launch reservations
  keep concurrency exact without holding the runner mutex across Git.
- Rejected unsupported isolation/source modes and simultaneous worktree plus
  explicit CWD before side effects. Dirty source defaults to rejection;
  explicit `ignore_dirty` uses committed HEAD and durably discloses bounded
  omitted paths.
- Bound Ready record identity/path/branch/base and lineage to runtime and
  durable Agent metadata before model entry. Clean terminal state removes once;
  dirty, ignored, untracked, ahead, cancelled, unknown, or cleanup-failed state
  retains a bounded changed-file/patch handoff.
- Added an engine-owned execution-CWD context across Read, Write, Edit, Glob,
  Grep, LSP, Brief attachments, NotebookEdit, and Bash. Persistent shells are
  per QueryEngine, start in the child CWD, and close before worktree cleanup;
  no process `chdir` is used.
- Reloaded project-source skills and permission rules/root from the worktree,
  while preserving runtime/user skills. Durable memory keeps the stable project
  root, and Agent transcripts stay in runner storage outside the ephemeral
  worktree.
- Made AgentRunner pre-store the first child turn and taught the child
  QueryEngine to consume that boundary once, preserving launch-before-model
  durability without duplicating the user prompt.
- Reconciled out-of-band service event sequence with shared child runtime state
  and projected the same typed handoff through foreground results, background
  notifications, TaskOutput, and persisted Agent snapshots.

### Evidence

- real Git integration proves dirty-source rejection before model entry,
  explicit committed-HEAD ignore, durable omitted-file disclosure, service
  binding, clean removal, and stable parent/process CWD;
- focused service/runner/tool/skill/runtime tests cover dirty and ahead
  handoff, bounded patch disclosure, ambiguous admission, background binding,
  every relative/default filesystem surface, scoped shell teardown, worktree
  skill generation, and launch transcript single-write semantics;
- focused race tests cover worktree service, Agent runner, runtime sequence,
  and scoped-shell boundaries; and
- repository Makefile, documentation, manifest, and diff gates close the
  iteration.

### Adoption Decision And Next Gate

This slice is `combine`: lifecycle state and Git side effects remain
project-owned rather than Eino-owned, while child execution uses explicit
context instead of process mutation. It changed no Eino dependency, Compose
Graph topology, Plan state, query-kernel selection, or public Eino schema.
P18.2 subsequently closed restart discovery, fork authority stripping,
interrupted-state classification, continuation admission, and explicit cleanup
retry. Automatic orphan pruning remains deferred pending an accepted retention
policy.

## P18.2 Restart Recovery And Cleanup Ownership

**Completed:** 2026-07-20
**Decision:** `combine`

### Problem

P18.1 persisted complete worktree and Agent handoff records, but a fresh
QueryEngine could not enumerate them. Runtime replay required a synthetic
Creating-first event, stopped worktree Agents always rejected continuation,
and no public QueryEngine entrypoint could reconstruct cleanup ownership after
restart. Path-only recovery would have let forks or stale records mutate Git.

### Resolution

- Added bounded deterministic store discovery for regular versioned JSON
  records. Malformed data, unknown versions, symlinks, invalid IDs, and
  filename/record identity mismatch remain diagnostics instead of lifecycle
  authority.
- Added metadata-only service discovery and runtime rehydration. Startup and
  session resume project inspect-only, recovery-pending, terminal, or
  unavailable dispositions without synthetic events, thread sequences, Git,
  model, tool, or Agent dispatch.
- Added explicit continuation admission. AgentRunner reconstructs the complete
  durable owner, proves the current executor's direct parent session did not
  change, and asks the engine service to revalidate repository common
  directory, managed path, branch, status, and branch HEAD before executor
  entry. The lifecycle call runs outside runner locks and an executor-generation
  check rejects raced fork/rebind ownership.
- Registered only Agent IDs named by the selected source session as durable
  evicted references. Existing fork metadata already clears Agent IDs and
  worktree path/branch, so a fork cannot continue the source worktree.
- Added `QueryEngine.RetryAgentWorktreeCleanup`, which resolves immutable owner
  fields from durable Agent metadata instead of accepting them from callers.
  Interrupted Removing becomes CleanupFailed diagnostic state; removal repeats
  identity and clean checks at the commit boundary.
- Kept automatic age-based orphan pruning out of the implementation because no
  retention age, unattended-deletion policy, or user-facing authority contract
  is accepted.

### Evidence

- store/service tests cover corrupt, unknown-version, symlink, missing,
  owner/fork mismatch, branch/common-directory mismatch, dirty, unknown status,
  interrupted Creating/Removing, clean removal, and concurrent duplicate retry;
- AgentRunner tests prove same-process and fresh-runner worktree continuation,
  exact recovered CWD, and fork-session rejection;
- runtime/engine tests prove startup rehydration is idempotent and creates no
  synthetic thread or event sequence;
- focused worktree/recovery race tests and repository Makefile,
  documentation, manifest, and diff gates close the iteration.

### Adoption Decision And Next Gate

This slice is `combine`: project-owned records, owner identity, Git admission,
and cleanup remain outside Eino, while the next foreground child execution
slice may now move its model-round control flow behind the public-Eino
ProjectGraph. P18.2 changed no Eino dependency, Graph topology, Plan state,
query-kernel selector, or public Eino schema. P13.9a-b subsequently completed
foreground and background child traversal without moving worktree ownership.

## P16.5d Extensibility And Orchestration Inspection

**Completed:** 2026-07-20
**Decision:** `combine`

### Problem

The command surface previously inspected several different owners: `/tasks`
could fall back to a package-global or disconnected task manager, `/agents`
used a package-global function hook, `/hooks` re-read configuration rather than
the executor generation, `/mcp` mixed inventory with mutation methods that
could not update model-visible tools atomically, and malformed dynamic sources
could disappear behind first-error or partial-reload behavior. Plugin command
replacement swapped command records but did not expose one revisioned source
and diagnostic generation.

### Resolution

- added `QueryEngine.RuntimeInspectionSnapshot` as the single command-facing
  aggregation point for detached task/Agent, skill, MCP, hook, and plugin read
  models; removed package-global and command-local inspection fallbacks;
- made `tools.TaskManager` an explicit root-QueryEngine-lineage dependency for
  Task tools, lifecycle draining, TUI controls, child sharing, and inspection;
  independent top-level engines no longer share local task truth;
- made `/tasks` read-only, made MCP add/remove/restart explicitly unavailable
  before side effects, and scoped `/agent` to the TUI thread picker;
- retained skill source and malformed-file diagnostics, cloned the exact
  executor shell-hook generation, and added a revisioned deterministic MCP
  inventory including failed-server health categories and deep-cloned schemas;
- changed plugin reload to validate the complete configured source set,
  aggregate every invalid source/command, qualify names and aliases, and build
  a functional digest without publishing a partial candidate; and
- changed the command registry to commit the dynamic command map/order and
  revision/digest/source/diagnostic metadata under one lock. Built-in, name,
  and alias collisions reject the candidate while the prior generation remains
  live.

### Evidence

- focused command tests prove all inspection handlers use only the engine read
  model and mutation-shaped task/MCP arguments plus non-TUI `/agent` cause zero
  side effects;
- two-engine and race tests prove task create/list/stop/inspection use the
  injected lineage manager and never expose another root engine's local tasks;
  a real child TaskCreate round proves one destructive lifecycle drain reduces
  into the lineage-shared runtime store and is visible from the root projection
  exactly once;
- plugin tests prove aggregate diagnostics, deterministic source precedence,
  qualified aliases, immutable metadata, collision rejection, and retained
  prior generations;
- skill, hook, and MCP tests prove source/health projection, malformed-source
  diagnostics, deep snapshot isolation, deterministic ordering, and revision
  behavior;
- focused race tests cover the runtime and generation boundaries; and
- repository Makefile, documentation, manifest, and diff gates close the
  implementation.

### Adoption Decision And Remaining Gates

This slice is `combine`: it preserves the useful inspection vocabulary, adapts
it to project-owned runtime snapshots, and uses an atomic generation contract
for dynamic prompt commands. It does not modify Eino or Eino-ext because these
are project command/runtime ownership and side-effect contracts, not missing
Compose primitives. Plugin install/trust and resolved-path containment remain
gated, plugin skill/hook/MCP contributions remain disconnected, and MCP
mutation remains blocked until persisted configuration, manager inventory,
runtime/model-visible tool generations, and rollback form one transaction.
With the command-owner prerequisite closed, P13.7 is ready to replace
`queue.Manager` with one project input coordinator around the existing public
Eino Compose Graph.

## P13.7 Project Input Coordinator Cutover

**Completed:** 2026-07-20
**Decision:** `project-native`

### Problem

Live user input, steering, child messages, Agent terminal notifications,
asynchronous hook rewakes, and stop controls previously crossed several
process-local owners. The production `queue.Manager` persisted only part of
that surface, child and notification polling removed records before durable
acceptance, the TUI owned idle rewake behavior, and ACP maintained a separate
hidden hook-rewake loop. A Graph migration on top of those paths would have
created multiple delivery truths and ambiguous replay.

### Resolution

- added one versioned session-scoped `RuntimeInputCoordinator` and typed
  `RuntimeItem` union for user prompts with images, steering, child messages,
  Agent notifications, async hook rewakes, and graceful or immediate stop;
- persisted enqueue, claim, release, cancellation, and settlement through an
  atomic `0600` ledger. Recovery returns processing items to pending, removes
  transcript-delivered items, drops stale stop controls, and fails closed on
  corrupt or unsupported state;
- made explicit priority followed by FIFO the only schedule. Metadata no
  longer overrides priority, and Graph local state contains only the plain
  coordinator revision while the live owner stays in invocation context;
- inserted the same coordinator collection and claim boundaries into the
  canonical preparation and after-tool policies used by Legacy and
  ProjectGraph. Tool results remain visible before newly claimed input;
- changed child message and Agent notification transfer to peek, durable batch
  accept, then explicit acknowledgment with stable delivery IDs;
- made transcript persistence the delivery-settlement boundary, including
  idle-claimed TUI turns and restart deduplication;
- made the TUI subscribe and start eligible idle items as fresh turns, hydrate
  previews from durable truth, and record immediate Ctrl+C stop before local
  cancellation. ACP and plain callers do not synthesize prompts; they absorb
  pending input only at their next inbound boundary; and
- deleted the obsolete production `queue.Manager`, its persistence and
  fixtures, all query-parameter wiring, and the ACP hidden rewake loop.

### Evidence

- coordinator tests cover priority/FIFO, bounded concurrent enqueue,
  processing recovery, transcript deduplication, stale stops, ownership,
  image codec, corrupt-ledger failure, idle claim, lifecycle settlement, and
  restart no-replay;
- Legacy and ProjectGraph canonical traces assert identical safe-point order,
  scope filtering, notification delivery, progress, and
  tool-result-before-attachment behavior;
- AgentRunner tests prove messages and notifications remain readable until
  explicit acknowledgment; async-hook tests prove exit-code-2 rewakes cannot
  bypass coordinator persistence;
- TUI tests prove durable preview hydration and real idle scheduling; ACP tests
  prove rewakes wait for inbound prompts and repeated cancellation remains
  immediate and race-safe; and
- repository Makefile, race, documentation, manifest, and diff gates close the
  implementation.

### Adoption Decision And Next Gate

This slice is `project-native`: public Eino `compose.Graph` remains the generic
typed orchestration mechanism, while input durability, transport legality,
stop semantics, transcript identity, and replay stay under one project-owned
boundary. It does not modify Eino or Eino-ext because neither library can own
this application's session ledger or transport-specific side-effect contract.
P13.8 is the next Graph slice: durable interrupt/resume must reconstruct the
exact invocation and re-evaluate current policy, never trust a stale approval
result.

## P13.8 Durable Compose Graph HITL

**Completed:** 2026-07-20
**Decision:** `project-native`

### Problem

ProjectGraph could traverse the canonical model/tool loop but an unresolved
permission, question, or Plan approval still depended on a process-local
callback. A restart lost the live owner, and treating a persisted answer as
authority would allow a changed tool invocation, schema, scope, or policy to
reuse stale approval.

### Resolution

- compiled the shared Graph with Eino's public checkpoint store and represented
  unresolved interactions through `compose.StatefulInterrupt`;
- persisted opaque Compose state plus exact arguments in one atomic `0600`
  sidecar while Session metadata stores only sanitized stable identities;
- added a typed durable permission-decision `RuntimeItem` and targeted
  `ResumeWithData` path that does not append a synthetic user turn or replay
  the model request;
- revalidated live tool selection, schema, Plan containment, rules, grants,
  mode, scope, hooks, and execution prerequisites before dispatch. All
  required decisions in a committed round are collected before any tool
  starts, and policy/schema drift expires the old intent;
- restored cold Graph attention through the canonical Session/runtime reducer,
  with TUI and ACP mapping existing structured responses back to the exact
  durable interrupt; and
- deleted a completed sidecar only after transcript and Session commit.
  Corrupt, unsupported, ownerless, cross-scope, or metadata-mismatched state
  fails closed.

### Evidence

Focused engine fixtures cover same-process and cold restart resume,
model/tool exactly-once counters for normal replay, multi-tool preflight,
question input replacement, Plan commit, policy/schema drift, file protection,
checkpoint corruption/version/scope, and sidecar deletion. TUI proves a cold
Graph attention response creates the targeted durable decision; ACP proves one
Prompt can drive interrupt, protocol permission, resume, and final completion.
The full engine/TUI/ACP suites preserve Legacy behavior.

### Adoption Decision And Next Gate

The slice uses Eino for generic Graph traversal, checkpoint callbacks, and
interrupt/resume mechanics. It does not modify Eino or Eino-ext: durable
Session identity, permission authority, transport projection, sanitization,
and side-effect ordering are application contracts and remain project owned.
P18.2 subsequently closed durable worktree ownership, and P13.9a-b later moved
foreground and background child traversal behind the same ProjectGraph.

## P13.9a Foreground Child ProjectGraph Kernel

**Completed:** 2026-07-20
**Decision:** `project-native`

### Problem

Foreground `Agent` execution still constructed an ordinary child
`QueryEngine`, so its traversal depended on the process canary instead of the
accepted ProjectGraph migration sequence. Promoting it without a pre-executor
kernel pin could let a crash recover the child through Legacy, while moving
permissions, worktrees, or terminal state into Compose would create duplicate
owners.

### Resolution

- added a process-local foreground marker at `RunAgent`; background launch
  retains the existing ordinary canary path and durable Agent JSON does not
  gain a kernel selector;
- added a no-clobber pre-executor admission after AgentRunner assigns exact
  child identity and worktree CWD. It commits the initial message seed,
  `project_graph/v1/foreground_child`, complete Session/Thread/Agent and parent
  lineage including tool-use causation, and an fsynced `session-start`;
- made existing-pin admission compare the complete durable lineage and CWD.
  Changed ownership or concurrent same-Session creation fails before the
  executor/model entrypoint and never replaces the winning transcript;
- constructed the foreground child through the existing public-Eino
  ProjectGraph while leaving AgentRunner generation, worktree, cancellation,
  later transcript checkpoints, and terminal ownership unchanged;
- kept foreground permission/question attention on the reachable project
  coordinator rather than persisting an unpresentable hidden-child HITL
  checkpoint. Ordinary ProjectGraph Sessions retain durable HITL; and
- translated Compose-wrapped parent cancellation back to the existing
  `aborted_streaming` or `aborted_tools` terminal without Legacy replay.

### Evidence

- a real foreground tool round proves two model calls, one mutating tool
  effect, one coordinator-owned permission, exact child/parent/tool lineage,
  and one terminal generation;
- restart fixtures prove the foreground kernel pin wins over a default-off
  process canary, while a message-only child transcript and missing Agent
  identity fail before model entry;
- concurrent launch proves exactly one same-Session admission wins and its
  metadata/message seed cannot be overwritten; parent-lineage and CWD drift
  fail closed without a model call;
- parent cancellation, foreground worktree binding/cleanup, background
  non-promotion, Agent transcript checkpoint preservation, and targeted race
  suites retain the existing owners;
- ACP cancellation stops transport writes after the first delivery error,
  locally cancels any later Graph permission attention, and drains the engine
  producer before `Agent.Close` can race its final durable commit; and
- repository Makefile, documentation, manifest, and diff gates close the
  implementation.

### Adoption Decision And Remaining Gate

This slice uses the existing public Eino `compose.Graph` only for generic
foreground traversal. Session identity, lineage, no-clobber admission,
worktree recovery, coordinator attention, cancellation translation,
transcript durability, and terminal ownership remain project contracts, so no
Eino or Eino-ext source changed.

The existing cross-file crash window between child Session admission and the
separate AgentRunner JSON commit is intentionally not converted into a new
transaction here. At that point no model/tool side effect exists, the Session
is already pinned and cannot fall back to Legacy, and P18.2 retains
restart-discoverable inspect-only worktree evidence. P13.9c must converge that
orphan admission with durable Agent replay/cleanup. P13.9b subsequently moved
background supervision without changing that durable replay boundary.

## P13.9b Background Child ProjectGraph Supervision

**Completed:** 2026-07-21
**Decision:** `project-native`

### Problem

Asynchronous `Agent` children still constructed ordinary child `QueryEngine`
instances. Promoting them through the process canary would have changed root
and historical Sessions, while moving generation, cancellation, join, steering,
or terminal state into a Graph node would have created a second lifecycle
owner.

### Resolution

- added a process-local background execution marker distinct from foreground
  and the zero-value unknown mode; retained and evicted async resume restore the
  marker only for admission, never as durable kernel authority;
- extended no-clobber child admission so a new background Session commits the
  initial message seed, exact child/parent/tool lineage, worktree CWD,
  `project_graph/v1/background_child`, and `session-start` before executor/model
  entry;
- preserved every valid identity-bearing Session selection on async
  continuation. Historical Legacy children remain Legacy, while a foreground
  child resumed through `SendMessage` retains `foreground_child`; message-only
  transcripts fail admission unchanged because Agent/lineage/CWD ownership
  cannot be proven;
- admitted the internal background stage only from persisted metadata. The
  process canary cannot select it, and internal child Graph runs keep
  coordinator-owned permission/question attention instead of an unreachable
  durable-HITL checkpoint; and
- left `AgentRunner` as the sole generation, parent-turn detachment, targeted
  abort, pause/resume, worktree, terminal persistence, join accounting, and
  engine-owned/shared-runner lifecycle owner.

### Evidence

- a real mutating background ProjectGraph round produced two model calls, one
  tool effect, one coordinator-owned permission request, one terminal, and a
  durable `background_child` pin;
- restart and continuation fixtures proved default-off does not override the
  background pin, foreground async resume preserves `foreground_child`, and a
  pre-P13.9b Legacy Session does not switch kernels; unattributed message-only
  history fails before model entry or transcript rewrite;
- the production Agent tool survived parent-turn cancellation, while targeted
  abort cancelled only the child and completed the same generation;
- existing Graph-backed pause/resume and abort-at-pause tests remained exact;
- an engine-owned runner Close cancelled and joined its background Graph child,
  while closing an engine with an injected runner left the outer-owned
  generation running;
- a cancellation-ignoring executor proved a shutdown deadline ends only the
  join attempt: the child remained non-terminal until eventual executor return,
  which then released join accounting and settled aborted once; and
- focused tests plus repeated race runs closed the lifecycle matrix before the
  repository gates.

### Adoption Decision And Remaining Gate

Public Eino `compose.Graph` owns generic child ReAct traversal. No Eino or
Eino-ext source changed: kernel admission, Session compatibility, permission
authority, cancellation, join, steering, worktree, transcript, and terminal
semantics are Eino-Agent product contracts and remain project-owned.

P13.9b did not add detach UX, terminal replay, a durable completion cursor, or
new TUI controls. P13.9c subsequently closed terminal and orphan replay;
P13.9d then closed current projection parity without adding those controls.

## P13.9c Durable Child Terminal Replay

**Completed:** 2026-07-21
**Decision:** `project-native`

### Problem

P10 could rebuild an Agent named by the selected parent Session, but P13 child
durability had two unclosed ownership questions. Live restore trusted only an
in-memory `running` status instead of proving the same durable generation and
scope. A process could also stop after the child Session and ProjectGraph pin
were committed but before Agent JSON and the parent's `AgentIDs` checkpoint,
leaving valid no-model/no-tool admission evidence undiscoverable.

### Resolution

- made `AgentRunner` expose only its runner-owned transcript directory and a
  read-only execution-generation projection; Agent JSON remains the sole
  continuation registration owner;
- changed Session restore to load durable Agent state before attachment.
  `live_attach` now requires exact Agent/Session/thread identity, parent
  lineage/tool causation, CWD/isolation/worktree/transcript scope, positive
  generation, running durable state, and the current callback-owning runner;
- retained completed, failed, and aborted status/generation exactly and kept
  P10's cold-running normalization to aborted. A matching runtime generation
  is not reinstalled, so repeated resume does not duplicate or replace the
  terminal thread projection;
- scanned only the newest bounded regular-file set under the runner-owned child
  transcript store and followed exact Session/thread/Agent lineage from the selected
  root, including reachable nested children omitted from the parent
  checkpoint; and
- classified a reachable ProjectGraph Session with missing Agent JSON as
  `aborted + project_graph_orphan`. It is projected from immutable transcript
  evidence, never placed in AgentRunner's continuation registry, and never
  calls executor, model, tool, queue, permission, message, abort, or cleanup
  control.

Corrupt or partially present Agent metadata is not treated as the crash window.
Identity conflict, duplicate child Agent identity, unsupported admission
generation, partial transcript corruption, symlink, and non-regular evidence
fail closed with deterministic warnings. The additive
`agent_generation` Session field records generation-1 admission for new child
Sessions; older valid admissions safely infer generation 1 only for the
missing-Agent-JSON window because later continuation already requires Agent
JSON.

### Evidence

- `TestProjectGraphTerminalGenerationsRestoreOnceWithoutDispatch` covers
  completed, failed, and aborted foreground/background generations, exact
  status/generation/lineage, zero executor calls, and repeated-resume
  idempotence;
- `TestProjectGraphSessionOnlyOrphanConvergesToInertReplay` reproduces the
  Session-commit/Agent-JSON crash window and proves one replay-only aborted
  projection, inert continuation control, zero dispatch, and byte-identical
  transcript evidence;
- `TestProjectGraphLiveAttachRejectsDurableGenerationMismatch` proves a current
  runner cannot attach when durable generation differs;
- `TestProjectGraphPartialAgentMetadataDoesNotBecomeOrphan` proves an existing
  but incomplete Agent JSON record fails closed instead of being reclassified
  as the missing-record crash window;
- `TestProjectGraphOrphanDiscoveryRejectsNonRegularAndConflictingLineage`
  proves symlink containment and parent-lineage rejection;
- `TestProjectGraphDuplicateAgentIdentityRejectsEveryCandidate` proves no
  dictionary-order winner is selected from conflicting durable identity;
- `TestProjectGraphNegativeAdmissionGenerationFailsClosed` proves only legacy
  zero or explicit generation 1 can enter the missing-Agent-JSON path;
- `TestProjectGraphBoundedDiscoveryKeepsNewestTranscript` proves the bounded
  scan retains the newest crash-window evidence and reports truncation; and
- existing live-attach, cold-running replay, process-restart lineage,
  evicted-continuation, foreground/background lifecycle, and worktree recovery
  suites remain the compatibility baseline.

### Adoption Decision And Remaining Gate

The public Eino `compose.Graph` remains the child ReAct traversal owner.
Durable Agent metadata, Session lineage, runtime projection, controls, and
worktree recovery are product contracts and therefore stay outside Eino.
P13.9c changed no Eino/Eino-ext source or dependency.

This slice did not add a child picker, switch presentation state, terminal
delivery cursor, detach transition, auto-wake, or automatic worktree cleanup.
P13.9d subsequently proved the existing child detail, lineage, transcript,
attention, attach-mode, and switching projections across live, terminal,
orphan, and evicted states.

## P13.9d Current Child TUI Parity

**Completed:** 2026-07-21
**Decision:** `project-native`

### Problem

P13.9c made child generations and the Session-only crash window durable, but
the promotion gate still relied on generic TUI fixtures. There was no direct
proof that real foreground/background ProjectGraph restore selected the same
live, replay-only, and evicted-transcript modes or that existing detail,
lineage, output, transcript, and attention views remained projection-only.
Thread switching also denied an active Plan approval as a side effect of
leaving its owner, contradicting the accepted rule that navigation cannot
mutate child runtime truth.

### Resolution

- kept `ThreadCatalogSnapshot`, `AgentDetailSnapshot`,
  `ThreadAttentionSnapshots`, and `AgentParentTraceSnapshots` as the existing
  bounded engine-owned selectors; no new child store or TUI runtime owner was
  added;
- changed owner-thread switching to detach the modal sender before closing the
  empty Bubble Tea response channel. A response already submitted remains
  deliverable exactly once; otherwise the canonical permission, question,
  repeated-tool, or Plan request stays queued and unsuppressed, receives no
  implicit response, and is re-presented after returning to its exact owner;
- preserved capture-before-activation for chat, draft, cursor, scroll,
  selection, search, queue preview, and detail-tab presentation; and
- proved cold foreground restart, retained live background attach, replay-only
  terminal state, runtime-ring eviction to durable transcript, and inert
  `project_graph_orphan` through the real P13.9c Session/Agent restore path.

The child event stream remains internal to `SubAgentExecutor`; Bubble Tea reads
the shared reducer selectors rather than consuming background child stream
frames as leader output. Navigation therefore needs no new event router.

### Evidence

- `TestP139dProjectGraphRestartProjectsReplayAndEvictedViewsWithoutDispatch`
  proves restart attachment, transcript/output/lineage detail, leader
  presentation restoration, runtime-ring eviction, stable reducer truth, and
  zero executor dispatch;
- `TestP139dProjectGraphBackgroundLiveAttachIsProjectionOnly` proves the exact
  current generation attaches live without a second executor call or reducer
  mutation;
- `TestP139dProjectGraphOrphanViewAndControlsRemainInert` proves Session-only
  orphan inspection and a failed control attempt remain dispatch-free and
  runtime-inert;
- `TestThreadViewSwitchSuspendsPlanPresentationWithoutResolvingRuntime` and
  `TestThreadViewSwitchSuspendsPermissionAndQuestionPresentation` prove modal
  suspension, empty-waiter closure, request retention, owner-only
  re-presentation, and one explicit final response;
- `TestThreadAttentionSubmittedResponseSurvivesPresentationSwitch` and
  `TestThreadAttentionResponseForwarderUsesProgramAfterPresentationSwitch`
  prove that response submission is the non-cancellable linearization point,
  including the real Bubble Tea `Program.Send` path;
- `TestThreadAttentionLateOwnerResponsePreservesNewOwnerDialog` and
  `TestThreadAttentionLatePlanResponseUsesFrozenOwnerData` prove a late
  settlement cannot remove the new owner's same-kind modal or read its
  structured Plan data;
- `TestP138ColdProjectGraphAttentionEnqueuesTargetedResume` proves
  ProjectGraph attention survives owner switching, re-presents only on the
  owner, mutates no reducer revision, dispatches no tool, and enqueues one
  targeted settlement; and
- focused internal/TUI tests plus repeated race runs cover the selector,
  switching, restart, live background attachment, and response-handoff
  boundaries. Background completion itself remains engine-owned P13.9b/P13.9c
  evidence rather than a new TUI lifecycle.

### Adoption Decision And Boundary

This slice is `project-native`: public Eino `compose.Graph` continues to own
child ReAct traversal, while runtime selectors, Bubble Tea presentation,
permission settlement, Session/Agent durability, and attach-mode semantics are
Eino-Agent product contracts. No Eino/Eino-ext source, dependency, checkpoint,
transcript schema, or Graph topology changed.

The slice added no dashboard, peek, detach, terminal-delivery cursor, lazy
transcript pagination, cross-thread permission shortcut, or child lifecycle
control. Current execution order and the remaining P13.10 deletion gate stay
owned by [`migration/PLAN.md`](../../PLAN.md).

## P13.10a ProjectGraph Default Cutover And Legacy Owner Deletion

**Completed:** 2026-07-21
**Decision:** `project-native`

### Problem

P13.9d left two complete production traversal owners. Direct `Query`,
default-off/ineligible new Sessions, and historical Sessions could still enter
the imperative `queryLoop`, while ProjectGraph owned promoted cohorts and
children. That duality kept two control-flow implementations, an environment
selector, fallback-shaped tests, and contradictory current documentation.
Silently moving an existing `legacy/v1` or unpinned transcript to Graph would
also violate the durable kernel pin and risk replaying history under a different
owner.

### Resolution

- made the shared compiled ProjectGraph the production kernel for direct
  `Query` and every new root Session;
- introduced the unrestricted durable root stage `full`, including local and
  MCP tool surfaces, while preserving older supported ProjectGraph stage pins;
- deleted `queryLoop`, `legacyQueryKernel`, `queryKernelLegacy`, the process
  canary environment flag, production Legacy selection, and Legacy-vs-Graph
  comparison fixtures;
- retained `legacy/v1` only as a diagnostic durable identity. Legacy, unpinned,
  unknown, and invalid-stage Sessions fail continuation and child admission
  before model/tool work, live Session mutation, or transcript rewrite;
- moved selected-source fork validation ahead of `BranchSession`, so a rejected
  Legacy, unpinned, unknown, or invalid-stage source remains byte-identical and
  cannot leave an unusable durable child artifact;
- kept retry/fallback in `runCanonicalModelRound`: it was already shared by
  ProjectGraph and is the single canonical provider-attempt owner, not a
  superseded Legacy executor; and
- allowed invocation-local Compose checkpointing only for a transcript-repair
  turn with durable HITL disabled. A permission-capable Session still requires
  the protected durable checkpoint sidecar and fails closed if it is
  unavailable; and
- preserved the distinction between absent and explicit `UpdatedInput` across
  durable permission decisions, so an ordinary allow resumes with the original
  tool arguments while structured question edits still replace them.

### Evidence

- `TestCanonicalProjectGraphQueryTrace` and
  `TestP1310ProductionProjectGraphMatchesCompiledGraphFixtures` preserve all
  canonical categorized goldens through the production Graph;
- `TestP1310NewSessionPinsAndPersistsFullProjectGraph`,
  `TestP1310FullProjectGraphExecutesExternalToolExactlyOnce`, and
  `TestP1310ACPNewSessionUsesFullProjectGraph` prove default root and ACP
  selection, complete tool-surface admission, exact execution count, and
  durable `project_graph/v1/full`;
- retired/unsupported construction and resume fixtures prove zero model calls,
  zero tool side effects, unchanged current Session identity, and byte-identical
  source transcripts;
- selected-source fork fixtures prove every unsupported durable identity is
  rejected before child persistence while preserving the source bytes;
- background-child admission fixtures prove historical Legacy continuation
  cannot create a new generation or rewrite its transcript;
- existing transcript-repair and durable-HITL suites keep their distinct
  persistence contracts;
- engine and ACP permission-resume fixtures prove original non-empty tool input
  survives the durable round trip and executes exactly once;
- permission-result fixtures prove absent `UpdatedInput` remains absent,
  durable nested input is deeply frozen, and non-durable mutable input is
  denied before it can enter the resume path;
- ACP load and TUI picker fixtures reject unpinned, retired Legacy, unknown,
  and invalid-stage Sessions without activation, model calls, Session remap,
  source transcript rewrite, or loss of the active picker; and
- repository search finds no production or test definition/reference of
  `queryLoop`, `legacyQueryKernel`, `queryKernelLegacy`, or the former process
  canary environment variable.

### Adoption Decision And Boundary

This slice is `project-native`: public Eino `compose.Graph` supplies traversal,
checkpoint, interrupt, and resume mechanics; Eino-Agent retains canonical
provider, recovery, tool, event, permission, Session, transcript, and TUI
contracts. No Eino/Eino-ext source or dependency changed.

At the P13.10a closeout, P13 remained open for P13.10b. That final slice was
reserved to remove retained rollout-era active-code vocabulary and test-only
construction plumbing, run the complete
entrypoint/race/PTY/golden/performance/checkpoint matrix, and record a
transcript-safe rollback drill against the pre-cutover binary.

## P13.10b ProjectGraph Rollout-Adapter Retirement And Final Proof

**Completed:** 2026-07-21
**Decision:** `project-native`

### Problem

P13.10a removed the Legacy executor but intentionally left one PR-sized
hardening boundary. Production and tests still used canary-era type, field,
function, file, and fixture names; a package-private constructor could still
force a root stage that production could not select. Current documentation
also retained rollout claims after ProjectGraph had become the only execution
owner. Finally, cutover needed an explicit proof that rolling back the binary
would reject the new `full` stage without rewriting its transcript.

### Resolution

- renamed the internal selection owner to `query_kernel_selection.go`, replaced
  active rollout identifiers with neutral `queryKernelStage` vocabulary, and
  removed the package-private root-stage override;
- made fresh root construction and `/new` call the same unconditional
  `project_graph/v1/full` selector, while foreground child construction retains
  its exact process-local admission marker;
- renamed the Go metadata field to `QueryKernelStage` while preserving the
  historical `query_kernel_canary_stage` JSON key. Existing `no_tools`,
  `read_only`, `local_tools`, `foreground_child`, and `background_child`
  transcripts therefore retain their pinned semantics and round-trip;
- kept `off`, retired Legacy, unpinned, invalid, and unknown selections
  fail-closed with no executor or fallback;
- moved ProjectGraph/HITL tests onto the production `full` construction path;
  only persisted-stage compatibility tests seed older metadata; and
- synchronized current entrypoint, code-map, query-engine, recovery, status,
  plan, gap, and history owners. Historical rollout evidence remains explicitly
  time-scoped.

### Proof Matrix

- direct `Query`, `QueryEngine`, ACP, TUI, plain, headless, foreground child,
  background child, resume, `/new`, and fork focused fixtures pass three
  consecutive runs;
- canonical ProjectGraph traces, categorized engine goldens, and provider
  stress goldens pass unchanged;
- ProjectGraph checkpoint-envelope/version rejection, required durable HITL,
  transcript encode repair, compact-boundary repair, initial checkpoint repair,
  and missing-tool-result repair pass repeated focused runs;
- Unix TUI workflow, terminal restoration, and sustained slow-PTY restoration
  pass three runs;
- TUI hot-path and 10,000-message Session inspection performance budgets pass
  three runs; and
- focused `-race` coverage across engine, ACP, TUI, plain, and headless
  entrypoints passes three runs.

The rollback drill checked out pre-cutover commit
`b12ef287746af0c1b0fd1dbba20f91e8c37b12ab`, created a real
`project_graph/v1/full` transcript, and invoked that revision's kernel
admission. Ten runs rejected the unknown stage before execution and preserved
the source JSONL byte-for-byte. The ephemeral drill source had SHA-256
`8acd4bef294ee9f6d16fa46f019a68ff3c78a6b8883c76331a35547114106169`
and was removed with its detached worktree after verification.

### Adoption Decision And Boundary

This slice remains `project-native`: public Eino Compose owns Graph traversal,
checkpoint, interrupt, and targeted resume; Eino-Agent owns stages, provider
retry/fallback, tool execution, permission, Session/transcript, child
lifecycle, event, transport, and TUI contracts. No Eino/Eino-ext source or
dependency changed.

P13 is complete. P14.0 is unblocked; future ProjectGraph changes require a new
user problem and accepted slice rather than reopening rollout or dual-owner
plumbing.

## P14.0 Explicit Foreground Detach

**Completed:** 2026-07-21
**Decision:** `combine`

### Problem

After P13.10 left one ProjectGraph execution owner, a long-running foreground
Agent still held its parent tool call until terminal state. Cancelling that
wait also cancelled the child, while relaunching in background would duplicate
identity, model/tool work, permissions, and terminal ownership. The missing
product operation was a narrow wait transition, not another Graph or actor.

### Resolution

- added a generation-scoped foreground wait lease to `AgentRunner` and an
  engine-owned `DetachAgent` control addressed by exact Agent ID, generation,
  and parent Session;
- moved foreground executor entry to one runner-owned goroutine while retaining
  the same execution context, ProjectGraph invocation, Agent record, worktree,
  transcript, permission scope, and join accounting;
- forwarded parent cancellation only while the foreground lease is active.
  A winning detach returns one structured `backgrounded` outcome; later parent
  cancellation does not reach the child, while explicit abort and owned-runner
  shutdown remain authoritative;
- serialized detach, completion, parent cancellation, abort, and Close against
  the same generation. Terminal, originally background, stale-generation,
  duplicate-detach, unknown, and wrong-owner requests fail closed;
- added a non-terminal `backgrounded` lifecycle projection. The runtime store
  assigns its sequence under the same admission mutex as live Graph events and
  reuses an active child `TurnID`, preserving lineage without starting,
  clearing, or completing a turn; and
- kept durable Agent and transcript schemas, default foreground launch,
  permission/tool capability, TUI controls, auto-detach, and completion
  delivery unchanged.

### Verification

- focused runner tests cover completion-first, detach-first, cancellation
  before and after detach, abort, shutdown, wrong owner, stale generation,
  originally background execution, concurrent duplicate detach, and 25
  detach/completion races. Recorder reentry, failure, and blocked-recorder
  abort/shutdown tests prove lifecycle publication does not hold runner locks
  or leak the foreground wait;
- focused `-race` tests pass for runner and engine detach boundaries;
- a real foreground ProjectGraph fixture reaches a blocking tool, detaches
  through `QueryEngine`, survives parent cancellation, and completes with the
  original Agent/Session/thread/generation after exactly two model calls and
  one tool side effect;
- runtime reducer replay proves `backgrounded` preserves the active turn,
  running status, and empty terminal fields; and
- repository formatting, lint, tests, builds, documentation links, and
  migration-ledger validation passed at closeout.

### Adoption Decision And Boundary

The slice combines Grok Build's explicit foreground/background transition with
Eino-Agent's existing runner, ProjectGraph, runtime reducer, and lineage
contracts. Public Eino Compose continues to own traversal; project code owns
the product-specific wait lease, cancellation routing, generation identity,
and runtime projection. No Eino/Eino-ext source or dependency changed.

P14.1 is now ready. It may add durable, idempotent terminal delivery, but must
not reinterpret the process-local P14.0 wait outcome as a completion receipt.

## P14.1 Durable Idempotent Completion Delivery

**Completed:** 2026-07-21
**Decision:** `project-native`

### Problem

Child terminal status survived restart, but parent delivery did not have a
durable identity or receipt. The runtime-input coordinator could retry a stable
process-local notification ID, yet a restart after terminal persistence or
after parent transcript commit could either lose the projection or inject it
again. A bounded in-memory/transcript projection could not be the authority
because compaction and long histories legitimately evict it.

### Resolution

- added a versioned child completion snapshot containing deterministic
  `CompletionID`, generation, terminal status/sequence, exact parent lineage,
  notification payload, and creation time. The runner commits it with terminal
  metadata before publication and advances terminal sequence across resumed
  generations;
- reconstructed retained and evicted durable terminals only for their exact
  parent Session/thread/Agent scope. Legacy terminal metadata receives an
  explicit compatibility identity, while stale disk-only running state remains
  inert aborted replay;
- retained `RuntimeInputCoordinator` as at-least-once transport: notification
  enqueue precedes source acknowledgement, and the parent transcript stores a
  versioned receipt in the same model-facing attachment before ledger
  settlement;
- bounded `LoadFull` receipt projection to the newest unique records for
  diagnostics, while correctness checks only current candidate/ledger IDs
  against the complete append-only audit. Historical runtime-item/command UUID
  coverage remains recognized, compact boundaries do not erase delivery, and
  unknown receipt versions fail closed for the identity they name;
- cached exact transcript coverage without holding the coordinator mutex across
  file I/O and recovered a transcript-covered processing item by removing it
  from the durable ledger before it could return to pending; and
- stored delivered completion identity in `RuntimeStateStore`, collapsing
  duplicate parent attachments without a second parent message or Agent
  mutation.

### Evidence

- runner fixtures cover durable-before-publication failure, exact-parent
  filtering, retained/evicted reconstruction, legacy metadata, and resumed
  terminal sequence/identity;
- transcript fixtures cover typed and JSON-round-tripped receipts, bounded
  unique projection, and unknown versions;
- engine restart fixtures cover terminal-to-enqueue redelivery, concurrent
  collection, parent receipt settlement, restart suppression, unknown-version
  suppression with child diagnostics, and reducer duplicate collapse;
- a crash-window fixture leaves one completion processing in the coordinator,
  commits its receipt directly, appends 300 newer receipts, installs an empty
  compact boundary, and proves restart settles the ledger and does not
  re-inject the child; and
- repeated focused race coverage and an independent recovery/concurrency review
  found no blocking issue before repository closeout.

### Adoption Decision And Boundary

This slice is `project-native`. Public Eino Compose continues to own Graph
traversal, while Eino-Agent owns child metadata, exact parent scope,
runtime-input durability, transcript audit/receipt semantics, and bounded
runtime projection. No Eino or Eino-ext source/dependency, Graph topology,
kernel selection, child cancellation, auto-wake behavior, or TUI control
changed.

P14.1 did not provide child transcript pagination, lazy TUI loading, or a
multi-Agent monitor. The following P14.2a slice added the durable record
identity prerequisite without changing those consumers.

## P14.2a Versioned Durable Transcript Entry Identity

**Completed:** 2026-07-22
**Decision:** `project-native`

### Problem

The append-only transcript was durable but its physical records had no stable
identity. Future bounded child-transcript paging could therefore neither merge
live and durable evidence safely nor continue across a supported rewrite.
Display text was not a valid substitute because duplicate messages are normal,
and old JSONL could not be eagerly rewritten without changing audit bytes.

### Resolution

- added an optional `entry_id {version,id}` envelope to every new physical
  message, lifecycle, replacement, metadata, file-snapshot, atomic-rewrite, and
  branch record. The v1 codec uses a cryptographically random 128-bit value;
- made `LoadFull` expose every valid physical record in source order together
  with an SHA-256 revision of the exact opened file bytes, while preserving its
  existing active-context, corruption, usage, and completion-receipt views;
- derived old-record fallback identity from source, valid-record ordinal,
  timestamp, kind, and canonical full payload digest. The fallback carries its
  revision, so reuse after append/rewrite fails with
  `ErrTranscriptRevisionChanged` rather than claiming continuity;
- preserved ID and timestamp across replace and atomic-replace only when an
  existing message record is proven by tracked identity plus payload or an
  ordered full-payload match. Identical records keep separate identities;
- issued fresh identities for consolidated replacement records, newly
  synthesized compact markers/summaries, legacy records promoted by rewrite,
  and branch copies. Unknown positive identity versions remain opaque and are
  retained; invalid or duplicate IDs stay readable under explicit legacy
  fallback plus a corruption diagnostic; and
- kept old files migration-free and left pagination, Agent detail/TUI,
  lifecycle, completion delivery, Graph topology, and Eino/Eino-ext unchanged.

### Evidence

- focused transcript tests cover all physical append kinds, old-reader
  compatibility, exact revision hashing, duplicate payloads and repeated
  message pointers, replace/atomic-replace retention, synthesized replacement
  and compact identity, legacy byte preservation and cursor invalidation,
  future versions, and branch source separation;
- ordinary and race transcript suites pass, followed by engine package tests;
- an independent persistence/recovery review found no blocking identity,
  revision, rewrite, or compatibility issue; and
- formatting, lint, full tests/builds, documentation links, manifest, and diff
  checks passed at closeout.

### Adoption Decision And Boundary

This slice is `project-native`. Public Eino Compose remains the sole Graph
traversal owner. Eino-Agent owns transcript record identity, rewrite proof,
legacy compatibility, and cursor-revision safety. P14.2b may now consume this
identity for bounded durable selection; it must not reintroduce display-text
deduplication or restore executable state from transcript records.

## P14.2b Bounded Durable Child Transcript Selector

**Completed:** 2026-07-22
**Decision:** `combine`

### Problem

The current Agent detail projection could eagerly expose only retained or
evicted snapshots. It had no durable cursor, used display-derived merge keys,
and could not inspect an unloaded child transcript without full materialization
or risk rebinding an asynchronous result to another generation.

### Resolution

- added a bounded reverse JSONL reader over a frozen regular-file prefix. It
  returns source-ordered messages from the newest active lifecycle context,
  limits page size and record bytes, accepts append without admitting new rows,
  and rejects symlink, replacement, truncation, oversized record, or identity
  conflict;
- retained exact legacy identity by scanning only the frozen prefix for its
  revision and valid-record ordinal when required. Modern records avoid a
  full-file hash; unsupported same-inode, same-size external overwrites remain
  outside the writer contract;
- added a process-local opaque cursor bound to Agent, Session, thread,
  generation, transcript path, file identity, frozen prefix, and continuation
  boundary. Cross-child, cross-generation, or stale file reuse fails closed;
- attached exact physical transcript provenance to runtime messages only after
  their checkpoint was durably persisted, and merged live/durable rows only by
  that identity. Duplicate display text remains distinct;
- made replay-only and evicted projections durable-authoritative and kept the
  selector disconnected from model, tool, queue, callback, approval, control,
  Session restore, and current TUI projection paths.

### Evidence And Boundary

Focused ordinary and race tests cover modern and legacy multi-page reads,
lifecycle reset/compact selection, source order, duplicate display text and
persisted IDs, append exclusion, replacement/truncation/symlink rejection,
legacy revision invalidation, cursor rebinding, exact live merge,
replay/eviction, and nil-runner dispatch. An independent runtime/persistence
review found no blocker. Repository formatting, lint, test, build,
documentation, manifest, and diff gates passed at closeout.

Public Eino Compose remains the Graph traversal owner. The project combines
that execution topology with its own transcript/storage and runtime-state
contracts at a read-only selector boundary; no Eino/Eino-ext source,
dependency, Graph topology, child lifecycle, TUI, or durable schema changed.
P14.2c may now consume the selector and owns stale asynchronous result
rejection plus presentation-state preservation.

## P14.2c Existing Child Detail Projection

**Completed:** 2026-07-22
**Decision:** `combine`

### Problem

P14.2b exposed safe bounded pages, but the three existing TUI Agent inspection
surfaces still called the eager `AgentDetailSnapshot` transcript projection.
Opening an evicted child could therefore materialize the whole transcript,
late reads lacked presentation-generation rejection, equal-content physical
records could be merged by mutable chat helpers, and replay/evicted detail
controls still routed live mutations.

### Resolution

- added one reusable TUI pager bound to Agent, thread, execution generation,
  request generation, and opaque cursor. Thread switching, Ctrl+B Agent detail,
  and Teams detail all call the engine-owned `AgentTranscriptPage` provider;
- made page reads Bubble Tea commands and re-resolved the current selector when
  applying results. Switching surfaces, changing child, rolling generation, or
  superseding cursor/request generation discards the old result;
- projected one immutable semantic history item per transcript entry identity,
  with selector message ID as the only fallback. Equal text remains distinct;
  continuation pages prepend in source order and preserve the visible anchor;
- kept page/cache state in the existing per-thread presentation store, so
  thread switching retains draft, editor cursor, scroll/follow position,
  search state, and selected detail tab without persisting process-local
  cursors into the Session sidecar;
- allowed explicit output/lineage compatibility reads while removing eager
  transcript reads from production open, refresh, and paging paths; and
- gated composer send plus shared message/pause/resume/abort controls on
  `live_attach`, making replay-only and evicted transcript surfaces inert.

### Evidence And Boundary

Focused ordinary and race tests cover one bounded first page, opaque cursor
continuation, source order, equal-content distinct identities, rapid A-to-B
switching, already-loaded old-generation replies, presentation restore,
responsive Ctrl+B/Teams panels, no-color rendering, and zero control calls from
read-only modes. Existing TUI detail, navigation, responsive, golden, and
performance suites remain green. Independent review and repository Makefile,
documentation, manifest, and diff gates are recorded by the closeout PR.

This slice combines the existing Eino Compose ProjectGraph owner with the
project-owned durable transcript selector at a presentation-only boundary.
Eino/Eino-ext source and dependencies, engine selectors and schemas, Graph
topology, model/tool traversal, child lifecycle, permission, and terminal
ownership are unchanged. Rollback removes the TUI consumer while leaving the
independent P14.2b selector intact.

## P14.3 Compact Multi-Agent Monitor And Read-Only Peek

**Completed:** 2026-07-22
**Decision:** `combine`

### Problem

The existing `/team` surface was an alternate child-control/detail panel. It
did not expose the exact foreground/background distinction, waiting attention,
elapsed work, or terminal outcome needed to scan several children. Opening it
while the leader request ran was blocked by the generic slash-command guard,
and inspection required either eager detail or a full thread switch.

### Resolution

- repurposed `/team` as one responsive read-only monitor. Rows come from the
  canonical `TaskAgentSnapshot` joined with `ThreadCatalogSnapshot`, rather than
  a widget-owned child store;
- derived foreground, originally background, and detached `backgrounded`
  display mode from the exact live `AgentRunner` generation when the selector
  is read. This adds no runtime event, reducer mutation, checkpoint, or durable
  schema;
- rendered distinct text for execution/status, waiting-input, paused,
  completed, failed, aborted, attention, elapsed time, current activity, and
  terminal outcome. Compact rows preserve the visible peek/switch commands;
- added a fixed-geometry `Tab` peek over `AgentDetailSnapshot` plus the bounded
  P14.2 transcript pager. Agent/thread/generation/request identity rejects stale
  pages without moving footer controls after load;
- routed `Enter` through `activateThreadByIDWithCmd`, preserving the existing
  per-thread draft, cursor, focus, scroll, and presentation-state behavior;
- removed send/resume, pause/resume, abort, and steering providers from the
  `/team` component. Permission/question settlement and execution-mode changes
  are likewise absent; and
- admitted `/team` before the running-request slash guard because inspection is
  read-only, while commands that can execute or mutate remain serialized.

### Evidence And Boundary

Focused selector and runtime-snapshot tests prove exact-generation mode
enrichment without fallback ownership. Responsive tests cover 40, 80, and 140
columns; no-color/reduced-motion rendering retains textual meaning; stale page,
completion refresh, fixed controls, read-only keys, and switch/return tests
cover the presentation boundary. The Unix PTY workflow covers open, move,
peek, switch, return, resize, live-to-completed refresh, and final terminal
restoration. Focused race tests and all repository Makefile/documentation gates
remain part of the closeout PR.

The adoption decision remains `combine`: public Eino Compose owns ProjectGraph
traversal, while project-owned selectors and Bubble Tea own inspection and
presentation. No Eino/Eino-ext source or dependency, Graph topology, child
execution, permission, persistence, or terminal writer changed. Rollback
removes the monitor/peek route and retains the independent thread picker,
detail views, selectors, and durable transcripts.

## P16.5a Unified Execution And Safety Controls

**Completed:** 2026-07-22
**Decision:** `combine`

### Problem

The typed command executor still admitted unknown model routes, treated local
continuation budget as reasoning effort, scoped plan/permission controls to
only some entrypoints, and left TUI/ACP direct controls with separate mutation
paths. Registration truth therefore did not guarantee capability truth or one
effective state.

### Resolution

- added runtime-resolved command availability so help, completion, palette,
  contextual lookup, and dispatch consume the same provider/model capability;
- injected the production provider runtime as a model resolver and made model
  changes validate before one serialized mutation. Invalid requests preserve
  model/effort, while incompatible successful switches clear effort;
- made effort a real Agentic Claude thinking-capability value forwarded to
  `output_config.effort`, independent from continuation `TokenBudget`, and
  omitted it after an incompatible fallback;
- extended `/permissions` with typed mode, rule, and explicit bypass-confirm
  operations, retained `/plan` as the canonical plan surface, and rewrote
  command output from engine-effective state after one transition; and
- routed TUI model/bypass controls and ACP model/effort/mode configuration
  through the same engine owners. Active-turn conflicts fail before mutation,
  and ACP emits configuration/current-mode updates after accepted changes.

### Evidence

- focused registry and engine tests cover contextual discovery/dispatch,
  rejection before mutation, effort forwarding and fallback clearing, exact
  permission confirmation, once-only rule persistence, effective plan output,
  and active-turn serialization;
- TUI tests cover capability-filtered palette/help, model-picker rejection, and
  confirmed bypass reuse without a local state owner;
- ACP tests cover New/Resume/Load configuration and mode projection, union
  validation, model/effort changes, bypass rejection, and slash/protocol update
  parity; and
- repository formatting, lint, tests, builds, documentation links, migration
  manifest validation, focused race checks, and independent review passed at
  closeout.

### Adoption Decision And Boundary

This slice combines the provider runtime's Eino-backed model route with a
project-owned command, permission, session, and entrypoint contract. It changes
neither Eino/Eino-ext source or dependency versions nor Compose Graph topology.
Reasoning effort remains process-local; durable configuration or transcript
replay requires a separate accepted migration.

## P16.5b Truthful Diagnostics

**Completed:** 2026-07-22
**Decision:** `combine`

### Problem

Status, context, stats, cost, settings, doctor, login, and the TUI status band
each reconstructed a different partial truth. They mixed hard-coded provider or
release defaults, four-characters-per-token estimates, generic prices,
credential suffixes, process environment guesses, and package-global state.
Provider usage was not accumulated by the live engine or carried through
lifecycle snapshots, so zero could mean empty, missing, or simply unwired.

### Resolution

- introduced one renderer-neutral `QueryEngine.DiagnosticsSnapshot` whose
  fields carry explicit state, source, observation time, and safe detail;
- made `/status`, `/context`, `/usage`, `/config`, and `/doctor` engine-owned
  across TUI, plain, headless, and ACP. `/stats`, `/cost`, and `/settings`
  became compatibility aliases, while `/login` became hidden because no auth
  flow is owned;
- added a version-1 cumulative transcript `UsageSummary`. Ordinary provider responses
  append exact `ResponseMeta.Usage`; clear, compact, checkpoint, new-session,
  and child-admission boundaries persist a cumulative snapshot, and replay
  replaces at boundaries before adding later responses;
- preserved unknown future usage snapshot versions as durable coverage gaps
  without interpreting their token fields or erasing the gap at the next
  boundary;
- retained missing response metadata, legacy lifecycle gaps, and transcript
  corruption as explicit partial/unavailable coverage. A known-zero ledger is
  reserved for a session with no usage-relevant provider response;
- resolved provider diagnostics through the injected production resolver but
  returned only provider/model, field sources, credential presence, and an
  endpoint origin. Config checks read at most 1 MiB, doctor IDs remain stable,
  and connectivity is skipped without a network call; and
- changed the TUI status and spinner to use provider-reported usage and a known
  model window. Generic dollar display, message-size fallback, cost threshold,
  and the cost-warning dialog were removed.

### Evidence

- source/freshness fixtures cover provider resolution, exact context windows,
  known zero, missing metadata, legacy boundaries, corruption, cancellation,
  stable doctor IDs, invalid bounded config, and JSON-level secret/URL
  redaction;
- transcript and compaction tests cover cumulative boundary replacement,
  post-boundary additions without double counting, LLM compaction usage, and
  restart restoration;
- registry and renderer tests cover canonical/alias snapshots, hidden login,
  forbidden estimate/price/secret markers, and identical diagnostic results
  across TUI, plain, headless, and ACP; and
- TUI tests prove provider-reported context/spinner values and no message-size
  or generic-price fallback. Focused race validation and the repository
  closeout gates passed.

### Adoption Decision And Boundary

This slice combines Eino's provider response metadata, the project provider
resolver, append-only transcript lifecycle, and the existing single command
executor behind a project-owned diagnostic contract. It changes no Eino or
Eino-ext source/dependency, Graph topology, kernel selection, provider request,
or authentication ownership. Billing remains unavailable until an
authoritative provider/model catalog is explicitly attached; network
connectivity remains the startup preflight's responsibility.

## P16.5c Workspace And Terminal Capability Convergence

**Completed:** 2026-07-22
**Decision:** `combine`

### Problem

Workspace and terminal commands had typed registry metadata but still applied
effects in incompatible owners. `/diff` and `/commit` started Git directly in
command handlers and discarded failures; `/copy` wrote OSC 52, launched native
clipboard processes, and always created a temp backup; `/init` wrote
Claude-shaped settings before permission admission; `/memory` created files and
launched an arbitrary editor; and `/add-dir` trusted a cleaned string, updated
only in-memory state, and did not canonicalize symlink aliases. TUI-only names
were not one coherent capability subset, and `/terminal-setup` remained a false
setup affordance.

### Resolution

- moved `/diff` behind one injected engine-owned read-only Git runner with
  cancellation, a bounded timeout, explicit terminal states, safe repository
  environment, disabled external diff/textconv/fsmonitor, and initial-repository
  handling. `/files` now projects only canonical transcript-observed paths
  inside the detached active-root snapshot;
- made `/add-dir` a raw typed intent. The engine action owner expands the exact
  home form, resolves symlinks to an existing directory, rejects unsafe paths,
  deduplicates existing-root coverage, commits the canonical root under the
  active turn lock, and persists it through the normal final checkpoint;
- made `/copy` a contextual TUI-only action over committed assistant messages.
  The handler performs no clipboard or temp-file I/O; the TUI reuses its
  terminal clipboard projection only after interactive-capability admission;
- adapted `/init` to project-native `AGENTS.md` and limited `/memory` mutation
  to explicit `edit project`, `delete project`, or `migrate project`. These and
  `/commit` now return ordinary Agent workflows instead of performing Git,
  editor, or filesystem effects in their handlers; and
- kept `/keybindings`, `/terminal`, `/search`, and `/copy` out of plain,
  headless, and ACP discovery, and removed `/terminal-setup` from registration.

### Evidence

- engine and command tests cover non-Git, unavailable, failed, timed-out,
  cancelled, and unborn Git states; redirect-environment isolation;
  no-external-command flags; end-to-end headless diff projection; and no runner
  call after pre-start cancellation;
- path tests cover real-path storage, symlink aliasing, nested duplicates,
  invalid-file rejection before mutation, outside-root file omission, and
  checkpointed additional roots;
- registry and workflow tests cover TUI clipboard capability, committed-result
  selection, no handler-side backup or configuration write, explicit memory
  scope, removed terminal setup, and cross-entrypoint discovery; and
- focused race checks, repository Makefile gates, documentation/link and
  manifest validation, diff checks, and independent review passed at closeout.

### Adoption Decision And Boundary

This slice preserves useful workspace outcomes, adapts project initialization
and memory to `AGENTS.md` plus the existing permission model, and combines the
typed engine action owner with TUI terminal capability. It changes no Eino or
Eino-ext source/dependency, Compose Graph topology, provider request, or public
Eino schema. The Git runner is a bounded read owner, not a second Agent tool
executor; workflow mutations continue through ordinary tools. Bundled workflow
source/precedence remains P16.6.

## P16.6 Bundled Workflow Commands

**Completed:** 2026-07-22
**Decision:** `combine`

### Problem

Seven prompt macros were compiled into Go command registration even though
their only runtime outcome was model prompt content. Their names and templates
therefore required a binary change, had no explicit source or trust class, and
did not participate in the complete atomic generation already used for
configured plugin commands. A partial or malformed reload could not be judged
against one bundled/configured snapshot.

### Resolution

- moved `/commit`, `/review`, `/pr-comments`, `/summary`, `/issue`,
  `/onboarding`, and `/commit-push-pr` into a schema-versioned embedded JSON
  pack while preserving exact argument-dependent prompt outcomes;
- generalized the dynamic registry boundary from plugin-only replacement to one
  prompt-command generation containing the bundled pack followed by qualified
  configured plugin sources. Core static commands retain precedence;
- added command source, source version, and trust metadata and exposed it in
  detailed help and runtime inspection. Bundled commands are unqualified and
  configured plugin commands retain `<plugin>:<command>` qualification;
- made pack parsing, template validation, source diagnostics, collision checks,
  digesting, and registry publication one fail-closed candidate operation. Any
  error retains the complete live generation; and
- retained every workflow as `ActionPrompt` with no handler-side Git,
  filesystem, or external-service action. A configuration switch disables the
  bundled pack without removing compiled runtime/session/safety commands.

### Evidence

- golden fixtures cover all seven workflows and both argument branches where
  applicable;
- loader and registry tests cover source/trust attribution, deterministic
  precedence, core/name/alias collisions, malformed-pack retention, pack
  disablement, immutable metadata, startup/reload counts, and shared TUI
  registry adoption;
- focused generation and engine/TUI race tests pass; and
- repository formatting, lint, tests, builds, documentation links, migration
  manifest validation, diff checks, and independent review passed at closeout.

### Adoption Decision And Boundary

This slice combines the project-owned atomic generation introduced in P16.5d
with a data-driven embedded pack. It does not modify Eino or Eino-ext because
command discovery, source precedence, and permission-visible prompt ownership
are application contracts rather than missing Graph or Agent runtime
capabilities. Compose Graph topology, kernel selection, provider requests, and
durable schemas are unchanged. Rollback may remove the bundled generation, but
must not restore privileged workflow side effects or partial publication.

## P16.7a CLI Foundations

**Completed:** 2026-07-22
**Decision:** `combine`

### Problem

Headless execution was hidden behind root `-p`; a root positional prompt could
be silently ignored, prompt plus piped stdin discarded the pipe, and no stable
machine result or cancellation exit existed. Root persistent flags leaked
model, permission, and tool options into `serve mcp` even though it consumed
none of them. Version facts were duplicated, completion was absent, and the
default MCP hook logged tool argument and raw error payloads.

### Resolution

- added explicit `exec [prompt]` while retaining root `-p` as a compatibility
  route to the same headless `QueryEngine`; no prompt and `-` read stdin, while
  prompt plus piped stdin appends an explicit `<stdin>` context block;
- rebuilt a fresh Cobra tree per execution with no persistent flags. Root
  interactive mode, `exec`, `resume`, and `serve acp` each declare consumed
  runtime flags; MCP/version/completion reject no-op runtime configuration;
- separated headless collection from rendering. Text or one schema-versioned
  JSON object exclusively owns stdout, diagnostics use stderr, and stable exits
  distinguish complete, runtime failure, usage, and cancellation;
- propagated OS interrupt through the command context and classified both
  context cancellation and aborted terminal reasons as exit `130`;
- introduced one renderer-neutral build identity shared by CLI version, slash
  `/version`, MCP implementation metadata, and release ldflags, plus
  runtime-independent shell completion; and
- reduced default MCP hook diagnostics to tool name, byte counts, outcome, and
  duration, excluding argument/result bodies and raw error text.

### Evidence

- command-tree negatives prove flag isolation, root positional rejection,
  usage exit `2`, and no runtime initialization for version/completion;
- prompt fixtures cover argument, stdin, explicit `-`, prompt-plus-stdin, empty
  input, and TTY behavior;
- renderer tests cover one JSON envelope, stable status/exit fields, terminal
  failure, cancellation `130`, and credential/URL/tool-body redaction;
- MCP tests prove metadata-only default logs, and focused CLI/buildinfo/MCP race
  tests pass; and
- all repository Makefile, lint-new, documentation, manifest, diff, smoke, and
  independent-review gates passed at closeout.

### Adoption Decision And Boundary

This slice combines Codex-style explicit stdin and stdout ownership,
Crush-style explicit non-interactive command and completion semantics,
Claude-style centralized process result handling, and OpenCode-style
protocol-local flag scope under a project-owned contract. It adds no second
conversation runtime and changes no Eino/Eino-ext dependency, Compose Graph
topology, query kernel, provider request, or durable schema. P16.7b-c may now
project the existing session, diagnostics, and extension services through this
CLI foundation.

## P16.7b Sessions CLI Projection

**Completed:** 2026-07-22
**Decision:** `combine`

### Problem

Durable session discovery and mutation already converged behind one
engine-owned `SessionService`, but process callers still had to enter the TUI,
submit a slash command through a conversation engine, or duplicate transcript
logic. Constructing the ordinary CLI engine also required provider resolution
and started unrelated runtime services. Hidden `/history`, `/rename`, and
`/export` shortcuts extended a transitional compatibility surface after the
canonical `/sessions` command was established.

### Resolution

- added provider-free `sessions {list,resume,rename,export,fork}` commands that
  call the existing service instead of reading or mutating transcript files in
  Cobra handlers;
- added a lightweight administration `QueryEngine` constructor that skips
  provider resolution, MCP connection, plugin generation, shell hooks, and the
  settings watcher, and does not compile ProjectGraph merely to list sessions;
- resume and fork activation still validate the selected durable kernel and
  preserve the canonical target-session restore checkpoint, but skip project
  MCP/skill/hook/watcher/worktree/Agent/long-service activation. Close appends
  neither a second target checkpoint nor a synthetic source transcript;
- kept exact-source and duplicate-ID resolution for list-to-action flows.
  Resume restores and reports then exits, while `exec --resume` remains the
  model-turn continuation path;
- preserved fork commit-before-activation ordering and operation-owned child
  compensation through the same service boundary used by slash execution;
- projected text and schema-versioned JSON from one result model with stable
  success/runtime/usage/cancellation exits `0`/`1`/`2`/`130`; and
- removed the superseded hidden `/history`, `/rename`, and `/export` shortcuts
  at their planned boundary. Archive/delete remain absent because no retention,
  confirmation, recovery, or rollback contract has been accepted.

### Evidence

- CLI tests prove list works with invalid provider configuration, emits stable
  JSON, creates no synthetic transcript, and exposes no archive/delete command;
- lifecycle tests cover exact-source resume, rename, atomic export, durable
  fork identity/lineage, missing-session failure, cancellation `130`, and
  removed shortcut rejection. CLI projection tests also cover duplicate-ID
  rejection and versioned usage errors before command execution;
- engine tests prove administration construction and close behavior plus
  ambiguous-ID failure, while focused race coverage exercises the shared
  session/CLI boundaries; and
- repository Makefile, lint-new, documentation, manifest, diff, and independent
  review gates passed at closeout.

### Adoption Decision And Boundary

This slice combines the project-owned session lifecycle service with a
project-native CLI administration host. Eino and Eino-ext are intentionally
unchanged: the work is process composition, durable-session ownership, and
output/exit policy, not model orchestration. No Compose Graph topology, query
kernel, provider request, or durable transcript schema changed. Rollback may
remove the CLI projection, but must not reintroduce a second transcript owner
or weaken fork commit/activation/compensation ordering.

## P16.7c Diagnostics And Extension CLI Projection

**Completed:** 2026-07-22
**Decision:** `combine`

### Problem

P16.5b and P16.5d already owned truthful diagnostic snapshots, MCP/runtime
inventory, and atomic prompt-command generations, but scripts still had to
start a conversation or parse slash-command text to inspect them. Reusing the
ordinary CLI engine would construct a provider runtime and unrelated services;
building a fresh unconnected MCP host could also falsely report configured
servers as healthy runtime state. Plugin validation needed the same final
registry collision checks as reload without publishing the candidate.

### Resolution

- added provider-free `config show`, `doctor`, `mcp {list,get}`, and
  `plugins {list,validate,reload}` Cobra projections with the shared versioned
  administration envelope and stable `0`/`1`/`2`/`130` exit taxonomy;
- added one short-lived inspection `QueryEngine` host that reuses current
  diagnostic and prompt-generation owners while skipping provider runtime,
  MCP connection, Graph compilation, transcripts, skills, hooks, watchers,
  worktree recovery, Agent replay, and long-lived services;
- reused the P16.5b configuration/doctor snapshot and text renderers. Provider
  resolution remains a side-effect-free fact source; invalid effective config
  fails `config show`, while `doctor` reports invalid settings through stable
  check IDs, keeps connectivity skipped, and marks the intentionally absent
  active Session transcript as skipped;
- projected configured MCP server name/enabled state through the existing
  inventory schema with snapshot source `configuration`, server state
  `configured` or `disabled`, and health `unprobed`. Connection commands,
  URLs, arguments, environments, headers, clients, and discovered tools are
  never retained or rendered;
- split prompt-generation preparation from commit so validation applies the
  same source/name/alias/live-registry collision checks and returns the retained
  generation under one read lock without mutation. Reload still swaps a whole
  generation atomically, but its result declares `inspection-host` process
  scope; and
- kept MCP add/remove and plugin install/uninstall/enable/disable/marketplace
  absent until their existing synchronization, containment, trust,
  persistence, and rollback gates close.

### Evidence

- CLI fixtures cover provider-invalid route reporting, malformed settings,
  secret and URL redaction, stable JSON and text, usage/cancellation exits, and
  unsupported mutation rejection without creating a transcript;
- an executable MCP fixture proves inspection never launches the configured
  command, while snapshots omit all connection material and distinguish
  configuration from runtime inventory even when health is unprobed;
- registry and engine tests prove candidate validation leaves command maps,
  order, revision, digest, and the retained generation unchanged; malformed
  candidates preserve the live generation and successful inspection reload is
  atomic; and
- focused ordinary/race, repository Makefile, lint-new, documentation,
  manifest, diff, and independent-review gates passed at closeout.

### Adoption Decision And Boundary

This slice combines the existing project diagnostic and extension owners with
a project-native process projection. Eino and Eino-ext are intentionally
unchanged because no model/tool traversal, interrupt, checkpoint, or Runner
problem is being solved. No Compose Graph topology, query kernel, provider
request, MCP runtime generation, durable transcript, or public schema changed.
Rollback may remove the CLI projection and inspection-only adapters, but must
not duplicate diagnostic collection, claim configured MCP health as live, or
weaken atomic prompt-generation replacement.
