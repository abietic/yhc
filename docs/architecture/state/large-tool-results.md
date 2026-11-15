# Large Tool-Result Storage

**Status:** current
**Persistence:** the lookup index is process-local
**Last verified:** 2026-11-16

> **Ownership:** This file owns large tool-result offload and retrieval. JSONL
> conversation authority belongs in [`transcripts.md`](transcripts.md);
> file-state snapshots used by resume are a separate transcript feature.

## Current Flow

`QueryEngine` creates one `storage.ResultStorage` rooted at
`<transcript-dir>/<session-id>/tool_results`. Tool execution calls
`maybeOffloadToolResult`; results above the storage threshold are written to a
UUID-named text file and replaced in the model-visible tool result by a bounded
preview containing the concrete file path.

Bash needs an earlier retention point because its own bounded preview can be
smaller than the generic storage threshold. `executeToolCall` binds the current
session's `ToolOutputPersister`; Bash saves complete captured output before
creating a head/tail preview. The result keeps its exit, timeout or cancellation
marker and adds a `Full output saved to:` path. The model can inspect omitted
diagnostics through ordinary Read, under the existing permission checks, without
running the command again. Background Bash retains the same receipt when
BashOutput filters or consumes its preview.

`StoreComplete` uses the existing session directory and metadata index, bypasses
only the inline-size threshold, and limits each retained output to 64 MiB. It
creates a private, exclusive file and refuses linked or writable-by-others
output directories. Incomplete captures, storage failures and execution
boundaries without a qualified readable path keep the original bounded preview
with an explicit unavailable notice. Contained execution does not receive host
storage paths. This adds no aggregate disk quota, model call or transcript-index
reconstruction.

```mermaid
flowchart LR
    accTitle: Large tool-result offload
    accDescr: Generic large results are offloaded after execution. Bash can retain complete output before its own preview. Both use session files and a process-local index.
    Tool["tool output"] --> Check["ResultStorage.ShouldStore"]
    Bash["Bash captured output"] --> Retain["StoreComplete before truncation"]
    Retain --> File
    Check -->|small| Inline["inline result"]
    Check -->|large| File["tool_results/<uuid>.txt"]
    File --> Index["in-memory StoredResult map"]
    File --> Preview["persisted-output preview"]
    Preview --> Transcript["tool result recorded in transcript"]
```

## Persistence Boundary

The content files survive process exit, but the `results` map is initialized
empty by `NewResultStorage`. `Retrieve`, `GetPreview`, `Cleanup`, and `Stats`
resolve only entries present in that in-memory map.

On session resume, the engine constructs a new `ResultStorage` for the resumed
session directory. It does **not** scan `tool_results`, persist a metadata index,
or reconstruct stored-result IDs. The transcript preview still contains the
saved path, so a model or user may read the file through ordinary filesystem
tools, but `ResultStorage.Retrieve(id)` cannot retrieve an old ID after process
resume unless it was re-indexed in the current process (which production code
does not do).

## APIs

| API | Behavior |
|---|---|
| `ShouldStore` | compares result length with the configured character threshold |
| `Store` | creates the directory, writes the full result, builds a preview, and indexes metadata in memory |
| `StoreComplete` | retains bounded pre-preview output as a private, exclusive file, even below the generic threshold |
| `Retrieve` | looks up the ID in memory, then reads the indexed file path |
| `Cleanup` | removes indexed files older than the requested age |
| `Stats` | summarizes only indexed entries |
| `ToolResultHandler` | alternate token-estimate wrapper; canonical tool execution calls `maybeOffloadToolResult` directly |

If file persistence fails, `maybeOffloadToolResult` returns the original result;
it does not silently replace it with an incomplete preview.

## Code References

| Symbol | Evidence |
|---|---|
| storage state and constructor | [`engine/storage/persistence.go`](../../../engine/storage/persistence.go), [`engine/storage/persistence.go`](../../../engine/storage/persistence.go) |
| store and retrieve | [`engine/storage/persistence.go`](../../../engine/storage/persistence.go), [`engine/storage/persistence.go`](../../../engine/storage/persistence.go) |
| production offload call | [`engine/tool_execution.go`](../../../engine/tool_execution.go), [`engine/tool_execution.go`](../../../engine/tool_execution.go) |
| pre-preview output owner and Bash receipts | [`tools/output_storage.go`](../../../tools/output_storage.go), [`tools/bash.go`](../../../tools/bash.go) |
| engine-owned instance | [`engine/engine.go`](../../../engine/engine.go) |
| fresh store on resume | [`engine/engine.go`](../../../engine/engine.go) |
| alternate handler | [`engine/storage/integration.go`](../../../engine/storage/integration.go) |

## Example

```text
<persisted-output>
Output too large (84KB). Full output saved to: .../tool_results/<id>.txt
Preview (first 2KB):
...
</persisted-output>
```

The path is durable text in the transcript; the `<id>` lookup entry is not
reconstructed across process resume.
