# P22.2b Permission Review Audit

**Status:** historical
**Completed:** 2026-07-27
**Last verified:** 2026-07-27

> **Ownership:** delivery evidence for the off-by-default bounded local
> permission-review measurement boundary. Current behavior belongs in
> [`permissions.md`](../../../architecture/capabilities/permissions.md) and
> [`permissions-and-safety.md`](../../../guides/permissions-and-safety.md);
> remaining reviewer promotion and capability work belongs in
> [`p22-auto-permission-review.md`](../../plans/p22-auto-permission-review.md)
> and [`PLAN.md`](../../PLAN.md).

## Decision And Authority

P22.2b was delivered under the **`combine`** decision:

- preserve QueryEngine as the permission, prompt, grant, settlement, and
  dispatch owner;
- preserve P22.2a as a separately enabled, non-authoritative reviewer shadow;
- combine typed QueryEngine lifecycle facts with a project-owned secure local
  journal and deterministic provider-free report; and
- keep legacy, direct-human, and explicit versioned-corpus truth in separate
  denominators.

Audit failure, corruption, unavailability, or panic cannot change a permission
outcome. This slice adds no reviewer enforcement, prompt or rule change,
approval UI, shell eligibility, child/dynamic expansion, remote telemetry, or
production-generated corpus truth.

## Record And Correlation Boundary

[`engine/permission/review_audit.go`](../../../../engine/permission/review_audit.go)
defines strict versioned typed records for eligible actions, reviewer attempts,
terminal outcomes, comparisons, and local recovery. QueryEngine correlates
them only through a fresh 32-character lowercase hexadecimal audit event ID.

Persisted records contain canonical tool and action class, deterministic class,
safe reviewer route and schema labels, terminal status/decision/reason/latency,
typed comparison source and expected decision, explicit versioned corpus IDs,
and recovery byte counts. They contain no request or tool-use identity, raw
input, absolute path, action or policy digest, nonce, rationale, credential,
secret, transcript, Session, Agent, or CWD.

Legacy comparison is admitted only from the typed classifier allow/deny result.
Direct-human comparison requires one structured adapter response for the exact
unchanged settled action. Coalesced interaction, context cancellation or
timeout, invalid response, fail-closed rewrite or binding drift, and changed
input never fabricate human truth. Production accepts corpus truth only through
an explicit typed `versioned_corpus` record.

## Storage, Recovery, And Deletion

One [`ReviewAuditStore`](../../../../engine/permission/review_audit.go) owns the
default `~/.eino-agent/permission-review-audit/v1` directory, with explicit
environment and runtime-flag overrides. It enforces:

- a pinned `0700` directory root and identity-checked `0600` coordination and
  segment file handles;
- one OS-locked `O_EXCL` sentinel with a two-second acquisition deadline and
  ownership-safe 30-second stale boundary;
- an active `events.jsonl` plus seven rotations, each bounded to 1 MiB;
- strict non-null JSON objects with duplicate, unknown, trailing, and semantic
  field rejection;
- counted skipped corrupt newline rows and visible partial tails;
- synchronized truncation of a partial tail followed by a typed recovery
  record before the next caller record; and
- deletion of only the exact owned segment set while preserving the directory
  and unknown neighboring files.

Directory and file handles remain pinned after identity checks, so concurrent
path replacement cannot redirect journal I/O outside the opened store.
Load, mutation, recovery, rotation, and deletion share the same lock boundary.
The retained eight-segment size window is not an age-retention promise.

## Report And Entrypoints

[`engine/permission/review_audit_report.go`](../../../../engine/permission/review_audit_report.go)
aggregates unique eligible actions, reviewer attempts, terminal outcomes,
nearest-rank p50/p95 latency, unavailable reasons, escalation, disagreement,
and every retained false allow. Legacy, direct-human, and versioned-corpus
sources remain separate. Missing, corrupt, rotated, repaired, duplicated,
orphaned, incomplete, or unmatched evidence yields `no_data` or `partial`
rather than an observed zero.

The runtime flags require both shadow review and explicit audit opt-in.
TUI/plain/headless roots share QueryEngine correlation. ACP constructs one
Agent-owned store and reuses it for new, load, and resume engines. Child Agent
and standalone MCP remain excluded.

The provider-free
[`permission-review-audit`](../../../../cmd/yhc/cmd/permission_review_audit.go)
admin command reports text or JSON and deletes only after explicit
`--confirm`. Reports, startup diagnostics, and errors do not expose the
resolved store path.

## Verification

Focused evidence covers strict schemas, size rotation, permission modes,
lock/stale-lock behavior, operation-time path replacement, corruption and
partial-tail recovery, exact deletion, report denominators, every false allow,
QueryEngine lifecycle correlation, unchanged permission outcomes under sink
error or panic, runtime flag constraints, provider-free administration, and
one-store ACP reuse.

Closeout used:

```text
go test ./engine/permission -run 'Test(ReviewAuditStore|DecodeReviewAuditRecord|BuildReviewAuditReport)' -count=20
go test -race ./engine/permission ./engine ./cmd/eino-agent/cmd ./server/acp -run 'Test(ReviewAudit|PermissionReviewAudit|BuildReviewAuditReport|ResolveApprovalReviewAudit|CLICommandTree|ACPApprovalReview)' -count=1
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c ./engine/permission
make fmt
make lint
make lint-new
make test
make build
make docs-check
go run ./scripts/migration_manifest.go check
go run ./scripts/migration_manifest.go check-ledger
git diff --check
```

Independent closeout review first rejected path-based segment I/O, unowned
stale-sentinel removal, and JSON `null` coercion. The accepted repair pins the
directory and verified file handles, serializes the O_EXCL sentinel and stale
recovery through an OS file lock, and rejects `null` before typed decode.
Deterministic replacement/reclamation tests, race tests, and Windows
cross-compilation passed; final read-only review found no blocking issue.

## Compatibility, Rollback, And Next State

P22.2b is additive and default-off. Enabling it adds bounded local I/O and a
size-window journal but no permission authority. Disabling it stops new
records; the explicit delete command removes the owned retained segments.
Rollback removes the audit sink, store, report, flags, and command as one unit.
No grant, rule, transcript, checkpoint, session, or provider schema needs
migration.

P22.3a remains queued because no real retained workload establishes a
latency/error budget or promotion threshold. P23.H1 is the sole `Ready` slice.
