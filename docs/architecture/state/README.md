# State Architecture

**Status:** current
**Last verified:** 2026-08-07

> **Ownership:** change-oriented index for sessions, transcripts, large results, and memory files

Use this group when changing durable conversation state, replay/resume, large tool-result storage, or model-visible memory files. State stores have different authorities and retention rules.

## Change routes

| Change | Start here | Required cross-check |
|---|---|---|
| Session listing, metadata, resume | [sessions](sessions.md) | [transcripts](transcripts.md), [runtime services](../platform/runtime-services.md) |
| WorkBoard recovery or owned-artifact cleanup | [sessions](sessions.md) | [tasks and agents](../runtime/tasks-and-agents.md), [entrypoints and transports](../platform/entrypoints-and-transports.md) |
| Durable messages/events, ordered prompt records, media references, and file snapshots | [transcripts](transcripts.md) | [query engine](../runtime/query-engine.md), [input queue](../runtime/input-queue.md) |
| Oversized tool-result persistence | [large tool results](large-tool-results.md) | [model and tool execution](../runtime/model-and-tool-execution.md) |
| Project/user memory prompt files | [memory directory](memory-directory.md) | [context assembly](../runtime/context-assembly.md), [runtime services](../platform/runtime-services.md) |

## Authority boundaries

- Prompt history in `engine/history` is a TUI navigation feature; it is not the session transcript.
- The active runtime appends file-state records through the engine cache and
  transcript recorder. Complete state-checkpoint repair preserves and validates
  the selected file-state projection, so resume can reconstruct the cache after
  a repaired tail. This is recovery, not rollback: the disconnected
  [`FileTracker`](../../../engine/filehistory/tracker.go) is still not
  constructed or exposed by `QueryEngine`, and `/rewind` remains unavailable.
- Ordered rich prompts persist versioned prompt records plus opaque references
  into a session-private media store. Transcript/session services own replay,
  fork, export, retention, and exact artifact cleanup; runtime snapshots are
  only bounded live projections.
- Large tool results are stored out of band and referenced from the conversation; they are not another transcript authority.
- Long-session memory extraction writes context inputs for future turns. It does not rewrite transcript history.

When changing state, define the writer, reader, path/identity key, retention policy, resume behavior, and failure semantics before changing the schema.
