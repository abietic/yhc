# Four-Project TUI Parity Harness

**Status:** verification
**Last verified:** 2026-07-16
**Acceptance:** M7.4
**Build tag:** `parity`
**Implementation:** `internal/tui/parity/`

> **Ownership:** reproducible multi-project PTY comparison procedure; current
> TUI contracts live in [`architecture/tui/README.md`](../../architecture/tui/README.md)

## Purpose

The harness executes terminal applications in a real PTY, feeds their output to
a VT emulator, waits on semantic text/stability conditions, captures normalized
frames, and emits pairwise structural diffs. It is comparison and compatibility
evidence, not product truth. Claude Code Ripe provides the broadest workflow
baseline in this fixture; Crush and Codex provide alternative UX and
architecture evidence. Adoption decisions remain governed by
[`PROJECT_DIRECTION.md`](../../../PROJECT_DIRECTION.md).

## Invocation Matrix

| Project | Default invocation | Isolation | Deterministic scope |
|---|---|---|---|
| `eino-agent` | built binary plus `--yolo` | fixed provider env and terminal size | all six scenarios |
| `claude-code-ripe` | `bun scripts/dev-cli.ts` | updater/telemetry disabled | all six scenarios |
| `crush` | local reference binary | fixed terminal size | all six scenarios |
| `codex` | installed CLI, `--no-alt-screen` | fresh `CODEX_HOME`, inherited API keys removed, startup update check disabled | logged-out `welcome_screen` only |

The Codex boundary is deliberate. An authenticated prompt/tool scenario would
depend on user credentials, remote model output, account feature flags, and
network timing. Those are not visual parity fixtures. `SupportsScenario`
therefore excludes Codex from prompt/help/command/multiline scenarios until a
deterministic local app-server/model fixture exists.

## Codex Determinism

`getCodexConfigWithHome` supplies:

```text
codex --no-alt-screen -c check_for_update_on_startup=false -C <project-root>
```

The driver removes `OPENAI_API_KEY`, `CODEX_API_KEY`, and `OPENAI_BASE_URL`
before applying an isolated `CODEX_HOME`. `NO_COLOR=1` and a fixed
`xterm-256color` identity reduce style variance. The deterministic test launches
Codex twice with separate homes and requires identical normalized captures. The
current captured surface is the three-choice sign-in/API-key onboarding screen;
no credential is submitted and no model request is made.

This implementation follows the local Codex source evidence:

- onboarding explicitly detects `OPENAI_API_KEY`;
- startup update behavior is controlled by `check_for_update_on_startup`;
- `CODEX_HOME` owns user config/auth state;
- `--no-alt-screen` is a supported CLI/TUI mode.

## Ordering and Normalization

- inherited environment removal happens before explicit overrides;
- projects and capture IDs are sorted before pairwise comparison so report order
  is deterministic;
- ANSI is stripped and trailing whitespace is normalized;
- project branding, including Codex, is replaced only for structural diffs;
- raw and plain captures remain available for debugging.

## Commands

Verify the deterministic Codex boundary:

```bash
go test -tags=parity ./internal/tui/parity \
  -run 'TestCodex(InvocationDeterministic|ParityEnvironment)' -count=1 -v
```

Inspect the captured Codex startup frame:

```bash
PARITY_PROJECT=codex PARITY_SCENARIO=welcome_screen \
  go test -tags=parity ./internal/tui/parity \
  -run TestCaptureSingle -count=1 -v
```

Override the binary without using the user's auth state:

```bash
go test -tags=parity ./internal/tui/parity \
  -run TestCodexInvocationDeterministic \
  -codex-bin /path/to/codex -count=1 -v
```

## Extension Rule

Do not expand Codex's supported scenario set merely because an authenticated
local CLI happens to pass. First provide an isolated deterministic transport or
model fixture, prove two-run normalized stability, prevent inherited secrets and
network-dependent update behavior, then update `SupportsScenario` and this
contract together.
