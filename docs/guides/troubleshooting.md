# Troubleshooting

**Status:** current
**Last verified:** 2026-07-15

> **Ownership:** symptom-oriented diagnosis for supported entrypoints and known current limitations

## `./yhc`: no such file

`make build` writes platform-specific paths under `build/`. Run the matching
binary, for example:

```bash
./build/darwin-arm64/yhc --help
```

Or bypass the build artifact while developing:

```bash
go run ./cmd/yhc --help
```

## Provider could not be determined

Select one provider and inspect only the variables relevant to it:

```bash
export PROV=openai
export OPENAI_API_KEY='replace-me'
go run ./cmd/yhc --provider-preflight -p "reply with ok"
```

If several provider keys are exported, `PROV` or `--provider` removes
auto-detection ambiguity. A provider-qualified model such as
`--model openai:gpt-4o` can select both fields. `/login`, `/settings`, and
`/doctor` provide interactive diagnostics, but never paste unmasked keys into a
bug report.

## A local setting has no effect

The production CLI/ACP roots use only user and project `settings.json` for
provider/model/UI/max-turn fields. `settings.local.json` is currently loaded for
permission rules, not those general fields. Move the field to project settings
or use a flag/environment variable, then restart.

## Headless denies a tool

This is expected when an invocation reaches the missing interactive prompt.
Choose one:

1. Add a narrow allow rule, such as `Bash(go test ./...)`.
2. Restrict model-visible built-ins with `--tools`.
3. Use `-y` only inside a trusted isolation boundary.

Workspace `Read`, `Grep`, and `Glob` paths can be allowed deterministically even
without `-y`. See [Permissions and safety](permissions-and-safety.md).

## No tools are visible

Check whether `--tools ''` was supplied or a blanket deny rule hides the tool.
Use `/status` and `/permissions` in an interactive mode. `--tools` names are
built-in registry names; MCP tools are added separately after connection.

## An MCP server or tool is missing

- Use the JSON key `mcpServers` in `.mcp.json` or `mcp_servers.json`.
- Confirm the configured command exists in `PATH`.
- Run `/mcp`; then try `/mcp restart NAME`.
- Remember that one connection failure is silently skipped so startup can
  continue. Run the server command directly to inspect its own stderr.
- Restart the engine if a server's tool list changed. Manager refresh updates
  its dynamic list but does not re-register the first-class registry entries.

## A plugin command is missing

- Confirm the layout is `.claude/plugins/DIRECTORY/plugin.json` or the user-level
  equivalent.
- Ensure the manifest `name` and each command `name` are non-empty.
- Run `/reload-plugins` and inspect its error.
- Invoke `/PLUGIN:COMMAND`. `/plugin` is registered, but currently prints only
  static help; it does not list, install, or uninstall plugins. Directory
  configuration plus `/reload-plugins` remain the operational management path.
- Only prompt commands are production-wired. Plugin skills/hooks/MCP declarations
  will not appear through current bootstrap.

## Resume cannot find a session

- Run from the project whose `.eino-agent/transcripts/` directory contains the
  session JSONL.
- Verify the session ID from the exit hint, `/sessions list`, or
  `yhc sessions list`.
- `YHC_SESSION_CATALOG` relocates only the root catalog, not transcripts.
- If the JSONL is partially corrupt, the loader skips malformed lines and uses
  recoverable entries; inspect warnings before continuing destructive work.

## `/undo` or `/rewind` did not restore what you expected

`/undo` is unavailable and returns before mutating the conversation because no
durable reversible-history contract exists. `/rewind` has no production
`FileTracker` and cannot restore files. Use git or another backup system for
file recovery.

## Terminal behavior is degraded

Use `/terminal` in the TUI. `TERM=dumb`, non-TTY stdin/stdout, SSH, and terminal
capability detection can disable interactive features. Switch to `--plain` for
line-oriented operation; set `YHC_DISABLE_MOUSE=1` if mouse tracking
interferes with selection.

## Diagnostics map

| Diagnostic | Answers |
|---|---|
| `/doctor` | Stable-ID checks for settings files, provider route/credential presence, transcript, tools, and permissions; connectivity is explicitly skipped |
| `/status` | Source-derived provider, model, tools, usage coverage, transcript, and session state |
| `/context` | Active message/tool contributors plus known provider input usage and model window |
| `/usage` | Persisted provider token totals and missing-metadata coverage; no generic money estimate |
| `/config` | Redacted effective provider/config values, per-field source, and precedence; removed `/settings` returns migration guidance without invoking `/config` |
| `/terminal` | TTY and capability degradation |
| Removed `/bug` | Explicitly reports that no project-owned delivery channel exists; use the project issue or support channel |
| `--provider-preflight` | Startup provider credential/connectivity check |

Standalone `serve mcp` logs tool calls through the standard logger on stderr.

## Maintainer reference

| Concern | Source |
|---|---|
| CLI errors and headless rendering | [`root.go`](../../cmd/yhc/cmd/root.go), [`headless.go`](../../cmd/yhc/cmd/headless.go) |
| Diagnostics commands | [`diagnostics.go`](../../engine/diagnostics.go), [`cmd_diagnostics.go`](../../engine/commands/cmd_diagnostics.go), [`registry.go`](../../engine/commands/registry.go) |
| MCP initialization | [`mcp_tool.go`](../../tools/mcp_tool.go), [`config.go`](../../engine/mcp/config.go) |
| Architecture | [Runtime](../architecture/runtime/README.md) |
