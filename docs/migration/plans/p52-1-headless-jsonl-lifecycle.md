# P52.1 Versioned Headless JSONL Lifecycle

**Status:** historical
**Accepted:** 2026-08-24
**Completed:** 2026-08-24
**Adoption:** `adapt`

> **Ownership:** retained compatibility contract for the bounded headless
> lifecycle stream; current behavior belongs to entrypoint architecture and
> reproducible results to the verification record

## Outcome

`yhc exec --output-format jsonl` exposes committed engine lifecycle facts as
newline-delimited, versioned records and closes every writable stream with
one classified process result. Existing text and single-object JSON output do
not change. The compatibility `yhc -p` route uses the same headless owner.

This is the first accepted implementation from the 2026-08-24 Codex platform,
DeepSeek Harness, and Pi audit. It solves bounded script and CI observation;
it does not introduce a daemon, SDK, second Session store, or second agent
loop.

## Intake evidence

Before this slice, headless JSON exposed only a final object even though
`QueryEngine` already published ordered event identities and an engine-owned
canonical assistant/tool projection. ACP consumed part of that lifecycle, but
its wire and Session ownership are IDE-specific. `engine/transport` compiled
outside the released CLI closure.

OpenAI's
[Codex as a platform](https://developers.openai.com/blog/codex-as-a-platform)
distinguishes bounded `exec`, programmatic SDK, and persistent app-server
integration. DeepSeek Harness demonstrates committed event observation and
ordered settlement; Pi demonstrates a smaller host-owned Session/runtime.
Those references justify a stable outbound seam, not another runtime owner.

## Observable contract

1. `exec` and the `-p` compatibility route accept `text`, `json`, or `jsonl`.
   Text and JSON retain their prior bytes and exit classification.
2. Each JSONL line is one `schema_version: 1` record whose `type` is `event`
   or `result`. Event and result payloads are mutually exclusive.
3. Outward identity is limited to Session, thread, turn, sequence, UTC
   timestamp, and causation ID. Once turn admission creates an event emitter,
   its terminal identity closes the result. A failure after engine creation but
   before turn admission has no valid turn or sequence to invent, so its result
   carries only the already-created Session ID. Usage/configuration failures
   before engine creation carry no runtime identity. Provider secrets,
   prompts, paths, Goal state, and mutable engine objects are not added by the
   transport.
4. Assistant and tool records originate only from a validated clone of the
   engine canonical projection. Tool input/output therefore uses the existing
   engine redaction boundary. Legacy assistant/tool events are skipped rather
   than duplicated.
5. Supported non-canonical events are command result, compaction boundary,
   max-turn boundary, and user interruption. Unknown events are omitted from
   version 1.
6. The engine terminal event is not emitted separately. After the event
   channel drains, exactly one `result` closes a writable stream with status,
   exit code, terminal reason, available runtime identity, complete output, and
   an optional sanitized error. Turn ordering identity is present only after
   turn admission.
7. Explicit assistant text appears both as deltas and complete result output.
   This is intentional for stream consumers that need either incremental or
   final output, and does not expose more assistant content than existing
   headless modes.
8. Invalid canonical payloads and invalid UTF-8 fail closed. Concurrent
   record writes cannot interleave.
9. Headless permission behavior remains non-interactive: the stream cannot
   authorize a tool or fabricate a Plan decision.

## Schema and ownership

| Record | Stable version-1 payload | Owner |
|---|---|---|
| `event` | bounded identity, kind, family, and one kind-specific payload | `engine/transport.ProjectLifecycleEvent` |
| `result` | available terminal identity, status, output, Session ID, reason, exit code, sanitized error | headless process adapter |
| canonical assistant/tool payload | committed delta or engine-redacted tool lifecycle | `engine.CanonicalProjectionEvent` |
| process exit | success, failure, cancellation, usage, and max-turn classification | existing headless classifier |

The writer serializes complete records, but it does not reorder engine events.
The embedded event sequence remains the ordering authority.

## Non-goals and deferred candidates

- No app-server, HTTP, JSON-RPC, or in-process public Host Session API.
- No replay cursor, reconnect, acknowledgement, or long-lived subscriber.
- No ACP schema change and no expansion of permission authority.
- No adoption of DeepSeek Harness's Cordis composition or experimental Agent
  Team.
- No claim that Pi's generic `AgentHarness` is production-ready; the pinned
  implementation remains an explicit scaffold.
- Cancelled-tool settlement is retained as a separate candidate because its
  non-cooperative-tool deadline and unknown-outcome semantics are not frozen.

## Verification and rollback

Focused tests cover projection validation and UTF-8 rejection, duplicate
terminal exclusion, one final result, safe error redaction, output-format
compatibility, and a public `exec` run through a loopback Responses provider,
real tool permission decisions, and an actual Write. Repository-owned focused
and committed-tree gates remain the completion authority.

Rollback removes the `jsonl` option, headless observer, and active transport
projection together. It leaves the pre-existing canonical engine projection,
ACP consumer, text/JSON modes, Session schema, transcript, and permissions
unchanged.
