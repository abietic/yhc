# P29.4 Bounded Overload Failover

**Status:** historical
**Closed gaps:** G31
**Completed:** 2026-07-30

> **Ownership:** completion evidence for the P29.4 overload-only logical
> request, ordered profile attempts, shared budgets, exact entrypoint
> commitment, attempt-scoped usage, and failed-output disposal boundary.

## Outcome

P29.4 completed the frozen `combine` slice. `runCanonicalModelRound` now owns
one logical-request coordinator over the detached role failover chain returned
by `engine/provider.Runtime`. It starts from the current primary on every new
request, evaluates ordered alternates without constructing skipped routes, and
creates a new attempt only for an admitted different profile.

Same-route retry remains an inner mechanism. Only typed `overloaded` failures
can reach another profile; 429 remains same-route-only, while transport,
timeout, auth, invalid input, policy, cancellation, deadline, conversion,
primary route construction, provider-usage ambiguity, and unknown failures
terminate. An unconstructable alternate is a pre-dispatch skip and does not
block a later candidate. One provider-call counter, switch counter, and
absolute deadline cover retries and all profiles. Candidate skips and
pre-dispatch rejection consume neither a provider call nor a switch.

Every actual dispatch rebuilds messages, system prompt, and tool schemas from
one immutable request snapshot. A different profile receives cleaned history
without failed provider-bound reasoning. Failed stream output remains
attempt-local, and the deferred P26.1 model round still cannot execute a tool.
Only a completely classified successful attempt reaches canonical
assistant/tool history.

## Entrypoints, Usage, And Compatibility

Attempt events carry bounded logical request, attempt, profile, provider,
API-model, route, retry, phase, failure, admission, and output-disposition
facts. TUI attaches visible assistant/thinking projection to the exact attempt
and removes only that projection before a switch. Plain/headless, ACP, and
default library consumers may switch only before visible assistant output is
offered; P29.4 adds no ACP retraction protocol.

Provider and Goal usage retain the existing accounting owner while adding
logical request, attempt, and retry attribution. Ambiguous usage is terminal
and cannot select an alternate. Attempt ownership is not persisted, so Session
schema and restart recovery remain unchanged.

Legacy `fallback_model` now compiles into the same canonical overload-only
policy with one switch, six provider calls, and a 45-second deadline. The old
direct fallback executor was removed. Eino v0.9.12 failover remains rejected
as an owner because its last-success and stream-event semantics do not preserve
the project attempt, event, transcript, usage, and output-disposal contract.

## Verification And Rollback

Focused taxonomy, coordinator, provider admission, usage, immutable replay,
entrypoint, runtime reducer, TUI, no-tool-side-effect, canonical trace, legacy
equivalence, source-owner, compatibility, and race checks plus all repository,
documentation, and manifest gates are recorded in
[`p29-4-bounded-overload-failover.md`](../../verification/p29-4-bounded-overload-failover.md).

Rollback disables named chain switching while retaining P29.1-P29.3 portfolio,
inventory, durable binding, role, reasoning, and provider adapters. The
canonically compiled single legacy alternate remains available as the narrow
compatibility path. Rollback never promotes failed attempt output to durable
history. P29.5 remains queued for a separate measurement and adaptive-health
decision.
