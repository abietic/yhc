# P19.3.2 Revontuli Semantic Colors

**Status:** historical
**Completed:** 2026-07-25

> **Ownership:** delivery evidence, compatibility boundary, and rollback for
> the completed P19.3.2 semantic-color slice

## Outcome

`theme.go` is now the only production TUI file that may declare literal hex
colors or construct `lipgloss.Color` from a string literal. `defaultStyles`
delegates to the Polar Night theme pipeline, so legacy/test construction and
runtime construction no longer carry independent palettes.

The visible mappings are:

| Surface | Semantic token |
|---|---|
| Informational error title and running tool state | brand teal (`ToolRunning`) |
| Warning title, shell badge, medium-risk and MCP warning surfaces | warning amber (`Warning`) |
| Error title, bypass badge, and high-risk surfaces | error red (`Error`) |
| Plan badge and answer review | sky (`AuroraSky`) |
| Dialog titles/tabs and provider labels | permission violet (`DialogTitle`) |
| Search bars and selected-message surfaces | `Element` and `Selection` |

Truecolor Markdown syntax highlighting now derives from the same palette.
ANSI Markdown still bypasses Glamour Chroma and remains ANSI-16-only. The
adoption decision was `adapt`: retain useful severity/category distinctions
while eliminating the borrowed literal color table and keeping the
project-owned explicit theme-identity boundary.

## Preserved Boundary

- `App` remains the only active-theme owner; no package-global active theme was
  restored from the earlier combined candidate.
- Error classification, permission decisions, Plan/Question interaction,
  tool status, Markdown source/cache lifecycle, transcript, and replay
  semantics are unchanged.
- P19.3.3 composer borders, P19.3.4 user-message panels, P19.3.5 welcome
  wordmark, P20 Plan interaction, and G9 display-cell work remain separate
  rollback units.
- Search overlays use the existing raised `Element` surface; this slice does
  not pre-implement the P19.3.4 `UserMessageBlock` presentation.

## Evidence

[`semantic_color_test.go`](../../../../internal/tui/semantic_color_test.go)
recursively rejects production TUI hex literals and literal
`lipgloss.Color` constructors outside `theme.go`. It also verifies every
supported theme maps running state to brand rather than warning, checks
severity-title mappings, drives production header mode selection, and compares
Polar Night, Daybreak, and dark-ANSI output with
[`semantic_colors.golden`](../../../../internal/tui/testdata/semantic_colors.golden).
[`highlight_verify_test.go`](../../../../internal/tui/highlight_verify_test.go)
pins the semantic Chroma downsampling used by labeled truecolor code blocks.

Closeout used:

```text
go test ./internal/tui -run 'Test(NoHardcodedColorsOutsideTheme|SemanticStatusColorsFollowThemeTokens|HeaderModeBadgesUseSemanticTokens|SemanticColorGolden)$' -count=1
go test -race ./internal/tui -run 'Test(NoHardcodedColorsOutsideTheme|SemanticStatusColorsFollowThemeTokens|HeaderModeBadgesUseSemanticTokens|SemanticColorGolden)$' -count=1
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

Reverting this slice restores the scattered literal color sources, warning-
colored running state, and duplicate `defaultStyles` palette. P19.0.0-P19.3.1
remain the safe owners for live theme propagation, palette values, mascot,
glyph, spinner, Markdown surface identity, and semantic tool badges.

Current rendering ownership is documented in
[`architecture/tui/README.md`](../../../architecture/tui/README.md#semantic-color-boundary).
