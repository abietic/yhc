# Agent Runtime Instruction Freshness Implementation Plan

> **Historical use:** Retained as completed implementation evidence; do not
> execute this checklist as current work.

**Status:** historical
**Created:** 2026-08-07

> **Ownership:** test-first execution plan for the accepted
> [Agent Runtime Instruction Freshness design](../specs/2026-08-07-agent-runtime-instruction-freshness-design.md)

**Goal:** Make the repository-root Agent instructions name the current
production query owners and fail `docs-check` when those stable ownership
claims regress.

**Architecture:** `AGENTS.md` remains human-owned policy. A narrow validator in
`scripts/docs_check` checks only the root file, requires the stable
`QueryEngine`/`projectGraphQueryKernel`/architecture-link contract, and rejects
the two retired ownership claims. Existing engine tests remain the runtime
wiring oracle; the documentation checker does not parse Go call graphs.

**Tech Stack:** Go 1.26.5, Markdown, standard-library `filepath`/`strings`, the
existing white-box docs checker tests, and repository Makefile gates.

## Execution Record

PR #309 completed this plan and was squash-merged as `46860bbe`. The root
instruction contract, narrow checker guard, focused runtime ownership oracle,
independent review, and full local gates all completed. The planned
intermediate test and implementation commits were consolidated into one
reviewable PR candidate; the checked steps below record completed outcomes,
not a claim that every suggested intermediate commit remained in history.

## Global Constraints

- Execute this plan as one independently reviewable documentation-governance
  PR from current `origin/master`.
- Adoption is `project-native`: current source and architecture ownership are
  authoritative; no reference repository defines this prose contract.
- Do not change runtime behavior, public APIs, durable formats, provider
  routing, permission behavior, or entrypoint support.
- Validate only a repository-root `AGENTS.md` when that file exists. Existing
  fixture repositories without it must remain valid.
- Keep the guard intentionally narrow: do not parse Go source, require exact
  paragraphs, or freeze incidental wording.
- Preserve unrelated `PROJECT_GUIDE.md` and `artifacts/` worktree content.
- Because the checker is Go code, final verification must use `make fmt`,
  `make lint`, `make test`, and `make build`, plus `make docs-check` and
  `git diff --check`.

---

## File Structure

| File | Responsibility in this change |
|---|---|
| `scripts/docs_check/main_test.go` | Prove valid instructions pass, each missing owner/link fails, each retired claim fails, and missing `AGENTS.md` remains optional. |
| `scripts/docs_check/main.go` | Validate only the root `AGENTS.md` against the stable ownership contract. |
| `AGENTS.md` | Replace the retired imperative-loop claims with the current Query Engine and ProjectGraph ownership. |

### Task 1: Specify the root instruction contract with failing fixtures

**Files:**

- Modify: `scripts/docs_check/main_test.go`

**Interfaces:**

- Consumes: `checkRepository(root string) checkResult`, `writeFixture`, and
  `errorsText`.
- Produces: fixture-only helper `validAgentInstructions() string`; no production
  API.

- [x] **Step 1: Add a valid minimal instruction fixture helper**

Add this helper near the other test helpers:

```go
func validAgentInstructions() string {
	return strings.Join([]string{
		"# Agent Instructions",
		"",
		"QueryEngine owns conversation and session composition.",
		"projectGraphQueryKernel owns the production traversal shared by direct Query calls.",
		"See [Query Engine architecture]" +
			"(docs/architecture/runtime/query-engine.md).",
		"",
	}, "\n")
}
```

Every test that writes this fixture must also write a reachable target at
`docs/architecture/runtime/query-engine.md`, linked from `docs/README.md`, with
valid status and ownership metadata. This keeps failures attributable to the
new contract rather than an existing link or reachability rule.

- [x] **Step 2: Add the positive and optional-file tests**

Add `TestCheckRepositoryAcceptsCurrentAgentRuntimeOwnership` using
`validAgentInstructions()` and assert `len(result.errs) == 0`.

Add `TestCheckRepositoryAllowsMissingAgentInstructions` with only a valid
`docs/README.md` and assert the checker remains green. Keep this explicit test
even though existing fixtures also omit `AGENTS.md`; it freezes the optional
root-file behavior.

- [x] **Step 3: Add table-driven missing-contract failures**

Add `TestCheckRepositoryRejectsMissingAgentRuntimeOwnership`:

```go
tests := []struct {
	name   string
	remove string
	want   string
}{
	{"query engine", "QueryEngine", `AGENTS.md: missing required runtime owner "QueryEngine"`},
	{"project graph kernel", "projectGraphQueryKernel", `AGENTS.md: missing required runtime owner "projectGraphQueryKernel"`},
	{"architecture link", "docs/architecture/runtime/query-engine.md", `AGENTS.md: missing required runtime architecture link "docs/architecture/runtime/query-engine.md"`},
}
```

For each case, replace exactly one token in the valid fixture, run
`checkRepository`, and assert `errorsText(result.errs)` contains `want`.

- [x] **Step 4: Add table-driven retired-claim failures**

Add `TestCheckRepositoryRejectsRetiredAgentRuntimeClaims` with the exact
forbidden substrings:

```go
tests := []struct {
	name  string
	claim string
}{
	{"imperative loop", "imperative agent loop, not graph-based"},
	{"query go authority", "`engine/query.go` remains the production loop authority"},
}
```

Append each claim to an otherwise valid fixture and assert a diagnostic of the
form:

```text
AGENTS.md: contains retired runtime ownership claim "..."
```

- [x] **Step 5: Run the focused test and verify red**

Run:

```bash
go test ./scripts/docs_check -run 'AgentRuntimeOwnership|AgentInstructions' -count=1
```

Expected: FAIL because `checkRepository` does not yet inspect root Agent
instruction ownership. Record no success claim until every new negative
fixture fails for the intended missing diagnostic.

- [x] **Step 6: Commit the red tests**

```bash
git add scripts/docs_check/main_test.go
git commit -m "test: specify agent runtime instruction ownership"
```

### Task 2: Add the narrow checker and correct the root instructions

**Files:**

- Modify: `scripts/docs_check/main.go`
- Modify: `AGENTS.md`

**Interfaces:**

- Produces: unexported
  `hasLocalMarkdownDestination(data []byte, want string) bool` and
  `validateAgentRuntimeOwnership(root, path string, data []byte) []error`.
- Preserves: `checkRepository` result fields and all existing link, metadata,
  confinement, and reachability behavior.

- [x] **Step 1: Implement the root-only validator**

Add these helpers after `validateDocumentMetadata`:

```go
func hasLocalMarkdownDestination(data []byte, want string) bool {
	text := string(data)
	for _, match := range inlineLinkRE.FindAllStringSubmatchIndex(text, -1) {
		raw := text[match[2]:match[3]]
		destination, _, local := parseDestination(raw)
		if local && filepath.ToSlash(filepath.Clean(destination)) == want {
			return true
		}
	}
	return false
}

func validateAgentRuntimeOwnership(root, path string, data []byte) []error {
	if filepath.Clean(path) != filepath.Join(root, "AGENTS.md") {
		return nil
	}

	text := string(data)
	var errs []error
	for _, required := range []struct {
		kind  string
		value string
	}{
		{"runtime owner", "QueryEngine"},
		{"runtime owner", "projectGraphQueryKernel"},
	} {
		if !strings.Contains(text, required.value) {
			errs = append(errs, fmt.Errorf("%s: missing required %s %q",
				displayPath(root, path), required.kind, required.value))
		}
	}
	const architecturePath = "docs/architecture/runtime/query-engine.md"
	if !hasLocalMarkdownDestination(data, architecturePath) {
		errs = append(errs, fmt.Errorf("%s: missing required runtime architecture link %q",
			displayPath(root, path), architecturePath))
	}

	for _, retired := range []string{
		"imperative agent loop, not graph-based",
		"`engine/query.go` remains the production loop authority",
	} {
		if strings.Contains(text, retired) {
			errs = append(errs, fmt.Errorf("%s: contains retired runtime ownership claim %q",
				displayPath(root, path), retired))
		}
	}
	return errs
}
```

Inside the existing `for _, source := range allFiles` loop, call it immediately
after a successful confined read and before link validation:

```go
result.errs = append(result.errs,
	validateAgentRuntimeOwnership(root, source, data)...)
```

Do not make `AGENTS.md` mandatory and do not apply the helper to nested files
with the same basename. Requiring a parsed Markdown destination prevents plain
prose containing the path from satisfying the link contract.

- [x] **Step 2: Replace the stale architecture summary**

In `AGENTS.md`, replace the `engine/` architecture bullet with language that
contains all three required stable anchors and links the current owner:

```markdown
- **`engine/`** — `QueryEngine` session/runtime composition and the single
  production `projectGraphQueryKernel` traversal shared by supported
  entrypoints; see the Query Engine architecture link.
```

Render `Query Engine architecture` as an inline Markdown link whose destination
is exactly `docs/architecture/runtime/query-engine.md`.

- [x] **Step 3: Replace the stale product-direction rule**

Replace the statement that `engine/query.go` is the production loop authority
with:

```markdown
- Current source, production wiring, and focused tests define current behavior.
  `QueryEngine` owns conversation/session composition and
  `projectGraphQueryKernel` owns the single production traversal shared by
  direct `Query` and supported engine entrypoints. Follow the current Query
  Engine architecture link.
```

Render `current Query Engine architecture` as an inline Markdown link whose
destination is exactly `docs/architecture/runtime/query-engine.md`.

Keep the adjacent rules about project-owned contracts, reference comparison,
permissions, cancellation, persistence, and recovery unchanged.

- [x] **Step 4: Run focused green tests**

```bash
go test ./scripts/docs_check -run 'AgentRuntimeOwnership|AgentInstructions' -count=1
go test ./scripts/docs_check -count=1
```

Expected: PASS. If an existing fixture fails merely because it omits
`AGENTS.md`, fix the validator scope rather than adding synthetic Agent files to
every fixture.

- [x] **Step 5: Re-run the runtime ownership oracle**

```bash
go test ./engine -run '^(TestP1310ProductionProjectGraphMatchesCompiledGraphFixtures|TestP1310ProductionKernelIsProjectGraphWithoutFixtureEffects)$' -count=1
make docs-check
```

Expected: PASS. The engine tests prove the prose still names the actual
production kernel; `make docs-check` proves the real root file satisfies the
new invariant.

- [x] **Step 6: Commit the implementation**

```bash
git add AGENTS.md scripts/docs_check/main.go
git commit -m "docs: guard current query runtime ownership"
```

### Task 3: Verify and prepare the independent PR

**Files:**

- Verify only; no planned content changes.

- [x] **Step 1: Run formatting and documentation gates**

```bash
make fmt
make docs-check
git diff --check origin/master...HEAD
```

If `make fmt` changes Go files, inspect and commit only task-owned formatting.

- [x] **Step 2: Run all repository-required code gates**

```bash
make lint
make test
make build
```

All four project code gates (`fmt`, `lint`, `test`, `build`) and `docs-check`
must pass locally. CI unavailability may be reported separately, but it does
not turn a failing local gate into success.

- [x] **Step 3: Inspect the final diff and commit graph**

```bash
git status --short
git diff --stat origin/master...HEAD
git diff origin/master...HEAD -- AGENTS.md scripts/docs_check/main.go scripts/docs_check/main_test.go
git log --oneline origin/master..HEAD
```

Confirm the diff contains only the three planned files, no runtime code, and
no unrelated worktree content.

- [x] **Step 4: Push and open a ready PR**

Use a short branch such as `codex/docs/agent-runtime-instruction-freshness`.
The PR must state:

- user problem: coding agents can follow a retired production-loop owner;
- scope: root Agent prose plus a narrow docs checker invariant;
- adoption: `project-native`;
- compatibility: no runtime or public-contract change;
- rollback: revert the prose and validator together;
- evidence: focused docs-check tests, P13.10 kernel tests, and all Makefile gates.

Resolve review findings, squash-merge through the protected `master` workflow,
then delete the topic branch. Start the closed-gap traceability implementation
only from the resulting current `origin/master`.
