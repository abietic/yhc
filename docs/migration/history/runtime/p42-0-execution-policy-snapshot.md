# P42.0 Execution Policy Snapshot

**Status:** historical
**Completed:** 2026-08-02
**Adoption:** `project-native`

> **Ownership:** completion record for the behavior-preserving P42.0 identity
> seam. The accepted contract remains
> [`p42-host-execution-containment.md`](../../plans/p42-host-execution-containment.md).
> Current lifecycle behavior belongs to the linked architecture owners. G28
> remains open because this slice implements no OS sandbox.

## Outcome

The runtime now resolves one immutable `ExecutionPolicySnapshot` before a
composition root can launch guest or configured-extension processes. P42.0
supports exactly one truthful adapter: `danger-full-access` with state
`disabled` and adapter family `ambient-host`. It adds no public selector,
warning, filesystem or network restriction, credential projection, resource
limit, or supported-platform claim.

The snapshot deep-copies and canonicalizes caller input, validates the only
implemented profile/state/adapter combination, and exposes a stable SHA-256
digest plus a path- and secret-free diagnostic. Root and child lineage are
part of that digest. Child Agent construction derives exact ambient authority
with an opaque child identity; a requested change to roots, network,
environment names, credential identities, resources, profile, or lineage is
rejected.

## Runtime Binding

TUI, Plain, ordinary headless, headless Goal, and ACP resolve the disabled
snapshot before `QueryEngine` construction. `QueryEngine` carries it into
tool contexts and binds the same identity to its persistent `ShellManager`,
shell-hook executor, and configured MCP manager. ACP session stdio descriptors
receive the snapshot before transactional connection preparation. Session MCP
reload retains it.

Persistent Bash stores the identity before `bash` starts. Background dispatch
uses the manager identity and the captured context. Shell hooks bind the same
identity independently; async hooks copy the dispatch snapshot into their
owned lifecycle and completion record. Configured stdio MCP transports retain
the manager-bound identity before process start while preserving their
existing process-tree owner. A child QueryEngine receives an exact-authority
derived snapshot; already-running parent MCP generations remain parent-owned.

The standalone MCP allowlist still contains only Task/Todo lifecycle tools.
Its source test now explicitly guards Bash, BashOutput, KillShell, and Agent
from registration, so the independent server does not invent a process-policy
dependency.

## Compatibility And Safety Evidence

Focused tests prove canonical digest stability, caller and returned-copy
immutability, redacted diagnostics, nil and invalid-pair rejection, and closed
child derivation. Engine fixtures prove TUI, Plain, headless, Goal, and ACP
identities are distinct and that Default, Plan, Accept Edits, Auto, bypass,
and an exact Bash grant leave the selected digest unchanged.

Bash, synchronous and asynchronous hooks, child derivation, MCP manager
ownership, and stdio transport tests prove pre-spawn binding and late
replacement rejection. Race runs cover the immutable core, concurrent child
derivation, async hook capture, shell recovery/launch, MCP ownership, and
stdio process construction. The existing lifecycle suites confirm that
commands, environment inheritance, timeouts, output, cancellation, and
process-tree cleanup remain unchanged.

The repository gates `make fmt`, `make lint`, `make lint-new`, `make test`,
and `make build`, documentation checks, migration manifest/ledger checks, and
`git diff --check` passed for the candidate.

Independent second-line review found three pre-spawn identity defects: an
unstarted hook executor could be rebound across roots, Shell's first policy
commit could race a concurrent bind, and a literal child executor could invent
parent authority. Parent closeout then found the same rebinding defect in an
unstarted MCP manager. The fixes freeze all three process owners at first bind,
revalidate and commit Shell identity under its launch lock, and reject child
derivation without an explicit parent. Deterministic mismatch/no-spawn tests,
parent-only MCP reuse tests, and follow-up race review passed with no remaining
finding.

## Current Owners And Rollback

- [`engine/containment`](../../../../engine/containment/policy.go) owns immutable policy
  construction, identity, diagnostics, context binding, and child derivation.
- [`QueryEngine`](../../../../engine/engine.go) owns root binding and propagation.
- [`ShellManager`](../../../../tools/bash_shell.go),
  [`hooks.Executor`](../../../../engine/hooks/hooks.go), and
  [`MCPToolManager`](../../../../tools/mcp_tool.go) own their pre-spawn binding.
- [`stdioProcessTransport`](../../../../engine/mcp/stdio_transport.go) retains
  the configured-extension identity while existing platform files own process
  trees and cleanup.

Rollback removes only this in-memory identity seam. It writes no durable state
and must not remove existing process-group or Job Object cleanup. G28 remains
open until a later accepted platform/profile adapter enforces every required
axis and passes real escape tests.
