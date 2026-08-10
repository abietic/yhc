# Migration Verification Index

**Status:** verification
**Last verified:** 2026-08-08

> **Ownership:** this file indexes reproducible verification procedures and
> gates. It does not own current status, active order, or backlog. Verified
> current facts belong in [`migration/STATUS.md`](../STATUS.md); accepted future work
> belongs in [`migration/PLAN.md`](../PLAN.md); unresolved gaps belong in
> [`migration/REMAINING.md`](../REMAINING.md).

## Verification artifacts

| Document | Purpose |
|---|---|
| [`tui-parity-harness.md`](tui-parity-harness.md) | Four-project TUI parity harness: real PTY execution, VT emulation, normalized captures, and pairwise structural diffs. |
| [`tui-performance-baseline.md`](tui-performance-baseline.md) | Diagnostic benchmark baselines and asserted p95 product budgets for TUI hot paths. |
| [`terminal-output-resilience.md`](terminal-output-resilience.md) | Deterministic blocked-writer, bounded cleanup, late-frame, Unix PTY, cross-platform, performance, and runtime-state evidence for completed P15. |
| [`p19-dirty-worktree-recovery.md`](p19-dirty-worktree-recovery.md) | Exhaustive 83/86-file checkpoint disposition, reset root cause, recovered behavior contracts, rejected stale code, and regression evidence. |
| [`g11-a-frame-integrity-characterization.md`](g11-a-frame-integrity-characterization.md) | Independent deterministic cell oracle, semantic-table lifecycle, cross-run grapheme, historical wide-frame evidence, the superseded pre-G11.B follow characterization, and G11.B/D1/D2/D3 replacement commands. |
| [`g11-f2-terminal-program-closeout.md`](g11-f2-terminal-program-closeout.md) | Real-program PTY lifecycle matrix, physical-grid claim boundary, structural steady-frame proof, portable budgets, and diagnostic benchmarks closing G11. |
| [`p22-enforcement-promotion-readiness.md`](p22-enforcement-promotion-readiness.md) | P22 readiness evidence proving why an empty retained report cannot authorize shell eligibility, reviewer enforcement, capability expansion, default promotion, or old-owner deletion; this is the evidence input to the owning defer decision. |
| [`p24-6-default-promotion-readiness.md`](p24-6-default-promotion-readiness.md) | P24.6 readiness audit proving why deterministic opt-in safety evidence and current Goal usage records cannot justify default enablement or a numeric budget; this is the evidence input to the owning defer decision. |
| [`p30-0-multimodal-characterization.md`](p30-0-multimodal-characterization.md) | P30.0 current TUI/ACP/engine/transcript/recovery mismatch fixtures and complete writer/reader/queue/branch/delete/export/provider owner inventory. |
| [`p29-0-route-characterization.md`](p29-0-route-characterization.md) | P29.0 current config/resolution/cache limitation, target-only route identity, secret sentinel, six-provider construction, concurrency, and CLI/ACP composition fixtures. |
| [`p29-1-trusted-portfolio-compiler.md`](p29-1-trusted-portfolio-compiler.md) | P29.1 source authority, strict portfolio compilation, named credentials, model-metadata provenance, route-identity caching, six-adapter construction, CLI/ACP startup, race, and redaction evidence. |
| [`p29-2-shared-inventory-model-binding.md`](p29-2-shared-inventory-model-binding.md) | P29.2 detached shared inventory, selector grammar, durable model-control transaction, binding v1, resume/compaction guard, safe Session projections, entrypoint parity, race, and redaction evidence. |
| [`p29-3-capability-admitted-role-routing.md`](p29-3-capability-admitted-role-routing.md) | P29.3 fixed role authority, selected-profile capability admission, child binding/recovery, trusted compatibility, root-only summaries, provider effort lowering, usage identity, source gates, and race evidence. |
| [`p29-4-bounded-overload-failover.md`](p29-4-bounded-overload-failover.md) | P29.4 overload-only logical attempts, shared provider-call/switch/deadline budgets, immutable request replay, entrypoint commitment and exact TUI retraction, usage attribution, retained tool ownership, source gates, and race evidence. |
| [`p29-5-observation-readiness.md`](p29-5-observation-readiness.md) | P29.5 readiness audit proving why current process-local attempt events, cumulative usage, and Goal-only attribution cannot supply the production baselines required for adaptive health; this is the evidence input to the owning defer decision. |
| [`p31-1a-reversible-workboard-shadow.md`](p31-1a-reversible-workboard-shadow.md) | P31.1a reversible WorkBoard comparison shadow, exact legacy compatibility, private bounded persistence, failure isolation, lifecycle exclusion, and rollback evidence. |
| [`p31-1b-authoritative-workboard.md`](p31-1b-authoritative-workboard.md) | P31.1b WorkBoard v2 authority, Task/Todo compatibility, marker-last cutover, strict storage, persistent uncertainty quarantine, Session lifecycle, exact recovery, source gates, and race evidence. |
| [`p31-2-canonical-explorer-snapshot.md`](p31-2-canonical-explorer-snapshot.md) | P31.2 process-local WorkBoard projection, immutable execution generations, cold bootstrap, deterministic bounds/order, explicit fixture links, compatibility adapter, source gates, and race evidence. |
| [`p31-3-read-only-task-explorer.md`](p31-3-read-only-task-explorer.md) | P31.3 selector-backed responsive explorer, exact-generation compatibility fence, display-cell matrix, bounded rendering, PTY lifecycle, source gates, and independent review evidence. |
| [`p32-1-plugin-file-authority.md`](p32-1-plugin-file-authority.md) | P32.1 configured-root/child identity, descriptor-relative manifest/prompt/skill reads, portable paths, link and replacement fencing, atomic rollback, entrypoint exclusions, race, and cross-target evidence. |
| [`p33-1-mcp-live-tool-generation.md`](p33-1-mcp-live-tool-generation.md) | P33.1 exact MCP client/manager generations, complete owned registry publication, refresh/reconnect/close convergence, permission and execution pinning, hook reentry, entrypoint scope, source gates, and race evidence. |
| [`p34-1-file-state-checkpoint-repair.md`](p34-1-file-state-checkpoint-repair.md) | P34.1 incremental file-state error handoff, one complete checkpoint repair, cumulative concurrency, partial-line recovery, terminal truth, restart/resume, source gates, and race evidence. |
| [`p35-1-tui-notification-lifecycle.md`](p35-1-tui-notification-lifecycle.md) | P35.1 bounded latest-three ingress, typed App ownership, deterministic generation-fenced idle expiry, pure rendering, program teardown, entrypoint exclusions, source gates, real-loop proof, and race evidence. |
| [`p41-1-fixed-size-geometry-promotion.md`](p41-1-fixed-size-geometry-promotion.md) | P41.1 promotion-snapshot tab-origin and Indic-width reproductions, named non-drift boundaries, target profile matrix, and fixed-box PTY lifecycle scope. |
| [`p41-2-renderer-pool-promotion.md`](p41-2-renderer-pool-promotion.md) | P41.2 promotion-snapshot unbounded cardinality, capacity-32 decision, and the proof matrix later established by the completed App-owned pool. |
| [`p43-0-real-repository-evaluation-promotion.md`](p43-0-real-repository-evaluation-promotion.md) | P43.0 disposable public-headless repository replay, deterministic outcome/policy/residual grade, bounded redaction, isolation mechanism, and explicit not-evaluated boundaries. |
| [`p39-0-workspace-recovery-promotion.md`](p39-0-workspace-recovery-promotion.md) | P39.0 disposable-workspace recovery matrix for exact pre-turn state, external-edit/root/symlink conflict fencing, confirmation and permission ordering, Git-index isolation, and partial-retry identity. |
| [`p46-1-complete-prompt-footprint.md`](p46-1-complete-prompt-footprint.md) | P46.1 production-path system/tool footprint admission, no-route/no-call smaller-context skip, later-alternate success, P29.4 regression, race, and repository-gate evidence. |
| [`p46-2-observable-failover.md`](p46-2-observable-failover.md) | P46.2 discarded-attempt ordering, exact optional tombstone, runtime replay, TUI/Plain/Headless/ACP/library projection, Unix PTY, race, and repository-gate evidence. |
| [`p48-1-acp-session-root-delete.md`](p48-1-acp-session-root-delete.md) | P48.1 process-local canonical Session-root observations, cross-CWD inactive delete, multi-root no-mutation conflict, exact forget, compatibility, race, contract, and SDK-wire evidence. |
| [`p48-2-acp-plan-tool-identity.md`](p48-2-acp-plan-tool-identity.md) | P48.2 exact engine tool identity across ACP Plan start, choice, Back/bypass rounds, and terminal delivery, plus blank-ID, deadline, failure, race, and SDK-wire evidence. |
| [`p48-3-acp-string-raw-output.md`](p48-3-acp-string-raw-output.md) | P48.3 exact string-valued tool `rawOutput` across independent live and replay SDK-wire paths, including JSON-looking and empty text, plus replay, race, contract, and SDK evidence. |
| [`p48-4-mcp-environment-identity.md`](p48-4-mcp-environment-identity.md) | P48.4 shared Windows-folded and non-Windows-exact environment-key identity across ACP admission, setup fingerprints, and stdio overlay, with original key/value preservation, race, SDK, and cross-compile evidence boundaries. |
| [`p48-5-remove-private-session-migration.md`](p48-5-remove-private-session-migration.md) | P48.5 real-dispatcher MethodNotFound rejection for both former private migration names, unchanged Session/project state, production-surface deletion, retained extension, race, contract, and SDK evidence. |
| [`p49-goal-default-unbudgeted.md`](p49-goal-default-unbudgeted.md) | P49 default-enabled supported roots, optional Goal token limiting, state/continuation compatibility, exact restart admission attribution, entrypoint/PTY/race coverage, and quiesced rollback evidence. |
| [`p50-1-project-graph-revision-fence.md`](p50-1-project-graph-revision-fence.md) | P50.1 post-check/pre-rebuild revision mismatch, mutex/barrier concurrency, persistence failure, public resume cancellation, late-item rejection, exact-rule, repetition, and race evidence. |
| [`p50-2-reviewer-latency-denominator.md`](p50-2-reviewer-latency-denominator.md) | P50.2 attempt-terminal sample admission, terminal-only outcome preservation, unavailable zero-pair JSON, unsorted nearest-rank percentiles, CLI store compatibility, and race evidence. |
| [`p50-3-review-audit-dispatcher.md`](p50-3-review-audit-dispatcher.md) | P50.3 non-blocking bounded single-writer admission, sink failure/panic isolation, typed evidence-loss diagnostics, concurrent close, bounded QueryEngine shutdown, permission/prompt/cancellation invariance, report, CLI, and race evidence. |
| [`p51-1-darwin-guest-seatbelt.md`](p51-1-darwin-guest-seatbelt.md) | P51.1 real Darwin fixed-binary capability probe, escape matrix, process-class binding, root/restore/child identity, ShellManager lifecycle, user-only selection, Go/Make/Git product, control-plane write, permission-invariance, race, and repository-gate evidence. |

## Docs-only gates

For documentation-only reorganizations, run these gates:

```bash
make docs-check
git diff --check
```

`make docs-check` validates document metadata and naming, Markdown link-label
freshness, local targets and anchors, source line anchors, reachability from
`docs/README.md`, and the migration manifest.

## Code-change gates

For any code change, run the project Makefile gates:

```bash
make fmt
make lint
make test
make build
```

## Ownership of artifacts

- Each verification document owns its own procedure, fixture, budget, and
  reproduction command.
- Results belong in the document only when the procedure has been executed and
  the relevant repository gates pass.
- Verification artifacts do not own current status; they support the evidence
  that feeds [`migration/STATUS.md`](../STATUS.md) and subsystem evidence documents.
