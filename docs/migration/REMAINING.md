# Remaining Product and Evolution Gaps

**Assessed:** 2026-08-19
**Status:** gap-inventory

> **Ownership:** reproduced unresolved product gaps. Accepted execution order
> belongs in [`PLAN.md`](PLAN.md), machine queue state in
> [`queue.yaml`](queue.yaml), verified current capabilities in
> [`STATUS.md`](STATUS.md), and completed work in
> [`history/`](history/README.md). Speculative options and explicit reference
> exclusions do not belong in this inventory.

## Verified Current-Implementation Gaps

An ID records one observable mismatch; it is not acceptance or priority. The
linked current owner and reproduction remain authoritative until the gap closes
or newer evidence disproves it.

| ID | Observable gap | Current evidence and consequence | Intake state |
|---|---|---|---|
| G2 | `/rewind` has no production runtime dependency and remains unavailable. | P39.0 froze complete-capture, logical-record, conflict/containment, confirmation/permission, partial-retry, entrypoint, migration/deletion, and rollback behavior in a disposable-workspace oracle. It intentionally added no schema, content store, writer, API, handler, or automatic restoration. See the [contract](plans/p39-workspace-recovery-contract.md), [closeout](history/runtime/p39-0-workspace-recovery-contract.md), and [`commands.md`](../architecture/capabilities/commands.md). | No accepted successor. |
| G14 | The separated permission reviewer remains advisory; the actor-model classifier is authoritative. | The default-off minimized reviewer route and bounded redacted journal cannot authorize actions. P50.2 makes retained-window latency samples truthful, and P50.3 isolates journal sink failure from permission authority while exposing evidence loss, but the provider-free report still lacks representative non-zero workload, latency, direct-human, and versioned-corpus evidence. See [`permissions.md`](../architecture/capabilities/permissions.md) and the [readiness decision](verification/p22-enforcement-promotion-readiness.md). | P44 is `defer`; no executable successor. |
| G28 | Some supported host-process authority remains outside an enforced envelope. | P51.1 gives model-issued Guest Bash and its descendants a real Darwin Seatbelt `workspace-write` binding with proven filesystem, network-denial, root-identity, descendant, wall-time, and bounded-output axes. P51.2 Core now consumes only that exact complete proof for ordinary Auto Bash and requires fresh `AllowOnce` for the narrow critical corpus. Missing, unsupported, failed, or drifted enforcement cannot supply proof-bound authority, and any attempted Guest launch fails before execution without ambient retry. Shell hooks and configured stdio MCP intentionally remain ambient, Guest environment credentials are inherited unchanged, and hard memory/FD/process-count limits remain absent, so aggregate state is still `degraded` and G28 stays open. See the [current P42.1 contract](plans/p42-host-execution-containment.md#p421-accepted-granular-proof-divergence), [P51.1 verification](verification/p51-1-darwin-guest-seatbelt.md), [P51.2 Core verification](verification/p51-2-auto-containment-admission.md), and [audit](reference/runtime/host-execution-containment-audit.md). | No accepted successor; G28 remains open after P51.2 Core. |

P49 closed G21 and G47 by default-enabling supported Goal composition roots
without inventing a numeric cap, allowing explicit unbudgeted execution, and
persisting exact provider-attempt admission identity. P47.1-P47.7 closed
G38-G41, P48.1-P48.5 closed G42-G46, and P50.1-P50.3 closed G48-G50 without
changing reviewer or permission authority. G2 has no accepted successor, G28
remains open after the completed P51.1 Guest and P51.2 Core permission
subsets, and G14 remains deferred without an executable queue row. The active
migration queue is empty.

## Gap Intake Record

Add a gap only with:

1. one observable mismatch on a supported entrypoint;
2. current source, production caller, and architecture-owner evidence;
3. a deterministic reproduction or focused negative test;
4. the consequence if it remains unresolved; and
5. a clear distinction between verified fact, inference, and product option.

Do not assign a P-number, estimate, or order here. Intake and adoption decisions
belong in a detailed plan; executable state belongs in `queue.yaml`.

## Closure Rule

Remove a gap only when:

1. the accepted observable behavior and compatibility consequences are
   documented;
2. every applicable entrypoint is wired;
3. focused regression plus required race, replay, PTY, performance, or
   end-to-end evidence passes;
4. current architecture, `STATUS.md`, one history record, and manifest evidence
   are updated only where their facts changed; and
5. required Makefile, docs, queue, manifest, and diff gates pass.

Closed narratives belong in current architecture and history, not in this
inventory. Rejected or speculative options remain discoverable through the
decision record or Git history without occupying the unresolved-gap owner.
