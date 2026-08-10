# P48.4 MCP Environment Identity

**Status:** historical
**Closed gaps:** G45
**Completed:** 2026-08-07
**Adoption:** `adapt`

> **Ownership:** completion record for P48.4/G45; current behavior belongs in
> [`acp-adapter.md`](../../../architecture/platform/acp-adapter.md) and
> [`mcp.md`](../../../architecture/capabilities/mcp.md)

## Outcome

One `engine/mcp` helper now defines environment-key identity for ACP stdio-MCP
duplicate admission, process-local setup fingerprints, and child-process
overlay. Windows maps names to uppercase identity; non-Windows retains exact
spelling. ACP rejects semantic duplicates before manager or process
construction, while admitted configuration retains the request's original key
spelling and exact value.

Setup fingerprints encode canonical names paired with exact values and remain
independent of Go map iteration. The launcher uses the same identity when it
removes inherited duplicates, without changing deterministic overlay order,
server-name normalization, commands, arguments, descriptor limits, lifecycle,
or persisted state.

## Compatibility And Rollback

Unix behavior is unchanged: `Path` and `PATH` remain distinct. On Windows, a
descriptor containing both spellings now fails with the existing bounded
`environment_name_duplicate` reason before construction, and otherwise
equivalent single-spelling descriptors share a fingerprint. This is the
accepted compatibility correction for Windows process semantics.

A squash revert must restore ACP exact-key admission/fingerprinting and the
launcher-local Windows fold together. No durable schema or data migration is
involved, but reverting only one side would reopen G45.

## Evidence

The closeout tree contains a pure OS-parameterized identity contract,
non-Windows admission/fingerprint compatibility, Windows-tagged duplicate and
fingerprint tests, immutable overlay-input checks, focused race coverage, full
MCP/ACP packages, the official ACP SDK harness, and repository gates. Detailed
commands and platform limits are in the
[verification record](../../verification/p48-4-mcp-environment-identity.md).

The Windows-tagged ACP test binary cross-compiles successfully, but no real
Windows host executed it during local closeout. Cross-compilation and the
cross-platform build therefore prove build compatibility only; they do not
claim real Windows `exec.Cmd` or process behavior. Remote CI is a separate
merge gate.
