# P51.2 Proof-Bound Auto Bash Admission

**Status:** historical
**Accepted:** 2026-08-13
**Adoption:** `project-native`
**Gap:** G28 remains open

> **Ownership:** executable contract for proof-bound ordinary Auto Bash,
> narrow critical live AllowOnce, durable constraint propagation, and final
> zero-submission revalidation

## Outcome

P51.2 may remove the ordinary Auto-mode Bash permission prompt only when
QueryEngine binds the exact canonical action to the complete Darwin Guest
proof delivered by P51.1. Literal critical `rm` and `rmdir` targets instead
require a fresh live decision for that exact invocation, and only `AllowOnce`
may authorize them.

The slice is implemented and removed from the active queue. Current behavior
belongs to architecture and operator documentation; reproducible results are
owned by the [verification record](../verification/p51-2-auto-containment-admission.md)
and delivery history by the [closeout](../history/runtime/p51-2-auto-containment-admission.md).

## Intake evidence

P51.1 supplies an available Darwin Guest binding with the accepted granular
axes. The public Desktop candidate supplies typed AppServer permission
presentation, so P51.2 includes AppServer and Web UI.

The accepted design is
[`Proof-Bound Auto Bash Public Forward-Port`](../../superpowers/specs/2026-08-13-proof-bound-auto-bash-forward-port-design.md).
P51.1 implementation and real Darwin evidence remain in the
[`Darwin Guest verification`](../verification/p51-1-darwin-guest-seatbelt.md).

## Observable contract

The slice is complete only when all of these outcomes hold:

1. Ordinary canonical Bash in `ModeAuto` runs without a permission request
   only when the exact action carries the complete P51.1 Darwin Guest proof.
2. A literal critical `rm` or `rmdir` target always receives a fresh live
   request for that exact invocation. Only `AllowOnce` may authorize it.
3. Tool selection, schema and custom validation, Plan containment, and explicit
   deny or ask retain authority ahead of the new shortcut. Incomplete or
   unavailable Guest proof cannot supply proof-bound admission and instead
   uses the existing Auto fallback.
4. Rules, grants, Bypass, DontAsk, classifier or reviewer output, and request
   coalescing cannot authorize a critical request. Bypass must still obtain
   the live `AllowOnce`; DontAsk denies because it cannot prompt.
5. ProjectGraph checkpoint, restart, replay, and resume preserve the decision
   constraint and reject stale, mismatched, or forged decisions.
6. Plain, TUI, ACP, foreground and background Child Agents, AppServer,
   Desktop, and Web UI project only the engine-permitted choices and return to
   the same engine settlement authority.
7. Action, root, proof, binding, ShellManager, or registry-generation drift
   returns `sandbox_binding_expired` before acquisition, process start, or
   persistent-shell submission and never retries with ambient authority.
8. Guest Bash continues to inherit the original environment and `TMPDIR`.
   P51.2 makes no credential-isolation or hard resource-limit claim.
9. Public repository, migration, race, real Darwin, Desktop, and publication
   gates pass on one clean committed candidate tree.

## Exact Guest proof

The detached execution identity is created only from the QueryEngine-owned
Guest binding and ShellManager. Tool input, hook output, model output, prompt
responses, settings, and protocol clients cannot create or broaden it. The
identity binds:

- canonical built-in `Bash` identity and canonical input;
- tool-registry capability generation;
- Guest process class;
- policy, binding, and adapter capability generations;
- Darwin Seatbelt with the `workspace-write` profile;
- the truthfully `degraded` aggregate state and ambient-environment mode;
- adapter, runtime, and combined enforcement axes; and
- canonical root identity.

Complete proof requires filesystem read, filesystem write, network denial,
root identity, descendant confinement, descendant cleanup, wall time, and
bounded output. Memory, file-descriptor, and process-count axes remain absent.
The aggregate `degraded` label alone never authorizes Auto execution.

## Decision order

Every supported entrypoint uses this QueryEngine-owned order:

1. Construct the registered canonical action and capture its current Guest
   execution identity.
2. Reject unselected or invalid Guest Bash before permission evaluation.
3. Apply Plan containment and explicit deny rules.
4. Recognize the narrow literal critical subset. A match bypasses rules,
   grants, Bypass mode, classifiers, reviewer output, and coalescing as
   positive authority and requires a constrained live `AllowOnce`. DontAsk
   denies without opening an interaction.
5. For non-critical actions, retain the existing rule, mode, exact-grant,
   read/search, memory, and contained Write/Edit ordering.
6. In `ModeAuto`, allow an exact non-critical Bash action without classifier
   or prompt only when its detached Guest proof is complete.
7. Otherwise retain the current Auto capability gate, reviewer shadow,
   classifier, prompt fallback, and denial behavior.
8. After a PreToolUse input rewrite, rebuild the canonical action and repeat
   the complete decision. Authority never survives changed input.
9. Immediately before `AcquireExecution`, rebuild the action, recapture the
   root, revalidate ShellManager's pinned identity, and compare every bound
   field. Drift fails with `sandbox_binding_expired` and zero execution.

## Narrow critical corpus

The recognizer accepts only a tokenizable subset of simple Bash commands and
separators. It recognizes literal `rm` or `rmdir`, optionally through literal
`command` or `builtin`, and evaluates literal targets after option processing.
It matches:

- `/` and a direct child of `/`;
- a volume root or direct volume child under `/Volumes`;
- the exact user home, `~`, or a literal path resolving to it; and
- `*` or a terminal literal directory `/*` all-entry target, including a
  relative parent resolved from the canonical workspace root.

Quotes and ordinary escapes may preserve a literal target. Variable expansion,
command substitution, unsupported globs, pipes, redirection, unsupported
operators, and malformed quoting produce an unknown non-match. Unknown syntax
is not a sandbox denial or a new prompt reason; it follows the same proof-bound
or existing Auto fallback as other non-critical Bash.

## Decision constraint

The engine owns a typed constraint whose zero value preserves existing
permission behavior and whose constrained value permits only `AllowOnce`,
denial, cancellation, or timeout. It is part of the canonical request, event,
ProjectGraph interrupt, durable runtime decision, and every binding digest.

For a constrained request, `PermissionPresentation.GrantScopes` projects only
`AllowOnce`. Presentation is not authority: settlement independently rejects
session or always decisions, disables positive grant reuse and production,
and prevents permission-request coalescing. One critical invocation always
owns one fresh human decision round.

## Entrypoint matrix

| Boundary | Accepted future requirement |
|---|---|
| Ordinary coordinator | Clone and validate the constraint, disable grant coalescing, and enforce it again during settlement. |
| ProjectGraph | Persist the constraint in interrupt and runtime decision state; bind it into invocation and decision digests; retain it through cold restart and replay. |
| Plain, headless, and Goal | Reconstruct the complete request identity and display or accept only decisions permitted by the constraint. |
| TUI | Carry the constraint through thread attention and the active permission dialog; hidden options and accelerators cannot return a persistent decision. |
| ACP | Advertise only `AllowOnce` plus rejection for a constrained request and reject an impossible client result. |
| AppServer | Include the constraint in broker cloning, callback/event conflict digest, public projection, and exact settlement validation. |
| Desktop and Web UI | Render only validated server scopes and accept the available one-scope presentation without inferring authority from tool text. |
| Foreground and background Child Agents | Derive an equal-or-narrower Guest proof and the same constraint in the child QueryEngine; parent adapters cannot broaden it. |

AppServer request identity must distinguish constrained and unconstrained
requests that otherwise share outward tool identity. Conflicting callback and
event projections fail closed rather than coalescing.

## Failure contract

| Condition | Required result | Forbidden result |
|---|---|---|
| Guest binding unavailable | No proof-bound shortcut; existing Auto fallback may ask or deny, and any eventual launch fails with the existing typed sandbox-unavailable result | Proof-bound admission, process start, or ambient execution retry |
| Critical request receives session or always | Bounded constraint denial | Persistent grant or execution |
| Durable decision is stale, mismatched, or forged | Reject without settlement | Resume or coalescing |
| Action, root, proof, binding, or registry identity drifts | `sandbox_binding_expired` before acquisition/submission | Shell write, process start, or ambient retry |
| Non-critical Auto Bash has incomplete proof | Existing classifier, prompt, or fail-closed path | Treat `degraded` as complete proof |
| Seatbelt denies an operation after spawn | Normal sandboxed command failure | Automatic authority expansion |

## Verification and promotion

Implementation is test-first and must retain deterministic evidence for:

- complete and incomplete proof matrices;
- the literal critical and unknown-syntax corpora;
- rules, grants, Bypass, DontAsk, classifier, reviewer, and coalescing attempts
  against a critical request;
- routine-to-critical and routine-to-routine hook rewrites;
- ProjectGraph live interrupt, cold restart, replay, and forged persistent
  decisions;
- Plain, TUI, ACP, AppServer, Desktop/Web UI, and both Child Agent modes;
- AppServer digest conflict and forged protocol responses;
- critical Bypass requiring live `AllowOnce` and critical DontAsk denying;
- pre-acquire action/root/proof/binding/registry drift with zero executor
  acquisition;
- ShellManager submission drift with zero stdin writes and zero new process
  starts; and
- unchanged environment and `TMPDIR` propagation.

Only real Darwin Seatbelt execution proves containment. At least one
interactive and one non-interactive supported entrypoint must demonstrate an
ordinary in-workspace write without a permission request and a denied
parent-root escape. Prompt counts are deterministic fixture evidence, not a
reviewer-accuracy or universal-safety claim.

Promotion additionally requires focused race tests, relevant PTY and Desktop
checks, full repository gates, clean committed-tree evidence, and the public
publication verification on the same candidate tree. Remote CI, unsigned
Desktop package smoke, and physical UI acceptance remain separately reported
evidence classes.

## Compatibility and non-goals

P51.2 intentionally changes ordinary proof-bound Auto Bash prompt frequency
and the narrow critical Bypass/DontAsk outcome. It does not change non-critical
Default, Plan, AcceptEdits, DontAsk, Bypass, or Bubble semantics beyond the
existing required Guest-availability rule.

It does not add Linux or Windows containment, environment sanitization, hard
memory/descriptor/process limits, reviewer enforcement, classifier redesign,
permission-driven sandbox escalation, ambient retry, or a client-accessible
sandbox-disable field. Shell hooks and configured stdio MCP remain ambient and
cannot contribute proof.

## Persistence and rollback

The constraint is an additive bounded typed value whose zero value preserves
old requests. Checkpoint and replay validate unknown or malformed values
fail-closed. No path migrates a stale decision into new authority.

Rollback removes the proof-bound Auto shortcut and the critical decision
constraint as one permission slice while retaining P51.1 Darwin Seatbelt
containment. A rollback must still refuse any unresolved constrained decision
that it cannot validate; it must not reinterpret it as an unconstrained grant.
