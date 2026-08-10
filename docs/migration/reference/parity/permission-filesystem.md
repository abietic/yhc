# Permission Filesystem Parity Audit

**Status:** reference-snapshot
**Assessed:** 2026-07-12
**Question:** How are filesystem paths canonicalized and cached before
permission matching, and when is cached state invalidated?
**Result:** completed on 2026-07-12; retained as pre-implementation evidence

> **Ownership:** snapshot comparison and accepted contract at the assessment
> boundary; current behavior lives in
> [`permissions.md`](../../../architecture/capabilities/permissions.md)

## Scope

The behavioral specification is the local `claude-code-ripe` snapshot:

- `src/utils/fsOperations.ts`: `safeResolvePath`,
  `resolveDeepestExistingAncestorSync`, and `getPathsForPermissionCheck`;
- `src/utils/permissions/filesystem.ts`: rule checks,
  `getResolvedWorkingDirPaths`, and `pathInAllowedWorkingPath`;
- `src/utils/permissions/pathValidation.ts`: glob, shell-expansion, sandbox,
  and resolved-path validation.

The Go runtime evidence is:

- `engine/permission_scope.go`;
- `engine/permission/accept_edits.go`;
- `engine/permission/path_validation.go`;
- `engine/permission/rules.go`;
- `engine/engine.go` permission wrapping and additional-directory state.

## Parity Matrix

| Behavior | Reference | Go runtime | Verdict | Observable consequence |
|---|---|---|---|---|
| Existing symlink target | Checks original, intermediate, and final canonical paths | Working-directory reads use `EvalSymlinks` on the final path | Partial | Existing read escapes prompt correctly, but other decision stages do not share the same representations |
| Nonexistent target under symlink parent | Resolves the deepest existing ancestor and rejoins the missing tail | `EvalSymlinks` failure falls back to the lexical path | Gap | A create/write path can appear inside CWD while landing outside it |
| Dangling/chained symlink | Collects intermediate targets with a bounded visited set | No intermediate-target collection | Gap | Target-specific deny/ask rules can be missed |
| Explicit deny/ask rules | Evaluates every permission path representation before implicit allows | Evaluates raw tool input before canonical path checks | Gap | A symlink alias can bypass a rule written for its target |
| `acceptEdits` write scope | Requires every original/resolved representation to remain in an allowed working path | Uses only `filepath.Clean` and lexical prefix containment | Gap | Symlinked writes may be auto-approved outside the workspace |
| Additional working directories | Included symmetrically with the original CWD | Persisted and exposed by the engine, but ignored by read/write permission checks | Gap | `/add-dir` does not grant the documented read/search permission scope |
| UNC/special files | Blocks UNC before filesystem access and avoids `realpath` on devices/FIFOs/sockets | No equivalent runtime path-representation helper | Gap | Windows network-path and special-file behavior is not proven at the authorization boundary |
| Working-directory cache | Memoizes resolved forms by stable directory string; input paths are resolved per check | No cache | Intentional adaptation | More syscalls, but no stale authorization result; correctness does not require a cache |
| `ValidateFilePath` | Path validation functions are called by tool permission flows | Go function has tests but no production caller | Stale evidence | Package tests do not prove runtime authorization behavior |

## Accepted Contract at Snapshot

The accepted implementation slice introduced one shared, fail-conservative
permission path resolver at the actual `QueryEngine` authorization boundary.

1. Resolve an absolute logical path without mutating tool input.
2. Return all security-relevant representations: logical path, intermediate
   symlink targets, deepest-existing-ancestor resolution for create paths, and
   final canonical path when available.
3. Evaluate deny and ask rules across every representation before approvals,
   working-directory defaults, or `acceptEdits`.
4. Require every representation to be contained by CWD or an additional working
   directory before implicit read/write approval.
5. Keep explicit allow behavior separate from implicit workspace approval.
6. Avoid filesystem access for UNC paths and avoid following special files.
7. Do not add a cache until profiling shows a need. If added later, cache only
   session-stable root representations by path key, never input authorization
   results.

## Focused Acceptance Tests

- existing file symlink escaping CWD;
- create under a live symlinked parent;
- dangling file and parent symlinks;
- chained symlink with a deny rule on an intermediate/final target;
- explicit ask on a resolved target;
- additional-directory read/search allow and sibling-prefix denial;
- `acceptEdits` write inside a real root and prompt outside every allowed root;
- UNC input performs no filesystem resolution on Windows;
- rule and approval ordering remains deny/ask before implicit allow.

## Outcome

This authorization gap was closed before classifier refinement. Shared path
representations and consistent ordering, rather than a cache, were the required
security fix.

## Implementation Status

Implemented on 2026-07-12 in `engine/permission/path_resolution.go` and the
engine permission wrapper. Focused tests cover resolved deny/ask rules,
existing and nonexistent symlink escapes, additional roots, UNC handling, and
`acceptEdits` behavior. The later classifier slice is recorded in
[`permission-classifier.md`](permission-classifier.md).
