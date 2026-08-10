# P19.2.1 Revontuli Spinner Breathing Pulse

**Status:** historical
**Completed:** 2026-07-25

> **Ownership:** delivery and closeout evidence for the P19.2.1 main and
> inline running-spinner glyph motion replacement

## Outcome

P19.2.1 removed the borrowed `· ✢ * ✶ ✻ ✽` ping-pong and replaced it with the
project-owned filled identity star `✦`. One symmetric eight-step foreground
sequence runs on the existing 120ms activity tick, so the full breathing cycle
repeats after 960ms without another timer or state owner.

The main activity icon peaks at the semantic `AssistantPrefix` brand style.
Stalled and inline task/tool icons keep their existing caller-selected peak
styles. Truecolor themes interpolate from `Subtle` to that peak; ANSI themes
choose between the same two semantic palette colors and never synthesize a
truecolor escape. Reduced motion renders the peak style statically while the
existing functional polling schedule remains active.

## Preserved Contracts

The slice does not change:

- contextual verb selection or classifier/hook overrides;
- effort, elapsed-time, provider token, waiting, or stalled text;
- verb shimmer colors, phase, or ordering;
- activity tick ownership, scheduling, state transitions, or cancellation;
- completed, failed, killed, and pending task icons.

The three-stop aurora verb shimmer, waiting-sky semantic, stalled-token
projection, and ANSI flat-cyan shimmer remain owned by P19.2.2.

## Evidence

- Pure sequence tests pin all eight intensities, negative wrapping, and the
  exact 960ms repeat boundary.
- Truecolor and ANSI tests pin semantic endpoint/interpolation behavior and
  ensure ANSI fallback contains no truecolor value.
- Reduced-motion tests pin a static peak-styled glyph while the existing
  accessibility test preserves functional polling and a frozen animation
  counter.
- Main, stalled, inline tool, and local/remote task renderers are checked for
  the filled star and absence of every borrowed frame glyph.
- The spinner pulse, app-layout, and product-state goldens record the sequence
  endpoints and normalized visible output.
- The complete TUI package and repository formatting, lint, test, build,
  migration-manifest, documentation, and whitespace gates passed after source
  and documentation synchronization.

## Compatibility And Rollback

This `project-native` presentation change deliberately removes borrowed visual
motion. It does not change runtime events, model/tool behavior, permissions,
durable state, or non-TUI entrypoints. Rollback restores the old frame list and
the five renderer call sites, plus the three affected goldens; the shared
120ms tick and all text/state contracts remain intact.

Current ownership and remaining work live in
[`architecture/tui/README.md`](../../../architecture/tui/README.md),
[`migration/STATUS.md`](../../STATUS.md), and
[`migration/PLAN.md`](../../PLAN.md).
