# DeepSeek Responses Eino Adapter Audit

**Status:** reference-snapshot
**Last verified:** 2026-08-24

> **Ownership:** this report records comparative evidence and the adoption
> decision for the DeepSeek Eino adapter. Current behavior belongs to
> [`engine/provider/agenticdeepseek`](../../../../engine/provider/agenticdeepseek/model.go),
> provider wiring belongs to [`engine/provider`](../../../../engine/provider/provider.go), and
> exact model capability policy belongs to
> [`engine/model`](../../../../engine/model/capabilities.go). This report does not schedule work.

## Observable question

How should YHC expose current DeepSeek agent, reasoning, tool, streaming, and
vision behavior through Eino without inheriting the old adapter's OpenAI Chat
Completions wire contract?

The success criterion is observable rather than package-based: one Eino
`AgenticModel` must emit the current DeepSeek request shape, preserve ordered
history and tool correlation, parse DeepSeek semantic SSE, publish only proven
exact-model capabilities, and reject unsupported image or protocol shapes
before they can silently degrade.

## Evidence

### Existing Eino surface

The former dependency,
[`eino-ext/components/model/agenticdeepseek` v0.1.0](https://github.com/cloudwego/eino-ext/tree/components/model/agenticdeepseek/v0.1.0/components/model/agenticdeepseek),
has the useful public shape: `Config`, `New`, `Generate`, `Stream`, `GetType`,
and `IsCallbacksEnabled`. Tools are supplied per request through Eino
`model.WithTools`, which avoids mutable model-level binding.

Its transport is not DeepSeek-owned. The model wraps Eino's OpenAI ACL, which
converts Agentic messages to OpenAI Chat Completions and delegates request,
stream, error, and response semantics to an OpenAI-compatible client. That
shape cannot express Responses items, semantic response events, image
`file_id`, image-bearing tool output, or `detail=original` as first-class
DeepSeek contracts.

### Current DeepSeek platform

- DeepSeek documents a dedicated
  [Responses API](https://api-docs.deepseek.com/zh-cn/guides/responses_api/)
  at `/responses`. It is stateless: callers send complete history rather than
  chaining `previous_response_id` or `store`.
- Streaming uses semantic SSE with increasing `sequence_number` and ends in
  `response.completed`, `response.incomplete`, or `response.failed`; it does
  not use the Chat Completions `[DONE]` marker.
- The 2026-08-21
  [`deepseek-v4-flash-vision-exp` release](https://api-docs.deepseek.com/zh-cn/news/news260821/)
  retains V4 Flash text, agent, reasoning, and tool capabilities while adding
  vision. DeepSeek documents Chat Completions, Messages, and Responses access.
- The [vision contract](https://api-docs.deepseek.com/zh-cn/guides/vision/)
  accepts JPEG, PNG, GIF, and WebP through an HTTP(S) URL, base64 data URL, or
  Files API `file_id`. Responses additionally permits images in function or
  custom-tool output. Only the exact vision model consumes images.
- Current [model metadata](https://api-docs.deepseek.com/zh-cn/quick_start/pricing)
  lists V4 Flash and V4 Pro with a 1M context and 384K maximum output. The
  `deepseek-chat` and `deepseek-reasoner` compatibility names passed their
  announced 2026-07-24 deprecation date.

## Adoption decision

| Concern | Decision | Reason |
|---|---|---|
| Eino `AgenticModel` and constructor shape | `preserve` | Existing callers need stable Generate/Stream, callback, and per-call tool behavior. |
| Old Config names that have exact equivalents | `adapt` | `MaxTokens` maps to Responses `max_output_tokens`; typed Responses fields are added without ambiguous dual values. |
| OpenAI Chat Completions ACL transport | `reject` | Its message, `[DONE]` stream, error, image, and reasoning shapes are the defect source. |
| Project-owned DeepSeek `/responses` codec | `adapt` | It expresses the official contract while retaining the Eino boundary. |
| Ordered full-history replay | `preserve` | Responses is stateless and tool/reasoning continuation depends on exact correlation and order. |
| Exact vision admission | `combine` | Reuse YHC's canonical ordered media bridge, then enforce DeepSeek model and image limits in the adapter. |
| Eino representation of Files API image IDs | `project-native` | Typed block constructors carry the missing `file_id` through reserved adapter metadata without changing Eino globally. |
| Automatic protocol fallback or model downgrade | `reject` | A successful request with weaker semantics would publish a false capability. |
| Files upload lifecycle | `defer` | This slice accepts existing `file_id` values but does not own upload/storage credentials or lifecycle. |
| DeepSeek server web search and custom `apply_patch` tools | `defer` | YHC currently dispatches client function tools; server-tool product semantics need a separate owner and acceptance contract. |

The aggregate recommendation is **`adapt`**: retain Eino's immutable
AgenticModel-facing interface, replace the transport and codec with a dedicated
DeepSeek Responses implementation, and publish vision only for the exact model
whose wire behavior is covered.

## Accepted implementation contract

The project-owned adapter:

- accepts an HTTP(S) API root without userinfo, query, or fragment and appends
  `/responses` while preserving an explicit path prefix;
- owns bearer authentication, bounded request/response/error bodies, typed
  conversion/protocol/API/transport errors, and redacted error formatting;
- maps system, user, assistant reasoning/text/function calls, function results,
  function tools, tool choice, structured text, user isolation, token usage,
  and reasoning effort without emitting Chat Completions fields;
- preserves text/image order, supports URL/base64/`file_id` image input and
  image-bearing function output, and fails locally for a non-vision model;
- parses multiline semantic SSE and keep-alive comments, requires increasing
  sequence numbers and a terminal response event, and retains terminal usage
  and incomplete/failure metadata;
- exposes DeepSeek terminal metadata through Eino `ResponseMeta.Extension`,
  which the existing classic-message bridge projects into canonical finish
  reasons; and
- changes the provider default to `deepseek-v4-flash`, adds the exact vision
  model to the catalog/registry, and publishes its image, tool, streaming, and
  reasoning capabilities through the existing provider/model intersection.

## Verification boundary

Local HTTP fixtures cover the actual `/responses` request body, URL and base64
vision input, Files API IDs, image-bearing tool results, function tools and
choice, non-stream output/usage, semantic SSE reasoning/text/tool deltas,
terminal usage, typed provider failures, cancellation, request-size and image
admission, malformed/truncated streams, and rejection of Chat Completions
`[DONE]`.

These fixtures prove local conversion and transport behavior. They do not
prove current account entitlement, provider availability, billing, external
image fetch behavior, or physical UI presentation. A live API-key canary and
interactive image acceptance remain separate evidence.
