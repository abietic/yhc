# P25 Agentic Provider Input Fidelity

**Status:** historical
**Closed gaps:** G22
**Completed:** 2026-07-26
**Last verified:** 2026-07-26

> **Ownership:** completed P25.1 `adapt` decision, delivery boundary,
> verification evidence, compatibility consequences, and rollback for the
> classic-message to Eino Agentic user-input bridge. Current behavior belongs
> in [`model-providers.md`](../../../architecture/platform/model-providers.md).

## Outcome

P25.1 closed the deterministic user-input loss at the Agentic provider leaf.
Every accepted classic user text, image, audio, video, and file/PDF part now
reaches the inner `AgenticModel` in exact message and part order. Invalid or
unrepresentable multipart input returns a typed redacted error before the
inner model is called; no reduced request is sent.

The wider runtime still owns classic `schema.Message`, ProjectGraph traversal,
transcripts, runtime events, retry/fallback, and durable input coordination.
P25.1 introduced no dependency upgrade, provider-specific fallback, public
upload surface, durable schema, or runtime-owner cutover.

## Delivered Contract

The provider bridge now applies these rules:

| Input | Delivered behavior |
|---|---|
| Empty multipart | Preserve the existing single text block from `Message.Content`. |
| Non-empty multipart | Treat the ordered parts as the sole content source; do not append `Content` again. |
| Image/audio/video/file URL | Preserve the exact URL and supported typed fields. |
| Image/audio/video/file base64 | Preserve exact bytes and MIME type without decode or rewrite. |
| Image detail and file name | Preserve the typed field; PDF remains a file block with `application/pdf`. |
| Nil, mismatched, unknown, unsupported, ambiguous, or incomplete input | Return `AgenticInputConversionError` before option normalization or provider invocation. |

`AgenticInputConversionError` exposes bounded message/part indexes, role, part
type, and one stable reason code through `errors.As`. Its formatted text omits
user text, URLs, base64 data, names, paths, arbitrary metadata, and
user-controlled role/type values. Historical assistant, system, tool, option,
streamed tool-call, and terminal-metadata conversion behavior remains
unchanged.

The direct and durable image boundaries also converged:

- `SubmitMessageWithImages` rejects missing base64 data or a blank MIME type
  synchronously with `TerminalImageError`, one terminal event, and no prompt
  hook, history, transcript, or model mutation.
- `RuntimeInputCoordinator` uses the same validation during enqueue and
  recovery, before delivered-ID or transcript-coverage short-circuits.
  Rejected enqueue leaves the durable ledger byte-identical.
- `newUserMessage` projects every admitted image and no longer silently skips
  an invalid one.

## Evidence

The implementation and focused fixtures are owned by:

- [`agenticChatModel`, `messagesToAgentic`, and the typed conversion error`](../../../../engine/provider/provider.go);
- [the exact Generate/Stream request-capture and negative matrix](../../../../engine/provider/user_input_conversion_test.go);
- [`validateUserImages` and `newUserMessage`](../../../../engine/user_input.go);
- [direct submission and transcript-boundary tests](../../../../engine/user_input_test.go);
- [`RuntimeInputCoordinator.validateItem`](../../../../engine/input_coordinator.go);
- [durable enqueue/recovery and byte-identity tests](../../../../engine/input_coordinator_test.go); and
- [public queued-input admission tests](../../../../engine/queued_input_test.go).

Closeout covered exact text and multipart precedence; URL/base64 image, audio,
video, and file/PDF mappings; consecutive messages; project-metadata
exclusion; all stable failure codes; zero inner calls for `Generate` and
`Stream`; no retry/fallback classification; direct and durable image
admission; provider regression tests; canonical ProjectGraph traces; focused
race tests; and the repository, documentation, manifest, scanner, and diff
gates.

## Compatibility And Rollback

The change is one source-compatible bridge correction. Existing classic
messages, transcripts, runtime-input JSON, sessions, provider configuration,
and ProjectGraph checkpoints require no migration. Providers may still return
an explicit remote capability error for a modality they do not support.

Rollback is the single P25.1 squash commit. Reverting it restores the previous
lossy bridge and silent invalid-image behavior without a data migration; it
does not change the last safe owners for runtime state or provider routing.

## Current Replacements

- Current provider behavior:
  [`model-providers.md`](../../../architecture/platform/model-providers.md)
- Current query and durable-input ownership:
  [`query-engine.md`](../../../architecture/runtime/query-engine.md)
- Verified product state:
  [`STATUS.md`](../../STATUS.md)
- Future execution selection:
  [`PLAN.md`](../../PLAN.md)
