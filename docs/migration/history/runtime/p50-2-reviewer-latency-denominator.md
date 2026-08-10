# P50.2 Reviewer Attempt-Latency Denominator

**Status:** historical
**Closed gaps:** G49
**Completed:** 2026-08-08
**Adoption:** `project-native`

> **Ownership:** completion record for P50.2/G49; current reviewer-audit
> measurement behavior belongs in
> [`permissions.md`](../../../architecture/capabilities/permissions.md)

## Outcome

Reviewer p50, p95, and sample count now describe retained events for which a
reviewer attempt and terminal result both exist. Terminal-only setup or
projection failures remain visible as outcomes and lifecycle diagnostics, but
no longer masquerade as reviewer duration. A retained window without any pair
reports latency as unavailable and omits percentile JSON fields.

Eligible, attempt, terminal, comparison, and corpus counts remain independent.
Completed and unavailable attempt-terminal results both contribute latency
because both represent an actual reviewer attempt. Nearest-rank percentile
math, field names, storage, retention, and provider-free reporting remain
unchanged.

## Compatibility And Rollback

This is a measurement-semantics correction only. It does not change permission
classification, reviewer authority, prompt behavior, exact grants, policy
persistence, tool dispatch, or entrypoint ownership. Reports over historical
terminal-only records may expose fewer latency samples and different
percentiles; their outcome counts remain intact.

A squash revert of the sample-admission guard and focused tests restores the
old denominator. No durable audit schema or record migration is required.

## Evidence

Test-first fixtures reproduce the old four-terminal/three-attempt mismatch and
the terminal-only false sample. A retained matrix covers completed, timeout,
reviewer-unavailable, invalid-result, terminal-only, attempt-only,
eligible-only, zero-pair JSON omission, and unsorted percentile input. The CLI
path proves two terminal outcomes produce one latency sample through the real
local store.

Focused, race, repository, documentation, queue, manifest, and diff gates are
recorded in the
[verification record](../../verification/p50-2-reviewer-latency-denominator.md).
Remote CI remains a separate merge gate.
