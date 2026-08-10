# P29.0 Route-Isolation Characterization

**Status:** verification
**Last verified:** 2026-07-30

> **Ownership:** reproducible P29.0 evidence for current legacy resolution,
> provider-only route caching, provider construction, diagnostics redaction,
> and composition roots. Test-only target helpers do not claim that a
> production model portfolio or `RouteIdentity` exists.

## Result

P29.0 changes no production behavior. Its fixtures prove:

- user settings are merged before project settings, with project routing
  scalars and alias keys overriding lower-precedence values;
- current provider resolution retains explicit, generic `PROV_*`,
  provider-specific environment, configured, and credential-store behavior;
- the current route registry caches one client per provider, so a
  same-provider model selection reuses the first endpoint and credential;
- a test-only complete identity can distinguish provider, canonical endpoint,
  auth kind/reference, and adapter digest while excluding profile ID,
  provider-local model, resolved secret, and secret hash;
- distinct endpoint and auth reference routes isolate, same-route different
  models share, unused routes stay uninitialized, and concurrent access
  constructs once in the target-only cache;
- all six current `newAgenticModel` branches construct deterministic clients
  without a provider request;
- one credential sentinel is absent from resolution/initialization errors,
  diagnostic JSON, and serialized test-only target identities; and
- CLI TUI/plain/headless/headless-goal plus ACP create/restore composition
  continue through their existing single provider-runtime boundaries.

## Current Limitation Versus Target Fixture

| Evidence | Meaning |
|---|---|
| `TestP290CurrentProviderCacheCannotIsolateSameProviderRoutes` | Reproduces current behavior: `routeRegistry.models` is keyed only by provider and same-provider selection inherits the main endpoint and credential. |
| `TestP290TargetRouteIdentityEqualityAndIsolation` | Describes accepted equality and lazy-isolation behavior only in `_test.go`; it is not a production compiler or cache. |
| `TestP290TargetRouteCacheConstructsOnceConcurrently` | Proves the test-only target cache admits one construction under concurrent access. |

## Fixture Map

| Contract | Fixture |
|---|---|
| Effective user/project merge | [`TestP290EffectiveConfigMergeRetainsLegacyLayerOrder`](../../../engine/config/p29_0_characterization_test.go), [`TestP290LoadEffectiveConfigMergesUserThenProject`](../../../engine/config/p29_0_characterization_test.go) |
| Legacy provider resolution | [`TestP290LegacyCredentialPrecedence`](../../../engine/provider/p29_0_characterization_test.go), [`TestP290LegacyProviderModelAndEndpointPrecedence`](../../../engine/provider/p29_0_characterization_test.go) |
| Current provider-only collision | [`TestP290CurrentProviderCacheCannotIsolateSameProviderRoutes`](../../../engine/provider/p29_0_characterization_test.go) |
| Target identity, isolation, unused route, and serialization | [`TestP290TargetRouteIdentityEqualityAndIsolation`](../../../engine/provider/p29_0_characterization_test.go) |
| Target concurrent single construction | [`TestP290TargetRouteCacheConstructsOnceConcurrently`](../../../engine/provider/p29_0_characterization_test.go) |
| Six constructors plus resolution/initialization error redaction | [`TestP290CurrentErrorsAndSixProviderConstructorsDoNotExposeSecret`](../../../engine/provider/p29_0_characterization_test.go) |
| CLI composition and diagnostic redaction | [`TestP290CLIRuntimeCompositionRootsStayUnified`](../../../cmd/yhc/cmd/p29_0_composition_test.go), [`TestP290DiagnosticProjectionDoesNotExposeSecret`](../../../cmd/yhc/cmd/p29_0_composition_test.go) |
| ACP composition | [`TestP290ACPCompositionRootsStayUnified`](../../../server/acp/p29_0_composition_test.go) |

## Reproduction

```bash
go test ./engine/config ./engine/provider ./cmd/eino-agent/cmd ./server/acp \
  -run '^TestP290' -count=1
go test -race ./engine/provider -run '^TestP290' -count=1
```

The repository closeout also runs:

```bash
make fmt
make lint
make lint-new
make test
make build
make docs-check
go run ./scripts/migration_manifest.go check
git diff --check
```

## Evidence Limit

Passing P29.0 establishes executable prerequisites for a later P29.1
promotion. It does not provide named accounts or profiles, a production
`RouteIdentity`, credential migration, portfolio diagnostics, durable profile
binding, role selection, manual switching, or failover. G31 remains open.
