# Non-Claude Reference Sync, 2026-08-24

**Status:** reference-snapshot
**Snapshot:** 2026-08-24, Asia/Shanghai
**Adoption:** `defer`

> **Ownership:** fast-forward evidence and source-backed change summary for the
> non-Claude repositories under `.reference/`. This snapshot does not own YHC
> product scope, current behavior, or execution order.

All five existing non-Claude references fast-forwarded to their configured
upstream, and `deepseek-harness` was added as a clean sixth comparison source.
The existing `.reference/codex/.yhc/transcripts/` files were not tracked by
upstream, did not overlap the incoming tree, and remain untouched. No reset,
forced update, or Claude Code Ripe operation was performed.

## What changed in this interval

| Repository | Range and interval | Diff inventory | Source-backed update themes |
|---|---|---:|---|
| Codex | `279b93242cfe..2161ec272a7d`, 2026-08-11 to 2026-08-23 | 596 commits; 1,967 files; +170,665/-39,900 | gRPC code-mode sessions; interrupted-turn recovery and thread queues; rollout/thread identity separation; parallel tool-call admission; Guardian, approval, and MCP hardening |
| Crush | `feb63006e945..61ce7c038af0`, 2026-08-10 to 2026-08-23 | 49; 79; +2,234/-523 | parsed-command approval checks; non-blocking MCP startup; exit-time Session ID; follow-to-bottom and text selection; provider/model restore |
| Grok Build | `b13fa526f511..07b2f7144fd5`, 2026-08-11 to 2026-08-23 | 10; 1,255; +247,606/-131,383 | chat-state compaction and queues; ACP subagent/permission routing; worktree safety and GC; MCP/session workflows; pager and TUI state |
| OpenCode | `0d927ba03f36..03bba464d46f`, 2026-08-11 to 2026-08-23 | 153; 228; +14,079/-4,297 | jittered bounded retry; restored request headers; run-mode subagent permission handling; surfaced child tool failures; small-model compaction structure |
| Pi | `2a9b4ebc6800..a69bef789bc9`, 2026-08-11 to 2026-08-23 | 126; 236; +7,959/-2,151 | compaction routing and tool-free summaries; usage notices; Session-scoped thinking level; bundled Node runtime; failed extension-factory cleanup |

Counts include tests, generated files, lockfiles, and release artifacts. They
measure the Git range, not implementation size or product quality.

### Codex

- `1e557a554e` adds `codex-rs/code-mode/src/grpc_session/` and focused gRPC
  conversion/deadline tests.
- `363427b5e3` changes `core/src/session/{mod,handlers,turn_input}.rs` and adds
  interrupted-turn submission coverage.
- `9341b38310` extends the app-server v2 thread protocol with experimental
  queue APIs and notifications.
- `4ef836f883` separates rollout identity from thread identity in the recorder,
  state database, and rollout filename owner.
- `86b1123ff6` removes model-name gating from parallel tool-call admission.

These commits make the host/session protocol the most relevant part of this
range for YHC. They do not prove that YHC should reproduce Codex app-server.

### Crush

- `945c518f` moves automatic command approval to parsed-command evidence and
  adds `safe_test.go` regressions.
- `c78cc674` lets interactive startup proceed while slow MCP initialization
  finishes behind the coordinator gate.
- `804324fb` exposes the Session ID at exit; `25bf6a25` and `08552fb9` add
  follow-layout and textarea-selection coverage.

The reusable lesson is explicit permission and readiness state, not the TUI
implementation itself.

### Grok Build

Every range commit is named `Synced from monorepo`, so an atomic feature cannot
be attributed to a public commit. Endpoint source and tests nevertheless show
substantial changes under `xai-chat-state`, `xai-grok-shell/src/session`,
`xai-grok-pager/src/app/acp_handler`, and `xai-fast-worktree`. This repository
is useful as architecture evidence only; the public history cannot establish
feature intent or issue provenance.

### OpenCode

- `c78986831c` adds jitter and an upper bound to Session retry, with focused
  retry tests.
- `0033bb3559` restores Session request headers after compaction.
- `08faeb3893` lets the non-interactive run process answer subagent permission
  requests.
- `35fe5b7212` prevents task-tool failures from being swallowed.
- `dab2637217` changes compaction prompts and tests for smaller models,
  including DeepSeek V4 Flash.

### Pi

- `58302d34e` routes compaction through the selected Session model path.
- `90305d90a` disables tools during branch summarization and adds regression
  coverage.
- `496185f6e` adds `/thinking` selection; `7d4c0e05d` packages the Node runtime
  and validates distribution behavior.
- `a69bef789` discards partial extension-factory state after failure.

Pi's production coding-agent path remains separate from its new generic
`AgentHarness` scaffold; the latter is not evidence of a working harness.

## New DeepSeek Harness reference

`.reference/deepseek-harness` now points at clean upstream `master` commit
`b150a551b8d465e31e418e1b2eaf5e79bbb7d28e`, tag `dsh-v0.1.1-rc.2`, dated
2026-08-21. It is an MIT-licensed TypeScript/pnpm workspace with CLI, web,
Python SDK/runtime, host/preset packages, append-only Session services, ACP,
and native subprocess containment components. Its architecture and Pi
comparison are owned by the
[DeepSeek Harness, Pi, and platform audit](deepseek-harness-pi-platform-audit.md).

## Evidence limits

- The synchronization closure is `HEAD...@{u} = 0/0` for all six repositories.
- This audit inspected commits, implementation, and focused tests; it did not
  run any upstream repository's full build, test suite, provider E2E, or PTY
  acceptance.
- Grok Build's public sync commits prevent reliable feature-level provenance.
- A reference change does not create a YHC gap without a reproduced user
  outcome and accepted contract.

## Recommendation

**`defer`: keep this range as a time-scoped evidence snapshot.** Only the
separately accepted P52.1 headless lifecycle stream becomes YHC scope; the
remaining upstream changes require their own observable problem and adoption
decision.
