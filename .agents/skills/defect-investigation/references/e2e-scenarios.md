# Sanitized E2E Scenario Catalog

Use this catalog to select an end-to-end boundary after a deterministic
reproduction exists. Each scenario must run in a test-owned temporary directory
or explicitly named disposable fixture. Never reuse or mutate a real user
session.

## Provenance and Privacy

A local structural audit on 2026-08-02 inspected administration metadata for 57
sessions associated with this project from a 505-entry scanned catalog. It did
not copy session titles, prompts, responses, credentials, environment values, or
raw transcript records.

The compatibility transcript store was sampled only by structural event and
tool names. Common tool counts were Bash 19, Read 11, Edit 9, TaskUpdate 5,
TaskCreate 3, TaskOutput 2, and Agent 2. Common structural record counts were
assistant 124, user 74, attachment 25, last-prompt 24, permission-mode 21, mode
21, file-history-snapshot 20, system 16, and queue-operation 6. These dated
counts justify the scenario shapes below; they are not a product success metric
or a claim about all sessions.

Future audits must keep the same boundary: prefer `sessions list
--output-format json`, aggregate only necessary structure, and never commit raw
session content.

## Executable Mapping

| Scenario | Automated owner | Primary command | Supplementary only |
|---|---|---|---|
| S1 Read-Edit-Test Loop | `scripts/e2e` | `go test ./scripts/e2e -run '^TestReadEditTest$' -count=1` | No; live providers and OS containment remain outside this oracle. |
| S2 Task and Child-Agent Lifecycle | None: the catalog has prose but no named executable oracle | None | No automation claim. |
| S3 Permission and Mode Transition | `scripts/e2e`, `engine` | `go test ./scripts/e2e -run '^TestPermissionRejectedNoWrite$' -count=1`; `go test -race ./engine -run '^TestPermissionCoordinatorClassifierWinsUserRaceExactlyOnce$' -count=1` | Cross-entrypoint mode coverage remains scenario-specific. |
| S4 Queue, Follow-Up, and Restart | `scripts/e2e` | `go test ./scripts/e2e -run '^TestSessionResumePreservesExactToolResult$' -count=1` | Queued-follow-up ordering has no catalog-wide executable oracle. |
| S5 File History and Session Replay | `engine/session`, `cmd/yhc/cmd` | `go test ./engine/session -run '^TestP234aReplaySnapshotMatchesResumeAndIsMutationIsolated$' -count=1`; `go test ./cmd/yhc/cmd -run '^TestSessionsCLILifecycleUsesCanonicalServiceWithoutTUI$' -count=1` | Destructive operations require test-owned storage. |
| S6 Cancellation, Recovery, and Compaction | `scripts/e2e` | `go test ./scripts/e2e -run '^TestCancellationTerminatesOwnedTreeWithoutWrite$' -count=1`; `go test ./scripts/e2e -run '^TestFailoverDisposition$' -count=1` | Compaction-specific behavior has no catalog-wide executable oracle. |
| S7 Session CLI Lifecycle | `cmd/yhc/cmd` | `go test ./cmd/yhc/cmd -run '^TestSessionsCLILifecycleUsesCanonicalServiceWithoutTUI$' -count=1` | Export remains excluded unless disclosure is the contract. |
| S8 ACP Replay and Stdio MCP Lifecycle | `server/acp` | `go test ./server/acp -run '^(TestP234bACPReplayProjectionPreservesOrderBytesAndToolFacts|TestP235ACPStdioLoadResumeAndExactActiveReconnect)$$' -count=1` | No desktop claim. |
| S9 TUI Terminal Lifecycle and Workflow | `cmd/yhc/cmd`, `internal/tui` | `make test-pty` | Physical terminal appearance is not covered. |
| S10 UI-Only Desktop Check | None | None | Computer Use for font, clipboard, focus, and window integration only after its structured or PTY gate passes. |

`TestMalformedToolInput` is the S2 malformed-input oracle:
`go test ./scripts/e2e -run '^TestMalformedToolInput$' -count=1`. These commands
are focused oracles; route final change gates through `make verify-focused`,
then commit, then `make verify-merge` before evidence handoff.

The [blind forward-test cases](forward-test-cases.md) validate the investigation
route without embedding a known cause or repair. Escalate each case in this
order:

```text
typed/runtime state -> package contract -> real process -> PTY bytes/modes
-> Computer Use only for remaining OS/window/pixel claim
```

The regression command or risk pack is not “merge evidence.” Persisted merge
evidence comes only from the exact committed-plan `make verify-merge`
lifecycle; `make verify-deep` remains first-failure diagnosis.

## S1 Read-Edit-Test Loop

- **Entrypoint:** plain/headless query or `QueryEngine` with a scripted model.
- **Setup:** disposable Git repository containing one localized defect and a
  deterministic test command.
- **Action:** read the owner, edit only the allowed file, run the focused test,
  and report the result.
- **Oracle:** expected file delta, allowed-write set, tool/event order, test exit
  status, and terminal reason.
- **Cleanup:** close streams and remove the temporary repository.

When the symptom fits its frozen boundary, reuse the opt-in P43
`localized-write-fix/v1` baseline through `make eval-baseline` instead of
building a second repository harness. It proves the public headless binary,
scripted-provider outcome, allowed writes, cleanup, usage, and redacted report;
it does not cover recovery, live providers, other entrypoints, or OS sandboxing.

## S2 Task and Child-Agent Lifecycle

- **Entrypoint:** root query with TaskCreate/TaskUpdate/Agent or equivalent
  scripted tools.
- **Setup:** two bounded child tasks with explicit dependency and one rejected
  or cancelled child path.
- **Action:** start, update, wait, cancel or finish, and reconcile the parent
  view.
- **Oracle:** one authoritative lifecycle transition per task, no orphaned
  child, deterministic dependency order, and no child completion mistaken for
  parent acceptance.
- **Cleanup:** cancel and wait for every child; close progress streams.

## S3 Permission and Mode Transition

- **Entrypoint:** plain, TUI, ACP, and child Agent where applicable.
- **Setup:** deterministic allow-once, exact allow-always, reject, timeout, and
  concurrent classifier/user decisions.
- **Action:** request permission while changing or restoring mode.
- **Oracle:** exactly one winner, stable public decision, correct persistence
  scope, no secret-bearing payload, and no post-reject execution.
- **Cleanup:** release prompts/semaphores and remove temporary policy state.

## S4 Queue, Follow-Up, and Restart

- **Entrypoint:** TUI/plain runtime with queued user input and a fresh-process
  resume path.
- **Setup:** an active turn, one queued follow-up, interruption, and a durable
  session fixture.
- **Action:** enqueue, stop or exit, restart, and inspect replay.
- **Oracle:** stable queue-operation order, no automatic redispatch on replay,
  no duplicate user input, and an explicit terminal reason.
- **Cleanup:** close the program and delete only the test-owned catalog and
  transcript.

## S5 File History and Session Replay

- **Entrypoint:** `yhc sessions` plus the canonical session service.
- **Setup:** temporary catalog, transcript, file-history snapshot, and optional
  corrupt or mismatched record.
- **Action:** list and resume; add fork/delete only when the test owns all
  artifacts.
- **Oracle:** stable JSON envelope, mutation-isolated replay, fail-closed corrupt
  input, no erroneous registration/persistence, and no replay-with-dispatch.
- **Cleanup:** close the service and remove every test-owned artifact. Never run
  destructive session commands against a real catalog.

## S6 Cancellation, Recovery, and Compaction

- **Entrypoint:** `QueryEngine` with blocking tools and scripted recoverable or
  terminal model errors.
- **Setup:** cancellation barrier, bounded recovery budget, and trace recorder.
- **Action:** cancel during tool execution or trigger context-limit recovery.
- **Oracle:** descendant context cancellation, bounded compact/retry count,
  ordered public events, no late side effect, and one terminal settlement.
- **Cleanup:** release blocked goroutines and close all readers/tools.

## S7 Session CLI Lifecycle

- **Entrypoint:** real `yhc sessions list/resume/rename/fork/delete`
  process against a temporary catalog.
- **Setup:** test-owned sessions with parent/branch metadata and isolated storage
  environment.
- **Action:** exercise structured JSON administration one operation at a time.
- **Oracle:** schema version, canonical service parity, deterministic IDs and
  cleanup flags, typed usage failures, and no prompt/transcript content in
  diagnostics.
- **Cleanup:** delete only the temporary root and wait for the process. Export is
  excluded unless content disclosure is the contract under test.

## S8 ACP Replay and Stdio MCP Lifecycle

- **Entrypoint:** ACP new/load/resume with a test MCP stdio subprocess.
- **Setup:** seeded public transcript projection, active-session identity, and
  delivery or subprocess failure injection.
- **Action:** load, invoke a tool, reconnect, close, and exercise rollback.
- **Oracle:** ordered public text/tool facts, exact active reconnect, no private
  state leakage, no registration after delivery failure, and child cleanup.
- **Cleanup:** close ACP/MCP streams, terminate and wait for subprocesses, and
  remove the temporary session store.

## S9 TUI Terminal Lifecycle and Workflow

- **Entrypoint:** real Bubble Tea binary under a Unix PTY.
- **Setup:** bounded terminal dimensions and scripted paste, mouse/focus, resize,
  EOF, cancel, and panic paths.
- **Action:** run a representative workflow, resize, exit, and observe raw
  terminal protocol bytes.
- **Oracle:** paired alternate-screen/paste/focus/mouse modes, cursor restore,
  no sticky input/selection state, correct child exit, and bounded final frame.
- **Cleanup:** restore the terminal, close the PTY, terminate if needed, and wait
  for the child even on assertion failure.

## S10 UI-Only Desktop Check

- **Entrypoint:** the real terminal application or IDE controlled through
  Computer Use.
- **Setup:** disposable session, known font/theme/window size, and no private
  content on screen.
- **Action:** inspect only the remaining physical claim: font fallback, pixel
  clipping, OS clipboard permission, focus, or window integration.
- **Oracle:** explicit human-visible expected/actual observation paired with the
  deterministic state or PTY test that already passed.
- **Cleanup:** close the disposable window/session and restore clipboard or app
  focus when the check changed them.

Computer Use evidence is supplementary and environment-specific. It must never
upgrade a missing deterministic regression into a verified fix.
