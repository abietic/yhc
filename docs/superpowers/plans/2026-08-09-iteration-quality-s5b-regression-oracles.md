# Iteration Quality S5B Regression Oracles Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `$defect-investigation` for
> first-failure ownership and test-first repair, then `$iteration-workflow` for
> committed evidence.

**Status:** active-plan
**Created:** 2026-08-09
**Plan state:** Ready after executed F0; B1 is the first regression slice

> **Ownership:** test-first sequence for the four missing regression oracles in
> the
> [S5 completion design](../specs/2026-08-09-iteration-quality-s5-completion-design.md)

**Goal:** Detect queue ordering, compaction restart, child completion, and
terminal process-tree regressions through independent durable or real-process
results before future iterations can merge them.

**Architecture:** engine tests own durable ordering and restart contracts;
`scripts/e2e` owns real-binary provider/tool composition; Unix PTY tests own
terminal modes and process liveness. Tests cross existing production seams and
do not export implementation helpers.

**Tech stack:** Go standard `testing`, Eino test models, the existing scripted
provider, JSONL transcripts, Unix PTY and termios APIs, process PIDs, and current
Make risk packs. No new test framework.

## Frozen invariants

- Every test observes durable transcript/model input, a real child PID, or raw
  PTY state. Logs and internal flags are diagnostics only.
- First run is preserved. If unchanged production already passes an independent
  oracle, commit a coverage addition and do not invent a repair.
- A failure is localized to one owner before production changes.
- A foreground child dispatches once and its synchronous parent tool result is consumed once before restart. The parent transcript persists exactly one `AgentCompletionReceipt`. Each resumed input may preserve one historical result; no-redelivery means no additional durable receipt/message, duplicate call ID in one input, or child redispatch.
- Compaction never loses the newest tail or directly re-injects the replaced
  old history after restart; existing failure fallback remains intact.
- PTY tests use a real binary and real process group and always restore/close/
  wait during cleanup.
- Computer Use and live providers are supplemental, never replacements.
- Start only after `make change-evidence-ready` exists and is the shared
  closeout assertion.

## PR B1: queued follow-up ordering

### Task 1: write the durable oracle

Create `engine/queued_followup_ordering_test.go` with
`TestQueuedFollowUpStartsAfterPriorTerminalAndPersistsOnce`.

- [ ] Block turn 1 at a deterministic model barrier, enqueue one follow-up,
  then release turn 1.
- [ ] Read the persisted transcript through the supported session owner and
  assert the terminal assistant result precedes exactly one follow-up user
  message.
- [ ] Capture turn 2 model input and assert it starts only after turn 1 terminal
  state and contains the follow-up once.
- [ ] Add a late old-stream event after terminal and prove it cannot reorder or
  duplicate the follow-up.

```bash
go test ./engine -run '^TestQueuedFollowUpStartsAfterPriorTerminalAndPersistsOnce$' -count=1 -timeout=30s
```

### Task 2: classify the first result

- [ ] If the test passes unchanged production, add its exact name to
  `make test-contract` and change no runtime code.
- [ ] If it fails, preserve the failure, identify whether
  `QueryEngine.EnqueueUserInput`, runtime claim/commit, or the TUI terminal
  handoff owns the divergence, then repair only that owner.

```bash
make test-contract
make test-race
```

- [ ] Run all repository gates, commit this behavior alone, and produce current
  merge evidence. Finish with `make change-evidence-ready`.

## PR B2: compaction across a fresh engine

### Task 1: write the restart oracle

Create `engine/query_autocompact_recovery_test.go` with
`TestAutoCompactRestartUsesDurableBoundaryNotOriginalHistory`.

- [ ] Force auto-compaction with a small deterministic context window and
  distinct sentinel text in old history, summary, newest tail, and new prompt.
- [ ] Close the first engine, reload the same durable session into a fresh
  engine, and submit the new prompt.
- [ ] Assert the persisted boundary/summary count, summary and newest-tail
  survival, and absence of the raw pre-compaction sentinel from the resumed
  model input.
- [ ] Retain a failure-path case proving compactor failure preserves original
  history rather than truncating it.

```bash
go test ./engine -run '^TestAutoCompactRestartUsesDurableBoundaryNotOriginalHistory$' -count=1 -timeout=30s
```

### Task 2: route and close B2

- [ ] Add the test to `make test-contract` when stable.
- [ ] Change production only if the independent restart boundary fails; keep
  persistence/reload as the seam and do not add a test-only export.

```bash
make test-contract
make test-fault-injection
make fmt
make lint
make test
make build
```

- [ ] Commit and verify this behavior independently from B1.
- [ ] Finish with `make change-evidence-ready` on the committed head.

## PR B3: Agent completion once through the real binary

### Task 1: extend only the hermetic script protocol

- [ ] Preserve v1 fixtures and their global `scenario.tools` equality check.
  Add a strict v2 provider script with separate `parent` and `child` request
  lanes, each owning one exact `expect_tools` set and an ordered step list.
- [ ] Route a request to a lane only by exact tool-set equality and reject
  missing or ambiguous matches before consuming a step. Never reuse the parent
  set for a child request or silently fall back to the global set.
- [ ] Freeze the scenario/root set as `Agent`, `Bash`, `Glob`, `Grep`, and
  `Read`. Invoke the child as built-in `Explore`; its accepted set is the
  same list without `Agent`. Resumed parent requests must match the root set
  again. A production tool-set drift fails the fixture; it is not learned
  dynamically.
- [ ] Give the two lanes deterministic test-owned barriers so the background
  child completes durably before the parent process exits, without depending
  on global cross-lane request order or sleeps.
- [ ] Add loader negatives for missing/duplicate/unknown tools, invalid lane
  names, duplicate tool-set matches, v1/v2 field mixing, and out-of-order steps
  within one lane.
- [ ] Journal only request index, sanitized lane, model, sorted tool names, and
  expected prior call IDs; never prompt, input body, transcript, or child
  output.

### Task 2: write the real-binary scenario

Add `TestAgentCompletionRealBinaryDeliversOnceAcrossRestart` under
`scripts/e2e`.

- [ ] Run a foreground child to a deterministic completion through the built
  CLI binary.
- [ ] Stop and restart the parent session using the supported session route.
- [ ] Inspect the durable parent transcript independently: assert one
  parent-visible foreground Agent tool result and one `AgentCompletionReceipt`.
  Assert one consumption in the next scripted provider request before restart.
- [ ] Assert fresh parent requests after each restart retain exactly one
  historical Agent result, cannot contain a duplicate call ID in one input,
  and cannot re-dispatch the child or add a durable receipt/message.
- [ ] Assert the journal contains the barrier-constrained parent/child/resumed-
  parent transitions with exact lane tool sets, so the test cannot pass by
  treating a child request as another parent turn.

```bash
go test ./engine -run 'TestCompletion.*Restart|TestForegroundChild' -count=1 -timeout=30s
make test-e2e
```

- [ ] If the scenario cannot request Agent through current CLI composition,
  stop as blocked and report the missing supported seam. Do not substitute a
  fake executor or exported test hook.
- [ ] Run the four repository gates and committed evidence before merging.
- [ ] Finish with `make change-evidence-ready`; a successful E2E command alone
  is not merge evidence.

## PR B4: PTY termios restoration and owned-child death

### Task 1: create the Unix real-binary oracle

Create
`cmd/eino-agent/cmd/terminal_child_liveness_pty_unix_test.go` with
`TestTUITerminalShutdownRestoresTermiosAndKillsOwnedShellTreePTY`.

- [ ] Start the built binary under a Unix PTY with a hermetic scripted provider
  that requests Bash.
- [ ] The Bash command writes a child PID and ready marker, then waits. Confirm
  with `kill(pid, 0)` that the child was alive before shutdown.
- [ ] Snapshot the relevant termios input/output/local flags on the PTY slave.
- [ ] Trigger the supported shutdown/cancellation path while Bash is active.
- [ ] Poll with a deterministic deadline until `kill(pid, 0)` returns ESRCH,
  then compare termios with the entry snapshot and retain existing terminal
  protocol-byte assertions.
- [ ] Cleanup always closes PTY descriptors and terminates/waits any surviving
  process before failing the test.

```bash
go test ./cmd/eino-agent/cmd -run '^TestTUITerminalShutdownRestoresTermiosAndKillsOwnedShellTreePTY$' -count=1 -timeout=30s
```

### Task 2: select the risk pack and repair only a proven owner

- [ ] Add the exact test to `make test-pty` and therefore the existing
  `make test-e2e` composition.
- [ ] If child liveness or termios fails, distinguish ShellManager cleanup,
  process-group ownership, signal handling, and terminal restoration before
  changing one owner.

```bash
make test-pty
make test-e2e
make test-risk
make fmt
make lint
make test
make build
```

- [ ] Commit B4 independently and report Unix coverage separately from Windows
  job-object tests and physical-terminal acceptance.
- [ ] Finish with `make change-evidence-ready` on the committed head.

## Completion

S5B is complete when each scenario is selected by its named risk pack, every
admitted first failure has one causal owner or is explicitly blocked, and the
committed diff passes `make change-evidence-ready`. A passing package helper
without the durable/real-process oracle does not complete the plan.
