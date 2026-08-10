# Product Evolution Guideline

**Status:** current
**Last verified:** 2026-07-17

> **Ownership:** product-evolution scope, reference-evidence levels, workflow,
> and definition of done for tracked slices

Documentation ownership, naming, lifecycle, and iteration closeout rules are
defined in
[`documentation-policy.md`](../contributing/documentation-policy.md).

## 1. Product objective

The normative project objective and decision hierarchy live in
[`PROJECT_DIRECTION.md`](../../PROJECT_DIRECTION.md). This tracker preserves
the useful results of the original Claude Code migration while supporting
multi-reference and project-native evolution.

Claude Code Ripe remains the broadest compatibility baseline, especially where
its observable lifecycle protects user work. It is not the sole specification
or an automatic backlog. Accepted work should improve:

- provider portability through Eino and provider capability adapters;
- configuration and local setup;
- native terminal usability and presentation;
- ACP, MCP, and other server protocol integration.

Work may preserve Claude behavior, adapt it, combine it with another design,
or implement a project-native contract. Every choice remains accountable for
user-visible consequences, compatibility, safety, and verification.

## 2. Scope classifications

Every Claude Code Ripe file tracked by `manifest.yaml` must eventually receive
exactly one ledger classification. This is reference-accounting scope, not the
complete product roadmap:

| Classification | Meaning |
|---|---|
| `required` | An explicitly accepted observable behavior should remain compatible with the reference |
| `adapted` | Preserve the accepted user outcome through a provider-neutral, Go/Eino-native, combined, or project-owned design |
| `excluded` | Deliberately omitted because it is vendor-internal, unreachable, deprecated, unsupported-platform-only, or not useful to this product |
| `pending_review` | Not yet classified; this is unfinished inventory work |

Typical `adapted` areas include Anthropic request/cache fields, model aliases,
usage events, authentication adapters, and React/Ink UI implementation.

Typical `excluded` areas include billing, referrals, GrowthBook experiments,
Anthropic employee-only tools, Claude.ai account services unrelated to model
calls, and verified disabled stubs.

An exclusion requires a reason and source reference in `manifest.yaml`. Do not
classify a difficult feature as excluded merely to improve the percentage. A
project-native feature normally lives in architecture and planning documents;
it does not need a synthetic Claude manifest entry.

## 3. Implementation statuses

Scope and implementation status are separate axes.

| Status | Required evidence |
|---|---|
| `not_started` | No meaningful target implementation |
| `minimal` | Types, stubs, or a narrow happy path exist |
| `partial` | Meaningful behavior exists, but branches, entrypoints, recovery, persistence, or tests remain |
| `implemented_unverified` | Intended behavior appears complete in Go, but required scenario or compatibility verification is missing |
| `done` | Accepted behavior, entrypoint wiring, focused tests, and all required compatibility or project-native scenarios exist |
| `blocked` | A concrete external dependency prevents progress and is documented |

The following are not sufficient for `done`:

- a similarly named Go file or function;
- equal or greater line count;
- a registered command or tool name;
- compilation and ordinary unit tests alone;
- a historical implementation-plan checkbox.

## 4. Evidence requirements

Each status claim should identify current implementation evidence first. A
reference-derived proposal or report should additionally identify its reference
evidence and adoption decision. Manifest entries keep the existing
`required`/`adapted`/`excluded` ledger classification rather than inventing a
second product-roadmap schema:

1. user-visible problem or accepted outcome;
2. Go implementation file(s), production callers, and relevant symbols;
3. focused Go tests and negative paths;
4. applicable entrypoints: TUI, headless CLI, ACP, MCP, or library API;
5. relevant reference source and tests, when a comparison is part of the claim;
6. adoption decision and known compatibility consequences.

Prefer symbol references over line numbers because line numbers become stale.
Examples:

```yaml
reference:
  - path: src/query.ts
    symbols: [query]
implementation:
  - path: engine/query.go
    symbols: [QueryLoop]
tests:
  - engine/query_tool_calls_test.go
entrypoints: [tui, headless, acp]
```

## 5. Required workflow for an evolution slice

1. Select the `Ready` slice from `PLAN.md`; use `queue.yaml` to distinguish
   hard dependencies, promotion gates, and risk priority.
2. Read current Go production wiring, helpers, tests, and supported entrypoints.
3. Compare only references that provide evidence for the unresolved decision.
4. Choose `preserve`, `adapt`, `combine`, `project-native`, `reject`, or
   `defer`; add or update manifest mapping only when Claude reference inventory
   changed.
5. Record observable contracts, event ordering, error paths, persistence,
   cancellation, compatibility, and rollback behavior.
6. Implement the smallest coherent Go slice. Use Eino/ADK as infrastructure,
   not as a replacement for product policy.
7. Wire every applicable entrypoint. A TUI-only or engine-only implementation is
   partial when other supported entrypoints require it.
8. Add focused tests plus compatibility scenarios when the decision preserves,
   adapts, or combines reference behavior.
9. Run `make fmt`, `make lint`, `make test`, and `make build`.
10. Update `manifest.yaml`, `STATUS.md`, and the owning architecture document
   only when their evidence changed.
11. Commit implementation, tests, and status changes in minimal-purpose units.

After reference files are added, removed, or renamed, synchronize the ledger:

```bash
go run ./scripts/migration_manifest.go sync
go run ./scripts/migration_manifest.go check
```

The sync command preserves reviewed fields. New files are deliberately added as
`pending_review`/`not_started`; this is not a claim that they belong in scope.

## 6. Provider adaptation rules

- Core runtime code must consume provider-neutral capabilities.
- Context-window resolution is provider-neutral. Known models use verified
  capability metadata, and explicit `[1m]`/`[2m]` model suffixes override local
  context accounting for any provider. Do not restore Anthropic-only gates around
  extended context; doing so would incorrectly compact or reject long sessions on
  providers such as DeepSeek and Gemini.
- Provider-specific fields belong in provider adapters.
- Unsupported capabilities need explicit fallback or explicit errors.
- The same core scenarios should run against multiple provider adapters.
- Anthropic Messages API behavior remains relevant where it affects streaming,
  tools, thinking, caching, usage, limits, and errors; Anthropic product APIs do
  not automatically belong in scope.

## 7. TUI and protocol rules

- Preserve or improve accepted user workflows; do not copy React component
  structure.
- Use the established Go TUI stack (Bubble Tea, Bubbles, Lip Gloss, Glamour,
  and ANSI helpers) unless a documented limitation requires a change.
- Translate React state and hooks into explicit Bubble Tea state transitions and
  engine events; do not recreate a hidden React-style lifecycle in Go.
- Commands are complete only when their side effects and interaction flows work.
- Engine features must expose sufficient events/state for TUI and server modes.
- ACP and MCP paths must receive cancellation, permission, session, and error
  behavior appropriate to their protocols.
- Visual quality and terminal interaction are product requirements, not optional
  polish or a parity-percentage adjustment.
- Follow [`architecture/tui/README.md`](../architecture/tui/README.md) for
  current TUI ownership and data flow, and the TUI contracts for stable
  behavioral boundaries.

## 8. Progress calculation

Report these independently:

1. reference-ledger inventory coverage;
2. current product capability and production wiring;
3. compatibility-scenario coverage for intentionally preserved behavior;
4. TUI/user-workflow quality and reliability;
5. protocol/server and provider compatibility;
6. project-native outcome, safety, performance, and maintenance evidence.

Do not combine these into one product-completeness or parity percentage.
Reference inventory size, line count, and command count are descriptive only.
When confidence is low, state the evidence boundary rather than increasing
precision.

## 9. Documentation ownership

- `manifest.yaml` owns exhaustive classifications, mappings, and evidence.
- `STATUS.md` owns current verified human-readable state.
- `REMAINING.md` owns unresolved differences.
- `queue.yaml` owns machine-readable active/deferred queue state, dependencies,
  promotion gates, and risk priority.
- `PLAN.md` owns the checked human projection plus selection and closeout rules.
- subsystem architecture, maps, and contracts own their documented boundaries.
- [`history/`](history/README.md) owns completed narrative.

The full update matrix and freshness rules are in
[`documentation-policy.md`](../contributing/documentation-policy.md). Link to
the owning document instead of
copying volatile counts or milestone prose.
