# Blind Defect Workflow Cases

These cases validate investigation routing without disclosing a known owner,
cause, repair, or prior implementation. Keep child output ephemeral or under
ignored `build/iteration/`; never commit prompts, transcripts, session IDs, or
raw process output.

## permission-late-write

- **Fixture and setup:** Run the test-owned permission-rejection scenario with
  a barrier that can hold the final write-capable action until cancellation and
  release it only while the test waits for process quiescence.
- **Symptom:** A rejected write appears after terminal cancellation.
- **Allowed artifacts:** Sanitized lifecycle categories, target-file metadata,
  barrier transitions, bounded process status, and the first failure log path.
- **Oracle:** The target file remains absent after barrier-controlled
  quiescence.

## resume-duplicate-dispatch

- **Fixture and setup:** Complete one test-owned real-binary turn that writes a
  deterministic marker, then resume the same session from a fresh process
  against a scripted provider that counts requests.
- **Symptom:** The resumed process sends the same tool call twice.
- **Allowed artifacts:** Provider request count, sanitized tool-event
  categories, exact marker hash and line count, bounded process status, and the
  first failure log path.
- **Oracle:** Provider request count has no duplicate dispatch, and the marker
  hash and line count remain exact.

## pty-child-leak

- **Fixture and setup:** Launch the CLI in a test-owned PTY, start a child
  process, capture the initial termios state and child PID, then cancel the CLI
  and wait through the bounded cleanup barrier.
- **Symptom:** The CLI exits, but a child remains and terminal mode is not
  restored.
- **Allowed artifacts:** PTY bytes and modes, child PID liveness, bounded
  process status, cleanup transitions, and the first failure log path.
- **Oracle:** The child PID disappears and the final termios state equals the
  captured initial state.
