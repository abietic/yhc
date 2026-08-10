# Cross-Entrypoint Multimodal Input Contract Audit

**Status:** reference-snapshot
**Assessed:** 2026-07-27
**Current Eino-Agent:** `cfe2bc1a04a9`
**Promotion refreshes:** P30.0 on 2026-07-29 at Eino-Agent
`4afa0f17507a831b85a2f972aa1a04deadf32a4a`; P30.4 on 2026-07-30 at
Eino-Agent `679db70451614789847239b172794495befd9bf2`; P30.5a on
2026-07-30 at Eino-Agent `84dacd98fd65bbbca181274ba7548f1e484c5880`;
P30.5b on 2026-07-30 at Eino-Agent
`2ca5a63f51c180e946a08ad5df3bbd1a8d271b3e`; P30.6 on 2026-07-30 at
Eino-Agent `5a5cef21ac961284ac59198a74fcf2f811eec01d`
**Local reference snapshots:** OpenCode `411eff73f026`, Claude Code Ripe
`4b9d30f79532`, Crush `2af939d8e900`, Codex `66bd101fff6f`

> **Ownership:** comparative evidence and the adoption recommendation for G32.
> Current implementation facts belong in the architecture documents, the
> unresolved mismatch belongs in [`REMAINING.md`](../../REMAINING.md), and the
> accepted target and slice order belong in
> [`p30-cross-entrypoint-multimodal-input.md`](../../plans/p30-cross-entrypoint-multimodal-input.md).

## P30.0 Promotion Refresh (2026-07-29)

The `combine` recommendation remains valid, but P23.H1 invalidated one original
premise before P30.0 promotion. The 2026-07-27 snapshot below correctly
describes `cfe2bc1a04a9`; it must not be read as the current ACP boundary.

Current source at `4afa0f17507a831b85a2f972aa1a04deadf32a4a` establishes:

| Boundary | Current verified behavior | P30.0 characterization |
|---|---|---|
| TUI to engine | [`composerSubmissionPrompt`](../../../../internal/tui/composer_elements.go) returns one expanded text string, [`composerSubmissionImages`](../../../../internal/tui/composer_elements.go) returns a separate ordered image slice, and [`newUserMessage`](../../../../engine/user_input.go) emits the complete text before all images | Pin a placeholder-interleaved draft whose submitted Eino parts are text then images |
| ACP | [`promptInputFromACP`](../../../../server/acp/agent.go) preserves ordered Text/ResourceLink fallback text without fetching the resource; image, audio, and embedded resource blocks return explicit unsupported errors before `SubmitMessage` | Pin exact fallback bytes/order, no-fetch behavior, and zero-mutation rich rejection; do not claim silent drop |
| Public image admission | [`validateUserImages`](../../../../engine/user_input.go) checks only non-empty data and MIME, while [`currentModelSupportsImages`](../../../../internal/tui/composer_elements.go) returns true when engine/model/registry facts are missing | Pin structural-only admission and the current missing-fact fail-open cases |
| Durability | [`Recorder.RecordMessages`](../../../../engine/transcript/persist.go) writes inline multipart base64 to one JSONL record, while `LoadFull` retains an 8 MiB scanner-record ceiling | Prove a currently legal image produces a complete record larger than the reader budget |
| Recovery | [`sanitizeReactiveMessage`](../../../../engine/compact/reactive.go) strips media from candidate messages without distinguishing the current turn, and the current query test expects a successful retry | Pin current-versus-historical behavior and source-message immutability without endorsing the semantic downgrade |

Direct reference revalidation also preserved the selected mechanisms:

- OpenCode
  `packages/opencode/src/acp/content.ts::promptContentToParts` maps content in
  source order. Its empty result for unknown/invalid blocks remains rejected
  because Eino-Agent requires an explicit whole-turn error.
- Claude Code Ripe `src/utils/imageStore.ts::storeImage` creates the file with
  mode `0600` and calls `datasync`; `validateImagesForAPI` enforces the API
  base64-size boundary. P30 adapts the privacy and explicit-validation
  properties, not its cleanup owner.
- Crush `internal/proto/message.go::UnmarshalParts` restores an explicitly
  tagged `BinaryContent`, and unknown tags fail. P30 adapts the durable tagged
  union while replacing inline bytes with session-private refs.
- Codex `codex-rs/protocol/src/user_input.rs::UserInput` keeps text, remote
  image, local image, audio, and local audio non-interchangeable;
  `prepare_response_items` applies bounded image preparation without changing
  item order. P30 adapts typed ordering and preparation, not the service
  protocol or placeholder-on-error policy.

P30.0 is therefore a behavior-preserving characterization slice. It must
compile the proposed untrusted/admitted type separation only in tests, produce
the complete current owner inventory, and update the plan again if any fixture
cannot reproduce from production wiring.

## P30.4 Promotion Refresh (2026-07-30)

The earlier snapshot remains the program-level evidence for P30's `combine`
decision. Completed P30.1b-P30.3 have since supplied strict admission, ordered
prompt types, private refs, queue/restart durability, lifecycle projections,
and current-turn-safe recovery. This refresh asks one narrower question:

> How should the supported TUI leader path project one immutable ordered
> text/image input onto those owners while retaining a rejected draft and
> keeping bytes and paths out of submitted history and the runtime ledger?

### Current production boundary

At Eino-Agent `679db70451614789847239b172794495befd9bf2`:

| Boundary | Verified behavior | Consequence |
|---|---|---|
| Composer order | `threadComposerElement` retains image ranges, but `composerSubmissionPrompt` returns one string and `composerSubmissionImages` returns a separate slice; `SubmitMessageWithImages` emits all text before all images | Placeholder order remains presentation-only |
| Idle settlement | `sendMessage` appends the user row and calls `clearInputAfterSubmit` before the asynchronous command invokes engine admission | A route/admission/persistence rejection loses the editable draft and creates optimistic history |
| Busy settlement | `enqueueLeaderInput` clears only after `EnqueueUserInput` succeeds, but its local preview copies `UserImage` base64 and source-bearing composer elements instead of using the durable record as truth | The saved ledger is ref-only while the TUI keeps a second byte/path-bearing dispatch snapshot |
| Capability | `currentModelSupportsImages` returns true for a nil engine, blank model, or missing registry row | Missing facts advertise support even though P30.1c correctly rejects unknown selected-route capability |
| Async identity | `composerImageLoadedMsg` carries bytes/path/MIME but no request, thread, or draft generation | A stale path/clipboard result can mutate a newer draft or another thread |
| History | `recordComposerHistory`, `AppendUserWithComposer`, undo, and thread snapshots clone full composer elements; persistent recall expands images to `[Image file: <path>]` | Submitted history retains base64/path-bearing state and may present an already removed clipboard path |
| Restart/edit | the engine can reload a ref-backed pending prompt, but the TUI preview is process-local and `/queue edit` restores that preview after a boolean cancel | Restart cannot reconstruct the exact UI edit source from the authoritative record; claim/edit races have no materialization transaction |
| Clipboard | Darwin/Linux/Windows helpers execute native commands; Darwin and Windows use one fixed temp filename and production code has no injectable platform fixture | Concurrent calls, cleanup, deadline, output bounds, and cross-platform behavior are not deterministic proof |

P30.4 therefore cannot be a composer-only range fix. The acceptance boundary,
draft generation, queue materialization transaction, history projection, and
clipboard adapter are part of the same observable integrity contract.

### Direct reference revalidation

The local reference snapshots remain Codex `66bd101fff6f`, Claude Code Ripe
`4b9d30f79532`, and Crush `2af939d8e900`.

| Reference mechanism | Useful evidence | Rejected boundary |
|---|---|---|
| Codex `submit_user_message_with_history_and_shell_escape_policy` and `restore_blocked_image_submission` | Unknown/unsupported image capability rejects the turn and reconstructs the draft; history persistence follows `submit_op` acceptance | TUI submission still constructs remote images, local images, then text rather than placeholder interleaving; local rich history retains image paths |
| Claude Code Ripe `popAllEditable` and queued-command attachments | Cancelling editable queued work can restore its images and text as one user-facing draft | Queue entries retain `PastedContent` bytes and build text followed by images; this is not a private ref-backed ledger |
| Crush `UI.sendMessage` and editor Enter handling | Go/Bubble Tea attachments and asynchronous run acceptance are implementation-fit evidence | The editor and attachment list reset before `AgentRun` returns acceptance; attachments are a separate ordered argument rather than interleaved parts |

No selected reference supplies all required properties, and all three lose
placeholder interleaving. Codex and Claude provide useful rejection/restore
outcomes; their local history/queue representations would reintroduce the byte
and path duplication that P30.2 removed.

### Recommendation: `project-native`

P30.4 is `project-native` within the accepted P30 `combine` program:

- keep Bubble Tea App/composer ownership for the active draft;
- perform one project-owned rune-range walk into the existing
  `UntrustedPromptInput`;
- let QueryEngine remain the only generic/route admission owner;
- let MediaStore and `RuntimeInputCoordinator` remain the only durable
  byte/ref and queue owners;
- adapt exact rejected-draft restoration from Codex and editable-queue outcome
  from Claude Code Ripe; and
- reject grouped image/text submission, byte/path-bearing submitted history,
  and reset-before-acceptance behavior.

The observable result is one ordered leader turn, cleared only after exact
admission or enqueue acceptance. Busy preview and restart consume sanitized
engine projections; edit materializes the exact still-pending record from
private refs or changes nothing. Submitted history never stores or reopens
image bytes or paths. Agent-thread, command, shell, ACP, and other non-TUI
restrictions remain unchanged.

## P30.5a Promotion Refresh (2026-07-30)

P30.5a asks one narrower observable question:

> How should ACP v1 accept one ordered live Text, ResourceLink, image, or
> embedded-resource prompt through the existing QueryEngine and Session
> durability owners without fetching a URI, silently dropping a block, or
> advertising audio or rich replay that production cannot deliver?

### Current production and protocol boundary

At Eino-Agent `84dacd98fd65bbbca181274ba7548f1e484c5880`:

| Boundary | Verified behavior | P30.5a consequence |
|---|---|---|
| ACP baseline ingress | [`promptInputFromACP`](../../../../server/acp/agent.go) validates one exact SDK union variant per block, preserves Text/ResourceLink order through engine-owned `PromptInput`, and rejects image, audio, and embedded resources before Session lookup | Preserve this fail-before-lookup structural boundary and the current ResourceLink representation |
| ResourceLink | [`PromptInput.Render`](../../../../engine/user_input.go) emits one deterministic descriptor bounded to 16 KiB and never dereferences its URI | Carry the typed metadata through durability, then lower it to the same descriptor; do not replace an already supported baseline with a new provider error |
| Ordered rich admission | [`QueryEngine.SubmitPromptInput`](../../../../engine/engine.go) owns literal ordered text/image admission, strict image validation, selected-route capability truth, durable ref publication, and provider lowering | Extend this owner with protocol resource kinds; do not create an ACP-only model or persistence path |
| Capability truth | [`Agent.Initialize`](../../../../server/acp/agent.go) advertises `loadSession: true` after P23.4b and leaves image, embedded context, and audio false | P30.5a may turn on image and embedded context only with live new/resume and restart proof; audio remains false |
| ACP load | [`buildACPReplayUpdates`](../../../../server/acp/replay.go) rejects any rich replay before the first update, while resume restores without replay | Preserve truthful text load and explicit rich-load failure; P30.5b alone adds historical rich projection |
| Durable schema | P30.2's prompt record stores ordered text/image parts and Session-private refs, and its lifecycle owners copy, validate, branch, delete, export, and collect those refs | Add a strict backward-compatible resource-capable record version and extend the same lifecycle owners so P30.5b can recover original ACP kinds |

The current official ACP v1 initialization and content contracts still make
Text and ResourceLink baseline prompt content and gate image and embedded
resources behind independent advertised capabilities. Content remains ordered.
The pinned `github.com/coder/acp-go-sdk v0.13.5` exposes the same five-way
`ContentBlock` union, with embedded text/blob resource variants and optional
image URI. The SDK's generated wire types do not perform Eino-Agent's base64,
MIME, size, route, or durability admission.

### Reference comparison

| Alternative | Useful evidence | Rejected boundary |
|---|---|---|
| OpenCode `promptContentToParts` and ACP service | Preserves source order, advertises image/embedded context, and maps embedded text/blob into typed session parts | It opens local/`zed://` ResourceLinks, accepts remote image URLs, silently returns no part for unsupported blocks, and stores provider-facing file URLs; those authority and error semantics are not adopted |
| Codex typed user input and image preparation | Keeps text and images non-interchangeable through queueing and prepares images explicitly before provider use | Its app-server protocol and local-path authority are not ACP v1 or Eino-Agent Session contracts |
| Crush tagged binary parts | Proves strict tagged durable unions can retain binary identity across persistence | Inline binary payloads would violate P30's bounded ref-only transcript contract |
| Claude Code Ripe private image store | Provides private-file, flush, and early-size-validation evidence already adapted by P30.2 | It does not own ACP ResourceLink, embedded-resource, or Eino-Agent replay semantics |

### Recommendation: `project-native`

P30.5a is `project-native` within the accepted P30 `combine` program:

- preserve P23's exact union validation, structured errors, capability owner,
  and deterministic non-dereferencing ResourceLink descriptor;
- extend the existing project-owned untrusted/admitted prompt union and
  ref-backed prompt record instead of copying OpenCode's URI/file authority;
- lower embedded text to one versioned deterministic user-text envelope and
  embedded raster blobs to that envelope followed by one ordinary admitted
  image at the same logical position;
- keep selected-route truth in QueryEngine, so an advertised agent capability
  can still fail one turn after an unsupported or unknown model switch;
- keep `loadSession: true` for P23.4b text/tool replay, but retain its
  fail-before-first-update rich rejection until P30.5b; and
- reject audio, unknown unions, URI fetching, remote image URLs, arbitrary
  embedded blob types, ACP-only durable state, and any silent downgrade.

The observable result is exact live ACP block order through engine admission,
Session restart/resume, transcript identity, and provider projection, or one
bounded typed error before the new turn reaches transcript or model state.
ResourceLink remains a supported no-egress textual representation rather than
becoming filesystem, MCP, or network authority.

## P30.5b Promotion Refresh (2026-07-30)

P30.5b asks the remaining historical-user-content question:

> How should ACP v1 load recover the original logical ResourceLink, image, and
> embedded-resource blocks from ref-backed Session durability, deliver every
> block before response, and retain P23.4b restore staging without guessing
> from provider-shaped messages or creating a second replay owner?

### Current production and protocol boundary

At Eino-Agent `2ca5a63f51c180e946a08ad5df3bbd1a8d271b3e`:

| Boundary | Verified behavior | P30.5b consequence |
|---|---|---|
| Load lifecycle | `Agent.LoadSession` holds the lifecycle lock, rejects an active conflict, loads one immutable Session snapshot, builds the complete replay projection, prepares restore/MCP, awaits replay/state/commands, then commits, registers, starts hooks, and responds | Preserve this single owner and all-before-response ordering |
| Rich failure | `buildACPReplayProjection` rejects any message with rich provider content as `session.load.replay.richContent` before restore setup or the first update | Replace only exact prompt-record-backed user rejection; keep unsupported provider-rich failure explicit |
| Durable identity | Versioned prompt records retain original text, ResourceLink, image, embedded-text, embedded-blob, annotations, and private media refs, while materialized `schema.Message` contains provider-lowered text/image parts | Bind the exact record to the final active user message; never infer the logical ACP kind from `schema.Message` |
| Ref validation | Transcript `LoadFullContext` strictly materializes prompt records and private refs before Session constructs its replay snapshot | Extend that snapshot with cloned neutral logical parts; do not add an ACP transcript or MediaStore reader |
| Resume | `ResumeSession` restores context without replay, then publishes current state and commits/registers/hooks | Emit no historical rich updates on resume |

The current
[ACP v1 session setup contract](https://agentclientprotocol.com/protocol/v1/session-setup)
requires `session/load` to replay the complete conversation through
`session/update` before returning, while `session/resume` must not replay
history. The
[ACP v1 content contract](https://agentclientprotocol.com/protocol/v1/content)
uses the same Text/Image/Resource/ResourceLink union for prompts and
client-visible content. Those protocol facts require logical content
fidelity; they do not authorize source paths, URI fetching, private refs, or a
provider-shaped approximation.

### Direct reference revalidation

| Reference mechanism | Useful evidence | Rejected boundary |
|---|---|---|
| Official Codex ACP `loadSession` / `streamThreadHistory` at `ba5bef59cfcea4229841fe9438d816696621307b` | Reads durable turns, maps every item through one history projector, awaits each ordered update, and responds only afterward | Image history is degraded to textual URI/path links; Codex thread storage and app-server input are not Eino-Agent Session authority |
| Official Claude Agent ACP `loadSession` / `replaySessionHistory` / `toAcpNotifications` at `d7a65ce1d042a90d24a71279a319735cb9200bf8` | Reuses one live/replay content notification mapper, awaits every notification, preserves grouping, and keeps resume replay-free | Vendor SDK storage, CLI markers, filtering rules, and vendor message metadata are not adopted |
| Eino-Agent P23.4a-P23.4b | Already owns immutable replay identity, ordered text/tool projection, restore staging, delivery settlement, registration, hooks, and response | A parallel rich replay loop or post-update validation would split ownership and weaken failure truth |
| Eino-Agent P30.5a | Already owns strict typed durable identity and complete private-media lifecycle | Provider-lowered envelopes/images cannot recover original ResourceLink or embedded-resource kind |

Both live references support one content projector and awaited order, but
neither preserves Eino-Agent's required private ref-backed logical identity.
The protocol supplies the wire shape, not the persistence owner.

### Recommendation: `project-native`

P30.5b is `project-native` within the accepted P30 `combine` program:

- preserve P23.4a's immutable Session snapshot and P23.4b's sole load,
  delivery, staging, registration, hook, and response owner;
- extend each exact final-user replay item with a cloned neutral logical
  projection derived from its already validated versioned prompt record;
- let `server/acp` map those neutral parts back to exact ACP
  Text/ResourceLink/Image/Resource blocks;
- adapt the official Codex/Claude same-projector and awaited-order mechanisms;
  and
- reject provider-message inference, vendor stores, path/URI degradation,
  metadata leakage, URI authority, and a second transcript reader.

All prompt records and private refs must validate and materialize before the
first update. Images and embedded blobs cross the wire only as canonical
base64 plus safe MIME and accepted annotations; source URI, path, ref, ID, and
digest remain private.

Embedded blobs return as one logical ACP Resource, not the provider
envelope/image pair. Text/tool replay remains unchanged.
Unsupported or unbound rich user/assistant provider content retains the
explicit pre-update failure. Delivery failure aborts restore staging but does
not claim that already delivered client updates can be rolled back. Resume
remains replay-free.

## P30.6 Promotion Refresh (2026-07-30)

P30.6 asks only the program-closeout question:

> After P30.1a-P30.5b, which production writer or reader paths still violate
> the single untrusted/admitted/ref-backed owner model, and which missing
> deterministic proof prevents G32 from closing?

At Eino-Agent `5a5cef21ac961284ac59198a74fcf2f811eec01d`:

| Boundary | Verified current behavior | Closeout consequence |
|---|---|---|
| TUI composer | `beginComposerAdmission` sends one immutable `UntrustedPromptInput` to `SubmitPromptInput` or `EnqueuePromptInput`; clear occurs only after acceptance | Preserve as the only TUI rich writer |
| TUI compatibility helper | `startEngineRequest` alone calls the image/metadata helper chain, always with nil images and metadata; the branch retaining `SubmitMessageWithImages` is unreachable | Delete the alternate helpers and leave one direct text-only call |
| ACP live/load | `Agent.Prompt` constructs `UntrustedPromptInput`; Session owns exact logical replay and ACP only maps wire blocks | Preserve; no new writer or replay owner |
| Legacy immediate API | `SubmitMessageWithImages` validates legacy commands and converts non-command rich input to `UntrustedPromptInput` | Preserve source compatibility and the legacy command guard |
| Legacy queued API | `EnqueueUserInput` still constructs a durable prompt record directly from `[]UserImage`, bypassing selected-route typed admission | Delegate image-bearing compatibility input to `EnqueuePromptInput`; delete the direct writer |
| Generic runtime enqueue | `EnqueueBounded` and `EnqueueBatch` still accept newly supplied inline `RuntimeUserPrompt.Images`; JSON recovery also needs to read that legacy shape | Reject new inline rich enqueue while keeping decode/restart compatibility |
| Durable owners | Immediate transcript and sealed queued writers persist versioned prompt records containing Session-private refs; branch, export, delete, collection, recovery, and ACP load consume those owners | Preserve every delivered owner and record version |
| Proof | Focused tests cover admission, TUI queue/edit, restart/corruption, lifecycle, ACP new/load/resume, fallback, cancellation, and privacy, but no named rich-compaction closeout test, owner source gate, or P30 record/materialization benchmark exists | Add only those missing fixtures and current-gate evidence |

The independent read-only security/lifecycle review found no reachable TUI
terminal-capability fail-open path and no unresolved high-risk issue. It also
found no evidence for deleting `ValidateUntrustedPromptInputMetadata`,
`validateUserImages`, `SubmitMessageWithImages`, or terminal image-protocol
detection. Structural ACP metadata validation and engine media/route admission
have different timing and responsibilities; treating them as duplicates would
weaken fail-before-mutation behavior.

### Recommendation: `preserve`

Use `preserve` for P30.6 within the accepted P30 `combine` program:

- keep the current public APIs source-compatible, but force every new rich
  production write through `UntrustedPromptInput`;
- keep `AdmittedPromptInput` private and let only the sealed engine writer
  create ref-backed durable records;
- retain legacy inline fields exclusively for strict decode, restart,
  projection, and explicit compatibility failure;
- delete only the proved direct legacy queued writer and unreachable TUI
  alternate;
- add source-owner gates, rich compaction/restart proof, and bounded
  record/materialization benchmarks; and
- update current architecture and status only after implementation, then close
  G32 without claiming provider-rich assistant replay.

P30.6 does not need a new upstream comparison because it introduces no
capability or mechanism. Reference identity cannot justify broader deletion;
current source reachability and compatibility tests are the deciding evidence.

## Verdict

Eino-Agent has a partial image path, not a complete multimodal input
capability.

- The TUI can read a local or clipboard image, retain it in a draft and busy
  queue, and submit it to the leader `QueryEngine`.
- The engine can put text followed by images into Eino
  `MessageInputPart` values.
- P25.1 can lower ordered Eino text/image/audio/video/file parts into typed
  Agentic content blocks before the provider call.
- ACP currently reads only text blocks. It silently drops baseline
  `ResourceLink` and optional image, audio, and embedded-resource blocks.
- The TUI and engine do not preserve interleaving. They build one complete text
  part followed by every image, regardless of where an image placeholder was
  placed in the draft.
- Public image admission checks only that base64 and MIME strings are non-empty.
  It does not decode, sniff, bound, or capability-check media at the shared
  engine boundary.
- Durable transcript and runtime-input JSON include base64 inline. A legal TUI
  attachment can make one transcript record exceed the current 8 MiB scanner
  limit after JSON and base64 expansion.
- Media-size recovery is allowed to remove media from the current user turn and
  continue to an answer. The model can therefore answer without the input the
  user asked it to inspect.

The recommended adoption decision is **`combine`**: preserve the existing
QueryEngine, ProjectGraph, transcript, runtime-input, and provider owners;
adapt OpenCode's ordered ACP projection and shared media validation, Claude Code
Ripe's private durable image store, Crush's typed durable binary parts, and
Codex's typed user-input and normalization behavior behind non-interchangeable
project-owned untrusted and admitted ordered prompt contracts.

## Observable Question

For a user turn containing text and client-provided rich content:

1. Do TUI, ACP, durable queue, resume, and the actual model invocation preserve
   the same ordered parts?
2. Is every byte validated, capability-checked, durably referenced, and
   recoverable before it becomes model-visible?
3. Can unsupported or damaged content fail explicitly without mutating the
   conversation or silently answering a different prompt?

This audit covers user-originated prompt input. Tool-result media and
model-generated media use different trust, lifecycle, and presentation
boundaries and are not used to enlarge this scope.

## Current Source Evidence

### TUI ingress exists, but ordering is lost

[`threadComposerElement`](../../../../internal/tui/thread_view_state.go) retains
image MIME, base64 data, source path, and placeholder range.
[`composerSubmissionPrompt`](../../../../internal/tui/composer_elements.go)
expands paste and textual context into one string, while
[`composerSubmissionImages`](../../../../internal/tui/composer_elements.go)
returns a separate image slice.

[`newUserMessage`](../../../../engine/user_input.go) always emits:

```text
Text(the whole prompt), Image(1), Image(2), ...
```

For a draft such as `compare [Image #1] with this paragraph`, the model does
not receive `Text("compare "), Image(1), Text(" with this paragraph")`.
Placeholder geometry is therefore presentation state, not a lossless ordered
input representation.

The leader and busy queue retain images, but an Agent thread, slash command, or
shell command rejects them. Prompt-recall history degrades path-backed images
to text and drops clipboard-only image bytes. Those restrictions are current
behavior, not proof of a cross-entrypoint contract.

### Shared admission is structural only

[`validateUserImages`](../../../../engine/user_input.go) rejects only an empty
base64 string or blank MIME string. It does not:

- perform strict or canonical base64 decoding;
- compare declared MIME with detected bytes;
- reject unsupported or active formats such as SVG;
- inspect image dimensions, animation, or decompression cost;
- enforce part, aggregate, or decoded-byte bounds; or
- resolve the selected route's image capability before mutation.

The TUI separately estimates decoded size as `len(base64) * 3 / 4`, uses a
5 MiB per-attachment and 10 MiB aggregate draft limit, and identifies formats
from clipboard metadata or filename extension. That UI guard is useful for
draft memory, but it is neither exact nor shared by ACP, SDK, durable replay,
or a caller that invokes `SubmitMessageWithImages` directly.

[`currentModelSupportsImages`](../../../../internal/tui/composer_elements.go)
returns true when the engine, model name, or registry row is unavailable. An
unknown capability therefore fails open at the TUI boundary.

### The provider leaf is already wider than the public product

[`messagesToAgentic`](../../../../engine/provider/provider.go) converts ordered
Eino user parts into typed Agentic text, image, audio, video, and file blocks.
It rejects nil, ambiguous, mismatched, unsupported, and unknown parts before
the inner provider call, and it does not forward arbitrary message/part
metadata.

This P25.1 boundary is valuable but intentionally narrow:

- it accepts a message that upstream already constructed;
- it does not own transport decoding, project policy, route capability, or
  durable media;
- it does not repair ordering already lost by the TUI; and
- it cannot make ACP blocks that were never submitted reappear.

Replacing this leaf would add risk without closing the product gap. It should
remain the final provider-specific lowering and validation boundary.

### Inline base64 is not a durable media design

[`Recorder.RecordMessages`](../../../../engine/transcript/persist.go) serializes
the full Eino message into one JSONL record. `LoadFull` uses an 8 MiB scanner
record limit. The runtime-input coordinator likewise marshals `UserImage`
base64 into its JSON sidecar.

The TUI currently permits up to 5 MiB of decoded image data. Base64 expands
that by roughly one third before JSON escaping and envelope overhead. A turn
with two individually valid images can also approach the TUI's 10 MiB aggregate
draft allowance. The resulting durable line is not bounded by the reader's
record budget. Restart can therefore skip a valid rich turn as a corrupt or
oversized record even though the live turn reached the model.

The same bytes are retained in draft state, runtime-item state, active Eino
messages, transcript JSON, and provider request construction. This multiplies
memory and storage cost and makes deletion, fork, export, and corruption
handling implicit.

### ACP silently narrows the protocol

[`Agent.Initialize`](../../../../server/acp/agent.go) advertises no optional
prompt media capabilities.
[`Agent.Prompt`](../../../../server/acp/agent.go) calls `extractPromptText`,
which reads only `ContentBlock.Text`, drops empty blocks, and inserts newlines
between surviving blocks. If no text remains, the adapter returns a successful
`end_turn` without invoking the engine.

As a result:

- a `ResourceLink`-only prompt succeeds without using the link;
- an image plus text becomes text-only;
- embedded text/blob content is discarded;
- original block order and exact text bytes are not preserved; and
- a command-looking text block can execute after its accompanying rich input
  was silently removed.

Official ACP v1 defines Text and ResourceLink as baseline prompt content and
image, audio, and embedded resources as capability-gated content. P23.H1
already owns the baseline ResourceLink/error-truth repair. P30 must build on
that owner, not create a competing ACP baseline.

### Recovery can change the user's question

For a `media_size` provider failure,
[`sanitizeReactiveMessage`](../../../../engine/compact/reactive.go) removes
`UserInputMultiContent`, `MultiContent`, and generated media from every
candidate message and marks the clone `media_stripped`.

`TestQueryMediaSizeRecoveryStripsMediaAndContinues` explicitly expects a
second provider call and a successful answer after the current user media has
been removed. The original message object remains immutable, but the semantic
request sent to the model has changed. This is not a lossless recovery:
historical media may be summarized or represented by a marker, while the
current turn's media must either reach the model or terminate with an explicit
input/media error.

## Protocol And Provider Baselines

These limits are evidence about variability, not stable project defaults. They
were checked on 2026-07-27 and may change.

### ACP v1

The official [initialization
contract](https://agentclientprotocol.com/protocol/v1/initialization),
[content types](https://agentclientprotocol.com/protocol/v1/content), and
[prompt-turn lifecycle](https://agentclientprotocol.com/protocol/v1/prompt-turn)
establish:

- Text and ResourceLink are baseline content.
- Image, audio, and embedded resources require advertised prompt
  capabilities.
- Content is an ordered block collection.
- ResourceLink is a reference supplied by the client; support does not grant
  the agent permission to fetch or open arbitrary URIs.

Agent capability and selected-model capability are different facts. Once the
adapter can safely ingest, persist, and project image/embedded content, it may
advertise that agent capability. A later per-session model switch can still
make a particular turn unsupported; that must produce a stable prompt error
before conversation mutation.

### Provider limits differ

The current official
[Anthropic vision guide](https://platform.claude.com/docs/en/build-with-claude/vision)
documents JPEG, PNG, GIF, and WebP input, platform-dependent request/image
limits, and first-frame handling for animated images.

The current official
[OpenAI image-input guide](https://developers.openai.com/api/docs/guides/images-vision)
documents PNG, JPEG, WebP, and non-animated GIF input, with model/request limits
and detail-dependent resizing.

The current official
[Gemini image-understanding guide](https://ai.google.dev/gemini-api/docs/image-understanding)
documents PNG, JPEG, WebP, HEIC, and HEIF input, with different inline and file
API limits.

One hard-coded `SupportsMedia` boolean cannot describe these differences.
Admission needs:

- a project-wide safety envelope;
- a selected-route capability snapshot;
- accepted MIME and source forms;
- count, byte, dimension, and pixel budgets; and
- `supported`, `unsupported`, or `unknown` truth.

The effective budget is the intersection of project policy and the resolved
route. Unknown rich-input capability must fail closed.

## Reference Mechanisms

### OpenCode: ordered ACP projection and shared provider validation

At snapshot `411eff73f026`:

- `packages/opencode/src/acp/content.ts::promptContentToParts` maps ACP content
  blocks to prompt parts in source order.
- `packages/opencode/src/acp/service.ts` advertises image and embedded context,
  then submits the converted parts to its canonical session prompt owner.
- session messages retain file parts and replay them back as media rather than
  flattening everything to one string.
- provider transformation validates declared media type, canonical base64, and
  encoded/decoded size before provider use.

The useful mechanism is one ordered part path shared by ACP, session state, and
provider lowering. The silent default branch that returns no part for an
unknown block should not be copied; Eino-Agent needs an explicit error.

### Claude Code Ripe: session-private durable image bytes

At snapshot `4b9d30f79532`:

- `src/utils/imageStore.ts` writes image files with mode `0600`, flushes the
  handle, and scopes cleanup by session directory.
- `src/utils/imageValidation.ts::validateImagesForAPI` validates image request
  size before API use.
- query and API error handling retain a dedicated image-size category.

This is strong evidence for private blob storage and early size validation.
Eino-Agent should not copy a globally shared content hash or reference-specific
cleanup policy because its transcript, runtime-input, branch, and session
deletion owners differ.

### Crush: typed binary content survives persistence

At snapshot `2af939d8e900`,
`internal/proto/message.go::BinaryContent` has explicit path, MIME, and byte
fields with tagged serialization/deserialization, while the model path lowers
it to a typed `FilePart`.

The useful property is a versioned durable tagged union that does not pretend
binary content is ordinary text. Inline raw bytes are not suitable for
Eino-Agent's JSONL record budget, so the target union should store a durable
`MediaRef`, not copy Crush's payload layout.

### Codex: typed user input and bounded image preparation

At snapshot `66bd101fff6f`:

- `codex-rs/protocol/src/legacy_events.rs` keeps image URLs, local images,
  details, and audio separate from text.
- `codex-rs/core/src/session/input_queue.rs` queues typed `UserInput` values.
- event-mapping fixtures preserve text/image order and distinguish display
  labels from actual image content.
- image preparation has explicit resize/error behavior instead of assuming an
  arbitrary path or payload is provider-ready.

This supports a typed queue and a provider-preparation step. Eino-Agent should
not import Codex's service protocol or legacy event schema.

### Eino v0.9.12: useful leaf schema, not a durable product contract

Eino `schema.MessageInputPart` already represents text, image, audio, video,
file, tool-search result, and extra metadata. Agentic `ContentBlock` has
corresponding user input variants.

Those types are appropriate for the provider boundary. They do not define:

- ACP ResourceLink semantics;
- session-private media identity;
- transcript/queue write ordering;
- capability provenance;
- fork/delete/export behavior; or
- current-turn recovery rules.

The project should therefore convert a public `UntrustedPromptInput` into an
internal/durable `AdmittedPromptInput`, then project the admitted form into Eino
parts, rather than making an upstream schema its transport or durable session
contract.

## Comparison Matrix

| Observable property | Current Eino-Agent | OpenCode | Claude Code Ripe | Crush | Codex | Target |
|---|---|---|---|---|---|---|
| TUI image input | Leader only | Supported | Supported | Supported | Supported | Preserve leader support; make ordering lossless |
| ACP rich blocks | Text only; silent drop | Ordered image/embedded | Adapter-specific | Not primary evidence | Stateful ACP/app-server paths | Text/image/embedded support; ResourceLink preserved as a bounded no-fetch descriptor |
| Ordered interleaving | Lost at TUI/engine | Preserved | Typed message blocks | Typed parts | Typed `UserInput` | Ordered untrusted input converted once to ordered admitted refs |
| Shared strict validation | No | Base64/MIME/size | API image size | Typed bytes | Image preparation | Project envelope intersected with route limits |
| Durable media | Inline base64 JSON | Durable file parts | Private image store | Tagged inline binary | Typed rollout/input | Session-private blob store plus `MediaRef` |
| Queue/restart identity | Duplicated base64 | Typed parts | Session-backed | Serialized union | Typed queue/rollout | Same versioned ref in queue and transcript |
| Capability truth | Boolean/fail-open | Provider/model metadata | Model-specific | Provider-specific | Model metadata | Three-state snapshot with provenance |
| Unsupported input | Mixed/silent | Some explicit, some drop | Explicit image errors | Provider conversion | Explicit preparation errors | Stable pre-mutation error |
| Media-size recovery | May strip current turn | Provider-dependent | Dedicated error path | Provider-dependent | Prepare or fail | Never answer after losing current-turn media |

## Decision

### Adopt under `combine`

1. Introduce non-interchangeable project-owned, versioned, ordered
   `UntrustedPromptInput` and `AdmittedPromptInput`; external callers cannot
   submit durable refs.
2. Keep P25.1 as the provider leaf and hydrate/project admitted parts into Eino
   immediately before that leaf.
3. Add a session-private content-addressed `MediaStore`; expose random
   session-scoped media IDs and keep the digest as integrity/storage metadata.
4. Store refs, not base64, in new transcript and runtime-input records.
5. Make route capability a three-state snapshot with provenance and fail rich
   input closed when it is unknown.
6. Preserve ACP ResourceLink as opaque ordered metadata at ingress. Never fetch,
   open, or forward its URI; version 1 retains P23's bounded deterministic
   user-text descriptor as the accepted no-egress representation.
7. Advertise ACP image and embedded-context support only after the production
   path and conformance fixtures pass. Keep audio unadvertised.
8. Permit deterministic historical-media omission for compaction, but never
   remove current-turn media and continue as though the original request was
   answered.

### Preserve

- QueryEngine and ProjectGraph as turn and model-loop owners.
- Transcript as durable conversation authority.
- RuntimeInputCoordinator as durable queued-input owner.
- TUI draft geometry and clear-after-accepted semantics.
- P23's ACP baseline and stateful projector program.
- P25.1 typed Agentic conversion and provider-specific adapters.
- Existing text-only APIs as compatibility wrappers.

### Reject or defer

- Reject silent downgrade of rich input to text-only.
- Reject remote URL fetching in the core input contract.
- Reject global cross-session content deduplication; it couples privacy and
  deletion lifetimes.
- Reject raw local paths, base64, or media bytes in transcript, logs,
  diagnostics, and terminal errors.
- Reject SVG in the initial safe image allowlist; rasterization requires a
  separate sandboxed design.
- Defer audio/video user input and TUI binary-file attachment.
- Defer dynamic price/quota routing to P29; P30 only consumes selected-route
  capability truth.
- Defer a new portable media export archive. Existing private export/import
  must fail explicitly for media-bearing sessions until such a format exists.

## Compatibility Consequences

- Existing `SubmitMessage` and `SubmitMessageWithImages` remain source
  compatible and become wrappers over the new input path.
- Existing inline-base64 transcript records remain readable. New rich turns use
  refs; no automatic transcript rewrite occurs on resume.
- Previously accepted SVG, malformed base64, MIME-mismatched, oversized, or
  unknown-capability images will be rejected before model/transcript mutation.
  This is an intentional safety correction.
- TUI text/image interleaving becomes observable to the model. Text-only and
  the current text-then-images case remain equivalent.
- ACP clients stop receiving successful empty turns for unsupported rich
  blocks. Once P30's live ACP slice lands, image and embedded-context
  capabilities become truthful; audio remains false and ResourceLink retains
  P23's stable no-fetch descriptor. Rich load/replay remains a separate
  P30.5b boundary over completed P23.4b.
- A provider media-size failure on the current turn becomes terminal instead of
  producing an answer without that media.

## Evidence Limits

- Official provider limits are time-sensitive and must not be copied into a
  stable static registry without provenance and refresh ownership.
- Local reference snapshots prove mechanisms, not their current upstream
  release behavior.
- This audit does not prove that every configured model accepts every MIME
  listed by its provider family.
- ACP client interoperability still requires a current SDK wire fixture and a
  real client such as Zed before capability advertisement.
- The target write/fork/delete protocol is a design commitment. Its crash
  behavior remains unimplemented until P30 slices provide deterministic fault
  injection.

## Current Validation Baseline

Before P30 implementation, keep these focused suites as compatibility evidence:

```bash
go test ./engine -run 'TestSubmitMessageWithImages|TestRuntimeInput.*Image|TestQueryMediaSize'
go test ./engine/provider -run 'TestMessagesToAgentic|TestAgenticInput'
go test ./internal/tui -run 'Test.*Image|TestBusyQueuePreservesRich'
go test ./server/acp -run 'Test.*Prompt'
```

Passing them proves the current partial path. It does not close G32. P30's
contract adds negative fixtures for order loss, oversized durable records,
unknown capability, restart/fork/delete, current-turn media recovery, and ACP
wire behavior.
