---
name: reference-parity-audit
description: Research one observable product or runtime question across YHC and relevant local reference repositories, producing source-backed comparative evidence and an explicit adoption recommendation. Use before designing an evolution slice, investigating a behavioral or product gap, deciding compatibility, or writing a reference report. Do not use as a substitute for product-scope ownership, implementation, or final verification.
---

# Reference Parity Audit

Research one concrete behavioral question at a time.

Parity is one comparison mode, not the project objective or the default
recommendation.

## Telemetry admission

Apply `$skill-runtime` admission before reading references. Skip logging when
the audit remains short, local, read-only, and has no Terra delegation or final
gate. Otherwise start an audit run before the first trigger with
`skill=reference-parity-audit`, `kind=reference-audit`, and the named subsystem
scope. Record only applicable decision-bearing milestones: `question_frozen`,
`references_verified`, `evidence_collected`, `matrix_finished`, and
`recommendation_finished`.

Use `blocked` when the selected reference or observable contract cannot be
established. The shared runtime owns data minimization, Terra accounting,
parent disposition, every terminal finish, and the measured-ROI boundary.

## Define one observable question

Good examples include:

- Which tools become model-visible and when?
- How are provider fallbacks selected?
- What constitutes recovery progress?
- How does hook cancellation terminate descendants?
- Which runtime component owns sub-agent state?

Avoid broad questions such as "How does the reference implement agents?"

## Verify references

Prefer repository-local references:

- `.reference/claude-code-ripe`
- `.reference/crush`
- `.reference/codex`
- `.reference/opencode`

Read `PROJECT_DIRECTION.md` before comparison. Treat `claude-code-ripe` as the
broadest capability and compatibility baseline, not as the sole product
specification. Select references because they provide evidence for the frozen
question; no reference is an automatic target or replacement specification.

## Inspect direct evidence

Read implementation source, helpers, call sites, and tests. Do not rely on README
summaries, generated status documents, identifier similarity, or comments without
matching runtime evidence.

## Produce the evidence and adoption matrix

Start with current YHC behavior, user outcome, and supported entrypoints.
Then compare only relevant references across:

- state ownership;
- event and callback flow;
- identity and lineage;
- tool/runtime boundaries;
- permissions;
- persistence and replay;
- cancellation and recovery;
- entrypoints;
- TUI projection;
- error and terminal semantics.

For each meaningful alternative, record:

- source and test evidence;
- user-visible benefit and compatibility effect;
- safety, recovery, provider, and entrypoint consequences;
- Go/Eino fit and new ownership or complexity;
- facts that remain unresolved.

Label conclusions as verified, inference, recommendation, or unresolved. A
missing Claude feature is not a product gap without an accepted user outcome.

End with exactly one recommendation from `PROJECT_DIRECTION.md`: `preserve`,
`adapt`, `combine`, `project-native`, `reject`, or `defer`. Explain observable
consequences and which project-owned contract would result.

## Persist only requested facts

Do not edit documentation merely because an audit completed. When the user or
accepted workflow explicitly requests durable documentation, apply
`$write-docs` and route facts to the owning document:

- put cross-reference research in `docs/migration/reference/`;
- update subsystem contracts/maps only for verified current facts;
- update `PLAN.md` only after prioritization;
- update `REMAINING.md` only for confirmed gaps.

The documentation skill owns link and owner validation; hand any changed tree
to `$iteration-workflow` for shared verification and committed evidence.
