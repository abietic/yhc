# Documentation Policy

**Status:** current
**Last verified:** 2026-07-17

> **Ownership:** information architecture, document lifecycle, naming, code
> references, and documentation closeout rules for the whole repository

## Purpose

The documentation system should let a reader answer one question without
traversing the migration diary:

- How do I use the product?
- How does the current implementation work?
- What is verified, unresolved, or accepted next?
- Why was a decision made?
- How can I reproduce its evidence?

Each fact has one owner. Other documents link to that owner instead of copying
volatile counts, active-state prose, or long implementation narratives.

## Source-of-truth map

| Question | Owner | Must not contain |
|---|---|---|
| Where should I start? | [`README.md`](../README.md) | Detailed subsystem behavior |
| What product are we building and how are references adopted? | [`PROJECT_DIRECTION.md`](../../PROJECT_DIRECTION.md) | Volatile implementation status or active execution order |
| How do I perform a user task? | [`docs/guides/`](../guides/README.md) | Internal call-path inventories |
| How is the current product composed? | [`docs/architecture/`](../architecture/README.md) | Future architecture target or milestone history |
| Which package owns a behavior? | [`architecture/code-map.md`](../architecture/code-map.md) | Unverified reachability assumptions |
| What is verified now? | [`migration/STATUS.md`](../migration/STATUS.md) | Future checklists |
| What remains unresolved? | [`migration/REMAINING.md`](../migration/REMAINING.md) | Completed feature inventories |
| What is accepted next? | [`migration/PLAN.md`](../migration/PLAN.md) | Duplicated current counters or closed milestones |
| What machine data owns queue state and dependencies? | [`migration/queue.yaml`](../migration/queue.yaml) | Current capability claims or completed narratives |
| What is the detailed contract for an accepted program? | [`migration/plans/`](../migration/plans/) | Comparative research presented as current code |
| What counts as evolution evidence or done? | [`migration/GUIDELINE.md`](../migration/GUIDELINE.md) | Volatile implementation counts |
| What Claude reference mappings exist? | [`migration/manifest.yaml`](../migration/manifest.yaml) | Product completeness or human progress narrative |
| What did source comparison establish? | [`migration/reference/`](../migration/reference/README.md) | Active execution order |
| How is evidence reproduced? | [`migration/verification/`](../migration/verification/README.md) | Current product ownership |
| How was completed work delivered? | [`migration/history/`](../migration/history/README.md) | Current status or next-step claims |

When documents disagree, inspect current source and focused tests, then update
the owning document. Do not resolve drift by copying the newest sentence into
every layer.

## Lifecycle and status vocabulary

Every durable Markdown document has one role:

| Status | Meaning |
|---|---|
| `current` | Describes supported behavior verified against current source. |
| `active-plan` | Owns accepted future work and executable acceptance criteria. |
| `gap-inventory` | Records reproduced but unresolved behavior. |
| `reference-snapshot` | Freezes source comparison at a named date or revision. |
| `verification` | Owns reproducible commands, fixtures, or measured budgets. |
| `historical` | Preserves completed decisions and evidence; never owns current truth. |

Use a short ownership banner near the top:

```markdown
# Title

**Status:** current
**Last verified:** YYYY-MM-DD

> **Ownership:** the exact question this file answers
```

Reference snapshots use `**Snapshot:**`; history may use `**Completed:**`.
Do not embed test counts, milestone IDs, or wiring summaries in the `Status`
field.

Moving a root Gap out of `REMAINING.md` is incomplete until its historical
closeout declares the canonical `Closed gaps` field. The optional field belongs
only on closeouts that resolve root Gaps; `docs-check` rejects duplicate
historical owners and overlap with unresolved root Gaps, so no second ledger is
needed.

## Directory and naming rules

- Directories encode audience or lifecycle; file names encode the subject.
- Current implementation belongs under `architecture/`, never under
  `migration/`, `reference/`, or `history/`.
- User workflows belong under `guides/`; they link to architecture for internals.
- Focused files use lower-kebab names.
- Every directory index uses the conventional `README.md` name. Only the
  migration control files `GUIDELINE.md`, `STATUS.md`, `REMAINING.md`, and
  `PLAN.md` keep subject names in uppercase.
- Numbering is reserved for a real sequence, such as historical implementation
  maps. Do not number current subsystems merely to create a reading order.
- Avoid ambiguous names such as `NOTES.md`, `NEW_PLAN.md`, `FINAL.md`, and
  `misc.md`.

## Writing for humans and agents

1. Lead with the decision, current behavior, or task outcome.
2. Explain ownership and entrypoint differences before helper details.
3. Use a small diagram only when sequence, branching, or ownership is easier to
   understand visually.
4. Use tables for inventories and comparisons; use prose for causality and
   tradeoffs.
5. Keep examples short and executable. State when an example omits security,
   persistence, or entrypoint details.
6. Separate verified fact, inference, recommendation, gap, and exclusion.
7. Link to history and research instead of reproducing their narrative in a
   current document.

## Technical document archetypes

Use the smallest structure that answers the owning question. The headings below
are a selection guide, not a template that requires empty sections.

| Document type | Recommended structure | Required boundary |
|---|---|---|
| Product direction | Objective; success criteria; decision hierarchy; reference roles; adoption vocabulary; non-goals | Own scope and decision policy, not current implementation or active execution order. |
| Current architecture | Decision or overview; ownership; flow or ordering; entrypoint differences; invariants and failures; code references | Describe only source- and test-verified current behavior. |
| User or operator guide | Outcome; prerequisites; executable steps; expected result; failure recovery; maintainer references | Prefer supported commands and observable results over internal inventories. |
| Active plan | Problem; scope and non-goals; frozen invariants; ordered slices; promotion gates; rollback; source owners | Do not present target architecture as current or duplicate gap/status facts. |
| Gap inventory | Observable mismatch; current evidence; consequence or risk; acceptance state; owning links | Record reproduced gaps, not speculative feature ideas. |
| Reference snapshot | Snapshot; observable question; evidence matrix; verdict; consequences; current replacement | Preserve the assessment boundary and never own active order. |
| Verification procedure | Contract; environment; command or fixture; pass/fail rule; evidence limitations | Separate portable gates from machine-specific baselines. |
| Historical record | Completed date; state at closeout; outcome; evidence; current replacement | Use past tense and remove active backlog language. |

Lead with the reader's answer. Put research chronology, long inventories, and
closed checklists only in the lifecycle layer that owns them.

## Diagrams and examples

Use a visualization only when it reduces reasoning cost:

| Relationship | Preferred form |
|---|---|
| Ordered calls across three or more actors | Mermaid sequence diagram |
| Branching, ownership transfer, or one source feeding several consumers | Mermaid flowchart |
| Exact mappings, entrypoint differences, capability, compatibility, or reference inventory | Table |
| One fact or short linear rule | Prose; no diagram |

- Quote Mermaid labels containing punctuation and use conceptual names rather
  than volatile counts or line numbers.
- Keep one diagram responsible for one relationship. Explain the causal rule in
  prose instead of repeating every node as a paragraph.
- Keep examples short and executable. State when security, persistence,
  cancellation, entrypoint, or error handling is intentionally omitted.
- Do not use screenshots as the sole source of a runtime contract when source,
  structured output, or a reproducible fixture exists.

## Source-backed authoring workflow

1. Freeze the reader question, lifecycle, and owner before writing.
2. Inspect current source, production callers, supported entrypoints, focused
   tests, and negative paths. Use CodeGraph first when `.codegraph/` exists.
3. Distinguish a definition, import, registration, or comment from production
   construction and invocation.
4. Classify statements as verified fact, inference, recommendation, gap, or
   exclusion; write boundary words conservatively.
5. Draft the smallest document structure and add only diagrams or examples that
   improve a decision or workflow.
6. Add symbol-level code references and link non-owned facts to their owner.
7. Re-read the document against every supported entrypoint and failure path.
8. Run documentation and repository gates after the last edit, then request an
   independent review for security-, persistence-, concurrency-, recovery-, or
   lifecycle-sensitive claims.

Project agents should use
[`$write-docs`](../../.agents/skills/write-docs/SKILL.md) for this
workflow, `$reference-parity-audit` for comparative reference evidence, and
`$iteration-workflow` for shared verification and committed evidence.

## Code-reference contract

Current architecture documents end with a compact code-reference table or
list. Each reference names the symbol, links to the repository-relative source,
and explains why it owns the boundary:

```markdown
| Boundary | Code reference | Why it matters |
|---|---|---|
| Turn entry | [`QueryEngine.SubmitMessage`](../../engine/engine.go#L381) | Creates one turn and its event stream. |
```

- Prefer `symbol + link + role` over a bare file path.
- Add a line anchor when it lands on the owning declaration or first executable
  statement. Do not anchor to blank lines, closing braces, or incidental calls.
- Do not paste large source excerpts into architecture documents.
- Reference and history documents may use commit-scoped paths, but they must
  identify their snapshot and link to the current owning architecture doc.

## Production-wiring labels

A package in the module or binary closure does not prove that every exported API
is active. Use one of these labels in code maps:

- **active:** reached by a supported production entrypoint;
- **entrypoint-specific:** active only through a named surface;
- **active seam / no-op:** called in production but currently produces no
  effect;
- **partially wired:** package is active but the named capability has no caller
  or constructed dependency;
- **outside product closure:** not reached by the released composition roots.

Examples that require explicit labels include standalone MCP dispatch,
registered-but-unavailable commands, plugin contribution types, helper-only
service APIs, and packages imported only because a command is registered.

## Freshness triggers

| Change | Required update |
|---|---|
| Product objective or reference-adoption policy changes | `PROJECT_DIRECTION.md`, affected Agent/Skill constraints, and entry indexes |
| Entrypoint or ownership changes | Architecture root, code map, and affected subsystem |
| Observable behavior changes | Affected architecture doc and task guide |
| Gap discovered | `REMAINING.md`; add to `PLAN.md` only after prioritization |
| Plan accepted or reordered | `PLAN.md` and its one detailed plan owner |
| Queue state, dependency, gate, or priority changes | `queue.yaml`, generated `PLAN.md` block, and its detailed plan owner |
| Slice completed | Current owner, trackers, one history record, and manifest evidence only when Claude mappings changed |
| Reference files change | Manifest sync/check and `STATUS.md` counters |
| Document moves | All repo-local links, indexes, source anchors, and external instructions |
| Commands, fixtures, or budgets change | Owning verification document |

## Safe document moves

1. Freeze an old-path to new-path map.
2. Move one lifecycle group at a time.
3. Recompute relative links from both moved sources and moved targets; source
   links change when directory depth changes.
4. Scan the whole repository for old paths, including `AGENTS.md`, skills,
   scripts, tests, and CI.
5. Verify every Markdown file is reachable from [`README.md`](../README.md)
   through a directory index.
6. Run link, anchor, manifest, whitespace, and repository gates before closeout.

`make docs-check` enforces the lifecycle status vocabulary, top-of-file
Ownership metadata, focused-file lower-kebab naming, `.md` link-label/target
agreement, link and anchor validity, and reachability from the documentation
entrypoint.

For `docs/superpowers/plans/`, the `Implementation Plan Index` is also a
machine-enforced lifecycle contract: each non-README plan appears exactly once
in its explicit table. `Executed` and `Historical` rows require
`**Status:** historical`; `Active`, `Ready`, `Queued`, `Draft`, `Accepted`, and
`Accepted-design` rows require `**Status:** active-plan`; any other prefix is
rejected. Only `Accepted` and `Accepted-design` plans must declare REQUIRED
SUB-SKILL in the document's first top-level blockquote. The checker ignores
fenced examples, nested blockquotes, ordinary prose, and later top-level
blockquotes. A live required block needs at least one canonical `$skill-name`
token (a Markdown code span may wrap it), and every token must resolve at
`.agents/skills/<skill>/SKILL.md`. Bare or malformed lookalikes, extra dollar
signs, legacy `superpowers:*`, and any repo-local skill name elsewhere in the
marker payload are rejected. Ordinary unrelated prose/code spans remain text;
historical plans cannot contain a live required-skill block.

Git history is the archive. Keep redirect stubs only when an old published path
has a real compatibility requirement; otherwise remove redundant indirection.

## Iteration closeout

`$iteration-workflow` is the sole shared owner for planning, selected checks,
committed-tree evidence, and final handoff. Domain skills retain only their
own invariants and route shared mechanics to that owner; retired aliases are
not active instructions.

For a documentation or product-evolution slice:

1. inspect the current worktree and exclude unrelated user changes;
2. verify source and tests before changing current-state claims;
3. update only the owning architecture, guide, tracker, reference, verification,
   or history layer;
4. close resolved gaps and advance plans without copying completion narratives;
5. run `make docs-check` and the checks in [`verification.md`](verification.md);
6. inspect the final diff and confirm no orphan or unclassified document remains.

## Review checklist

- Every document is linked from one directory index.
- Every current production package has an owner or explicit wiring label.
- Current architecture contains no future target presented as implemented.
- Guides contain no invented flags, environment variables, or command effects.
- `PLAN.md`, `REMAINING.md`, and `STATUS.md` respect their distinct ownership.
- Reference snapshots are time-scoped; history uses past-tense status.
- Local links and source anchors resolve and do not land on blank lines.
- Volatile counts appear only in their owning current-status document.
- `git diff --check` and the required repository gates pass.
