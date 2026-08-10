# Structured Composer Contract

**Status:** current

**Last verified:** 2026-08-07

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
7. Paste, image, file, and MCP-resource payloads are individually bounded by
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

## Ordered Submission

[`captureComposerSubmission`](../../../../internal/tui/composer_submission.go)
performs one stable rune walk over the validated draft:

1. text before each element remains in place;
2. paste placeholders expand in place;
3. images become `engine.UntrustedPromptImage` parts at the placeholder
   position;
4. file, skill, and MCP placeholders remain visible while their existing
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
| Busy leader | `QueryEngine.EnqueuePromptInput` | Persist ref-backed ordered queue input |
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

The cross-entrypoint program and remaining ACP work are owned by
[`P30 Cross-Entrypoint Multimodal Input`](../../../migration/plans/p30-cross-entrypoint-multimodal-input.md).
