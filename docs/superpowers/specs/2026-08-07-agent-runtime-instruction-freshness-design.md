# Agent Runtime Instruction Freshness Design

**Status:** active-plan
**Accepted:** 2026-08-07
**Last verified:** 2026-08-07

> **Ownership:** approved design for keeping root Agent instructions aligned
> with the current production query owner; current runtime behavior remains
> owned by the [Query Engine architecture](../../architecture/runtime/query-engine.md)

## Outcome

Root Agent instructions must describe `QueryEngine` as the conversation and
session boundary and `projectGraphQueryKernel` as the single production query
traversal. Public `Query` and supported `QueryEngine` entrypoints use that same
kernel. `engine/query.go` is an invocation and terminal-projection boundary,
not an independent imperative production loop.

The repair changes no runtime behavior, public API, durable format, provider,
permission, or entrypoint contract. It corrects instructions consumed by coding
agents and adds a narrow documentation regression guard.

## Problem

`AGENTS.md` currently says that `engine/` is "not graph-based" and that
`engine/query.go` remains the production-loop authority. Current source proves
the opposite:

- [`productionQueryKernel`](../../../engine/query_kernel.go) returns the shared
  ProjectGraph kernel or a fail-closed unavailable kernel;
- [`newProjectGraphQueryKernel`](../../../engine/graph_query_kernel.go)
  compiles the project-owned Eino Compose Graph; and
- [`Query`](../../../engine/query.go) resolves that kernel and delegates the
  turn to it.

This drift does not change the current binary, but it can cause future coding
agents to review or implement against a retired owner and recreate duplicate
execution paths.

## Decision And Alternatives

Use the current architecture owner plus a small `docs-check` invariant.

- A prose-only correction is rejected because the same drift can recur without
  a failing gate.
- A machine-generated `AGENTS.md` is rejected because most of the file contains
  human-owned workflow and safety policy that should remain directly editable.
- The selected design keeps the prose human-owned, links the current
  architecture owner, and checks only the stable production-owner boundary.

## Frozen Contract

`AGENTS.md` must:

1. name `QueryEngine` and `projectGraphQueryKernel` in its architecture summary;
2. link to `docs/architecture/runtime/query-engine.md` as the detailed current
   owner;
3. state that direct `Query` calls share the ProjectGraph execution owner; and
4. contain neither the retired "imperative agent loop, not graph-based" claim
   nor the claim that `engine/query.go` is a separate production-loop authority.

`scripts/docs_check` must validate this contract only for the repository-root
`AGENTS.md`. A failure reports the missing required owner or the exact retired
claim. The checker must continue to accept fixture repositories that omit
`AGENTS.md` unless another existing rule requires it.

The guard deliberately does not parse Go call graphs or claim that prose can
prove runtime wiring. Current source and the existing P13.10 production-kernel
tests remain the runtime oracle.

## Implementation Boundary

One independently reviewable documentation-governance change may modify:

- `AGENTS.md` for the two stale ownership statements;
- `scripts/docs_check/main.go` for the root instruction invariant; and
- `scripts/docs_check/main_test.go` for positive and negative fixture coverage.

No architecture document needs a behavior rewrite because the current Query
Engine document already describes the production owner correctly.

## Verification And Failure Behavior

Focused proof must cover:

- valid ProjectGraph instructions pass;
- each retired claim fails independently with a path-specific diagnostic;
- missing `QueryEngine`, `projectGraphQueryKernel`, or the architecture link
  fails independently; and
- an otherwise valid fixture without `AGENTS.md` preserves existing checker
  behavior.

Implementation validation uses:

```bash
go test ./scripts/docs_check -count=1
go test ./engine -run '^(TestP1310ProductionProjectGraphMatchesCompiledGraphFixtures|TestP1310ProductionKernelIsProjectGraphWithoutFixtureEffects)$' -count=1
make docs-check
```

Final closeout still runs the repository Makefile gates because the checker is
Go code.

## Rollback

Rollback removes the narrow instruction validator and restores the prior
instruction text. It requires no data migration, but it reopens the risk that
coding agents treat a retired imperative owner as current architecture.
