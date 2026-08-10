# Project Direction

**Status:** current
**Last verified:** 2026-07-17

> **Ownership:** product objective, reference-evidence hierarchy, adoption
> decisions, and long-term evolution principles for YHC

## Product objective

YHC is an independent Go/Eino coding agent. Its objective is to preserve
the most useful and difficult-to-reproduce capabilities demonstrated by Claude
Code Ripe, combine verified strengths from other mainstream coding agents, and
develop project-native capabilities where they improve real coding work.

The project is no longer governed by a 1:1 porting objective. Compatibility with
Claude Code Ripe remains valuable, especially for mature tool, permission,
recovery, compaction, session, and Agent workflows, but reference similarity is
evidence rather than the product goal.

The target is a coding agent that is:

- effective at understanding, changing, and verifying real repositories;
- safe and predictable around tools, permissions, cancellation, and recovery;
- durable across long sessions, restarts, and multi-Agent work;
- portable across providers and usable through TUI, CLI, ACP, and MCP surfaces;
- extensible without turning every reference feature into permanent core
  complexity; and
- recognizably YHC through project-owned runtime contracts and workflows.

## Decision hierarchy

Use the following order when evidence or implementations disagree:

1. **Accepted user outcome and product value** determine whether work belongs in
   scope.
2. **Safety, integrity, and recoverability invariants** constrain every design;
   popularity or reference fidelity cannot override them.
3. **Current YHC source, production wiring, and tests** define what the
   product does today and what a change may regress.
4. **Verified reference behavior** supplies alternatives, edge cases, and
   compatibility expectations; no single reference automatically owns the
   target design.
5. **Go/Eino fit, maintainability, performance, and operational cost** decide
   between alternatives that deliver comparable user value.

Documentation, a reference comment, a similarly named symbol, or a parity
percentage cannot override current runtime evidence or create product scope.

## Reference roles

Reference projects are a comparative evidence set, not a precedence chain:

| Reference | Primary value to YHC | Boundary |
|---|---|---|
| Claude Code Ripe | Broad capability baseline, mature lifecycle ordering, difficult edge cases, and compatibility scenarios | Not an automatic feature backlog or mandatory architecture |
| Eino and Eino ADK | Native Go Agent mechanisms, model/tool orchestration, middleware, interruption, and runner lifecycle | Framework capability does not define product policy or outward contracts |
| Codex | Typed thread/event identity, replay, multi-Agent control, and multi-client protocol patterns | App-server or persistence architecture requires a measured local need |
| Crush | Go/Bubble Tea composition, service boundaries, and terminal-native implementation patterns | Similar language or UI stack is not sufficient reason to copy ownership |
| OpenCode | Extensibility, session/workspace patterns, permissions, and product integration ideas | Experimental or high-complexity patterns require independent evidence |
| Pi and other focused agents | Small-kernel composition and extension boundaries | Minimalism must not remove required safety, durability, or workflows |

Add or refresh a reference only for a named product question. Record its
snapshot and do not present local research as upstream-current fact.

Existing decision evidence includes the
[`whole-runtime comparison`](docs/migration/reference/runtime/agent-runtime-comparison.md),
[`modern TUI synthesis`](docs/migration/reference/tui/modern-coding-agent-synthesis.md),
and
[`Eino kernel convergence audit`](docs/migration/reference/runtime/query-engine-eino-convergence-audit.md).

## Adoption decisions

Every reference-derived proposal must end in one explicit decision:

| Decision | Meaning |
|---|---|
| `preserve` | Keep an existing observable contract because compatibility and user value justify it. |
| `adapt` | Preserve the useful outcome through a Go/Eino-native or provider-neutral design. |
| `combine` | Synthesize complementary behavior from multiple references behind one project-owned contract. |
| `project-native` | Implement a requirement owned by YHC rather than by a reference. |
| `reject` | Do not adopt the behavior because value, safety, complexity, or platform fit is insufficient. |
| `defer` | Evidence or priority is insufficient; this is not accepted backlog. |

Intentional divergence is normal. Document the user-visible consequence, the
reason, compatibility impact, and validation evidence. Do not maintain duplicate
legacy and new owners indefinitely merely to avoid declaring a decision.

## Evolution workflow

1. Define the user problem, affected entrypoints, and measurable success.
2. Trace current source, callers, tests, failures, persistence, and recovery.
3. Compare only the references relevant to the unresolved decision.
4. Produce an evidence matrix and choose one adoption decision.
5. Freeze one project-owned observable contract and rollback boundary.
6. Implement the smallest coherent slice with focused tests and compatibility
   scenarios only where the decision preserves, adapts, or combines reference
   behavior.
7. Update only the architecture, gap, plan, reference, history, or manifest
   owners whose facts changed; do not duplicate facts to make every layer move.
8. Run the repository gates after the final code or contract change.

The historical `docs/migration/` path remains the project evolution ledger for
continuity. Its manifest measures classified Claude Code Ripe evidence; it does
not measure total product completeness or define the future feature backlog.

## Non-goals

- Maximizing parity percentages, mirrored files, commands, or lines of code.
- Treating every upstream change or hosted/vendor-specific surface as scope.
- Replacing working project contracts solely because another agent is newer or
  more popular.
- Hiding regressions behind an `adapted` label without tests and consequences.
- Adding product-native features without an owner, entrypoint plan, failure
  semantics, and verification path.
