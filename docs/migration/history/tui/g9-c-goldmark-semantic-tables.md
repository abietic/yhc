# G9.C Goldmark Semantic Tables

**Status:** historical
**Completed:** 2026-07-25

> **Ownership:** G9.C delivery decision, compatibility boundary, verification,
> and rollback evidence

## Outcome

The `combine` decision replaced the custom renderer's raw pipe-splitting
grammar with Goldmark GFM AST semantics for top-level complete tables.
Goldmark now defines headers, alignments, normalized row width, escaped pipes,
and inline structure. The project renderer consumes terminal-independent runs
for text, emphasis, code, strike, images, and links while the G9.B
`WidthProfile` remains the only geometry owner.

Pinned Goldmark v1.7.13 still treats an unescaped pipe inside a code span as a
table delimiter. A project-native same-byte-length parse view masks only pipes
inside syntactically closed, equal-length backtick runs. AST source segments
continue to read the unchanged canonical bytes at identical offsets; unmatched
or escaped openings retain the parser's ordinary behavior.

## Safety And Compatibility

Semantic text passes one terminal-safety boundary after entity decoding.
Literal or decoded C0/C1 controls become replacement characters, so model
Markdown cannot turn `&#27;` into a terminal command. Links and image
destinations open OSC 8 only when their UTF-8 is valid and they contain no
control or terminator byte. Width wrapping closes SGR/OSC state on every
physical line.

This slice did not change `StreamingMarkdown` stable-prefix promotion, cache
identity, or the temporary Glamour alignment repair. Nested blockquote/list
tables stay inside their Goldmark/Glamour container path; recursively replacing
their source lines here would discard container semantics. G9.D owns renderer
convergence for nested, stable-prefix, and finalized tables, and G9.E owns
repair deletion.

## Verification

Focused tests cover escaped and code-span pipes, multibyte and multi-backtick
offsets, escaped openings and code-span closing rules, empty/short/extra rows,
header-only and multiple tables, placeholder collisions, semantic inline runs,
invalid/control-bearing links, entity-decoded and literal terminal controls,
narrow fallback, and per-line SGR/OSC closure. Race validation covers the
shared parser and renderer path.

Repository closeout uses:

```text
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

Roll back semantic extraction, structured runs, and the parse-view adapter
together. Do not restore a separate raw pipe-splitting grammar alongside
Goldmark. Current behavior belongs in
[`architecture/tui/README.md`](../../../architecture/tui/README.md); remaining
G9 order belongs in
[`migration/PLAN.md`](../../PLAN.md).
