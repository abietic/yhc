# Project Evolution Status

**Status:** current
**Reference ledger:** `claude-code-ripe` (TypeScript)
**Target:** `eino-agent` (Go)
**Last verified:** 2026-08-13

> **Ownership:** current verified evolution facts and volatile repository
> counts. Future work belongs in [`PLAN.md`](PLAN.md), unresolved behavior in
> [`REMAINING.md`](REMAINING.md), detailed current behavior in
> [`architecture/`](../architecture/README.md), and delivery narratives in
> [`history/`](history/README.md).

The reference ledger measures classified evidence, not product completeness.
Product scope and adoption rules are owned by
[`PROJECT_DIRECTION.md`](../../PROJECT_DIRECTION.md).

## Current Snapshot

Generated with `go run ./scripts/migration_scan -json` on 2026-08-08:

| Metric | Value |
|---|---:|
| Production Go files | 574 |
| Production Go lines | 234,244 |
| Product test Go files | 579 |
| Product test Go lines | 221,041 |
| Product Go packages | 63 |
| Registered tools | 41 |
| Compiled active core commands | 40 |
| Canonical compatibility traces | 12 |
| TUI production files / lines | 100 / 50,663 |
| TUI test files / lines | 138 / 39,577 |
| Reference files | 1,884 |

Counts are a dated inventory, not a quality score. Refresh them after source,
test, registration, or reference changes.

## Current Verified Boundaries

| Surface | Current owner and verified boundary | Open boundary |
|---|---|---|
| Query runtime | `QueryEngine` and ProjectGraph own model rounds, tool admission/execution, event ordering, cancellation, recovery, and supported entrypoint projection. See [`query-engine.md`](../architecture/runtime/query-engine.md). | A framework primitive may replace an owner only with observable equivalence and old-owner deletion. |
| Tools, permissions, and Guest execution | The registry owns canonical tool identity; QueryEngine owns permission policy, exact grants, rewrites, Plan admission, final dispatch binding, and the immutable Guest/hook/stdio-MCP process-class matrix. P50.1-P50.3 retain their revision, latency-denominator, and non-blocking audit guarantees. P51.1 now defaults Darwin model-issued Guest Bash to a real Seatbelt `workspace-write` binding, fails unavailable Guest enforcement before spawn without ambient retry, and leaves hooks, configured stdio MCP, environment inheritance, and every permission outcome unchanged. See [`tool-registry.md`](../architecture/capabilities/tool-registry.md), [`permissions.md`](../architecture/capabilities/permissions.md), and [`runtime-services.md`](../architecture/platform/runtime-services.md). | G28 remains open for ambient environment credentials, hooks/MCP, and missing hard memory/FD/process-count limits; G14 reviewer promotion remains deferred. P51.2 is `Ready` but not implemented, so current Auto Bash still prompts or fails closed. |
| Sessions and recovery | Append-only transcripts, immutable replay snapshots, staged restore, fork/delete containment, provider-free administration, and the P39 recovery conformance contract are verified. See [`sessions.md`](../architecture/state/sessions.md) and [`transcripts.md`](../architecture/state/transcripts.md). | G2 has no production workspace snapshot writer or rewind command. |
| Tasks and Agents | WorkBoard owns logical work; AgentRunner owns execution generations; exact WorkItem terminal transitions consult only links for the target items; TaskExplorer supplies the bounded read model, exact generation-bound switch target, and exact current-generation output/lineage reader. See [`tasks-and-agents.md`](../architecture/runtime/tasks-and-agents.md). | Wider stress or control surfaces require a reproduced outcome, not parity inventory. |
| Providers | Six provider-specific Eino adapters share route identity, capability admission, canonical rounds, complete-request context admission, explicit failed-attempt disposal, cross-entrypoint bounded overload notices, redacted diagnostics, and exact private Agentic reasoning-origin proof. See [`model-providers.md`](../architecture/platform/model-providers.md). | External SDK drift and live-provider modality remain release-time risks, not accepted backlog by default. |
| Goal lifecycle | Supported saved-root TUI and Plain composition roots default-enable Goal without a numeric cap. Explicit create/resume drives version-4 optional-budget state, exact provider-attempt accounting across restart, durable continuation, bounded headless execution, and negotiated ACP control. See [`query-engine.md`](../architecture/runtime/query-engine.md) and [`sessions.md`](../architecture/state/sessions.md). | No accepted Goal gap remains. Deterministic tests do not claim live-provider cost, representative adoption, remote CI, or physical-terminal evidence. |
| TUI | Bubble Tea projects engine state through App-owned layout, theme, notification, selection, display-cell, transcript, bounded Markdown-renderer, and terminal-lifecycle owners. Ctrl+T now combines exact mixed rows, local filters/focus, defensive cached WorkItem/execution overview/activity, and execution-only lazy transcript/output/lineage tabs whose results remain bound to exact selection, generation, request, tab, and cursor identity. See [`architecture/tui/`](../architecture/tui/README.md). | No accepted Task Explorer gap remains; PTY evidence proves protocol/process behavior, not physical font or pixel layout. |
| Desktop workbench | `yhc serve app` and the Electron host provide an authenticated loopback session workbench: selection replays durable chat history without resuming runtime, while first send activates the selected session. The renderer receives opaque workspace handles, safe Markdown text, typed interactions, and semantic Activity entries. See [`desktop-workbench.md`](../architecture/desktop-workbench.md). | Local package QA does not establish code signing, notarization, remote CI, or compatibility of an arbitrary external provider endpoint/tool dialect. |
| ACP | ACP v1 supports staged load/replay, bounded listing, process-local observed-root inactive deletion, exact Plan tool-call identity across permission rounds, string-valued exact tool `rawOutput` on live and replay paths, OS-aware stdio-MCP environment identity, command/config/mode projection, permission/tool lifecycle, rich user ingress, public assistant replay, and negotiated Goal control. The former private migration names are rejected as ordinary unknown methods. See [`acp-adapter.md`](../architecture/platform/acp-adapter.md). | No accepted ACP gap remains; ACP v2 and assistant media are unaccepted, not hidden backlog. |
| MCP | Project and ACP session managers publish exact tool generations; standalone MCP retains its intentionally narrow allowlist and policy. See [`mcp.md`](../architecture/capabilities/mcp.md). | Plugin-bundled activation and live management need separate accepted outcomes. |
| Evaluation | The opt-in P43.0 harness drives the public headless binary through a loopback scripted provider against two fresh Git repositories and grades task outcome, writes, policy, usage, residual state, cleanup, and redacted publication. | It is non-authoritative, outside `verify`/required CI, and does not prove live providers, recovery, OS containment, or other entrypoints. |

The exact unresolved inventory is [`REMAINING.md`](REMAINING.md). Queue state is
generated from [`queue.yaml`](queue.yaml) into
[`PLAN.md`](PLAN.md#execution-topology).

## Manifest State

| Classification | Files |
|---|---:|
| Required | 396 |
| Adapted | 406 |
| Excluded | 1,082 |
| Pending review | 0 |
| **Total reference files** | **1,884** |

All 817 accepted ledger-scope mappings are classified `done`; this means the
ledger evidence is explicit, not that every possible coding-agent feature is
implemented or desirable. `manifest.yaml` remains the machine owner.

## Scope Decisions

Reference-only PowerShell, hosted/internal Anthropic, billing/growth, voice,
and remote-trigger surfaces remain excluded. Go-specific `BashOutput` and
`KillShell` remain intentional additions. `/rewind` is unavailable rather than
falsely projected as working compatibility.

Candidate marketplaces, app-server/store rewrites, visual polish, automatic
multi-role orchestration, and framework replacement do not become gaps until a
supported-entrypoint problem is reproduced and accepted.

## Verification

Portable final gates:

```bash
make fmt
make lint
make test
make build
make docs-check
git diff --check
```

Risk-specific race, fuzz, PTY, protocol, and end-to-end packs are defined in
[`testing-strategy.md`](../contributing/testing-strategy.md). The real-repository
baseline is opt-in through `make eval-baseline`. Local gates, remote CI, PTY,
physical-terminal, and live-provider evidence must be reported separately.
