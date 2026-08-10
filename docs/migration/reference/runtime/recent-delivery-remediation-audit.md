# Recent Delivery Remediation And Modern Agent Foundations

**Status:** reference-snapshot
**Assessed:** 2026-07-26
**Eino-Agent snapshot:** `303a757fb18651bb9ac436eabb2a6c02ccf84a9a`
**Reference snapshots:** Codex `66bd101fff6f`; Claude Code Ripe
`4b9d30f79532`; OpenCode `411eff73f026`

> **Ownership:** this document records current-source evidence, repair
> recommendations, atomic implementation boundaries, and promotion gates. It
> does not declare runtime behavior complete, assign a P-number, or make a
> slice executable. Reproduced gaps belong in
> [`REMAINING.md`](../../REMAINING.md), accepted order and the sole `Ready`
> slice belong in [`PLAN.md`](../../PLAN.md), current facts belong in
> [`STATUS.md`](../../STATUS.md), and delivered evidence belongs in
> [`history/`](../../history/README.md).

## Result

The recent delivery baseline is substantial, but its documentation currently
overstates four observable properties:

1. P20 bypass confirmation is visible and defaults to No, but review paging and
   pointer geometry can still mutate the underlying dialog while confirmation
   owns focus.
2. G11 has one project width profile at most migrated boundaries, but the
   fixed-size Lip Gloss adapter remains a second geometry owner and the global
   Markdown renderer/lock caches have no lifetime or capacity bound.
3. P21 validates a palette snapshot before dispatch, but records a command as
   Recent before strict dispatch and action application succeed.
4. Permission policy still decides whether an action may run; it does not
   contain an allowed shell process at an OS boundary. The project also lacks a
   real-repository task evaluation harness, so broad runtime changes cannot be
   compared against a stable product outcome.

The correct response is not one cross-owner P25 program. The findings have
different state owners, failure modes, rollback boundaries, and proof
requirements. They should remain separate gaps and be promoted one at a time.
P22.H0 remains the only `Ready` work because its Bash prefix auto-allow is an
active authorization defect.

> **Current replacement (2026-07-26):** P22.H0 subsequently closed G13 and
> left the live queue. See
> [`p22-h0-bash-containment.md`](../../history/runtime/p22-h0-bash-containment.md)
> and current [`PLAN.md`](../../PLAN.md). The findings and queue wording below
> remain the assessed snapshot.

## Evidence Boundary

The labels below are deliberate:

- **Verified** means current production source or a focused current test
  directly establishes the behavior.
- **Inference** means the source establishes a mechanism and risk, but a
  deterministic fixture is still required before claiming a user-visible
  failure on every terminal or platform.
- **Recommendation** is a project-owned target contract, not current behavior
  or accepted execution order.

Registration, comments, completed history records, and reference similarity
are not runtime proof. This audit does not reopen a correctly delivered owner
merely because a later implementation gap exists beside it.

## Finding Matrix

| Gap | Current-source result | Consequence | Decision | Ledger state |
|---|---|---|---|---|
| G13 / P22.H0 | **Verified:** `AcceptEditsCheck` classifies Bash from the first whitespace-delimited token, and both `acceptEdits` and `auto` call it before prompt/review. | A compound, redirected, substituted, wrapped, outside-root, protected, or symlink-sensitive command can inherit an approval that was never proven. | `combine`; execute the already accepted deterministic containment slice before model review. | Sole `Ready` slice. |
| G3 | **Verified:** standalone MCP blocks writes only when the raw mode string equals lowercase `strict`; an unknown non-empty value behaves as open. | A typo silently weakens a separately exposed entrypoint that bypasses QueryEngine policy. | `adapt`; parse one typed fail-closed policy at startup. | Reproduced, not promoted. |
| G4 | **Verified:** plugin prompt/skill containment uses cleaned lexical paths without resolving symlinks. | A plugin-controlled link can escape its declared authority root. | `adapt`; bind loading to resolved authority and reject link escape. | Reproduced, not promoted. |
| G24 | **Verified:** Plan key paging runs before confirmation-focus routing; mouse routing can still consume previously published review/action/feedback geometry. | A modal safety decision does not exclusively own input while visible. This is a presentation-state defect, not a direct QueryEngine authorization bypass. | `preserve`; finish the frozen P20 confirmation-isolation contract. | New reproduced gap. |
| G25 | **Verified:** the fixed-size adapter expands tabs with the selected profile, then delegates width/height geometry to Lip Gloss. **Inference:** Unicode-version differences can move a border or hit target until differential fixtures prove the exact cases. | The claimed single geometry owner has an explicit second owner and removal condition that is not yet closed. | `preserve`; complete profile-owned fixed-size projection, then delete the exception. | New reproduced gap. |
| G26 | **Verified:** Markdown renderer and renderer-lock maps key theme/geometry generations and never evict; real resize advances geometry generation. | Long-lived resize/theme churn can retain renderers and mutexes without bound. | `project-native`; give the cache one bounded lifetime owner. | New reproduced gap. |
| G27 | **Verified:** palette selection calls `RecordRecent` after contextual snapshot validation but before `Registry.Dispatch`, command execution, and action application. | Failed, unsupported, cancelled, or stale-at-dispatch commands can appear as successful Recent selections. | `preserve`; mutate Recent only after the matching successful result commits. | New reproduced gap. |
| G28 | **Verified:** Bash provides CWD, timeout, pipes, and process-group cancellation but no filesystem, network, syscall, credential, or resource containment boundary. | Permission mistakes or approved risky commands inherit the host user's ambient authority. | `project-native`; add an execution envelope without conflating it with permission policy. | New reproduced gap. |
| G29 | **Verified:** canonical trace fixtures cover kernel compatibility, but there is no isolated real-repository task suite with outcome, policy, cost, and recovery grading. | Large agent changes can pass package tests while reducing task success, safety, or efficiency. | `project-native`; establish a versioned evaluation harness before broad runtime replacement or autonomous expansion. | New reproduced gap. |

## Repair Contract: P22.H0 Bash Deterministic Containment

P22.H0 already owns this implementation. This audit does not duplicate or
change its accepted contract in
[`p22-auto-permission-review.md`](../../plans/p22-auto-permission-review.md).
Its key boundary is:

- remove Bash from token-based `acceptEdits`/`auto` approval;
- keep proven contained Write/Edit behavior unchanged;
- return unmatched Bash to the existing prompt or fail-closed path;
- do not add a model reviewer, expand auto policy, or reinterpret permission
  modes in this slice.

Negative tests must include compound commands, substitutions, wrappers,
redirections, absolute/outside-root paths, protected paths, and symlink-relevant
effects. This is the only work in this document that is currently executable.

## Repair Contract: G24 Confirmation Input Isolation

### Owner and state transition

`planDialog` remains the only presentation-state owner. When its focus is
`BypassConfirmation`, route input through a confirmation-only transition
before any review paging, action selection, feedback editing, or generic mouse
branch:

```text
BypassConfirmation
  Up/Down/Tab/Shift+Tab -> move only between visible No and Yes
  Enter or visible No   -> previous safe Plan state, no permission settlement
  Enter or visible Yes  -> one typed terminal bypass intent
  Esc                   -> previous safe Plan state, no permission settlement
  every non-confirmation input -> exact no-op
```

The overlay projection must publish only the visible No/Yes controls while
confirmation is active. Review, action, feedback, and page hitboxes from an
earlier frame must be cleared or replaced atomically with the returned frame.
Rendering and hit testing must consume the same confirmation geometry.

### Atomic implementation boundary

- primary code: `internal/tui/plan_dialog.go`;
- focused state tests: `internal/tui/plan_dialog_state_test.go`;
- mouse/final-frame tests only where the existing test owner requires them;
- no QueryEngine, permission settlement, persistence, replay, or plain/ACP/MCP
  behavior change.

### Acceptance

- PageUp/PageDown, wheel, review clicks, action clicks, and feedback clicks
  leave offset, focus, selected action, draft, cursor, and terminal result
  unchanged during confirmation;
- Up/Down/Tab/Shift+Tab change only the visible No/Yes selection, Enter
  activates that selection, and primary clicks can activate only the visible
  No/Yes hitboxes;
- Esc and visible No restore the safe prior state;
- visible Yes emits exactly one typed bypass result;
- stale geometry cannot activate a hidden control;
- focused TUI tests, race tests for touched state, normalized frames, and the
  existing P20 confirmation PTY scenario pass.

Rollback is a single TUI PR with no schema or persistent-state migration.

## Repair Contract: G25 One Fixed-Size Geometry Owner

### Owner

`DisplayCellProfile` must own wrap, truncate, pad, border-column, and hitbox
geometry. Lip Gloss may apply color and decoration, but its `Width`/`Height`
layout result cannot decide the final cell rectangle.

Introduce one package-private profile-owned fixed-size projection path in the
existing content-geometry owner:

1. derive the inner rectangle from requested outer width, border, and padding;
2. expand tabs at the actual column origin;
3. wrap/truncate semantic runs using the selected profile;
4. pad every projected row to the exact profile-cell width;
5. apply visual styling without asking a second library to resize content;
6. return both rendered rows and the geometry used by interaction routing.

Delete the fixed-size Lip Gloss exception from the G11 source gate only after
all production callers use the new projection.

### Differential proof

Add deterministic fixtures for post-Unicode-15 code points, Indic clusters,
variation selectors, ZWJ sequences, ambiguous-width scalars, tabs at non-zero
origins, combining-only input, ANSI SGR, OSC hyperlinks, narrow widths, and
border/padding combinations. For every row, assert:

- profile-measured outer width equals the requested width;
- borders occupy the expected exact columns;
- controls remain balanced;
- render and hit-test rectangles are identical;
- no supported-platform build selects a direct library width owner.

The production effect of Unicode-version disagreement remains an inference
until this differential matrix reproduces it. The repair can therefore be
reviewed as an owner-deletion slice without inventing an unsupported terminal
claim.

## Repair Contract: G26 Bounded Markdown Renderer Pool

Replace the process-global renderer and lock maps with one App-owned
`MarkdownRendererPool`, or an equivalently lifetime-bound owner injected
through the render environment. The pool should:

- use one atomic entry containing renderer, exact environment identity, and
  its serialization lock;
- key the exact display-cell profile, theme generation, geometry generation,
  color mode, and width required for correctness;
- enforce a small explicit capacity with LRU or generation retirement;
- permit an evicted in-flight entry to finish through its retained pointer;
- never destroy or reuse a renderer under another identity;
- expose test-only size/eviction counters without adding runtime diagnostics
  noise.

If measurement proves geometry generation is not semantically required for a
renderer, removing it from identity is a separate, source-backed optimization;
the first repair should bound retention without weakening cache correctness.

Acceptance requires deterministic theme/resize churn beyond capacity, stable
output before and after eviction, a hard size bound, race-clean concurrent
lookup/render/eviction, no lock-map orphan, and updated steady-frame/churn
benchmarks. Rollback restores only the renderer-pool owner; it changes no
canonical Markdown, transcript, or persistent state.

## Repair Contract: G27 Result-Bound Recent Commands

### Identity and ordering

Palette snapshot validation remains an early UX check, not successful
admission. Remove `RecordRecent` from the selection branch. Keep this a TUI
presentation repair rather than extending the durable engine event schema.

For engine-owned commands, bind a pending palette selection to the existing
monotonic `App.queryID` that `startEngineRequest` allocates synchronously and
that `engineEventMsg`/`engineBatchMsg` already carry. Store:

- canonical command name;
- source marked as palette;
- the exact live TUI `queryID`.

The Update loop already rejects events whose `queryID` is stale. On that same
owner goroutine:

```text
matching queryID + canonical command + CommandResultSucceeded
  -> apply the command action successfully
  -> RecordRecent(canonical name) exactly once
  -> clear pending identity

matching Failed / Unsupported / cancelled / terminal-without-result
  -> clear pending identity without recording
```

A manually typed command has no palette provenance and cannot match merely
because its text is equal. A newer `queryID` supersedes the older pending
selection; a duplicate or replayed result finds no live pending identity after
the first commit. Runtime replay and async-hook projections never synthesize a
pending palette record. `CommandResultEvent`, its runtime causation identity,
and ACP/plain/headless schemas remain unchanged because Recent is process-local
TUI state.

For TUI-local commands, use a separate App-local monotonically increasing
submission sequence or an equivalent synchronous outcome token. Record only
after local dispatch and action application return success; no engine
`queryID` is invented for that path.

### Acceptance

Test palette selection followed by missing engine, capability loss between
snapshot and dispatch, strict-dispatch rejection, action-application failure,
cancellation, terminal without result, duplicate result delivery, batched and
single-event delivery, two consecutive same-name commands with different
`queryID` values, successful local and engine execution, replay without a live
pending record, and a same-text manual submission. Only the matching
successful palette submission appears once in Recent. No command registry,
durable event schema, command semantics, persistence, or cross-entrypoint
discovery behavior changes.

## Repair Contract: G3 And G4 Extension Admission

These two gaps share one broad user outcome—standalone extension loading must
fail closed—but require separate implementation PRs, state owners, adoption
decisions, and rollback boundaries. This audit grouping does not create a
shared program. Root `PLAN.md` may select either gap independently; P28 selects
only G3 under `adapt`, while G4 remains unaccepted.

### G3 typed standalone MCP policy

Parse `MCP_PERMISSION_MODE` once during server startup into a closed enum:

| Input | Result |
|---|---|
| empty | current compatibility default `open` |
| `open` | typed open policy |
| `strict` | typed strict policy |
| any other non-empty value | startup error with the invalid value redacted or safely quoted |

Pass the typed policy to tool execution; do not re-read or compare the raw
environment string per call. Tests cover empty, both canonical values,
misspellings, case variants, surrounding whitespace, read-only tools, write
tools, and startup diagnostics. This adapts Codex's typed configuration
boundary without importing its server architecture.

### G4 authority-bound plugin files

Resolve the plugin authority root and every prompt/skill candidate through the
filesystem before containment. Reject a candidate if resolution fails, crosses
the resolved root, traverses a broken link, or changes identity between
validation and use. For managed installs, prefer rejecting links or copying
dereferenced regular content beneath the managed root. If future untrusted
marketplace sources require adversarial guarantees, use a root-anchored open
mechanism rather than a check-then-open path.

Tests cover a symlinked file, symlinked directory, root symlink, broken link,
replacement after validation, normal nested content, and supported Windows
path semantics. This adapts Codex's authority-bound resource rule. Claude Code
Ripe and OpenCode examples that follow symlinks are evidence of a different
trusted-content model, not the target for an untrusted plugin boundary.

## Repair Contract: G28 Execution Containment

Permission answers “may this action run?”; a sandbox answers “what can the
allowed process affect?”. Preserve that separation.

Resolve one immutable engine-scoped `ExecutionPolicySnapshot` at the process
composition root from explicit sandbox configuration, supported-entrypoint
capability, and a platform-adapter probe. QueryEngine carries that snapshot to
Bash/process tools; it does not derive or upgrade it from permission mode,
classifier result, allow rule, or bypass confirmation. The resulting
project-owned `ExecutionEnvelope` has these profiles:

| Profile | Filesystem | Network | Process/resources | Intended use |
|---|---|---|---|---|
| read-only | repository and declared readable roots; no writes | denied by default | bounded descendants, time, memory, descriptors, output | inspection and evaluation |
| workspace-write | writes only in explicit workspace roots and approved temporary area | denied by default, capability opt-in | same bounds | normal coding work |
| danger-full-access | ambient host authority with an explicit visible warning | current host behavior | timeout/process-group cleanup retained | user-authorized exceptional work |

`workspace-write` is the semantic default on a platform that can enforce it.
`read-only` is an explicit downgrade for inspection/evaluation. Elevation to
`danger-full-access` requires explicit user/runtime configuration scoped to the
process or session and a visible diagnostic; permission
`allow`/`auto`/`bypass` never performs that elevation. If the requested profile
is unavailable, autonomous or non-interactive execution fails closed. An
interactive user may explicitly choose danger-full-access, but the runtime
must not silently degrade.

The interface reports supported, degraded, or unavailable containment for the
current OS. Secrets, SSH agents, cloud metadata, and inherited credential
sockets require an explicit projection policy. Network capability is
independent from filesystem write capability.

Implement platform adapters and conformance tests independently; do not place
OS process semantics in permission predicates. Acceptance includes escape
attempts, symlink/mount boundaries, descendant cleanup, cancellation, timeout,
resource exhaustion, network denial, credential non-projection, supported
CLI/TUI/ACP/child entrypoints, a complete permission-mode × envelope-profile
orthogonality matrix, proof that bypass never upgrades host authority, and
truthful degraded-mode diagnostics. Rollback must restore the prior
unsandboxed execution adapter only through explicit danger-full-access
configuration with a compatibility warning.

## Repair Contract: G29 Real-Repository Evaluation

Create a versioned, opt-in evaluation harness outside production dispatch. It
must execute isolated disposable repositories through supported public
entrypoints and grade observable outcomes, not internal call counts.

Initial deterministic scenario classes:

- localized bug fix with hidden regression tests;
- multi-file API refactor with compile and behavior gates;
- interrupted turn followed by process-restart recovery;
- permission denial and sandbox escape attempts;
- MCP tool addition/removal between safe lifecycle boundaries;
- child-agent delegation with cancellation and result handoff;
- long-context compaction followed by an evidence-dependent edit.

Each scenario freezes repository input, task specification, allowed
capabilities, time/token/tool budgets, deterministic local dependencies, and a
grader. Record at least task success, test delta, policy violations, retries,
tool failures, wall time, model tokens/cost where available, recovery success,
and residual workspace changes. Separate deterministic correctness gates from
provider/model trend measurements.

The harness must not become a second QueryEngine or production scheduler. It
drives existing entrypoints, stores redacted artifacts, and publishes a
versioned schema plus reproducible local command. Baseline the current runtime
before using results as a promotion gate. Broad Eino owner replacement, model
review enforcement, or autonomous Goal expansion should state the evaluation
delta rather than rely only on package tests.

## Later Contract: G5 Live MCP Generation

After the immediate correctness and measurement foundations, G5 can be
promoted independently. One registry-generation owner should atomically replace
the model-visible MCP snapshot only at a documented safe turn boundary.
In-flight calls retain the generation they admitted against; disconnect removes
the server from the next safe snapshot; CLI, TUI, ACP, leader, and child
projections receive equivalent precedence and diagnostics. Eino-ext may supply
transport or discovery mechanics, but cannot become a second tool registry or
permission owner.

## Recommended Promotion Sequence

This is an intake recommendation, not executable order:

1. close P22.H0, the sole current `Ready` slice;
2. select G3 and G4 as two small fail-closed extension-safety PRs;
3. select G24, G25, G26, and G27 one at a time by their existing state owners;
4. baseline G29 before broad runtime or autonomous-lifecycle changes;
5. introduce G28 through platform-specific containment adapters and truthful
   capability reporting;
6. consider G5 live MCP generations, G1/G2 durable recovery, queued P24 Goal
   continuation, and later P22 model-review enforcement using measured product
   outcomes and the root PLAN selection policy.

Security-critical evidence may justify moving one atomic repair earlier. It
does not justify combining owners, bypassing acceptance, or placing multiple
`Ready` rows in the queue.

## Slice Template

When any gap is promoted, its plan must record:

1. user-visible failure and supported entrypoints;
2. current owner and exact state transition;
3. adoption decision and compatibility consequence;
4. allowed production/test/documentation paths;
5. ordering, permission, cancellation, persistence, recovery, and concurrency
   invariants;
6. focused negative, race, PTY, performance, or platform tests as applicable;
7. rollback boundary and data/schema consequence;
8. source-backed closure evidence and all required repository gates.

No slice may claim completion from compilation, a happy path, a historical
closeout, or reference resemblance alone.

## Exclusions

This audit deliberately does not:

- change runtime code, assign a P25 program, or promote a second `Ready` row;
- reopen P20's QueryEngine authorization settlement or G11's delivered
  follow-state owner;
- treat design guidance as an implemented sandbox, evaluation suite, live MCP
  cutover, or Eino replacement;
- bundle model-review enforcement with deterministic P22.H0 containment;
- import Codex, Claude, or OpenCode architecture by identity; or
- claim CI, runtime, terminal, race, or performance gates that this
  documentation-only iteration did not execute.

## Source Anchors

| Boundary | Current source |
|---|---|
| Bash auto-allow | [`engine/permission/accept_edits.go`](../../../../engine/permission/accept_edits.go), [`engine/engine.go`](../../../../engine/engine.go) |
| Plan confirmation state and geometry | [`internal/tui/plan_dialog.go`](../../../../internal/tui/plan_dialog.go), [`internal/tui/plan_dialog_state_test.go`](../../../../internal/tui/plan_dialog_state_test.go) |
| Profile-fixed content geometry | [`internal/tui/content_geometry.go`](../../../../internal/tui/content_geometry.go), [`internal/tui/display_cell_g11f1_test.go`](../../../../internal/tui/display_cell_g11f1_test.go) |
| Markdown renderer cache | [`internal/tui/markdown.go`](../../../../internal/tui/markdown.go), [`internal/tui/app.go`](../../../../internal/tui/app.go) |
| Command Recent and result path | [`internal/tui/dialog_stack.go`](../../../../internal/tui/dialog_stack.go), [`internal/tui/app.go`](../../../../internal/tui/app.go), [`engine/command_executor.go`](../../../../engine/command_executor.go), [`engine/events.go`](../../../../engine/events.go) |
| Standalone MCP policy | [`server/mcp/server.go`](../../../../server/mcp/server.go) |
| Plugin path authority | [`engine/plugins/loader.go`](../../../../engine/plugins/loader.go), [`engine/plugins/manager.go`](../../../../engine/plugins/manager.go) |
| Shell process boundary | [`tools/bash.go`](../../../../tools/bash.go), [`tools/bash_shell.go`](../../../../tools/bash_shell.go) |
| Kernel trace fixtures | [`engine/canonical_trace_test.go`](../../../../engine/canonical_trace_test.go), [`engine/canonical_trace_fixture_test.go`](../../../../engine/canonical_trace_fixture_test.go) |
