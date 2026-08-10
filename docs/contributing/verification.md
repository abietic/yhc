# Verification Guide

**Status:** current
**Last verified:** 2026-08-10

> **Ownership:** required validation surfaces for documentation and code changes

## Documentation-only changes

Run:

```bash
make docs-check
git diff --check
git status --short
```

The documentation check validates H1/status/ownership metadata, lower-kebab
focused filenames, Markdown link-label freshness, local targets and anchors,
source line anchors, and reachability from `docs/README.md`. The manifest check
validates the reference ledger; it does not replace the documentation check.

Also inspect the final diff for:

- source-of-truth duplication;
- current behavior placed under reference or history;
- future behavior presented as current;
- stale file names in link labels and prose;
- untracked files omitted from the review.

## Code changes

Use focused tests while iterating, then run all repository gates through the
Makefile before declaring completion:

```bash
make fmt
make lint
make test
make build
make docs-check
git diff --check
git status --short
```

Add risk-proportional checks when the boundary requires them:

| Boundary | Additional evidence |
|---|---|
| Concurrency or shared runtime state | focused `go test -race` with deterministic synchronization |
| TUI rendering | representative width goldens and long-history performance |
| Terminal lifecycle | PTY normal, error, cancel, panic, resize, and restoration paths |
| Session/transcript | resume, fork, replay-without-dispatch, corruption, and process restart |
| Permissions/hooks/processes | cancellation, timeout, descendant cleanup, and exactly-once settlement |
| Multiple entrypoints | TUI, plain, headless, ACP, standalone MCP, and child Agent where applicable |

The evidence layers, standard Go method choices, and maintained focused targets
are owned by [`testing-strategy.md`](testing-strategy.md). Do not copy their
commands into a second tracker.

## Select checks for a change

Use the read-only planner before running gates:

```bash
make change-plan
make change-evidence
```

`make change-plan` selects owners and required checks for the current tracked
diff. `make change-evidence` reports their current status. Neither command runs
a gate, and `blocked` is not success; use `make verify-focused` or
`make verify-merge` to execute selected checks. The migration queue orders
accepted product slices; the quality plan selects evidence for the diff. They
are linked only by an optional `slice_id`.

`make change-evidence-ready` is the separate completion assertion. It reads
evidence for explicit `HEAD` and returns non-zero unless the clean committed
tree has exact `evidence_ready` state. It does not execute or repair a gate.

Both verification commands still render their structured evidence when a
selected gate fails or remains blocked, but return non-zero so Make, hooks,
and CI cannot mistake an incomplete state for success.

Set `ITERATION_FORMAT=json` for machine-readable output, `ITERATION_BASE` to
compare another accepted base, or `ITERATION_SLICE_ID` to attach one executable
migration slice. Plans exclude untracked contents from `diff_digest`; they
report only the outside-scope untracked count. An unclassified tracked path,
ambiguous equal-priority owner, invalid policy, or in-progress merge/rebase
fails closed.

The project-owned contract and implementation steps are in the
[`Iteration Quality Kernel design`](../superpowers/specs/2026-08-08-iteration-quality-kernel-design.md)
and
[`S1 policy/planner plan`](../superpowers/plans/2026-08-08-iteration-quality-s1-policy-planner.md).

## Worktree inventory

Use the report-only worktree inventory when deciding which worktrees need
preservation or manual cleanup review:

```bash
make worktree-audit
WORKTREE_AUDIT_FORMAT=json make worktree-audit
```

The audit only uses read-only Git commands; it does not fetch, prune, repair,
remove, delete, check out, reset, clean, or otherwise mutate Git state. Its
review hints are decision aids, not authorization to remove a worktree or
branch, and reported risk does not make the command fail. An unreadable
worktree or invalid Git porcelain report does fail the audit so the inventory
cannot be treated as complete.

## Check module boundaries and run deep discovery

For a direct diagnostic, run:

```bash
make check-boundaries
make verify-deep
```

`check-boundaries` is selected as a required target for every
non-documentation plan and therefore also runs during the applicable merge
verification. It compares repository-internal production and test-only import
edges globally, but fails only for new forbidden production edges or new
packages beneath a configured flat root.

`verify-deep` is opt-in and independent from focused/merge evidence. It
selects bounded fault, fuzz, real-binary E2E, and PTY targets from the current
diff, stops at the first `fail` or required `blocked` result, and writes only
the strict `build/iteration/<diff-digest>/deep-intake.json`. A retry cannot
replace that intake. The first failing report normalizes volatile timing and
exit metadata to the locked intake fields; later invocations replay the same
ordered report and original platform without running a target. A passing deep
run and a `not_applicable` platform result never promote or weaken
`evidence_ready`.

Boundary enforcement and deep defect discovery are implemented. Overall S4
product-module acceptance remains open until an accepted `Ready` product
slice touches a broad owner and demonstrates behavior-preserving deepening in
its own test-first plan. Tooling cleanup, file-size reduction, or a deferred
gap does not satisfy that gate. This terminal governance state does not create
a migration queue row and does not imply that P44 is ready.

The executable details and privacy-safe defect workflow are owned by the
[`S4 defect and boundary plan`](../superpowers/plans/2026-08-08-iteration-quality-s4-defect-boundaries.md)
and [testing strategy](testing-strategy.md).

## Persist evidence for a reviewed change

Use `$iteration-workflow` as the only shared owner for this sequence. The
retired compatibility alias is no longer an active project skill.

After the planner has selected the change's risk evidence, use:

```bash
make verify-focused
# Commit the reviewed candidate, leaving its tracked tree clean.
make verify-merge
make change-evidence-ready
```

`make verify` remains the ordinary `fmt-check`, lint, test, and build gate; it
does not persist iteration evidence. `make verify-focused` persists the exact
focused results for the current diff digest. `make verify-merge` requires those
same-digest results, a named clean non-`master` topic branch without a merge or
rebase, runs `fmt`, replans, and stops if formatting made the focused evidence
stale. It reaches `evidence_ready` only after every applicable merge gate has
completed, passing through `merge_verified` during the final persistence.

Committing an otherwise identical candidate changes only the plan's `head`.
In that one transition, `verify-merge` may explicitly move complete successful
focused gates to the new head after confirming the clean topic branch. It
rejects any other plan difference and never carries failed, blocked, or merge
results. Ordinary `evidence` reads and pre-push `--require-ready` checks never
perform this transition; merge gates still run for the exact committed head.

Results are retained below `build/iteration/<diff-digest>/` with bounded target
logs. The first executed failure is retained permanently for that evidence; a
retry cannot turn it into a pass. Only an unexecuted `blocked` placeholder can
be replaced when its target first executes. When `.reference` is absent,
iteration evidence marks `docs-check` not applicable, but it still executes
`docs-check-ci`.

The versioned pre-push hook checks each non-deletion commit being pushed with
`iteration evidence --require-ready --head <sha>`. The check is read-only: it
does not rerun gates, and it fails unless the stored base, head, diff digest,
gate state, and clean committed tree match that exact object. This evidence
check runs after the protected-`master` rule, so `EINO_ALLOW_MASTER_PUSH=1`
bypasses only direct-push protection and still requires ready evidence.

`EINO_ALLOW_STALE_EVIDENCE=1` is a separate recovery escape hatch. It prints a
warning and bypasses only committed-evidence lookup; it cannot bypass protected
`master`, malformed ref input, or normal review responsibility. Record either
bypass explicitly in the final handoff.

`coverage.json` is advisory output, never a gate. `make test-e2e` is hermetic
real-binary evidence with a local scripted provider and independent file, Git,
and tool-result oracles. It covers permission rejection, read/edit/test,
malformed input, streamed cancellation, write-based resume, and typed-overload
fallback, plus the selected engine, ACP, race, and PTY seams. `make
eval-baseline` remains a separate product evaluation. Do not substitute either
for applicable fuzz, race, PTY, live-provider, or Computer Use evidence.

## Optional Codex lifecycle hooks

The tracked `.codex/hooks.json` adds concise SessionStart context and maintains
diff/evidence state after tool, subagent, stop, and session lifecycle events.
Codex runs project hooks only after the user trusts the exact project hook
definition; disabling the `hooks` feature or omitting the config leaves the
manual Make workflow unchanged. See the
[official Codex hook documentation](https://learn.chatgpt.com/docs/hooks).

Matching hooks may run concurrently, so the adapter serializes same-session
state transitions under `build/iteration/hooks/`. It accepts one bounded JSON
document, persists only allowlisted iteration/session fields, never parses the
transcript, and emits no routine status messages. SessionStart context is
bounded to 2,048 UTF-8 bytes; Stop may request one continuation when this
session created a tracked diff and current evidence is stale.

Hooks are optional context maintenance, not a security or completion boundary.
Turning them off does not bypass the manual workflow, the versioned pre-push
checks, or `$skill-runtime` admission and terminal judgments. Run
`bash .codex/hooks/iteration_test.sh` to exercise the adapter and the
hooks-present/hooks-absent repository paths without reading a real transcript.

## Evidence interpretation

A green narrow test proves only its stated boundary. A package in
`go list -deps` proves reachability, not that every helper is wired. A command in
the registry proves discoverability, not that all dependencies are constructed.
Use [`architecture/code-map.md`](../architecture/code-map.md) and the owning
current document to interpret the result.
