# P51.2 Proof-Bound Auto Bash Core

**Status:** historical
**Completed:** 2026-08-19
**Adoption:** `project-native`

> **Ownership:** completed Core delivery narrative for proof-bound Auto Bash
> and critical AllowOnce; current behavior belongs to architecture and operator
> documentation

## Outcome

P51.2 connected Darwin Guest containment proof to the QueryEngine-owned Auto
permission path. Ordinary canonical built-in Bash now skips the ordinary
permission request through the new automatic path only when its exact action
carries the complete available Seatbelt proof. Existing exact local/user allow
rules remain independent explicit user authority and never receive the
proof-bound admission marker.

A narrow literal critical `rm`/`rmdir` subset instead requires one fresh live
`AllowOnce`. Exact rules, persistent grants, Bypass, classifier, reviewer, and
request coalescing cannot supply that authority, while DontAsk fails closed.

QueryEngine binds the decision to canonical input, registry generation, Guest
policy/binding/generation, granular enforcement axes, and root identity.
ProjectGraph persists and validates the same constraint across first interrupt,
replay, cold restart, and resume. Plain, TUI, ACP, and foreground/background
Child paths project only the engine-owned choices.

The final dispatch rebuilds the action and revalidates the Guest identity
before registry acquisition. ShellManager repeats root identity validation
immediately before a process start or persistent-shell stdin submission. Drift
returns `sandbox_binding_expired` without ambient retry.

## Delivery boundary

This is the public master-native Core delivery. AppServer, Desktop, and Web UI
were intentionally not ported from the stacked Desktop candidate, are not
current P51.2 behavior, and have no presentation or settlement claim here. A
later accepted projection must consume the merged engine-owned constraint and
prove its own invariants.

Non-critical Default, Plan, AcceptEdits, DontAsk, Bypass, and Bubble semantics
remain unchanged. Unknown shell syntax is a critical-classifier non-match, not
a new block. Environment and `TMPDIR` remain unchanged.

G28 remains open because environment credentials, shell hooks, configured
stdio MCP, and hard memory/file-descriptor/process-count limits are still
outside the accepted enforced envelope.

## Evidence

The reproducible proof matrix and explicit claim limits are recorded in
[`p51-2-auto-containment-admission.md`](../../verification/p51-2-auto-containment-admission.md).
The accepted contract remains available in
[`p51-2-auto-containment-admission.md`](../../plans/p51-2-auto-containment-admission.md),
but it no longer owns active queue state.
