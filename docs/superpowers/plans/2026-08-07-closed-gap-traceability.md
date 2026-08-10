# Closed Gap Traceability Implementation Plan

> **Historical use:** Retained as completed implementation evidence; do not
> execute this checklist as current work.

**Status:** historical
**Created:** 2026-08-07

> **Ownership:** test-first execution plan for the accepted
> [Closed Gap Traceability design](../specs/2026-08-07-closed-gap-traceability-design.md)

**Goal:** Make every closed root Gap directly discoverable from its historical
closeout, enforce one canonical owner, and reject overlap with unresolved Gap
rows.

**Architecture:** Historical closeout Markdown remains the durable fact owner.
An optional `**Closed gaps:**` field within the first 30 lines records root Gap
IDs. `scripts/docs_check` parses only history metadata and root Gap rows in
`REMAINING.md`, then enforces canonical formatting, numeric order, uniqueness,
and open/closed disjointness. No second migration-state ledger is introduced.

**Tech Stack:** Go 1.26.5, Markdown lifecycle metadata, standard-library
`filepath`/`regexp`/`sort`/`strconv`/`strings`, white-box fixture tests, and
repository Makefile gates.

## Execution Record

PR #312 completed this plan and was squash-merged as `fc768b1a`. The branch
rebased after PR #311 closed G38, so the final integration adds G38's new
historical owner beyond the design's frozen G1-G37 bootstrap input. Independent
review also hardened the accepted parser to ignore fenced Markdown examples
and compare arbitrarily large numeric Gap IDs without integer overflow. The
planned intermediate test, checker, mapping, and policy commits were
consolidated into one reviewable PR candidate; the checked steps below record
completed outcomes rather than separate retained commits.

## Global Constraints

- Execute this plan as a second independent PR from current `origin/master`
  after the Agent runtime instruction freshness PR is merged or rebased.
- Adoption is `project-native`: `REMAINING.md` owns unresolved root Gaps and
  `docs/migration/history/` owns completed delivery evidence.
- Do not change migration queue state, Ready-slice ordering, runtime behavior,
  public APIs, durable product data, or the resolved/unresolved decision for
  any Gap.
- `**Closed gaps:**` is optional for history documents that close no root Gap.
- Accept only `G[1-9][0-9]*`, separated by comma plus one space, in strictly
  increasing numeric order. `G0`, leading zeroes, sub-program IDs such as
  `G11.F2`, duplicates, and alternative separators are invalid.
- Enforce uniqueness across every Markdown file under
  `docs/migration/history/`; reject any identity also present as a root Gap row
  in `docs/migration/REMAINING.md`.
- Backfill exactly the mapping frozen in the accepted design. G2, G14, G21,
  and G28 remain unresolved and must never appear in closeout metadata.
- Preserve unrelated `PROJECT_GUIDE.md` and `artifacts/` worktree content.
- Because the checker is Go code, final verification must use `make fmt`,
  `make lint`, `make test`, and `make build`, plus `make docs-check`, a complete
  mapping audit, and `git diff --check`.

---

## File Structure

| File | Responsibility in this change |
|---|---|
| `scripts/docs_check/main_test.go` | Specify canonical parsing, malformed metadata, duplicate ownership, unresolved overlap, and optional metadata. |
| `scripts/docs_check/main.go` | Cache confined Markdown reads, parse the metadata, and enforce repository-wide traceability invariants. |
| `docs/migration/history/README.md` | Explain where the closeout field belongs and what it owns. |
| `docs/contributing/documentation-policy.md` | Require the field when a closeout resolves a root Gap. |
| Bootstrap history files listed in Task 3 | Become the durable owners of G1, G3-G13, G15-G20, G22-G27, and G29-G37. |

### Task 1: Specify canonical closed-Gap metadata

**Files:**

- Modify: `scripts/docs_check/main_test.go`

**Interfaces:**

- Consumes: `checkRepository`, `writeFixture`, and `errorsText`.
- Produces: test-only fixture helpers for a reachable history tree and
  unresolved root Gap table.

- [x] **Step 1: Add a minimal history fixture builder**

Add a helper that creates all required reachable documents:

```go
func writeGapTraceabilityFixture(t *testing.T, root string) {
	t.Helper()
	writeFixture(t, root, "docs/README.md", "# Docs\n\n**Status:** current\n\n> **Ownership:** docs index\n\n[Migration]"+"(migration/README.md)\n")
	writeFixture(t, root, "docs/migration/README.md", "# Migration\n\n**Status:** current\n\n> **Ownership:** migration index\n\n[Remaining]"+"(REMAINING.md)\n[History]"+"(history/README.md)\n")
	writeFixture(t, root, "docs/migration/REMAINING.md", "# Remaining\n\n**Status:** gap-inventory\n\n> **Ownership:** unresolved gaps\n\n| Gap | State |\n|---|---|\n")
	writeFixture(t, root, "docs/migration/history/README.md", "# History\n\n**Status:** historical\n\n> **Ownership:** history index\n")
}
```

Individual tests may append links to closeout fixtures and root Gap rows. Do
not bypass `checkRepository`: integration fixtures prove the new validation
coexists with existing metadata, confinement, link, and reachability rules.

- [x] **Step 2: Add canonical acceptance coverage**

Add `TestCheckRepositoryAcceptsCanonicalClosedGapMetadata` with subtests:

- `single`: a reachable closeout contains `**Closed gaps:** G22`;
- `multiple`: a reachable closeout contains `**Closed gaps:** G6, G7`;
- `optional`: a reachable closeout has no `Closed gaps` field.

Each closeout must have `**Status:** historical` and `**Ownership:**` within its
first 30 lines. Assert `len(result.errs) == 0` for every subtest.

- [x] **Step 3: Add malformed-field coverage**

Add `TestCheckRepositoryRejectsMalformedClosedGapMetadata` with these exact
cases and diagnostic substrings:

| Value | Expected diagnostic substring |
|---|---|
| `G6,G7` | `must use comma-space separators` |
| `G0` | `invalid closed Gap ID "G0"` |
| `G06` | `invalid closed Gap ID "G06"` |
| `G7, G6` | `must be in strictly increasing numeric order` |
| `G6, G6` | `duplicates closed Gap G6` |
| `G11.F2` | `invalid closed Gap ID "G11.F2"` |

Also add a case where `**Closed gaps:**` appears after line 30. It must fail
with `Closed gaps metadata must appear in first 30 lines`; this prevents a
syntactically valid but lifecycle-invisible field from being silently ignored.

- [x] **Step 4: Add repository-wide conflict coverage**

Add `TestCheckRepositoryRejectsDuplicateClosedGapOwners`. Create two reachable
history closeouts that both declare G22. Assert the error includes:

```text
closed Gap G22 has multiple historical owners
```

and both paths:

```text
docs/migration/history/runtime/first.md
docs/migration/history/runtime/second.md
```

Add `TestCheckRepositoryRejectsOpenAndClosedGapOverlap`. Put a root row such as
`| G22 | unresolved |` in `REMAINING.md`, declare G22 in one closeout, and
assert:

```text
closed Gap G22 is still present in docs/migration/REMAINING.md
```

- [x] **Step 5: Run the focused tests and verify red**

```bash
go test ./scripts/docs_check -run 'ClosedGap|GapTraceability' -count=1
```

Expected: FAIL because no closed-Gap metadata parser or repository-wide
invariant exists. Check that every negative fixture fails for the intended new
diagnostic rather than reachability or generic document metadata.

- [x] **Step 6: Commit the red tests**

```bash
git add scripts/docs_check/main_test.go
git commit -m "test: specify closed gap traceability"
```

### Task 2: Implement parsing and repository-wide validation

**Files:**

- Modify: `scripts/docs_check/main.go`

**Interfaces:**

- Produces: unexported `closedGapOwner`, `firstDocumentLines`,
  `isHistoryMarkdown`, `parseClosedGapMetadata`, `parseOpenRootGaps`, and
  `validateClosedGapTraceability` helpers.
- Preserves: all existing `checkRepository` link, metadata, confinement, and
  reachability contracts.

- [x] **Step 1: Add the explicit regular expressions and owner value**

Add these package-level expressions next to existing metadata expressions:

```go
closedGapsRE   = regexp.MustCompile(`(?m)^\*\*Closed gaps:\*\*[ \t]+([^\r\n]+)$`)
closedGapAnyRE = regexp.MustCompile(`(?m)^\*\*Closed gaps:\*\*`)
closedGapIDRE  = regexp.MustCompile(`^G([1-9][0-9]*)$`)
openGapRowRE   = regexp.MustCompile(`(?m)^\|[ \t]*(G[1-9][0-9]*)[ \t]*\|`)
```

Add:

```go
type closedGapOwner struct {
	id   string
	path string
}
```

Use a struct rather than parallel maps so diagnostics can retain both owners.

- [x] **Step 2: Cache confined reads in `checkRepository`**

Create `contentCache := make(map[string][]byte, len(allFiles))` before the
source loop. After each successful `readFileConfined`, store a defensive copy:

```go
contentCache[source] = bytes.Clone(data)
```

After the loop and before reachability validation, call:

```go
result.errs = append(result.errs,
	validateClosedGapTraceability(root, docFiles, contentCache)...)
```

Do not re-read files outside `readFileConfined`; one confined snapshot must
drive metadata, links, and the new cross-file invariant.

- [x] **Step 3: Parse only history documents and only lifecycle metadata**

Implement a shared first-30-lines helper:

```go
func firstDocumentLines(data []byte, limit int) string {
	lines := strings.Split(string(data), "\n")
	if len(lines) > limit {
		lines = lines[:limit]
	}
	return strings.Join(lines, "\n")
}
```

Refactor `validateDocumentMetadata` to call
`firstDocumentLines(data, 30)` without changing its observable diagnostics.

Recognize history Markdown by repository-relative path, not a raw substring:

```go
func isHistoryMarkdown(root, path string) bool {
	rel, err := filepath.Rel(filepath.Join(root, "docs", "migration", "history"), path)
	return err == nil && rel != "." && rel != ".." &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator)) &&
		strings.EqualFold(filepath.Ext(path), ".md")
}
```

In `parseClosedGapMetadata`, inspect the whole document with
`closedGapAnyRE`. If a field exists but no canonical field is found in the
first 30 lines, return the line-placement or malformed-field diagnostic; never
silently treat it as absent.

- [x] **Step 4: Enforce canonical IDs and numeric order**

For the one field captured in the first 30 lines, reject every comma that is
not followed by exactly one space or is preceded by whitespace:

```go
raw := strings.TrimSpace(match[1])
for index := 0; index < len(raw); index++ {
	if raw[index] != ',' {
		continue
	}
	if index == 0 || raw[index-1] == ' ' || index+2 >= len(raw) ||
		raw[index+1] != ' ' || raw[index+2] == ' ' {
		return nil, fmt.Errorf("%s: Closed gaps metadata must use comma-space separators",
			displayPath(root, path))
	}
}
parts := strings.Split(raw, ", ")
```

Reject a second field in the same prefix. For every part, match
`closedGapIDRE`, parse the numeric capture with `strconv.Atoi`, and require the
number to be greater than the prior number. Use a local `seen` map so a repeated
ID reports `duplicates closed Gap G<number>` before the generic order error.

Return a `[]closedGapOwner` with the canonical ID and source path. Do not accept
or normalize malformed input.

- [x] **Step 5: Parse unresolved root rows and validate cross-file state**

`parseOpenRootGaps` reads only the cached
`docs/migration/REMAINING.md` snapshot and returns a set from
`openGapRowRE`. A missing file in a minimal fixture yields an empty set; the
existing docs-link rules remain responsible for real-repository presence.

In `validateClosedGapTraceability`:

1. parse history files in sorted `docFiles` order;
2. collect owners by Gap ID;
3. sort IDs numerically before emitting diagnostics;
4. for owner lists longer than one, sort display paths and report every path in
   one deterministic error; and
5. reject every closed ID also found in the unresolved set.

Use this diagnostic shape:

```go
fmt.Errorf("closed Gap %s has multiple historical owners: %s",
	id, strings.Join(displayPaths, ", "))
fmt.Errorf("closed Gap %s is still present in docs/migration/REMAINING.md (historical owner: %s)",
	id, displayPath(root, owners[0].path))
```

- [x] **Step 6: Run focused and complete checker tests**

```bash
go test ./scripts/docs_check -run 'ClosedGap|GapTraceability' -count=1
go test ./scripts/docs_check -count=1
```

Expected: PASS. Existing diagnostics and fixture repositories without history
metadata must remain unchanged.

- [x] **Step 7: Commit the checker implementation**

```bash
git add scripts/docs_check/main.go
git commit -m "docs: validate closed gap ownership"
```

### Task 3: Backfill the frozen historical owners

**Files:**

- Modify: `docs/migration/history/runtime/p34-1-file-state-checkpoint-repair.md`
- Modify: `docs/migration/history/runtime/p28-h0-standalone-mcp-permission-policy.md`
- Modify: `docs/migration/history/runtime/p32-1-plugin-file-authority.md`
- Modify: `docs/migration/history/runtime/p33-1-mcp-live-tool-generation.md`
- Modify: `docs/migration/history/tui/p19-3-5-welcome-wordmark.md`
- Modify: `docs/migration/history/tui/p35-1-tui-notification-lifecycle.md`
- Modify: `docs/migration/history/tui/g9-e-table-repair-deletion.md`
- Modify: `docs/migration/history/runtime/p20-r3-plan-interaction-closeout.md`
- Modify: `docs/migration/history/tui/g11-f2-terminal-program-closeout.md`
- Modify: `docs/migration/history/tui/p40-1-startup-theme-polarity.md`
- Modify: `docs/migration/history/runtime/p22-h0-bash-containment.md`
- Modify: `docs/migration/history/runtime/p23-h0-session-deletion-containment.md`
- Modify: `docs/migration/history/runtime/p23-4b-acp-replay-bounded-listing.md`
- Modify: `docs/migration/history/runtime/p23-5-transactional-stdio-mcp.md`
- Modify: `docs/migration/history/runtime/p23-2-acp-tool-lifecycle.md`
- Modify: `docs/migration/history/runtime/p23-3-acp-assistant-commands.md`
- Modify: `docs/migration/history/runtime/p36-1-acp-rich-assistant-replay.md`
- Modify: `docs/migration/history/runtime/p25-agentic-provider-input-fidelity.md`
- Modify: `docs/migration/history/runtime/p26-canonical-model-round-owner-cleanup.md`
- Modify: `docs/migration/history/tui/g24-plan-confirmation-input-isolation.md`
- Modify: `docs/migration/history/tui/p41-1-fixed-size-geometry-owner.md`
- Modify: `docs/migration/history/tui/p41-2-bounded-markdown-renderer-pool.md`
- Modify: `docs/migration/history/tui/g27-result-bound-command-recency.md`
- Modify: `docs/migration/history/runtime/p43-0-real-repository-evaluation.md`
- Modify: `docs/migration/history/tui/p27-selection-viewport-geometry.md`
- Modify: `docs/migration/history/runtime/p29-4-bounded-overload-failover.md`
- Modify: `docs/migration/history/runtime/p30-6-multimodal-program-closeout.md`
- Modify: `docs/migration/history/runtime/p31-5-old-owner-closeout.md`
- Modify: `docs/migration/history/runtime/p38-0-provider-reasoning-origin.md`
- Modify: `docs/migration/history/runtime/p37-1-project-graph-permission-settlement-chain.md`
- Modify: `docs/migration/history/runtime/p46-1-complete-prompt-footprint.md`
- Modify: `docs/migration/history/runtime/p46-2-observable-failover.md`

- [x] **Step 1: Insert metadata without rewriting historical evidence**

For each file, insert exactly one field within the first 30 lines, adjacent to
existing lifecycle metadata. Apply this frozen mapping:

```text
G1     docs/migration/history/runtime/p34-1-file-state-checkpoint-repair.md
G3     docs/migration/history/runtime/p28-h0-standalone-mcp-permission-policy.md
G4     docs/migration/history/runtime/p32-1-plugin-file-authority.md
G5     docs/migration/history/runtime/p33-1-mcp-live-tool-generation.md
G6, G7 docs/migration/history/tui/p19-3-5-welcome-wordmark.md
G8     docs/migration/history/tui/p35-1-tui-notification-lifecycle.md
G9     docs/migration/history/tui/g9-e-table-repair-deletion.md
G10    docs/migration/history/runtime/p20-r3-plan-interaction-closeout.md
G11    docs/migration/history/tui/g11-f2-terminal-program-closeout.md
G12    docs/migration/history/tui/p40-1-startup-theme-polarity.md
G13    docs/migration/history/runtime/p22-h0-bash-containment.md
G15    docs/migration/history/runtime/p23-h0-session-deletion-containment.md
G16    docs/migration/history/runtime/p23-4b-acp-replay-bounded-listing.md
G17    docs/migration/history/runtime/p23-5-transactional-stdio-mcp.md
G18    docs/migration/history/runtime/p23-2-acp-tool-lifecycle.md
G19    docs/migration/history/runtime/p23-3-acp-assistant-commands.md
G20    docs/migration/history/runtime/p36-1-acp-rich-assistant-replay.md
G22    docs/migration/history/runtime/p25-agentic-provider-input-fidelity.md
G23    docs/migration/history/runtime/p26-canonical-model-round-owner-cleanup.md
G24    docs/migration/history/tui/g24-plan-confirmation-input-isolation.md
G25    docs/migration/history/tui/p41-1-fixed-size-geometry-owner.md
G26    docs/migration/history/tui/p41-2-bounded-markdown-renderer-pool.md
G27    docs/migration/history/tui/g27-result-bound-command-recency.md
G29    docs/migration/history/runtime/p43-0-real-repository-evaluation.md
G30    docs/migration/history/tui/p27-selection-viewport-geometry.md
G31    docs/migration/history/runtime/p29-4-bounded-overload-failover.md
G32    docs/migration/history/runtime/p30-6-multimodal-program-closeout.md
G33    docs/migration/history/runtime/p31-5-old-owner-closeout.md
G34    docs/migration/history/runtime/p38-0-provider-reasoning-origin.md
G35    docs/migration/history/runtime/p37-1-project-graph-permission-settlement-chain.md
G36    docs/migration/history/runtime/p46-1-complete-prompt-footprint.md
G37    docs/migration/history/runtime/p46-2-observable-failover.md
```

Do not change narrative wording, status, evidence, source links, verification
claims, or historical dates unless `docs-check` reveals a pre-existing invalid
document directly blocking this scoped metadata change.

- [x] **Step 2: Run the real-tree checker and audit exact coverage**

```bash
make docs-check
rg -n '^\*\*Closed gaps:\*\*' docs/migration/history
```

Then run this independent read-only audit:

```bash
awk '/^\*\*Closed gaps:\*\*/ {print FILENAME ":" $0}' $(rg --files docs/migration/history -g '*.md') | sort
```

Confirm all and only the frozen IDs are present. In particular, the output must
not contain `G2`, `G14`, `G21`, or `G28` as standalone metadata values.

- [x] **Step 3: Commit the bootstrap mapping**

```bash
git add docs/migration/history/runtime docs/migration/history/tui
git commit -m "docs: map closed gaps to historical owners"
```

### Task 4: Document the lifecycle rule at the two policy owners

**Files:**

- Modify: `docs/migration/history/README.md`
- Modify: `docs/contributing/documentation-policy.md`

- [x] **Step 1: Explain the history-local convention**

In `docs/migration/history/README.md`, add a compact section near the lifecycle
metadata guidance with these exact facts:

- a root-Gap closeout declares `**Closed gaps:** G22` within its first 30 lines;
- multiple IDs use `**Closed gaps:** G6, G7`;
- IDs are canonical, numerically ordered, and unique across the history tree;
- closeouts with no root Gap omit the field; and
- sub-program IDs such as `G11.F2` remain narrative text.

- [x] **Step 2: Add the contributing requirement**

In `docs/contributing/documentation-policy.md`, state that moving a root Gap
out of `REMAINING.md` is incomplete until its historical closeout declares the
canonical `Closed gaps` field. Explain that `docs-check` rejects duplicate
owners and unresolved overlap. Do not prescribe a second ledger or require the
field on unrelated history documents.

- [x] **Step 3: Verify links, wording, and current ownership**

```bash
make docs-check
git diff --check
```

Review both pages to ensure `REMAINING.md` is still described only as the
unresolved inventory and history remains the completed-delivery owner.

- [x] **Step 4: Commit the policy documentation**

```bash
git add docs/migration/history/README.md docs/contributing/documentation-policy.md
git commit -m "docs: define closed gap metadata lifecycle"
```

### Task 5: Verify and prepare the independent PR

**Files:**

- Verify only; no planned content changes.

- [x] **Step 1: Run formatting, checker, and exact mapping audit**

```bash
make fmt
go test ./scripts/docs_check -count=1
make docs-check
git diff --check origin/master...HEAD
```

Compare the sorted `rg`/`awk` metadata output against Task 3's frozen mapping.
Every closed root Gap has exactly one owner; unresolved G2, G14, G21, and G28
have none.

- [x] **Step 2: Run all repository-required code gates**

```bash
make lint
make test
make build
```

All four project code gates (`fmt`, `lint`, `test`, `build`) and `docs-check`
must pass locally. CI quota exhaustion may be reported as a remote evidence
limitation only; it does not waive local failures.

- [x] **Step 3: Inspect final scope and history-only semantics**

```bash
git status --short
git diff --stat origin/master...HEAD
git diff origin/master...HEAD -- scripts/docs_check docs/migration/history docs/contributing/documentation-policy.md
git log --oneline origin/master..HEAD
```

Confirm there are no changes to `docs/migration/REMAINING.md`,
`docs/migration/queue.yaml`, generated `PLAN.md`, runtime source, or unrelated
worktree content.

- [x] **Step 4: Push and open a ready PR**

Use a short branch such as `codex/docs/closed-gap-traceability`. The PR must
state:

- user problem: current-tree audits cannot resolve several historical Gap IDs;
- scope: optional history metadata, checker invariants, frozen backfill, and
  documentation policy;
- adoption: `project-native`;
- compatibility: no runtime, migration queue, or unresolved-Gap change;
- rollback: revert metadata and validator together;
- evidence: focused fixtures, real-tree mapping audit, and all Makefile gates.

Resolve review findings, squash-merge through the protected `master` workflow,
then delete the topic branch.
