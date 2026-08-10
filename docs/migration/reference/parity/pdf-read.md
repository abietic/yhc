# PDF Read Parity Audit

**Status:** reference-snapshot
**Last verified:** 2026-07-12
**Result:** P8 adaptation complete at this snapshot

> **Ownership:** This report owns the reference comparison and accepted Go/Eino
> adaptation for P8 PDF reads. Current implementation belongs in
> [`architecture/capabilities/tool-registry.md`](../../../architecture/capabilities/tool-registry.md);
> closeout evidence belongs in
> [`migration/history/runtime/p1-p8.md`](../../history/runtime/p1-p8.md).

## Observable Question

What does the model receive when `Read` targets a PDF, how are page ranges and
resource limits enforced, and how does the runtime recover when native PDF or
extractable text is unavailable?

## Reference Contract

| Behavior | Reference evidence | Observable result |
|---|---|---|
| Input | `FileReadTool.ts:inputSchema`, `pdfUtils.ts:parsePDFPageRange` | `pages` accepts one page or an inclusive 1-indexed range; malformed, inverted, zero, and ranges over 20 pages fail before execution. |
| Full-read guard | `FileReadTool.ts:readFileWithDependencies` | When `pdfinfo` reports more than 10 pages, a read without `pages` fails and instructs the model to request a smaller range. |
| Native path | `pdf.ts:readPDF`, `FileReadTool.ts` | Supported models receive a validated base64 PDF document block; empty files, invalid magic, and files over 20 MB fail clearly. |
| Render path | `pdf.ts:extractPDFPages` | Explicit ranges and unsupported native models use `pdftoppm` at 100 DPI; direct JPEG pages are sorted and attached to the next model turn. |
| Extraction limit | `constants/apiLimits.ts` | Page rendering rejects PDFs over 100 MB and one Read call is capped at 20 pages. |
| Tool result ordering | `services/tools/toolExecution.ts` | The textual tool result is emitted first, then tool-provided supplemental messages, then remaining hook results. |
| Failure classification | `pdf.ts` | Empty, too-large, password-protected, corrupt, unavailable, and unknown failures produce actionable messages; command timeouts are bounded. |
| Prompt | `FileReadTool/prompt.ts` | The model is told to use `pages` for PDFs over 10 pages and that one request is limited to 20 pages. |

The reference does not perform OCR or local text extraction. Its preferred
small-PDF path relies on Anthropic-compatible native document blocks, while the
range path relies on Poppler-rendered images.

## Pre-Slice Go State

| Area | Evidence | Classification |
|---|---|---|
| Extension handling | `tools/read.go:isPDFExtension` | `.pdf` bypasses binary rejection. |
| Model-visible result | `tools/read.go:ReadTool` | Raw PDF bytes are converted to a string and line-numbered; the result is unusable and may pollute context. |
| Page input and limits | `tools/read.go` schema | Missing. |
| Page count, extraction, and rendering | No production symbols | Missing. |
| Supplemental media result | `engine/tool_execution.go` | User attachments exist in the query loop, but ordinary tools cannot emit them. |

## Accepted Go/Eino Adaptation

1. Preserve the reference `pages` grammar, 20-page per-call cap, 10-page
   implicit-read threshold, 100 MB extraction cap, PDF magic validation, and
   bounded Poppler command execution.
2. Prefer `pdftotext` for provider-neutral model-visible text. Preserve page
   boundaries and identify the selected range in the tool result.
3. If text extraction is unavailable or returns no text, use `pdftoppm` to
   render bounded JPEG pages. Attach base64 images only when the configured
   model capability says image input is supported; otherwise return an
   actionable dependency/capability error.
4. Add an engine-owned supplemental-message callback to tool execution context.
   Successful Read media appears after the textual tool result in both streamed
   and non-streamed execution, matching reference ordering without changing the
   public `ToolExecutor` signature.
5. Keep rendered files temporary: read and validate them, inject immutable
   base64 snapshots, then remove the directory. Reject an individual rendered
   image above the existing raw-image safety target.
6. Fail closed when `pdfinfo` cannot determine an implicit full-read page count.
   Explicit bounded ranges may proceed because the caller has already limited
   work.

Native PDF file blocks are intentionally not the default: the Go runtime routes
across Anthropic, OpenAI, Gemini, Qwen, DeepSeek, and other adapters whose file
part support is not uniform. Text-first with image fallback gives one stable
model-visible contract.

## Acceptance Criteria

- PDF bytes are never returned as line-numbered text.
- Page parsing covers single, closed, open-ended, malformed, inverted, zero,
  and over-limit ranges; open-ended ranges fail the 20-page cap.
- Empty, oversized, invalid-magic, password-protected, corrupt, unavailable,
  timeout, and cancellation paths are deterministic and actionable.
- A known PDF over 10 pages requires `pages`; a bounded explicit range executes
  without requiring page-count discovery.
- Extractable text is returned with page/range metadata and no media attachment.
- Empty/unavailable text extraction falls back to sorted JPEG attachments only
  for image-capable models; images respect count and raw-size limits.
- Supplemental messages follow the tool result and survive streamed and
  non-streamed query execution, transcript serialization, and compaction image
  stripping.
- Focused tests, race tests, all Makefile gates, manifest validation, and diff
  checks pass.

## Non-Goals

- OCR for scanned page images;
- password prompts or PDF mutation;
- recursive or persistent render caches;
- provider-specific native document blocks without a verified common adapter
  contract;
- broad redesign of image reads outside the accepted PDF slice.

## Completion Evidence

- `tools/pdf.go` owns strict page parsing, file validation, bounded Poppler
  execution, page counting, text formatting, numeric JPEG ordering, image-size
  guards, and temporary cleanup.
- `tools/read.go` exposes `pages`, direct/context execution, and PDF dispatch;
  `tools/tool_prompts.go` owns model guidance.
- `tools/registry.go`, `engine/engine.go`, and `engine/tool_execution.go` carry
  model media capability and close the supplemental-message collector at tool
  return. Tool attachments follow the textual result and are discarded on
  failure.
- `tools/pdf_test.go` covers parsing, limits, extraction, fallback, dependency
  failures, cancellation/timeout, bounded buffers, numeric ordering, cleanup,
  and a real Poppler smoke test when the utilities are installed.
- `engine/tool_attachment_test.go` covers success/failure/late-emission
  semantics, streamed ordering, and model capability propagation.
- Focused race tests and all repository Makefile, manifest, and diff gates pass.
