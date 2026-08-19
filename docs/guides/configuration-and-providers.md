# Configuration and Providers

**Status:** current
**Last verified:** 2026-08-20

> **Ownership:** production configuration sources, precedence, provider selection, and runtime settings

## Know which loader owns each file

The current production entrypoints do not use one universal settings merge.

| Concern | Files and merge behavior |
|---|---|
| Provider, model, UI, max turns | `~/.claude/settings.json`, then `<project>/.claude/settings.json` |
| Permission rules | local, project, and user rules are all aggregated; matching actions resolve `deny` > `ask` > `allow` |
| MCP servers | `~/.claude/mcp_servers.json`, project `.claude/mcp_servers.json`, then nearest `.mcp.json` |

Important: `settings.local.json` is consulted for permission rules, but the CLI
and ACP composition roots currently do **not** load its provider, model, base
URL, theme, or max-turn fields. Put those fields in project settings or pass a
flag/environment override.

## Current runtime settings

These fields are consumed by at least one CLI or ACP composition path:

```json
{
  "provider": "openai",
  "model": "gpt-4o",
  "api_base_url": "https://api.openai.com/v1",
  "fallback_model": "openai:gpt-4o-mini",
  "model_aliases": {
    "fast": "openai:gpt-4o-mini"
  },
  "max_turns": 0,
  "custom_system_prompt": "Follow the repository instructions.",
  "permission_mode": "default",
  "theme": "dark",
  "reduced_motion": false,
  "goal": {
    "enabled": true
  }
}
```

`theme` and `reduced_motion` are TUI presentation settings; the ACP server does
not consume them. Provider, model, base URL, max-turn, prompt, and permission
fields are resolved by the composition roots described below.

`goal.enabled` defaults to true in supported production composition roots. It
exposes commands in saved-root TUI or Plain Sessions and also gates dedicated
Headless Goal and negotiated ACP; set it to `false` as the kill switch. The
project intentionally ships no numeric Goal budget. To set a host default cap,
configure a positive value explicitly:

```json
{
  "goal": {
    "enabled": true,
    "default_token_budget": 200000
  }
}
```

Without `default_token_budget`, `/goal <objective>` becomes active immediately
and emits its initial turn. Nil means only the Goal token limiter is disabled;
provider and usage accounting still accumulate. A zero configured budget is
invalid, and setting a positive budget later is measured against already
committed usage. This setting does not activate Goal in ordinary headless,
unnegotiated ACP, child/review, administration, ephemeral, or standalone MCP
contexts. Negotiated ACP still requires its private Goal capability and an
explicit continue.

`max_turns: 0` means unlimited. The schema contains additional fields, but do
not assume a field changes production behavior merely because it unmarshals;
verify its composition-root wiring before documenting or operating it.

## Provider resolution is field-specific

Provider and model are resolved together with ranked sources:

1. `--provider` / `--model`
2. `PROV` / `PROV_MODEL`
3. merged project-over-user settings

A qualified model such as `openai:gpt-4o` also selects its provider at the
model source's rank. A higher-ranked provider can discard an incompatible
lower-ranked model; equal-ranked conflicts return an error. If no provider is
selected, model-name detection, provider-specific API keys, and stored
credentials are tried in deterministic provider order.

Other fields use these exact orders:

| Field | High to low precedence |
|---|---|
| API key | `--api-key`, `PROV_API_KEY`, selected provider's key variables, `~/.claude/credentials.json` store |
| Base URL | `--base-url`, `PROV_BASE_URL`, `api_base_url`, selected provider's base-URL variable, provider default |
| Fallback model | `--fallback-model`, `PROV_FALLBACK_MODEL`, `fallback_model` |
| Max turns | explicitly supplied `--max-turns`, `CLAUDE_MAX_TURNS`, `max_turns` |
| Permission mode | `-y`/`--yolo`, `--permission-mode`, `permission_mode`, `default` |

There is no API-key field in the production settings schema. Use an environment
variable, `--api-key`, or an already populated compatible credential store. The
project owns no interactive provider-authentication flow, so `/login` is
hidden and non-mutating.

Use `/config` to inspect the effective provider/model route, credential
presence, endpoint origin, fallback model, permission mode, and per-field
winning sources. It never renders a credential value or suffix, and it removes
endpoint userinfo, path, query, and fragment. Use `/doctor` for stable-ID
runtime/provider/transcript/settings checks. The read-only doctor does not test
provider connectivity; enable `--provider-preflight` when a network/auth probe
is required.

For scripts or startup diagnosis without a conversation runtime, use:

```bash
yhc config show --output-format json
yhc doctor
```

These commands reuse the same redacted snapshot and stable doctor check IDs
without constructing `provider.Runtime` or sending a provider request.
`config show` fails closed when effective settings cannot be loaded; `doctor`
still returns its ordered check set and marks the invalid settings source as a
failure. Connectivity remains skipped rather than inferred. Because this
inspection process has no active conversation, `session.transcript` is also
`skipped`; use the in-session `/doctor` command when transcript health is part
of the diagnosis.

## Provider environment variables

| Provider | Key variables | Base URL variable |
|---|---|---|
| Anthropic | `ANTHROPIC_API_KEY` | `ANTHROPIC_BASE_URL` |
| OpenAI | `OPENAI_API_KEY` | `OPENAI_BASE_URL` |
| Gemini | `GOOGLE_API_KEY`, `GEMINI_API_KEY` | `GOOGLE_BASE_URL` |
| DeepSeek | `DEEPSEEK_API_KEY` | `DEEPSEEK_BASE_URL` |
| Qwen | `DASHSCOPE_API_KEY`, `QWEN_API_KEY` | `QWEN_BASE_URL` |
| Ark | `ARK_API_KEY` | `ARK_BASE_URL` |

Generic variables are `PROV`, `PROV_MODEL`, `PROV_API_KEY`, `PROV_BASE_URL`,
and `PROV_FALLBACK_MODEL`. When `PROV` is set, generic key/base-URL values apply
only to that selected provider.

## CLI flags

| Flag | Scope |
|---|---|
| `--provider`, `--model`, `--api-key`, `--base-url`, `--fallback-model`, `--provider-preflight` | Root interactive/`-p`, `exec`, `resume`, and `serve acp` model setup |
| `--permission-mode`, `-y`/`--yolo`, `--max-turns`, `--tools` | Root interactive/`-p`, `exec`, `resume`, and `serve acp` runtime policy |
| `--mouse` | Root TUI and `resume`; use `--mouse=false` to disable |
| `-p`/`--print`, `--plain`, `--resume`, `--output-format` | Root interaction/compatibility selection |
| `--resume`, `--output-format` | `exec` session selection and text/JSON output |
| `--output-format` | Scoped independently to `sessions`, `config`, `doctor`, `mcp`, and `plugins` administration trees |

No runtime flag is persistent. Put it after the command that consumes it, such
as `yhc exec --model gpt-4o "prompt"` or
`yhc serve acp --model gpt-4o`. `serve mcp`, `version`, and
`completion` intentionally reject provider, permission, and tool flags instead
of accepting no-op configuration.

## Change model and reasoning effort safely

Use `/model list` to inspect known model metadata and `/model PROVIDER:MODEL`
to request a route change. The active provider runtime resolves the route
before mutation; an invalid provider/model leaves the current model and effort
unchanged. Client construction for a newly selected valid route remains lazy.

`/effort [level]` is shown only when the resolved active model metadata and its
adapter share at least one exact reasoning value. Run `/effort` to see the
current model's choices; the list is not a global enum. For example, DeepSeek
V4 Pro/Flash currently expose `default`, `none`, `high`, and `max`. `none`
disables thinking, while `high` and `max` enable thinking and send that exact
effort.

This controls provider request reasoning, not the local continuation token
budget. The selected value is checkpointed with the active model binding. An
incompatible manual model switch or Session resume clears it visibly; an
in-flight failover instead skips a candidate that cannot preserve the exact
value. YHC does not silently clamp one effort to another.

## Restrict model-visible built-ins

```bash
go run ./cmd/yhc --tools Read,Grep,Glob
go run ./cmd/yhc --tools ''
```

Omitting `--tools` exposes the default built-in pool. Supplying an empty value
disables built-ins. This selector does not remove dynamically registered MCP
tools; control those with permission rules or MCP configuration.

## Non-provider runtime variables

| Variable | Current effect |
|---|---|
| `YHC_PROVIDER_PREFLIGHT` | Enable startup preflight when truthy |
| `YHC_SIMPLE` | Use the simple tool pool and disable persistent memory |
| `YHC_REDUCED_MOTION`, `YHC_ACCESSIBILITY` | Reduce TUI motion when truthy |
| `YHC_DISABLE_MOUSE` | Disable TUI mouse tracking when truthy |
| `YHC_DISABLE_ACP_ASSISTANT_MESSAGE_IDS` | Omit optional ACP assistant message IDs without changing chunk content |
| `YHC_DISABLE_ACP_COMMAND_UPDATES` | Disable ACP command-list notifications without changing dispatch |
| `YHC_DISABLE_AUTO_MEMORY` | Disable automatic persistent memory |
| `YHC_REMOTE_MEMORY_DIR` | Select the remote-memory base directory |
| `YHC_CONFIG_DIR` | Override the user configuration directory |
| `YHC_MEMORY_PATH_OVERRIDE` | Select one validated absolute automatic-memory path |
| `YHC_TEAM_MEMORY_DIR` | Select one validated absolute team-memory path |
| `YHC_SESSION_CATALOG` | Override the session-root catalog path |
| `YHC_PERMISSION_REVIEW_AUDIT_DIR` | Override the permission-review audit directory |

Each canonical `YHC_*` name also accepts its exact `EINO_AGENT_*` legacy alias.
When both are present, the canonical name wins even when its value is empty or
invalid; the setting owner then applies its normal validation or default.

CLI and TUI boolean helpers accept `1`, `true`, `yes`, and `on`,
case-insensitively. The memory-directory owner preserves its narrower grammar:
`YHC_DISABLE_AUTO_MEMORY` and the memory effect of `YHC_SIMPLE` accept only
`1`, `true`, and `yes`.

## Maintainer reference

| Concern | Source |
|---|---|
| Production settings merge | [`config.go`](../../engine/config/config.go), [`root.go`](../../cmd/yhc/cmd/root.go) |
| Provider field precedence | [`resolver.go`](../../engine/provider/resolver.go) |
| Runtime model and effort controls | [`execution_controls.go`](../../engine/execution_controls.go) |
| Redacted configuration and doctor diagnostics | [`diagnostics.go`](../../engine/diagnostics.go), [`cmd_diagnostics.go`](../../engine/commands/cmd_diagnostics.go) |
| Provider-free diagnostic CLI | [`diagnostics_extensions.go`](../../cmd/yhc/cmd/diagnostics_extensions.go), [`inspection_administration.go`](../../engine/inspection_administration.go) |
| Provider environment map | [`provider_detect.go`](../../engine/model/provider_detect.go) |
| Architecture | [Platform](../architecture/platform/README.md) |
