# Interaction Modes and Commands

**Status:** current
**Last verified:** 2026-08-07

> **Ownership:** supported entrypoint selection and user-visible command projection differences

## Pick an entrypoint

The examples use `go run`; replace it with your platform binary after building.

| Mode | Command | Use it for | Current limitation |
|---|---|---|---|
| TUI | `go run ./cmd/yhc` | Full interactive terminal experience | Requires interactive stdin/stdout |
| Plain REPL | `go run ./cmd/yhc --plain` | Line-oriented terminals and logs | TUI panels/composer actions degrade to text or are unavailable |
| Headless | `go run ./cmd/yhc exec "prompt"` | One prompt in scripts | No interactive permission prompt; use `-y` only when bypass is intended |
| Bounded Goal run | `go run ./cmd/yhc goal run --resume SESSION_ID --max-continuations 8` | Continue an existing saved Goal in automation | Goal must already exist and be enabled; no prompt/stdin objective or interactive answer |
| Resume in TUI | `go run ./cmd/yhc resume SESSION_ID` | Continue a saved session | Resolves catalogued roots for the current project; duplicate IDs fail |
| Resume in plain | `go run ./cmd/yhc --plain --resume SESSION_ID` | Text-only continuation | Same current-project constraint |
| Session administration | `go run ./cmd/yhc sessions list` | List/resume/rename/export/fork durable sessions without a TUI or provider | Current-project scope; archive/delete are absent |
| Diagnostics administration | `go run ./cmd/yhc config show` / `doctor` | Inspect redacted effective configuration and stable checks without a model runtime | Connectivity is deliberately not probed |
| Extension administration | `go run ./cmd/yhc mcp list` / `plugins validate` | Inspect configured MCP and plugin generations without entering a conversation | MCP health is unprobed; plugin reload is inspection-process local |
| ACP server | `go run ./cmd/yhc serve acp` | IDE clients over stdio | Client owns the interactive surface |
| MCP server | `go run ./cmd/yhc serve mcp` | Expose built-in tools over stdio | Tools only; bypasses `QueryEngine` |

Pipe a generated prompt to headless mode:

```bash
printf '%s\n' 'inspect the failing tests' | go run ./cmd/yhc exec -
```

`exec` reads stdin when no prompt is supplied or when the prompt is `-`. If a
positional prompt and piped stdin are both present, stdin is appended as an
explicit `<stdin>` block. Root `-p`/`--print` remains compatible with the same
runtime, but a root positional prompt without `-p` is now a usage error instead
of being ignored.

Use `--output-format text` for human output or `--output-format json` for one
schema-versioned result object. Stdout has one renderer owner; progress and
redacted diagnostics stay on stderr. Exit codes are `0` complete, `1` runtime
failure, `2` usage/validation, and `130` cancelled.

Runtime flags are command-local. Put them after the selected subcommand, for
example `yhc exec --provider openai "prompt"` or
`yhc goal run --resume SESSION_ID --max-continuations 8 --provider openai`
or `yhc serve acp --provider openai`. `serve mcp`, `version`, and
`completion` reject model/runtime flags because those entrypoints do not use
them.

Use the dedicated process only for an existing saved Goal:

```bash
yhc goal run \
  --resume SESSION_ID \
  --max-continuations 8 \
  --output-format json
```

It consumes no prompt or stdin. Exit `0` means the durable Goal is
`complete`; paused, blocked, budget/usage limited, waiting, not-runnable, or a
continuation limit return `1`; usage errors return `2`; cancellation after
durable stop handling returns `130`. JSON stdout is one versioned `goal_run`
object. Ordinary `exec` and `-p` still execute exactly one prompt.

The `sessions` tree has its own `--output-format text|json` flag and stable
`0`/`1`/`2`/`130` exits. It builds a provider-free administration host solely
to reuse the engine-owned session service, so list/rename/export do not start a
model runtime. `sessions resume` restores and reports then exits; use
`exec --resume` to continue with a prompt.

`config`, `doctor`, `mcp`, and `plugins` likewise own scoped
`--output-format text|json` flags and the same stable exit taxonomy. They do
not accept provider/model/runtime flags. `config show` and `doctor` construct
no provider runtime; `mcp list|get` never connect or launch configured servers;
`plugins validate` does not replace the live candidate, and `plugins reload`
replaces only the generation in that short-lived CLI process.

Disable mouse tracking with `--mouse=false` or
`YHC_DISABLE_MOUSE=1`. The TUI enables an alternate screen; use
`--plain` when terminal ownership is undesirable.

## Slash commands

Use `/help` for the registry's visible list and `/help NAME` for syntax. The
list is filtered by the active entrypoint and typed availability. Prefer these
verified outcomes:

| Task | Commands |
|---|---|
| Conversation | TUI/plain/headless: `/new`, `/clear`, `/compact`, `/sessions`, `/resume SESSION_ID`, `/fork`; old `/history`, `/rename`, and `/export` shortcuts are removed; use `yhc sessions ...` for provider-free administration or root `resume SESSION_ID` for the TUI |
| Runtime | `/model`, `/plan`, `/effort`; `/status`, `/context`, `/usage`, `/config`, and `/doctor` share one source-derived diagnostic snapshot |
| Interactive Goal | In a saved-root TUI or Plain Session unless `goal.enabled: false`, `/goal`, `/goal OBJECTIVE`, `/goal edit OBJECTIVE`, `/goal pause`, `/goal resume`, `/goal clear`, and `/goal budget POSITIVE_TOKENS`; `/goal OBJECTIVE` starts immediately even with no budget, and no shipped numeric budget is implied |
| Workspace | `/diff [stat|full|staged]`, `/files`, `/add-dir PATH`; `/init [--force]`; `/memory status` plus explicit `edit project`, `delete project`, or `migrate project` |
| Bundled workflows | TUI/plain: `/commit`, `/review`; `/help NAME` shows bundled source and trust. Removed `/summary`, `/onboarding`, `/pr-comments`, `/issue`, and `/commit-push-pr` (`/cpr`) return migration guidance rather than a prompt |
| Safety and diagnostics | `/permissions mode ...`, `/permissions bypass confirm`, `/permissions rules ...`; TUI-only `/terminal`; diagnostic fields always show state, source, and observation time and never expose credential values |
| Agents and tasks | `/tasks` labels its durable WorkBoard source and read-only command control; `/agents` is execution inspection; `/agent`, `/team`, `/queue`, and `/search` are TUI-owned |
| Extensions | `/mcp` for inspection, `/skills`, `/reload-plugins`; `/mcp add` does not yet synchronize model-visible tools |

The stable aliases are `/reset` for `/clear`, `/exit` for `/quit`, `/ctx` for
`/context`, and `/teams` for `/team`; they do not emit deprecation warnings.
Expired `/stats`, `/cost`, `/settings`, `/allowed-tools`, and `/bashes` names
return replacement guidance without executing `/usage`, `/config`,
`/permissions`, or `/tasks`. Removed `/login`, `/mode`, top-level `/bypass`,
and `/yolo` are non-discoverable tombstones; configure provider credentials
through their owning environment/configuration and use the typed
`/permissions` forms. `/new` is a canonical command: it commits a new durable
empty session and preserves the source transcript.

## Projection differences matter

- The TUI handles panels, selectors, theme/vim changes, queue UI, fork
  selection, and permission dialogs. Session export and fork persistence remain
  engine-service owned.
- Ctrl+T, Ctrl+B, `/team`, activity, sidebar, thread detail, and queued-input
  cancellation all use one bounded `TaskExplorerSnapshot` and exact
  engine-declared actions. Plain/headless and ACP add no explorer mutation
  surface; standalone MCP has only per-server ephemeral Task/Todo state.
- Goal is a saved-root TUI/Plain workflow enabled by default in supported
  production composition roots, with a separate explicit bounded automation
  consumer. Its command mutations,
  provider usage, budget, completion/blocker guards, and continuation
  admission remain engine-owned. The TUI status line projects reducer state;
  Plain labels automatic continuation and lifecycle progress in text while
  one stdin broker gives completed user input and permission/Plan interaction
  precedence. EOF or cancellation durably pauses active automatic work.
  `/goal` commands remain absent from both headless entrypoints. The dedicated
  `goal run` process can see `get_goal`/`update_goal` only during an exact
  admitted Goal turn and can only consume the dedicated cursor. Ordinary
  headless, unnegotiated ACP, child/review, ephemeral, disabled, and standalone
  MCP contexts retain no Goal authority. A negotiated ACP client still uses
  private capability negotiation and explicit continue rather than automatic
  dispatch.
- Plain mode and TUI project the same typed engine-owned command result for
  supported runtime mutations. TUI-only panels and selectors remain explicitly
  scoped.
- TUI, plain, headless, and ACP project the same five diagnostic command
  results. Unknown token, cost, credential, or connectivity facts stay
  unavailable; no entrypoint substitutes a local estimate.
- TUI, plain, headless, and ACP also share engine-owned `/diff`, `/files`, and
  explicit `/add-dir`. `/diff` reports non-Git, unavailable, failed, timed-out,
  and cancelled runner outcomes rather than returning an empty success.
  `/files` omits transcript-observed paths outside active workspace roots;
  `/add-dir` stores the canonical real directory for the current durable
  session and reports nested duplicates without broadening scope.
- `/copy [N]`, `/keybindings`, `/terminal`, and `/search` are TUI-only.
  `/copy` is hidden when the TUI is not interactive and selects only completed
  assistant responses; it does not create a backup file. `/terminal-setup` has
  been removed because it never performed setup.
- `/init` and project-scoped `/memory edit|delete|migrate` return follow-up
  prompts. Git and filesystem work then uses the ordinary Agent tools and the
  active permission flow; the slash handlers do not write configuration,
  launch an editor, or delete files directly. Plain mode can run these prompt
  workflows; headless and ACP do not discover them.
- `/commit` and `/review` are versioned bundled prompt content, not privileged
  Go handlers. They enter the same atomic generation as configured plugin
  prompt commands, remain subject to ordinary tool permissions, and are absent
  when the bundled pack is disabled. A malformed bundled or configured reload
  retains the previous whole generation instead of partially replacing help
  or dispatch. Removed defaults return typed guidance, while qualified
  configured-plugin workflows remain independent.
- `/help` and the TUI help overlay group all currently reachable primary and
  secondary commands by category. The empty command palette shows at most
  three process-local Recent commands followed by the twelve ordered Suggested
  commands; typing searches the complete reachable set. Recent entries are
  recorded only after fresh admission and are never persisted.
- While a TUI request is running, discovery exposes only `/agent`, `/team`,
  `/keybindings`, and `/queue`. Direct dispatch rechecks the live phase, so a
  command selected from an idle snapshot cannot execute after the turn starts.
- Headless `exec` and compatibility `-p` pass through the same `QueryEngine`,
  registry, and typed result projection, but discovery intentionally exposes
  only its small supported command set. `exec` is prompt execution, not a
  second administration runtime.
- ACP protocol resume/load/fork remains supported. ACP slash `/new`, `/resume`,
  and `/fork` fail before mutation because the protocol host owns the external
  session-handle map; `/clear` and `/compact` remain engine-owned. Protocol
  fork registers a separate child handle only after durable restore. ACP
  advertises model/effort configuration and permission modes from the engine
  capability result, and projects successful changes through protocol updates.
- ACP uses `QueryEngine` per ACP session. Standalone MCP directly dispatches
  registry tools and has no conversation, compaction, transcript, or slash
  command runtime.

## Removed commands in the current build

P21.0 and P21.2 moved retired names out of the active command catalog.
The separate catalog contains 27 canonical tombstones and reserves 33
canonical-or-alias keys. Dispatching any reserved key returns typed removal
and replacement guidance with `ActionNone`; it never invokes the replacement
or mutates runtime state. In particular, `/logout` deletes no credentials, and
`/plugin`, `/color`, `/fast`, `/tag`, `/share`, `/release-notes`, `/rewind`,
`/undo`, `/branch`, and `/rewrite` are not supported workflows. Use `/fork` or
version control instead of treating them as recovery primitives.

The current command matrix is owned by
[`commands.md`](../architecture/capabilities/commands.md). The source-backed
P16 comparison is retained in
[`command-surface-audit.md`](../migration/reference/runtime/command-surface-audit.md);
the closed simplification program is recorded in
[`P21 history`](../migration/history/runtime/p21-command-surface-simplification.md).
The default
bundled pack contains only `/commit` and `/review`; the five removed canonical
workflows plus `/cpr` return P21.2 migration guidance.

## Maintainer reference

| Concern | Source |
|---|---|
| Cobra dispatch | [`root.go`](../../cmd/yhc/cmd/root.go), [`headless.go`](../../cmd/yhc/cmd/headless.go), [`headless_goal.go`](../../cmd/yhc/cmd/headless_goal.go), [`diagnostics_extensions.go`](../../cmd/yhc/cmd/diagnostics_extensions.go) |
| Command registry | [`registry.go`](../../engine/commands/registry.go) |
| Workspace command service | [`workspace_commands.go`](../../engine/workspace_commands.go), [`execution_controls.go`](../../engine/execution_controls.go) |
| Entrypoint projections | [`app.go`](../../internal/tui/app.go), [`root.go`](../../cmd/yhc/cmd/root.go), [`agent.go`](../../server/acp/agent.go) |
| Architecture | [Runtime](../architecture/runtime/README.md) |
