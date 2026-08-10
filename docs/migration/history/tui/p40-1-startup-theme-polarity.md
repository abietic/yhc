# P40.1 Startup Theme Polarity

**Status:** historical
**Closed gaps:** G12
**Completed:** 2026-08-01
**Adoption:** `adapt`

> **Ownership:** completion evidence for polarity-preserving startup theme
> compatibility, visible invalid-value diagnostics, and G12 closure. Current
> behavior belongs in the [TUI architecture](../../../architecture/tui/README.md).

## Outcome

P40.1 closed the startup configuration path that could silently turn a known
light theme request into the dark terminal default. The startup-only allowlist
maps `dark-daltonized` to Polar Night and `light-daltonized` to Daybreak. These
mappings preserve polarity only; Eino-Agent does not claim to implement the
reference daltonized palettes or their accessibility properties.

Canonical project theme IDs, the one-release `dark`/`light` aliases, existing
palettes, and environment-over-config precedence are unchanged. Runtime
`/theme` remains independently validated and rejects the startup-only names.
Prefix-like unknown values remain invalid rather than being guessed from
`light-` or `dark-` text.

## Typed Resolution And Delivery

Startup resolution now retains one typed source and a bounded list of typed
issues. A valid `EINO_THEME` value wins. An invalid environment value records
an environment issue and continues to config; an invalid config value records
a config issue and continues to the existing terminal-capability default.

`App.New` applies the resolved theme and retains only the typed issues.
`App.Init` transfers them once into the Bubble Tea loop, and `App.Update`
projects them through the existing warning-notification lifecycle. This keeps
notification mutation under the P35.1 App owner. User-controlled values are
trimmed, bounded to 64 runes, and quoted, so control bytes cannot become raw
terminal output.

No engine state, renderer, viewport, geometry, persisted configuration, theme
palette, session, replay, permission, ACP, Plain, headless, or standalone-MCP
behavior changed.

## Proof And Review

Focused TUI tests prove:

- both compatibility names preserve polarity across truecolor and ANSI-16
  capability snapshots;
- invalid environment values fall through to valid config with exact source
  provenance;
- prefix-like unknown config values fall through to terminal capability with
  a config issue;
- explicit runtime selection rejects the startup-only names;
- diagnostics are bounded and quote control bytes; and
- `App.Init` delivers both environment and config issues through `App.Update`
  exactly once.

Existing theme, palette, propagation, render-environment, and P35.1
notification lifecycle tests remain green. Repository formatting, lint, test,
build, documentation, manifest, and diff gates close the exact candidate.

## Compatibility And Rollback

Existing configuration remains readable and no durable schema changed. A
squash revert removes the two-name startup allowlist, typed issue projection,
and focused tests as one unit. It restores conservative invalid-value skipping
but reopens G12 because `light-daltonized` can again fall back silently to a
dark theme.
