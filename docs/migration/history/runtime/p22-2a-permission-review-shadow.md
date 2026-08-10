# P22.2a Permission Review Shadow

**Status:** historical
**Completed:** 2026-07-27
**Last verified:** 2026-07-27

> **Ownership:** delivery evidence for the off-by-default, separately routed,
> non-authoritative permission-review shadow. Current behavior belongs in
> [`permissions.md`](../../../architecture/capabilities/permissions.md) and
> [`model-providers.md`](../../../architecture/platform/model-providers.md);
> remaining audit and enforcement work belongs in
> [`p22-auto-permission-review.md`](../../plans/p22-auto-permission-review.md)
> and [`PLAN.md`](../../PLAN.md).

## Decision

P22.2a was delivered under the **`combine`** decision:

- preserve QueryEngine as the only deterministic policy, interaction,
  settlement, denial-accounting, and dispatch owner;
- preserve the legacy actor-model classifier as the authoritative model path
  until measured enforcement is independently promoted;
- combine the P22.1b canonical action/policy identity with a project-owned
  minimized request, strict result schema, separate provider route, absolute
  deadline, and fresh process-local binding; and
- project bounded advisory lifecycle status across supported entrypoints
  without adding approval UI or reviewer authority.

The slice adds no enforcement, durable audit sink, grant/rule mutation, prompt
suppression, shell parser, child/dynamic eligibility, OS sandbox, remote
telemetry, or standalone MCP behavior.

## Request And Provider Boundary

[`engine/permission/reviewer.go`](../../../../engine/permission/reviewer.go)
defines `permission_review_v1:user_authority+host_policy+redacted_action`.
QueryEngine projects:

- canonical tool/action labels and bounded host-owned risk/policy facts;
- recursively typed action shape, byte counts, relative/root path labels, and
  explicit secret-redaction markers;
- at most the last three bounded process-local submission-presence records
  captured only by QueryEngine's public user-message APIs, with no user text;
  and
- no historical/synthetic user-role message, assistant/tool content, raw
  secret, absolute host path, repository/web/MCP evidence, child return,
  request digest, or nonce.

[`engine/provider/reviewer.go`](../../../../engine/provider/reviewer.go)
constructs one explicitly named provider/model route with a positive timeout.
It ignores generic actor `PROV_*` routing values, never falls back to the actor
model, permits only one bounded generation, and rejects oversized, empty,
tool-calling, duplicate-field, trailing, unknown-field, or semantically invalid
JSON output. Initialization errors redact the exact configured API key and base
URL.

## Shadow Binding And Lifecycle

[`engine/permission_review.go`](../../../../engine/permission_review.go) starts
the shadow only after deterministic policy leaves an eligible complete
main-agent built-in for legacy classifier review. Incomplete shell,
Agent/child, network, MCP/app/dynamic, direct-interaction, ProjectGraph-probe,
and non-Auto paths are excluded.

The process-local pending entry binds request ID, tool call, cryptographic
nonce, canonical action, action digest, effective policy, registry generation,
roots, Session/Agent/entrypoint identity, route, and data-boundary version.
Settlement claims the entry once, rebuilds the descriptor, and emits completed
only when every bound fact still matches. Deadline, cancellation, late or
duplicate delivery, cross-delivery, capacity, identity drift, malformed output,
and engine close emit unavailable or no second terminal result.

Pending and seen state is bounded, never checkpointed, and cancelled and joined
at engine close. A cold engine begins with fresh request identity and cannot
replay a reviewer result.

## Entrypoint And Compatibility Evidence

| Boundary | Verified result |
|---|---|
| TUI | A local checking spinner and bounded completed/unavailable toast are presentation only; cancellation and terminal events clear checking state. |
| Plain and headless | One bounded status line is printed without request ID, nonce, action input, rationale, or control characters. |
| ACP | `_session/status` projects checking/completed/unavailable through bounded safe labels; new/load/resume construction shares the same engine owner. |
| Child Agent | Reviewer shadow is disabled; the existing human-required boundary remains. |
| ProjectGraph probe | Shadow is skipped to avoid speculative duplicate review. |
| Standalone MCP | Excluded; no reviewer factory or P22 lifecycle is constructed. |

With the shadow disabled, no reviewer client is constructed and behavior is
unchanged. With it enabled, the legacy classifier, prompt/fail-closed outcome,
grants/rules, denial counters, and dispatched bytes remain unchanged.

## Verification

Focused and adversarial evidence covers:

- strict request/result schemas, typed recursive action redaction,
  public-user-input ownership, synthetic user-role exclusion, and proof that
  contextual/bare/opaque credentials, URLs, paths, private keys, and ordinary
  user text never enter reviewer intent records;
- explicit provider/model selection, ignored actor `PROV_*` values, independent
  client construction, bounded output, absolute deadline, and error redaction;
- timeout/cancel/late/duplicate/cross-delivery, exact deduplication, pending
  capacity, cold identity, close/join, and request/action/policy/runtime drift;
- default-off, child, Agent, ProjectGraph-probe, and unsupported capability
  exclusions;
- unchanged authoritative classifier outcome and production emitter/reducer
  ordering; and
- bounded TUI/plain/headless/ACP projection with no opaque identity or control
  leakage.

Closeout used:

```text
go test ./engine/permission ./engine/provider ./engine ./internal/tui ./cmd/eino-agent/cmd ./server/acp -run 'Test(PermissionReview|ApprovalReviewer|ACPApprovalReview|ResolveApproval|DrivePlainQueryEventsProjectsPermissionReview|ACPProjectsPermissionReview)' -count=1
go test -race ./engine ./engine/provider -run 'Test(PermissionReview|ApprovalReviewer)' -count=1
go test ./engine -run 'TestPermissionReview(ConcurrentExactDeduplicationAndChangedInput|DuplicateClaimAndColdEngineIdentity|CloseCancelsAndJoinsReviewer|ProductionEmitterPreservesRuntimeOrdering|TrustedIntentUsesOnlyPublicUserSubmissionOwner|UserIntentNeverForwardsRawContent)' -count=20
make fmt
make lint
make lint-new
make test
make build
make docs-check
go run ./scripts/migration_manifest.go check-ledger
git diff --check
```

Independent closeout review initially rejected raw-intent sanitization and
message-role provenance as insufficient security boundaries. The accepted fix
moved intent ownership to QueryEngine's public user-submission APIs and replaced
all reviewer-visible user text with fixed presence markers. Final read-only
review found no blocking issue after that change and after making P22.2b the
only `Ready` row.

## Compatibility, Exclusions, And Rollback

P22.2a is additive and default-off. Enabling it creates an additional provider
call and bounded status events for eligible actions, but does not alter any
permission outcome. The explicit separate route may require an additional
provider credential and contributes its own timeout/cost; unavailability is
advisory.

Rollback removes one code/test/document unit and the opt-in flags. No persisted
permission, transcript, checkpoint, audit, grant, or rule schema requires
migration because reviewer state is process-local and no audit sink exists.

G14 remains open: P22.2b still owns bounded redacted measurement, P22.3 owns
opt-in enforcement, and later slices own entrypoint/capability expansion,
promotion, and legacy-owner deletion.

## Next State

P22.2b is the sole `Ready` slice. It must freeze one bounded local redacted
audit owner, storage/permission/rotation/retention/deletion behavior, explicit
measurement denominators, and deterministic aggregate reporting before any
reviewer enforcement is eligible.
