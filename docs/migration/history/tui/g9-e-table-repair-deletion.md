# G9.E Table Repair Deletion

**Status:** historical
**Closed gaps:** G9
**Completed:** 2026-07-25

> **Ownership:** G9.E deletion decision, terminal-variability boundary,
> verification, compatibility, and rollback evidence

## Outcome

The accepted `combine` program now has one complete-table geometry owner.
[`renderMarkdownFragment`](../../../../internal/tui/markdown.go) sends Glamour
output directly to semantic-island splicing; `fixTableAlignment`,
`trimTableRow`, and `padTableRow` are deleted. Complete top-level, stable, and
nested tables therefore keep Goldmark grammar, semantic runs, allocation,
wrapping, padding, borders, and assertions inside the same immutable
[`WidthProfile`](../../../../internal/tui/display_cell.go).

This slice changes no table source, style, container-prefix, streaming
completeness, cache identity, engine, permission, session, ProjectGraph, Eino,
or Eino-ext contract. It removes only the now-redundant mutation of
Glamour-rendered lines before semantic tables are spliced into them.

## Terminal Variability Boundary

The historical broad emoji-range estimate still protects non-table chat and
status chrome from overflowing terminals that display bare symbols as two
cells. It moved out of the Markdown renderer and is now explicitly named
`terminalLayoutSafetyWidth`, with
`isTerminalLayoutSafetyWideRune` as its private range policy.

This helper is deliberately conservative: a terminal that renders one of
those symbols narrowly may leave a spare column. It is not used by table
parsing, layout, rendering, cache identity, or test geometry. Tables consume
only the deterministic project `WidthProfile`, while future terminal-specific
profiles remain valid only if every table stage and cache identity adopt the
same generation.

## PTY And Geometry Evidence

[`markdown_table_pty_unix_test.go`](../../../../internal/tui/markdown_table_pty_unix_test.go)
starts the package test binary in 32-, 48-, and 72-column pseudoterminals.
Each helper finalizes one semantic table containing CJK, Indic, ZWJ emoji, a
bare label, and a flag. The parent capture proves:

- literal pipe rows are absent and semantic borders are present;
- every row and border has equal `WidthProfile` geometry;
- no physical line exceeds the PTY column count; and
- every line closes SGR and OSC 8 state.

The independent terminal-layout-safety test retains the exact historical
ASCII, ANSI, bare-symbol, lone-regional-indicator, flag, and mixed-text
results while also proving that a lone regional indicator remains one cell in
the table profile. This makes the deterministic table default and the
terminal-variability guard observably separate.

## Verification

Focused closeout includes:

```text
go test ./internal/tui -run 'TestG9ESemanticTablePTYGeometry|TestCompletedSemanticTableAlignment|TestTerminalLayoutSafetyWidth|TestAlignStatusLineConservativeFallback' -count=10
go test -race ./internal/tui -count=1
rg 'emojiAwareWidth|isEmojiWide|fixTableAlignment|trimTableRow|padTableRow' internal/tui
```

Repository closeout uses:

```text
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

## Compatibility And Rollback

Chat and status overflow decisions retain their previous conservative
behavior under the new non-table name. Markdown tables may no longer be
silently trimmed or padded after rendering; any future mismatch must be fixed
in Goldmark semantic extraction, `WidthProfile`, or structured layout.

Do not restore the deleted repair as an emergency second owner. Roll back to
the last structured semantic renderer boundary and correct that owner, or
reopen G9 explicitly if evidence disproves the deterministic profile. Current
architecture belongs in
[`architecture/tui/README.md`](../../../architecture/tui/README.md); future
work belongs in [`migration/PLAN.md`](../../PLAN.md).
