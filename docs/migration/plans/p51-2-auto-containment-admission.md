# P51.2 Proof-Bound Auto Bash Core

**Status:** historical
**Accepted:** 2026-08-19
**Completed:** 2026-08-19
**Adoption:** `project-native`
**Gap:** G28 remains open

> **Ownership:** executable Core contract for proof-bound ordinary Auto Bash,
> critical live AllowOnce, durable constraint propagation, and final dispatch
> revalidation

## Outcome

P51.1 constrains model-issued Guest Bash but deliberately leaves every
permission outcome unchanged. P51.2 may remove the ordinary Auto-mode Bash
prompt only when QueryEngine binds the exact canonical invocation to P51.1's
complete Darwin Guest proof. A narrow literal critical `rm`/`rmdir` subset
instead requires a fresh live decision for that invocation, and only
`AllowOnce` may authorize it.

This master-native Core slice was rebuilt from public `origin/master`. It did
not replay the stacked Desktop candidate or make that candidate's broader
surface claims. It is now removed from the active queue. Current behavior
belongs to architecture and operator documentation; reproducible results are
owned by the [verification record](../verification/p51-2-auto-containment-admission.md)
and delivery history by the
[closeout](../history/runtime/p51-2-auto-containment-admission.md).

## Intake evidence

P51.1 supplies the available Darwin Seatbelt Guest binding, immutable policy
and binding digests, adapter generation, granular adapter/runtime axes, root
identity, child derivation, and fail-closed unavailable behavior. QueryEngine
already owns canonical action validation, permission order, ProjectGraph
interrupts, settlement, and final registry acquisition. The Core can therefore
consume existing proof without creating a second policy or execution owner.

The prior two-stage design remains useful background in
[`Darwin Sandbox And Auto Permission Design`](../../superpowers/specs/2026-08-07-darwin-sandbox-auto-permission-design.md).
This contract supersedes its earlier critical-command wording: persistent
authority never satisfies the critical subset.

## Observable contract

The slice is complete only when all of these outcomes hold:

1. P51.2's new automatic no-prompt path applies only when an ordinary
   canonical built-in Bash action in `ModeAuto` carries the complete P51.1
   Darwin Guest proof. Existing exact local/user allow rules remain a separate
   explicit authority and never acquire the proof-bound admission marker.
2. A supported literal critical `rm` or `rmdir` target always receives a fresh
   live request. Only `AllowOnce` may authorize it.
3. Tool selection, schema and custom validation, Plan containment, and explicit
   deny retain authority. Incomplete or unavailable Guest proof cannot supply
   proof-bound admission and follows the existing Auto fallback.
4. Positive rules, grants, Bypass, classifier or reviewer output, and request
   coalescing cannot authorize a critical request. The exact-rule compatibility
   in item 1 does not apply to critical Bash. DontAsk denies without opening an
   interaction.
5. ProjectGraph checkpoint, cold restart, replay, and resume preserve the
   decision constraint and reject stale, mismatched, malformed, or forged
   decisions.
6. Plain, TUI, ACP, and foreground/background Child Agents project only the
   engine-permitted choices and return intent to the same engine settlement
   authority.
7. Action, root, proof, binding, ShellManager, or registry-generation drift
   returns `sandbox_binding_expired` before acquisition, process start, or
   persistent-shell submission, with no ambient retry.
8. Pre-tool input replacement rebuilds the canonical action and repeats the
   complete policy. An interactive rewrite from ordinary Bash into the
   critical corpus is denied before execution or grant persistence; the final
   command needs a new live constrained request. No admission survives changed
   input.
9. Guest Bash continues to inherit the original environment and `TMPDIR`.
   P51.2 makes no credential-isolation or hard resource-limit claim.

## Core entrypoint boundary

| Boundary | Required behavior |
|---|---|
| QueryEngine and tools | Own proof admission, critical precedence, final action/root/proof/registry revalidation, and the stable drift error. |
| ProjectGraph and runtime state | Persist and validate the constraint in request, decision, digest, checkpoint, replay, and pending-interaction projections. |
| Plain | Offer once/deny only for a constrained request and preserve the constraint when reconstructing ProjectGraph input. |
| TUI | Carry the constraint through thread attention and both permission dialogs; hidden choices and accelerators cannot return persistent intent. |
| ACP | Advertise allow-once/reject only and reject an impossible persistent client result. |
| Headless and headless Goal | Ordinary complete-proof Bash may run unattended; any critical request fails closed because no live adapter exists. |
| Foreground/background Child | Derive an equal-or-narrower Guest identity; ordinary proof and critical constraint use the child QueryEngine and live parent route. |

Deferred projection, not current behavior: AppServer, Desktop, and Web UI are
outside this master-native Core delivery. This record does not claim that those
surfaces implement, preserve, expose, or settle the P51.2 constraint. A later
accepted projection must consume the merged engine-owned constraint and
independently prove its presentation and settlement invariants.

## Exact proof and decision order

The detached execution identity is created only from the QueryEngine-owned
Guest binding and ShellManager. Tool input, hook output, model output, prompt
responses, settings, and protocol clients cannot construct or broaden it. The
identity binds canonical built-in Bash and input, registry generation, Guest
process class, policy/binding/adapter generations, Seatbelt workspace-write
profile, ambient-environment credential mode, exact adapter/runtime axes, and
canonical root device/inode.

Every supported Core entrypoint uses this order:

1. build and validate the registered canonical action and Guest identity;
2. enforce Plan containment and explicit deny;
3. recognize the narrow critical subset before positive rule, grant, mode,
   classifier, reviewer, or coalescing authority;
4. retain existing non-critical rule, mode, grant, read/write, and memory
   ordering, including exact local/user rules as independent explicit
   authority;
5. in Auto, admit exact non-critical Bash only with complete proof;
6. otherwise use the existing capability gate, classifier, prompt, or
   fail-closed fallback;
7. after a hook rewrite, restart at step 1; deny an interactive ordinary-to-
   critical rewrite so an earlier unconstrained response cannot authorize it;
   and
8. immediately before execution, rebuild and revalidate the full binding, then
   let ShellManager repeat the root check before start or stdin submission.

## Narrow critical corpus

The recognizer accepts only tokenizable simple Bash segments. It recognizes
literal `rm` or `rmdir`, optionally through literal `command` or `builtin`, and
evaluates literal targets after option processing. It matches root and direct
root children, volume roots/direct children under `/Volumes`, the exact home,
`~`, and `*` or terminal literal `/*` all-entry targets. Quotes and ordinary
escapes may preserve a literal target.

Variable or command substitution, unsupported globs, pipes, redirection,
background/subshell syntax, and malformed quoting are unknown non-matches.
They are not a new sandbox denial; they follow ordinary proof-bound or existing
Auto fallback behavior.

## Persistence, compatibility, and failure

`PermissionDecisionConstraint` is additive. Its zero value preserves existing
requests; `allow_once_only` permits AllowOnce, deny, cancellation, or timeout.
Unknown values fail closed. The constraint participates in ProjectGraph
invocation/decision identity, survives checkpoint JSON and runtime replay, and
is independently enforced during adapter normalization and engine settlement.

Non-critical Default, Plan, AcceptEdits, DontAsk, Bypass, and Bubble behavior
is unchanged. Critical Bash is the deliberate exception: Bypass still requires
live AllowOnce and DontAsk cannot authorize it. Environment credentials, shell
hooks, configured stdio MCP, Linux/Windows containment, and hard memory,
descriptor, or process-count limits remain outside scope, so G28 remains open.

| Condition | Required result |
|---|---|
| Guest proof incomplete or unavailable | No proof shortcut; existing Auto fallback, followed by fail-closed Guest launch if execution is attempted. |
| Critical response is session/always | Constraint denial; no grant and no execution. |
| Durable constraint or decision is stale, invalid, or mismatched | Reject without settlement or replay authority. |
| Action/root/proof/binding/registry drifts | `sandbox_binding_expired` before acquisition/submission. |
| Seatbelt denies after spawn | Normal sandboxed command failure; never broaden or retry ambient. |

## Verification and rollback

Deterministic evidence covers the proof matrix, critical corpus and precedence,
forged persistent choices, ProjectGraph persistence/restart, Plain/TUI/ACP
projection, hook rewrites, dispatch/submission drift, and foreground/background
child behavior. Real Darwin tests must demonstrate an ordinary in-root write
without a prompt and a denied child escape. Unsupported-platform skips are not
containment evidence.

Completion requires focused race tests, repository gates, documentation,
publication safety, and clean committed-tree evidence. Remote CI remains a
separate merge gate. Rollback removes the shortcut and constraint as one
permission slice while retaining P51.1 containment; an unresolved constrained
decision must fail closed rather than become an unconstrained grant.
