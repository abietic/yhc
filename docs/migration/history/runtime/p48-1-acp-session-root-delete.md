# P48.1 ACP Observed Session-Root Delete

**Status:** historical
**Closed gaps:** G42
**Completed:** 2026-08-07
**Adoption:** `project-native`

> **Ownership:** completion record for P48.1/G42; current behavior belongs in
> [`acp-adapter.md`](../../../architecture/platform/acp-adapter.md) and
> [`sessions.md`](../../../architecture/state/sessions.md)

## Outcome

ACP now keeps one synchronized process-local locator from Session ID to a
canonical project root. Successful new, resume, load, fork, and returned list
rows record effective roots only after their existing success boundary. Close
and Agent shutdown do not erase correlation needed by the normal
close-then-delete sequence.

Inactive delete still holds the existing lifecycle and active-registry locks.
It rejects an active target, resolves the exact observed root or canonical
default fallback, and passes only the resulting transcript directory to the
unchanged `engine/session.DeleteSession` owner. Success and idempotent absence
forget one matching exact observation; every other error retains it.

Different canonical roots for the same ID make the locator monotonically
ambiguous. Delete then returns a privacy-safe typed Session conflict and
touches neither tree. Canonical clean and symlink aliases remain one root.

## Compatibility And Rollback

ACP v1 request/response shapes, default-CWD deletion for unknown IDs, opaque
safe Session IDs, active-session rejection, owned-sidecar containment, and
non-`ErrNotExist` failures are unchanged. There is no durable/global catalog,
home-directory scan, public API, schema migration, or second filesystem owner.

A squash revert removes the locator and observation calls and restores
default-CWD-only targeting. It requires no data rollback, but reopens G42 and
allows cross-project deletion to false-succeed again.

## Evidence

Public cross-CWD new/close/delete, fresh-Agent list reconstruction, same-ID
multi-root no-mutation, locator alias/ambiguity/forget/concurrency, existing
delete compatibility, focused race, full ACP, Session contract, repository
race, official ACP SDK v1.3.0 wire, all four Makefile gates, docs/queue/manifest,
and diff checks pass on the closeout tree. Detailed commands and limitations
are in the [verification record](../../verification/p48-1-acp-session-root-delete.md).

No durable global lookup, cold cross-project delete without observation,
multi-process coordination, remote CI, or live-provider claim is made.
