# P46.2 Observable Attempt Discard and Switch Implementation Plan

> **Historical use:** Retained as completed implementation evidence; do not
> execute this checklist as current work.

**Status:** historical
**Created:** 2026-08-06

> **Ownership:** test-first implementation steps for the accepted P46.2/G37
> failed-attempt disposal and cross-entrypoint fallback-visibility slice

**Goal:** Make every successful overload switch observable as one explicit
failed-attempt disposal followed by one safe fallback notice, without changing
canonical assistant output, transcripts, structured headless results, or
failover authority.

**Architecture:** The existing model-attempt coordinator remains the sole
runtime owner. After it has proved that the next candidate is admissible and
constructable, it emits `discarded`, an exact tombstone only for retractable
output, and the next `started` event before provider dispatch. Runtime state
records those typed facts. TUI, Plain, Headless, and ACP project only a bounded
`started` event with `attempt_index > 0`; library callers keep the typed events
without a forced writer.

**Tech Stack:** Go 1.26.5, Bubble Tea notifications, Cobra Plain/Headless
writers, ACP `_session/status`, white-box engine and adapter tests, Unix PTY,
migration queue, and Makefile gates.

## Global Constraints

- Execute only P46.2/G37; do not add adaptive health, scoring, cooldowns, or a
  second routing owner.
- Preserve `candidate_skipped`, `started`, `retry_wait`, `failed`, and
  `committed`; add `discarded` only for a switch-eligible attempt with a
  constructable successor.
- Emit `discarded -> tombstone-if-output -> next started -> next dispatch`.
  A switched attempt must not also emit `failed`.
- Preserve first-visible-output commitment for Plain, Headless, ACP, and
  library entrypoints. Only the TUI may retract partial output and switch.
- Notices expose only the normalized fallback profile and bounded switch
  count. Never expose API model, provider endpoint, account, credential, raw
  error, prompt, or failed output.
- Notices are presentation only: never append them to assistant history,
  canonical projection, transcript, tool output, or structured headless
  result.
- Preserve unrelated `PROJECT_GUIDE.md` and `artifacts/` worktree content.
- Final verification uses `make fmt`, `make lint`, `make test`, and
  `make build`, plus docs, manifest, diff, focused race, and Unix PTY gates.

---

## Task 1: Prove and implement the engine-owned disposal lifecycle

**Files:**

- Modify: `engine/events.go`
- Modify: `engine/model_failover.go`
- Modify: `engine/query_fallback_test.go`
- Modify: `engine/p29_4_runtime_state_test.go`
- Modify: `engine/testdata/canonical_trace/retry_fallback.golden.json`

**Interfaces:**

- Consumes: `modelAttemptCoordinator.nextSwitchCandidate`, `discard`,
  `startCandidate`, `RuntimeStateStore`, and the existing P29.4 budgets.
- Produces: `ModelAttemptDiscarded`; no configuration, provider, or durable
  Session schema change.

- [x] **Step 1: Add failing production-path ordering tests**

Add focused engine tests for zero-output and retractable partial-output
overload switches. Record both yielded event markers and provider-dispatch
markers from the synchronous `Query` seam. Require:

```text
zero output: started -> discarded(never_started) -> started -> dispatch fallback
partial TUI output: assistant -> discarded(discarded) -> tombstone -> started -> dispatch fallback
```

Require the discarded event to retain the failed attempt identity, typed
`overloaded` failure, provider-call count, and switch count before the next
attempt starts. Require no `failed` phase for that attempt and no tombstone for
zero output.

- [x] **Step 2: Run focused red**

```bash
go test ./engine/ -run '^(TestP462|TestP294OverloadRetriesThenSwitchesThroughOneCoordinator)$' -count=1
```

Expected: FAIL because the current coordinator emits tombstone then `failed`.

- [x] **Step 3: Add the minimal phase and reorder disposal**

Add `ModelAttemptDiscarded = "discarded"`. Change `discard` to emit the
discarded attempt first with `never_started` or `discarded`, then emit the exact
tombstone only when output was offered. Keep `runCanonicalModelRound` ordering
unchanged so the successor `started` event still precedes its dispatch.

- [x] **Step 4: Pin reducer and replay truth**

Add a deterministic reducer/replay fixture that records the discarded attempt,
exact tombstone, and next started attempt with contiguous identity. Assert the
old attempt output is removed and the bounded event ring preserves the
discarded phase without dispatching any work.

- [x] **Step 5: Run focused engine green and race tests**

```bash
go test ./engine/ -run '^(TestP462|TestP294)' -count=1
go test -race ./engine/ -run '^(TestP462|TestP294)' -count=1
```

## Task 2: Project one safe notice across supported entrypoints

**Files:**

- Modify: `internal/tui/app.go`
- Modify: `internal/tui/p29_4_failover_test.go`
- Modify: `cmd/eino-agent/cmd/root.go`
- Modify: `cmd/eino-agent/cmd/root_test.go`
- Modify: `cmd/eino-agent/cmd/headless.go`
- Modify: `cmd/eino-agent/cmd/cli_contract_test.go`
- Modify: `server/acp/agent.go`
- Modify: `server/acp/agent_test.go`

**Interfaces:**

- Consumes: a safe `EventModelAttempt` whose phase is `started`, attempt index
  is positive, profile is normalized, and switch count is positive.
- Produces: a TUI warning notification, one identical Plain/Headless stderr
  notice, and one ACP `_session/status` update with status `model_fallback`.

- [x] **Step 1: Add failing adapter contract tests**

Use synthetic typed events to require the following:

- TUI removes only the exact tombstoned attempt, then creates one warning when
  the next attempt starts; the notice is not a chat item.
- Plain writes the notice only to stderr; stdout remains exact assistant text.
- Headless writes the same notice only to stderr; `headlessResult.Output` and
  JSON/text result output remain unchanged.
- ACP emits exactly one `_session/status` extension and zero assistant chunks
  for the fallback event.
- Primary starts, discarded/failed events, candidate skips, nil/incomplete
  payloads, and unsafe profile fixtures produce no notice.

- [x] **Step 2: Run adapter red**

```bash
go test ./internal/tui/ -run '^TestP462' -count=1
go test ./cmd/eino-agent/cmd/ -run '^TestP462' -count=1
go test ./server/acp/ -run '^TestP462' -count=1
```

- [x] **Step 3: Implement presentation-only projections**

Use one small safe-notice helper for Plain and Headless because they share a Go
package. TUI and ACP independently validate the typed start boundary and use
their existing notification/status owners. Do not synthesize assistant events,
transcript messages, or structured result fields.

- [x] **Step 4: Run adapter green and representative-width checks**

```bash
go test ./internal/tui/ -run '^(TestP462|TestP294)' -count=1
go test ./cmd/eino-agent/cmd/ -run '^TestP462' -count=1
go test ./server/acp/ -run '^TestP462' -count=1
```

## Task 3: Pin the Unix terminal boundary

**Files:**

- Create: `cmd/eino-agent/cmd/p46_2_failover_notice_pty_unix_test.go`

- [x] **Step 1: Add a subprocess PTY fixture**

Run a helper process under an 80-column PTY. Feed the Plain adapter one fallback
start plus assistant and terminal events. Assert one bounded fallback line, one
assistant payload, a usable completion marker, and absence of an API model,
endpoint, raw overload error, and secret fixture. Unit tests remain the owner
of stdout/stderr separation because a PTY combines both descriptors.

- [x] **Step 2: Run the focused PTY test**

```bash
go test ./cmd/eino-agent/cmd/ -run '^TestP462PlainFallbackNoticePTY$' -count=1
```

PTY evidence proves terminal protocol/process behavior only, not physical font
or pixel rendering.

## Task 4: Close G37 and the P46 repair

**Files:**

- Modify: `docs/architecture/platform/model-providers.md`
- Modify: `docs/architecture/tui/contracts/runtime-events.md`
- Create: `docs/migration/verification/p46-2-observable-failover.md`
- Modify: `docs/migration/verification/README.md`
- Create: `docs/migration/history/runtime/p46-2-observable-failover.md`
- Modify: `docs/migration/history/README.md`
- Modify: `docs/migration/REMAINING.md`
- Modify: `docs/migration/queue.yaml`
- Modify generated: `docs/migration/PLAN.md`
- Modify: `docs/migration/plans/p46-model-failover-repair.md`
- Modify: `docs/migration/plans/README.md`

- [x] **Step 1: Synchronize only changed fact owners**

Describe the explicit discarded lifecycle and presentation boundaries, add
verification/history records, remove G37 and P46.2, and render the now-empty
active queue. Do not claim live-provider, physical-terminal, or remote-CI
acceptance.

- [x] **Step 2: Run iteration closeout and all gates**

```bash
make fmt
make lint
make test
make build
make docs-check
go run ./scripts/migration_manifest.go check
git diff --check
```

- [x] **Step 3: Commit, push, review, and squash-merge the atomic slice**

Stage only P46.2 source, tests, plan, and owned documentation. Commit with:

```bash
git commit -m "fix: expose model failover disposal and switch"
```

Create one ready PR with the `preserve` decision, compatibility, rollback,
local evidence, and the user's remote-CI usage-limit exception. Merge only this
slice through the protected branch.
