# P32 Plugin File Authority

**Status:** historical
**Decision:** `project-native`
**Last updated:** 2026-07-31
**Delivery:** completed; see
[`p32-1-plugin-file-authority.md`](../history/runtime/p32-1-plugin-file-authority.md)

> **Ownership:** the accepted contract for binding plugin roots, manifests,
> file-backed prompt commands, and plugin skill candidates to one filesystem
> authority before loading. Root execution order belongs in
> [`PLAN.md`](../PLAN.md); current behavior belongs in
> [`plugins.md`](../../architecture/capabilities/plugins.md).

## Outcome

A configured plugin may contribute prompt bytes only from the plugin directory
that the loader actually opened. A link or concurrent path replacement cannot
redirect manifest, command, or skill reads outside that authority.

P32 preserves the existing product boundary:

- configured prompt commands remain active only in TUI and Plain;
- provider-free `plugins validate/reload` remains inspection-process local;
- plugin skills, hooks, and MCP declarations remain disconnected;
- a failed candidate retains the prior complete live generation;
- no durable schema, watcher, marketplace, trust UI, or new execution
  capability is added.

## User problem

Before P32.1, the lexical `filepath.Rel` check answered where a path string
appeared to be, not which filesystem object would be read. A plugin-local
symlink could point outside the plugin directory and inject arbitrary external
bytes into a configured prompt command. A path could also be replaced between
validation and `os.ReadFile`.

This matters now because the prompt-command path is production-reachable and
labels its content `configured`. Users need one auditable rule: installing or
configuring a plugin grants that plugin access to its own root, not to an
unbounded filesystem path selected through a link.

The reproduced source and reference evidence is
[`plugin-file-authority-audit.md`](../reference/runtime/plugin-file-authority-audit.md).

## Supported boundary

| Surface | P32 behavior |
|---|---|
| TUI | Startup and `/reload-plugins` publish only a root-authorized complete command generation |
| Plain | Same owner and result as TUI |
| `plugins validate` | Runs the same candidate and authority checks without mutation |
| `plugins reload` | Replaces only the inspection host's local generation after the same checks |
| ACP | Continues to expose no configured plugin prompt commands |
| ordinary headless / `goal run` | Continue to expose no configured plugin prompt commands |
| standalone MCP | Continues to have no conversation-command or plugin bootstrap |
| `RegisterSkills` library API | Uses the same root authority and identity checks, but remains outside production bootstrap |
| plugin hooks / MCP declarations | Remain inventory only and are not activated by this plan |

## One authority, one candidate

P32.1 introduced one package-private plugin file authority backed by `os.Root`.
The authority is not a second registry or plugin lifecycle.

1. Open each configured root once for discovery. The configured path itself may
   be a symlink; its resolved target becomes the authority selected for that
   candidate build.
2. Enumerate one directory level through the root handle. Open each real plugin
   child through `Root.OpenRoot`, not by joining and reopening an ambient path.
3. Read `plugin.json` through that child handle and capture the child directory
   identity. The manifest must be a regular file after contained link
   resolution.
4. Normalize every nonempty `filePath` with one platform-independent local-path
   rule. Reject absolute, drive-qualified, UNC, parent-escaping, NUL-containing,
   and invalid paths before opening.
5. Open each file through the same plugin root, verify the opened descriptor is
   regular, and read from that descriptor. Never validate one path and read a
   separately resolved path.
6. Materialize every prompt command body before closing the candidate-local
   root. Published command closures perform no filesystem read.
7. `Loader.Load` records the plugin directory identity needed by the
   library-only skill loader. `RegisterSkills` reopens through the configured
   parent authority, requires `os.SameFile`, and traverses/reads only through
   that root for the duration of registration.

A relative internal symlink is allowed only when `os.Root` resolves it beneath
the bound plugin directory. Absolute links, broken links, and links that escape
fail closed, including an absolute link whose target happens to be inside the
plugin root. Plugin child directory symlinks remain undiscovered, matching
current behavior.

## Frozen invariants

### Publication and precedence

- Root order and duplicate-name diagnostics remain unchanged.
- One invalid manifest, command, or authority check rejects the entire
  candidate.
- An invalid higher-precedence source does not fall back to a lower-precedence
  plugin with the same name.
- Rejection leaves the previous registry revision, digest, commands, source
  snapshot, and dispatch behavior unchanged.
- Candidate digests cover the exact materialized command bytes that will be
  dispatched.

### Identity and replacement

- Discovery, manifest loading, and prompt materialization use the same opened
  plugin root.
- Replacing or retargeting an ambient configured-root/plugin path after the
  root opens cannot redirect that candidate.
- The skill loader rejects a reopened plugin directory whose filesystem
  identity differs from the directory that supplied its manifest.
- File type is checked on the descriptor whose bytes are read.
- A regular hard-link entry under the plugin root is accepted. Authority binds
  reachable directory entries, not exclusive inode provenance; publication
  still materializes the bytes read from the opened descriptor.
- Candidate errors expose source and typed diagnostic context, never file
  contents.

### Entrypoints and authority

- P32 changes file admission, not command execution authority.
- Command qualification, aliases, trust class, prompt argument substitution,
  collision checks, and core-command precedence stay byte-compatible.
- No new plugin contribution reaches the model, hooks, MCP manager, ACP,
  headless, or standalone MCP.
- Direct user/project skill directories keep their current independent owner.

### Cancellation, persistence, and recovery

Candidate loading is synchronous and provider-free. It creates no goroutine,
background watcher, Session record, transcript event, or durable artifact.
Cancellation remains the caller's existing command/engine lifecycle concern.
Recovery is the prior complete in-memory command generation plus a later
explicit retry.

## Frozen P32.1 proof inventory

The implementation PR added deterministic proof for all of the following.

| Area | Required proof |
|---|---|
| Static traversal | Existing `../` command and skill rejection stays green |
| Portable paths | Accept slash and normalized backslash relatives; reject Unix absolute, drive absolute/relative, UNC, cleaned parent escape, empty declared skill path where required, and NUL |
| Manifest link | `plugin.json` linked outside the plugin root is rejected |
| Command link | File-backed command linked outside is rejected and external bytes never enter diagnostics or the candidate |
| Contained link | A relative link to a regular file inside the same plugin root is accepted; an absolute link to that same target is rejected |
| File replacement | Replacing a candidate file with an outside link after root acquisition cannot escape; the read either uses the already opened object or rejects |
| Directory replacement | Renaming/replacing the ambient plugin directory after child-root acquisition cannot redirect manifest or command reads |
| Skill file | Explicit skill file outside-link rejection and contained-link acceptance |
| Skill directory | Default and explicit skill-directory outside links reject without partial registration |
| Identity fence | Replacing the plugin directory between `Load` and `RegisterSkills` rejects before skill registration |
| Precedence rollback | Invalid higher-precedence duplicate rejects the full candidate and retains the exact prior live revision/digest/dispatch |
| Generation stability | Successful publication keeps materialized bytes after later file replacement until an explicit successful reload |
| Entrypoint scope | TUI/Plain remain supported; ACP/headless/standalone-MCP remain absent; inspection remains non-persisting |
| Platform closure | Focused Darwin/Linux behavior, Windows-target compilation, `make build`, and portable path fixtures |
| Concurrency | Focused `go test -race ./engine/plugins ./engine/skills -count=1` |

Tests may use package-private authority methods to place a barrier between root
acquisition and read. Production code must not add a global mutable test hook.

## Non-goals

- plugin installation, uninstall, reconcile, marketplace, update, signature,
  or trust UX;
- enabling `RegisterSkills`, `RegisterShellHooks`, or `RegisterMCPServers` in a
  composition root;
- containing inline shell-hook commands or the disconnected MCP `cwd`;
- changing direct `.claude/skills`, hook files, or ordinary MCP config;
- detecting privileged bind mounts, hostile kernels, or writes through an
  already opened descriptor;
- adding a file watcher or automatically refreshing a running process.

## Adoption decision

The decision is `project-native`.

Claude, Codex, and Crush prove that real path identity, plugin-root provenance,
and no-follow traversal are mature concerns. Their plugin stores, marketplaces,
namespaces, and update lifecycles do not match this repository's current
surface. Go's `os.Root` directly supplies the required descriptor-relative
authority on all supported targets while preserving the existing loader and
registry owners.

`EvalSymlinks`-only containment is rejected because it leaves a check/use
window. Copying every plugin into a new immutable store is deferred because no
current installation/update product requires a second store.

## Compatibility

The intentional incompatibility is narrow: a plugin file path that currently
escapes its plugin directory through a symlink will fail validation or reload.
The loader reports the affected source and retains the last complete
generation. Users must copy the content into the plugin root or use a contained
link.

Configured-root symlinks remain supported because the configured root itself is
the granted authority. Qualified command names, aliases, inline prompt
commands, source precedence, and published prompt text remain unchanged for
valid plugins. Relative contained links remain valid; absolute links become
invalid even when their target is beneath the plugin root. Existing regular
hard links remain valid, without implying exclusive ownership of their inode.

## Rollback

P32.1 has no durable migration. A squash revert restores the prior loader and
skill behavior, but it also reopens G4; rollback is safe only if configured
file-backed plugins are disabled or treated as fully trusted filesystem
content.

The registry's previous-generation retention is the operational rollback for a
bad plugin update. No implementation may delete or mutate that retained
generation on candidate failure.

## Delivery and closeout

P32.1 closed after:

1. every proof row above passes;
2. `make fmt`, `make lint`, `make lint-new`, `make test`, and `make build` pass;
3. `make docs-check`, `make docs-check-ci`, manifest validation, and
   `git diff --check` pass;
4. an independent security review finds no authority, replacement,
   cross-platform, or partial-publication gap;
5. current plugin architecture and user guidance describe the new root rule;
6. `STATUS.md` recorded the verified behavior, G4 left `REMAINING.md`, and
   [`p32-1-plugin-file-authority.md`](../history/runtime/p32-1-plugin-file-authority.md)
   replaced the live queue entry.

## Source owners

| Boundary | Owner |
|---|---|
| Root authority, path normalization, skill collection | [`engine/plugins/file_authority.go`](../../../engine/plugins/file_authority.go) |
| Discovery, manifests, command materialization, publication | [`engine/plugins/loader.go`](../../../engine/plugins/loader.go) |
| Skill byte parsing and bounded registry registration | [`engine/skills/skills.go`](../../../engine/skills/skills.go) |
| Atomic dynamic command generation | [`engine/commands/registry.go`](../../../engine/commands/registry.go) |
| Engine and inspection candidate construction | [`engine/input_processor.go`](../../../engine/input_processor.go), [`engine/inspection_administration.go`](../../../engine/inspection_administration.go) |
| Current plugin behavior | [`docs/architecture/capabilities/plugins.md`](../../architecture/capabilities/plugins.md) |
| User-visible configuration and recovery | [`docs/guides/extensions-mcp-skills-plugins.md`](../../guides/extensions-mcp-skills-plugins.md) |
| Comparative evidence | [`plugin-file-authority-audit.md`](../reference/runtime/plugin-file-authority-audit.md) |
| Reproduction commands | [`p32-1-plugin-file-authority.md`](../verification/p32-1-plugin-file-authority.md) |
| Completion and rollback evidence | [`p32-1-plugin-file-authority.md`](../history/runtime/p32-1-plugin-file-authority.md) |
