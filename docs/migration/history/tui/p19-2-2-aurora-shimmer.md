# P19.2.2 Aurora Spinner Shimmer

**Status:** historical
**Completed:** 2026-07-25

> **Ownership:** delivery evidence, compatibility boundary, and rollback for
> the completed P19.2.2 spinner-verb shimmer slice

## Outcome

The TUI replaced the borrowed amber/orange spinner-verb treatment with the
Revontuli aurora sequence while preserving the existing spinner lifecycle.
Thinking, responding, and tool-use modes now share one 2.4-second sine phase on
the existing 120ms clock. The positional three-cell glimmer remains unchanged;
its highlight interpolates from brand teal through `AuroraSky` to the
permission-violet foreground.

Both ANSI themes use the flat cyan `SpinnerShimmer` semantic without
constructing a truecolor value. Reduced motion renders the whole verb through
the static brand style. After the existing quiet threshold, early waiting uses
`AuroraSky` and full stall uses `SpinnerStalled`.

The adoption decision was `adapt`: the useful reference glimmer and waiting
outcomes remain, but their identity, period, fallback, and accessibility
behavior are project-owned.

## Preserved Boundary

- The fixed `✦` and eight-tick P19.2.1 foreground pulse remain the only glyph
  animation.
- No timer, Bubble Tea message, runtime event, state owner, layout rectangle,
  cache, or durable field changed.
- Spinner verbs, the three-second thinking delay, positional sweep, waiting
  text, classifier/hook override, and effort/elapsed/token suffix ordering
  remain intact.
- Inline Agent/task/tool icons continue using their caller-owned P19.2.1 peak
  styles; P19.2.2 changes only the main spinner verb and waiting/stalled
  treatment.

## Evidence

[`spinner_aurora_test.go`](../../../../internal/tui/spinner_aurora_test.go)
pins the four truecolor palettes, both ANSI fallbacks, phase period, reduced
motion, waiting/stalled semantic colors, and deterministic verb rendering.
[`spinner_aurora.golden`](../../../../internal/tui/testdata/spinner_aurora.golden)
records Polar Night, Daybreak, and both ANSI paths. Existing P19.2.1 pulse
tests remained green.

Closeout used:

```text
go test ./internal/tui -run <P19.2.1-and-P19.2.2-focused-pattern> -count=20
go test -race ./internal/tui -run <P19.2.2-focused-pattern> -count=1
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

Reverting this slice restores the two-stop amber shimmer and subtle early
waiting color. P19.2.1 remains the safe owner for the fixed glyph, semantic
pulse, reduced-motion icon, inline running icons, and existing 120ms clock.

Current spinner ownership is documented in
[`architecture/tui/README.md`](../../../architecture/tui/README.md#spinner-motion-ownership).
