# P32.1 Plugin File Authority

**Status:** historical
**Closed gaps:** G4
**Completed:** 2026-07-31

> **Ownership:** completion evidence for descriptor-relative plugin discovery,
> manifest and prompt materialization, plugin-skill identity fencing, G4
> closure, and the retained no-new-capability rollback boundary.

## Outcome

P32.1 completed the accepted `project-native` contract. Each configured plugin
root is opened once as an `os.Root`; a configured root symlink binds its opened
target, while a child plugin must be a real enumerated directory whose
filesystem identity still matches when opened. Manifest and file-backed prompt
bytes are opened through that child root, checked as regular on the descriptor
that is read, and materialized before the authority closes.

One portable rule normalizes slash and backslash relatives and rejects Unix
absolute, drive-qualified, UNC, parent-escaping, NUL-containing, and invalid
paths. Relative contained links and regular hard-link entries remain valid.
Absolute, broken, escaping, or replaced links fail closed without exposing
external bytes in diagnostics.

## Publication, Skills, and Entrypoints

Configured command bodies and digests now use the same materialized bytes.
Later ambient file replacement changes neither dispatch nor generation
metadata until an explicit successful reload. One invalid manifest, command,
or higher-precedence duplicate rejects the full candidate and retains the
previous revision, digest, source snapshot, and dispatch.

The library-only `RegisterSkills` path reopens each plugin through its
configured parent authority and requires the directory identity captured by
`Loader.Load`. Explicit and conventional skill paths traverse the same child
root and enter the target registry only after the complete batch succeeds.
Plugin skills remain disconnected from production bootstrap, as do plugin
hooks and MCP declarations.

TUI and Plain keep configured prompt commands. ACP, ordinary headless,
`goal run`, and standalone MCP gain no plugin command or contribution
bootstrap. Provider-free `plugins validate` remains non-mutating, and
`plugins reload` still replaces only the inspection process generation.

## Verification and Rollback

Deterministic link, replacement, identity, file-type, portable-path,
precedence, generation, entrypoint, race, and Windows-target compilation
evidence passes. Independent security review found and caused correction of
one child-entry replacement window; re-review reported no remaining findings.
Reproducible commands are in
[`p32-1-plugin-file-authority.md`](../../verification/p32-1-plugin-file-authority.md).

Rollback may revert the authority and materialization changes because there is
no durable schema. That reopens G4, so it is safe only when configured
file-backed plugins are disabled or treated as fully trusted ambient
filesystem content. Rollback does not activate plugin skills, hooks, MCP
declarations, installation, marketplace, or watcher behavior.
