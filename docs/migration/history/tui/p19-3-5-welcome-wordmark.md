# P19.3.5 Revontuli Welcome Wordmark And P19 Closeout

**Status:** historical
**Closed gaps:** G6, G7
**Completed:** 2026-07-25

> **Ownership:** delivery evidence, compatibility boundary, rollback, and
> program closeout for the completed P19.3.5 welcome-wordmark slice

## Outcome

The welcome `Eino Agent` title now renders the project-owned Revontuli
wordmark. Polar Night and Daybreak interpolate horizontally from the current
Header brand teal through `AuroraSky` to the permission violet carried by
`DialogTitle`. ANSI, reduced-color, and no-color terminals retain the flat
semantic Header treatment.

The adoption decision is `project-native`. `App.styles` remains the only live
theme identity: the renderer consumes that snapshot and the App's immutable
terminal capability on every frame. The preserved combined WIP candidate's
package-global `ActiveTheme` mechanism was deliberately not extracted because
it would have introduced a second mutable theme authority.

P19.0.0-P19.3.5 are now complete. The final composed visual matrix closes G6
(borrowed product identity) and G7 (incoherent runtime theme application).
G8 remains separate intake because Bubble Tea notification delivery/expiry is
a lifecycle contract, not a visual treatment.

## Preserved Boundary

- Compact-text, condensed-mascot, full-bordered, and wide full-bordered
  layouts keep the established title padding, display width, line count,
  mascot geometry, editor/status placement, and normalized golden output.
- The wordmark is static. Reduced motion preserves it and continues to suppress
  only mascot/spinner animation; no new tick or state machine exists.
- Runtime `/theme` restyles the next welcome render through the current
  `Styles`; environment/config resolution, component propagation, chat cache
  identity, and Markdown cache identity are unchanged.
- ANSI and reduced-color paths do not synthesize truecolor. No-color final
  output remains escape-free through the existing App output boundary.
- QueryEngine, permission, interaction, transcript, replay, provider, and
  terminal lifecycle ownership are unchanged.

## Evidence

[`welcome_wordmark_test.go`](../../../../internal/tui/welcome_wordmark_test.go)
pins the exact Polar Night and Daybreak SGR sequence, flat reduced-color/ANSI
fallback, all four welcome layouts, no-color final output, visible-width
identity, and live theme restyling.
[`welcome_wordmark.golden`](../../../../internal/tui/testdata/welcome_wordmark.golden)
owns the exact gradient stops. The existing
[`eno_welcome.golden`](../../../../internal/tui/testdata/eno_welcome.golden)
remains byte-identical after ANSI normalization, proving that this color-only
slice does not move responsive geometry.

Focused verification included:

```text
go test ./internal/tui -run 'TestWelcomeWordmark|TestEnoWelcomeResponsiveGolden|TestNoColorFinalFrameContainsNoTerminalStyles' -count=1
go test -race ./internal/tui -run 'TestWelcomeWordmark|TestEnoWelcomeResponsiveGolden' -count=1
go test ./internal/tui -run '^$' -bench 'BenchmarkRenderWelcomeWordmark|BenchmarkAppViewExplicitLayout' -benchmem -count=3
go test ./internal/tui -count=1
make fmt
make lint
make test
make build
make lint-new
make docs-check
make docs-check-ci
go run ./scripts/migration_manifest.go check
git diff --check
```

`BenchmarkAppViewExplicitLayout` remains 402 allocations per operation. The
dedicated static wordmark benchmark records 94 allocations per direct render;
it does not affect the cached chat-layout hot path.

The migration manifest classification remains unchanged. Reference
`WelcomeV2.tsx` stays excluded as a file-level port: the Go welcome renderer
owns this project-native behavior and does not claim reference parity.

## Rollback

Reverting P19.3.5 restores the flat semantic Header title without changing any
other P19 slice. P19.0.0-P19.3.4 remain the safe owners for theme propagation,
palette values, Eno, identity glyphs, spinner motion, Markdown, tool badges,
semantic static colors, the composer mode border, and the user-message panel.

Current rendering ownership is documented in
[`architecture/tui/README.md`](../../../architecture/tui/README.md#welcome-wordmark).
The completed fourteen-slice contract is retained at
[`migration/plans/p19-tui-revontuli-identity.md`](../../plans/p19-tui-revontuli-identity.md).
