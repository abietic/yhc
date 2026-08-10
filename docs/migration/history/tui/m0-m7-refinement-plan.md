# Eino-Agent Modern TUI Execution Plan

**Created:** 2026-07-10
**Completed:** 2026-07-11
**Status:** historical
**Closeout result:** M0-M7 verified at the recorded snapshot; 3,515 tests passed
**Owner surfaces:** `engine/`, `tools/`, `internal/tui/`, session/transcript
storage, and TUI tests

> **Ownership:** historical executable checklist; not an active plan

## Purpose

This was the actionable post-parity TUI plan and is retained as completion
evidence. New work belongs in `docs/migration/PLAN.md`. Structural migration
status remains owned by `docs/migration/manifest.yaml` and
[`migration/STATUS.md`](../../STATUS.md). This plan tracks a different
question: whether the current implementation provides a coherent modern coding-
agent experience, especially for asynchronous subagents.

The design decision and comparison are in:

- [`migration/reference/tui/modern-coding-agent-synthesis.md`](../../reference/tui/modern-coding-agent-synthesis.md)
- [`migration/reference/tui/claude-code-ripe.md`](../../reference/tui/claude-code-ripe.md)
- [`migration/reference/tui/codex.md`](../../reference/tui/codex.md)
- [`migration/reference/tui/crush.md`](../../reference/tui/crush.md)
- [`migration/reference/tui/eino-agent-2026-07-10.md`](../../reference/tui/eino-agent-2026-07-10.md)

The completed subsystem status record is indexed by
[`migration/history/tui/README.md`](README.md); current ownership is documented
by [`architecture/tui/README.md`](../../../architecture/tui/README.md).
The accepted runtime contract is
[`architecture/tui/contracts/runtime-events.md`](../../../architecture/tui/contracts/runtime-events.md).
The accepted structured composer contract is
[`architecture/tui/contracts/composer.md`](../../../architecture/tui/contracts/composer.md).
The accepted busy-submission contract is
[`architecture/tui/contracts/busy-queue.md`](../../../architecture/tui/contracts/busy-queue.md).
The accepted editing contract is
[`architecture/tui/contracts/editing.md`](../../../architecture/tui/contracts/editing.md).
The accepted session contract is
[`architecture/tui/contracts/sessions.md`](../../../architecture/tui/contracts/sessions.md).

## Historical Product Goal

The TUI must let a user:

1. see useful live progress for every active Agent;
2. understand parent, child, session, turn, and tool-call lineage;
3. inspect either a compact parent trace or the full child transcript;
4. switch among leader and Agent conversations without losing drafts or scroll
   state;
5. send, queue, resume, pause, or abort through one runtime control path;
6. notice and resolve approvals/questions from inactive Agent threads;
7. compose safely while other work runs;
8. resume and search long sessions without eagerly rendering everything;
9. retain responsive, correct behavior across terminals and viewport sizes.

## Constraints Frozen for This Program

- Keep the imperative `engine.QueryEngine` authoritative.
- Keep Bubble Tea, Bubbles, Lip Gloss, Glamour, and current chat caches.
- Extend event contracts additively during migration.
- TUI components consume engine snapshots; they do not become runtime truth.
- Keep `tools/` flat per repository convention.
- Use existing session/transcript/output storage before adding a database.
- Do not port Codex's full app-server or Claude's custom React terminal stack.
- Preserve event order and behavioral compatibility with
  `.reference/claude-code-ripe` when the post-parity UX does not require an
  intentional documented divergence.

## Baseline at Plan Start

### Integrated

- additive runtime event identity and per-thread sequence;
- engine-owned bounded reducer, replay, snapshots, and unresolved-request selector;
- child Agent identity/lineage allocation before executor entry and shared child event reduction;
- live bounded Agent progress derived from child events, synchronously reflected
  in `AgentRunner`, and reduced into the shared runtime snapshot;
- one canonical-first task/Agent selector consumed by Ctrl+T, Ctrl+B, `/team`,
  inline status, and the background count;
- leader chat, streaming, thinking, tools, interruption, compaction;
- O(viewport) chat rendering, item caches, frozen completed items;
- semantic `HistoryItem` identity/version/finalization, rich/raw/height
  contract, optional projections/capabilities, and legacy `ChatItem` adapters;
- permission, plan, MCP, and user-question dialogs;
- owner-scoped cross-thread permission/question attention with bounded inactive
  summaries and unresolved-only replay;
- searchable leader/Agent thread switching with per-thread draft, scroll,
  selection, search, and queue-preview state;
- command/shell modes, history, search, selection, model and MCP workflows;
- resume metadata picker, session branch/tag/export commands;
- task, background, and team summaries;
- themes, Vim, mouse, notifications, reduced motion.
- one bounded range-aware composer element model for large paste, local/
  clipboard image, file, skill, and MCP resource inputs;
- multimodal leader submission, asynchronous mention indexing/payload loading,
  rich in-session recall/rewrite, and text-only cross-session history.

### Foundation present but not product-integrated

- AgentRunner transcript/retention beyond the selected detail workflow;
- engine AppState task reducer;
- mirror TUI state/component packages.

### Baseline gaps closed by this plan

- busy Enter now queues without interrupting the active leader turn;
- ordinary prompts have external editing, reverse history, and rich undo;
- reducer, transition, product golden, PTY, performance, and four-project
  parity coverage protect Agent workflows.

## Historical Status Vocabulary

Each task uses one of these states in this document and the subsystem tracker:

- `planned`: accepted work with no implementation claim;
- `in_progress`: a bounded slice is currently being implemented;
- `implemented_unverified`: code exists but all acceptance/gates are not met;
- `done`: implementation, focused tests, docs, and required Makefile gates pass;
- `blocked`: a concrete dependency prevents progress.

Source presence alone is never `done`.

## Milestone Summary

| ID | Milestone | Priority | Status | Depends on |
|---|---|---:|---|---|
| M0 | Research, contracts, and baselines | P0 | Complete | None |
| M1 | Canonical state and observable Agents | P0 | Complete | M0 |
| M2 | Agent detail, lineage, and control | P0 | Complete | M1 |
| M3 | Thread switching and attention | P0 | Complete | M1, M2 read model |
| M4 | Semantic rendering and tool traces | P1 | Complete | M1 |
| M5 | Composer modernization | P1 | Complete | M3 thread view state |
| M6 | Session/transcript modernization | P1 | Complete | M3, M4 |
| M7 | Terminal hardening and product polish | P1 | Complete | M3-M6 |

## M0: Research, Contracts, and Baselines

### M0.1 Reference reports

- [x] Record reference commits and local-snapshot limitation.
- [x] Write separate Claude Code Ripe, Codex, Crush, and Eino-agent reports.
- [x] Produce the cross-reference decision and target architecture.
- [x] Separate integrated features from disconnected scaffolds.

### M0.2 Runtime contract specification

- [x] Define stable `SessionID`, `ThreadID`, `TurnID`, `AgentID`, sequence, and
  causal fields.
- [x] Define terminal states and allowed transitions.
- [x] Define typed runtime event families and bounded payload rules.
- [x] Define snapshot/replay and unresolved-request filtering semantics.
- [x] Define memory retention versus durable transcript responsibilities.
- [x] Record intentional divergences from Claude behavior.

### M0.3 Baseline tests and measurements

- [x] Golden current leader chat, permission, resume, background, and team views
  at 40/80/120/180 columns.
- [x] Record long-transcript render and stream batching benchmarks.
- [x] Freeze busy-Enter behavior explicitly; the accepted M5.3 test now proves
  queue-without-interrupt and documents the intentional semantic replacement.
- [x] Replace the initial state-source disagreement with convergence tests across
  Ctrl+T, Ctrl+B, `/team`, and inline Agent status.
- [x] Add configured-action reachability/validation tests and keep Help/status
  generated from reachable bindings.

M0.3 closes on the 40/80/120/180 baseline sections in
`product_states.golden`, the accepted performance baseline, busy queue tests,
canonical selector convergence tests, and resolver reachability tests. These
are stronger end-state regression guards than retaining tests for the obsolete
interrupting/disagreement behavior.

**M0 exit:** event/state ADR is accepted and current behavior is frozen well
enough to detect migration regressions.

## M1: Canonical State and Observable Agents

### M1.1 Runtime event envelope

Targets: `engine/events.go`, query/subagent event producers, focused tests.

- [x] Add thread, session, turn, Agent, sequence, timestamp, and causation fields
  without changing existing event ordering.
- [x] Assign leader thread identity at session initialization/resume.
- [x] Allocate child thread and Agent identity before Agent execution starts.
- [x] Attach parent thread and parent tool-use identity at spawn.
- [x] Guarantee lossless terminal and interactive events.
- [x] Document which high-frequency progress events may be coalesced.

### M1.2 Reducer-backed runtime snapshot

Targets: new bounded engine files near `app_state_tasks.go`; do not add a
parallel TUI-owned runtime store.

- [x] Reduce session/thread/turn/message/tool/Agent/interactive events.
- [x] Keep a bounded event ring and monotonic snapshot revision per thread.
- [x] Expose defensive point-in-time snapshots and focused selectors.
- [x] Preserve terminal thread identity after heavy live state is evicted.
- [x] Add deterministic replay tests and invalid-transition tests.

### M1.3 Real-time subagent event/progress pipeline

Targets: `engine/subagent.go`, `tools/agent_runner.go`, query queue drain.

- [x] Publish child QueryEngine events with child thread identity.
- [x] Derive bounded `ToolActivity`, tool count, token count, last tool, recent
  activity, and summary while the Agent is running.
- [x] Update AgentRunner from the same events; do not infer a second timeline.
- [x] Emit progress to the parent without copying unbounded tool output.
- [x] Test progress before terminal completion, failure, abort, and resume.

### M1.4 One task/Agent selector

Targets: `internal/tui` adapter, background/team/task views.

- [x] Build one read adapter over the engine runtime snapshot.
- [x] Migrate Ctrl+T, Ctrl+B, `/team`, and inline Agent status.
- [x] Preserve direct global snapshot access only behind a temporary fallback.
- [x] Remove the engine-backed global fallback after M2.1 launch metadata and convergence tests pass.
- [x] Ensure completed/failed/aborted rows converge without stale running state.

**M1 exit:** every running Agent shows the same live status and recent activity
in every TUI projection; reducer replay is deterministic.

## M2: Agent Detail, Lineage, and Control

### M2.1 Durable launch metadata

- [x] Persist Agent/thread identity, parent session/thread/tool-use identity,
  prompt, type/role, model, isolation, worktree, transcript, and output paths at
  launch.
- [x] Make metadata available before the first model response.
- [x] Test crash/reload-style reads for running and terminal metadata.

### M2.2 Unified Agent detail

- [x] Replace summary-only output with Overview, Activity, Transcript, Output,
  and Lineage views.
- [x] Render live messages from the runtime snapshot/transcript access path.
- [x] Bootstrap evicted transcripts from disk and merge by stable message ID.
- [x] Show pending input, unresolved attention, elapsed time, usage, and
  terminal reason.
- [x] Keep terminal Agents inspectable until explicit dismissal/retention expiry.

### M2.3 Runtime control actions

- [x] Send to running Agent using the existing pending-message path.
- [x] Resume terminal retained/evicted Agent using
  `SendOrResumeAgentMessage`.
- [x] Abort through AgentRunner and publish a terminal event.
- [x] Wire pause/resume at safe query/tool-round boundaries after send/resume is
  stable.
- [x] Show immediate optimistic user input without duplicating it after replay.

### M2.4 Parent nested trace

- [x] Add a dedicated Agent tool history item.
- [x] Show a bounded recent activity tail under the parent call.
- [x] Include attention and terminal status.
- [x] Link/open the full Agent transcript without embedding it in another card.

**M2 exit:** a user can inspect and control an Agent from start through retained
terminal state, with complete lineage.

## M3: Thread Switching and Attention

### M3.1 Per-thread live event stores

- [x] Maintain bounded buffers for active and inactive threads.
- [x] Store canonical turns plus live event tail and active turn ID.
- [x] Track unresolved approvals/questions independently from ordinary history.
- [x] Define live-attach, replay-only, and evicted-transcript modes.

### M3.2 Per-thread TUI view state

- [x] Capture draft, structured elements, queue preview, scroll/follow,
  selection, search, and detail tab by thread.
- [x] Restore the target state after switch.
- [x] Prevent queued input from auto-sending during replay.
- [x] Preserve current leader view as the default thread.

### M3.3 Navigation

- [x] Add searchable `/agent` picker using stable spawn order.
- [x] Add configurable previous/next Agent shortcuts, defaulting to
  `Alt+Left`/`Alt+Right` where supported.
- [x] Display active thread/Agent label in the header/footer.
- [x] Keep closed threads inspectable and in stable traversal order.
- [x] Fail over to leader when a visible child closes unexpectedly.

### M3.4 Cross-thread attention

- [x] Keep background approvals/questions pending on their owning thread.
- [x] Show a bounded inactive-thread attention summary.
- [x] Switch to the owning thread before rendering the full request.
- [x] Replay only unresolved requests.
- [x] Test resolved, canceled, evicted, and terminal-with-pending cases.

**M3 exit:** users can switch among live/closed conversations with no lost
drafts, messages, or interactive requests.

## M4: Semantic Rendering and Tool Traces

### M4.1 History item contract

- [x] Define identity, version, finished, rich/raw render, and height behavior.
- [x] Add optional compact, expanded, transcript, nested, selectable, and
  animatable capabilities.
- [x] Port existing ChatItems incrementally behind adapters.
- [x] Preserve item/version/width cache and frozen terminal output.

### M4.2 Dedicated tool renderers

- [x] Bash/background shell.
- [x] Read/Grep/Glob grouped operations.
- [x] Edit/Write/diff.
- [x] Agent/nested trace.
- [x] MCP: double-underscore first-class tools, single-underscore compatibility,
  legacy gateway identity, redacted bounded arguments, structured text/content
  blocks, protocol failures, large-response warning, full expanded/raw/
  transcript projections, narrow width, race, and all Makefile gates.
- [x] Plan/task/todo: plan enter/approval/team/rejection states, bounded plan
  preview and full evidence, task CRUD/list/dependency/retrieval semantics,
  local-Agent output isolation, Todo ratio/current/check-state, legacy `Task`
  Agent compatibility, narrow width, race, and all Makefile gates.
- [x] Web fetch/search: URL/query/filter identity, redirect and AI fallback,
  structured/current result protocols, source counts and safe hyperlinks,
  HTTP/network/empty states, remote control-sequence sanitization, bounded
  normal output, complete projections, narrow width, race, and all Makefile
  gates.
- [x] Generic fallback: all 41 defaults classified, 19 generic defaults pinned,
  unknown dynamic tools retained, textual lifecycle, credential-safe compact
  args, explicit head/tail and 240-column bounds, complete expanded/raw/
  transcript evidence, malformed/Unicode/narrow/cache/race coverage, and all
  Makefile gates.

### M4.3 Streaming regions

- [x] Golden current Markdown streaming output first. The fixture records 12
  append-only stages across headings, partial inline syntax, lists, tables,
  fences, and the final mutable tail.
- [x] Commit newline-complete stable regions and keep a mutable tail. Goldmark
  top-level blocks define a monotonic source boundary; only the final block is
  reparsed and rerendered as deltas arrive.
- [x] Hold active Markdown tables until shape is stable. Lists, tables,
  setext headings, HTML, and fences remain in the final mutable block until a
  following block proves their shape complete; global reference links keep a
  conservative single-region fallback.
- [x] Rerender from source on width changes. Width invalidation discards
  rendered fragments and reseeds regions from the original Markdown source.
- [x] Ensure finalization produces one canonical transcript item.
  `AssistantMessage.Finalize` clears stitched regions, forces a complete-source
  render, keeps authoritative snapshots and the delta builder consistent, and
  prevents a later stream from reusing the terminal item.

Verification: 12-stage output golden, block/table/fence/reference/resize/
replacement/finalization/item-identity tests, 3219-test full suite, four-target
build, TUI race, lint, manifest, and diff checks. The complex Markdown stream
benchmark improved from about 1.169 s/op and 818 MB/op to 156 ms/op and
120 MB/op on the same machine; the short mixed stream remains about 6 ms/op.

### M4.4 Layout/dialog boundary

- [x] Introduce explicit rectangles for header, chat, attention/tasks, editor,
  optional sidebar, status, and overlay. `calculateLayout` now partitions the
  complete viewport into contiguous `layoutRect` owners, budgets the actual task
  tree and hint heights, and clips every band to its assigned rectangle.
- [x] Route input through one formal dialog stack. All 15 modal states now share
  one top-first keyboard/focus boundary, back-to-front overlay order, and
  explicit return-state frames; covered async dialogs can resolve without
  losing the underlying search/thread mode.
- [x] Keep string rendering initially; adopt a screen buffer only if profiling
  proves ANSI decode/composition dominates. The retained string compositor is
  width-safe and exact-height; its focused benchmark is about 4.2 us/op, while a
  100-turn `App.View` is about 127-130 us/op on the verification machine, so a
  cell buffer is not currently justified.

Verification: base and modal goldens, contiguous-region/minimum-viewport tests,
nested/covered/async dialog lifecycle and input-isolation tests, 3229-test full
suite, four-target build, TUI race, lint, manifest, diff checks, and three-run
layout/view benchmarks.

**M4 exit:** long multi-Agent traces remain readable, selectable, and bounded
without generic-tool output dominating the chat.

## M5: Composer Modernization

### M5.1 Integrate configurable keybindings

- [x] Route active key handling through `keybindings.Resolver` by context.
  Global, Chat, Autocomplete, Scroll, Help, Transcript, Task, and
  MessageSelector now resolve through one deterministic dispatcher with
  specific-context precedence and global fallback.
- [x] Preserve terminal-enhancement fallbacks and Vim precedence. Ctrl+J plus
  distinguishable Shift+Enter remain newline paths, Windows retains Meta+M mode
  cycling, Agent Alt navigation remains configurable, and Vim editing keys run
  before ordinary character actions while non-rebindable Ctrl+C stays first.
- [x] Remove or implement every advertised action. All 40 default action rows
  have real product handlers; disconnected image/external-editor/history-search/
  stash/undo actions remain schema-only until their later milestones. Help,
  status, task hints, and `/keybindings` project the active resolver; renderer-
  local details use key-neutral expand text.
- [x] Add conflict/reserved-shortcut validation tests. Strict context/action/key
  parsing, normalized alias conflicts, reserved terminal/product shortcuts,
  null unbinding, defensive merge, invalid-config rollback, and chord lifecycle
  are covered.

Verification: contextual App-path tests, Help/status projection, Vim precedence,
chord isolation, config load/rollback and validation tests, 3249-test full suite,
four-target build, TUI/keybinding race, lint, manifest, and diff checks.

### M5.2 Structured draft elements

- [x] Integrate large pasted text as placeholders with full retained content.
  Bracketed paste bypasses shortcut dispatch, uses rune-count thresholds, keeps
  up to 5 MiB per payload/10 MiB per draft, and expands only valid ranges for
  engine submission.
- [x] Integrate clipboard/local images with stable rows/placeholders. `Ctrl+V`
  reads the clipboard asynchronously; pasted local image paths resolve off the
  Update path; valid placeholders produce Eino `UserInputMultiContent` image
  parts with known-model capability checks and leader-thread targeting.
- [x] Integrate file, skill, and MCP resource mentions. One `@` popup combines
  a bounded async file index, engine skill registry, and timeout-bounded MCP
  list/read providers; file/MCP payloads rejoin by stable element ID.
- [x] Rebase/prune element ranges on edit and submit. A single-edit rune diff
  shifts disjoint elements and drops every intersected or stale placeholder;
  late async results cannot resurrect an edited element.
- [x] Persist rich local history and text-only cross-session history. Recent
  in-memory history, per-thread drafts, and rewrite-selected user rows retain
  payloads; the existing project/session history writes expanded paste text and
  canonical file/image/skill/MCP labels only.

Verification: 3268-test full suite, four-target build, lint, focused TUI/
keybinding/attachment/engine race, manifest, and diff checks. Tests cover
Unicode paste thresholds, range rebase/prune, thread restore, bounded rich
history, local image/fallback/multimodal engine delivery, async file/MCP loading,
skill registry mentions, stale result suppression, and heavy-directory skips.

### M5.3 Safe busy submission and queue

- [x] Change running-thread Enter from implicit interrupt to visible queue/steer.
- [x] Keep Ctrl+C as explicit cancellation.
- [x] Show/edit/remove queued input.
- [x] Preserve queue and draft per thread.
- [x] Evaluate interrupt-and-replace and intentionally defer it: Ctrl+C plus
  ordinary submit/edit is explicit and no second destructive gesture is needed.

Implementation: each `QueryEngine` owns one persistent queue manager. Busy
leader Enter enqueues an immutable rich input; a completed tool round can drain
it into the current query, while a no-tool terminal schedules an atomic claim as
the next turn. Running child input continues through the engine-scoped
`AgentRunner`, with pending UUID cancellation for edit/remove. Queue previews
remain per-thread and promote exactly once on lifecycle start.

Cancellation no longer detaches the TUI from the old event stream. Ctrl+C sends
the cancel request, displays one interruption marker, and keeps consuming until
terminal; pending input is then eligible to start. Max-turn/interruption events
are informational rather than pseudo-terminals. The queue is bounded to 32 user
inputs per route and the TUI exposes `/queue list|edit|remove` plus active status
hints.

Verification: 3287-test full suite, four-target build, lint, focused TUI/engine/
queue/Agent race, manifest, and diff checks. Scenarios cover same-turn tool
steer, terminal fallback, stale-stream rejection, Ctrl+C handoff, unexpected
stream close, FIFO/priority snapshots, capacity, image retention, exact preview
promotion, edit/remove, Agent pending cancellation, and thread-local draft/queue
restore.

### M5.4 Editing ergonomics

- [x] Wire ordinary prompt external editor while runtime work continues.
- [x] Add `Ctrl+R` reverse incremental history search.
- [x] Add bounded per-thread rich undo and document the Bubbles deletion/no-kill-
  ring boundary.
- [x] Keep shell mode and command/file popup semantics consistent.

Implementation: `Ctrl+G` writes an expanded ordinary prompt to a secure
temporary Markdown file and launches `$EDITOR` through Charmbracelet's editor
helper plus `tea.ExecProcess`. Runtime goroutines continue under bounded event
backpressure; the callback validates target `ThreadID`, trims trailing whitespace,
conservatively reconciles structured ranges, and can be undone.

`Ctrl+R` now owns a dedicated HistorySearch context and hint-row state. It
searches newest-to-oldest, repeats toward older matches, previews rich recent
history, accepts with Enter, and restores the exact original draft on Esc,
Ctrl+C, no-match, or thread switch. Prior-message rewriting remains available as
`/rewrite`/`/retry` instead of conflicting with the conventional key.

Composer undo stores text, cloned elements, and rune cursor at a maximum of 100
entries per thread and clears after submit. Existing Bubbles word-delete keys
are undoable; no kill ring is claimed because Ctrl+K/Ctrl+U retain command/
scroll roles. Verification passed 3298 tests, four builds, lint, focused race,
manifest, and diff checks.

**M5 exit:** composing is non-destructive, target-aware, and reliable while
multiple threads run.

## M6: Session and Transcript Modernization

### M6.1 Session data API

- [x] Add paginated/lazy session listing or an equivalent bounded iterator.
- [x] Support CWD/repository/all-session scopes, sort, and filters.
- [x] Deduplicate moving pages and preserve stable selection.

M6.1 adds a stat-first opaque-cursor API with a 25-row default, 100-row hard
page limit, and 512-read default scan cap. A minimal atomically updated root
catalog discovers exact CWD, shared Git common-dir repository/worktree, and all
previously registered project transcript stores without a home-directory walk.
Filters now run during bounded head/tail enrichment; model/provider filtering
is no longer a no-op.

The TUI rejects stale query generations, merges moving pages by stable source
path, preserves selection by key, debounces search, cycles scope/sort, and loads
near the final three rows. Focused race tests plus 3308 tests, lint, and four
build targets pass.

### M6.2 Picker experience

- [x] Support resume and fork modes.
- [x] Load recent transcript preview lazily.
- [x] Add full transcript overlay/search.
- [x] Show branch, parent, Agent lineage, status, model, and worktree metadata.

M6.2 adds explicit resume/fork modes, selected-history fork-at-end semantics,
and source-aware resume within the same CWD. The picker
lazily reads the latest four message entries from at most 512 KiB of transcript
tail; Ctrl+T loads the full transcript only on demand into the existing
searchable raw/expanded view and returns to the same picker state on exit.

Lite metadata now carries provider/model, branch/parent, thread/Agent lineage,
status, permission mode, and worktree fields when persisted. Full action,
preview, transcript, corruption, fork immutability, overlay/search/return, and
cross-CWD rejection tests passed under focused race and the 3319-test full
gates before M6.3 replaced that temporary restriction.

### M6.3 Restore contract

- [x] Restore messages plus model, permission mode, worktree, Agent metadata,
  and safe view state.
- [x] Restore unresolved requests only when runtime state says they remain
  actionable.
- [x] Reattach live Agent threads when possible; otherwise enter replay-only
  inspection mode.

M6.3 persists a full execution checkpoint after every transcript rewrite and
model/permission change, restores cross-CWD/worktree context with explicit
fallback warnings, and keeps unresolved request IDs reference-only. Agent
threads still owned by the current runner reattach live; durable-only or
process-interrupted Agents become non-actionable replay views. A separate
versioned `0600` sidecar restores only bounded plain drafts, cursor/scroll,
follow, input mode, detail tab, and catalog-compatible thread identity. Queue,
structured/image payloads, undo, selection, dialogs, and request inputs never
cross the restart boundary. ACP prompt close/join and context-scoped hook status
complete the multi-session lifecycle boundary.

**M6 exit:** large session histories become quickly searchable and Agent work
remains inspectable after restart.

## M7: Terminal Hardening and Product Polish

### M7.1 Responsive modes

- [x] Define compact behavior at narrow width/height.
- [x] Add optional wide Agent/task sidebar without nesting cards.
- [x] Test 40/80/120/180 columns and 20/30/50 rows.
- [x] Keep every command reachable when a panel cannot fit.

M7.1 defines deterministic compact/standard/wide modes. Compact hides the
multi-row hint band and caps activity while preserving chat/editor/status and
all handlers. Wide activates only at 150x24 with canonical Agent/task rows,
keeps at least 100 main columns, and renders a 32-42 column unframed sidebar
from the shared engine selector. Main/sidebar lines compose horizontally while
full-screen overlays retain ownership. The 12-size matrix, compact command
reachability, bounds, golden, and focused race tests pass.

### M7.2 Terminal capabilities

- [x] Probe enhanced keys, focus, color, hyperlinks, image support, and mouse.
- [x] Suppress desktop notifications while focused.
- [x] Verify SSH, macOS, Linux, and Windows degradation paths.
- [x] Verify panic, suspend/resume, EOF, and terminal restoration.

M7.2 centralizes startup facts in `terminalcap.Capabilities` and runtime focus
in one atomic `FocusState`. Themes, hyperlinks, mouse startup, focus reporting,
`/terminal`, and external notification policy consume those facts. Unknown
focus fails closed; only an observed blur permits BEL/OS delivery while TUI
toasts remain visible. `/suspend` is idle-only and explicitly degrades on
Windows/noninteractive terminals. Bubble Tea owns normal mode restoration; a
defensive cleanup resets every owned protocol after a panic. Unit/race tests
cover the platform matrix and real Unix PTY Ctrl+D/panic restoration.

Enhanced-key and image-protocol support are deliberately reported rather than
overclaimed: Bubble Tea v1 compatibility mode does not enable Kitty CSI-u, and
M7.2 does not render terminal images. See
[`architecture/tui/contracts/terminal-lifecycle.md`](../../../architecture/tui/contracts/terminal-lifecycle.md).

### M7.3 Accessibility

- [x] Verify reduced motion and no-color output.
- [x] Add raw/copy-friendly history projection.
- [x] Ensure status is not color-only.
- [x] Verify Unicode, CJK, emoji, combining marks, and long unbroken paths.

M7.3 gives reduced motion real config/environment entrypoints, freezes
decorative cursor/spinner/stream/thinking/mascot/scroll motion while retaining
500ms runtime polling and deadlines, and strips every SGR/OSC escape at the
final frame when `NO_COLOR`/`TERM=dumb` selects `ColorNone`. Ctrl+O conversation
history now toggles with `r` between complete expanded and ANSI-free raw
projection. Status/tool/task/projection rows retain explicit text, Unicode CWD
truncation is display-width-safe, and 40x20/80x30/120x50 CJK/emoji/combining/
long-path frames remain valid UTF-8 and width bounded. See
[`architecture/tui/contracts/accessibility.md`](../../../architecture/tui/contracts/accessibility.md).

### M7.4 Test and performance matrix

- [x] Reducer/replay property tests.
- [x] Bubble Tea thread switch and modal transition tests.
- [x] Goldens for tool, Agent, permission, composer, and responsive states.
- [x] PTY scenarios for paste, resize, mouse, switch, inactive approval,
  cancel, and restore.
- [x] Benchmarks for 10K messages, 20 Agent threads, stream batching, and
  thread switch.
- [x] Add Codex to the parity harness where invocation can be made deterministic.

The reducer gate now runs 128 reproducible randomized event sequences spanning
turn/message, tool, permission, Agent progress/lifecycle, local task, terminal,
and bounded-eviction behavior. Each seed compares incremental `Apply` with a
fresh `Replay`, verifies limits and resolved-request filtering, and proves that
a rejected sequence gap cannot mutate the accepted snapshot.

Bubble Tea transition coverage now drives inactive-owner approval, owner-thread
switch, modal key isolation, per-thread draft restoration, and asynchronous
resolution through `App.Update`. Permission, question, and plan requests are
also resolved while covered by Help; resolution removes the exact owned layer
instead of leaving a stale covered dialog in the stack.

`product_states.golden` fixes representative Read/Bash/generic/Agent semantic
tool rows, an Agent transcript, permission overlay, rich large-paste plus queued
composer state, and complete 40/80/120/180 compact/standard/wide frames. Its
fixtures use fixed time/status facts and ANSI-free normalization, while wide
mode is driven by a real engine AppState task projection.

The Unix PTY workflow now executes one real Bubble Tea program through bracketed
multiline paste, SIGWINCH resize, SGR drag selection, `/agent` thread switching,
an approval owned by the previously inactive child, approval resolution,
Ctrl+C cancellation, and Ctrl+D exit. It asserts alt-screen, paste, mouse, and
cursor cleanup sequences and requires a post-cleanup ordinary-output marker,
proving terminal restoration rather than only child-process termination.

The accepted performance baseline now covers 10K visible-row rendering,
20-Agent catalog/render and narrow/full runtime snapshots, 64-event stream
batches, cached thread switching, ordinary key-to-frame, and a 10K-message
disk-backed recent transcript. Normal tests enforce the synthesis p95 budgets
and the batching window is exactly `time.Second / 30`; machine-specific
nanosecond/allocation data lives in
[`migration/verification/tui-performance-baseline.md`](../../verification/tui-performance-baseline.md).

Codex is now the fourth parity project. Its CLI runs with a fresh `CODEX_HOME`,
inherited auth/base-URL variables removed, startup update checks disabled, and
inline terminal mode. Two isolated runs must produce the same normalized
logged-out onboarding frame. The harness intentionally limits Codex to
`welcome_screen` until an offline deterministic app-server/model fixture exists;
see [`migration/verification/tui-parity-harness.md`](../../verification/tui-parity-harness.md).

**M7 exit:** all product metrics in the synthesis document are met and terminal
failures restore the user's shell cleanly.

## Recommended Release Cuts

### Release A: Observable Agents

M0 contract plus M1 and read-only M2 detail.

User value: real live progress, one consistent Agent list, and transcript
inspection.

### Release B: Controllable Agents

M2 controls and lineage plus parent nested trace.

User value: send/resume/pause/abort and clear parent-child causality.

### Release C: Navigable Agent Threads

M3.

User value: switch, preserve drafts, and resolve inactive-thread attention.

### Release D: Modern Daily Workflow

M4-M6 core slices.

User value: semantic traces, safe queueing, rich paste/images, and scalable
resume/fork.

### Release E: Hardened Product

M7 complete: responsive/accessibility/terminal lifecycle plus broad
property/transition/golden/PTY/performance/four-project parity gates.

## Historical Per-Slice Engineering Loop

Every checked task should follow the repository migration/iteration rules:

1. select one bounded task ID and state its acceptance criteria;
2. inspect the exact Claude source plus relevant Codex/Crush adaptation;
3. verify current Go source and tests rather than trusting docs;
4. implement the smallest engine contract before its TUI projection;
5. add focused tests and at least one user-observable scenario/golden;
6. update this plan, subsystem 15, `PLAN.md`, `REMAINING.md`, and `STATUS.md` as
   appropriate;
7. update `manifest.yaml` only when Claude reference classification/evidence
   changes, not merely because a post-parity UX task exists;
8. run `make fmt`, `make lint`, `make test`, and `make build` for code changes;
9. review the diff for overclaims and unrelated work before committing.

## Historical Definition of Done

A milestone is done only when:

- runtime truth has one owner;
- event ordering, cancellation, replay, resume, and terminal states are tested;
- TUI behavior is reachable through real key/command paths;
- narrow and wide rendering is covered;
- high-frequency work is bounded;
- no help/keybinding advertises an unreachable action;
- focused and full Makefile gates pass;
- docs and status trackers describe the verified implementation, not the plan.

## Follow-Up Boundary

The modern TUI M0-M7 checklist has no remaining item. The broader migration
plan's next owner at this historical closeout was **P1 tool-pool assembly and
filtering**. P1 later completed; current execution order belongs only in
[`migration/PLAN.md`](../../PLAN.md).
