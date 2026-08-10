# Permission Classifier Parity Audit

**Status:** reference-snapshot
**Assessed:** 2026-07-12
**Question:** How are auto-mode classifier prompts constructed and decisions
extracted, and what happens when output is malformed?
**Result:** completed on 2026-07-12; retained as pre-implementation evidence

> **Ownership:** snapshot comparison and accepted contract at the assessment
> boundary; current behavior lives in
> [`permissions.md`](../../../architecture/capabilities/permissions.md)

## 2026-07-26 revalidation

This file remains the 2026-07-12 acceptance snapshot; it is not current-source
authority. Current production does call `ClassifyToolUse` and strips thinking
regions, so the original “unwired” findings are historical. However, the
accepted transcript claim below did not survive current implementation:
`extractRecentContext` copies the last five non-empty message contents
regardless of role, and its focused test explicitly requires assistant prose.
Production also supplies the primary `ChatModel` rather than a separately
configured reviewer and has duplicated name allowlists.

The current facts, reproduced Bash/classifier gaps, cross-product evidence, and
new `combine` decision are in
[`auto-permission-review-audit.md`](../runtime/auto-permission-review-audit.md)
and
[`p22-auto-permission-review.md`](../../plans/p22-auto-permission-review.md).
Those documents supersede this snapshot for future design without rewriting
the historical 2026-07-12 contract.

## Scope

Reference evidence:

- `src/utils/permissions/classifierDecision.ts`;
- `src/utils/permissions/classifierShared.ts`;
- `src/utils/permissions/yoloClassifier.ts`;
- classifier call sites in the permission pipeline.

Go evidence:

- `engine/permission/classifier.go`;
- `engine/permission/handlers.go` and `interactive.go`;
- `engine/engine.go` permission wrapping;
- `engine/tool_execution.go` classifier lifecycle events;
- TUI and ACP classifier-status projections.

## Parity Matrix

| Behavior | Reference | Go runtime | Verdict | Observable consequence |
|---|---|---|---|---|
| Production auto-mode call | Invokes the classifier after rule/mode fast paths | No production caller constructs `ClassifierConfig` or calls `ClassifyToolUse` | Gap | `auto` mode falls through to the interactive permission prompt |
| Output contract | Structured `classify_result` tool schema or staged XML `<block>yes/no</block>` | One-stage text `<allow/>` or `<block/>` | Intentional adaptation | Cross-provider Eino support is simpler, but needs equivalent parser safety |
| Thinking isolation | Removes `<thinking>...` before parsing verdict tags | Searches the full response text | Security gap | An `<allow/>` tag only inside reasoning can be treated as approval |
| Malformed output | Missing tool use, invalid schema, or unparseable XML blocks for safety | Missing/ambiguous tags return deny | Verified foundation | Fail-closed behavior exists when the classifier is actually called |
| Model error | Returns a safe blocking/fallback result with diagnostics | Returns `ask` plus an error; handlers fall through to the user | Adapted, safe | Availability failure does not silently approve |
| Prompt policy | Template with additive/replacement allow, soft-deny, and environment sections | Supports allow, deny, environment lists in a compact custom prompt | Partial | Configuration fields exist but are never populated in production |
| Project instructions | Separate user-provided `<user_claude_md>` message with explicit trust label | Concatenated into the classifier system prompt | Gap | User-controlled project text has a less explicit trust boundary |
| Transcript | User text, queued commands, and assistant tool-use blocks; action is the final tool-use block | Last five text messages plus raw JSON input | Partial | Queued intent and prior tool lineage are missing; assistant prose gets excess influence |
| Two-stage decision | Fast allow path, then thinking stage for blocks; malformed output blocks | Single 256-token call | Deferred adaptation | Higher latency/false-positive tuning differs, but correctness does not require two stages initially |
| Speculation/cache | Runtime-integrated speculative checks and request reuse | Implemented primitives and LRU cache have no production owner | Gap | Dead code and status surfaces create a false maturity signal |
| TUI/ACP progress | Classifier lifecycle is observable | Reducers and projections exist | Unwired foundation | No `checking/completed` event is emitted in normal execution |

## Accepted Contract at Snapshot

The accepted classifier slice wired the existing one-stage adaptation before
adding two-stage complexity.

1. After explicit rules and safe fast paths, `ModeAuto` invokes one engine-owned
   classifier for every otherwise-ask tool use.
2. Emit `checking` before the side query and exactly one `completed` or
   `cleared` event on every exit.
3. Allow returns immediately; deny returns a classifier denial and updates
   denial tracking; model errors or `ask` fall back to the interactive callback.
4. Strip complete and unterminated `<thinking>` regions before parsing verdict
   tags. A verdict found only in thinking is unparseable and therefore denied.
5. Keep malformed or ambiguous output fail-closed.
6. Build classifier context from bounded user intent, queued commands, and
   assistant tool-use names/inputs; do not treat assistant prose as user intent.
7. Keep project instructions explicitly delimited and labeled as user-provided.
8. Leave speculative racing and caching disabled until the synchronous path has
   entrypoint and cancellation evidence.

## Focused Acceptance Tests

- safe allowlist performs no model call and emits no classifier status;
- allow emits `checking -> completed(allow)` and skips the TUI prompt;
- block emits `checking -> completed(deny)` and records denial state;
- model error emits `checking -> cleared` and reaches the interactive callback;
- `<thinking><allow/></thinking>` without an external verdict is denied;
- thinking containing a conflicting tag does not alter the external verdict;
- ambiguous and empty outputs remain denied;
- bounded transcript includes queued user intent and tool-use context while
  excluding assistant prose;
- TUI and ACP receive the same classifier lifecycle through engine events;
- cancellation ends the side query and clears progress without leaking a late
  approval.

## Outcome

The synchronous classifier was wired and hardened after the filesystem slice.
The one-stage Go/Eino adaptation and its security/event lifecycle were proven;
two-stage classification remains outside the completed contract unless future
measurements justify its latency.

## Implementation Status

Implemented on 2026-07-12 in the engine permission wrapper and
`engine/permission/classifier.go`. Auto mode now runs the synchronous
classifier, emits bounded lifecycle events, records denials, falls back to the
interactive callback on model errors, and strips thinking regions before
verdict parsing. Speculation, caching, and two-stage tuning remain deferred.
