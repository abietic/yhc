# P34.1 File-State Checkpoint Repair Verification

**Status:** verification
**Last verified:** 2026-07-31
**Scope:** P34.1 only

> **Ownership:** reproducible acceptance evidence for incremental file-state
> error propagation, one complete turn-local repair, replay, concurrency,
> terminal settlement, and unchanged successful tool semantics.

## Acceptance Evidence

| Boundary | Evidence |
|---|---|
| Successful path | Read, Edit, and Write each retain exact successful result bytes, execute once, append their cumulative snapshot, emit no full checkpoint, and reconstruct flags after restart. |
| Failure handoff | An injected durability-uncertain snapshot failure sets the exact turn's repair requirement after recorder return without changing the tool result or retrying the side effect. |
| Complete repair | Ordinary append cannot clear the requirement. One `state-checkpoint` commits messages, replacements, file state, and usage before successful settlement. |
| Failed repair | A failed full checkpoint emits `TerminalPersistenceError` and retains `transcriptCheckpointRequired`; it is never described as durable success. |
| Partial line | A deterministic closed-file write failure plus injected partial JSONL suffix is truncated before the complete boundary, leaving no corruption selected for replay. |
| Concurrency | Two concurrency-safe file tools prove both all-failure and first-failure/second-success outcomes coalesce into one full checkpoint with cumulative flags and no race. |
| Terminal paths | Hook stop, interrupt, max turns, and terminal model error cannot bypass an outstanding repair requirement. |
| Recovery | Constructor restart and explicit `ResumeSession` reconstruct the repaired cumulative file-state flags. |
| Ownership | QueryEngine alone selects append versus full checkpoint and holds no QueryEngine mutex across recorder I/O. Compatibility rewrite APIs are not repair owners. |
| Review | Independent persistence/concurrency review found no production defect and requested the added uncertainty, explicit-resume, and mixed-concurrency proofs. |

## Focused Commands

```text
go test ./engine -run '^TestP341' -count=1
go test ./engine/transcript -run 'Test(FileSnapshotWriteFailureRepairsPartialJSONLBeforeCheckpoint|MessageEncodeFailureRepairsPartialJSONLBeforeCheckpoint)' -count=5
go test ./engine -run '^(TestP341|TestQueryEngineNormalCheckpointsAppendMessagesWithoutFullStateDuplication|TestQueryEngineRepairsTransientInitialTranscriptFailureWithFullCheckpoint|TestQueryEngineSurfacesUnrepairedFinalTranscriptFailure|TestQueryEngineFlushesUserMessageBeforeModelErrorTerminal)$' -count=5
go test -race ./engine -run '^TestP341' -count=10
```

## Source Gates

```text
test -z "$(rg -n '_ = deps\\.Transcript\\.RecordFileHistorySnapshot' engine/tool_execution.go || true)"
test -z "$(rg -n 'ReplaceWithReplacements' engine/tool_execution.go engine/p34_1_file_state_checkpoint_test.go || true)"
```

The first gate prevents restoration of the ignored incremental error. The
second keeps compatibility rewrite APIs out of the repair implementation and
its proof.

## Repository Closeout

```text
make fmt
make lint
make lint-new
make test
make build
make docs-check
make docs-check-ci
go run ./scripts/migration_manifest.go check
git diff --check
```

All commands passed. GitHub Actions billing or usage failures may be waived
only after the exact job annotation proves that no runner started; they are
never described as green CI.
