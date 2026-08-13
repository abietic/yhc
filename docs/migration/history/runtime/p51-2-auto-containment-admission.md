# P51.2 Proof-Bound Auto Bash Admission

**Status:** historical
**Completed:** 2026-08-13
**Adoption:** `project-native`

> **Ownership:** completed delivery narrative for proof-bound Auto Bash and
> critical AllowOnce; current behavior belongs to architecture and operator
> documentation

## Outcome

P51.2 connected Darwin Guest containment proof to Auto permission admission.
Ordinary canonical built-in Bash now skips the interactive permission request
only when its exact action carries the complete available Seatbelt proof. A
narrow literal critical `rm`/`rmdir` subset instead requires one fresh live
`AllowOnce`; persistent rules/grants, Bypass, classifier, reviewer, and request
coalescing cannot supply that authority, while DontAsk fails closed.

QueryEngine owns the decision and binds it to canonical input, registry
generation, Guest policy/binding/generation, granular enforcement axes, and
root identity. ProjectGraph persists and validates the same constraint across
first interrupt, replay, cold restart, and resume. Plain, TUI, ACP, AppServer,
Desktop, Web UI, and foreground/background Child paths only project the
engine-owned choices.

The final dispatch rebuilds the action and revalidates the Guest identity
before registry acquisition. ShellManager repeats root identity validation
immediately before a process start or persistent-shell stdin submission.
Drift returns `sandbox_binding_expired` without ambient retry.

## Compatibility boundary

Non-critical Default, Plan, AcceptEdits, DontAsk, Bypass, and Bubble semantics
remain unchanged. The critical subset is the explicit exception: Bypass still
requires live AllowOnce and DontAsk denies. Unknown shell syntax is a
non-match, not a new block. Environment and `TMPDIR` remain unchanged.

G28 remains open because environment credentials, shell hooks, configured
stdio MCP, and hard memory/file-descriptor/process-count limits are still
outside the accepted enforced envelope.

## Evidence

The reproducible proof matrix and explicit claim limits are recorded in
[`p51-2-auto-containment-admission.md`](../../verification/p51-2-auto-containment-admission.md).
The accepted contract remains available in
[`p51-2-auto-containment-admission.md`](../../plans/p51-2-auto-containment-admission.md),
but it no longer owns active queue state.
