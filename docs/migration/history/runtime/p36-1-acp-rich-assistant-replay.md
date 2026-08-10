# P36.1 ACP Provider-Rich Assistant Replay

**Status:** historical
**Closed gaps:** G20
**Completed:** 2026-08-01
**Adoption:** `combine`

> **Ownership:** completion record for the P36.1 ACP v1 provider-rich
> assistant replay slice. Current behavior belongs in
> [`acp-adapter.md`](../../../architecture/platform/acp-adapter.md); the
> compatibility-retained contract is
> [`p36-acp-assistant-replay.md`](../../plans/p36-acp-assistant-replay.md).

## Outcome

ACP v1 can reopen the provider-rich assistant shape produced by the current
Agentic output path without exposing its private continuation material. The
existing immutable Session replay snapshot now carries a separate public
assistant presentation only after validating the complete durable message.
The ACP projector emits every persisted text part, including empty text
parts, in order under one logical assistant message ID. Reasoning-only
messages emit no empty fallback chunk.

Only text and reasoning output parts are accepted. Text parts are closed
unions, reasoning payloads must be present, and the exact concatenation of
all text bytes must equal `Message.Content`. Image, audio, video, unknown,
mixed, nil, and mismatched shapes fail the complete snapshot before the first
wire update. Diagnostics use stable reason codes and bounded indexes without
content, raw provider type, signature, metadata, or transcript path.

## Ownership And Lifecycle

[`SessionReplayAssistantPresentation`](../../../../engine/session/replay_snapshot.go#L114)
owns the public-only immutable projection, while
[`replayAssistantPresentation`](../../../../engine/session/replay_snapshot.go#L442)
owns complete shape and byte validation. The durable message clone still
contains `ReasoningContent`, reasoning text/signature, output-part `Extra`,
and message metadata; none of those fields is copied into the presentation.

[`buildACPReplayProjection`](../../../../server/acp/replay.go#L41) consumes
only the public projection and builds the complete update list before
    delivery. The existing [`LoadSession`](../../../../server/acp/agent.go#L2528)
path still restores a separate unregistered QueryEngine from the transcript,
delivers replay/configuration/mode/commands, commits staging, registers the
Session, starts hooks, and only then responds. Load runs no model call and
does not rewrite the transcript. A later prompt sees the complete private
fields in restored model context. `session/resume` remains no-replay.

## Verification Evidence

Focused Session and ACP fixtures cover ordered text bytes and one logical ID,
empty persisted text parts, reasoning-only messages with and without tool
calls, restored continuation context, durable immutability, rollback-mode ID
omission, privacy, and fail-before-first-update image/audio/video/unknown/
nil/mixed/mismatch cases. Existing ordinary assistant, rich-user, tool,
delivery-failure, active-conflict, Resume, and lifecycle suites remain the
compatibility baseline. The complete Session/ACP race suite passed with an
explicit 20-minute package timeout; the ACP package required 700.719 seconds,
so Go's 10-minute default is shorter than the current full race baseline.

The official `@agentclientprotocol/sdk@1.3.0` real-program harness generated
the rich assistant through a deterministic Agentic OpenAI Responses fixture,
closed the Session, loaded it through the candidate binary, and observed the
exact six persisted text chunks:

```text
"" -> "sdk-provider-rich-one " -> "" -> "" ->
"sdk-provider-rich-two" -> ""
```

The rollback-mode wire omitted assistant message IDs, exposed no reasoning,
signature, thought chunk, or provider metadata, and completed a subsequent
provider request.

A real Zed `1.13.1+stable.332.00bd72e7838f4b875a913cd112b47a0ebe1ca62b`
restart used the exact Darwin arm64 candidate binary with SHA-256
`1afaee30c3b362db693f1b0ebb4e9cd72e6a9051061265692a62d55ea57f7227`, an
isolated project, Zed data directory, Agent home, and deterministic provider.
After a complete application exit, the method trace was exactly
`initialize -> session/load -> session/prompt`; no `session/resume` occurred.
Load delivered the same six text parts under one assistant ID before its
response. Zed rendered `zed-p36-public-one zed-p36-public-two`, exposed
neither `zed-p36-private-reasoning` nor `zed-p36-private-signature`, and the
post-restart prompt rendered `zed-p36-continuation-ok`.

An independent runtime/privacy review found one empty-text-part boundary
error in the first candidate. The final implementation preserved those
parts, added the missing no-tool reasoning-only fixture, and passed the
reviewer's follow-up with no remaining finding.

## Adjacent Gap And Rollback

P36.1 preserves private continuation fields through Session restore and the
QueryEngine model-call boundary; it does not change provider-specific origin
markers or cross-provider signature policy. Current Agentic OpenAI transport
may still suppress restored reasoning/signature when its self-generated
origin marker was lost during canonical stream aggregation. That separately
reproduced boundary is tracked as G34 in
[`REMAINING.md`](../../REMAINING.md#verified-current-implementation-gaps).

Rollback removes only the public assistant presentation and restores the
pre-P36 blanket rich-assistant load rejection. It rewrites no durable data,
but would reopen G20 for provider-rich ACP conversations.
