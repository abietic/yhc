# P29.1 Trusted Portfolio Compiler And Route Identity

**Status:** verification
**Last verified:** 2026-07-30

> **Ownership:** reproducible acceptance evidence for P29.1 production source
> authority, compiler, named credential, metadata, route identity/cache,
> configured CLI/ACP startup, legacy compatibility, and redaction.

## Result

P29.1 maps the P29.0 target fixtures to production behavior:

| Contract | Production evidence |
|---|---|
| Source layers and project authority | `TestLoadConfigSourcesRejectsProjectPortfolioAsWholeBeforeDecode`, `TestCompilePortfolioSelectionAuthorityAndMixedMode` |
| Strict schema, URL/auth/ID/collision validation | `TestLoadUserConfigRejectsUnknownSecretFieldsWithoutEchoingValue`, `TestCompilePortfolioRejectsInvalidDefinitions` |
| Metadata value plus per-field provenance | `TestResolvePortfolioMetadataPerFieldProvenance`, `TestResolvePortfolioMetadataUnknownRemainsUnknown`, `TestResolvePortfolioMetadataRejectsInvalidOverrides` |
| Deterministic non-secret revision and legacy compilation | `TestCompilePortfolioNamedProfile`, `TestCompilePortfolioLegacyCallbackAndWarning`, `TestConfiguredRuntimeLegacyCompilerPreservesResolutionAndSafeInventory` |
| Role/failover validation without execution | `TestCompilePortfolioValidatesRoleAndFailoverDefinitions` |
| Exact named credential resolution | `TestResolveNamedCredential`, `TestResolveNamedCredentialFromDefaultStore`, `TestConfiguredRuntimeResolvesNamedCredentialOnlyForConstructedRoute` |
| Complete production route identity | `TestRouteIdentityEqualityAndIsolation`, `TestRouteIdentityRejectsUnsafeEndpointAndContainsNoSecret` |
| Same-route API-model reuse; endpoint/auth isolation; unused laziness | `TestConfiguredRuntimeNamedRouteIsolationReuseAndLaziness` |
| Concurrent single construction | `TestConfiguredRuntimeConstructsNamedRouteOnceConcurrently`, `TestRouteKeyedRuntimeConstructsLazyRouteOnceConcurrently` |
| Concrete `provider_default` lowering | `TestConfiguredRuntimeProviderDefaultLowersConcreteIdentity` |
| Six existing adapters | `TestConfiguredRuntimeLowersNamedProfilesThroughSixAdapters`, `TestP290CurrentErrorsAndSixProviderConstructorsDoNotExposeSecret` |
| Error and serialized-state sentinel redaction | `TestConfiguredRuntimeRedactsNamedConstructionFailure`, `TestP290CurrentErrorsAndSixProviderConstructorsDoNotExposeSecret`, compiler/runtime snapshot checks |
| Shared CLI and ACP startup | `TestP291CLIConfiguredProfileReachesSharedRuntime`, `TestP291RuntimeCommandsExposeModelProfileFlag`, `TestP291ACPCreateAndRestoreUseConfiguredProfile`, updated AST composition gates |

Named `ResolveModel` accepts only the startup profile in P29.1. Tests use the
internal route fixture to prove unselected-profile cache equality and
isolation without making manual switching observable.

## Reproduction

```bash
go test ./engine/config ./engine/model ./engine/auth ./engine/provider \
  ./cmd/eino-agent/cmd ./server/acp -run 'TestP291|TestConfiguredRuntime|TestRouteIdentity|TestResolvePortfolio|TestResolveNamedCredential|TestCompilePortfolio|TestLoadConfigSources|TestLoadUserConfig' -count=1
go test -race ./engine/provider -count=1
```

Repository closeout:

```bash
make fmt
make lint
make lint-new
make test
make build
make docs-check
make docs-check-ci
go run ./scripts/migration_manifest.go check
git diff --check
```

The secret gate scans changed non-test/runtime artifacts and captured command
output for the unique compiler/runtime sentinels and their SHA-256 digests.
Sentinel literals exist only in focused test sources.

## Boundary

Passing this matrix proves configured startup, not P29.2 inventory or manual
switching. It does not persist profile identity in a Session, route roles,
execute failover policies, add protocol/UI inventory, hot-reload credentials,
or change standalone MCP. G31 remains open.
