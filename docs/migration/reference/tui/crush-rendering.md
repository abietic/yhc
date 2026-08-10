# Crush TUI Rendering Architecture Reference

**Status:** reference-snapshot
**Research date:** 2026-07-10
**Direction note:** 2026-07-16
**Local snapshot:** `.reference/crush` at `d20e29ae7500`

> **Ownership:** implementation reference for the native Go
> TUI. It does not define product parity or current completion. See
> [`architecture/tui/README.md`](../../../architecture/tui/README.md),
> [`migration/history/tui/workstreams.md`](../../history/tui/workstreams.md), and
> [`migration/STATUS.md`](../../STATUS.md).

The original research used Crush to identify efficient Go rendering patterns.
Claude Code Ripe supplies broader workflow compatibility evidence, while Crush
supplies Go-native rendering evidence. Neither automatically defines the target;
adoption follows [`PROJECT_DIRECTION.md`](../../../../PROJECT_DIRECTION.md).

## 1. Crush Overview

Crush is a Go-based terminal AI coding assistant by Charm, built on:

- **Bubble Tea v2** — terminal UI framework (event loop + model/update/view)
- **lipgloss v2** — styling and layout
- **glamour v2** — terminal Markdown rendering

Module path: `github.com/charmbracelet/crush`

---

## 2. Architecture — Hybrid Rendering

Crush uses a **hybrid rendering** model that combines two rendering strategies:

- **Screen-based (Ultraviolet ScreenBuffer)** for top-level layout — the main UI model writes directly to a 2D screen buffer via `Draw()` calls
- **String-based sub-components** (`list.List`, completions, etc.) render to plain strings which are then painted onto the screen buffer

The top-level `View()` lifecycle is:

1. Create a new `ScreenBuffer`
2. Call `Draw(scr, area)` on sub-components — each writes cells into the buffer
3. Call `canvas.Render()` to flatten the 2D buffer into a single output string

Sub-components that produce string output (e.g., `list.List.Render(width)`) return a string that the parent decodes and blits onto the screen buffer. This is where the F9 draw cache (see below) eliminates redundant ANSI parsing.

---

## 3. Centralized Message Handling

- The **UI model** (`model.go`) is the sole Bubble Tea model — it owns `Init()`, `Update()`, and `View()`
- Sub-components (Chat, List, etc.) have **NO** `Update` method — the main model calls imperative methods on them:
  - `HandleMouseDown(x, y)`
  - `ScrollBy(delta)`
  - `SetMessages(msgs)`
  - `Animate(msg)`
- Components return `tea.Cmd` for side effects (timers, IO) but never process messages themselves
- Rendering is driven by `Render(width int) string` (string-based) or `Draw(scr, area)` (screen-based)

This design ensures a single, predictable message-handling path with no ambiguity about which component owns which message.

---

## 4. 5-Layer Cache System (Bottom-Up)

Crush implements five distinct caching layers to minimize re-rendering work. Listed from lowest (closest to raw content) to highest (closest to final output):

### Layer F8: Streaming Markdown Prefix Cache

**File:** `internal/ui/chat/streaming_markdown.go`

**Purpose:** Avoid re-rendering the entire Markdown document on every streaming token.

**Key struct — `streamingMarkdown`:**

| Field                | Type     | Description                                      |
|----------------------|----------|--------------------------------------------------|
| `width`              | `int`    | Current render width                             |
| `stablePrefix`       | `string` | Content up to the last safe boundary             |
| `stablePrefixRender` | `string` | Cached glamour render of `stablePrefix`          |

**Algorithm:**

1. `findSafeMarkdownBoundary()` finds the latest blank-line position where it is safe to split the Markdown document without breaking any block-level construct
2. `Render()` splits the streaming content at this boundary
3. The prefix portion is cached; only the trailing portion (after the boundary) is re-rendered on each update
4. When new tokens arrive, the boundary may advance — the prefix cache grows monotonically

**Thread safety:** Mutex protection via `common.LockMarkdownRenderer()` — goldmark's internal `BlockStack` is stateful and not safe for concurrent use.

**Eino-Agent M4.3 adaptation:** the Go port no longer uses Crush's blank-line
walk-back as its primary boundary. `StreamingMarkdown` parses only the mutable
suffix with the already-present Goldmark GFM parser and promotes every complete
top-level block before the final block. This matches Claude Code's parser-level
stable/mutable split while preserving Crush's synchronous rendered-prefix
cache. The old conservative detector remains only as a source-position
fallback and for global reference-link hazards.

---

### Layer F3: Per-Section FNV-64 Hash Caches

**File:** `internal/ui/chat/assistant.go`

**Purpose:** Cache rendered output for each logical section of an assistant message independently.

**Key struct — `assistantSection`:**

| Field     | Type     | Description                                      |
|-----------|----------|--------------------------------------------------|
| `width`   | `int`    | Render width when cached                         |
| `srcHash` | `uint64` | FNV-64 hash of source content                    |
| `extra`   | `uint64` | FNV-64 hash of additional state (focus, etc.)    |
| `out`     | `string` | Cached rendered output                           |
| `h`       | `int`    | Cached height in lines                           |
| `valid`   | `bool`   | Whether cache entry is valid                     |

**Design:**

- Each assistant message has **3 independent sections**: `thinkingSec`, `contentSec`, `errorSec`
- During streaming, only `contentSec` is invalidated — the expensive `thinkingSec` render is preserved
- Hash computation uses `fnv64()` and `fnvFields()` helper functions

---

### Layer F3: Per-Item Prefixed Render Cache

**File:** `internal/ui/chat/messages.go`

**Purpose:** Cache both the raw content render and the final prefixed output for each message item.

**Key struct — `cachedMessageItem`:**

| Field              | Type     | Description                                               |
|--------------------|----------|-----------------------------------------------------------|
| `rendered`         | `string` | Raw content render (without prefix)                       |
| `width`            | `int`    | Width used for raw render                                 |
| `height`           | `int`    | Height of raw render                                      |
| `prefixedRendered` | `string` | Final output with per-line prefix applied                 |
| `prefixedWidth`    | `int`    | Width used for prefixed render                            |
| `prefixedKey`      | `string` | Composite cache key (folds focus, section hashes, etc.)   |

**Two-tier caching:**

1. **Raw content cache** — keyed by `width`
2. **Prefixed output cache** — keyed by `width` + composite key incorporating focus state, section hashes, and other render-affecting state

---

### Layer F6: List-Level Version+Pointer Cache

**File:** `internal/ui/list/list.go`

**Purpose:** Cache rendered output for each list item, keyed by item identity, width, and version.

**Key struct — `listCacheEntry`:**

| Field     | Type       | Description                                      |
|-----------|------------|--------------------------------------------------|
| `width`   | `int`      | Render width when cached                         |
| `version` | `uint64`   | Item version when cached                         |
| `frozen`  | `bool`     | Whether item is frozen (terminal state)          |
| `content` | `string`   | Cached rendered string                           |
| `lines`   | `[]string` | Pre-split lines (avoids repeated `strings.Split`)|
| `height`  | `int`      | Number of lines                                  |

**Cache key:** `(Item pointer, width, version)` — stored in `map[Item]*listCacheEntry`

**Render flow in `renderItemEntry()`:**

1. Run render callbacks first (focus/highlight may bump version)
2. Check cache: if pointer + width + version match, return cached entry
3. On cache miss: call `Item.Render(width)`, store result
4. **Frozen items:** Items where `Finished()` returns `true` get `frozen=true` and are never re-rendered
5. **Freeze suppression:** `freezeSuppressed` map provides an escape hatch during selection-drag operations
6. **Width change:** Drops the entire cache (all entries invalidated)

---

### Layer F9: Draw Cache (ANSI Decode)

**File:** `internal/ui/model/chat.go`

**Purpose:** Avoid re-parsing ANSI escape sequences when the rendered string hasn't changed.

**Key struct — `chatDrawCache`:**

| Field      | Type           | Description                                    |
|------------|----------------|------------------------------------------------|
| `rendered` | `string`       | Last rendered string from `list.Render()`      |
| `method`   | (render method) | Which render path produced it                 |
| `buf`      | `ScreenBuffer` | Decoded screen buffer ready for blitting       |

**Behavior:**

- If `list.Render()` returns the same string as last time, the cached `ScreenBuffer` is blitted directly — no ANSI parsing required
- Single-entry cache: invalidated by string inequality
- This is the bridge between string-based rendering (list) and screen-based rendering (Ultraviolet)

---

## 5. Key Interfaces

### `list.Item` (`list/item.go`)

The fundamental interface for anything rendered in a list:

```go
type Item interface {
    Render(width int) string
    Version() uint64    // monotonic mutation counter
    Finished() bool     // terminal state — safe to freeze
}
```

- `Version()` returns a monotonically increasing counter. Every mutation that affects rendering must bump this value.
- `Finished()` signals that the item has reached a terminal state and its rendered output will never change again — enabling permanent cache freezing.

### `Versioned` Helper (Embeddable)

```go
type Versioned struct { v uint64 }
func (vc *Versioned) Version() uint64  // returns current version
func (vc *Versioned) Bump()            // increment version
```

Embed `Versioned` in your item struct to get automatic version tracking.

### Optional Interfaces

Items may optionally implement these for extended behavior:

| Interface       | Methods                                            | Purpose                                |
|-----------------|----------------------------------------------------|----------------------------------------|
| `RawRenderable` | `RawRender(width int) string`                      | Render without per-line prefix         |
| `Focusable`     | `SetFocused(bool)`                                 | Focus state — bumps version on change  |
| `Highlightable` | `SetHighlight(startLine, startCol, endLine, endCol int)` | Text selection highlighting     |
| `Animatable`    | `StartAnimation()`, `Animate(msg) tea.Cmd`         | Frame-based animation support          |
| `Expandable`    | `ToggleExpanded() bool`                            | Collapsible content sections           |

---

## 6. Boundary Detection Algorithm (Crush Reference)

`findSafeMarkdownBoundary()` determines where streaming Markdown content can be safely split for incremental rendering.

### Walk-back Algorithm

The function walks **backward** through blank-line positions in the content:

1. For each candidate position `p`, call `isSafeBoundaryAt(content, p)`
2. A position is safe if **all** of the following are true:
   - **Even fence count** — no open code block (an odd number of `` ``` `` lines before position means we're inside a fenced code block)
   - **No "open hazards" in prefix** — no list markers, HTML block openers, or link reference definitions (conservative: any occurrence forfeits the boundary)
   - **Last non-blank line doesn't open a construct** — not indented code, blockquote, list item, table pipe, or setext underline
   - **Line after boundary isn't a setext underline** — prevents splitting a heading from its underline (`===` or `---`)

### Helper Functions

| Function                      | Purpose                                              |
|-------------------------------|------------------------------------------------------|
| `blankLineBefore()`           | Find position of blank line before a given offset    |
| `isBlankOrSpaces()`           | Check if a line is blank or whitespace-only          |
| `countFenceLines()`           | Count `` ``` `` fence lines in content prefix        |
| `isFenceLine()`               | Check if a line is a code fence                      |
| `lastNonBlankLine()`          | Find last non-blank line before a position           |
| `firstNonBlankLine()`         | Find first non-blank line after a position           |
| `lineOpensConstruct()`        | Check if a line opens a block-level construct        |
| `isListItemMarker()`          | Detect `- `, `* `, `1. ` etc.                       |
| `isSetextUnderlineCandidate()`| Detect `===` or `---` setext heading underlines      |
| `isHTMLBlockOpener()`         | Detect raw HTML block starts                         |
| `isLinkRefDefinition()`       | Detect `[label]: url` reference definitions          |
| `prefixHasOpenHazard()`       | Check prefix for any open hazard constructs          |

### Eino-Agent stable-region contract

The current implementation uses four invariants:

1. `stablePrefix` is a literal, newline-complete prefix of assistant source.
2. The final Goldmark top-level block is always the mutable tail, so active
   tables, lists, setext/HTML blocks, and fences cannot leak into frozen output.
3. Width change or non-prefix source replacement discards rendered fragments
   and reparses source; no rendered string is treated as reflow input.
4. Finalization clears all stitched fragments and renders the complete source
   into the same terminal `AssistantMessage`, which becomes the sole canonical
   transcript item.

Reference-style links deliberately disable region splitting because a later
definition can change an earlier block. Parser and Glamour renderer access are
serialized independently so parallel tests and render widths remain race-free.

---

## 7. Rendering Performance Patterns

### Manual Per-Line Prefix

```go
"  " + line  // instead of lipgloss.Render()
```

For long messages, manually prepending indent strings per line is significantly faster than running each line through lipgloss's style renderer.

### Width-Capped Messages

Maximum message width is capped at **120 characters** for readability, regardless of terminal width.

### Animation Visibility

- Only animate items that are currently visible (bounded by `VisibleItemIndices()`)
- Off-screen animated items are moved to a `pausedAnimations` map
- When items scroll back into view, animations resume

### O(viewport) Rendering

`List.Render()` only processes visible items starting from `offsetIdx`, never rendering more than `height` lines total. Items outside the viewport are completely skipped.

### Pre-Split Lines

Cache entries store both the `content` string and a pre-split `lines []string` slice. This avoids repeated `strings.Split()` calls during scrolling and viewport calculations.

---

## 8. Logic Mapping to Eino-Agent

This table records implementation evidence, not product completion.

| Rendering pattern | Eino-Agent evidence | Current judgment |
|---|---|---|
| Item `Version`/`Finished` contract | `internal/tui/chat.go:ChatItem` | Implemented, unverified against long-session scenarios |
| Version-based render cache | `ChatView.renderItem` | Implemented |
| Frozen completed entries | `renderCacheEntry.frozen`, `ChatView.renderItem` | Implemented |
| O(viewport) rendering | `ChatView.Render`, `offsetIdx`, render budget | Implemented |
| Item-index scrolling/follow | `ScrollUp`, `ScrollDown`, `computeFollowOffset` | Implemented |
| Streaming Markdown stable/mutable regions | `StreamingMarkdown.Render` | Implemented with output golden, focused tests, and benchmark evidence |
| Parser-derived Markdown boundaries | `findSafeMarkdownBoundary` | Goldmark top-level block split plus conservative reference/fallback checks |
| Empty-cache advancement | `StreamingMarkdown.tryAdvanceFromEmpty` | Implemented |
| Fragment joining | `glueRenders` | Implemented |
| Canonical terminal render | `StreamingMarkdown.Finalize`, `AssistantMessage.Finalize` | Implemented; one source-backed transcript item |
| Renderer synchronization | `lockRenderer` | Implemented |
| Readability width cap | `ChatView.renderWidth`, assistant rendering | Implemented at 120 columns |
| ANSI/Unicode table correction | `fixTableAlignment`, emoji width helpers | Implemented with focused tests |
| Visibility-aware animation | No equivalent visibility lifecycle | Not implemented; evaluate after task/sub-agent presentation |
| Rectangle layout | `layoutRect`, `calculateLayout`, `renderLayoutBands` | Implemented with contiguous exact-height ownership and geometry/golden tests |
| Formal dialog routing | `DialogStack`, `appDialogStack` | Implemented with top-only input and back-to-front overlays |
| Ultraviolet draw cache | No Ultraviolet screen buffer | Intentionally deferred: string bands measure about 4.2 us/op and a 100-turn full view about 127-130 us/op, so composition is not the demonstrated bottleneck |
| Per-section assistant caches | Thinking and assistant are separate message items | Adapted rather than copied |

Future updates should refer to Go symbols rather than line numbers. The exact
rendering implementation may diverge from Crush as long as performance,
correctness, and maintainability are demonstrated.
