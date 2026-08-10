# YHC

默认用中文回复，除非用户明确要求英文。先从用户问题、可观察契约和当前源码出发；不要把计划、注释、参考仓库或生成状态当作已实现能力。

## Product and architecture

This repository is an independent Go/Eino coding agent. Read
`PROJECT_DIRECTION.md` before changing scope, compatibility, or reference
adoption.

- Go version and module path are owned by `go.mod`; use `make build` for the
  supported cross-platform build and `make debug` for Delve.
- `cmd/yhc/` is the thin CLI composition surface.
- `QueryEngine` owns conversation and session composition.
- `projectGraphQueryKernel` owns the single production traversal shared by
  supported engine entrypoints. See the
  [Query Engine architecture](docs/architecture/runtime/query-engine.md).
- `tools/` remains one flat package; do not create tool subpackages.
- Use standard `(T, error)` returns, wrapped errors, grouped imports, white-box
  Go tests, and gofumpt formatting through repository commands.

## Iteration contract

Preserve unrelated user changes. Work from current `origin/master` on a short-
lived `codex/feat/*`, `codex/fix/*`, `codex/docs/*`, `codex/test/*`, or
`codex/chore/*` branch. Keep one independently reviewable behavior change per
PR and commit only explicit task paths.

Use the public evidence workflow instead of reconstructing target selection:

1. `make change-plan` identifies changed owners and required checks.
2. `make verify-focused` runs and records the selected focused checks.
3. Commit the reviewed change, then run `make verify-merge` on the clean
   committed tree.
4. `make change-evidence` inspects current status;
   `make change-evidence-ready` must succeed before push.

`make verify` remains the whole-repository convenience verification command; it
does not replace diff-bound committed evidence. A required failed or blocked
check means the iteration is incomplete. Report local gates, remote CI,
live-provider checks, PTY checks, and physical UI acceptance separately.

Treat `master` as protected and releasable. Push only a topic branch, merge a
reviewed PR with squash, and delete the branch. The versioned pre-push hook is
defense in depth; install it once per clone with `make setup-git-hooks`.

## Skill routing

Use `.agents/skills/iteration-workflow` for the shared plan-to-committed-
evidence path. Domain skills add their own invariants:

- `migration-loop` selects one align, plan, or execute phase.
- `migration-slice` implements exactly one accepted evolution slice.
- `runtime-depth-change` protects provider, hook, recovery, cancellation,
  ordering, and lifecycle contracts.
- `tui-runtime-change` protects reducer, replay, queue, PTY, and terminal
  contracts.
- `defect-investigation` owns reproduction, causal localization, regression
  placement, and E2E evidence boundaries.
- `write-docs` owns source-backed documentation routing and lifecycle.
- `reference-parity-audit` owns bounded comparative evidence and adoption
  recommendations.
- `skill-runtime` owns telemetry admission, privacy, delegation accounting, and
  terminal closure; it does not choose iteration phases or verification gates.

Project-scoped Terra agents live in `.codex/agents/`: use `terra_explorer` for
bounded read-only evidence, `terra_worker` for an isolated frozen implementation
or focused test, and `terra_reviewer` for second-line review of a cohesive risky
diff. Keep architecture, public APIs, security boundaries, concurrency,
persistence, recovery, and adoption acceptance in the parent. Writing agents
must work in an isolated snapshot and return a patch for parent review.

## Reference decisions

Current source, production wiring, focused tests, and supported entrypoints
define current behavior. Compare references only for the same observable user
problem; no reference wins by identity. Classify adoption as `preserve`,
`adapt`, `combine`, `project-native`, `reject`, or `defer`, with
`PROJECT_DIRECTION.md` as the policy owner.

Preserve ordering, permission, cancellation, persistence, recovery, and
entrypoint invariants whenever the accepted contract depends on them. Keep the
CLI thin, put reusable behavior in runtime/tool owners, and use `.reference/...`
or `REFERENCE_DIR` rather than machine-specific paths in committed files.
