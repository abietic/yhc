# Design Specification Index

**Status:** active-plan
**Last verified:** 2026-08-15

> **Ownership:** discoverability and lifecycle routing for approved design
> specifications written before implementation planning

| Specification | Scope | State |
|---|---|---|
| [`2026-08-15-public-canonical-cutover-design.md`](2026-08-15-public-canonical-cutover-design.md) | Public YHC development-home cutover, task continuity, private recovery inventory, archive preconditions, and Desktop deferral | Approved 2026-08-16; implementation planning active |
| [`2026-08-13-yhc-desktop-workbench-forward-port-design.md`](2026-08-13-yhc-desktop-workbench-forward-port-design.md) | Public-history-safe Desktop forward-port, YHC identity and state adaptation, app-server authority, Node dependency clearance, and promotion gates | Approved; implementation planning active |
| [`2026-08-13-untracked-sbom-design.md`](2026-08-13-untracked-sbom-design.md) | Remove the committed SBOM while retaining open-source licensing and fail-closed dependency-license checks | Historical; implemented 2026-08-13 |
| [`2026-08-09-yhc-public-release-design.md`](2026-08-09-yhc-public-release-design.md) | Clean public repository, private archive, identity compatibility, source-mapping retention, and publication gates | Historical; implemented 2026-08-11 |
| [`2026-08-09-iteration-quality-s5-completion-design.md`](2026-08-09-iteration-quality-s5-completion-design.md) | Residual plan/worktree hygiene, independent regression oracles, and local performance measurement after S1-S4 | Approved; F0 executed and S5A-S5C implementation is active |
| [`2026-08-08-todo-workboard-transcript-mode-hotfix-design.md`](2026-08-08-todo-workboard-transcript-mode-hotfix-design.md) | Legacy transcript-directory repair, truthful Todo runtime-state permission metadata, and Guest protection of `.eino-agent` writes | Implemented; retained as design and verification evidence |
| [`2026-08-08-iteration-quality-kernel-design.md`](2026-08-08-iteration-quality-kernel-design.md) | Local-first change planning, risk-selected verification, context and skill consolidation, defect discovery, and incremental module boundaries | S1-S4 governance executed; product-module deepening remains intake-gated |
| [`2026-08-07-permission-remediation-design.md`](2026-08-07-permission-remediation-design.md) | Three independent permission-runtime repairs for revision fencing and truthful non-blocking reviewer audit | Approved; implementation plans pending |
| [`2026-08-07-darwin-sandbox-auto-permission-design.md`](2026-08-07-darwin-sandbox-auto-permission-design.md) | Darwin Seatbelt Guest containment and later verified Auto Permission admission | Historical; P51.1 and P51.2 Core complete, Desktop projection deferred |
| [`2026-08-07-agent-runtime-instruction-freshness-design.md`](2026-08-07-agent-runtime-instruction-freshness-design.md) | Root Agent instruction alignment with the production ProjectGraph owner | Approved; implementation plan pending |
| [`2026-08-07-closed-gap-traceability-design.md`](2026-08-07-closed-gap-traceability-design.md) | Durable closed-gap-to-history mapping and validation | Approved; implementation plan pending |
| [`2026-08-07-acp-boundary-remediation-design.md`](2026-08-07-acp-boundary-remediation-design.md) | Five independent ACP v1 boundary repairs and their rollback boundaries | Approved; P48 contract and implementation plans ready |
| [`2026-08-13-desktop-session-creation-activation-design.md`](2026-08-13-desktop-session-creation-activation-design.md) | Immediate new-session selection, non-focus-stealing background hydration, and explicit single-flight UI admission | Approved; implementation planning pending |

These artifacts freeze a reviewed design but do not change current behavior or
promote migration queue state. Accepted execution order remains owned by
[`migration/PLAN.md`](../../migration/PLAN.md).
