# P19.3.4 Revontuli User-Message Panel

**Status:** historical
**Completed:** 2026-07-25

> **Ownership:** delivery evidence, compatibility boundary, and rollback for
> the completed P19.3.4 user-message panel slice

## Outcome

Every visible row of a user message now begins with the project-owned teal
`▎` edge instead of the tool-status-like `●`. Polar Night and Daybreak keep the
full row on their `userBg` surface; dark ANSI renders the same brand edge
without inventing a background. The scrolled sticky prompt uses the same
treatment.

The adoption decision is `project-native`: the panel follows the Revontuli
message hierarchy, making human input visually distinct from agent and tool
output.

## Preserved Boundary

- `UserMessage` content, composer elements, history identity, finished/version
  state, raw projection, and cache ownership are unchanged.
- The established `width-4` wrap budget and final rendered width are
  unchanged. This slice does not alter the rune-based wrapper; G9 owns the
  separate display-cell contract.
- Scroll offset/follow transitions, sticky lookup/truncation, selection,
  transcript/replay, and non-user message renderers are unchanged.
- P19.3.5 welcome wordmark, P20 Plan interaction, and G9 display-cell work
  remain separate rollback units.

## Evidence

[`user_message_panel_test.go`](../../../../internal/tui/user_message_panel_test.go)
checks every row's brand edge in Polar Night, Daybreak, and dark ANSI, verifies
the truecolor background reaches the content after the nested foreground
reset, preserves long-message width and raw content, and drives the scrolled
sticky projection. Exact ANSI output is pinned by
[`user_message_panel.golden`](../../../../internal/tui/testdata/user_message_panel.golden);
the app-layout and multi-width product-state goldens pin the normalized visible
change without altering tool/agent status dots.

The pre/post `BenchmarkAppViewExplicitLayout` result remains 402 allocations
per operation. The dedicated user-panel benchmark records its direct renderer
cost without changing the cached App hot path.

Closeout used:

```text
go test ./internal/tui -run 'TestUserMessagePanel|TestApplyThemeRestylesRenderedOutput' -count=1
go test -race ./internal/tui -run 'TestUserMessagePanel|TestApplyThemeRestylesRenderedOutput' -count=1
go test ./internal/tui -run '^$' -bench 'Benchmark(AppViewExplicitLayout|UserMessagePanel)$' -benchmem -count=3
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

Reverting this slice restores the first-row `●`, two-space continuation rows,
and the `❯` sticky prefix. P19.0.0-P19.3.3 remain the safe owners for live
theme propagation, palette values, mascot, glyph, spinner, Markdown, tool
badges, semantic static colors, and the composer mode border.

Current rendering ownership is documented in
[`architecture/tui/README.md`](../../../architecture/tui/README.md#user-message-panel).
