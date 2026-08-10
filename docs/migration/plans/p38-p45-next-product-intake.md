# P38-P45 Next Product Intake

**Status:** active-plan
**Accepted:** 2026-08-01
**Adoption:** mixed; each program records its own decision

> **Ownership:** accepted contracts, queue order, promotion gates, and rollback
> boundaries for G2, G12, G14, G21, G25-G26, G28-G29, and G34. Root
> [`PLAN.md`](../PLAN.md) alone owns the executable row. Current behavior stays
> in [`REMAINING.md`](../REMAINING.md) and the linked architecture documents.
> This intake changes no runtime behavior.

## Outcome

All nine reproduced gaps received an explicit intake decision. Seven gaps
entered accepted P38-P43 programs. P38.0 is complete and closed G34, P40.1 is
complete and closed G12, P41.1-P41.2 are complete and closed G25-G26, and
P42.0 completed its behavior-preserving identity seam while G28 remains open.
P43.0 completed its reusable opt-in baseline and closed G29 after a separate
disposable-repository promotion. P39.0 completed its test-backed recovery
characterization contract after its own disposable-workspace promotion; the
closeout does not close G2 or accept a writer. No slice became ready
automatically from another closeout.
G14 and G21 remain reproduced, but P44 and P45
record `defer` because their previous evidence
gates are still unsatisfied. Intake does not turn missing evidence into
observed-zero risk.

| Program | Gap | Decision | First slice | State |
|---|---|---|---|---|
| P38 | G34 provider-bound reasoning continuation | `adapt` | P38.0 exact origin sidecar, route recheck, and conservative stripping | `Complete` |
| P39 | G2 workspace rewind | `project-native` | P39.0 recovery-contract characterization | `Complete`; G2 open |
| P40 | G12 startup theme polarity | `adapt` | P40.1 polarity-preserving startup resolution | `Complete` |
| P41 | G25 geometry owner; G26 renderer-cache lifetime | `project-native` | P41.1 geometry; P41.2 bounded pool | `Complete`; G25-G26 closed |
| P42 | G28 host execution containment | `project-native` | P42.0 immutable policy and disabled-adapter seam | `Complete`; G28 open |
| P43 | G29 real-repository evaluation | `combine` | P43.0 isolated baseline harness | `Complete`; G29 closed |
| P44 | G14 reviewer enforcement | `defer` | no successor accepted | no queue row |
| P45 | G21 Goal default promotion | `defer` | no successor accepted | no queue row |

P38.0, P39.0, P40.1, P41.1, P41.2, P42.0, and P43.0 are complete. No slice is
`Ready`. Risk, not program number, owned the completed order: the rewind
contract had the widest durability and recovery surface and completed only
after its own isolated evidence and contract closeout.

## Shared Admission Rules

Every program preserves these boundaries:

1. One slice owns one observable contract and one rollback boundary.
2. Permission approval is not OS containment. Provider payload shape is not
   proof of private-data origin. A process-local cache is not durable recovery.
3. Durable state, authorization, cancellation, replay, and external side
   effects must fail closed when their required identity is absent or drifts.
4. A characterization, benchmark, or shadow report is non-authoritative until
   root PLAN separately promotes the behavior it measures.
5. P41.1 and P41.2 may share fixtures, but geometry correctness and cache
   lifetime remain separate changes and separate rollback boundaries.
6. G14 and G21 may re-enter only through their existing non-zero evidence
   gates. No new telemetry store is accepted solely to manufacture a promotion
   denominator.

## P38: Provider-Bound Reasoning Continuation

### User problem and current owner

A restored Agentic OpenAI conversation keeps durable reasoning/signature data,
but `messagesToAgentic` reconstructs the assistant block without the downstream
self-generated classification needed by the same provider route. The next
Responses request therefore retains public history but strips private
continuation material. Retaining it unconditionally would create a more serious
cross-provider, model, or credential disclosure risk.

The provider runtime, route identity, canonical model-round aggregation, and
Agentic conversion boundary are the only accepted owners. P36's ACP public
assistant replay remains independent and must never expose reasoning text,
signatures, encrypted continuation tokens, or provider metadata.

### Decision and P38.0 scope

P38 uses `adapt`: preserve the downstream adapter's self-generated safety
rule, but supply project-owned durable origin evidence rather than an inferred
marker. P38.0 delivered the detailed contract, private sidecar, exact route
proof, and conservative production reuse boundary as one rollback slice.

P38.0 defines:

- the provider account, API family, API model, and credential-origin identity
  required for private continuation reuse;
- how stream aggregation, transcript restore, manual model changes, bounded
  failover, and recovery carry or invalidate that identity;
- the exact conversion point that converts verified same-origin history into
  a downstream self-generated message; and
- the redacted diagnostic used when identity is absent, stale, or different.

No payload-shape heuristic, provider-name-only comparison, or public transcript
field may grant private continuation reuse.

### Historical promotion, delivery, and rollback

P38.0 was promoted after the focused characterization fixture and detailed
contract proved all of the following without enabling production reuse:

- exact same provider/account/API-family/API-model/credential origin is the
  sole positive path;
- any manual switch, fallback route, credential change, missing legacy origin,
  or recovery mismatch strips private material before transport;
- public text and tool history remain byte-for-byte compatible; and
- ACP load/replay still contains no private provider material.

The completed implementation adds Generate and Stream parity, restart and
failover cases, redacted diagnostics, account-scoped publication race coverage,
and ACP/export exclusion. Delivery evidence is
[`p38-0-provider-reasoning-origin.md`](../history/runtime/p38-0-provider-reasoning-origin.md).
Rollback restores conservative stripping; the additive origin schema and its
public-exclusion rules remain frozen in the detailed contract.

Evidence: [provider architecture](../../architecture/platform/model-providers.md),
[detailed P38 contract](p38-provider-reasoning-origin.md),
[comparative origin audit](../reference/runtime/provider-reasoning-origin-audit.md),
[P36 contract](p36-acp-assistant-replay.md), and
[P36 closeout](../history/runtime/p36-1-acp-rich-assistant-replay.md).

## P39: Workspace Recovery And Rewind

### User problem and current owner

`/rewind` is a non-executable tombstone. The current interaction cache and the
process-local `filehistory.FileTracker` cannot safely restore file contents:
production does not construct the tracker, and the cache lacks authoritative
content bytes, digest, mode, external-modification identity, and partial-failure
state.

P39 uses `project-native`. The future owner must be a durable workspace
recovery service bound to the Session/turn identity and the exact workspace
root. It must not revive the disconnected tracker or overload transcript
interaction-cache persistence with content rollback authority.

### P39.0 scope

P39.0 is characterization and contract only. It freezes:

- snapshot identity and the file-content/mode authority captured before an
  accepted mutation;
- root containment, symlink and external-edit conflict detection;
- user confirmation and permission boundaries for preview and apply;
- deterministic order, idempotence, cancellation, partial rollback, crash,
  retry, and recovery semantics; and
- supported TUI, Plain, headless, ACP, child, and standalone-MCP behavior.

No `/rewind` handler, durable schema, automatic destructive restoration, or
reference-derived history format is accepted by P39.0.

### Promotion, tests, and rollback

P39.0 becomes `Ready` only when an isolated workspace matrix proves the target
contract can detect external edits without overwriting them, cannot escape the
saved root, cannot bypass confirmation or permission policy, and can report a
partially applied rollback precisely enough for safe retry. The implementation
plan must name schema/version migration and deletion ownership before any
writer is accepted.

#### P39.0 promotion freeze

The disposable-workspace matrix now satisfies this gate and promotes P39.0 as
the sole `Ready` slice. The versioned test-only reference state binds snapshot
ID, Session, turn, canonical root identity, and exact before/after path bytes,
digest, existence, and mode. It restores only when the current path still
matches the recorded post-mutation state, rejects external edits, symlinks, and
root replacement without overwriting them, leaves the Git index unchanged,
and reports deterministic per-path completion precisely enough to resume after
serialization, cancellation, or partial failure. Confirmation and permission
both precede mutation.

Root TUI, Plain, headless, and ACP sessions share the future contract. Child
agents and standalone MCP remain unsupported and receive no root-session
rollback authority. The evidence is intentionally test-only: it does not add a
durable schema, filesystem writer, preview/apply path, or `/rewind` handler.
The subsequent P39.0 closeout froze the final project-owned contract and
retained the matrix as its conformance oracle without accepting automatic
restoration or a reference-derived history format. See the
[promotion evidence](../verification/p39-0-workspace-recovery-promotion.md).

#### P39.0 closeout

P39.0 is complete. The final
[workspace recovery contract](p39-workspace-recovery-contract.md) freezes
complete-capture admission, logical snapshot/operation v1 records, conflict and
containment rules, preview-confirmation-permission separation, deterministic
ordered apply and crash retry, entrypoint behavior, explicit schema/migration/
retention/deletion gates, non-goals, and rollback. The merged matrix remains
its conformance oracle.

No production record, content store, writer, preview/apply API, command, or
automatic restoration exists. G2 remains reproduced and `/rewind` remains a
non-executable tombstone. Any writer or user-visible recovery surface requires
a new accepted slice; P39.0 closeout promotes none.

Rollback keeps `/rewind` unavailable and preserves current Git/workspace
recovery guidance. Evidence:
[file-state recovery audit](../reference/runtime/file-state-checkpoint-recovery-audit.md)
and [command architecture](../../architecture/capabilities/commands.md).

## P40: Startup Theme Polarity

### User problem and decision

Before P40.1, startup configuration accepted arbitrary theme text while the
resolver accepted only canonical IDs and `dark`/`light`. A known
light-oriented compatibility value such as `light-daltonized` was silently
ignored; a truecolor terminal then fell back to dark Polar Night. The user's
explicit polarity was reversed with no diagnostic.

P40 uses `adapt`: canonical project themes remain authoritative, while an
explicit allowlist maps verified external compatibility names to the closest
canonical theme without claiming palette parity. Unknown names fail visibly
through a typed startup diagnostic and then use the existing deterministic
capability fallback. Generic prefix inference such as `strings.HasPrefix`
is forbidden.

### P40.1 completed slice

P40.1 changed startup theme normalization/resolution, configuration issue
projection, focused TUI tests, and current theme documentation. It:

1. preserved environment-over-config precedence and explicit runtime `/theme`
   ownership;
2. preserved the light/dark polarity of every allowlisted compatibility name;
3. surfaced an unknown configured value before applying the existing terminal
   capability fallback;
4. left canonical themes and their palettes unchanged; and
5. avoided renderer, viewport, geometry, persistence, and engine changes.

Focused evidence covers env and config provenance, canonical/legacy/
compatibility/unknown names, truecolor/ANSI capability snapshots,
startup-to-App issue delivery, bounded control-byte-safe diagnostics, and
explicit runtime theme selection. Known light and dark aliases retain their
polarity, while prefix-like unknown values remain invalid. Rollback removes
only the compatibility allowlist and diagnostic seam; canonical IDs, palettes,
and persisted configuration remain compatible.

Evidence: [TUI architecture](../../architecture/tui/README.md) and current
`ResolveThemeForCapabilities` fixtures in `internal/tui`. Delivery and final
gate evidence is in
[`p40-1-startup-theme-polarity.md`](../history/tui/p40-1-startup-theme-polarity.md).

## P41: TUI Geometry And Renderer Lifetime

P41 uses `project-native`. The App-selected `DisplayCellProfile` and its
immutable render-environment generation remain the single target authority.
Two gaps share this environment but are intentionally separate slices.

### P41.1 fixed-size geometry owner

At intake, the fixed-size adapter expanded tabs through `DisplayCellProfile`
and then asked Lip Gloss for width and height. P41.1 later removed that second
geometry owner after differential fixtures reproduced or excluded Unicode,
border, wrapping, and hit-target drift.

Promotion requires one target profile-owned width/height projection, golden and
differential fixtures for tabs, combining marks, emoji, ambiguous-width cells,
borders, wraps, and PTY resize, plus proof that selection and hit testing use
the same published frame. Rollback restores the existing adapter seam without
changing profile identity or persisted state.

#### P41.1 promotion freeze

**Snapshot:** `bc7ce07e127dfbb03924ad9da18212a2117d288f`

**Status:** completed on 2026-08-02

Portable fixtures now reproduce two current differences: tab expansion uses
origin 0 even when border and padding place content at column 2, and an Indic
conjunct rendered through asymmetric padding measures one selected-profile cell
wider than the requested body. The same differential matrix records named
combining, ZWJ emoji, wrapping, SGR, and OSC8 non-drift cases so the promotion
does not claim every Unicode input fails.

Root PLAN accepted one implementation PR. It preserves the current body-width
plus border convention, introduces one profile-owned fixed-box projection that
returns rendered rows and inner/outer geometry together, keeps tabs semantic
until their actual aligned origin is known, makes every fixed row exact under
the selected profile, connects selection/pointer publishers to that same frame,
and removes the two G11.F1 Lip Gloss fixed-size exceptions only after all
callers migrate. P41.2 remains unchanged and separately rollbackable.

The exact algorithm, compatibility consequence for an impossible over-width
cluster, proof matrix, source owners, and rollback are in
[`p41-1-fixed-size-geometry-owner.md`](p41-1-fixed-size-geometry-owner.md).
Reproduction commands and evidence limits are in
[`p41-1-fixed-size-geometry-promotion.md`](../verification/p41-1-fixed-size-geometry-promotion.md).
Completion evidence is in
[`p41-1-fixed-size-geometry-owner.md`](../history/tui/p41-1-fixed-size-geometry-owner.md).

### P41.2 bounded Markdown renderer pool

At the promotion snapshot, Markdown renderer and renderer-lock maps keyed
entries by theme and geometry generation but never evicted them. P41.2 now
provides one App-owned bounded pool with race-safe creation and eviction. It
does not change Markdown semantics, theme identity, geometry calculation, or
the P41.1 owner.

Promotion requires a measured bound and eviction policy, render equivalence,
theme/resize churn tests, cache-cardinality or memory evidence, and race
coverage showing an in-use renderer/lock cannot be invalidated. Rollback
restores the existing cache implementation; no durable state changes.

#### P41.2 promotion freeze

**Snapshot:** `5d13285719ea2f73d7b8c6e1d5fe2e1f264e7165`

**Status:** completed on 2026-08-02

The promotion fixture drives 64 geometry generations at one width through both
normal and selection rendering. Current source retains 128 renderers and 128
separate locks. This exceeds the selected 32-entry target and deterministically
reproduces G26 without relying on process memory sampling. The diagnostic
construction benchmark and its evidence boundary are recorded in
[`p41-2-renderer-pool-promotion.md`](../verification/p41-2-renderer-pool-promotion.md).

Root PLAN accepts the following implementation contract under the existing
`project-native` decision:

1. `App` constructs one private `markdownRendererPool` with a hard capacity of
   32 and passes that exact owner through `RenderEnvironment`. Style and
   geometry updates retain the same pool pointer. Compatibility constructors
   may create their own private 32-entry pool; no process-global fallback is
   allowed.
2. One `markdownRendererEntry` atomically owns the exact current `rendererKey`,
   its `TermRenderer`, and its serialization mutex. The key retains width,
   semantic theme, color profile, display-cell profile identity, theme and
   geometry generations, and selection mode.
3. Lookup and insertion are serialized by the pool mutex. Each successful
   lookup advances a monotonic access sequence. Inserting past capacity removes
   the least-recently-used indexed entry; rendering never holds the pool mutex.
4. Eviction removes only lookup ownership. A caller that already holds the
   entry pointer may lock it and finish the entire render sequence. An evicted
   renderer or mutex is never reset, destroyed, reused for another key, or
   recovered through a second lock map.
5. Renderer construction failure is not cached and retains the current safe
   literal fallback. Pool size, creation, and eviction counters are private
   test evidence, not diagnostics, configuration, runtime state, or durable
   data.

Capacity 32 is a correctness-independent retention bound: the characterization
workload retains the most recent 16 two-mode generations, while any additional
widths may evict older entries without changing output. Performance tuning may
change the capacity only in a later measured slice; it may not weaken the hard
bound or exact key.

Implementation acceptance requires:

- existing streaming, finalized, table, theme, ANSI, and selection goldens to
  remain byte-equivalent;
- deterministic LRU order, exact key separation, hard `size <= 32`, and exact
  creation/eviction counters;
- theme and resize churn beyond capacity followed by fresh-render equivalence;
- a barrier-controlled test that evicts a locked/in-use entry, lets the old
  holder finish, and proves a later lookup receives a new indexed entry;
- concurrent lookup, render, and forced eviction to pass `go test -race`; and
- steady-hit and churn benchmarks to report time, allocations, peak indexed
  size, creations, and evictions without converting machine-specific numbers
  into a portable product budget.

The completed slice changes no Markdown semantics, render-environment identity,
display-cell geometry, P41.1 owner, transcript, Session, permission, or
persistence contract. Rollback may restore the current dual global maps because
there is no schema consequence, but that rollback reopens G26 and is not an
acceptable steady state.

P41.1 and P41.2 remain separate behavior changes. P41.2 completion evidence is
[`p41-2-bounded-markdown-renderer-pool.md`](../history/tui/p41-2-bounded-markdown-renderer-pool.md).
P41.1 completion evidence is
[`p41-1-fixed-size-geometry-owner.md`](../history/tui/p41-1-fixed-size-geometry-owner.md).
Contract evidence:
[G25 repair contract](../reference/runtime/recent-delivery-remediation-audit.md#repair-contract-g25-one-fixed-size-geometry-owner)
and
[G26 repair contract](../reference/runtime/recent-delivery-remediation-audit.md#repair-contract-g26-bounded-markdown-renderer-pool).

## P42: Host Execution Containment

### User problem and decision

An allowed Bash action inherits the host process's ambient filesystem, network,
credential, syscall, and resource authority. CWD containment, timeout, pipes,
and process-group cancellation are not an OS sandbox. P42 uses
`project-native` because containment must fit supported Go targets and every
project entrypoint instead of copying one reference runtime.

### P42.0 scope

P42.0 freezes a behavior-preserving execution-envelope contract before any
sandbox implementation. One immutable snapshot resolved at the composition
root must own:

- filesystem read/write roots, network policy, resource limits, credential and
  environment projection, process descendants, and cleanup;
- platform capability detection and truthful degraded/unavailable behavior;
- CLI/TUI, Plain, headless Goal, ACP, child/Agent, and standalone-MCP coverage;
- fail-closed selection when a required profile cannot be enforced; and
- redacted diagnostics and an explicit operator-controlled disabled mode.

Permission allow, exact grants, `auto`, or any future bypass decides whether an
action may run; it can never choose, relax, or elevate the containment envelope.
P42.0 adds no sandbox and makes no supported-platform claim.

### Promotion, tests, and rollback

P42.0 completed under the detailed
[host-execution containment contract](p42-host-execution-containment.md). The
contract names per-platform primitive families, unsupported/degraded matrices,
immutable policy identity, entrypoint wiring, process classes, and
deterministic escape tests. P42.0 adds only the behavior-preserving immutable
identity and disabled-adapter seam. Completion evidence is
[`p42-0-execution-policy-snapshot.md`](../history/runtime/p42-0-execution-policy-snapshot.md).
Later enforcement needs filesystem,
network, environment/credential, resource, cancellation, descendant, startup,
and recovery coverage on every claimed platform. Rollback may return to an
explicitly reported disabled-containment mode; it must not describe permission
prompts as sandboxing.

Evidence:
[G28 repair contract](../reference/runtime/recent-delivery-remediation-audit.md#repair-contract-g28-execution-containment)
[permission architecture](../../architecture/capabilities/permissions.md), and
[comparative audit](../reference/runtime/host-execution-containment-audit.md).

## P43: Real-Repository Evaluation

### User problem and decision

Canonical traces prove kernel compatibility but do not grade whether the agent
successfully changes a repository, violates policy, recovers after failure,
stays within cost/latency budgets, or leaves residual workspace damage. P43
uses `combine`: retain canonical traces for protocol compatibility and add a
project-owned isolated product-outcome harness.

### P43.0 scope

P43.0 establishes a non-authoritative baseline only. It may add versioned
isolated repository fixtures, deterministic task and policy graders, bounded
capability budgets, redacted reports, and a local/CI invocation. It must not
change runtime selection, model routing, permission policy, default Goal
behavior, or release gates.

Each fixture must pin repository state and public entrypoint, isolate network,
credentials, and host workspace, and grade task outcome, policy violations,
recovery, provider usage/cost inputs, latency, and residual filesystem state.
Artifacts must be bounded and redacted; raw repository, prompt, credential, or
provider-private data cannot escape the isolated run.

### Promotion, tests, and rollback

P43.0 becomes `Ready` when one representative fixture can replay from a clean
snapshot, produce the same deterministic outcome grade, prove artifact
redaction and isolation, and report unsupported entrypoints honestly. A
separate root-PLAN decision is required before any score becomes a promotion
or release gate. This harness cannot substitute for the representative,
privacy-reviewed P22 or P24 evidence denominators. Rollback deletes only the
harness and its generated artifacts.

#### P43.0 promotion freeze

**Snapshot:** `03faac2575a0de6e17c54d1c310cfd4eba081649`

**Promotion status:** satisfied on 2026-08-02

The promotion fixture replays one localized missing-implementation task twice
through the public `exec --output-format json` path. A local scripted provider
first requests a root-escape Write that the headless fallback denies, then one
contained Write that `acceptEdits` admits. Public and hidden graders, outside
sentinel, residual tree, bounded budgets, usage coverage, and report redaction
produce one byte-stable canonical result from fresh Git snapshots.

The proof deliberately exposes no Read, Bash, WebFetch, MCP, Agent, hook, or
process tool. It reports OS process/network containment, recovery, and TUI,
Plain, ACP, and standalone-MCP coverage as not evaluated. The delivered P43.0
implementation is one standalone command under `scripts/evaluation`, one
versioned fixture/report schema, deterministic failure lifecycle, and one
opt-in local invocation outside required CI and release gates. The exact
contract is
[`p43-real-repository-evaluation.md`](p43-real-repository-evaluation.md), and
the reproduction is
[`p43-0-real-repository-evaluation-promotion.md`](../verification/p43-0-real-repository-evaluation-promotion.md).
Delivery evidence is
[`p43-0-real-repository-evaluation.md`](../history/runtime/p43-0-real-repository-evaluation.md).

Evidence:
[G29 repair contract](../reference/runtime/recent-delivery-remediation-audit.md#repair-contract-g29-real-repository-evaluation).

## P44: Permission Reviewer Re-entry

G14 remains reproduced. P22.H0-P22.2b provide deterministic safety, exact
authority, advisory shadow review, and bounded redacted reporting, but the
current provider-free report has no eligible, attempt, terminal, latency,
direct-human, or versioned-corpus denominator. P44 therefore records `defer`;
it accepts no enforcement, capability expansion, default promotion, or legacy
classifier deletion slice.

Re-entry requires non-zero representative retained evidence, privacy and
retention review, direct-human and versioned-corpus comparison, latency/error
budgets, independent security review, and rollback rehearsal. Missing data is
not zero. No measurement-only store or synthetic traffic is accepted solely
to clear this gate. The complete gate is
[`p22-enforcement-promotion-readiness.md`](../verification/p22-enforcement-promotion-readiness.md).

## P45: Goal Default-Promotion Re-entry

G21 remains reproduced. The durable Goal runtime is complete and intentionally
opt-in, but there is no representative usage, monetary-cost,
continuation-latency, independent lifecycle/security review, or rollback
rehearsal evidence for default-on behavior. P45 records `defer`; it accepts no
default budget, default enablement, entrypoint expansion, or telemetry-only
slice.

Re-entry requires an independently verified user outcome, representative
privacy-reviewed non-zero sessions, a named price/cost metric, latency and
safety budgets, independent lifecycle/security review, and rollback rehearsal.
Until then the existing opt-in switch, explicit positive budget, and kill
switch remain authoritative. The complete gate is
[`p24-6-default-promotion-readiness.md`](../verification/p24-6-default-promotion-readiness.md).

## Documentation And Closeout

Each implementation slice must update its current architecture owner, retain
its gap in `REMAINING.md` until the observable mismatch closes, record delivery
evidence under `history/`, and return root PLAN to exactly one `Ready` row or
intake. A program number proves only acceptance; it does not prove current
behavior or completion.
