# P29.0 Route-Isolation Characterization

**Status:** historical
**Completed:** 2026-07-30

> **Ownership:** completion evidence for P29.0's behavior-preserving
> characterization and compatibility fixtures. Current provider behavior
> remains owned by the linked source and architecture documents; later P29
> slices own any production portfolio behavior.

## Outcome

P29.0 completed a test-only `combine` slice inside the accepted P29 program.
It preserved current config merging, provider resolution, provider-specific
adapters, lazy provider routing, diagnostics, and CLI/ACP composition while
adding executable prerequisites for route-isolated portfolio work.

The slice adds no production configuration field, named account/profile,
credential destination, client cache key, provider request, diagnostic field,
entrypoint behavior, session record, role routing, or failover policy.

## Evidence

The characterization fixtures:

- pin user-then-project merging and the legacy flag/environment/config/store
  resolution order;
- reproduce the current provider-only cache collision with fake clients;
- define a private `_test.go` target identity and cache, including all equality,
  isolation, unused-route, and concurrent single-construction cases;
- construct all six current provider branches without network calls;
- scan current errors, diagnostic JSON, and target-only serialization for one
  unique credential sentinel; and
- use AST composition gates to retain CLI TUI/plain/headless/headless-goal and
  ACP create/restore ownership through one provider runtime.

The full fixture-to-contract map and exact reproduction commands are in
[`p29-0-route-characterization.md`](../../verification/p29-0-route-characterization.md).

## Verification

The focused `TestP290` matrix and provider race target passed. Repository
format, lint, test, and cross-platform build gates passed; the full test gate
completed 6,197 tests with one documented opt-in physical-terminal diagnostic
skip. Documentation, migration-manifest, source-scan, and diff gates are part
of the same closeout.

## Compatibility And Rollback

Current runtime and durable data are unchanged. Rollback deletes the four
`_test.go` fixtures plus this verification/history record and restores the
previous queue text; no data or configuration migration is required.

P29.1 remains queued after this closeout. It requires a separate root-PLAN
promotion and must map these fixtures to production compiler, route-identity,
secret-redaction, adapter, and entrypoint acceptance tests. G31 remains open.
