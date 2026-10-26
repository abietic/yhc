# Structured Composer Contract

**Status:** current

**Last verified:** 2026-10-26

**Ownership:** `internal/tui` owns one mutable active-draft projection;
`QueryEngine` owns accepted prompt and queued-input truth.

## Model

Each thread view owns text, cursor, and a bounded set of
[`threadComposerElement`](../../../../internal/tui/thread_view_state.go)
records. An element carries stable correlation identity, kind, visible label,
and a half-open rune range. Paste and context elements retain bounded textual
source data. Image elements retain only identity, label, MIME descriptor, and
range.

The App-owned draft media table is the only TUI owner of captured image bytes.
An image ID is reachable from the active thread's live/draft/undo projection or
an inactive thread view, but raw bytes are not copied into composer elements,
undo records, queue previews, chat rows, search rows, or prompt history.
Unreachable draft media is zeroed and removed.

Example:

```text
Draft:  "Review [Image #1] after [Pasted Content 1200 chars]"
Image:  element ID -> one draft-media entry; range -> visible label
Paste:  range -> visible label; Value -> retained source text
```

## Invariants

1. `text[Start:End]` in rune coordinates equals `Label` exactly.
2. Live element ranges do not overlap and image IDs are unique.
3. Each image ID resolves to exactly one bounded App-owned draft-media entry.
4. Async image load results bind request ID, leader thread ID, draft revision,
   and captured insertion anchor. A stale result is discarded and zeroed.
5. At most one image load and one submission admission may be in flight for a
   draft.
6. Submission rejects the whole snapshot on a missing, duplicate, edited,
   overlapping, unsupported, or inconsistent element.
7. Paste, image, file, MCP-resource, selected-output, and Git-diff payloads are individually bounded by
   5 MiB. The App retains at most 10 MiB of image bytes and at most 32
   elements across active thread drafts.
8. Durable history, chat, rewrite, search, selection, queue preview, and
   runtime-state projection never own raw image bytes, paths, refs, digests,
   or restorable rich payload.

The bounds come from
[`attachments.MaxAttachmentBytes`](../../../../internal/tui/attachments/attachments.go)
and [`composer_elements.go`](../../../../internal/tui/composer_elements.go).

## Editing And Reconciliation

[`reconcileComposerElements`](../../../../internal/tui/composer_elements.go)
finds one contiguous edit using the longest common rune prefix and suffix.
Elements before the edit remain unchanged; elements after it shift; elements
intersecting it are removed. A shifted element is retained only if its label
still matches the new text.

Undo, thread switching, history recall, external-editor replacement, rewrite,
and accepted submission all run draft-media reachability collection. A
recalled submitted rich row restores only sanitized text and an explicit
`image content not restored` label; it never reconstructs image elements.
Detailed editing behavior is owned by [`editing.md`](editing.md).

## Command arguments

Command-name completion reads the engine command registry and matches canonical
names and aliases. Tab accepts the selected row, or the first row when none is
selected, as `/name ` without submitting. The trailing space closes command-name
completion and opens the available argument candidates. The engine command
registry owns advisory choices, positional/subcommand metadata, runtime model
and effort values, MCP names, session IDs, and explicitly declared path sources.
The TUI queries these asynchronously; directory and session requests debounce
for 80 ms, and directory listings use a bounded one-second cache. Ordinary
arguments and display-only hints never imply a filesystem source.

[`argument_completion.go`](../../../../internal/tui/argument_completion.go)
projects one candidate result into both the popup and inline ghost. Up/Down
changes the selected preview without changing the draft. Tab accepts the
selected or first candidate as one undoable edit; Right Arrow accepts a ghost
only at the end of the draft. Enter accepts an explicitly selected row without
execution; with no selected row it retains ordinary submission semantics.
Completed arguments close their candidates. Escape dismisses candidates, the
free-text suggestion prompt, or a pending request and preserves the draft until
another edit; a second Escape retains ordinary draft cancellation. Replacement
uses half-open rune ranges, preserves text outside the replaced argument, and
quotes values for the existing strict dispatch parser. Non-prefix and
middle-cursor replacements use the popup without an append-only ghost.

Request identity includes engine, thread, draft revision, query, cursor, CWD,
model selector, route generation, and request serial. History recall, modal
input, structured elements, submission admission, edits, and engine/thread
changes hide or cancel completion; mismatched late results are discarded.

Ctrl+Space explicitly requests a free-text argument suggestion when metadata
permits it. It never runs automatically while typing and never predicts closed
choices such as model selectors or effort levels. The engine's
[`GenerateCommandArgumentSuggestion`](../../../../engine/command_argument_suggestion.go)
uses the shared tool-free auxiliary provider boundary with a four-second
deadline, bounded command metadata and prefix, strict JSON output, prefix
validation, and separate auxiliary usage accounting. It reads no prompt-recall
history, skill body, files, or transcript. Plan mode, pending permission input,
active turns, and unfinished Goals suppress the call. Accepting its candidate
still edits only the draft.

For an exact available command with no typed arguments, a focused single-line
composer shows `Command.ArgumentHint()` in gray after the cursor. This display
projection is clipped to the available cells and is hidden during history
recall, modal ownership, cursor movement into the text, or actual argument
entry. It never changes text, cursor, undo, structured elements, or submission
bytes. Tab, Right Arrow, and Enter cannot accept argument placeholders.
Selecting a skill in the command palette stages it for argument editing.

The skill discovery and invocation contract belongs to
[`Skills`](../../capabilities/skills.md). Regression evidence is in
[`command_argument_hint_test.go`](../../../../internal/tui/command_argument_hint_test.go)
and [`skill_commands_test.go`](../../../../internal/tui/skill_commands_test.go).
Candidate and terminal regressions are in
[`argument_completion_test.go`](../../../../internal/tui/argument_completion_test.go)
and [`argument_completion_pty_unix_test.go`](../../../../internal/tui/argument_completion_pty_unix_test.go).

## Next-prompt ghost

After an ordinary leader turn completes successfully, the TUI may request one
model-generated next prompt from
[`QueryEngine.GeneratePromptSuggestionResult`](../../../../engine/prompt_suggestion.go). The
request reads a bounded snapshot of the current engine conversation. It does
not use prompt-recall history, mutate the conversation, expose tools, or write
a transcript record.

The returned text is an ephemeral App projection. It is visible only while the
leader composer is idle, empty, ordinary, and following the chat, with no
dialog, Plan mode, history search/recall, command/file/mention candidate,
attachment load, or submission admission owning the input. Request and result
identity include the engine, thread, composer revision, query ID, and
generation. User input, thread/engine changes, new runtime work, or shutdown
cancel the request; a late result that misses any fence is discarded.

With an empty ordinary leader draft, the existing `chat:argumentSuggest`
action (`Ctrl+Space` by default) explicitly requests a next-prompt suggestion.
With a slash-command argument it retains its argument-completion behavior.
The manual path displays progress and a bounded explanation when the request
is suppressed, empty, filtered, timed out, cancelled, failed, or invalidated
by a route change. Automatic post-turn requests remain quiet when they produce
no candidate. The shared result and reason-code owner is
[`Interactive prompt suggestions`](../../platform/runtime-services.md#interactive-prompt-suggestions).

Pressing the shortcut again while a request is pending exposes its progress
without starting another call. Typing, `Esc`, or `Ctrl+C` dismisses the
ephemeral feedback and cancels the request; no candidate, reason, or status is
appended to chat or prompt history. Late results cannot restore dismissed
feedback. A cancelled provider that has not returned retains the service's
single-flight slot.

The ghost is rendered through a cloned textarea placeholder. It does not
change the authoritative textarea value, cursor, structured elements, history,
undo, or submission snapshot. `Tab` or Right Arrow copies it into the draft as
one undoable edit; it never submits. `Enter` dismisses an unaccepted ghost and
retains ordinary empty-draft behavior. Typing replaces the prediction path
with the user's actual edit. YHC exposes one candidate and does not cycle
alternatives. The ordinary `Ask anything...` initial placeholder remains local
and is not generated from Git history.

## Input Sources

- A paste over 800 runes becomes a compact placeholder and retains its bounded
  source text
  ([`handleComposerPaste`](../../../../internal/tui/composer_elements.go)).
- Image path and clipboard loading is asynchronous. The captured bytes enter
  one draft-media entry only after the exact request/thread/revision/anchor
  fence still matches.
- Clipboard capture runs through an injected typed boundary with a deadline,
  bounded stdout/stderr and file reads, unique private temporary files where
  the platform requires them, and deterministic Darwin/Linux/Windows
  behavior.
- `@` mentions load file, skill, or MCP resource context asynchronously and
  rejoin by element ID.

Loading failures remain visible errors or ordinary text; they never create a
hidden submit payload.

### File line ranges

TUI file mentions accept `@path#N` and `@path#N-M`, including the `@file:`
prefix. Lines are positive, one-based and inclusive. Tab stages the selected
candidate without submitting. Exact indexed filenames take precedence over
range syntax, including literal names containing `#` and case-insensitive
completion matches; append another selector to select lines from such a file.
Parsing and projection live in
[`composer_file_range.go`](../../../../internal/tui/composer_file_range.go).

[`ReadFileRange`](../../../../internal/tui/attachments/file_range.go) reads a
regular UTF-8 text file through the existing 5 MiB source limit and a bounded
reader. Empty files, NUL/non-UTF-8 sources, invalid ranges and endpoints past
EOF fail without clamping, truncating or falling back to full-file context.
The captured excerpt preserves LF/CRLF and final-newline bytes. Once loaded,
submission, undo and thread switching use that captured content without
rereading a subsequently changed file.

While the editor cursor is within or immediately after an accepted range, the
hint area shows selected lines, total source lines and selected byte size.
Its preview reads at most 1024 bytes and three lines, escapes terminal control
characters, and reports preview clipping separately from the complete attached
excerpt. In the standard editor, Backspace at the reference end removes the
reference and payload as one undoable edit; editing inside a label retains
ordinary reconciliation. A removed element cannot be restored by a late read.

The existing `composer_context` block keeps the original path in `source` and
adds numeric `start_line` and `end_line` only for a selected range. Submission
rejects inconsistent range metadata. Sanitized history retains only the visible
range reference; history recall does not reload the file or restore its body.
Whole-file, Skill and MCP references keep their existing contracts. No model
call is needed to select, preview or remove a range.

### Selected terminal output

The configurable `selection:attachContext` action (`Alt+A` by default) captures
the current chat selection or the current conversation's expanded-view
selection. The ordinary mouse drag remains the application selection path;
Shift+drag belongs to the terminal. Selection itself keeps the existing
copy-on-select behavior. The explicit action adds one `terminal_output`
element at the draft cursor and returns to the composer without submitting.

[`attachSelectedOutputContext`](../../../../internal/tui/composer_output_context.go)
reads the selected rendered text through the existing copy semantics. Chat
selection uses semantic spans and soft/hard line boundaries; expanded selection
uses display-line copying. `source` identifies `chat_selection` or
`expanded_transcript_selection`. These labels identify a display view;
canonical runtime events continue to own execution outcomes. The new context
block is added to the next prompt alongside the existing conversation history.

Extraction checks a 5 MiB budget before appending each selected segment and
counts separators. Expanded source rows have the same individual bound.
Oversized, empty, invalid-text or stale selections fail without partial
attachments. The shared 10 MiB draft and 32-element limits still apply.
Search, external-editor, pending-admission, command/shell, replay-only and
Session-preview input owners cannot attach a selection into another draft.
Insertion inside an existing attachment label is rejected rather than
silently discarding its payload.

Chat selection retains its existing content/environment/endpoint identity
checks. Entering, leaving, replacing or changing the raw projection of an
expanded view clears its selection; geometry changes also clear it. Switching
threads closes an expanded current-conversation view before restoring the new
draft. Once attached, the captured text survives source updates, undo and
thread switching without following the live output.

The shared
[`context preview`](../../../../internal/tui/composer_context_preview.go)
shows byte size and at most 1024 bytes/three lines with escaped terminal
controls. Preview clipping leaves the complete selected text in the attachment.
Standard-editor Backspace at the reference end removes one whole context
element; undo restores it. History and view sidecars keep visible references,
while history recall explicitly marks output content as not restored. Selection,
preview and removal perform no provider call, storage reread or command replay.

### Git file and hunk context

The TUI-only `/attach-diff [all|staged|unstaged]` command returns a typed local
intent. It opens the same selector as `d` outside search focus in the context
manager, preserving an existing ordinary draft. `/diff` keeps its engine-owned
display behavior and 200-line display limit; its formatted output is never
used as the attachment source.

[`CaptureWorkspaceDiff`](../../../../engine/workspace_diff_context.go) owns
the cancellable read-only Git boundary and fixes the engine CWD for capture.
It requires a bounded runner, caps stdout at 5 MiB and stderr at 64 KiB, disables
external diff/textconv, and discards the whole result on truncation. `all`
compares HEAD to worktree, `staged` compares HEAD to index, and `unstaged`
compares index to worktree. An unborn repository must select staged or
unstaged explicitly; untracked files are not included.

[`ParseDiffContext`](../../../../engine/commands/diff_context.go) parses only
the captured text. It preserves raw file and hunk bytes, quoted/renamed paths,
add/delete ranges and no-final-newline markers. A hunk attachment retains the
file metadata plus exactly one hunk. Binary entries are visible but unavailable;
malformed, combined, invalid-text and oversized patches fail closed. Parsing
is bounded to 512 files, 4096 hunks and 10 MiB of retained patch projections.

[`composer_diff_context.go`](../../../../internal/tui/composer_diff_context.go)
owns the transient selector. Left/Right changes source, Tab toggles file/hunks,
Up/Down navigates, Enter attaches one selection, and Esc/Ctrl+C cancels. The
selector requires an idle editable leader draft. Requests bind serial, engine,
thread, draft revision, source and CWD; source changes, edits, thread/engine
changes, cancellation and program shutdown cancel or invalidate pending work.
A permission dialog stacked above the selector retains input ownership.

An explicit selection inserts one undoable `git_diff` element with MIME
`text/x-diff`. The generic `composer_context` envelope contains the exact
captured patch plus source/file/hunk metadata. Preview, budget estimates,
submission, undo and thread snapshots never reread Git. Manager search,
the Diffs filter, token sorting, batch removal and budget warnings use the
existing attachment path. History and view sidecars retain references only;
history recall marks diff content as not restored. The selector cannot stage,
apply or revert changes, submit a turn or dispatch a model call.

### Attached-context manager

The TUI-owned `chat:manageContext` action (`Alt+C` by default) opens one passive
dialog for the active ordinary draft. It lists file ranges, whole files,
selected output, Skills, MCP resources, large pastes, images and Git diffs, initially in
draft order. The list reads existing element identities and captured payloads;
opening or navigating performs no source reread, provider call or submission.
An empty draft has an explicit empty state. History-only literal references do
not become attachments.

[`composer_context_manager.go`](../../../../internal/tui/composer_context_manager.go)
owns the bounded projection and exact removal. Text previews show byte size,
source and at most the first 1024 bytes/three lines. Labels and metadata are
bounded and terminal controls are escaped. Images show captured MIME type,
name and size without exposing bytes. Clipping affects only the preview.
Loading references show their status; removal waits for a captured payload so
undo can restore complete content without initiating a new load.

`/` focuses a separate 128-rune search field. Matching is case-insensitive
substring search over labels, names, source paths/IDs and human type labels,
bounded to the first 4096 bytes of each metadata field. It never scans text or
image payloads, including paste bodies. Enter or Esc leaves search focus while
retaining the query; Up/Down leaves search focus and moves the selection.
Tab/Shift+Tab cycles All, Files (whole and ranges), Output, Skills, MCP
resources, Pasted text, Images and Diffs. Type and search intersect. Empty matches
have a distinct empty state. Search input normalizes line breaks/tabs to
spaces and removes unsafe terminal controls.

Outside search focus, `s` cycles Draft, Tokens high (descending), and Tokens
low (ascending). Sorting applies only to the filtered projection, reusing the
draft-wide numeric cache. Equal estimates retain draft order; pending and
unavailable estimates stay last in either direction. Images participate with
their explicitly generic estimate. Selection and marks keep stable element
identity across sorting and load-driven reordering. The draft, submission
order, original rune ranges and undo history do not change. Configured undo
bindings/chords retain precedence over `s`; inside search it remains ordinary
query text.

Up/Down selects by stable identity. Space toggles a mark; Ctrl+A toggles all
matching attachments, including those outside the viewport. Changing query or
type clears marks so hidden items cannot remain deletion targets. Delete or
Backspace removes the marked set, or the highlighted element when nothing is
marked. The complete target set is checked before any mutation: stale,
missing, duplicate, hidden or loading targets reject the whole batch. Invalid
marked identities never fall back to deleting the highlighted element.

Removal validates stable identity and all non-overlapping rune ranges, deletes
each exact label and at most one following unowned space, then rebases
remaining elements and the cursor once. This preserves distinct payloads
behind identical labels. One ordinary composer undo record retains the entire
original rich draft for a batch; image bytes remain in the existing
reachability-owned draft-media table. Undo uses the configured chat binding
outside search focus and clears marks. Enter outside search returns to the
highlighted reference; Esc closes the manager and Ctrl+C closes even from
search, without submitting. Query, filter, sort and marks reset on open and are
never persisted in history or view sidecars.

The dialog accepts ordinary editable live drafts only. Pending admission/image
loading, external editing, history search, shell/command input and replay-only
views cannot open it. Modal keys and paste do not reach the composer, and a
stacked permission dialog retains input ownership. Pending key chords reset
when the manager opens, closes, or loses modal ownership. Thread switching removes
the manager before capturing/restoring the next view. Every mutation rechecks
the active thread, input owner and reference integrity. Only transient search,
navigation, selection and derived token counts stay in the dialog; session view
sidecars and history gain no payload or restorable dialog state.

#### Attachment tokens and capacity

[`composer_context_budget.go`](../../../../internal/tui/composer_context_budget.go)
projects per-attachment and all-attachment token estimates. Text uses the
existing `compact.EstimateTokenCount` UTF-8 byte heuristic (roughly bytes/4),
excluding shared message/transport overhead. File, Skill, MCP and output
estimates include the visible reference and the complete escaped JSON context
envelope; paste estimates preserve the full raw text, including boundary
whitespace. The encoder is shared with
submission in [`composer_elements.go`](../../../../internal/tui/composer_elements.go).
Preview clipping never reduces the count. Images use the existing generic
media weight, marked `*` with a legend; their provider-specific cost remains
unknown, and no base64 encoding or image decode is performed for estimation.

The total includes every attachment even when search/type filters hide rows.
Loading or unavailable content is excluded and marks the total partial.
[`composerContextTokenCache`](../../../../internal/tui/composer_context_budget.go)
stores only numbers/status by draft revision. Load completion, removal and undo
refresh it; search, selection and repaint reuse it. A newly opened manager has
a fresh cache. No payload copy is retained in the cache or sidecar.

[`QueryEngine.GetContextUsageSnapshot`](../../../../engine/context_usage.go)
reads model capacity and the latest main-loop provider input usage under one
lock, without provider resolution or transcript I/O. The active validated
binding's window is authoritative, including missing metadata; an unbound
legacy model may use only a known capability-table value. The panel shows
attachment share of that window, retaining values above 100%. Last input is a
separate measured fact; unknown values are unavailable, while a reported zero
remains zero. Child views do not reuse the leader's capacity or measured usage.

At 80% of a known window, the panel warns that attachment estimates alone are
near capacity. At 100% it shows an at/over-capacity alert. Thresholds use
unrounded counts; rounded display percentages do not select the warning tier.
Partial totals retain that qualification in the warning and may warn when
their known estimates already reach a threshold. Generic image weights remain
estimates. Filtering cannot hide a draft-wide warning; load, removal and undo
refresh it. An unknown window produces no threshold assertion, and absence of
a warning does not establish that the next request fits. Last input is never
added to the draft total, since it can already include the same attachments.

These are attachment estimates and maximum-window shares, not a tokenizer,
request total, remaining admission budget, auto-compaction threshold or billing
quote. Ordinary draft text, history, instructions and tools are excluded from
the attachment total. Counts do not trim context, block submission or trigger
compaction. Short viewports keep warnings, sort state, row estimates and close
controls ahead of optional summary/detail rows.

## Ordered Submission

[`captureComposerSubmission`](../../../../internal/tui/composer_submission.go)
performs one stable rune walk over the validated draft:

1. text before each element remains in place;
2. paste placeholders expand in place;
3. images become `engine.UntrustedPromptImage` parts at the placeholder
   position;
4. file, skill, MCP and selected-output placeholders remain visible while their
   `composer_context` blocks append to trailing text; and
5. only leading whitespace on the first text part and trailing whitespace on
   the last text part are trimmed.

The result is one literal ordered `engine.UntrustedPromptInput`. Image-only
input is valid. A literal string such as `[Image #1]` without a matching
element remains ordinary text.

[`App.sendMessage`](../../../../internal/tui/app.go) applies this matrix:

| Target or mode | Text/paste/context | Image |
|---|---|---|
| Idle leader | `QueryEngine.SubmitPromptInput` | Submit under exact selected-route admission |
| Busy leader | Explicit steer/next-turn choice via `EnqueuePromptInputWithDelivery` | Persist ref-backed ordered queue input |
| Agent thread | Send through Agent control | Reject and retain draft |
| Slash or shell mode | Typed command path only | Reject attachments and retain draft |

The TUI never clears the draft, appends a user row, or marks the query running
before synchronous engine acceptance returns. Admission rejection retains the
draft. A second Enter, editor/history/model/thread action, or media load is
rejected while admission is pending. `Ctrl+C` cancels the pending admission
and retains the draft; if durable busy acceptance already won, the exact queue
item is cancelled when possible and otherwise remains authoritative.

P30.6 seals this table as the only TUI rich-input owner. The older
`startEngineRequestWithImages` and metadata helper no longer exist;
`startEngineRequest` is the text-only compatibility path and calls only
`SubmitMessage`. A future TUI rich-input source must extend the immutable
composer snapshot and the typed engine boundary instead of adding another App
submission branch.

QueryEngine independently performs authoritative format, decoded-byte,
aggregate, pixel, animation, MIME, capability, generation, and
terminal-boundary admission.

## Persistence And Sanitization

Prompt-recall history is separate from transcript truth.
`expandComposerElementsForPersistence` expands paste text and converts file,
skill, MCP, and image elements into bounded visible references. Image history
includes an explicit non-restorable label and no bytes or path.

`saveHistoryEntry` writes the sanitized string to both
`~/.claude/history.jsonl` through `engine/history.Manager` and the legacy
project-local `.eino-agent/history`. New JSONL files request mode `0600`; the
legacy file requests `0644`. Neither path repairs an existing file's mode.

Pending leader input follows [`busy-queue.md`](busy-queue.md). The TUI
rebuilds a bounded sanitized descriptor preview from engine truth; no separate
rich preview exists.

## Evidence

- ordered validation, image-only input, missing/overlapping media, async-load
  fencing, admission settlement, GC, and sanitized rewrite:
  [`p30_4_composer_test.go`](../../../../internal/tui/p30_4_composer_test.go)
- element rebasing and pruning:
  [`composer_elements_test.go`](../../../../internal/tui/composer_elements_test.go)
- deterministic clipboard backends and bounds:
  [`attachments_test.go`](../../../../internal/tui/attachments/attachments_test.go)
- engine selected-route admission:
  [`engine/user_input_test.go`](../../../../engine/user_input_test.go)
- ordered durable queue/restart/edit:
  [`engine/p30_4_queued_prompt_test.go`](../../../../engine/p30_4_queued_prompt_test.go)
- ghost render/accept/cancellation/stale-result fencing:
  [`composer_suggestion_test.go`](../../../../internal/tui/composer_suggestion_test.go)
- Git capture, hunk isolation, selection fencing and attachment lifecycle:
  [`workspace_diff_context_test.go`](../../../../engine/workspace_diff_context_test.go),
  [`diff_context_test.go`](../../../../engine/commands/diff_context_test.go),
  [`composer_diff_context_test.go`](../../../../internal/tui/composer_diff_context_test.go)
- real CLI file/output context, manager removal and provider-payload boundary:
  [`composer_context_pty_unix_test.go`](../../../../scripts/e2e/composer_context_pty_unix_test.go)

The cross-entrypoint program and remaining ACP work are owned by
[`P30 Cross-Entrypoint Multimodal Input`](../../../migration/plans/p30-cross-entrypoint-multimodal-input.md).
