# Product Evolution Ledger

**Status:** current
**Last verified:** 2026-08-02

> **Ownership:** operational entrypoint for verified status, gaps, accepted
> evolution work, comparative evidence, and migration history; current product
> architecture lives outside this tree

Return to the [`docs` home](../README.md) for user and current-architecture
routes. The `migration/` path is retained for historical continuity; it no
longer implies that Claude Code Ripe defines the complete product target. The
normative objective lives in
[`PROJECT_DIRECTION.md`](../../PROJECT_DIRECTION.md).

This directory answers only four operational questions:

1. What is verified now?
2. What remains unresolved?
3. What work is accepted next?
4. What evidence, research, and history support those decisions?

## Read order

| Order | Document | Owner |
|---:|---|---|
| 1 | [`GUIDELINE.md`](GUIDELINE.md) | Scope, evidence levels, workflow, and definition of done |
| 2 | [`STATUS.md`](STATUS.md) | Verified current evolution facts and volatile counts |
| 3 | [`REMAINING.md`](REMAINING.md) | Reproduced unresolved gaps, including unprioritized gaps |
| 4 | [`PLAN.md`](PLAN.md) | Checked human execution topology and next ready slice |
| 5 | [`plans/`](plans/) | Authoring rules and detailed contracts for accepted multi-slice programs; completed compatibility-retained contracts are explicitly historical |
| 6 | [`manifest.yaml`](manifest.yaml) | Machine-readable reference classification and mapping ledger |

The active/deferred queue, hard dependencies, promotion gates, and risk
priority are machine-owned by [`queue.yaml`](queue.yaml). `PLAN.md` renders its
human table and Mermaid topology; `make docs-check` rejects drift or cycles.

To understand the code affected by a plan, leave the migration tree and read
[`architecture/README.md`](../architecture/README.md) plus the owning module
document. Future target architecture in a plan must never be read as current
implementation.

## Supporting evidence by lifecycle

| Directory | Use it when... | Never treat it as... |
|---|---|---|
| [`reference/`](reference/README.md) | A decision needs source comparison, compatibility evidence, or a frozen baseline | Current product ownership or active execution order |
| [`verification/`](verification/README.md) | You need reproducible commands, fixtures, or measured budgets | A status tracker |
| [`history/`](history/README.md) | You need completed plans, implementation maps, or closeout evidence | Current status, unresolved gaps, or next work |

## Directory layout

```text
docs/migration/
  README.md          this operational index
  GUIDELINE.md       evolution scope, evidence, workflow, and done policy
  STATUS.md          verified current facts
  REMAINING.md       unresolved gap inventory
  PLAN.md            accepted order and ready work
  manifest.yaml      machine-readable Claude reference ledger
  plans/             detailed-contract authoring and active contract index
  reference/         time-scoped source comparison
  verification/      reproducible evidence procedures
  history/           completed records
```

Current runtime, capability, platform, persistence, and TUI documentation lives
under [`docs/architecture/`](../architecture/README.md), not under migration.

## Agent update protocol

1. Start from an accepted user outcome or reproduce a gap from source, tests,
   or runtime evidence. A missing reference feature is not a gap by itself.
2. Record it in `REMAINING.md`; do not put it in `PLAN.md` before acceptance.
3. When accepted, record the adoption decision from `PROJECT_DIRECTION.md`, let
   `PLAN.md` own priority and order, and put detailed multi-slice contracts in
   one file under `plans/`.
4. During implementation, source and tests remain the facts; do not pre-mark
   docs complete.
5. At closeout, update the affected architecture owner, trackers, one history
   record, and manifest evidence only when Claude reference mappings changed.
   Do not copy the same completion narrative into multiple files.

## Manifest maintenance

```bash
# Synchronize reference additions/removals while preserving reviewed fields.
go run ./scripts/migration_manifest.go sync

# Validate inventory, enums, evidence paths, and summary counts.
go run ./scripts/migration_manifest.go check
```

New reference files enter as `pending_review` / `not_started`. Synchronization
maintains inventory; it is not an implementation-status claim.

Documentation ownership, naming, code-reference, and movement rules are in
[`documentation-policy.md`](../contributing/documentation-policy.md).
