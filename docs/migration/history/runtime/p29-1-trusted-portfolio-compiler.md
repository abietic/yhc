# P29.1 Trusted Portfolio Compiler And Route Identity

**Status:** historical
**Completed:** 2026-07-30

> **Ownership:** completion evidence for P29.1's source-aware configuration,
> named-auth, metadata, provider-runtime, and CLI/ACP startup boundary.
> Durable selection, manual inventory/switching, role execution, and failover
> execution remain owned by later P29 slices.

## Outcome

P29.1 completed the frozen `combine` slice. User settings can now define
non-secret provider accounts and stable model profiles, while project settings
can select only an explicitly authorized user profile. The same compiler and
runtime boundary serves CLI interactive, exec, resume, and headless Goal paths
plus ACP create/restore startup.

The implementation:

- preserves user and project source layers until authority checks finish;
- removes a forbidden project portfolio subset before typed value decoding and
  retains unrelated project settings;
- validates account/profile IDs, endpoint canonicalization, auth forms,
  metadata overrides, role bindings, failover definitions, and mixed
  legacy/profile selection;
- resolves exact named credentials only at client construction;
- compiles unchanged legacy routing into reserved non-secret
  `legacy.main`/`legacy.fallback` definitions;
- keys lazy clients by provider, canonical endpoint, concrete auth
  kind/reference, and adapter digest;
- keeps profile ID, API model, credential value, and credential hash outside
  that identity, so compatible profiles share a client while different
  endpoint/auth routes isolate; and
- exposes stable redacted compiler warnings and credential-presence
  diagnostics without constructing unused routes.

All six provider-specific adapters remain distinct and are reached through
the existing constructor dispatch.

## Security And Compatibility

Plaintext `api_key`, arbitrary secret-bearing account fields, and unknown
nested portfolio fields fail strict user decoding. Project account/profile/
role/policy values are never decoded when their source lacks authority.
Credential values exist only in the local construction call; snapshots,
route identities, diagnostics, errors, events, and durable Session/transcript
state contain neither the value nor its hash.

Legacy flags, `PROV_*`, provider-specific environment variables, configured
values, aliases, credential-store fallback, and bounded overload fallback keep
their compatibility resolver. The legacy project `api_base_url` behavior is
retained only there and emits `legacy_project_route_authority`.

P29.1 adds no session schema, manual model/profile control, role-specific call,
failover attempt, TUI/ACP protocol field, provider adapter rewrite, hot reload,
or standalone-MCP model runtime.

## Verification And Rollback

Focused compiler/auth/metadata/provider/composition tests, all-six-adapter
construction, same-provider route isolation, provider race tests, secret
sentinel scans, repository gates, documentation checks, manifest validation,
source scan, and independent review are recorded in
[`p29-1-trusted-portfolio-compiler.md`](../../verification/p29-1-trusted-portfolio-compiler.md).

Rollback reverts the source-aware compiler, `--model-profile`, named auth,
portfolio metadata, configured runtime, and route-keyed cache together, then
restores CLI/ACP composition to the direct legacy resolver. No durable
migration or stored Session rewrite is required.

P29.2 remains queued until a separate promotion review freezes its additive
session binding and shared inventory contract. G31 remains open.
