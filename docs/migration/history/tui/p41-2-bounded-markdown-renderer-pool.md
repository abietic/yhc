# P41.2 Bounded Markdown Renderer Pool

**Status:** historical
**Closed gaps:** G26
**Completed:** 2026-08-02
**Adoption:** `project-native`

> **Ownership:** completion evidence for the App-owned renderer lifetime,
> strict LRU bound, in-flight eviction safety, and G26 closure. Current behavior
> belongs in the [TUI architecture](../../../architecture/tui/README.md).

## Outcome

P41.2 replaced two process-global, unbounded maps with one private renderer
pool carried by `RenderEnvironment`. `App` creates a capacity-32 pool and keeps
the same pointer across theme and geometry generations. Zero-value
`StreamingMarkdown` compatibility callers retain their own capacity-32 pool;
there is no global renderer or lock index.

The exact key is unchanged: width, semantic theme, color profile,
display-cell profile, theme generation, geometry generation, and selection
mode all participate. A successful lookup advances a monotonic access
sequence. A miss constructs before mutation, evicts the strict least-recently
used indexed entry when full, and never allows indexed size to exceed 32.
Construction failure is not cached and still returns the safe literal output.

## One Entry Owns One Render Lifetime

Each indexed entry owns its exact key, `TermRenderer`, and serialization mutex.
The pool mutex protects lookup, construction, access order, insertion, and
eviction, but it is released before rendering begins. Eviction removes only
index ownership: a caller that already holds the entry pointer can finish with
the same renderer and mutex. A later lookup for that key creates a distinct
entry instead of resetting or reusing the old one.

This removes the separate renderer-to-lock map and its lifetime split. It does
not change Markdown parsing, styles, tables, ANSI behavior, selection markup,
display-cell geometry, transcript state, Session state, permissions, or
persistence.

## Proof

Focused tests establish strict `A/B/A/C` LRU order, exact size/create/eviction
counters, uncached construction failure and literal fallback, private
compatibility ownership, output equivalence after theme and resize churn,
barrier-controlled in-flight eviction, App pointer propagation, and concurrent
lookup/render/eviction. The focused race matrix passes ten repetitions.

The diagnostic benchmark command is:

```bash
go test ./internal/tui \
  -run '^$' \
  -bench '^BenchmarkP412MarkdownRendererPool(SteadyHit|GenerationChurn)$' \
  -benchtime=3x -benchmem -count=1
```

One Darwin/arm64 Apple M5 Pro run reported a zero-allocation steady hit. The
fixed churn operation reported 128 creations, 96 evictions, and a peak indexed
size of 32. Time and allocation figures remain diagnostic machine-local data,
not portable product budgets or retained-memory measurements.

Existing streaming, finalized, table, theme, ANSI, selection, display-cell,
and TUI package tests remain byte-compatible. Repository formatting, lint,
test, build, documentation, manifest, race, and diff gates close the candidate.

## Compatibility And Rollback

No durable schema or public API changed. A squash revert can restore the old
dual-map implementation without data migration, but that rollback reopens G26
and restores unbounded process lifetime. P41.1 remains a separate queued
geometry-owner slice and is not implied by this completion.
