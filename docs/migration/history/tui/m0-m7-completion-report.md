# Modern TUI M0-M7 Completion Report

**Completed:** 2026-07-11
**Scope:** `engine/`, `tools/`, `internal/tui/`, CLI/ACP integration, session and
transcript storage, terminal lifecycle, verification infrastructure, and
migration documentation
**Status:** historical
**Closeout result:** M0-M7 complete at the recorded snapshot; 3,515 tests and four platform builds passed

> **Ownership:** M0-M7 closeout summary and evidence pointers; not current
> architecture or an active plan

## Executive Summary

This stage turns the Bubble Tea client from a leader-only coding chat with
several disconnected Agent panels into a coherent multi-thread coding-agent
experience. Runtime truth now belongs to an engine reducer with stable session,
thread, turn, Agent, sequence, and causal identity. The TUI projects that truth
into switchable leader and Agent conversations, owner-scoped approvals, live
progress, semantic tool history, a structured per-thread composer, durable
session recovery, and a terminal-safe responsive interface.

The work deliberately retains the imperative Eino query loop and Bubble Tea
renderer. Claude Code Ripe remains the behavioral porting specification; Codex
and Crush provide post-parity interaction and architecture references. This
avoided a framework rewrite while addressing the actual product constraints:
state ownership, replay, asynchronous attention, thread navigation,
long-session performance, and terminal restoration.

## Original Problems

Before this track, the project had broad structural parity but five systemic TUI
weaknesses remained:

1. Leader and subagent activity did not share a canonical event/read model.
2. Agent panels summarized work but did not behave as durable conversations.
3. Permissions and questions were global modal state instead of thread-owned
   pending interactions.
4. Composer, tool rendering, session restore, and responsive behavior contained
   useful pieces without one integrated daily workflow.
5. Verification did not prove real PTY interaction, replay determinism,
   long-session budgets, or reference invocation stability.

## Delivered Architecture

### M0: Research and Contracts

- Audited Claude Code Ripe, Codex, Crush, and Eino-Agent separately.
- Defined one target architecture instead of copying a reference framework.
- Accepted contracts for runtime events, composer elements, busy queueing,
  editing, sessions, responsive layout, terminal capabilities, accessibility,
  performance, and four-project parity.
- Separated structural Claude fidelity from intentional post-parity UX changes.

Purpose: establish ownership and acceptance criteria before changing the central
query/TUI loop.

### M1: Canonical Runtime State and Live Progress

- Added stable runtime identity and parent lineage to events.
- Added a bounded, defensive `RuntimeStateStore` reducer and narrow selectors.
- Classified lossless versus coalescible event families.
- Derived bounded child activity, tool/token usage, and status while the Agent
  is still running.
- Unified Ctrl+T, Ctrl+B, `/team`, inline status, and background counts over one
  engine-owned selector.

Purpose: make every UI projection agree and make replay a first-class operation.

### M2: Agent Detail, Lineage, and Control

- Persisted launch identity, parent/tool lineage, model, permission/isolation,
  worktree, transcript, output, status, error, and generation before execution.
- Added Overview, Activity, Transcript, Output, and Lineage views.
- Merged bounded live and durable messages by stable identity.
- Added send, queued follow-up, retained/evicted resume, safe pause/resume, and
  canonical abort.
- Added compact parent Agent traces linked to the existing full detail view.

Purpose: turn an Agent row into an inspectable and controllable runtime object
without embedding unbounded child output in its parent.

### M3: Thread Switching and Cross-Thread Attention

- Added a bounded catalog with `live_attach`, `replay_only`, and
  `evicted_transcript` modes.
- Added independent per-thread chat, draft, cursor, history, rich elements,
  queue preview, scroll/follow, selection, search, and detail-tab state.
- Added exact `/agent`, a searchable stable-order picker, and configurable
  previous/next navigation.
- Added owner-thread permission/question/plan queues and inactive summaries.
- Prevented resolved/canceled requests from replaying and retained terminal
  threads that still own unresolved interaction.

Purpose: make asynchronous subagents usable without stealing focus or losing
leader/child work in progress.

### M4: Semantic History, Tools, and Stable Streaming

- Introduced semantic history identity, versions, finalization, raw/transcript/
  expanded projections, nested activity, selection, and animation capabilities.
- Added bounded renderers for Bash/background shells, Read/Grep/Glob,
  Edit/Write/diffs, Agent traces, MCP, plan/task, web, and generic/plugin tools.
- Added source-backed stable Markdown regions with mutable list/table/fence
  tails, width reflow, and canonical finalization.
- Formalized contiguous layout owners and one modal stack.

Purpose: preserve transcript truth while keeping tool traces scannable and
redraw work bounded to visible or active regions.

### M5: Structured Composer and Explicit Busy Semantics

- Integrated contextual keybindings and generated Help/status projections.
- Added bounded large-paste, image, file, skill, and MCP-resource elements with
  range rebasing and multimodal submission.
- Changed busy Enter from implicit interruption to a visible engine-owned queue;
  Ctrl+C remains explicit cancellation.
- Added queue edit/cancel, reverse history, external editing, and 100-entry
  per-thread rich undo.

Purpose: prevent accidental cancellation and hidden attachment loss across
long-running leader and Agent turns.

### M6: Sessions, Transcript Inspection, and Recovery

- Added stat-first opaque cursor pages, a project catalog, CWD/repository/all
  scopes, sort/filter, moving-page deduplication, and stable selection.
- Added explicit resume/fork mode, bounded recent preview, full searchable
  transcript, and richer lineage/model/worktree/status metadata.
- Persisted execution and safe presentation sidecars.
- Restored cross-CWD/worktree context and selected live versus replay-only Agent
  attachment without replaying stale callbacks or interaction payloads.
- Hardened transcript replacement with sync, atomic rename, and append reopen.

Purpose: keep large session catalogs responsive and recover work context rather
than only message text.

### M7: Responsive, Accessible, and Terminal-Safe Hardening

- Added deterministic compact, standard, and wide geometry plus a canonical
  Agent/task sidebar when width permits.
- Centralized terminal color, hyperlink, mouse, focus, paste, image protocol,
  enhanced-key compatibility, and suspend/resume facts.
- Added focus-aware notifications, platform degradation, idle-only suspend,
  Ctrl+D handling, and defensive panic cleanup.
- Added reduced motion, complete `NO_COLOR` finalization, raw history, textual
  status, and Unicode/CJK/emoji/combining/path width safety.
- Added exact 30 FPS stream batching, p95 budgets, product goldens, and real PTY
  workflows.
- Added Codex as a fourth parity project through an isolated logged-out startup
  boundary.

Purpose: make terminal behavior dependable across long transcripts,
multi-Agent workloads, and heterogeneous terminal capabilities.

## Difficulties and Resolutions

### Runtime Dependencies

The engine, tools, and Agent runner already used hooks to avoid package cycles.
The implementation preserved this boundary with additive event envelopes,
context-scoped identity/progress, and narrow selector/control APIs instead of
moving the query loop or adding a graph runtime.

### State Ownership Without a Rewrite

Legacy fields and task snapshots represented overlapping facts. Connecting a
second mirror AppState would worsen divergence. The accepted split is: the
engine reducer owns runtime facts, transcript/session files own recoverable
history, and per-thread TUI state owns presentation only.

### Asynchronous Modal Ownership

Inactive approvals must remain visible without opening a global dialog.
Owner-thread attention solved routing, while transition tests found a deeper
bug: canonical resolution under a covering Help modal removed only the stack
top. Resolution now removes the exact permission/question/plan layer and keeps
the covering modal and base state intact.

### Stable Streaming and Long Histories

Full Markdown rerender and transcript copying scale with session length.
Stable-prefix source regions, frozen completed-item caches, O(visible rows)
viewport assembly, bounded runtime rings, narrow selectors, and disk-backed
recent inspection make interaction latency independent of the full transcript.

### Terminal Lifecycle Proof

Pure model tests cannot prove shell restoration. Real PTY helpers exercise
bracketed paste, SIGWINCH resize, SGR mouse selection, Agent switching, inactive
approval, cancellation, EOF, panic, and cleanup. A post-cleanup ordinary-output
marker proves restoration occurred before control returned to the shell.

### Deterministic Codex Comparison

Authenticated Codex turns inherit credentials, account flags, network timing,
and model variance. The harness uses a fresh `CODEX_HOME`, removes inherited
auth/base-URL variables, disables startup update checks, uses inline mode, and
requires two independent normalized captures to match. Only logged-out startup
is claimed until a deterministic local transport/model fixture exists.

### Deterministic Concurrency Verification

The final full-suite rerun exposed a timing-based TeamRunner parity assertion:
two parallel members had to enter within 15 ms. Scheduler pressure could exceed
that window even when both goroutines were concurrent. The test now uses a
barrier that requires both executors to enter before either can finish, proving
the behavior directly without relying on wall-clock timing.

The broader race rerun also found ACP sessions concurrently rewriting legacy
package-level initialization hooks and the query test validator. ACP engine
construction now serializes only that compatibility initialization region, and
the validator is published through an atomic pointer. Session query execution
remains concurrent after construction.

## Verification Evidence

```text
make fmt                              PASS
make lint                             PASS
make test                             PASS (3,515 tests)
make -B build                         PASS (linux amd64, darwin amd64/arm64, windows amd64)
parity build tag                      PASS
isolated Codex two-run determinism    PASS
focused engine/session/TUI/ACP race   PASS
migration manifest                    PASS (1,884 / 1,871 / 834)
git diff --check                      PASS
TUI unchecked/stale-status audit      PASS
```

Representative Apple M5 Pro baselines:

| Path | Observed | Product budget |
|---|---:|---:|
| 10K-message visible render | about 4.81 us | `< 50 ms` |
| 20-Agent catalog/overlay | about 198.54 us | responsive interaction |
| 64-event stream batch/frame | about 203.05 us | `< 500 ms` |
| cached Agent switch/frame | about 110.74 us | `< 100 ms` p95 |
| 20-Agent narrow snapshot | about 8.34 us | bounded selector |
| 10K disk recent projection | about 220.63 us | `< 500 ms` p95 |

Machine-specific timings are regression context. Normal tests enforce the
portable p95 product budgets.

## Documentation Truth

- `STATUS.md` owns current verified counts and confidence.
- `PLAN.md` owns ordered remaining work and the next minor goal.
- `REMAINING.md` owns only unresolved depth gaps.
- This report and [`README.md`](README.md) retain M0-M7 acceptance evidence.
- [`m0-m7-refinement-plan.md`](m0-m7-refinement-plan.md) retains the closed executable checklist.
- Contract files own invariants and intentional boundaries, not progress.
- Reference reports describe source observations, not current status.

Current update rules live in
[`documentation-policy.md`](../../../contributing/documentation-policy.md).

## Commit Decomposition

The stage implementation is committed as four reviewable layers:

1. `feat(runtime): add canonical multi-agent thread state`
   - runtime identity/reducer/progress/control/queue/session/transcript behavior
     and focused backend tests.
2. `feat(tui): add modern multi-agent terminal workflows`
   - thread navigation, semantic history, structured composer, session UI,
     responsive/accessibility/terminal behavior.
3. `test(tui): add product hardening matrix`
   - product goldens, transition/PTY/performance support, and parity harness.
4. `docs(tui): close M0-M7 modernization track`
   - research, contracts, trackers, counts, this report, and Kimi workflow.

Final gate reruns produced two bounded follow-up commits:

5. `test(tools): make parallel parity assertion deterministic`
   - replaces a scheduler-sensitive 15 ms assertion with a synchronization
     barrier.
6. `fix(acp): serialize compatibility engine initialization`
   - makes query validator publication atomic and prevents concurrent ACP
     sessions from racing on legacy package-level engine wiring.

This keeps runtime behavior, visible product behavior, verification, and status
evidence independently reviewable without creating non-compiling intermediate
commits around the tightly coupled `App` component graph.

## Boundary At Closeout

When M0-M7 closed, the broader migration depth backlog still remained and
**P1 Tool-Pool Assembly and Filtering** was the next accepted slice:

1. inspect Claude tool-preset, mode, deny-rule, and ordering behavior;
2. extend the existing default preset without changing the flat Go tool package;
3. assemble one deterministic model-visible pool while preserving runtime
   permission defense in depth;
4. add focused assembly tests and cross-entrypoint coverage;
5. update manifest, STATUS, REMAINING, and tool/command docs;
6. pass all four Makefile gates before completion.

Deferred TUI boundaries remain explicit: Bubble Tea v1 enhanced-key reports are
detected but disabled, image protocols are diagnostic rather than rendered, and
screen-reader-specific protocols are not claimed.

P1 and the later P2-P12 stages subsequently completed. Their evidence is in
[`migration/history/runtime/p1-p8.md`](../runtime/p1-p8.md) and
[`migration/history/runtime/post-parity.md`](../runtime/post-parity.md). Any accepted work after
this closeout is owned only by [`migration/PLAN.md`](../../PLAN.md).
