# P48.5 Remove Private Session Migration

**Status:** historical
**Closed gaps:** G46
**Completed:** 2026-08-07
**Adoption:** `reject`

> **Ownership:** completion record for P48.5/G46; current behavior belongs in
> [`acp-adapter.md`](../../../architecture/platform/acp-adapter.md) and
> [`sessions.md`](../../../architecture/state/sessions.md)

## Outcome

The ACP extension dispatcher no longer recognizes `_session/export` or
`_session/import`. Both names return the SDK's ordinary MethodNotFound response
with a nil result, before engine construction, Session registration, or project
filesystem mutation. The private token, checksum, import/export handlers,
migration-only errors, construction hook, and success-path tests were deleted
with their only production callers.

Public new, load, resume, fork, delete, and list behavior remains unchanged.
Negotiated Goal methods, private `_session/status`, shared Session conflict
semantics, and `engine/session`'s sanitized presentation export remain owned by
their existing boundaries. P48.5 therefore closes the misleading migration
promise without creating an alias, feature flag, archive schema, or second
restore lifecycle.

## Compatibility And Rollback

Clients that called either unadvertised private name now receive the same
MethodNotFound response as any unknown extension. This is the accepted
compatibility consequence of the `reject` decision: the former metadata token
was not a portable archive, and import bypassed supported Load initialization
and ordering.

A squash revert must restore the complete private surface and its tests as one
unit only if new compatibility evidence reverses that decision. Restoring only
dispatcher recognition or only token helpers would recreate a partial and
unsafe lifecycle. No durable schema or data migration is involved.

## Evidence

A test-first dispatcher regression exercised valid-looking requests for both
names. Before deletion it observed the former migration error path and an
import engine-construction attempt; after deletion it proves MethodNotFound,
nil results, an unchanged Session map, and a byte-and-metadata-stable sentinel
project tree. Production source scans prove that no private migration handler,
token, hook, or alternate path remains; the retained test literals record the
negative contract.

Focused retained-extension, full ACP, race, official SDK, contract, and
repository gates cover the surrounding boundary. Detailed commands and the
distinction between RED instrumentation and final source proof are in the
[verification record](../../verification/p48-5-remove-private-session-migration.md).
Remote CI remains a separate merge gate.
