---
name: write-docs
description: Write, reorganize, or audit source-backed YHC technical documentation with explicit ownership, lifecycle, diagrams, examples, and code references. Use when the user or an accepted workflow explicitly requests documentation work in project direction, architecture, guides, contributing policy, evolution ledgers, reference evidence, verification, history, or link structure. Do not trigger merely because code changed, invent unverified facts, or replace comparative research and final closeout.
---

# Write Docs

Produce one-owner documentation that lets a human or coding agent find the
current behavior, its evidence, and the correct next document without reading a
migration diary.

## Telemetry admission

Apply `$skill-runtime` admission before selecting a document owner. Skip logging
when the task remains a short, local, read-only audit with no Terra delegation or
final gate. Otherwise start an audit run before the first trigger with
`skill=write-docs`, `kind=documentation`, and the owning document or directory.
Record only applicable decision-bearing milestones: `reader_contract_frozen`,
`owner_selected`, `evidence_verified`, `document_updated`, and
`validation_finished`.

The shared runtime owns safe structured fields, Terra accounting, the
child/parent/caller-worktree distinction, every terminal finish, and the rule
that an unlogged run cannot support measured delegation or ROI claims.

## Load the governing context

1. Read `AGENTS.md`, `PROJECT_DIRECTION.md`, and
   [`docs/contributing/documentation-policy.md`](../../../docs/contributing/documentation-policy.md).
2. Read `docs/README.md`, the affected directory index, and the existing owner
   document before editing.
3. Read `docs/migration/STATUS.md`, `REMAINING.md`, and `PLAN.md` only when the
   change affects their owned facts.
4. Use CodeGraph before broad search when `.codegraph/` exists. Confirm symbols,
   callers, entrypoints, and tests from current source.
5. Use `$reference-parity-audit` first when a claim depends on reference
   behavior rather than current project source.
6. Use `$human-agent-docs` when reader-task structure, terminology, examples,
   tables, diagrams, or accessibility need substantive improvement. This skill
   retains ownership of Eino facts and document routing.

## Select exactly one fact owner

Route the document by the question it answers:

| Question | Owner |
|---|---|
| What is the product objective or reference-adoption rule? | `PROJECT_DIRECTION.md` |
| How does the current implementation work? | `docs/architecture/` |
| How does a user perform a task? | `docs/guides/` |
| What is verified now? | `docs/migration/STATUS.md` |
| What reproduced gap remains? | `docs/migration/REMAINING.md` |
| What accepted work runs next? | `docs/migration/PLAN.md` |
| What is the detailed accepted contract? | `docs/migration/plans/` |
| What did a source comparison establish? | `docs/migration/reference/` |
| How is evidence reproduced? | `docs/migration/verification/` |
| How was completed work delivered? | `docs/migration/history/` |

Link from non-owners instead of copying counts, current state, plans, or
completion narratives.

## Freeze the evidence boundary

1. State the reader question and the observable contract.
2. Trace composition root to owning symbol, helpers, side effects, terminal
   behavior, and all supported entrypoints.
3. Read focused tests and negative paths. Treat definitions, registration,
   imports, and comments as insufficient proof of production wiring.
4. Classify each claim as verified fact, inference, recommendation, gap, or
   exclusion.
5. Label wiring as active, entrypoint-specific, active seam/no-op, partially
   wired, or outside product closure when reachability is not uniform.
6. For reference-derived proposals, label the adoption decision as `preserve`,
   `adapt`, `combine`, `project-native`, `reject`, or `defer`; do not turn a
   missing reference feature into product scope.

For runtime-sensitive ordering, provider, recovery, permission, hook, service,
session, or TUI state claims, also use `$runtime-depth-change` or
`$tui-runtime-change` as applicable.

## Write the document

Start every durable document with H1, lifecycle metadata, freshness or snapshot
metadata, and a precise Ownership banner. Use lower-kebab focused filenames and
the repository status vocabulary from the policy.

Choose the smallest useful structure:

- Current architecture: decision and boundary, ownership, flow or ordering,
  entrypoint differences, invariants and failures, code references.
- Guide: outcome, prerequisites, executable steps, expected result, failure
  recovery, maintainer references.
- Plan: problem, scope and non-goals, frozen invariants, ordered slices, gates,
  rollback, source owners.
- Gap: observable mismatch, current evidence, consequence, acceptance state.
- Reference: named snapshot, observable question, evidence matrix, verdict,
  consequences, current owner link.
- Verification: contract, environment, command or fixture, pass/fail rule,
  evidence limitations.
- History: completed date, state at closeout, outcome, evidence, current
  replacement. Use past tense.

Omit empty sections. Lead with the result rather than research chronology.
Define project vocabulary once, use exact runtime names, and qualify
entrypoint-specific or helper-only behavior.

## Select diagrams and examples

- Use a sequence diagram for ordered interactions across three or more actors.
- Use a flowchart for branching, ownership transfer, or one source feeding
  several consumers.
- Use a table for exact mappings, capability matrices, inventories, or
  entrypoint differences.
- Skip a diagram for a single fact or linear explanation already clear in
  prose.
- Keep Mermaid labels quoted when they contain punctuation. Use stable
  conceptual labels instead of volatile counts.
- Keep examples executable and state omitted security, persistence, entrypoint,
  or failure details.

## Add code references

- Write `symbol + repository-relative link + ownership reason`.
- Anchor to the declaration or first executable statement, never a blank line,
  comment-only line, closing brace, or incidental caller.
- Link production callers when a helper's wiring status matters.
- Do not paste large source excerpts or machine-specific absolute paths.

## Reorganize or audit one by one

1. Enumerate every file in scope exactly once.
2. Record H1, filename, lifecycle, ownership, current owner, inbound index,
   code evidence, and KEEP/CORRECT/RENAME/MERGE decision.
3. Freeze rename and merge mappings before moving files.
4. Preserve distinct evidence; merge only duplicated ownership or narrative.
5. Update directory indexes and all Markdown link labels, not only targets.
6. Scan for old names, stale active language in history/reference, duplicated
   plan state, and volatile counts outside their owner.
7. Request independent review for security, permissions, persistence,
   concurrency, recovery, or lifecycle claims.

## Validate the document, then hand off closeout

Run document-specific checks after the last edit:

```bash
make docs-check
```

Use `$human-agent-docs` structural auditing when readability or accessibility
was in scope. Then hand the changed owners, code evidence, local validation,
exclusions, and remaining uncertainty to `$iteration-workflow` for shared
caller-tree verification, committed evidence, staging, and any authorized
commit.

Stop and mark the claim unresolved when current source, tests, and entrypoints
cannot establish it. Never convert intent, a reference recommendation, or a
registered symbol into a current implementation claim.
