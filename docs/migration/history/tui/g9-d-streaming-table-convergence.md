# G9.D Streaming Table Convergence

**Status:** historical
**Completed:** 2026-07-25

> **Ownership:** G9.D delivery decision, visible streaming behavior,
> verification, compatibility, and rollback evidence

## Outcome

The `combine` decision made
[`StreamingMarkdown`](../../../../internal/tui/markdown.go) the only table
fragment lifecycle and cache owner. Stable-prefix promotion and Finalize now
route completed tables through the same Goldmark semantic extraction,
`WidthProfile` layout, and ANSI/OSC encoding path. Tables descended from the
active final top-level block alone remain source-like literal rows until a
following sibling or Finalize proves completeness.

This closed the former switch among custom finalized top-level tables,
Glamour stable-prefix tables, and Glamour nested-container tables. It did not
replace the non-table Markdown renderer or change engine, transcript, replay,
permission, ProjectGraph, Eino, or Eino-ext ownership.

## Container And Failure Boundary

[`extractTableIslands`](../../../../internal/tui/table_render.go) replaces each
Goldmark table range with a short collision-free alphanumeric sentinel inside
a fenced text block. Glamour renders that skeleton only to project the
blockquote/list continuation prefix. The splice path requires exactly one raw
and one ANSI-stripped sentinel occurrence, removes the fixed two-cell code
margin, restyles only visible quote bars, and prefixes every physical semantic
or literal line. Ordered and unordered item markers therefore stay on the
preceding item line and are not repeated by table rows.

Missing or duplicate sentinels fail closed to sanitized literal source.
Deferred rows replace invalid UTF-8 and C0/C1 controls, expand tabs, and
hard-wrap through the selected width profile. Completed semantic rows still
accept SGR and OSC 8 only from validated metadata and close control state on
every physical line.

## Cache And Streaming Contract

Renderer, stable-prefix, and full-output identities now include canonical
source, terminal width, semantic theme, explicit color profile,
width-profile generation, and one of streaming-incomplete, stable-complete,
or finalized-complete. Theme, profile, width, source replacement, promotion,
and Finalize cannot reuse the wrong projection.

The visible compatibility change is deliberate: a syntactically valid table
at the end of a live stream remains literal instead of becoming a provisional
grid. Appending a following sibling promotes it once to the semantic renderer;
Finalize performs one canonical complete-source reflow. This avoids a second
table renderer and row-by-row layout churn while retaining the bounded
stable-prefix cache.

## Verification

Focused tests cover:

- every valid UTF-8 append boundary, live literal output, promotion, Finalize,
  resize, theme, profile generation, and completeness invalidation;
- top-level, blockquote, unordered/ordered list, and blockquote-list
  projection without repeated list markers;
- multiple tables, source-marker collisions, missing/duplicate sentinel
  failure, controls, invalid UTF-8, hard-wrap bounds, and per-line SGR/OSC
  closure;
- all six supported themes at widths 10, 18, and 48; and
- the retained direct `fixTableAlignment` characterization that G9.E must
  delete rather than silently abandon.

The stable-prefix/active-table benchmark re-renders only the mutable tail
beside a 7,100-byte committed prefix. The full Markdown render and incremental
benchmarks remain in the same measured order as the G9.C baseline; benchmark
values are machine observations rather than portable pass/fail thresholds.
Focused tests, the complete `internal/tui` suite, and full package race
validation passed before repository closeout. Narrow vertical fallbacks are
also characterized at widths 1, 2, 9, and 10 with empty fields and ANSI/OSC
content; every physical line remains width-bounded with balanced control state.

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

Roll back the completeness-aware fragment routing, semantic-island projection,
and cache identity together. Do not restore a mixed state in which stable or
nested complete tables return to Glamour ownership while finalized tables use
the semantic renderer. The current implementation belongs in
[`architecture/tui/README.md`](../../../architecture/tui/README.md); the
completed repair-deletion boundary is recorded in
[`g9-e-table-repair-deletion.md`](g9-e-table-repair-deletion.md).
