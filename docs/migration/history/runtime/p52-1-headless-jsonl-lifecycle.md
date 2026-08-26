# P52.1 Versioned Headless JSONL Lifecycle

**Status:** historical
**Completed:** 2026-08-24
**Adoption:** `adapt`

> **Ownership:** completed delivery narrative for the bounded headless
> lifecycle stream; current behavior belongs to entrypoint architecture

## Outcome

P52.1 added `jsonl` to `yhc exec --output-format` and the shared `-p`
compatibility route. A new active `engine/transport` projector emits only
validated committed canonical assistant/tool facts plus a small closed set of
renderer-neutral boundaries. Identity is bounded, canonical tool payloads use
the existing engine redaction owner, and invalid payloads fail closed.

The adapter drains the engine event channel before writing one classified
result. It suppresses the engine terminal event so a writable stream has one
terminal owner. Existing text and single-object JSON output, QueryEngine,
ProjectGraph, Session persistence, ACP, and permissions are unchanged.

## Delivery boundary

This slice serves bounded scripts, CI, and process hosts. It adds no daemon,
SDK, reconnect/replay protocol, business-record refresh contract, or new
approval path, and the later AppServer protocol remains a separate long-lived
Desktop transport. DeepSeek Harness's cancelled-tool settlement remains a
separate candidate, and Pi's generic `AgentHarness` remains non-production
evidence at the audited snapshot.

The retained [contract](../../plans/p52-1-headless-jsonl-lifecycle.md) owns the
compatibility boundary. The
[verification record](../../verification/p52-1-headless-jsonl-lifecycle.md)
owns reproducible evidence.
