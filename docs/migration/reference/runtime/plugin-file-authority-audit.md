# Plugin File Authority Audit

**Status:** reference-snapshot
**Snapshot:** 2026-07-31; Eino-Agent
`e334f06b995b6119d0792d44b8fc420bb661cffe`, Claude Code Ripe
`4b9d30f79532`, Codex `66bd101fff6f`, and Crush `2af939d8e900`

> **Ownership:** source-backed evidence for deciding how plugin roots,
> manifests, and file-backed contributions are authorized before loading. The
> accepted contract and execution order belong in
> [`p32-plugin-file-authority.md`](../../plans/p32-plugin-file-authority.md)
> and [`PLAN.md`](../../PLAN.md).

## Decision

Adopt a **project-native** file authority based on Go's `os.Root`. Keep the
existing whole-generation validation and rollback behavior, but replace the
lexical check-then-read boundary with root-relative, descriptor-backed opens.

This is a safety repair, not a plugin expansion. Only prompt commands are
production-wired today. The plugin skill loader receives library-level
containment proof so it cannot become an unsafe future caller, while plugin
hooks and MCP declarations remain disconnected.

## Pre-P32 reproduced failure

`Loader.BuildCommandGeneration` scans the user and project plugin roots,
parses each `plugin.json`, and converts file-backed commands into prompt
workflows. `buildPluginCommand` joins `filePath` to `Plugin.Directory`, checks
the result with `filepath.Rel`, and then calls `os.ReadFile`.

That sequence rejects a literal `../outside.md`, but it does not resolve or
bind filesystem identity. A plugin can therefore contain:

```text
.claude/plugins/review/
├── plugin.json
└── commands/security.md -> /outside/private.md
```

The lexical path remained under `review`, so the snapshot loader read
`/outside/private.md` and publishes those bytes as configured prompt content.
Changing the file after successful publication does not mutate the live
generation because the command closure already owns a string copy. The unsafe
read occurs while building the candidate.

The skill loader repeats the lexical pattern for explicit skill paths and the
default `skills/` directory. It is not reached by production plugin bootstrap,
but it is an exported loading path with the same authority defect.

## Snapshot ownership and entrypoints

| Boundary | Current behavior | Reachability |
|---|---|---|
| Engine construction | `newQueryEngineWithOptions` configures plugin roots and calls `ReloadPromptCommands` | TUI and Plain engines |
| Candidate build | `QueryEngine.promptCommandCandidate` constructs `plugins.Loader` and calls `BuildCommandGeneration` | TUI, Plain, and provider-free inspection |
| Live replacement | `ReloadPromptCommands` calls `Registry.ReplacePromptCommandGeneration` only after complete validation | TUI and Plain |
| Explicit reload | `/reload-plugins` uses the same engine owner | TUI and Plain |
| Administration | `plugins validate` does not mutate; `plugins reload` changes only its short-lived inspection host | CLI inspection |
| Plugin skills | `RegisterSkills` can load files but has no production bootstrap caller | Outside current product closure |
| Plugin hooks and MCP | Manifest records are inventoried, not registered into production | Outside current product closure |
| ACP, headless, standalone MCP | Configured prompt commands are not advertised or dispatched | Intentionally unsupported |

Later configured roots override earlier roots for duplicate plugin names. Any
manifest or command error rejects the complete candidate and keeps the prior
registry revision, digest, commands, and source projection live.

## Why canonical strings are insufficient

`filepath.EvalSymlinks` would detect the reproduced static escape, but a
separate canonicalize-then-read sequence still leaves a replacement window.
The path can change after validation and before `os.ReadFile`.

Go 1.26.5 already provides the narrower primitive this project needs:

- `os.OpenRoot` binds a directory handle;
- `Root.OpenRoot` opens a child relative to that handle;
- `Root.Open` follows relative links only while the resolved target remains
  beneath the bound root, and rejects absolute links even when they name an
  object beneath that root;
- moving or replacing the path does not redirect an already opened root on
  the supported Linux, Darwin, and Windows targets.

The implementation still needs portable manifest-path normalization and
regular-file checks. `os.Root` does not prohibit bind mounts, mounted
filesystems, `/proc` special files, or device files by itself.

## Comparative evidence

| Source | Useful mechanism | Adoption consequence |
|---|---|---|
| Claude Code Ripe | Plugin copy resolves link targets and checks that copied links stay under the resolved source tree; skill identity uses `realpath` | Preserve the idea that link identity matters, but do not copy its string-prefix check or installation lifecycle |
| Codex | Plugin manifests resolve paths relative to a typed plugin root; skill loading canonicalizes identities and namespaces; bundle archives reject symlink and hard-link entries | Preserve explicit plugin-root provenance and deterministic namespaces; do not import its marketplace/store model |
| Crush | Filesystem lookup canonicalizes with `EvalSymlinks`, and walkers avoid following directory links | Use as evidence that lexical containment is inadequate; descriptor-relative opens are stronger for the current race boundary |
| Eino-Agent | Whole prompt-command generations validate before one atomic replacement, and failed candidates retain the prior generation | Preserve exactly |
| Go standard library | `os.Root` owns root-contained, descriptor-relative filesystem access on every supported release target | Adopt as the project-native authority |

No reference wins by identity. The standard-library primitive fits the current
Go runtime, removes the check/use split, and requires no second plugin store,
watcher, or lifecycle.

## Accepted policy choices

1. A configured plugin root may itself be a symlink. Opening that configured
   path selects and binds its resolved target for one candidate build.
2. A plugin child remains a real directory discovered one level below its
   configured root. Discovery does not start following child-directory
   symlinks.
3. A manifest, prompt file, or skill file may traverse a relative internal
   symlink only when the final object remains under the bound plugin directory.
   Absolute symlinks are rejected even when their target is inside that
   directory.
4. Absolute, volume-qualified, UNC, empty, NUL-containing, and cleaned
   parent-escaping manifest paths fail before filesystem access. Slash and
   backslash input receives one portable normalization rule.
5. If the higher-precedence source is invalid, candidate publication fails.
   The loader does not silently downgrade to a lower-precedence plugin with the
   same name.
6. Published prompt commands contain materialized bytes and perform no later
   plugin-file read during dispatch.
7. A regular hard-link entry inside the plugin directory is within authority.
   P32 binds reachable directory entries, not exclusive inode provenance; the
   candidate still materializes the exact bytes it reads before publication.

These choices intentionally reject existing plugin configurations that use an
in-root link to read content outside the plugin directory. The failure is
visible during startup, validation, or reload, and the last complete live
generation remains available.

## Frozen evidence required before closeout

- outside-link rejection for manifest, prompt command, explicit skill, and
  default skill-directory paths;
- acceptance of a relative contained link to a regular file and rejection of
  an absolute link whose target is inside the same plugin root;
- a deterministic replacement test proving an acquired plugin root cannot be
  redirected to a replacement directory;
- one-open regular-file reads so replacing a file with an outside link cannot
  cross authority;
- portable path fixtures covering slash, backslash, parent traversal, drive
  prefixes, UNC, and NUL;
- duplicate-name precedence failure retaining the prior complete generation;
- unchanged TUI/Plain command dispatch and absent ACP/headless/standalone-MCP
  plugin command capability;
- focused race tests, repository gates, and documentation/manifest checks.

## Explicit exclusions

- enabling plugin skills, hooks, MCP declarations, installation, marketplace,
  or remote update workflows;
- changing command names, aliases, trust class, prompt substitution, or core
  collision rules;
- treating a lower-precedence plugin as an automatic fallback;
- defending against privileged bind mounts, hostile kernels, or mutation of an
  already opened regular file's bytes;
- changing direct user/project skill directories outside plugin bootstrap.

## Source anchors

| Boundary | Evidence |
|---|---|
| Engine startup and candidate owner | [`newQueryEngineWithOptions`](../../../../engine/engine.go), [`QueryEngine.promptCommandCandidate`](../../../../engine/input_processor.go) |
| Discovery and precedence | [`Loader.discover`](../../../../engine/plugins/loader.go) |
| Delivered root authority and skill traversal | [`file_authority.go`](../../../../engine/plugins/file_authority.go) |
| Delivered command materialization and publication | [`loader.go`](../../../../engine/plugins/loader.go) |
| Atomic live generation | [`Registry.ReplacePromptCommandGeneration`](../../../../engine/commands/registry.go) |
| Current production wiring | [`plugins.md`](../../../architecture/capabilities/plugins.md) |
| Completed delivery | [`p32-1-plugin-file-authority.md`](../../history/runtime/p32-1-plugin-file-authority.md) |

## Current replacement

P32.1 completed this audit's `project-native` decision on 2026-07-31. Current
plugin behavior is owned by
[`plugins.md`](../../../architecture/capabilities/plugins.md), while exact
implementation, review correction, compatibility, and rollback evidence is in
[`p32-1-plugin-file-authority.md`](../../history/runtime/p32-1-plugin-file-authority.md).
The pre-P32 lexical reproduction above remains historical decision evidence;
G4 no longer appears in the unresolved inventory.
