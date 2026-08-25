# Getting Started

**Status:** current
**Last verified:** 2026-07-28

> **Ownership:** prerequisites, build outputs, first provider setup, and first run

## Prerequisites

- Go 1.26.5, as declared by `go.mod`.
- An API key for one supported provider.
- A terminal for the default full-screen TUI.

## Run from source

Set a provider-specific key in the environment, then start the TUI:

```bash
export ANTHROPIC_API_KEY='replace-me'
go run ./cmd/yhc
```

Run one non-interactive prompt with the explicit `exec` command:

```bash
go run ./cmd/yhc exec "summarize this repository"
```

`-p`/`--print` remains a compatibility route. For scripts, `exec` also accepts
stdin and can emit one stable JSON result:

```bash
printf '%s\n' 'inspect the failing tests' | go run ./cmd/yhc exec -
go run ./cmd/yhc exec --output-format json "summarize this repository"
go run ./cmd/yhc version --output-format json
```

Machine output uses exit `0` for completion, `1` for runtime failure, `2` for
usage/validation, and `130` for cancellation. Diagnostics stay on stderr.

Do not put a real key in shell history. Environment variables are preferable to
`--api-key`, which may be visible in a process listing. `/login` reports masked
environment-key status and setup help; it is not an interactive key writer.

## Build binaries

```bash
make build
```

`make build` produces four platform binaries; it does not create `./yhc`:

```text
build/linux-amd64/yhc
build/darwin-amd64/yhc
build/darwin-arm64/yhc
build/windows-amd64/yhc.exe
```

For example, on Apple silicon:

```bash
./build/darwin-arm64/yhc --help
```

## Supported providers

| Provider aliases | Default model | API-key variables, in priority order |
|---|---|---|
| `anthropic`, `claude` | `claude-sonnet-4-6` | `ANTHROPIC_API_KEY` |
| `openai` | `gpt-4o` | `OPENAI_API_KEY` |
| `google`, `gemini` | `gemini-2.5-flash` | `GOOGLE_API_KEY`, `GEMINI_API_KEY` |
| `deepseek` | `deepseek-v4-flash` | `DEEPSEEK_API_KEY` |
| `qwen`, `dashscope` | `qwen-max` | `DASHSCOPE_API_KEY`, `QWEN_API_KEY` |
| `ark`, `volcengine` | `doubao-1.5-pro-32k` | `ARK_API_KEY` |

`PROV_API_KEY` is the generic, higher-priority key override for all providers.
Select explicitly when several provider keys are present:

```bash
export PROV=openai
export OPENAI_API_KEY='replace-me'
go run ./cmd/yhc --model gpt-4o
```

## Add project defaults

Create `.claude/settings.json` in the project:

```json
{
  "provider": "openai",
  "model": "gpt-4o",
  "max_turns": 0,
  "permission_mode": "default"
}
```

Then run from that project directory. Use `--provider-preflight` when you want
startup to test credentials and connectivity before accepting a prompt:

```bash
go run ./cmd/yhc --provider-preflight
```

## Next

- [Configuration and providers](configuration-and-providers.md)
- [Interaction modes and commands](interaction-modes-and-commands.md)
- [Permissions and safety](permissions-and-safety.md)

## Maintainer reference

| Concern | Source |
|---|---|
| Build outputs | [`Makefile`](../../Makefile) |
| Cobra flags and mode selection | [`root.go`](../../cmd/yhc/cmd/root.go) |
| Headless input/output contract | [`headless.go`](../../cmd/yhc/cmd/headless.go) |
| Shared version identity | [`buildinfo.go`](../../internal/buildinfo/buildinfo.go) |
| Provider defaults | [`provider_detect.go`](../../engine/model/provider_detect.go) |
| Architecture | [Platform](../architecture/platform/README.md) |
