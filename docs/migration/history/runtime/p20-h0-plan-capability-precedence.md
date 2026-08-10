# P20.H0 Exact Plan Capability Precedence

**Status:** historical
**Completed:** 2026-07-25
**Last verified:** 2026-07-25

> **Ownership:** delivery boundary, adoption decision, compatibility effect,
> rollback, and verification evidence for exact Plan-file permission precedence

## Outcome

An exact active Plan Write/Edit no longer opens an ordinary filesystem
permission prompt. `evaluatePlanToolPolicy` issues a typed internal capability
only for a runtime request that proves the canonical registered Write/Edit
identity and the exact absolute, clean, session/Agent-owned, symlink-safe Plan
path. `wrapCanUseTool` applies this order:

1. tool selection and central Plan admission;
2. typed `ExitPlanMode` approval;
3. explicit permission deny, including canonical deny for a registered alias;
4. exact Plan-file capability; and
5. ordinary allow/ask, modes, grants, safe paths, classifier, and prompting.

The old later Plan Write/Edit fast path was removed, leaving one production
capability owner. The execution boundary still evaluates pre-tool
stop/deny/permission-deny first and revalidates the Plan path after any input
rewrite and immediately before execution.

## Adoption And Compatibility

P20.H0 is a `combine` decision:

- `preserve` the P17 QueryEngine phase owner, tool selection, exact path and
  symlink containment, model-visible filtering, runtime revalidation, typed
  Exit approval, recovery, and supported entrypoints;
- `adapt` Claude Code Ripe's explicit-deny-before-internal-capability ordering;
  and
- `adapt` Grok Build's use of one admitted-edit predicate for both the edit
  gate and ordinary permission bypass.

Compatibility narrows only the redundant prompt: explicit deny, hook denial,
wrong paths, lexical aliases, traversal, symlinks, tool exclusion, and
`ExitPlanMode` remain fail closed. Registered tool-name aliases canonicalize
to the same Write/Edit capability and cannot evade a canonical deny.

## Non-Goals

- no Plan approval outcome, reviewed-byte digest, or prior-mode change;
- no TUI dialog, feedback editor, viewport, or terminal lifecycle change;
- no new durable state, public API, Graph topology, Eino/Eino-ext dependency,
  or standalone MCP behavior; and
- no generic permission-order change outside the exact Plan capability.

## Verification

Focused tests cover:

- exact Write and Edit under ordinary `ask`, with zero callback, Graph
  interrupt, denial-list, or denial-tracker effects;
- explicit deny for exact Write/Edit with zero prompt;
- registered alias canonicalization under canonical ask and deny rules;
- wrong-path containment under allow, ask, and bypass;
- pre-tool hard deny before permission or execution;
- typed `ExitPlanMode` remaining separate from the file capability;
- existing lexical/non-clean/traversal/symlink rejection and post-rewrite
  revalidation; and
- scoped race execution.

Repository-wide formatting, lint, test, build, new-lint, documentation,
manifest, and diff gates passed before merge.

## Rollback

Revert the typed `planToolPolicyClass`, the exact capability branch, and its
tests as one unit. The previous exact containment and late Plan Write/Edit
fast path can be restored without schema or state migration. Later P20 slices
must not rely on this rollback path until P20.H0 remains merged.
