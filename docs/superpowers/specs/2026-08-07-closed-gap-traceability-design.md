# Closed Gap Traceability Design

**Status:** active-plan
**Accepted:** 2026-08-07
**Last verified:** 2026-08-07

> **Ownership:** approved design for durable closed-gap-to-history mapping;
> unresolved gaps remain owned by
> [`REMAINING.md`](../../migration/REMAINING.md), while completed delivery
> remains owned by [`history/`](../../migration/history/README.md)

## Outcome

Every historical closeout that closes a root product Gap records that identity
in one canonical metadata field. `docs-check` validates the field, rejects
duplicate closure ownership, and prevents a currently unresolved Gap from also
being declared closed.

The repair restores direct discoverability for mappings such as G18 to P23.2,
G22 to P25.1, and G23 to P26.1 without putting completed work back into
`REMAINING.md` or creating another migration-state ledger.

## Problem

The documentation-governance cleanup correctly reduced `REMAINING.md` to
unresolved behavior. Some explicit closed-Gap mappings existed only in the
removed completion inventory, while the retained closeout documents describe
the delivered behavior without naming the Gap. Current source and tests still
prove the implementations, but a reader cannot resolve every old Gap ID to its
closeout from the current tree.

Git history is useful evidence but is not an acceptable primary navigation
mechanism for a current audit contract.

## Decision And Alternatives

Add optional closeout metadata to the existing historical fact owner:

```markdown
**Closed gaps:** G22
```

Use a comma and one space for multiple identities:

```markdown
**Closed gaps:** G6, G7
```

- Restoring a completed inventory to `REMAINING.md` is rejected because that
  file owns unresolved behavior only.
- A new `closed-gaps.yaml` is rejected because it would duplicate the historical
  closeout owner and require generated synchronization.
- A README-only table is rejected because it would separate the identity from
  the evidence document that actually closed the Gap.

## Metadata Contract

The field is optional for historical documents that close no root Gap. When
present, it must:

1. appear within the first 30 lines with the other lifecycle metadata;
2. contain one or more comma-space-separated IDs matching `G[1-9][0-9]*`;
3. list IDs in strictly increasing numeric order without duplicates; and
4. be unique across all Markdown files under `docs/migration/history/`.

`docs-check` also reads the root Gap rows in `docs/migration/REMAINING.md`. An
identity cannot be both unresolved and closed. Sub-program identities such as
`G11.F2` remain ordinary narrative identifiers and do not enter this root-Gap
metadata field.

## Bootstrap Mapping

The first implementation backfills the current historical owners below. This
table freezes the migration input; the metadata in each closeout becomes the
durable owner after the repair.

| Closed Gap | Historical owner |
|---|---|
| G1 | `history/runtime/p34-1-file-state-checkpoint-repair.md` |
| G3 | `history/runtime/p28-h0-standalone-mcp-permission-policy.md` |
| G4 | `history/runtime/p32-1-plugin-file-authority.md` |
| G5 | `history/runtime/p33-1-mcp-live-tool-generation.md` |
| G6, G7 | `history/tui/p19-3-5-welcome-wordmark.md` |
| G8 | `history/tui/p35-1-tui-notification-lifecycle.md` |
| G9 | `history/tui/g9-e-table-repair-deletion.md` |
| G10 | `history/runtime/p20-r3-plan-interaction-closeout.md` |
| G11 | `history/tui/g11-f2-terminal-program-closeout.md` |
| G12 | `history/tui/p40-1-startup-theme-polarity.md` |
| G13 | `history/runtime/p22-h0-bash-containment.md` |
| G15 | `history/runtime/p23-h0-session-deletion-containment.md` |
| G16 | `history/runtime/p23-4b-acp-replay-bounded-listing.md` |
| G17 | `history/runtime/p23-5-transactional-stdio-mcp.md` |
| G18 | `history/runtime/p23-2-acp-tool-lifecycle.md` |
| G19 | `history/runtime/p23-3-acp-assistant-commands.md` |
| G20 | `history/runtime/p36-1-acp-rich-assistant-replay.md` |
| G22 | `history/runtime/p25-agentic-provider-input-fidelity.md` |
| G23 | `history/runtime/p26-canonical-model-round-owner-cleanup.md` |
| G24 | `history/tui/g24-plan-confirmation-input-isolation.md` |
| G25 | `history/tui/p41-1-fixed-size-geometry-owner.md` |
| G26 | `history/tui/p41-2-bounded-markdown-renderer-pool.md` |
| G27 | `history/tui/g27-result-bound-command-recency.md` |
| G29 | `history/runtime/p43-0-real-repository-evaluation.md` |
| G30 | `history/tui/p27-selection-viewport-geometry.md` |
| G31 | `history/runtime/p29-4-bounded-overload-failover.md` |
| G32 | `history/runtime/p30-6-multimodal-program-closeout.md` |
| G33 | `history/runtime/p31-5-old-owner-closeout.md` |
| G34 | `history/runtime/p38-0-provider-reasoning-origin.md` |
| G35 | `history/runtime/p37-1-project-graph-permission-settlement-chain.md` |
| G36 | `history/runtime/p46-1-complete-prompt-footprint.md` |
| G37 | `history/runtime/p46-2-observable-failover.md` |

G2, G14, G21, and G28 remain unresolved and must not appear in closeout
metadata.

## Implementation Boundary

One independently reviewable history-governance change may modify:

- the closeout documents in the bootstrap table;
- `docs/migration/history/README.md` to explain the metadata convention;
- `docs/contributing/documentation-policy.md` to require the field when a
  closeout resolves a root Gap;
- `scripts/docs_check/main.go` for parsing and validation; and
- `scripts/docs_check/main_test.go` for deterministic fixtures.

No runtime source, migration queue state, current architecture claim, or
unresolved-Gap decision changes.

## Verification And Failure Behavior

Focused checker tests must prove:

- one and multiple canonical IDs pass;
- malformed separators, zero/leading-zero IDs, unsorted IDs, and local
  duplicates fail;
- the same Gap in two closeouts fails and reports both files;
- an ID present in `REMAINING.md` and closeout metadata fails; and
- a historical document without the optional field remains valid.

Implementation validation uses:

```bash
go test ./scripts/docs_check -count=1
make docs-check
```

Final closeout runs all repository Makefile gates because the validator is Go
code. The backfill is accepted only when every mapping in the frozen table is
discoverable from the current tree and no unresolved Gap is marked closed.

## Rollback

Rollback removes the validator and metadata as one documentation-governance
change. It requires no runtime or data migration, but it restores the current
audit gap where several old Gap IDs require Git-history archaeology.
