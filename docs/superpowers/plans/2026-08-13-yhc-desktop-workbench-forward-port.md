# YHC Desktop Workbench Forward-Port Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `$iteration-workflow` for
> test-first execution and committed evidence, `$runtime-depth-change` for
> shared runtime contracts, `$tui-runtime-change` for replay and renderer-state
> compatibility, and `$write-docs` for current-state closeout.

**Status:** active-plan
**Created:** 2026-08-13
**Plan state:** Active; Task 1 is the first implementation slice

> **Ownership:** executable, test-first delivery sequence for the accepted
> [YHC Desktop Workbench Forward-Port design](../specs/2026-08-13-yhc-desktop-workbench-forward-port-design.md)

**Goal:** Deliver a usable YHC-native Desktop workbench without publishing the
private source branch history, bypassing canonical session admission, or
weakening the existing TUI and ACP contracts.

**Architecture:** Shared runtime owners produce typed interaction and transcript
projections. A loopback-only Go app-server owns authentication, canonical
session activation, durable history, and exactly-once settlement. The embedded
WebUI and Electron main process consume bounded DTOs; the renderer never owns a
backend capability, filesystem path, provider secret, or authorization fact.

**Tech Stack:** Go 1.26.5, Eino `QueryEngine`, JSONL transcripts, loopback
HTTP/SSE, vanilla ES modules, vendored Marked 18.0.9, Electron 41.10.4,
electron-builder 26.15.6, Node's built-in test runner, CycloneDX Go and Node
SBOMs, GitHub Actions, and the repository publication sieve.

## Global constraints

- Work only from the public-history topic branch. Do not merge, cherry-pick,
  fetch, push, or preserve the private Desktop branch's Git objects or author
  metadata.
- Adopt behavior by owner and observable contract. Do not overwrite newer YHC
  runtime, identity, state, publication, CI, or session-admission code with an
  older full-file snapshot.
- Use `YHC`, `yhc`, `github.com/abietic/yhc`, `com.abietic.yhc.desktop`,
  `YHC_BIN`, `yhcDesktop`, `yhc.desktop.*`, and canonical `.yhc` roots at every
  new public boundary.
- Keep legacy `.eino-agent` rows read-only. Import is an explicit two-step
  `import -> canonical attach` flow with stopped-producer attestation; Send
  never imports implicitly.
- The Go child creates the per-process bearer and emits one size-bounded
  bootstrap record. Electron main retains it in memory. Renderer, preload
  results, argv, files, URLs, storage, diagnostics, and logs never receive it.
- A selected durable row renders history only. No `QueryEngine`, event stream,
  lease, or provider starts before the first accepted user turn.
- `PermissionPresentation` and Activity are server-authored display
  projections. They do not become client-authored authorization inputs.
- Assistant Markdown passes through the vendored parser and a fixed DOM
  allowlist. User, tool, system, reasoning, answer, Plan feedback, and raw
  protocol content remain literal or excluded.
- The new vector icon is project-owned source expression. Generated PNG bytes
  are derived only from that tracked SVG and documented as such.
- Maintain separate deterministic Go and Node SBOM/license evidence. Electron
  and its full packaged dependency closure are not exempt because they are npm
  `devDependencies`.
- The first package is unsigned local QA output only. No release, updater,
  signing, notarization, or distribution-ready claim belongs in this plan.
- Preserve unrelated worktree changes. Every task uses red-green tests, stages
  only its named paths, and ends with current focused evidence.

---

## Task 1: Produce typed interaction and bounded transcript contracts

**Files:**

- Create: `engine/permission_presentation.go`
- Create: `engine/permission_presentation_test.go`
- Create: `tools/ask_user_test.go`
- Modify: `engine/permission_interaction.go`
- Modify: `engine/permission_interaction_test.go`
- Modify: `engine/events.go`
- Modify: `engine/engine.go`
- Modify: `engine/graph_hitl.go`
- Modify: `engine/graph_hitl_test.go`
- Modify: `engine/graph_query_kernel.go`
- Modify: `engine/runtime_state.go`
- Modify: `engine/runtime_state_test.go`
- Modify: `engine/thread_attention_test.go`
- Modify: `engine/thread_catalog_test.go`
- Modify: `engine/tool_execution.go`
- Modify: `engine/repeated_tool_integration_test.go`
- Modify: `engine/transcript/message_page.go`
- Modify: `engine/transcript/message_page_test.go`
- Modify: `tools/ask_user.go`
- Test: `internal/tui/permission_lifecycle_test.go`
- Test: `server/acp/agent_test.go`
- Test: `server/acp/replay_test.go`

**Interfaces:**

- Produces:

```go
const (
	PermissionInteractionKindPermission   = "permission"
	PermissionInteractionKindQuestion     = "question"
	PermissionInteractionKindPlanApproval = "plan_approval"
	PermissionInteractionKindRepeatedTool = "repeated_tool"
)

type PermissionPromptRequest struct {
	Kind              string
	Attempt           int
	Source            string
	ToolName          string
	CanonicalToolName string
	Input             any
	Presentation      *PermissionPresentation
	// Existing identity, scope, policy, and callback fields remain unchanged.
}

type PermissionPresentation struct {
	Version     int
	Capability  string
	Target      string
	Risk        string
	Evidence    []PermissionPresentationEvidence
	AllowAlways bool
}

func ValidateUserQuestions([]UserQuestion) error

const (
	MessagePageScopeActive transcript.MessagePageScope = "active"
	MessagePageScopeAudit  transcript.MessagePageScope = "audit"
)
```

- Consumers: Task 2 maps the immutable request to typed protocol v2; Task 3
  reads audit scope for bounded durable reconstruction. Existing TUI and ACP
  may ignore new presentation fields but must preserve authorization behavior.

- [ ] **Step 1: Add AskUserQuestion validation tests**

Add table tests that accept one to four questions and either zero or two to
four options, and reject invalid UTF-8, more than four questions, exactly one
option, more than four options, blank labels, normalized duplicate questions
or options, oversized fields, and payloads above 16 KiB.

```bash
go test ./tools -run 'TestValidateUserQuestions' -count=1
```

Expected before implementation: FAIL because `ValidateUserQuestions` and the
bounded validation constants do not exist.

- [ ] **Step 2: Implement shared question validation**

Make `executeAskUserQuestion` call `ValidateUserQuestions` before invoking any
adapter. Compare normalized identities after trimming and case folding, count
runes rather than bytes for field limits, reject invalid UTF-8 first, and keep
the serialized payload limit at 16 KiB.

```bash
go test ./tools -run 'TestValidateUserQuestions|TestAskUserQuestion' -count=1
```

Expected: PASS.

- [ ] **Step 3: Add producer-owned interaction identity tests**

Add tests that prove ordinary permission, question, Plan approval, and
repeated-tool requests emit exact `Kind`, `Source`, `Attempt`, and canonical
tool identity from their producers. Add cold-restart and reducer tests proving
the fields survive ProjectGraph persistence, replay, resolution, and runtime
snapshots. A legacy empty-kind fixture may retain the existing compatibility
fallback; a new explicit kind must never be reclassified from `ToolName`.

```bash
go test ./engine -run 'Test.*(PermissionInteraction|TypedInteraction|RepeatedTool|ProjectGraph.*Permission|ThreadAttention)' -count=1
```

Expected before implementation: FAIL on missing fields or name-based
classification.

- [ ] **Step 4: Carry immutable identity across every producer and replay**

Populate `Kind` at `QueryEngine.promptForTool`, project-graph HITL, repeated
tool guard, and `ReportPermissionPromptRequested`. Use
`repeated_tool_guard` with `Attempt == 3` for the guarded third identical call.
Persist and reproject `Kind`, `Source`, `Attempt`, and `CanonicalToolName` as
one identity. Keep exact-request settlement fail-closed; do not merge or repair
conflicting event and callback identities in a broker.

```bash
go test ./engine -run 'Test.*(PermissionInteraction|TypedInteraction|RepeatedTool|ProjectGraph.*Permission|ThreadAttention)' -count=1
```

Expected: PASS.

- [ ] **Step 5: Add safe presentation tests and implementation**

Test read, write, destructive, path, network, and child-process capability
presentations, alias canonicalization, field bounds, forged values, and
unavailable fallback. Only ordinary permission receives a presentation;
question, Plan approval, and repeated-tool projections receive `nil`.

```bash
go test ./engine -run 'Test.*PermissionPresentation' -count=1
```

Expected before implementation: FAIL because the presentation owner is absent;
after implementing fixed evidence allowlists and normalization: PASS.

- [ ] **Step 6: Add active and audit transcript scope tests**

Test that active scope retains the current lifecycle projection, audit scope
skips lifecycle snapshots, audit refuses lifecycle cursors, malformed scopes
fail closed, and both modes retain existing byte/page limits.

```bash
go test ./engine/transcript -run 'TestLoadMessagePage.*(Active|Audit|Scope|Cursor)' -count=1
```

Expected before implementation: FAIL on absent audit scope; after the smallest
selection change: PASS.

- [ ] **Step 7: Run shared-entrypoint regressions and commit**

```bash
go test ./tools ./engine ./engine/transcript ./internal/tui ./server/acp -count=1
go test -race ./engine ./engine/transcript ./tools -count=1
make test-contract
make test-pty
git add engine tools internal/tui/permission_lifecycle_test.go server/acp
git commit -m "feat(runtime): freeze desktop interaction projections"
```

Expected: all named tests pass; ACP wire authorization, TUI question handling,
MCP dispatch, cancellation, and exactly-once settlement remain unchanged.

## Task 2: Add the loopback app-server and typed live protocol

**Files:**

- Create: `server/appserver/activity.go`
- Create: `server/appserver/activity_test.go`
- Create: `server/appserver/browser_auth.go`
- Create: `server/appserver/browser_auth_test.go`
- Create: `server/appserver/event_log.go`
- Create: `server/appserver/event_log_test.go`
- Create: `server/appserver/execution_settings.go`
- Create: `server/appserver/execution_settings_test.go`
- Create: `server/appserver/permission_broker.go`
- Create: `server/appserver/permission_broker_test.go`
- Create: `server/appserver/protocol.go`
- Create: `server/appserver/interaction_protocol_test.go`
- Create: `server/appserver/review_diff.go`
- Create: `server/appserver/review_diff_test.go`
- Create: `server/appserver/server.go`
- Create: `server/appserver/server_test.go`
- Create: `server/appserver/session.go`
- Create: `server/appserver/session_test.go`
- Modify: `engine/commands/registry.go`
- Modify: `engine/commands/registry_entrypoint_test.go`
- Modify: `engine/permission_policy_test.go`

**Interfaces:**

- Consumes: Task 1 `PermissionPromptRequest`, `PermissionPresentation`, typed
  producer identity, and runtime events.
- Produces:

```go
type Bootstrap struct {
	BaseURL string `json:"base_url"`
	Token   string `json:"token"`
}

type SessionOptions struct {
	CWD              string
	Model            string
	ReasoningEffort  string
	PermissionMode   string
	ResumeSessionID  string
	TranscriptDir    string
}

type InteractionEnvelope struct {
	Version   int             `json:"version"`
	Kind      string          `json:"kind"`
	RequestID string          `json:"request_id"`
	Revision  uint64          `json:"revision"`
	Payload   json.RawMessage `json:"payload"`
}

func New(Config) (*Server, error)
func (s *Server) BootstrapFor(net.Listener) Bootstrap
func (s *Server) Serve(net.Listener) error
func (s *Server) Shutdown(context.Context) error
```

- Task 4 consumes the HTTP/SSE routes and bootstrap. Task 3 extends session
  creation with server-resolved canonical admission.

- [ ] **Step 1: Add server authority and browser-auth tests**

Create tests for loopback-only listeners, exact normalized Host and port,
wrong/stale bearer, constant-time comparison behavior, short single-use
pairing, exact Origin, `Sec-Fetch-Site`, YHC-prefixed HttpOnly Strict cookie,
YHC-prefixed CSRF header on mutations, expiry/replay, and shutdown clearing all
capabilities.

```bash
go test ./server/appserver -run 'Test.*(Loopback|Host|Bearer|Pairing|Cookie|CSRF|Shutdown)' -count=1
```

Expected before implementation: FAIL because `server/appserver` does not
exist.

- [ ] **Step 2: Implement authenticated loopback lifecycle**

Generate the process bearer in `New`, emit it only through `BootstrapFor`,
enforce exact host/origin before routing, and keep browser pairing state bounded
and memory-only. `Shutdown` stops admission before cancelling sessions and
waits for owned workers without releasing active-turn resources early.

```bash
go test ./server/appserver -run 'Test.*(Loopback|Host|Bearer|Pairing|Cookie|CSRF|Shutdown)' -count=1
```

Expected: PASS.

- [ ] **Step 3: Add typed interaction, Activity, and event-log tests**

Test exact v2 variants for ordinary permission, AskUserQuestion, Plan review,
and repeated-tool intervention. Reject unknown fields, mismatched kind or
revision, stale/cross-session settlement, malformed answers, and duplicate
resolution. Test that Activity emits only fixed semantic families and bounded
identity/state fields; it excludes model text, reasoning, questions, answers,
Plan feedback, tool input, raw transport names, and unbounded event history.

```bash
go test ./server/appserver -run 'Test.*(Interaction|Activity|EventLog|PermissionBroker)' -count=1
```

Expected before implementation: FAIL on absent protocol and projections.

- [ ] **Step 4: Implement protocol v2 and semantic projections**

Map only Task 1 producer-owned kinds. Store pending request identity by
session, request ID, revision, kind, source, attempt, and immutable digest.
The resolve route accepts only the allowed decision fields for that kind and
settles once. Build Activity from admitted lifecycle events rather than SSE
frame names or transcript prose.

```bash
go test ./server/appserver -run 'Test.*(Interaction|Activity|EventLog|PermissionBroker)' -count=1
```

Expected: PASS.

- [ ] **Step 5: Add app-server command entrypoint after composition exists**

Add `commands.EntrypointAppServer`, reuse ACP discovery visibility, and make
the app-server composition set that entrypoint explicitly. Add policy tests
proving app-server `EnterPlanMode` enters the typed Plan lifecycle while
explicit deny remains dominant. TUI and ACP behavior remain unchanged.

```bash
go test ./engine/commands ./engine ./server/appserver -run 'Test.*(AppServer|EnterPlanMode|Discovery)' -count=1
```

Expected: PASS.

- [ ] **Step 6: Run race/lifecycle checks and commit**

```bash
go test ./server/appserver ./engine/commands ./engine -count=1
go test -race ./server/appserver ./engine -count=1
git add server/appserver engine/commands engine/permission_policy_test.go
git commit -m "feat(appserver): add typed desktop runtime authority"
```

Expected: tests pass with no goroutine, request, event-stream, or capability
leak after shutdown.

## Task 3: Add durable history, canonical first-send activation, and import

**Files:**

- Create: `server/appserver/attach_turn.go`
- Create: `server/appserver/attach_turn_test.go`
- Create: `server/appserver/durable_sessions.go`
- Create: `server/appserver/durable_sessions_test.go`
- Create: `server/appserver/lease.go`
- Create: `server/appserver/lease_test.go`
- Create: `server/appserver/transcript_page.go`
- Create: `server/appserver/transcript_page_test.go`
- Create: `server/appserver/import_session.go`
- Create: `server/appserver/import_session_test.go`
- Modify: `server/appserver/protocol.go`
- Modify: `server/appserver/server.go`
- Modify: `server/appserver/session.go`
- Modify: `server/appserver/session_test.go`
- Modify: `engine/engine.go`
- Test: `engine/session/admission_test.go`
- Test: `engine/session/migration_test.go`

**Interfaces:**

- Consumes: `session.QuerySessions`, `session.AdmitSessionResume`,
  `SessionService.ImportLegacyAndResumeInfo`, Task 1 audit pages, and Task 2
  server lifecycle.
- Produces:

```go
type AttachTurnRequest struct {
	Prompt       string `json:"prompt"`
	ClientTurnID string `json:"client_turn_id"`
}

type ImportSessionRequest struct {
	ConfirmLegacyStopped bool `json:"confirm_legacy_stopped"`
}

type ImportSessionResponse struct {
	SessionID string `json:"session_id"`
	State     string `json:"state"` // "imported" or "already_imported"
}

func acquireSessionLease(transcriptDir, sessionID, serverID string) (*sessionLease, error)
```

- Task 4 uses durable catalog/history, explicit import, and attach routes. The
  import response never activates a runtime; the client follows it with the
  ordinary attach-turn route.

- [ ] **Step 1: Add read-only durable discovery and history tests**

Test bounded catalog paging/search, stable cursor ownership, canonical and
legacy rows, ambiguous candidates, malformed/torn transcript tails, audit
paging, and descriptor isolation. Assert selection and paging create no
engine, provider, event stream, lease, catalog write, or legacy mutation.

```bash
go test ./server/appserver -run 'Test.*(DurableSession|DurableTranscript|HistoryOnly)' -count=1
```

Expected before implementation: FAIL on absent routes.

- [ ] **Step 2: Implement server-resolved history projection**

Use `session.QuerySessions` and retain server-side `SessionInfo` provenance.
Never accept CWD, transcript directory, catalog path, or legacy/canonical flags
from the renderer. Read bounded transcript pages through the transcript owner;
return typed import-required state for a legacy-only descriptor.

```bash
go test ./server/appserver -run 'Test.*(DurableSession|DurableTranscript|HistoryOnly)' -count=1
```

Expected: PASS.

- [ ] **Step 3: Add attach idempotency and canonical admission tests**

Test normalized non-empty prompt bytes, UUID `client_turn_id`, one in-flight
activation per session, same-ID receipt replay, same-ID/different-prompt
conflict, cancellation, retry after failure, pending interaction before prompt,
shutdown release, and session-limit reservation. Assert the lease path is
`<admitted TranscriptDir>/<sessionID>/.app-server.lock`; forged renderer paths
cannot affect it.

```bash
go test ./server/appserver -run 'TestAttachTurn|TestSessionLease' -count=1
```

Expected before implementation: FAIL on absent attach and admitted-dir lease.

- [ ] **Step 4: Implement first-send activation**

Resolve the trusted descriptor again, call `session.AdmitSessionResume`, carry
the admitted `SessionInfo.CWD` and `TranscriptDir` into engine construction,
then acquire the lease and submit exactly one normalized prompt. On failure,
retain draft/history, release only resources actually acquired, and expose an
explicit retry state.

```bash
go test ./server/appserver -run 'TestAttachTurn|TestSessionLease' -count=1
```

Expected: PASS.

- [ ] **Step 5: Add explicit legacy import tests**

Test attach to a legacy row returns import-required; import without
`ConfirmLegacyStopped` returns the existing typed attestation error; attested
import creates or recognizes one canonical bundle; legacy bytes and metadata
remain unchanged; import failure creates no runtime or lease; a later attach
performs fresh canonical admission and succeeds.

```bash
go test ./server/appserver ./engine/session ./engine -run 'Test.*(ImportLegacy|ImportSession|Legacy.*Attach|Canonical.*Attach)' -count=1
```

Expected before implementation: FAIL on absent endpoint.

- [ ] **Step 6: Implement two-step import and commit**

Call `SessionService.ImportLegacyAndResumeInfo` only from the explicit import
handler. Return `imported` or `already_imported`; do not construct an engine,
acquire a lease, or submit the draft. Require the subsequent attach route to
resolve and admit the canonical row again.

```bash
go test ./server/appserver ./engine/session ./engine -count=1
go test -race ./server/appserver ./engine/session -count=1
git add server/appserver engine/engine.go engine/session
git commit -m "feat(appserver): activate canonical history on first send"
```

Expected: all tests pass; legacy state remains read-only and attach receipts
remain exactly-once across retries and shutdown.

## Task 4: Add the safe WebUI and YHC Electron host

**Files:**

- Create: `internal/webui/assets.go`
- Create: `internal/webui/assets/activity.mjs`
- Create: `internal/webui/assets/app.mjs`
- Create: `internal/webui/assets/catalog.mjs`
- Create: `internal/webui/assets/index.html`
- Create: `internal/webui/assets/layout.mjs`
- Create: `internal/webui/assets/markdown.mjs`
- Create: `internal/webui/assets/provider_setup.mjs`
- Create: `internal/webui/assets/state.mjs`
- Create: `internal/webui/assets/styles.css`
- Create: `internal/webui/assets/transport.mjs`
- Create: `internal/webui/assets/view_models.mjs`
- Create: `internal/webui/assets/vendor/marked.esm.js`
- Create: `internal/webui/assets/vendor/marked.LICENSE.txt`
- Create: `internal/webui/assets/vendor/marked.NOTICE.txt`
- Create: `desktop/assets/icon.svg`
- Create: `desktop/assets/icon.png`
- Create: `desktop/lifecycle.cjs`
- Create: `desktop/main.cjs`
- Create: `desktop/package.json`
- Create: `desktop/package-lock.json`
- Create: `desktop/preload.cjs`
- Create: `desktop/provider_setup.cjs`
- Create: `desktop/request.cjs`
- Create: `desktop/test/*.test.mjs`
- Create: `cmd/yhc/cmd/serve_app.go`
- Modify: `cmd/yhc/cmd/serve.go`
- Modify: `cmd/yhc/cmd/root.go`
- Modify: `cmd/yhc/cmd/cli_contract_test.go`
- Modify: `cmd/yhc/cmd/p29_0_composition_test.go`
- Modify: `.gitignore`
- Modify: `Makefile`

**Interfaces:**

- Consumes: Tasks 2-3 bootstrap and fixed operations. The preload exposes only
  `request(operation, payload)`, `startEvents(sessionID, listener)`,
  `stopEvents(sessionID)`, workspace selection, provider setup submission, and
  sanitized provider status.
- Produces: `yhc serve app --web`, `make desktop-check`,
  `make desktop-backend`, `make desktop-dev`, and `make desktop-package`.

- [ ] **Step 1: Add renderer state, transport, and interaction tests**

Port the Node behavior tests first. Cover history-only selection, draft
preservation, first-send attach, explicit import, live rebind, typed
interaction forms, semantic Activity, execution settings, review diff,
catalog paging, responsive sheets, and normalized request bytes.

```bash
node --test desktop/test/state.test.mjs desktop/test/transport.test.mjs desktop/test/interactions.test.mjs desktop/test/catalog.test.mjs desktop/test/activity.test.mjs
```

Expected before implementation: FAIL because renderer modules do not exist.

- [ ] **Step 2: Implement bounded renderer state and transport**

Use `yhc.desktop.sessions.v1` and `yhc.desktop.drafts.v1` only for untrusted
UI hints. Rebuild durable history from server pages, activate only on Send,
model import as a separate action, and accept only protocol-v2 view models.
Unknown or malformed interaction variants render a non-actionable safe error.

```bash
node --test desktop/test/state.test.mjs desktop/test/transport.test.mjs desktop/test/interactions.test.mjs desktop/test/catalog.test.mjs desktop/test/activity.test.mjs
```

Expected: PASS.

- [ ] **Step 3: Add Markdown, layout, and appearance tests**

Test headings, lists, tables, fenced and inline code, links, Unicode, partial
stream updates, prohibited raw HTML, dangerous schemes, external images,
event-handler attributes, and DOM-node budgets. Add structure tests for the
three-surface layout, sticky composer, compact/narrow sheets, keyboard focus,
and readable empty/loading/error states.

```bash
node --test desktop/test/markdown.test.mjs desktop/test/layout.test.mjs desktop/test/composer_structure.test.mjs desktop/test/onboarding_structure.test.mjs
```

Expected before implementation: FAIL on absent parser/DOM and layout.

- [ ] **Step 4: Implement safe Markdown and polished three-surface UI**

Parse assistant text with the byte-identical vendored Marked module, build DOM
nodes through a fixed tag/attribute/scheme allowlist, cap input and output
nodes, and keep all other roles literal. Implement the accepted session rail,
central timeline/composer, Activity/Changes inspector, responsive sheets,
focus states, typography, code blocks, tables, and native-light/dark color
tokens under YHC branding.

```bash
node --test desktop/test/markdown.test.mjs desktop/test/layout.test.mjs desktop/test/composer_structure.test.mjs desktop/test/onboarding_structure.test.mjs
```

Expected: PASS.

- [ ] **Step 5: Add Electron trust-boundary and lifecycle tests**

Test Electron's sandbox/context isolation/Node integration settings, denied
navigation and new windows, trusted IPC sender checks, size-bounded bootstrap,
no token in renderer or argv, stale-child rejection, event stream cleanup,
provider secret redaction/encryption, active-turn quit warning, and child
shutdown timeout. Test binary lookup as `yhc` or `YHC_BIN` only.

```bash
node --test desktop/test/desktop_security.test.mjs desktop/test/lifecycle.test.mjs desktop/test/request.test.mjs desktop/test/provider_setup.test.mjs
```

Expected before implementation: FAIL on absent Electron host.

- [ ] **Step 6: Implement the YHC host and CLI composition**

Launch `yhc serve app`, parse exactly one bounded bootstrap record from stdout,
hold bearer and streams in Electron main, expose fixed `yhcDesktop` methods,
and serve embedded assets through Go. Register `serve app` beneath the existing
YHC command without changing ACP/MCP routes. Build provider launch environment
in main without returning secrets to renderer.

```bash
go test ./server/appserver ./internal/webui ./cmd/yhc/cmd -count=1
node --test desktop/test/*.test.mjs
```

Expected: PASS.

- [ ] **Step 7: Create the project-owned icon and package scripts**

Track an SVG whose source contains only YHC-owned shapes and text outlines,
derive the PNG from that SVG, and add a test comparing expected dimensions and
source provenance. Configure `yhc-desktop`, `com.abietic.yhc.desktop`, product
name `YHC`, staged backend `yhc`, and unsigned local packaging. Ignore
`node_modules`, `dist`, `release`, staged backend resources, and `.DS_Store`.

```bash
npm --prefix desktop ci
make desktop-check
make desktop-backend
make desktop-package
git status --short --ignored desktop
git add .gitignore Makefile cmd/yhc/cmd desktop internal/webui
git commit -m "feat(desktop): add YHC workbench application"
```

Expected: 126 or more Node behavior tests pass, the Go backend builds, the
unsigned local package is created only under ignored output paths, and no
binary/build directory is staged.

## Task 5: Clear Node dependencies and public governance

**Files:**

- Create: `quality/node-dependency-licenses.yaml`
- Create: `scripts/publication/node_dependencies.go`
- Create: `scripts/publication/node_dependencies_test.go`
- Modify: `scripts/publication/config.go`
- Modify: `scripts/publication/dependencies.go`
- Modify: `scripts/publication/main.go`
- Modify: `scripts/publication/scan.go`
- Modify: `scripts/publication/scan_test.go`
- Modify: `quality/publication.yaml`
- Modify: `quality/iteration.yaml`
- Modify: `scripts/iteration/runner.go`
- Modify: `scripts/iteration/runner_test.go`
- Modify: `Makefile`
- Modify: `.github/dependabot.yml`
- Modify: `.github/workflows/ci.yml`
- Modify: `NOTICE`
- Modify: `docs/publication/README.md`
- Modify: `docs/migration/manifest.yaml`
- Modify: `PUBLICATION_MANIFEST.json`

**Interfaces:**

- Produces: ignored `build/publication/sbom.node.cdx.json`, `make desktop-audit`,
  `make node-sbom`, `make node-license-check`, and a Desktop-aware
  `Required gates` aggregate.
- Consumes: the exact `desktop/package-lock.json` graph and vendored Marked
  license/notice from Task 4.

- [ ] **Step 1: Add strict Node dependency-policy tests**

Test lockfile v3 parsing, root package identity, one policy decision for every
reachable package/version, unknown and disallowed licenses, missing integrity,
non-registry hosts, local/file/git dependencies, stale policy rows,
deterministic CycloneDX components, and the vendored Marked classification.

```bash
go test ./scripts/publication -run 'TestNode.*(Lock|License|SBOM|Vendor)' -count=1
```

Expected before implementation: FAIL because Node policy support does not
exist.

- [ ] **Step 2: Implement separate Node SBOM and license checks**

Parse only the tracked lockfile, require the approved npm registry host and
integrity for remote packages, classify the entire reachable Electron build
graph, emit deterministic ignored `build/publication/sbom.node.cdx.json`, and validate it against
`quality/node-dependency-licenses.yaml`. Keep the existing Go SBOM and license
schema unchanged.

```bash
make node-sbom
make node-license-check
go test ./scripts/publication -run 'TestNode.*(Lock|License|SBOM|Vendor)' -count=1
```

Expected: PASS.

- [ ] **Step 3: Add Desktop owners and dependency gates**

Add `app-server-adapter`, `webui-adapter`, and `desktop-workbench` owners and a
five-minute `desktop-check` target. Extend dependency/governance risk packs to
select full npm audit, Node SBOM/license, publication safety, Go test, and build
when Desktop package or supply-chain files change. Add runner tests for target
mapping and deadline selection.

```bash
go test ./scripts/iteration -run 'Test.*Desktop' -count=1
make change-plan
make verify-focused
```

Expected: the plan selects every owner and required gate touched by this
forward-port; no Desktop path falls through a broad unrelated owner.

- [ ] **Step 4: Classify every new public path and license**

Add specific project-owned rules for `server/appserver/**`,
`internal/webui/**` excluding vendored Marked, `desktop/**`, and YHC CLI files
before broad rules. Classify Marked separately as MIT third-party with retained
LICENSE/NOTICE; update root NOTICE and publication documentation. Review npm
registry URLs/integrities through exact structural handling, not a directory
privacy waiver. Regenerate the publication manifest from the clean reviewed
tree.

```bash
go test ./scripts/publication -count=1
make publication-safety
```

Expected: every tracked path has exactly one decision; privacy scanning,
licenses, Go SBOM, and Node SBOM pass without unresolved or wildcard waivers.

- [ ] **Step 5: Add CI and Dependabot coverage**

Add monthly npm updates for `/desktop`. Add a pinned Node setup and independent
Desktop job that runs `npm ci`, syntax/tests, full audit, Node SBOM, and Node
license checks. Extend `changes` and the existing `Required gates` aggregation:
docs-only changes require Desktop skipped; non-docs Desktop changes require
Desktop success. Preserve read-only permissions and immutable action pins.

```bash
make workflow-policy-check
make docs-only-policy-check
make publication-safety
git add .github Makefile NOTICE PUBLICATION_MANIFEST.json docs/migration/manifest.yaml docs/publication quality scripts
git commit -m "chore(desktop): add public Node supply-chain gates"
```

Expected: local workflow policy tests and publication safety pass; the remote
PR later proves the aggregate context.

## Task 6: Document, package, physically verify, and publish the topic branch

**Files:**

- Create: `docs/architecture/desktop-workbench.md`
- Create: `docs/guides/desktop-workbench.md`
- Create: `docs/migration/history/2026-08-13-desktop-workbench-forward-port.md`
- Modify: `README.md`
- Modify: `docs/README.md`
- Modify: `docs/architecture/README.md`
- Modify: `docs/architecture/code-map.md`
- Modify: `docs/guides/README.md`
- Modify: `docs/migration/STATUS.md`
- Modify: `docs/migration/history/README.md`
- Modify: `docs/superpowers/plans/2026-08-13-yhc-desktop-workbench-forward-port.md`
- Modify: `docs/superpowers/plans/README.md`
- Modify: `docs/superpowers/specs/README.md`
- Modify: `docs/superpowers/specs/2026-08-13-yhc-desktop-workbench-forward-port-design.md`
- Modify: `quality/publication.yaml`
- Modify: `PUBLICATION_MANIFEST.json`

**Interfaces:**

- Current architecture owns the verified runtime and trust boundaries.
- The user guide owns local install, launch, provider setup, history/import,
  interaction, recovery, and unsigned-package expectations.
- History owns closeout evidence; active plan/spec become historical only after
  all local and remote acceptance gates pass.

- [ ] **Step 1: Run current-tree acceptance before writing claims**

```bash
make desktop-check
make desktop-audit
make node-sbom
make node-license-check
go test ./server/appserver ./engine ./engine/session ./engine/transcript ./tools ./cmd/yhc/cmd -count=1
go test -race ./server/appserver ./engine ./engine/session ./engine/transcript ./tools -count=1
make test-contract
make test-pty
make test-e2e
make build
make publication-safety
```

Expected: all portable gates pass on the same tree. Record any unavailable
live-provider, signing, notarization, or cross-platform package checks as
separate limitations rather than successes.

- [ ] **Step 2: Perform physical macOS application acceptance**

Launch the freshly built unsigned application and verify: provider setup;
new-session send/cancel; assistant Markdown headings/lists/table/code; history
selection without backend activation; first-send attach; legacy import
attestation; ordinary permission; AskUserQuestion; Plan review/revise;
repeated-tool intervention; bounded Activity; Changes; reconnect; quit with an
active turn; backend restart invalidating the prior capability; and clean
relaunch. Capture screenshots/log digests outside tracked source.

Expected: every listed interaction is usable from the packaged UI. Signing and
notarization remain explicitly not accepted.

- [ ] **Step 3: Write only source- and acceptance-backed documentation**

Describe the actual app-server authority flow, canonical state admission,
renderer trust boundary, typed interactions, semantic Activity, dependency
evidence, launch/use/recovery commands, and unsigned limitation. Add symbol
references to current owners. Do not cite the private branch or claim a public
release.

```bash
make docs-check-strict
make publication-safety
```

Expected: links, lifecycle, skills, terminology, publication classification,
and privacy checks pass.

- [ ] **Step 4: Close plan lifecycle and commit**

Change this plan and design to `historical`, add completion date, remove the
live required-skill block, update indexes/status/history, and regenerate the
manifest only after Steps 1-3 pass.

```bash
git diff --check
make change-plan
make verify-focused
git add README.md PUBLICATION_MANIFEST.json docs quality/publication.yaml
git commit -m "docs: publish YHC desktop workbench guidance"
make verify-merge
make change-evidence-ready
```

Expected: the committed tree is clean and all diff-bound evidence is current.

- [ ] **Step 5: Independent review and PR**

Review the final diff against the accepted design along four axes: runtime and
session safety, Electron/Web security, Desktop usability/appearance, and public
provenance/supply chain. Fix findings, rerun the affected focused gate and full
merge evidence, push only `codex/feat/desktop-workbench`, and open one ready PR.

Expected remote result: `Required gates`, CodeQL, publication safety, and the
Desktop supply-chain job all succeed. Do not merge the PR or publish a release
under this plan.

## Rollback and failure boundaries

- Before push, rollback is ordinary topic-branch commit reversion; never reset
  or rewrite unrelated public work.
- If shared runtime regressions appear, keep the Desktop entrypoint disabled and
  revert the smallest typed-contract commit rather than weakening TUI/ACP
  tests.
- If canonical admission or import cannot be proven, retain history-only mode
  and disable attach/import routes. Never fall back to direct legacy resume.
- If Node license, vulnerability, integrity, or provenance clearance fails,
  source may remain under review but no package or PR may be described as
  complete.
- If packaged physical acceptance fails, fix the behavior and rebuild from the
  final tree. Passing Node unit tests alone is not a Desktop usability result.
- A failed signing or notarization check is expected for this slice and blocks
  distribution claims, not local unsigned QA source acceptance.
