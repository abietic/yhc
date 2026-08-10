# P41.2 Renderer Pool Promotion Evidence

**Status:** verification
**Snapshot:** `5d13285719ea2f73d7b8c6e1d5fe2e1f264e7165`
**Measured:** 2026-08-02

> **Ownership:** promotion-snapshot cache characterization, the selected
> renderer-pool capacity, and the proof contract later established by P41.2

## Result

At the measured promotion snapshot, 64 geometry generations at one width and
two render modes retained 128 renderers and 128 independent serialization
locks. Neither map evicted. That evidence selected one App-owned, 32-entry
strict-LRU pool whose entry owns both the renderer and its lock.

P41.2 is now complete and G26 is closed. Current behavior and final proof are
recorded in
[`p41-2-bounded-markdown-renderer-pool.md`](../history/tui/p41-2-bounded-markdown-renderer-pool.md).

The bound controls lookup retention, not rendering correctness. An entry may be
removed from the index while an existing holder finishes through its retained
pointer. Markdown output, exact render identity, and the safe literal fallback
remain unchanged.

## Promotion behavior was deterministically unbounded

The named snapshot stored renderers by exact width, theme, color, profile,
generation, and selection identity in one process-global map and their locks
in a second pointer-keyed map. The render path performed a separate lock lookup
after renderer acquisition.

The promotion characterization held width and theme constant, advanced only
the geometry generation, and requested normal and selection renderers. The
result was exactly 128 renderers and 128 locks, four times the selected target.
The implementation replaced that temporary defect characterization with
hard-bound desired-behavior tests.

## Why the capacity is 32

The measured scenario creates two exact keys per environment. A capacity of 32
therefore retains the latest 16 such generations and caps the index even when
resize or theme generations continue indefinitely. More simultaneous widths
may reduce hit rate but cannot change output because every miss constructs a
fresh exact-key renderer.

This is an initial retention policy, not a claim that 32 is a globally optimal
performance value. Any later capacity change requires new churn and steady-hit
evidence and remains subject to a hard App-local bound.

The current diagnostic run measures steady hits and fixed churn through the
completed pool:

```bash
go test ./internal/tui \
  -run '^$' \
  -bench '^BenchmarkP412MarkdownRendererPool(SteadyHit|GenerationChurn)$' \
  -benchtime=3x -benchmem -count=1
```

The pre-implementation snapshot reported 128 renderers/op, 2,405,806 ns/op,
6,070,776 B/op, and 24,450 allocs/op on one Darwin/arm64 Apple M5 Pro run. The
completed fixed-churn operation reports 128 creations, 96 evictions, and peak
indexed size 32. Machine-specific time and allocation numbers are diagnostic,
not retained-memory measurements or portable performance budgets.

## The implementation proves one atomic lifetime

The completed implementation establishes every row below:

| Boundary | Required proof |
|---|---|
| Ownership | One pool is created with `App` and the same pointer survives style and geometry projection; compatibility constructors get private pools and no global fallback. |
| Exactness | Width, semantic theme, color profile, display-cell profile, theme generation, geometry generation, and selection mode remain in the key. |
| Bound and order | Deterministic churn proves strict LRU order, exact counters, and indexed size never exceeding 32. |
| Render equivalence | Existing streaming, finalized, table, theme, ANSI, and selection goldens remain byte-equivalent before and after eviction. |
| In-flight eviction | A barrier holds one entry while other keys evict it; the old holder finishes, and a later lookup creates a distinct indexed entry. |
| Concurrency | Parallel lookup, rendering, and forced eviction are race-clean and deadlock-free. |
| Performance | Steady-hit and churn benchmarks report time, allocations, peak size, creations, and evictions as diagnostics. |

Use synchronization barriers rather than timing windows for the in-flight
test. Run its race matrix repeatedly:

```bash
go test -race ./internal/tui \
  -run '^TestP412MarkdownRendererPool' \
  -count=10
```

The final implementation also runs the repository Makefile gates. The earlier
green characterization alone did not close P41.2 or G26.

## Reference and evidence limits

Crush snapshot `2af939d8e900f15edb5e78d766ff0b74dd4fe87e` atomically clears
its renderer and lock maps on a theme change and explicitly allows old holders
to finish. It still uses process-global, width-keyed maps and supplies no hard
capacity. P41.2 therefore keeps the safe old-holder consequence but uses a
project-native App lifetime and LRU bound rather than copying that owner.

The promotion itself changed no production code. The completed implementation
does not claim a resident-memory ceiling, change display-cell geometry, tune
P41.1, or make machine-specific benchmark values normative.
