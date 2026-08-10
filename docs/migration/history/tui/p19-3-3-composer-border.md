# P19.3.3 Revontuli Composer Border

**Status:** historical
**Completed:** 2026-07-25

> **Ownership:** delivery evidence, compatibility boundary, and rollback for
> the completed P19.3.3 mode-reactive composer-border slice

## Outcome

The rounded composer border now reflects the current visible mode on every
render:

| Mode | Semantic token |
|---|---|
| Default | brand teal from `AssistantPrefix` |
| Plan | sky from `AuroraSky` |
| Shell input | amber from `Warning` |
| Bypass permissions | red from `Error` |

Shell input has priority over Plan and bypass, matching the existing header
badge. The adoption decision is `project-native`: the visual signal is part of
the Revontuli design rather than a compatibility port from a reference agent.

## Preserved Boundary

- `App.permissionMode` remains the current permission projection and keeps an
  attached engine authoritative; the border stores no duplicate mode state.
- Input handling, permission transitions, keys, dialogs, runtime events,
  transcript, replay, and theme propagation are unchanged.
- The existing rounded border, padding, minimum width, responsive layout
  rectangles, and reduced-motion behavior are unchanged.
- P19.3.4 user-message panels, P19.3.5 welcome wordmark, P20 Plan interaction,
  and G9 display-cell work remain separate rollback units.

## Evidence

[`composer_border_test.go`](../../../../internal/tui/composer_border_test.go)
checks the four modes plus shell precedence in Polar Night, Daybreak, and dark
ANSI; drives sequential mode changes through one App to prove next-frame
updates; and preserves geometry at 48×18, 80×24, and 150×24 including the
reduced-motion path. Exact foreground SGR, width, and height are pinned in
[`composer_border.golden`](../../../../internal/tui/testdata/composer_border.golden).

The same `BenchmarkAppViewExplicitLayout` command measured 402 allocations per
operation before and after the change. The dedicated
`BenchmarkRenderEditorModeBorder` reports the same 347 allocations for all
four modes, proving mode selection itself does not introduce an
allocation-dependent branch.

Closeout used:

```text
go test ./internal/tui -run 'TestComposerBorder' -count=1
go test -race ./internal/tui -run 'TestComposerBorder' -count=1
go test ./internal/tui -run '^$' -bench 'Benchmark(AppViewExplicitLayout|RenderEditorModeBorder)$' -benchmem -count=3
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

The manifest classification does not change: this slice changes the current
TUI presentation owner and adds no reference-derived capability mapping.

## Rollback

Reverting this slice restores the single subtle border foreground. P19.0.0-
P19.3.2 remain the safe owners for live theme propagation, palette values,
mascot, glyph, spinner, Markdown, tool badges, and semantic static colors.

Current rendering ownership is documented in
[`architecture/tui/README.md`](../../../architecture/tui/README.md#composer-mode-border).
