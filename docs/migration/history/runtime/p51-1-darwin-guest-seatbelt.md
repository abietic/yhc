# P51.1 Darwin Guest Seatbelt

**Status:** historical
**Completed:** 2026-08-08
**Adoption:** `project-native`

> **Ownership:** completion record for the P51.1 Darwin Guest subset; current
> execution and permission behavior belongs in architecture and operator guides

## Outcome

Model-issued Bash on Darwin amd64/arm64 now defaults to a real Seatbelt
`workspace-write` Guest binding. The adapter uses only
`/usr/bin/sandbox-exec`, probes real filesystem/network/socket/descendant
behavior, and binds its proof to the immutable policy and capability
generation. ShellManager preserves persistent Bash semantics while pinning the
exact binding, recapturing the root before start and every command, bounding
wall time and retained output, and owning process-group cleanup.

Composition roots now carry separate Guest, ShellHooks, and StdioMCP bindings.
Hooks and configured stdio MCP remain explicitly ambient; async hooks retain
their own binding. ACP create/load/resume, restore staging, and child Agents
cannot turn those ambient bindings into Guest authority. Restore and child
derivation recapture and re-probe equal-or-narrower Guest roots before spawn.

User configuration and `--sandbox` are the only selection authorities. Project
and project-local sandbox fields are discarded with redacted diagnostics.
Missing or failed enforcement creates an unavailable Guest while keeping the
engine usable; Bash then fails before spawn with no ambient retry. An explicit
`danger-full-access` selection is the only ambient Guest rollback and emits a
visible warning.

## Compatibility And Remaining Risk

Default, Plan, AcceptEdits, Auto, bypass, and DontAsk permission outcomes are
unchanged. In particular, unmatched Auto Bash still reaches the existing human
or fail-closed boundary; P51.1 does not implement the later containment-backed
shortcut.

Guest Bash inherits the parent environment byte-for-byte. Hard memory,
file-descriptor, and process-count limits are not enforced, and hooks/MCP remain
ambient. The aggregate state is therefore `degraded` and G28 remains open.
Unsupported platforms fail the default workspace-write Guest before spawn
rather than silently widening it.

A squash revert removes the Darwin selection, binding matrix, adapter, and
ShellManager enforcement as one compatibility unit. Retaining the new default
while removing the adapter would make Guest Bash unavailable; silently falling
back to ambient is not a valid partial rollback. Existing process-group cleanup
must remain even if the containment slice is reverted.

## Evidence

Real Darwin capability, escape, Go/Make/Git product, control-plane write,
configuration-authority, cross-entrypoint, restore, child, async hook, stdio
MCP, permission-invariance, fail-closed, race, repository, documentation,
queue, manifest, and diff gates are recorded in the
[verification record](../../verification/p51-1-darwin-guest-seatbelt.md).
Remote CI remains a separate merge gate.
