# Sessions and Transcripts

**Status:** current
**Last verified:** 2026-08-10

> **Ownership:** durable conversation locations and supported new, clear,
> compact, resume, fork, and inspection workflows

## What is persisted

Every `QueryEngine` gets a session ID. With the CLI defaults, its JSONL
transcript is:

```text
<project>/.yhc/transcripts/<session-id>.jsonl
```

The transcript stores an append-only message audit plus metadata, file/replacement
state, and lifecycle boundaries used by resume. Normal turns append only new
messages. `/clear` and `/compact` select a new active model context without
deleting earlier JSONL records. The CLI prints a resume hint on exit.

The user-level session-root catalog is separate:

```text
~/.yhc/session-roots.json
```

Override only the catalog location with `YHC_SESSION_CATALOG`; this does
not relocate project transcripts.

The default picker also discovers rows registered in the archived
`~/.eino-agent/session-roots.json`, but those rows are read-only. YHC never
appends to, repairs, or registers a legacy transcript in place. An explicit
catalog override is exact and disables this legacy union.

## Resume from the same project

```bash
go run ./cmd/yhc resume SESSION_ID
go run ./cmd/yhc --plain --resume SESSION_ID
go run ./cmd/yhc exec --resume SESSION_ID "continue with the next check"
```

`resume SESSION_ID` starts the TUI. Root `--resume` composes with root modes;
`exec --resume` is the canonical non-interactive form.
Direct resume resolves the ID across catalogued transcript roots for the
current project. Run it from the project that owns the transcript; duplicate
IDs in different roots fail before mutation rather than choosing one
arbitrarily. Use `/sessions` for recent sessions in the current project.

If `SESSION_ID` belongs only to the archived legacy catalog, non-interactive
and protocol entrypoints return `legacy_session_import_required` before model,
provider, MCP, hook, WorkBoard, recorder, or `QueryEngine` initialization. To
copy one attested stable bundle and then start the TUI, run:

```bash
go run ./cmd/yhc resume SESSION_ID --confirm-legacy-stopped
```

Use this flag only after stopping the archived producer. The import copies the
legacy transcript and its exact WorkBoard files into `.yhc`, verifies the
project marker, journal, file hashes/modes, and canonical catalog entry, and
then resumes through the ordinary canonical path. It never deletes or merges
later writes from `.eino-agent`.

For provider-free inspection or lifecycle administration that exits without a
TUI or model turn, use:

```bash
go run ./cmd/yhc sessions list --limit 20
go run ./cmd/yhc sessions list --search "plan mode" --cursor CURSOR
go run ./cmd/yhc sessions resume SESSION_ID
go run ./cmd/yhc sessions rename SESSION_ID "release investigation"
go run ./cmd/yhc sessions export SESSION_ID report.md
go run ./cmd/yhc sessions fork SESSION_ID review-alternative
go run ./cmd/yhc sessions --output-format json list
go run ./cmd/yhc migrate-state inspect --owner session --session SESSION_ID
go run ./cmd/yhc migrate-state apply --owner session --session SESSION_ID --confirm-legacy-stopped
```

`sessions resume` restores and reports the selected durable identity, then
exits. Use `exec --resume` when the goal is to submit another prompt. All five
administration commands reuse the same engine-owned `SessionService`; they do
not resolve a provider, enter a ProjectGraph turn, or activate MCP, skills,
hooks, settings watchers, worktree recovery, Agent replay, or long-lived
services. Resume/fork activation validates the durable kernel and preserves
the target session's canonical restore checkpoint; close adds neither a second
checkpoint nor a synthetic source transcript. Text/JSON results use exit `0`/`1`/`2`/`130` for
success/runtime/usage/cancellation. Archive and delete are intentionally absent.

`sessions resume` reports `legacy_session_import_required` and does not import.
Use the explicit `migrate-state` form when the goal is to import without
initializing a model/runtime. A partial or tampered canonical bundle remains
unresumable until the recoverable import transaction completes successfully.

Resume restores messages and recorded execution context when still valid. If a
recorded worktree or working directory no longer exists, the engine falls back
and emits warnings. In-process callbacks such as a pending permission dialog
cannot be recreated after a process restart.

Inside TUI/plain/headless, direct slash resume is:

```text
/resume SESSION_ID
```

ACP protocol resume/load/fork remains supported, but ACP slash `/resume` and
`/fork` are rejected before mutation because the protocol host owns its
session-handle mapping. ACP slash `/sessions` is also unavailable; use the
explicit ACP List/Load/Resume/Fork protocol operations.

## Start, clear, and compact

Use `/new` when you want a different durable session:

```text
/new
```

The current transcript remains unchanged. A new session ID, query-kernel pin,
execution context, and empty active conversation are committed before the
engine switches. If persistence fails, the source identity stays active.
If the operating system reports a sync failure after accepting bytes, the
result is explicitly indeterminate rather than reported as a clean rollback;
restart replay is the authority for the visible session.

Use `/clear` when you want an empty context but the same session identity:

```text
/clear
```

This appends a reset boundary. It is not an erase or privacy purge: earlier
prompts, outputs, tool data, and paths remain in the JSONL audit.

Use `/compact [instructions]` to replace active context with a durable summary:

```text
/compact preserve decisions and unresolved risks
```

Compaction appends one compact boundary, and restart restores the same compacted
context. Earlier records remain auditable.

## Session discovery and fork

Use the canonical session surface:

```text
/sessions
/sessions list 20
/sessions search "plan mode" 10
/sessions resume SESSION_ID
/sessions rename SESSION_ID "release investigation"
/sessions export SESSION_ID report.md
```

Use `current` instead of an ID for current-session rename or export. Export
reads the persisted active transcript, includes tool calls and metadata, and
writes Markdown relative to the active CWD unless an absolute path is supplied.
It does not export pre-clear/pre-compact audit records.

The old hidden `/history`, `/rename`, and `/export` compatibility shortcuts are
removed. Use canonical `/sessions ...` interactively or
`yhc sessions ...` for provider-free process administration.

The no-argument TUI `/resume` opens the repository-scoped, searchable,
cursor-paginated picker. Its list and selected-source resume use the same
`SessionService` as direct and startup resume; the picker owns presentation
only.

`/fork [name]` is the accepted lineage command:

```text
/fork review-alternative
```

The engine first commits a complete child JSONL containing the active messages,
replacement/file state, lineage, operation identity, branch name, and child
execution metadata. The same-directory child install is no-clobber and becomes
visible only after its contents are synced. The source transcript is not
rewritten. TUI/plain/headless activate the child only after commit; an
activation failure compensates the operation-owned child. Repeating the same
typed operation resolves to the same child instead of creating another one.
ACP provides the equivalent outcome through its explicit Fork operation and
registers a new handle only after durable child restore.

Fork preserves the Plan phase and return context in child metadata and binds
the child to its own Plan path. It does not copy the source session's external
Plan Markdown file; write child-specific Plan content before exiting Plan mode
when the forked workflow needs it.

`/branch`, `/undo`, `/redo`, `/rewrite`, and `/rewind` are unavailable before
mutation; do not treat fork as a reversible recovery workflow.

## Prompt history is not the transcript

The TUI writes submitted prompt text to both:

```text
~/.claude/history.jsonl
<project>/.eino-agent/history
```

The first is project-filtered JSONL with paste-store support; the second is a
legacy project-local compatibility file. Plain and headless entrypoints do not
use the TUI history writer. None of these prompt-history files is the session
transcript.

## Privacy and retention

Transcripts can contain prompts, model output, tool inputs/results, file paths,
prior cleared/compacted context, and session metadata. Prompt history can
contain expanded pasted text. Keep
`.eino-agent/` out of version control, protect project directories, and delete
all related stores when a complete local purge is required. Current transcript
and legacy-history creation uses ordinary `0644` file modes; do not assume they
are private on a shared machine solely because their directory is hidden.

## Maintainer reference

| Concern | Source |
|---|---|
| Transcript recorder | [`persist.go`](../../engine/transcript/persist.go) |
| New-session commit | [`session_lifecycle.go`](../../engine/session_lifecycle.go) |
| Resume and context restore | [`session_restore.go`](../../engine/session_restore.go), [`resume.go`](../../engine/session/resume.go) |
| Session command service | [`session_service.go`](../../engine/session_service.go), [`cmd_sessions.go`](../../engine/commands/cmd_sessions.go) |
| Provider-free sessions CLI | [`sessions.go`](../../cmd/yhc/cmd/sessions.go), [`session_administration.go`](../../engine/session_administration.go) |
| Durable fork commit | [`branch.go`](../../engine/session/branch.go), [`persist.go`](../../engine/transcript/persist.go) |
| Session-root catalog | [`catalog.go`](../../engine/session/catalog.go) |
| TUI prompt history | [`history.go`](../../internal/tui/history.go), [`history.go`](../../engine/history/history.go) |
| Architecture | [State](../architecture/state/README.md) |
