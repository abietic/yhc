# P30.0 Multimodal Characterization And Owner Inventory

**Status:** verification
**Last verified:** 2026-07-30

> **Ownership:** reproducible P30.0 evidence for the current cross-entrypoint
> multimodal mismatch and the complete production owner inventory. This file
> does not claim that P30's target ordered input, media store, capability
> resolver, ACP rich input, or current-turn-safe recovery is implemented.

## Result

P30.0 proves the accepted premise without changing production behavior:

- the TUI retains an interleaved image placeholder range, but submits one
  flattened prompt plus a separate image slice;
- `newUserMessage` lowers that pair to one complete text part followed by all
  images, so the placeholder position is not provider part order;
- ACP preserves Text/ResourceLink fallback order without dereferencing the
  resource and rejects image, audio, and embedded content before engine,
  transcript, session-registry, or model mutation;
- two images that satisfy the current TUI per-image and aggregate limits can
  produce one transcript JSONL record larger than the `LoadFull` 8 MiB scanner
  budget;
- the TUI's current model-capability lookup is fail-open when engine, model
  name, or registry-row facts are missing; and
- media-size recovery applies the same strip transform to historical and
  current-turn media. The current Query path can then complete against the
  stripped request while preserving the caller's source message.

The test-only `p30UntrustedPart` and `p30AdmittedPart` interfaces compile as
distinct ordered unions. Their private marker methods prevent accidental
interchange. They are defined only in `_test.go` and are not a public or
production API.

## Production Owner Inventory

Inline means that the current owner retains base64 bytes inside the named
message or JSON record. No session-private `MediaRef` or media store exists.

| Boundary | Production owner and supported reachability | Current media behavior |
|---|---|---|
| TUI draft | [`App.addComposerImage`, `composerSubmissionPrompt`, and `composerSubmissionImages`](../../../internal/tui/composer_elements.go) are TUI-only. | Placeholder ranges retain visual order in memory. Submission returns one flattened string and a separate `[]UserImage`; image base64 stays inline. |
| Idle turn admission | [`QueryEngine.SubmitMessageWithImages`](../../../engine/engine.go), [`validateUserImages`](../../../engine/user_image_admission.go), and [`newUserMessage`](../../../engine/user_input.go) are reached by the idle TUI leader and the public Go engine API. Plain, ordinary headless, sub-Agent, and ACP production callers use text-only `SubmitMessage`. | P30.1b copies the caller slice, validates strict base64, supported content/MIME equality, exact terminal structure, complete decode, count/byte/pixel limits, and single-frame GIF before mutation. One complete text part still precedes every image; no selected-route capability check runs here. |
| Busy queue projection | [`QueryEngine.EnqueueUserInput`](../../../engine/queued_input.go) projects TUI busy-submit snapshots into [`RuntimeUserPrompt`](../../../engine/input_coordinator.go). | `Prompt` and `[]UserImage` remain separate and image base64 is copied inline. P30.1b applies the shared validator and clears caller `Name`/`Path` provenance. |
| Runtime-input writer and reader | [`RuntimeInputCoordinator.EnqueueBounded`, `persistLocked`, and recovery construction](../../../engine/input_coordinator.go) own the session/thread/Agent-scoped `*.runtime-inputs.json` ledger used below the QueryEngine safe-point path. | JSON persists admitted `RuntimeUserPrompt.Images` inline under a protected `0600` replace. P30.1b applies the shared validator on admission and recovery and removes caller `Name`/`Path`; the record still has no ordered part identity or media ref. |
| Transcript writers | [`Recorder.Record`, `RecordMessages`, `Replace`, and `RecordLifecycleBoundary`](../../../engine/transcript/persist.go) are reached by QueryEngine turns, checkpoints, restore, child delivery, and session operations. | Complete Eino messages, including base64 multipart fields, are JSON-encoded inline. |
| Transcript readers | [`Recorder.LoadFull`](../../../engine/transcript/persist.go) is the canonical full replay reader used by resume, branch, export, and ACP load staging. Bounded message-page readers serve child inspection separately. | `LoadFull` configures an 8 MiB scanner token limit; an oversized otherwise valid record becomes corruption and is not replayed. |
| Branch and fork | [`SessionService.CreateFork`](../../../engine/session_service.go) delegates to [`session.BranchSession`](../../../engine/session/branch.go), which calls [`Recorder.BranchWithState`](../../../engine/transcript/persist.go). TUI session actions and ACP unstable fork reach this owner. | The selected transcript prefix is decoded and re-encoded into the child with inline media. No blob reachability or copy owner exists. |
| Delete | [`session.DeleteSession`](../../../engine/session/delete.go) is reached by the session service, ACP inactive deletion, and bulk deletion. | It preflights and removes the transcript, temp file, runtime-input sidecar, and ProjectGraph sidecar. There is no media root, manifest, or blob preflight. |
| Human-readable export | [`SessionService.Export`](../../../engine/session_service.go) delegates to [`session.ExportSession`](../../../engine/session/export.go) for active or durable sessions. | Markdown and JSON exports serialize `Message.Content`, tool calls, and metadata; multipart media is not represented. |
| ACP migration token | [`Agent.ExportSession`](../../../server/acp/streaming.go) is ACP-specific and exports session identity, CWD, timestamps, count, model, and checksum. Import reopens the local transcript. | The token contains no message or media bytes and is not a portable media export. |
| ACP prompt ingress | [`Agent.Prompt` and `promptInputFromACP`](../../../server/acp/agent.go) are ACP-only and submit the rendered fallback through text-only `SubmitMessage`. | Text and ResourceLink preserve order in one deterministic string; ResourceLink is not fetched. Image/audio/embedded blocks fail before QueryEngine submission. There is no typed rich-input production path. |
| Capability lookup | [`App.currentModelSupportsImages`](../../../internal/tui/composer_elements.go) guards TUI image paste and send. | Known registry rows use `SupportsMedia`; nil engine, empty model name, and missing row all return supported. Plain/headless/ACP and the public engine image API do not consume this lookup. |
| Recovery and compaction | P30.3 superseded the P30.1a production owner while retaining it as rollback: canonical after-model reconciliation now reaches [`handleMediaSizeFailure`](../../../engine/media_recovery.go), which requires exact prompt-record turn identity and one bounded lifecycle commit before retry. [`compact.TryReactiveCompact`](../../../engine/compact/reactive.go) retains the P30.0 test-only evidence of why undifferentiated stripping is unsafe, but is not a production media retry. | Only recorder-proved historical images can become in-position markers; current-turn media stays canonical and any derivative is attempt-local. The sequence is bounded to original, one selected-route recovery call, and one freshly admitted fallback. |
| Provider lowering | [`agenticChatModel.Generate`, `Stream`, and messagesToAgentic`](../../../engine/provider/provider.go) are the P25.1 Eino Agentic leaf for configured Agentic providers. | Existing ordered Eino multipart parts lower losslessly or fail before the inner provider call. This leaf cannot recover order already flattened by ingress. |

## Ownership Overlaps

- P23 remains the owner of ACP capability advertisement, block decoding,
  structured errors, session mutation ordering, and replay. P30.0 calls that
  production path and adds no ACP capability.
- P25.1 remains the provider-leaf conversion owner. P30.1b superseded
  structural direct/durable image admission with one strict engine validator
  but did not change provider lowering.
- P29 remains the accepted future owner of configured model profiles,
  capability provenance, and route selection. P30.0 only characterizes the
  current static TUI lookup and does not introduce a P29 schema.

## Reproduction

The focused characterization commands are:

```bash
go test ./engine -run 'TestSubmitMessageWithImages|TestQueryMediaSize|TestP30'
go test ./engine/transcript -run 'Test.*P30|Test.*Image|Test.*Record'
go test ./engine/session -run 'Test.*P30|Test.*Branch|Test.*Delete|Test.*Export'
go test ./internal/tui -run 'Test.*P30|Test.*Composer.*Image|Test.*ModelSupportsImages'
go test ./server/acp -run 'Test.*P30|Test.*Prompt'
```

The fixtures are:

| Claim | Fixture |
|---|---|
| TUI placeholder order versus split handoff | [`TestP300ComposerImageDraftOrderDiffersFromEngineSubmissionShape`](../../../internal/tui/composer_elements_test.go) |
| Flattened engine part order | [`TestP300FlattenedPromptPrecedesAllImages`](../../../engine/user_input_test.go) |
| Non-interchangeable test-only target types | [`TestP300TargetTypesKeepUntrustedAndAdmittedPartsDistinctAndOrdered`](../../../engine/p30_characterization_test.go) |
| Known/unknown capability behavior | [`TestP300CurrentModelSupportsImagesCharacterizesMissingFactsAsFailOpen`](../../../internal/tui/composer_elements_test.go) |
| ACP ordered fallback and no fetch | [`TestP23H1P300PromptPreservesResourceLinkOnlyAndMixedOrderWithoutFetch`](../../../server/acp/agent_capability_truth_test.go) |
| ACP rich zero-mutation rejection | [`TestP23H1P300PromptRejectsUnsupportedRichContentBeforeMutation`](../../../server/acp/agent_capability_truth_test.go) |
| Legal TUI record exceeds reader budget | [`TestP300TUILegalImageTurnExceedsTranscriptScannerBudget`](../../../engine/transcript/persist_test.go) |
| P30.0 unsafe strip characterization and current P30.1a fail-closed production behavior | [`TestP300MediaRecoveryStripsHistoricalAndCurrentTurnWithoutMutatingSource`](../../../engine/query_overflow_recovery_test.go), [`TestQueryMediaSizeFirstFailureReturnsImageError`](../../../engine/query_overflow_recovery_test.go), and [`TestP301aMediaSizeHistoricalOnlyFailsClosed`](../../../engine/query_overflow_recovery_test.go) |

## Evidence Limit

Passing P30.0 proves the mismatch and its baseline ownership graph. P30.1a
later superseded the production recovery row, and P30.1b superseded structural
legacy image admission and caller path/name propagation. The retained strip
fixture still proves why trusted turn identity is required. P30.0 does not
prove a target ordered API, selected-route compatibility, ref-backed replay,
portable export, or lossless recovery. P30.1c-P30.6 remain queued under root
[`PLAN.md`](../PLAN.md).
