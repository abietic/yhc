# TUI Performance Baseline

**Status:** verification
**Acceptance:** M7.4, the P10 workflow audit, and G11.F2
**Measured:** 2026-07-26
**Machine baseline:** Darwin arm64, Apple M5 Pro

> **Ownership:** reproducible TUI benchmark baseline and portable performance
> gates; current rendering behavior lives in
> [`architecture/tui/README.md`](../../architecture/tui/README.md)

## Contract

The benchmark numbers below are diagnostic baselines, not portable pass/fail
thresholds. The ordinary test gate owns user-visible p95 budgets:

| Path | Gate |
|---|---:|
| ordinary key to rendered frame | `< 50 ms` p95 |
| cached in-memory Agent thread switch plus frame | `< 100 ms` p95 |
| 20-Agent attention search plus picker overlay | `< 50 ms` p95 |
| 256-message first usable Agent transcript | `< 100 ms` p95 |
| 64 stream events reduced into a visible frame | `< 500 ms` p95 |
| 10K disk-backed transcript initial recent view | `< 500 ms` p95 |
| high-frequency streaming redraw | `<= 30 fps` |
| 10K frozen-history steady dirty frame | `< 20 ms` p95 |
| 100 live-sidebar-row steady frame | `< 50 ms` p95 |

The 10K in-memory transcript benchmark explicitly dirties the viewport cache on
every iteration. It therefore measures visible-row assembly with frozen item
caches, not the no-op cached `View()` fast path. The 20-Agent switch benchmark
uses already materialized per-thread views, which is the product contract for
normal navigation. Durable transcript loading has its own disk-backed recent
projection gate.

## Baseline

Command:

```bash
go test ./internal/tui ./engine ./engine/session -run '^$' \
  -bench 'Benchmark(ChatRender10KMessages|AgentThreadCatalog20|StreamBatch64|ThreadSwitch20Agents|RuntimeSnapshot20Agents|InspectRecent10KMessages)' \
  -benchmem -benchtime=200ms -count=3
```

Median of three runs for the first five benchmark families:

| Benchmark | Median | Bytes/op | Allocs/op |
|---|---:|---:|---:|
| 10K-message visible render | 4.81 us | 1,600 | 2 |
| 20-Agent catalog/filter/overlay | 198.54 us | about 69,000 | 808 |
| 64-event stream batch plus frame | 203.05 us | about 55,500 | 623 |
| cached thread switch plus frame | 110.74 us | about 281,000 | 435 |
| 20-Agent narrow runtime snapshot | 8.34 us | 19,936 | 49 |
| 20-Agent full runtime snapshot | 12.38 us | 27,616 | 109 |
| 10K-message disk recent projection | 220.63 us | about 1,126,000 | 59 |

The same run's p95 smoke gate observed 3.58 us for the 10K visible render,
296.88 us for a 20-Agent thread switch, 285.58 us for a 64-event batch, and
546.42 us for ordinary key-to-frame. The disk-backed 10K recent-view gate
observed 742.42 us p95; the explicit background event-to-visible-frame path
observed 321.21 us p95. These values provide regression context; only the
contract table is asserted across machines.

The P10 workflow audit added two composite gates. Three closeout runs observed
97.29-117.67 us p95 for a 20-Agent attention search plus picker overlay and
606.08-682.92 us p95 from switching into a cold 256-message child view to the
first usable visible transcript. The real PTY measured 34.2-58.5 ms for
`/agent`, visible-label search, selection, and first child transcript, plus
11.5-23.2 ms for failed evicted transcript retrieval. PTY values include
terminal transport and 10 ms polling granularity and remain diagnostic rather
than portable assertions.

G11.F2 adds two steady-frame diagnostics:

```bash
go test ./internal/tui -run '^$' -bench '^BenchmarkG11F2' \
  -benchmem -benchtime=200ms -count=3
```

On the machine baseline, the median 10K frozen-history dirty frame was
18.95 us with 1,344 bytes and 2 allocations per operation. The median
100-live-sidebar-row frame was 1.636448 ms with about 793 KB and
4,448-4,449 allocations per operation. These values are diagnostic; the
20 ms and 50 ms p95 rows above are the portable product gates.

The frozen-history test uses 10,000 native completed `HistoryItem` values with
semantic render counters. After warming, only a viewport-bounded subset may
enter the render cache, and a dirty steady frame must not increment any
counter. That structural assertion proves that a passing latency sample is not
hiding frozen-item re-segmentation or a full-history render.

## Architecture Implications

- `ChatView.Render` remains O(visible rows) because follow offset walks backward
  only until the viewport budget is filled and completed items use frozen
  render caches.
- `TaskAgentSnapshot` is the frequent-render selector. Full event/message rings
  are copied only for detail/replay paths.
- Per-thread presentation objects are retained and switched by identity;
  switching does not rebuild every Agent transcript or scan the full catalog.
- Stream deltas are losslessly reduced in batches of at most 64 and the timer
  window is `time.Second / 30`, making the redraw ceiling exact rather than an
  approximate 33 ms value.
- Catalog and switch allocations are the next optimization opportunity, but
  their measured latency is far below product budgets. Any allocation refactor
  must preserve defensive-copy and independent draft/search/selection state.
- A steady dirty `ChatView` frame may assemble visible cached rows but may not
  invoke completed-item rendering again. Wide sidebar projection remains
  bounded by its viewport even when the runtime selector contains 100 live
  rows.

## Reproduction

Run the benchmark command above after changes to `ChatView`, runtime selectors,
thread view cloning, event batching, session inspection, or responsive
rendering. Run `TestTUIHotPathPerformanceBudgets` and
`TestG11F2SteadyFramePerformanceBudgets` in an uninstrumented
`go test ./internal/tui` invocation; coverage instrumentation is diagnostic and
the timing tests skip it rather than treating instrumented p95 as product
latency. Keep `TestInspectRecent10KPerformanceBudget` and
`TestG11F2SteadyFrameKeepsFrozenHistorySegmentedAndViewportBounded` in the
normal test gate. Do not replace those p95/structural checks with
machine-specific nanosecond assertions.
