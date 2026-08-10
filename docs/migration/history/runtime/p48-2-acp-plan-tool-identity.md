# P48.2 ACP Plan Tool-Call Identity

**Status:** historical
**Closed gaps:** G43
**Completed:** 2026-08-07
**Adoption:** `preserve`

> **Ownership:** completion record for P48.2/G43; current behavior belongs in
> [`acp-adapter.md`](../../../architecture/platform/acp-adapter.md) and
> [`permissions.md`](../../../architecture/capabilities/permissions.md)

## Outcome

ACP now converts one non-empty engine `PermissionPromptRequest.ToolUseID` to a
typed SDK ID and reuses it for the initial Plan choice, every Back retry, and
bypass confirmation. The existing lifecycle ledger uses that same invocation
identity for its de-duplicated start and exactly one canonical terminal
update.

Plan RequestID, revision, reviewed digest, target, settlement, and the single
absolute interaction deadline remain unchanged engine-owned facts. Blank Plan
tool identity fails closed before snapshot I/O or client permission I/O.

## Compatibility And Rollback

Plan choices, option labels, two-step bypass confirmation, Back behavior,
deadline budget, timeout/cancellation/transport failures, engine execution
authorization, and non-Plan permission fallback are unchanged. No persisted
state, public schema, ACP version, or transcript data changes.

A squash revert restores only synthetic Plan permission IDs. It requires no
data rollback, but reopens G43 and again splits one tool invocation across
unrelated ACP identities.

## Evidence

The closeout tree contains a real engine-to-ACP ordered trace for approve,
bypass, and reject; structured target and Back reuse checks; blank-ID
pre-snapshot failure; deadline, timeout, cancellation, and transport failure
coverage; focused race; and the full ACP package. Detailed repository,
contract, race, and official SDK-wire commands and limitations are in the
[verification record](../../verification/p48-2-acp-plan-tool-identity.md).
All listed local commands pass on the closeout tree.

No remote-CI, live-provider, ACP v2, new authorization owner, or non-Plan
missing-ID behavior claim is made.
