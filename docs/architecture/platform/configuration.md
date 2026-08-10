# Configuration

**Status:** current
**Last verified:** 2026-08-07

> **Ownership:** `engine/config` for file settings; CLI and ACP composition roots for runtime resolution

## Effective Configuration

The file configuration layer reads user settings from
`~/.claude/settings.json` and project settings from
`<project>/.claude/settings.json`, then applies project values over user values
and defaults. Provider flags, environment, stored credentials, and aliases are
resolved later by `engine/provider`; configuration is not itself a model
factory.

## Current contract

- Defaults use model `default`, unlimited turns (`0`), default permission mode,
  auto-compaction enabled, and the dark theme.
- Scalar non-zero values override lower layers. An explicitly present
  `max_turns: 0` is tracked and preserves the unlimited setting.
- Model aliases and MCP server maps merge by key; tool and permission lists
  replace when non-nil.
- `goal.enabled` and `goal.default_token_budget` merge field by field. Supported
  production composition roots enable Goal by default and ship no numeric token
  budget; `goal.enabled: false` is the kill switch. A low-level
  `QueryEngine` embedding with a nil `GoalCapability` remains disabled.
  `default_token_budget`, when present, must be positive.
- `SaveUserConfig` requests mode `0700` when creating the user directory and
  `0600` when creating the settings file. `MkdirAll`/`WriteFile` do not repair
  permissions on paths that already exist.
- `SettingsWatcher` is owned by `QueryEngine` and reloads permission-facing
  settings during the engine lifetime.

## Composition example

```go
cfg, err := config.LoadEffectiveConfig(cwd)
runtime, err := provider.NewRuntime(ctx, provider.RuntimeOptions{
    Resolution: resolveProviderInput(cfg),
})
```

The first call produces layered application settings; the second applies the
provider-specific precedence and constructs model routes.

## Invariants and edge cases

- Missing files are not errors; malformed JSON is.
- `settings.local.json` has a loader but is not part of
  `LoadEffectiveConfig`'s current two-layer path.
- Secrets belong to provider resolution and credential storage. Diagnostics may
  report source names but must not print credential values.
- `AutoCompact` is a plain boolean in the file schema; callers should verify
  presence semantics before changing merge behavior.
- Enabling Goal configuration grants saved-root TUI and Plain sessions plus the
  dedicated bounded `goal run` process their documented Goal capabilities.
  ACP gains its narrower Goal surface only when the client also negotiates the
  private version-1 Goal extension; it does not expose slash `/goal`. Ordinary
  one-shot headless, unnegotiated ACP, child/review, administration, ephemeral,
  and standalone MCP contexts cannot claim or expose Goal work.

## Code references

- [`Config`, `GoalConfig`, and `MCPServerConfig`](../../../engine/config/config.go)
- [`DefaultConfig`](../../../engine/config/config.go)
- [`LoadEffectiveConfig`](../../../engine/config/config.go)
- [`MergeConfigs`](../../../engine/config/config.go)
- [`SaveUserConfig`](../../../engine/config/config.go)
- [`SettingsWatcher`](../../../engine/config/settings.go)
- [CLI `buildEngineConfig`](../../../cmd/yhc/cmd/root.go)

## Related tracking

Provider precedence is documented in [`model-providers.md`](model-providers.md).
Migration gaps belong in [`migration/REMAINING.md`](../../migration/REMAINING.md).
