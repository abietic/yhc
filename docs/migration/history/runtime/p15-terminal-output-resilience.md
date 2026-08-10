# P15 Terminal Output Resilience Plan

**Status:** historical
**Last verified:** 2026-07-17

> **Ownership:** completed slow-terminal measurement and bounded terminal
> output ownership contract

Root [`migration/PLAN.md`](../../PLAN.md) owns execution order and slice state.
Current guarantees belong in
[`architecture/tui/contracts/terminal-lifecycle.md`](../../../architecture/tui/contracts/terminal-lifecycle.md).
Grok Build evidence remains in
[`migration/reference/tui/grok-build.md`](../../reference/tui/grok-build.md); Pi's
changed-span renderer evidence remains in
[`migration/reference/tui/pi.md`](../../reference/tui/pi.md).

## Decision

P15 is a `combine` decision at the contract level. P15.0 was a `defer`
decision at the mechanism level until local evidence existed:

- combine Grok Build's "drain before terminal restore" invariant and explicit
  late-frame concern with Eino-Agent's Bubble Tea lifecycle;
- use Pi's render coalescing as comparative evidence for reducing unnecessary
  output;
- defer a dedicated writer goroutine/thread until a deterministic P15.0 probe
  reproduces a local failure.

That gate failed, and P15.1 completed the smallest project-native adaptation:
a synchronously acknowledged single-writer adapter with no project frame
queue. It did not copy a reference writer by identity.

P15.0 completed as a test-only slice without waiting for the ADK kernel
migration. It reproduced deterministic blocked-writer shutdown and silent
writer-error failures. P15.1 repaired those production-path failures while
retaining the direct Bubble Tea probes as dependency-characterization tests.

## Current Baseline And Gap

Tests now freeze normal Bubble Tea exit, Ctrl+D, suspend/resume, panic cleanup,
PTY restoration, blocked writes, late sends, rapid invalidation, writer errors,
runtime-state preservation, and a paused-reader Unix PTY. They prove the
current replacement-frame buffer is bounded and ordered after a blocked write
returns.

Production now routes Bubble Tea output through `TerminalOutput`. Its
unbuffered request channel and synchronous acknowledgement permit one packet in
flight, surface the first sink failure once, and bound write, drain, and
platform interrupt stages. Fallback terminal restoration runs only after the
writer has stopped. Grok's dedicated writer remains comparative evidence, not
the mechanism adopted here.

## Frozen Program Invariants

1. Exactly one component owns final terminal writes at any instant.
2. No application frame may be written after cursor/raw/alternate-screen
   restoration completes.
3. Quit, suspend, panic, and normal completion share one explicit ordering model
   even if their cleanup mechanics differ.
4. A slow or failed sink cannot create an unbounded in-memory queue.
5. Lossless runtime terminal, interaction, and child lifecycle state is reduced
   before any presentation frame can be dropped or coalesced.
6. Output optimization never changes runtime event order, transcript content,
   permission settlement, or session state.
7. Inline, image, enhanced-key, or new screen modes are outside this program.

## P15.0 Deterministic Terminal Stress Baseline

**Completed:** 2026-07-17

### User outcome

Maintainers can reproduce slow-output, late-frame, and teardown races before
changing the terminal architecture. A green baseline proves current Bubble Tea
ownership is sufficient; a red categorized result is the only entry gate for
P15.1.

### Allowed scope

- test-only helper models, PTY fixtures, and instrumented writers;
- a narrow package-private injection seam only when required to observe write
  order deterministically;
- verification documentation for portable and machine-specific measurements;
- no production behavior change.

### Required scenarios

1. a deterministic writer barrier holds one frame while quit is requested;
2. a slow PTY reader receives sustained streaming/tool/Agent progress followed
   by normal quit;
3. panic occurs with a frame pending;
4. suspend drains or abandons pending presentation safely, then resumes with a
   clean ownership state;
5. terminal completion races with late tool/child progress;
6. writer failure surfaces a bounded diagnostic and does not deadlock shutdown;
7. resize and rapid render invalidation do not produce unbounded pending output.

### Pass/fail rules

- an instrumented sequence proves no frame write occurs after the restore
  boundary;
- every helper exits within its explicit context deadline with no goroutine
  leak;
- the slow reader receives a valid restore suffix and the parent shell remains
  usable;
- pending output has a measured finite upper bound; if current Bubble Tea owns
  no queue, the probe records that fact rather than inventing one;
- terminal event and unresolved-attention runtime snapshots remain complete
  even when presentation frames are intentionally skipped by the fixture;
- repeated runs produce the same categorical result; wall-clock observations
  are reported separately and are not used as flaky golden text.

### Atomicity and rollback

P15.0 cannot modify terminal write behavior, add a queue, or alter Bubble Tea
program options. Rollback removes only test helpers and private observation
seams. P15.1 was blocked until an equivalent reproduced failure existed.

### Measured result

The portable and Unix PTY fixtures establish:

- graceful quit, panic cleanup, and `ReleaseTerminal` cannot finish while the
  current renderer is inside a blocked `io.Writer`;
- once that write returns, application frames precede restoration and late
  sends produce no new output;
- writer errors are ignored by Bubble Tea and never become a program error or
  bounded diagnostic;
- resize followed by rapid invalidation holds one replacement frame and
  backpressures the next update rather than growing an unbounded project queue;
- a paused Unix PTY reader receives sustained streaming/tool/Agent content
  after release, the complete restore suffix follows, and the parent shell is
  usable; and
- engine terminal identity and unresolved attention survive presentation-frame
  omission because runtime reduction remains the lossless owner.

The exact commands, categories, and evidence limitations are in
[`terminal-output-resilience.md`](../../verification/terminal-output-resilience.md).
The deterministic blocked-shutdown result opens P15.1.

## P15.1 Bounded Output Ownership Change

**Completed:** 2026-07-17

### Entry gate

**Gate result:** closed. P15.0 reproduced a deterministic event-loop/shutdown
block caused by a held writer. It also reproduced silent writer errors. P15.1
retains the following original gate categories as regression fixtures:

- a frame written after restoration;
- an unbounded pending-output growth path;
- a deterministic event-loop or shutdown deadlock caused by a blocked writer;
- terminal restore corruption under a supported normal, suspend, or panic path.

A subjective impression that rendering is slow is insufficient. The failing
fixture, measured ownership path, and rollback criterion must be linked from
`PLAN.md` before implementation starts.

### Selection order

Choose the smallest mechanism that closes the reproduced failure:

1. Bubble Tea configuration or lifecycle correction;
2. render invalidation/coalescing at the existing App boundary;
3. a bounded single-writer adapter with explicit overflow policy;
4. a dedicated writer goroutine only if the first three cannot satisfy the
   deterministic fixture.

### Required contract if a queue is introduced

- one bounded queue with documented capacity and coalescible frame classes;
- terminal restore and interaction frames are never silently dropped;
- one close state rejects later frames;
- drain has a bounded deadline and a defined abandon path;
- the writer reports failure once and cannot recursively enqueue diagnostics;
- cleanup orders final frame decision, writer close/drain, cursor and screen
  restoration, then raw-mode release;
- normal, suspend, panic, and failed-writer paths share the same state machine;
- no package other than the selected writer owner writes application frames
  directly.

### Excluded scope

- copying Grok's writer thread or Pi's renderer without the reproduced gate;
- replacing Bubble Tea/Lip Gloss;
- alternate inline/minimal screen modes;
- terminal images, enhanced keyboard protocols, mouse UX, or visual redesign;
- coalescing lossless runtime events instead of presentation frames.

### Acceptance gate

- every failing P15.0 category has a green wrapped-path fixture without
  weakening the direct dependency assertion;
- the one-in-flight bound, no-queue decision, writer failure, drain deadline,
  and late-send rejection have focused deterministic tests;
- existing PTY normal/panic/suspend/quit, streaming, and Agent-detail workflows
  pass; P14.3 remains downstream of this repaired terminal gate;
- frame optimization does not change runtime snapshots, transcript, terminal
  reason, or interaction settlement;
- performance evidence shows no material regression for ordinary local PTYs.

### Completed mechanism

- Bubble Tea receives one `TerminalOutput` writer through `tea.WithOutput`.
- An unbuffered handoff and synchronous result acknowledgement bound pending
  project output to one copied packet; no coalescing queue or overflow policy
  was needed.
- Production budgets are 750 ms for a write, 1 s for drain, and 250 ms for
  platform interruption.
- Unix duplicates the output descriptor, uses nonblocking netpoll deadlines,
  closes the duplicate to interrupt, and restores the original descriptor
  flags. Windows duplicates the handle and cancels the writer thread with
  `CancelSynchronousIo` before closing the duplicate.
- The first writer failure kills the Bubble Tea program, is returned once as a
  typed terminal-output error, and permits direct fallback restoration only
  after `Stopped` is true.
- Focused race tests cover ordering, concurrent callers, late-write rejection,
  sink failure, write/drain/interrupt deadlines, Unix flag restoration, and
  production-path fail-closed cleanup. Existing normal/panic PTY and P15.0
  dependency-characterization probes remain green.

### Rollback

Keep the previous direct Bubble Tea path available only as a code rollback, not
a live runtime flag. Since P15 owns presentation output only, rollback requires
no transcript, session, checkpoint, or runtime-state migration.

## Per-Slice Closeout

P15.0 and P15.1 ran focused PTY/stress checks, `make docs-check`, manifest
validation, `git diff --check`, `make fmt`, `make lint`, `make test`, and
`make build`. P15.1 additionally ran race tests, repeated PTY loops,
cross-platform compilation, and the terminal-output microbenchmark before the
architecture owner was updated.
