# Terminal Output Resilience Verification

**Status:** verification
**Last verified:** 2026-07-17

> **Ownership:** reproducible P15 terminal-writer, teardown, late-frame, PTY,
> runtime-state, and bounded-cleanup verification procedure

## Result

P15.0 reproduced deterministic blocked-writer shutdown and silent writer-error
handling in Bubble Tea v1.3.10. P15.1 closed the production-path gate with one
unbuffered, synchronously acknowledged `TerminalOutput` writer. At most one
copied packet is in flight; the first sink failure is surfaced once; and
write, drain, and platform interruption have typed bounded deadlines.

The direct P15.0 Bubble Tea probes remain red dependency characterization: an
unwrapped blocked `io.Writer` still blocks Bubble Tea. The equivalent
production seam now fails closed, stops the writer before fallback restore,
and emits no bytes after that boundary. No renderer replacement, project frame
queue, or lossy runtime-event coalescing was introduced.

## Reproduction

Run the bounded adapter and production-path probes:

```bash
go test -race ./internal/tui -run '^TestTerminalOutput' -count=20
go test -race ./cmd/eino-agent/cmd -run '^(TestP151|TestTUITerminalRestorationPTY)$' -count=10
```

Retain the direct Bubble Tea dependency characterization and lossless
runtime-state probes:

```bash
go test -race ./cmd/eino-agent/cmd -run '^TestP150' -count=10
go test -race ./engine -run '^TestP150' -count=10
```

On Unix, the first command also runs the real PTY fixture. It pauses the PTY
reader after startup, holds a sustained streaming/tool/Agent frame at the
writer boundary, requests normal quit, then releases and drains the frame. A
parent shell prints `P150_SHELL_USABLE` only after the child returns.

Repository closeout additionally runs:

```bash
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c ./internal/tui -o /tmp/eino-agent-p15-tui-windows.test
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c ./cmd/eino-agent/cmd -o /tmp/eino-agent-p15-cmd-windows.test
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go test -c ./internal/tui -o /tmp/eino-agent-p15-tui-linux.test
go test ./internal/tui -run '^$' -bench '^BenchmarkTerminalOutputFastSink$' -benchmem -count=5
make fmt
make lint
make test
make build
make docs-check
go run ./scripts/migration_manifest.go check
git diff --check
```

## Scenario Matrix

| Scenario | Deterministic result | Classification |
|---|---|---|
| Direct Bubble Tea graceful quit/panic/suspend with one frame held | Dependency probe remains blocked until release; restore follows the frame | `dependency_blocked_shutdown` |
| Wrapped production path with one frame held | Write timeout signals once, kills the program, interrupts/drains the sink, then fallback restore runs only after `Stopped` | `bounded_fail_closed` |
| Concurrent adapter callers | All packets reach exactly one physical sink writer; no project queue exists beyond one in-flight packet | `single_writer_bounded` |
| Writer remains blocked after interrupt | Close returns typed write, drain, and interrupt timeout categories; `Stopped` remains false until release | `bounded_abandon_diagnostic` |
| Unix duplicate descriptor fills a pipe | Netpoll deadline stops the writer and original descriptor nonblocking state is restored | `platform_interruptible` |
| Writer returns an error | Bubble Tea performs further writes and returns success without exposing the sink error | `silent_writer_error` |
| Wrapped writer returns an error | The first error closes one failure signal, later writes reuse it, and program cleanup returns one diagnostic | `surfaced_once` |
| Resize followed by rapid invalidation while the writer is held | resize reaches the renderer boundary and the next invalidation backpressures | `bounded_backpressure` |
| Progress sent after `Program.Run` returns | `Program.Send` is a no-op and produces no new bytes | `late_frame_rejected` |
| Paused Unix PTY reader plus sustained progress | restore suffix is complete and precedes the parent-shell sentinel | `drained_after_release` |
| Permission request and terminal event with presentation skipped | runtime snapshot retains terminal identity and unresolved attention | `lossless_runtime_state` |

The categories are the golden evidence. Wall-clock duration is an environment
observation only and is not used as expected test text.

## Evidence Boundary

The portable barrier writer deterministically models a blocked terminal sink;
it does not claim that every real terminal will fill its kernel PTY buffer at
the same byte count. The Unix fixture adds real raw-mode, alternate-screen,
mouse, focus, bracketed-paste, and parent-shell restoration evidence while
keeping the blocked-write point channel-controlled.

The probes establish both Bubble Tea's dependency behavior and the project
adapter boundary:

- the standard renderer stores one replaceable frame and performs terminal
  writes from its renderer goroutine;
- stop/kill flush or lock that renderer before terminal restoration;
- `TerminalOutput` is the only production `io.Writer` passed to Bubble Tea;
- its unbuffered request channel and synchronous result limit project-owned
  pending output to one packet;
- Unix and Windows production sinks own interruptible duplicate descriptors or
  handles, leaving the original output available for ordered fallback cleanup;
- a platform writer that still cannot stop returns a typed interrupt-timeout
  diagnostic and forbids fallback restoration while `Stopped` is false;
- engine runtime reduction remains independent of presentation-frame delivery.

On the development Apple M5 Pro, five ordinary in-memory sink benchmark runs
were approximately 0.75 microseconds per packet with 442 bytes and six
allocations per operation. This is an observational regression check, not a
portable performance threshold.

Windows evidence is compile-time only in this environment. The build proves
the handle/thread API boundary, but a real Windows blocked-handle fixture has
not yet executed `CancelSynchronousIo`; that platform runtime interaction
remains residual release risk.

## Code Evidence

| Boundary | Code reference | Why it matters |
|---|---|---|
| Bounded single-writer owner | [`TerminalOutput`](../../../internal/tui/terminal_output.go#L114) | Owns one in-flight packet, first failure, typed deadlines, and close state. |
| Unix interruption | [`newTerminalOutputSink`](../../../internal/tui/terminal_output_unix.go#L22) | Duplicates the descriptor, enables deadline-driven nonblocking writes, and restores original flags. |
| Production entrypoint | [`runTUIProgram`](../../../cmd/yhc/cmd/root.go) | Kills Bubble Tea on writer failure and restores only after writer stop. |
| Adapter failure fixtures | [`TestTerminalOutputBlockedWriteTimesOutAndCloseInterrupts`](../../../internal/tui/terminal_output_test.go#L155) | Freezes one failure signal and typed write/drain/interrupt behavior. |
| Production fail-closed fixture | [`TestP151BlockedWriterFailsClosedAndRestoresAfterWriterStops`](../../../cmd/yhc/cmd/terminal_output_resilience_test.go#L483) | Proves bounded program termination and no post-restore output. |
| Portable writer and lifecycle probes | [`TestP150GracefulQuitHoldsFrameUntilWriteReleased`](../../../cmd/yhc/cmd/terminal_output_resilience_test.go#L257) | Freezes quit, panic, release/resume, writer-error, backpressure, and late-send categories. |
| Unix PTY and parent shell | [`TestP150SlowPTYRestoresParentShellAfterSustainedProgress`](../../../cmd/yhc/cmd/terminal_output_resilience_unix_test.go#L101) | Exercises the supported PTY lifecycle with deterministic reader/writer barriers. |
| Lossless runtime projection | [`TestP150RuntimeSnapshotsSurviveSkippedPresentationFrames`](../../../engine/terminal_output_resilience_test.go#L5) | Proves terminal and unresolved-attention truth survives presentation omission. |

The current implementation boundary is documented in
[`terminal-lifecycle.md`](../../architecture/tui/contracts/terminal-lifecycle.md);
the completed correction contract is retained in
[`p15-terminal-output-resilience.md`](../history/runtime/p15-terminal-output-resilience.md).
