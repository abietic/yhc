# P19.3.0 Revontuli Markdown Palette

**Status:** historical
**Completed:** 2026-07-25

> **Ownership:** delivery evidence, compatibility boundary, and rollback for
> the completed P19.3.0 Markdown palette and cache-identity slice

## Outcome

Markdown headings, inline code, quotes, and horizontal rules now use the
Revontuli palette on finalized assistant output, compatibility streaming
output, and the Plan dialog. H1 uses brand teal, H2 sky, H3 permission violet,
H4-H5 inactive, and H6 subtle. Inline code uses sky on the independent
`element` surface; quote content is inactive behind a brand `▎`; rules use the
subtle-border token.

The App remains the only active-theme owner. `Styles` carries a private
canonical identity, and every production Markdown caller passes it explicitly.
The renderer pool keys wrap width plus immutable semantic palette identity.
The per-message stable-prefix/full-output cache tracks that same identity and
invalidates before its exact-cache fast path. A finalized source retains its
lifecycle while rerendering under the new theme.

Dark ANSI selects Glamour's ANSI profile and disables the custom Chroma path,
whose terminal256 formatter otherwise bypasses that profile. Polar Night,
Daybreak, and dark-ANSI actual output is pinned. The adoption decision was
`adapt`: reference theme-sensitive cache identity is retained through an
explicit Go/Bubble Tea style boundary, without copying the React component or
adding a process-global theme.

## Preserved Boundary

- No runtime event, interaction, permission, dialog option, layout rectangle,
  clock, streaming throttle, stable-boundary algorithm, table renderer, or
  persistent field changed.
- Truecolor syntax highlighting retains its previous Glamour/Chroma behavior;
  only ANSI uses the plain code-block fallback to guarantee ANSI-16 output.
- The renderer cache remains per-renderer locked, and the cached Markdown hot
  path remains zero-allocation.
- P19.3.1 tool badges, P19.3.4 message surfaces, P20 Plan interaction, and G9
  display-cell work remain separate slices.

## Evidence

[`markdown_theme_test.go`](../../../../internal/tui/markdown_theme_test.go)
pins semantic token mapping, actual rendered output for Polar Night, Daybreak,
and dark ANSI, ANSI escape containment, stable-prefix and finalized cache
invalidation, and Plan-dialog live-style propagation.
[`markdown_theme.golden`](../../../../internal/tui/testdata/markdown_theme.golden)
records the visible output and active SGR state at each owned Markdown surface.
The existing streaming golden and full TUI package tests remained green.

Closeout used:

```text
go test ./internal/tui -run <P19.3.0-focused-pattern> -count=1
go test -race ./internal/tui -run <P19.3.0-cache-and-golden-pattern> -count=1
go test ./internal/tui -run ^$ -bench BenchmarkStreamingMarkdown -benchmem
go test ./internal/tui -count=1
make fmt
make lint
make test
make build
make lint-new
make docs-check
go run ./scripts/migration_manifest.go check
git diff --check
```

## Rollback

Reverting this slice restores the width-only renderer pool and literal
Markdown colors. P19.0.0-P19.2.2 remain the safe owner for live style
propagation, palette values, mascot, glyph, and spinner identity.

Current Markdown and theme ownership is documented in
[`architecture/tui/README.md`](../../../architecture/tui/README.md#markdown-theme-boundary).
