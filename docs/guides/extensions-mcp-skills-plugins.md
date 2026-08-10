# Extensions: MCP, Skills, and Plugins

**Status:** current
**Last verified:** 2026-07-31

> **Ownership:** supported extension setup and the boundary between active and loader-only capabilities

These are three different extension paths. MCP adds external tools/resources,
skills add reusable prompts, and current plugin bootstrap adds slash-command
prompts.

## Connect MCP servers to conversations

Prefer a project-root `.mcp.json`:

```json
{
  "mcpServers": {
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "."],
      "env": {
        "EXAMPLE_TOKEN": "${EXAMPLE_TOKEN}"
      },
      "timeout": 60
    }
  }
}
```

The loader merges, from low to high precedence:

1. `~/.claude/mcp_servers.json`
2. `<project>/.claude/mcp_servers.json` (legacy project path)
3. the first `.mcp.json` found while walking from the current directory upward

All three use the top-level key `mcpServers`, not `mcp_servers`. `$VAR` and
`${VAR}` references are expanded in commands, arguments, environment values,
CWD, and headers.

On engine startup, enabled servers are connected independently. One failed
server does not prevent other servers or the agent from starting. Successfully
discovered tools are registered as `mcp__SERVER__TOOL` and pass through normal
permission checks. A server `tools/list_changed` notification replaces that
server's complete registered tool set atomically. Disconnect or invalid refresh
removes only that server's tools; reconnect publishes a new exact connection
generation. The next model round sees the settled set.

Operate MCP interactively:

```text
/mcp
/mcp filesystem
```

This surface is read-only manager inspection and reports its current generation,
source, health, bounded connection diagnostics, and tools. `/mcp add`,
`/mcp restart`, and `/mcp remove` are unavailable: editing persisted config,
changing the manager, and rollback are not one user-facing configuration
transaction. Runtime lifecycle changes for an already configured server do
keep manager and registry state synchronized, but configuration mutation does
not. Edit the owning MCP configuration and restart the runtime to add, remove,
or repair a server that remains failed.

Inspect configured MCP without starting a conversation or connecting a server:

```bash
yhc mcp list
yhc mcp get filesystem --output-format json
```

This reports configuration-sourced `configured`/`disabled` state with
`unprobed` health. It intentionally omits commands, URLs, arguments,
environment values, and headers. It is not the live runtime inventory shown by
slash `/mcp`.

## Expose YHC tools as an MCP server

```bash
MCP_PERMISSION_MODE=strict go run ./cmd/yhc serve mcp
```

This is a separate stdio tools server over the built-in registry. It does not
load a model, construct `QueryEngine`, connect configured outbound MCP servers,
persist a transcript, or run slash commands. The default policy is `open`; only
exact lowercase `strict` blocks non-read-only tools.

## Add a skill

Create a Markdown file under either `~/.claude/skills/` or
`<project>/.claude/skills/`. User skills load first, so a project skill with the
same name replaces it.

```markdown
---
name: review-package
description: Review one Go package
args:
  - name: package
    description: Package path
    required: true
---
Review {{package}} for correctness, concurrency, and missing tests.
```

Skills are loaded recursively at engine construction. `/skills` lists/searches
the registry with source/health and malformed-source diagnostic counts; the
model-visible `Skill` tool invokes a skill and substitutes declared `{{arg}}`
values. Restart or resume into a reloaded engine after adding a skill; there is
no general skill-reload slash command.

## Add a plugin prompt command

Place a directory containing `plugin.json` under `~/.claude/plugins/` or
`<project>/.claude/plugins/`:

```json
{
  "name": "quality",
  "version": "1.0.0",
  "commands": [
    {
      "name": "review",
      "description": "Review current changes",
      "filePath": "commands/review.md"
    }
  ]
}
```

The command becomes `/quality:review [arguments]`; its Markdown body is injected
as a model prompt. Run `/reload-plugins` after changing a manifest or prompt.
Reload validates the versioned bundled workflow pack together with every
configured source and command, reports the accepted
revision/digest/source/trust health, and commits the complete generation
atomically. Any malformed bundle, invalid configured source, or
core/name/alias collision retains the previous live generation. Configured
plugin names remain qualified and cannot replace unqualified core or bundled
commands.

Use the provider-free administration projection when you only need source and
generation evidence:

```bash
yhc plugins list
yhc plugins validate --output-format json
yhc plugins reload
```

Validation builds and collision-checks the whole bundled/configured candidate
without replacing it. Reload uses the same atomic registry replacement, but
only inside the short-lived inspection process; it does not signal another
running agent. Install/uninstall/enable/disable/marketplace are intentionally
absent until their containment, trust, persistence, and rollback gates close.

Current runtime bootstrap wires only `commands`. Loader methods exist for
plugin-declared `skills`, `hooks`, and `mcpServers`, but production composition
roots do not call them. Do not advertise those declarations as active.

The configured plugin-root path may itself be a symlink. Once opened, that
target is the authority for the whole candidate: child plugin directories must
be real enumerated directories, manifests and prompt files must resolve beneath
their child root, and command bytes are materialized before publication.
Relative contained links work. Absolute, broken, outside-root, drive-qualified,
UNC, parent-escaping, NUL-containing, and invalid paths fail validation or
reload; backslash relatives are normalized like slash relatives. A rejected
candidate leaves the previous complete revision and prompt bytes live.

This is containment, not content trust. Installing a plugin still authorizes
its in-root prompt text to reach the model. If an existing plugin used a link
to content outside its directory, copy that content into the plugin root or
replace the link with a contained relative link, then run
`yhc plugins validate` before reloading.

## Maintainer reference

| Concern | Source |
|---|---|
| MCP config/client | [`config.go`](../../engine/mcp/config.go), [`mcp_tool.go`](../../tools/mcp_tool.go) |
| Skills | [`skills.go`](../../engine/skills/skills.go), [`skill.go`](../../tools/skill.go) |
| Plugin command bootstrap | [`loader.go`](../../engine/plugins/loader.go), [`input_processor.go`](../../engine/input_processor.go) |
| Standalone MCP | [`server.go`](../../server/mcp/server.go) |
| Architecture | [Capabilities](../architecture/capabilities/README.md) |
