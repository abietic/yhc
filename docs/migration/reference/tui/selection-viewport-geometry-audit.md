# TUI Selection Viewport Geometry Audit

**Status:** reference-snapshot
**Snapshot:** 2026-07-26

> **Ownership:** source-backed evidence for the sticky-header selection,
> highlight, and clipboard mismatch; relevant local-reference comparison; and
> the G30 adoption recommendation. Accepted execution order belongs in
> [`migration/PLAN.md`](../../PLAN.md).

## Result

The reported copy error was a deterministic one-row coordinate drift at
snapshot `52e7627`, not a clipboard encoding defect. When history was scrolled
away from the bottom,
[`ChatView.Render`](https://github.com/abietic/eino-agent/blob/52e7627672bf1961ab28d9fe1393196b570bb5e3/internal/tui/chat.go#L1410)
reserved one row for the sticky prompt and prepended that row after rendering
transcript content.
The forward and inverse selection transforms still calculate against the full
chat height without representing that final-frame row.

For the reported frame, the final rows are effectively:

| Final chat row | Visible role | Current selection interpretation |
|---:|---|---|
| 0 | sticky prompt | first transcript line |
| 1 | `资源: The Rust Programming Language (Rust Book)` | following transcript line, `适合人群：零基础入门` |
| 2 | `适合人群：零基础入门` | next transcript line |

The drag therefore stores the item point for row 2 while the inverse transform
paints row 1. [`RenderItemRange`](https://github.com/abietic/eino-agent/blob/52e7627672bf1961ab28d9fe1393196b570bb5e3/internal/tui/chat.go#L337) later
extracts the stored item point, so the visual highlight remains on `资源…` while
the clipboard receives `适合人群…`.

The accepted P27 contract is
[`p27-tui-selection-viewport-geometry.md`](../../plans/p27-tui-selection-viewport-geometry.md).
At this snapshot boundary it was queued; current execution state belongs only
in root [`PLAN.md`](../../PLAN.md). The target contract does not describe
current behavior.

## Snapshot Boundary

| Source | Snapshot | Question answered |
|---|---|---|
| Eino-Agent | `2fbbd05251ef` | Current render, mouse, selection, highlight, extraction, and clipboard ownership |
| Claude Code Ripe | `4b9d30f79532` | Fixed sticky chrome, actual-layout viewport origin, screen-buffer selection, and soft-wrap extraction |
| Crush | `2af939d8e900` | Bubble Tea layout-origin conversion, item-local selection, rendered-buffer highlight/extraction, and clipboard command ownership |
| Codex | `66bd101fff6f` | Terminal-native selection boundary when application mouse events are not consumed |
| OpenCode | `411eff73f026` | Scroll-content/footer separation, selection-aware click suppression, and asynchronous clipboard feedback |
| Pi | `c55ae2faa5d8` | Grapheme-aware ANSI-safe display-width and clipping primitives |
| Grok Build | `a5727c596045` | Content-rectangle drag auto-scroll, resolved selection rows, and multi-click policy |

These are local evidence snapshots. This report makes no claim about floating
upstream behavior.

## Observable Question

When sticky header, bottom-gravity padding, item gaps, or the jump pill changes
the final chat frame, how can Eino-Agent guarantee that:

1. the cell under the pointer;
2. the cell painted as selected; and
3. the text sent to the clipboard

all identify the same transcript source?

Success also requires chrome to remain outside transcript selection, Shift-drag
to keep the terminal-native escape path, Unicode clusters to remain indivisible,
and rendering to stay proportional to the viewport rather than full history.

## Snapshot Source Evidence

### The final frame has an unmodelled sticky row

[`ChatView.Render`](https://github.com/abietic/eino-agent/blob/52e7627672bf1961ab28d9fe1393196b570bb5e3/internal/tui/chat.go#L1435) built a fixed
`stickyLine` while away from follow mode and decrements the transcript budget at
[`height--`](https://github.com/abietic/eino-agent/blob/52e7627672bf1961ab28d9fe1393196b570bb5e3/internal/tui/chat.go#L1455). It then rendered and
bottom-pads transcript rows using the reduced height before prepending the
sticky row at [`lines = append(...)`](https://github.com/abietic/eino-agent/blob/52e7627672bf1961ab28d9fe1393196b570bb5e3/internal/tui/chat.go#L1511).
The jump pill may subsequently replace a row in that final slice.

This render path knows the final row roles, but it publishes only the rendered
string. No shared row projection survives for interaction code.

### Hit testing and highlight use a different geometry

[`viewportPosToItemPoint`](https://github.com/abietic/eino-agent/blob/52e7627672bf1961ab28d9fe1393196b570bb5e3/internal/tui/chat.go#L212) and
[`visibleContentHeight`](https://github.com/abietic/eino-agent/blob/52e7627672bf1961ab28d9fe1393196b570bb5e3/internal/tui/chat.go#L302) used
`c.height`. They account for bottom-gravity padding, partial first items, and
inter-item gaps, but not the prepended sticky row or a pill-overwritten row.

[`ItemPointToViewportRow`](https://github.com/abietic/eino-agent/blob/52e7627672bf1961ab28d9fe1393196b570bb5e3/internal/tui/chat.go#L273) independently
reconstructs the inverse mapping and likewise adds padding against `c.height`.
[`Selection.GetViewportHighlightRange`](https://github.com/abietic/eino-agent/blob/52e7627672bf1961ab28d9fe1393196b570bb5e3/internal/tui/selection.go#L222)
uses that inverse, and
[`App.applyViewportHighlight`](https://github.com/abietic/eino-agent/blob/52e7627672bf1961ab28d9fe1393196b570bb5e3/internal/tui/app.go#L2456) paints the
returned row in the already-final rendered string.

The forward and inverse transforms agree with each other but disagree with the
frame that the user sees. That explains why the wrong logical item can be
painted on the expected visible row.

### App removes only the outer layout origin

[`App.Update`](https://github.com/abietic/eino-agent/blob/52e7627672bf1961ab28d9fe1393196b570bb5e3/internal/tui/app.go#L769) correctly converts the
screen row to chat-local coordinates by subtracting `layout.chatRect.Y` at
[`chatY`](https://github.com/abietic/eino-agent/blob/52e7627672bf1961ab28d9fe1393196b570bb5e3/internal/tui/app.go#L900). The remaining drift is inside
`ChatView`, after chat-local row zero has become sticky chrome.

The same unmodelled bounds affect drag edge scrolling at
[`App.Update`](https://github.com/abietic/eino-agent/blob/52e7627672bf1961ab28d9fe1393196b570bb5e3/internal/tui/app.go#L910) and the Agent trace release
target at [`App.Update`](https://github.com/abietic/eino-agent/blob/52e7627672bf1961ab28d9fe1393196b570bb5e3/internal/tui/app.go#L924). Fixing only the
selection start would leave adjacent pointer behavior inconsistent.

### Extraction follows the stored item point

[`Selection.ExtractTextFromChat`](https://github.com/abietic/eino-agent/blob/52e7627672bf1961ab28d9fe1393196b570bb5e3/internal/tui/selection.go#L213)
passes normalized item/line/cell endpoints to `RenderItemRange`. That is why
the copied row is internally consistent with the wrong hit-test result.
Clipboard delivery happens only after extraction and cannot repair the
coordinate mismatch.

### Clipboard feedback has four synchronous callers

[`CopyToClipboard`](https://github.com/abietic/eino-agent/blob/52e7627672bf1961ab28d9fe1393196b570bb5e3/internal/tui/selection.go#L426) is called by
expand selection and chat selection in
[`App.Update`](https://github.com/abietic/eino-agent/blob/52e7627672bf1961ab28d9fe1393196b570bb5e3/internal/tui/app.go#L769), keyboard selection copy in
[`key_actions.go`](https://github.com/abietic/eino-agent/blob/52e7627672bf1961ab28d9fe1393196b570bb5e3/internal/tui/key_actions.go#L177), and command
[`ActionCopy` projection](https://github.com/abietic/eino-agent/blob/52e7627672bf1961ab28d9fe1393196b570bb5e3/internal/tui/app.go#L3922).
All four paths project success immediately after a helper that returns no
result.

The production composition root creates one serialized
[`TerminalOutput`](https://github.com/abietic/eino-agent/blob/52e7627672bf1961ab28d9fe1393196b570bb5e3/internal/tui/terminal_output.go#L110) and gives it
to Bubble Tea at
[`root.go`](https://github.com/abietic/eino-agent/blob/52e7627672bf1961ab28d9fe1393196b570bb5e3/cmd/eino-agent/cmd/root.go#L247). Selection does not
receive that writer and sends OSC 52 directly to `os.Stdout`, outside the
renderer serialization and shutdown contract.

## Strengths To Preserve

- [`Selection.HandleMouseForChat`](https://github.com/abietic/eino-agent/blob/52e7627672bf1961ab28d9fe1393196b570bb5e3/internal/tui/selection.go#L86)
  returns without consuming Shift-modified mouse input, preserving the
  terminal-native escape to the extent supported by the host terminal.
- [`selectionSliceCells`](https://github.com/abietic/eino-agent/blob/52e7627672bf1961ab28d9fe1393196b570bb5e3/internal/tui/selection_geometry.go#L53)
  and [`selectionHighlightCells`](https://github.com/abietic/eino-agent/blob/52e7627672bf1961ab28d9fe1393196b570bb5e3/internal/tui/selection_geometry.go#L95)
  share the App-selected `DisplayCellProfile`, strip ANSI for copied text, and
  do not split a grapheme cluster at an interior cell boundary.
- Item-local endpoints survive ordinary viewport scrolling better than raw
  screen rows.
- `ChatView.Render` and its item cache remain `O(viewport)` rather than walking
  all transcript history for every frame.

Existing tests protect those pieces independently:

- [`sticky_header_geometry_test.go`](https://github.com/abietic/eino-agent/blob/52e7627672bf1961ab28d9fe1393196b570bb5e3/internal/tui/sticky_header_geometry_test.go#L12)
  verifies sticky content, truncation, and pill placement;
- [`selection_test.go`](https://github.com/abietic/eino-agent/blob/52e7627672bf1961ab28d9fe1393196b570bb5e3/internal/tui/selection_test.go#L75)
  verifies forward and inverse mapping without a sticky header; and
- [`TestG11E4SelectionUsesCellBoundariesWithoutSplittingClusters`](https://github.com/abietic/eino-agent/blob/52e7627672bf1961ab28d9fe1393196b570bb5e3/internal/tui/display_cell_g11e4_test.go#L314)
  verifies grapheme-safe cell slicing.

No current test crosses sticky-header rendering with selection hit testing,
highlighting, and extraction in one fixture.

## Additional Verified Selection Gaps

These are not the root cause of the screenshot, but they are verified parts of
G30's accepted final-frame-to-clipboard outcome. Fixing geometry without
freezing them would leave selection correctness ambiguous:

| Gap | Current behavior | Required decision |
|---|---|---|
| Soft wrap | `RenderItemRange` inserts `\n` between every rendered row. | Preserve hard source newlines, but rejoin visual soft-wrap continuations according to renderer metadata. |
| Trailing whitespace | `selectionSliceCells` trims selected spaces and tabs. | Distinguish source whitespace from layout padding and define exact partial-row behavior. |
| Reflow identity | Endpoints use rendered line and cell coordinates without a compatible-render identity. | Scroll-only reprojection may retain endpoints; resize, profile, collapse, replacement, or reflow must reproject exactly or fail closed. |
| Business clicks | A release can still target an Agent trace after selection routing. | A non-empty application selection suppresses the release action beneath it. |
| Clipboard result | `CopyToClipboard` writes OSC 52 directly, runs a native helper synchronously, ignores errors, and all four callers show success. | Inject the composition root's existing `TerminalOutput`, use a typed asynchronous result at every caller, and report only observed delivery attempts truthfully. |

Terminal-specific OSC 52 acceptance, tmux passthrough configuration, remote SSH
clipboard behavior, and native helper availability are unresolved environment
facts. They require explicit fake-transport and PTY evidence in P27.3; they are
not inferred from the coordinate reproduction.

## P27.2 Promotion Refresh

P27.2 intake refreshed the extraction question on Eino-Agent master
`276df8cb8a6c7d50e0db25e951a56d0a7376e615`. P27.1 had already replaced the
row-offset reconstruction, so this refresh isolates only copy-source and stale
content identity.

Two disposable focused probes used the production `UserMessage` renderer:

1. selecting the whole 14-column rendering of
   `alpha  beta   gamma  \nhard  \nend` returned
   ` ▎ alpha\n beta\n ▎ gamma\n ▎ hard\n ▎ end`; and
2. selecting the single 40-column row for `alpha beta gamma delta`, then
   resizing to 14 columns, left `Selection.HasSelection()` true but changed the
   extracted bytes from ` ▎ alpha beta gamma delta` to ` ▎ alpha`.

The behavior follows directly from current source:

- [`renderItem`](https://github.com/abietic/eino-agent/blob/276df8cb8a6c7d50e0db25e951a56d0a7376e615/internal/tui/chat.go#L1364)
  caches final styled rows without selectable-text or boundary metadata;
- [`UserMessage.RenderWithEnvironment`](https://github.com/abietic/eino-agent/blob/276df8cb8a6c7d50e0db25e951a56d0a7376e615/internal/tui/chat.go#L1644)
  performs width-dependent wrapping and presentation styling;
- [`RenderItemRange`](https://github.com/abietic/eino-agent/blob/276df8cb8a6c7d50e0db25e951a56d0a7376e615/internal/tui/chat.go#L345)
  inserts one newline for every final row and relies on a numeric
  `NoSelectPrefix`; and
- [`selectionSliceCells`](https://github.com/abietic/eino-agent/blob/276df8cb8a6c7d50e0db25e951a56d0a7376e615/internal/tui/selection_geometry.go#L53)
  unconditionally trims selected spaces and tabs, while
  [`Selection`](https://github.com/abietic/eino-agent/blob/276df8cb8a6c7d50e0db25e951a56d0a7376e615/internal/tui/selection.go#L31)
  retains line/cell endpoints without content identity.

The relevant local-reference refresh found:

| Reference evidence | Verified extraction fact | Adoption consequence |
|---|---|---|
| Claude Code Ripe `src/ink/selection.ts`, `selectionBounds`, `extractRowText`, and `getSelectedText`; `src/ink/screen.ts`, `softWrap` and `noSelect` | The renderer marks soft continuations and non-selectable cells; forward/reverse ranges normalize identically; copy and overlay consume the same screen. Logical-line endings are trimmed, and scroll capture retains rows leaving the viewport. | Adapt renderer-owned wrap/non-select metadata and same-fact highlight/copy. Reject the full screen buffer and its trailing-whitespace policy. |
| Crush `internal/ui/model/chat.go`, `getHighlightRange`, `applyHighlightRange`, and `HighlightContent`; `internal/ui/list/highlight.go`, `HighlightBuffer` | Item-local endpoints normalize reverse and cross-item drags, but extraction applies `NormalizeSpace` and `TrimSpace`. | Preserve item-local ordering and action suppression; reject its copy bytes as source-fidelity evidence. |
| OpenCode `packages/tui/src/routes/session/index.tsx` user/tool `onMouseUp`; `packages/tui/src/clipboard.ts` | A non-empty renderer selection suppresses the business click. Clipboard completion is asynchronous, but native failure is swallowed. | Retain the P27.1 release precedence; do not use this as extraction or truthful-result evidence. |
| Codex `codex-rs/tui/src/tui/event_stream.rs`, `poll_crossterm_event` and `map_crossterm_event` | Mouse events are deliberately ignored. | Preserve Shift/terminal-native escape only; it does not answer application extraction. |

No reviewed reference proves exact endpoint reprojection after a width,
display-profile, environment, expansion, or item-version change. The
project-owned P27.2 contract therefore clears stale content identity before
highlight, extraction, copy, or release-action fallthrough. Frame-only scroll,
follow, sticky, and pill changes may retain item-local endpoints.

Evidence and target behavior remain distinct:

| Case | Verified current/reference fact | Selected P27.2 consequence |
|---|---|---|
| Visual soft wrap | Eino inserts `\n` per row; Claude records `softWrap`. | Renderer metadata joins continuations without bytes. |
| Semantic hard boundary | Eino cannot distinguish it from wrap. | Crossing the declared boundary inserts exactly one `\n`. |
| Inter-item boundary | Eino currently adds one row newline; the visible gap is separate P27.1 chrome. | Insert one `\n`; never copy the gap as a blank line. |
| Selected semantic spaces/tabs | Eino trims; Claude trims logical endings; Crush normalizes all whitespace. | Preserve represented selected bytes; layout fill remains excluded. |
| Gutter, identity glyph, indentation, padding, and controls | Numeric prefixes and stripped final rows cannot express every origin. | Renderer declares non-selectable spans; ANSI/OSC bytes never enter copy. |
| Grapheme/cell boundary | Eino already uses one immutable `DisplayCellProfile`; Claude skips wide-cell spacer slots. | Preserve Eino's profile and clamp without splitting or duplicating a cluster. |
| Reverse/cross-item/scroll | Crush normalizes item ranges; Claude captures scrolled rows. | Preserve normalized item-local endpoints while content identity matches. |
| Resize/reflow/stale content | The Eino probe reuses changed line/cell meaning; no reference proves exact reprojection. | Clear and consume the stale interaction without clipboard or business action. |

This refresh supports the existing `combine` decision: preserve Eino-Agent's
`DisplayCellProfile`, item-local endpoints, P27.1 projection, and string
renderer; adapt renderer-owned wrap/non-select metadata and normalized
interaction rules; add a project-owned semantic copy projection and
fail-closed identity rather than importing a second screen or renderer.

## P27.3 Promotion Refresh

P27.3 intake refreshed the transport question on Eino-Agent master
`b03b16e31deb457b2ddeaf43d8c1036de8156fa5`. The current helper still returns
no result, writes OSC 52 directly to `os.Stdout`, runs a native helper
synchronously, ignores the helper error, and lets all four callers announce
success immediately:

- expand-view release and chat release in
  [`App.Update`](https://github.com/abietic/eino-agent/blob/b03b16e31deb457b2ddeaf43d8c1036de8156fa5/internal/tui/app.go);
- keyboard selection copy in
  [`key_actions.go`](https://github.com/abietic/eino-agent/blob/b03b16e31deb457b2ddeaf43d8c1036de8156fa5/internal/tui/key_actions.go); and
- command `ActionCopy` in
  [`app.go`](https://github.com/abietic/eino-agent/blob/b03b16e31deb457b2ddeaf43d8c1036de8156fa5/internal/tui/app.go).

The TUI construction root creates one
[`TerminalOutput`](https://github.com/abietic/eino-agent/blob/b03b16e31deb457b2ddeaf43d8c1036de8156fa5/internal/tui/terminal_output.go)
with an unbuffered, synchronously acknowledged request channel and a 750 ms
write deadline. It passes that exact writer to Bubble Tea in
[`runTUI`](https://github.com/abietic/eino-agent/blob/b03b16e31deb457b2ddeaf43d8c1036de8156fa5/cmd/eino-agent/cmd/root.go).
A write failure closes `Failed`; `runTUIProgram` then kills the program and
owns bounded close, restore, and the returned terminal error. P27.3 reuses
that owner rather than adding a clipboard-specific stdout path.

The reference refresh separates useful mechanisms from truthful evidence:

| Evidence | Verified transport behavior | P27.3 consequence |
|---|---|---|
| OpenCode `packages/tui/src/clipboard.ts` and `src/util/selection.ts` | Selection feedback is Promise-driven; OSC 52 and a native method are attempted asynchronously. Native command failures are swallowed, and `TMUX` and `STY` share one tmux wrapper. | Adapt asynchronous result-driven UI and fixed helper argv. Reject swallowed failures and a shared unproven multiplexer wrapper. |
| Crush `internal/ui/common/common.go` and `internal/clipboard` | A Bubble Tea command sequences terminal and native clipboard writes, then always reports success. Its clipboard API returns no write result. | Preserve the command boundary only; reject it as truthful-result evidence. |
| Claude Code Ripe `useCopyOnSelect.ts` and `ScrollKeybindingHandler.tsx` | Mouse-up copy feedback distinguishes native, tmux-buffer, and raw OSC 52 paths and explicitly warns that OSC 52 may require terminal settings. | Adapt path-specific wording and the rule that OSC 52 is not acceptance. Its path prediction is not a substitute for observing Eino-Agent writes and helper exits. |
| tmux [`Clipboard`](https://github.com/tmux/tmux/wiki/Clipboard) and [`FAQ`](https://github.com/tmux/tmux/wiki/FAQ) documentation | OSC 52 support depends on tmux and outer-terminal configuration. DCS passthrough uses a `tmux;` prefix and doubled escape bytes; current tmux also gates passthrough by configuration. | Freeze exact tmux bytes and keep acceptance unresolved. GNU screen receives its own deterministic DCS wrapper rather than inheriting the tmux prefix. |

No reviewed source provides portable OSC 52 acknowledgement. Native helper
exit zero is the strongest available local observation, but still does not
prove a later paste/read-back. The project-owned budget and outcome contract
therefore is:

| Boundary | Frozen P27.3 value |
|---|---|
| Input | Non-empty UTF-8 text, at most 262,144 source bytes before base64; reject rather than truncate. |
| Terminal write | Same `TerminalOutput`; its existing 750 ms complete-packet deadline and fatal failure lifecycle remain authoritative. |
| Native helper | At most one fixed helper, fixed argv plus stdin, no shell, one two-second end-to-end context deadline. |
| Cardinality | One App request in flight; later callers receive a busy result and create no queued command. |
| SSH | Skip native clipboard access and use only the serialized terminal sequence. |
| Multiplexer bytes | Direct `\x1b]52;c;<base64>\x07`; tmux `\x1bPtmux;\x1b\x1b]52;c;<base64>\x07\x1b\\`; screen `\x1bP\x1b]52;c;<base64>\x07\x1b\\`; tmux wins if both markers exist. |
| Success wording | “Copied” only after native helper exit zero. OSC-only delivery says the request was sent and acceptance is unconfirmed. |
| Failure | Oversize/busy/helper categories are typed and redacted; terminal-output failure emits no success and preserves kill/drain/restore. |

This refresh retains the P27 `combine` decision and closes its promotion gaps:
the current writer and four callers are verified, exact payload/time budgets
are selected, the construction-root handoff is frozen, and the transport
outcome table is accepted. It does not claim that physical terminals, tmux,
screen, or remote sessions will accept OSC 52.

## Reference Evidence

| Reference | Verified mechanism | Selected consequence |
|---|---|---|
| Claude Code Ripe | `FullscreenLayout.tsx:338-361,539-588` places a fixed one-row sticky sibling before the scroll box. `render-node-to-output.ts:695-724` publishes the first visible content row from actual layout and padding. `VirtualMessageList.tsx:494-514` uses that origin for screen highlights. `ink.tsx:451-550,1011-1039` highlights and copies from the same screen cells; `selection.ts:712-795` records soft-wrap joins. | Adapt one final-layout viewport origin and one highlight/copy geometry. Do not transplant the full screen buffer. |
| Crush | `internal/ui/model/ui.go:983-995,1047-1074` removes the outer layout origin; `internal/ui/model/chat.go:839-908,950-1021` keeps item-local selection. `internal/ui/list/highlight.go:54-134` draws and extracts through one rendered buffer. | Adapt the single rendered-geometry rule and Bubble Tea-owned command boundary, while retaining Eino-Agent cell semantics. |
| OpenCode | `packages/tui/src/routes/session/index.tsx:1165-1282` separates transcript scroll content from the footer, and `:1254-1265,1891-1900` suppresses click actions while renderer selection exists. `packages/tui/src/util/selection.ts:26-77` reports asynchronous clipboard completion. | Adapt chrome separation, selection-aware release suppression, and result-driven feedback. Renderer internals remain external evidence, not a geometry proof. |
| Pi | `packages/tui/src/utils.ts:71-139,162-260` segments graphemes and strips ANSI before width calculation. | Preserve the same class of grapheme-aware policy already owned by `DisplayCellProfile`; do not add a competing width owner. |
| Codex | `codex-rs/tui/src/tui/event_stream.rs:189-193,250-251` deliberately skips mouse events. | Preserve Shift as an escape path; reject replacing the requested application selection with terminal-only behavior. |
| Grok Build | `scrollback/text_selection.rs:20-104` defines auto-scroll from a content rectangle and resolved selectable rows. Its wrapping helpers retain byte ranges but do not prove Eino-Agent grapheme semantics. | Adapt content-boundary auto-scroll and explicit row resolution; reject a character-width downgrade. |

Claude's sticky header is static chrome outside the scroll transform, but it is
still drawn into the same selectable screen buffer and is not marked
non-selectable. Eino-Agent intentionally adopts the geometry separation while
making the stronger project-owned choice that sticky, padding, pill, status,
and footer rows cannot start transcript selection.

## Verified, Inferred, And Unresolved

| Classification | Finding |
|---|---|
| Verified | Sticky rendering changes final row zero without changing the current forward or inverse selection transforms. |
| Verified | Highlight and extraction consume the same wrong item endpoint through different row paths, producing the observed visible/copy mismatch. |
| Verified | The same missing content rectangle reaches edge-scroll and Agent trace pointer behavior. |
| Inferred | Other one-row or overlay chrome can cause equivalent drift when it is added after a helper reconstructs transcript rows independently. P27 tests must prove each named row kind. |
| Unresolved | Which clipboard transport succeeds in the user's terminal, tmux, SSH, Wayland, X11, or macOS environment. |

## Compatibility Consequences

- Transcript selection can no longer begin on sticky, padding, gap, pill,
  status, or footer cells. A drag that began in transcript content clamps at
  the nearest visible transcript boundary.
- Existing Shift-drag behavior remains available for terminal-native selection.
- A real selection takes precedence over Agent trace or other release actions.
- Resize or reflow may clear a selection when exact reprojection cannot be
  proved; copying different text is never an accepted fallback.
- Soft-wrap extraction and clipboard delivery change only in their later
  independently promoted slices.
- No engine event, reducer, session schema, transcript, replay, provider,
  permission, or Eino/Eino-ext ownership changes.

## Rejected Shortcuts

- subtracting one from `chatY` only when `stickyLine != ""`;
- adding a second sticky offset to highlight without changing hit testing;
- treating the pill row as ordinary transcript content;
- rebuilding row ownership separately in each mouse, highlight, link, and Agent
  trace helper;
- replacing `DisplayCellProfile` with rune or byte columns;
- copying the entire Claude or Crush screen-buffer architecture;
- declaring clipboard success because an OSC 52 sequence was written; and
- moving presentation-only selection state into QueryEngine or durable session
  state.

## Evidence Limits

The screenshot establishes the user-visible symptom; source and focused tests
establish the causal path. No physical-terminal clipboard success is claimed.
Reference paths and commits are evidence snapshots, not product authority.
Current implemented TUI behavior remains owned by
[`architecture/tui/README.md`](../../../architecture/tui/README.md).

## Recommendation

`combine`: preserve Eino-Agent's item-local selection, immutable
`DisplayCellProfile`, Shift escape, and viewport-bounded rendering; adapt the
references' actual-layout content rectangle, shared rendered-row ownership,
soft-wrap metadata, selection-aware click suppression, and asynchronous
clipboard result; add one project-owned final-frame projection and fail-closed
render identity rather than adopting any reference wholesale.
