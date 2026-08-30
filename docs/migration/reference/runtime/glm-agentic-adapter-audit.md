# GLM Agentic Eino Adapter Audit

**Status:** reference-snapshot
**Last verified:** 2026-08-30
**Snapshot:** Eino Ext `16ddf563ec68c4202994b67d86b574a2125cbb70` and current GLM official documentation

> **Ownership:** comparative evidence and the adoption decision for GLM's Eino
> adapter. Current runtime behavior belongs to
> [`model-providers.md`](../../../architecture/platform/model-providers.md);
> this snapshot does not schedule future work.

## Observable question

Can YHC use an existing Eino Ext GLM agentic SDK for `glm-5.3-flash`, and if
not, what provider-owned wire contract preserves Eino compatibility without
claiming OpenAI behavior as GLM behavior?

Success means a production-reachable Eino `AgenticModel` can use the exact
model's reasoning, tools, streaming, image/video/file input, and Files API with
bounded failures and without a silent protocol or model downgrade.

## Evidence

| Source | Observed fact | Consequence |
|---|---|---|
| [Eino Ext provider tree at `16ddf563`](https://github.com/cloudwego/eino-ext/tree/16ddf563ec68c4202994b67d86b574a2125cbb70/components/model) | No `agenticglm`, Zhipu, or Z.ai provider package exists. | There is no upstream GLM AgenticModel to import. |
| [Eino Ext Agentic DeepSeek](https://github.com/cloudwego/eino-ext/blob/16ddf563ec68c4202994b67d86b574a2125cbb70/components/model/agenticdeepseek/model.go) and [Agentic Qwen](https://github.com/cloudwego/eino-ext/blob/16ddf563ec68c4202994b67d86b574a2125cbb70/components/model/agenticqwen/model.go) | Both wrap the Eino OpenAI ACL and use Chat Completions compatibility. | Their public Eino shape is reusable evidence, but their transport does not own GLM-specific semantics. |
| [GLM-5.3-Flash model guide](https://docs.bigmodel.cn/cn/guide/models/vlm/glm-5.3-flash) | Exact model ID `glm-5.3-flash`; text/image/video/file input; 1M context; 128K maximum output; thinking, reasoning effort, tools, and tool streaming. | Capability publication and request validation must be exact-model scoped. |
| [GLM OpenAPI](https://docs.bigmodel.cn/openapi/openapi.json) | Chat Completion messages include provider fields such as `reasoning_content`, `thinking`, `reasoning_effort`, and multimodal file objects; streaming ends with `[DONE]`. | A direct codec can preserve GLM history and terminal semantics without importing OpenAI types. |
| [GLM Files upload](https://docs.bigmodel.cn/api-reference/%E6%96%87%E4%BB%B6-api/%E4%B8%8A%E4%BC%A0%E6%96%87%E4%BB%B6), [list](https://docs.bigmodel.cn/api-reference/%E6%96%87%E4%BB%B6-api/%E6%96%87%E4%BB%B6%E5%88%97%E8%A1%A8), and [delete](https://docs.bigmodel.cn/api-reference/%E6%96%87%E4%BB%B6-api/%E5%88%A0%E9%99%A4%E6%96%87%E4%BB%B6) | Bearer-authenticated `/files` distinguishes `agent` resources from reusable `user_data` files and documents a separate format set for each purpose. The content endpoint is batch-only and is outside this client. | A separate bounded resource client must preserve purpose and format; Chat Completions `file_id` uses `user_data`, not an Agent API resource. |

## Adoption decision

| Concern | Decision | Reason |
|---|---|---|
| Eino `Config`, `New`, immutable `AgenticModel`, callbacks, and per-call tools | `preserve` | This is the useful caller-facing compatibility surface. |
| OpenAI SDK or Eino OpenAI ACL transport | `reject` | Compatible paths do not own GLM reasoning history, exact multimodal bounds, `tool_stream`, or provider error semantics. |
| Direct GLM Chat Completion codec | `project-native` | The official endpoint is Chat Completions-shaped, but provider-specific fields and validation remain GLM-owned. |
| Exact `glm-5.3-flash` capability admission | `project-native` | The requested model has a new multimodal contract that must not leak to unknown GLM IDs. |
| Historical `reasoning_content` plus `clear_thinking=false` | `preserve` | Multi-turn tool loops require provider reasoning history, and the model guide recommends preserved thinking. |
| Forced tool choice and multimodal tool results | `reject` | The exact-model documentation does not establish those semantics; local rejection is safer than an inferred downgrade. |
| Files resource lifecycle | `project-native` | Eino message types can carry a typed file ID, but no Eino Ext GLM client owns upload/list/delete. Batch-only content download is excluded. |

The aggregate decision is **`project-native`**: retain the Eino Ext-style
public interface, but implement and test GLM's official HTTP, SSE, reasoning,
multimodal, tool, error, and file-resource contracts directly.

## Verification boundary

Local HTTP fixtures prove direct request bodies, authorization, exact-model and
reasoning validation, preserved assistant reasoning, tools, usage, semantic
stream chunks, required `[DONE]` and finish reason, typed redacted errors,
multimodal conversion, and the Files lifecycle. A live provider comparison
confirmed that an `agent` text resource fails Chat Completions parsing with
provider code `1210`, while a `user_data` DOCX completes the exact
upload/list/model/delete lifecycle. `make test-glm-live` retains that external
canary for current account entitlement and provider behavior; it does not run
in ordinary repository verification.
