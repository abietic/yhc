# Contributing Guide

**Status:** current
**Last verified:** 2026-07-17

> **Ownership:** contributor and coding-agent route from task intake to verified handoff

Public repository governance is defined by the root
[`CONTRIBUTING.md`](../../CONTRIBUTING.md), [`LICENSE`](../../LICENSE),
[`NOTICE`](../../NOTICE), [`SECURITY.md`](../../SECURITY.md), and
[`CODE_OF_CONDUCT.md`](../../CODE_OF_CONDUCT.md). Source mappings and
publication evidence follow the [source-mapping policy](../publication/README.md).

## Before changing code

1. Read [`AGENTS.md`](../../AGENTS.md) for repository rules and
   [`PROJECT_DIRECTION.md`](../../PROJECT_DIRECTION.md) for product scope and
   reference-adoption decisions.
2. Inspect `git status --short`; preserve unrelated and uncommitted user work.
3. Use [`architecture/code-map.md`](../architecture/code-map.md) to locate the
   current owner and supported entrypoints.
4. If `.codegraph/` exists, use CodeGraph before broad grep or manual file walks.
5. When a decision uses reference behavior, inspect only the relevant local
   `.reference/` sources, compare them against current production wiring, and
   freeze ordering, permissions, cancellation, recovery, persistence, and
   compatibility consequences before implementation.

## Choose the workflow

| Change type | Required documents |
|---|---|
| Product objective or reference-adoption policy | [`PROJECT_DIRECTION.md`](../../PROJECT_DIRECTION.md) |
| Current behavior or ownership | Affected [`architecture/`](../architecture/README.md) owner |
| User-visible workflow | Affected architecture owner plus [`guides/`](../guides/README.md) |
| New reproduced gap | [`migration/REMAINING.md`](../migration/REMAINING.md) |
| Accepted evolution work | [`migration/PLAN.md`](../migration/PLAN.md) and one detailed plan under `migration/plans/` |
| Reference comparison | [`migration/reference/`](../migration/reference/README.md) |
| Completed milestone narrative | [`migration/history/`](../migration/history/README.md) |
| Commands, fixtures, or performance budget | [`migration/verification/`](../migration/verification/README.md) |

Do not update `STATUS.md` from intent or a checklist. Source, focused tests,
entrypoint wiring, and required repository gates are the evidence.

## Implementation loop

1. Define the observable problem and success condition.
2. Trace the current composition root and all applicable entrypoints.
3. Make one independently reviewable behavior change.
4. Add focused tests for happy path, boundary, failure, and ordering where they
   affect the contract.
5. Synchronize only the owning documents.
6. Run [`verification.md`](verification.md).
7. Inspect the final diff for unrelated changes, stale links, and duplicated
   documentation ownership.

Choose additional race, fuzz, replay, PTY, and end-to-end evidence from
[`testing-strategy.md`](testing-strategy.md). Focused risk packs supplement the
ordinary repository gates; they do not replace them.

## Branch and review

`master` is protected. Start from current `origin/master` on a short-lived
branch, normally with a `codex/feat/`, `codex/fix/`, `codex/docs/`,
`codex/test/`, or `codex/chore/` prefix for Codex-created work. Keep one accepted
evolution slice or one behavior change per pull request.

Before pushing code, all four repository gates must pass:

```bash
make fmt
make lint
make test
make build
make docs-check
```

The documentation lifecycle and ownership rules are defined in
[`documentation-policy.md`](documentation-policy.md). Project agents should use
[`$write-docs`](../../.agents/skills/write-docs/SKILL.md) when
creating, reorganizing, or auditing technical documentation.
