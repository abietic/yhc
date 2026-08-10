# P49 Default-Enabled Budget-Optional Goal

**Status:** historical
**Closed gaps:** G21, G47
**Completed:** 2026-08-07
**Adoption:** `adapt`

> **Ownership:** completion record for P49/G21/G47; current behavior belongs in
> [`query-engine.md`](../../../architecture/runtime/query-engine.md),
> [`sessions.md`](../../../architecture/state/sessions.md), and
> [`configuration.md`](../../../architecture/platform/configuration.md)

## Outcome

Supported saved-root TUI and Plain composition roots now project Goal as
enabled when configuration omits the field. An explicit create without a token
budget persists active version-4 state and starts the existing initial Goal
turn immediately. Nil disables only the Goal token limiter: exact provider
usage still accumulates, and a positive cap added later applies to all already
committed usage. Explicit zero remains invalid.

Continuation state and coordinator payloads use version 2 so budget presence is
part of immutable identity without inventing a zero or maximum sentinel.
Legacy budgeted identity remains stable; legacy active nil-budget state remains
fail-closed. Pending provider admission version 2 persists logical-request and
model-attempt ID, index, profile, and retry index, and settlement compares every
field. An unresolved legacy admission is preserved verbatim and admits no new
provider call.

TUI and Plain retain their dedicated Goal wake/claim path. Dedicated Headless
Goal still requires an explicit positive process continuation bound. ACP
configuration defaults on but private capability negotiation and explicit
continue remain mandatory. Ordinary headless, unnegotiated ACP, child/review,
administration, Plan, standalone MCP, and generic runtime-item paths gain no
Goal authority.

## Compatibility And Rollback

Explicit `goal.enabled: false` remains the kill switch. Default enablement
alone creates no Goal and makes no provider call. Direct low-level
`QueryEngine` embeddings that omit `GoalCapability` also remain disabled; the
default is projected only by supported production composition roots.

Safe rollback uses the P49-capable binary to pause or otherwise settle active
work, reject pending continuations, checkpoint the quiescent state, and then
persist `goal.enabled: false` before starting an older binary. A deterministic
restart fixture proves that this order leaves an unbudgeted Goal paused with no
wake channel, claim, or dispatch. New schema versions have no downgrade writer;
older readers fail closed until P49 is restored or the Goal is cleared.

## Evidence

State, continuation, provider-admission, configuration, TUI, Plain, Headless,
ACP, disable-first, quiesced-restart, race, and Unix PTY fixtures pin the
contract. Repository Makefile, documentation, queue, manifest, and diff gates
are listed in the [verification record](../../verification/p49-goal-default-unbudgeted.md).

The evidence does not claim a live-provider cost bound, representative
adoption, remote-CI availability, or physical-terminal rendering.
