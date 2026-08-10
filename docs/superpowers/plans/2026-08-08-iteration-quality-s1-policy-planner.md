# Iteration Quality S1 Policy And Planner Implementation Plan

> **Historical use:** Retained as completed implementation evidence; do not
> execute this checklist as current work.

**Status:** historical
**Created:** 2026-08-08
**Completed:** 2026-08-09
**Plan state:** Executed; strict policy, read-only planner, and initial evidence schema

> **Ownership:** first executable stage of the accepted
> [Iteration Quality Kernel design](../specs/2026-08-08-iteration-quality-kernel-design.md)

**Goal:** Add one strict, versioned quality policy and one read-only planner
that turn a concrete Git diff into deterministic module, risk, owner-document,
and gate requirements without changing product runtime behavior.

**Architecture:** A thin `scripts/iteration` command reads
`quality/iteration.yaml`, resolves a merge-base against an explicit or default
base ref, computes a canonical tracked diff digest, and emits a versioned
`Plan`. The same command derives an initial `Evidence` view whose required
gates are `blocked` until S2 executes them. The existing migration queue stays
the product-order owner; a new read-only `describe` command supplies an
optional accepted-slice link without duplicating queue validation.

**Tech Stack:** Go 1.26.5, `gopkg.in/yaml.v3`, standard-library
`crypto/sha256`, `encoding/json`, `os/exec`, `path`, and `text/template`, the
existing migration-queue and docs-check packages, and repository Make targets.

## Global Constraints

- Implement this stage from current `origin/master` as one reviewable
  governance PR. Rebase or recreate the branch before implementation if the
  accepted design or Make targets drift.
- Adoption is `project-native`. Current source, Make targets, and owner docs
  define the policy; no reference repository is a runtime authority.
- Do not change engine, tool, provider, permission, persistence, TUI, ACP, MCP,
  or CLI product behavior in S1.
- Preserve the current meaning of `make verify` as
  `fmt-check lint test build`. S1 adds wrappers; it does not rewrite that target.
- Default base resolution is `git merge-base origin/master HEAD`. `--base`
  accepts another ref, but evidence stores the resolved commit, never only the
  symbolic ref.
- The tracked diff is `git diff --binary --full-index --no-ext-diff <base> --`.
  Its SHA-256 is the canonical `diff_digest`. Ignored and untracked content is
  outside that digest; record only the untracked count.
- Path patterns use repository-relative slash-separated paths. Implement
  segment matching locally: `*` and `?` use `path.Match`; a segment equal to
  `**` matches zero or more segments. Reject absolute paths, backslashes,
  traversal, empty patterns, and malformed segment globs. Do not add a glob
  dependency.
- Equal-priority matches owned by different modules or change classes fail.
  A higher priority wins. A path with no match fails closed.
- `docs/migration/queue.yaml` remains the only product-order SSOT. Zero active
  slices is valid. S1 may attach an accepted active `slice_id`; it must not
  create another queue or mark a deferred row executable.
- Persist no prompts, source text, diff content, transcript content,
  credentials, environment dumps, or full command output. S1 plan/evidence
  commands are read-only and write nothing under `build/iteration/`.
- All new Go tests use the repository's white-box package style and independent
  input/output oracles. Final verification uses all four Makefile gates because
  this stage adds Go code.

---

## Locked Interfaces

All later stages consume these exact JSON and Go concepts. A later schema
change requires a version bump and reader compatibility test; field renames are
not incidental refactors.

```go
type GateStatus string

const (
	GatePass          GateStatus = "pass"
	GateFail          GateStatus = "fail"
	GateBlocked       GateStatus = "blocked"
	GateNotApplicable GateStatus = "not_applicable"
)

type PathKind string

const (
	PathProduction PathKind = "production"
	PathTest       PathKind = "test"
	PathClass      PathKind = "class"
)

type ChangedPath struct {
	Path   string   `json:"path"`
	Status string   `json:"status"`
	Owner  string   `json:"owner"`
	Kind   PathKind `json:"kind"`
}

type FocusedCheck struct {
	Owner    string   `json:"owner"`
	Packages []string `json:"packages"`
}

type Plan struct {
	SchemaVersion       int           `json:"schema_version"`
	Repository          string        `json:"repository"`
	PolicyVersion       int           `json:"policy_version"`
	BaseRef             string        `json:"base_ref"`
	Base                string        `json:"base"`
	Head                string        `json:"head"`
	DiffDigest          string        `json:"diff_digest"`
	Slice               *SliceRef     `json:"slice,omitempty"`
	Platform            string        `json:"platform"`
	Changed             []ChangedPath `json:"changed"`
	Modules             []string      `json:"modules"`
	ChangeClasses       []string      `json:"change_classes"`
	Risks               []string      `json:"risks"`
	OwnerDocs           []string      `json:"owner_docs"`
	FocusedChecks       []FocusedCheck `json:"focused_checks"`
	RequiredTargets     []string      `json:"required_targets"`
	NotApplicable       []string      `json:"not_applicable"`
	OutsideUntracked    int           `json:"outside_untracked_count"`
}

type GateEvidence struct {
	Target           string     `json:"target"`
	Level            string     `json:"level"`
	Status           GateStatus `json:"status"`
	ExitCode         *int       `json:"exit_code,omitempty"`
	DurationMillis   int64      `json:"duration_ms"`
	FailureLogPath   string     `json:"failure_log_path,omitempty"`
	FirstFailingSeed string     `json:"first_failing_seed,omitempty"`
}

type Evidence struct {
	SchemaVersion int            `json:"schema_version"`
	Plan          Plan           `json:"plan"`
	State         string         `json:"state"`
	Gates         []GateEvidence `json:"gates"`
}
```

The only gate levels are `focused`, `merge`, `deep`, `remote`, and `live`. The
only lifecycle values are `planned`, `changed`, `focused_verified`,
`merge_verified`, and `evidence_ready`. S1 emits `planned` when the diff is
empty and `changed` otherwise. Every applicable required target initially has
status `blocked`; a policy-proven platform exclusion has status
`not_applicable`.

## File Structure

| File | Responsibility in this stage |
|---|---|
| `quality/iteration.yaml` | Versioned module, path, risk-pack, class, and initial boundary policy. |
| `scripts/iteration/main.go` | CLI dispatch for `plan`, `evidence`, and `policy-check`. |
| `scripts/iteration/policy.go` | Strict YAML decoding and semantic validation. |
| `scripts/iteration/matcher.go` | Small deterministic `**` path matcher. |
| `scripts/iteration/git.go` | Base/head/diff resolution through an injectable Git source. |
| `scripts/iteration/plan.go` | Priority-based classification, union, ordering, and slice attachment. |
| `scripts/iteration/evidence.go` | Initial gate-state derivation and JSON/Markdown rendering. |
| `scripts/iteration/*_test.go` | Policy, Git, classification, rendering, and CLI fixtures. |
| `scripts/iteration/testdata/` | Minimal valid and invalid policies; no repository snapshot copies. |
| `scripts/migration_queue/main.go` | Add the read-only `describe --slice-id` command. |
| `scripts/migration_queue/main_test.go` | Freeze active/deferred/missing slice lookup behavior. |
| `Makefile` | Add `change-plan`, `change-evidence`, and private policy-check wiring. |
| `docs/contributing/verification.md` | Explain planning output and its non-proof boundary. |

### Task 1: Freeze strict policy decoding and path matching

**Files:**

- Create: `quality/iteration.yaml`
- Create: `scripts/iteration/policy.go`
- Create: `scripts/iteration/matcher.go`
- Create: `scripts/iteration/policy_test.go`
- Create: `scripts/iteration/matcher_test.go`
- Create: `scripts/iteration/testdata/policy-valid.yaml`
- Create: `scripts/iteration/testdata/policy-unknown-field.yaml`

**Interfaces:**

- Produces: `loadPolicy(root *os.Root, name string) (Policy, error)`.
- Produces: `matchPathPattern(pattern, name string) (bool, error)`.
- Consumes: repository-root `Makefile` target names and owner-document paths
  only for validation; it never executes a target.

- [ ] **Step 1: Add matcher tests before implementation**

Add a table test with these exact positive and negative cases:

```go
func TestMatchPathPattern(t *testing.T) {
	tests := []struct {
		pattern string
		name    string
		want    bool
		wantErr bool
	}{
		{"engine/**/*.go", "engine/query.go", true, false},
		{"engine/**/*.go", "engine/session/store_test.go", true, false},
		{"**/*.md", "AGENTS.md", true, false},
		{"**/*.md", "docs/architecture/runtime/query-engine.md", true, false},
		{".codex/**", ".codex/hooks.json", true, false},
		{"tools/**/testdata/**", "tools/testdata/case/input.json", true, false},
		{"engine/**/*.go", "tools/read.go", false, false},
		{"../engine/**", "engine/query.go", false, true},
		{"/engine/**", "engine/query.go", false, true},
		{"engine\\**", "engine/query.go", false, true},
		{"", "engine/query.go", false, true},
	}
	for _, test := range tests {
		got, err := matchPathPattern(test.pattern, test.name)
		if (err != nil) != test.wantErr || got != test.want {
			t.Fatalf("matchPathPattern(%q, %q) = %v, %v", test.pattern, test.name, got, err)
		}
	}
}
```

- [ ] **Step 2: Run the matcher test and verify red**

```bash
go test ./scripts/iteration -run '^TestMatchPathPattern$' -count=1
```

Expected: FAIL because `matchPathPattern` does not exist.

- [ ] **Step 3: Implement the segment-recursive matcher**

Use memoized segment traversal so `**` cannot cause exponential work:

```go
func matchPathPattern(pattern, name string) (bool, error) {
	if pattern == "" || path.IsAbs(pattern) || strings.Contains(pattern, `\`) ||
		strings.Contains(pattern, "\x00") || strings.Contains(name, "\x00") {
		return false, fmt.Errorf("invalid repository path pattern %q", pattern)
	}
	if clean := path.Clean(pattern); clean != pattern || clean == ".." ||
		strings.HasPrefix(clean, "../") {
		return false, fmt.Errorf("invalid repository path pattern %q", pattern)
	}
	if clean := path.Clean(name); clean != name || clean == "." || clean == ".." ||
		strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
		return false, fmt.Errorf("invalid repository path %q", name)
	}

	patterns := strings.Split(pattern, "/")
	parts := strings.Split(name, "/")
	type key struct{ pattern, part int }
	memo := map[key]bool{}
	seen := map[key]bool{}
	var visit func(int, int) (bool, error)
	visit = func(i, j int) (bool, error) {
		state := key{i, j}
		if seen[state] {
			return memo[state], nil
		}
		seen[state] = true
		if i == len(patterns) {
			memo[state] = j == len(parts)
			return memo[state], nil
		}
		if patterns[i] == "**" {
			zero, err := visit(i+1, j)
			if err != nil || zero {
				memo[state] = zero
				return zero, err
			}
			if j < len(parts) {
				memo[state], err = visit(i, j+1)
				return memo[state], err
			}
			return false, nil
		}
		if j == len(parts) {
			return false, nil
		}
		matched, err := path.Match(patterns[i], parts[j])
		if err != nil || !matched {
			return false, err
		}
		memo[state], err = visit(i+1, j+1)
		return memo[state], err
	}
	return visit(0, 0)
}
```

- [ ] **Step 4: Add strict policy fixtures and validation tests**

Define the policy types without `map[string]any`:

```go
type Policy struct {
	Version       int                     `yaml:"version"`
	Repository    string                  `yaml:"repository"`
	Modules       map[string]ModulePolicy `yaml:"modules"`
	RiskPacks     map[string]RiskPack     `yaml:"risk_packs"`
	ChangeClasses map[string]ChangeClass  `yaml:"change_classes"`
	Boundaries    BoundaryPolicy          `yaml:"boundaries"`
}

type ModulePolicy struct {
	Priority          int        `yaml:"priority"`
	ProductionPaths   []PathRule `yaml:"production_paths"`
	TestPaths         []string   `yaml:"test_paths"`
	Packages          []string   `yaml:"packages"`
	OwnerDocs         []string   `yaml:"owner_docs"`
	Risks             []string   `yaml:"risks"`
	FocusedPackages   []string   `yaml:"focused_packages"`
}

type PathRule struct {
	Include string   `yaml:"include"`
	Exclude []string `yaml:"exclude"`
}

type RiskPack struct {
	Target      string   `yaml:"target"`
	DeepTargets []string `yaml:"deep_targets"`
	Platforms   []string `yaml:"platforms"`
}

type ChangeClass struct {
	Priority        int      `yaml:"priority"`
	Paths           []string `yaml:"paths"`
	Targets         []string `yaml:"targets"`
	FocusedPackages []string `yaml:"focused_packages"`
}

type BoundaryPolicy struct {
	ForbiddenProductionEdges []ForbiddenEdge `yaml:"forbidden_production_edges"`
	FlatPackageRoots          []string        `yaml:"flat_package_roots"`
}

type ForbiddenEdge struct {
	From []string `yaml:"from"`
	To   []string `yaml:"to"`
}
```

Tests must reject: an unknown YAML field, a second YAML document, version other
than 1, unknown risk, missing Make target, missing owner document, empty module,
duplicate list entries, invalid platform, malformed pattern, and an empty
repository identifier. Use `yaml.Decoder.KnownFields(true)` and the same
single-document check already proven in `scripts/migration_queue`.

- [ ] **Step 5: Add the initial repository policy**

Use this complete owner inventory; do not add a catch-all class:

```yaml
version: 1
repository: github.com/yuhaichuan/eino-agent

modules:
  engine-runtime:
    priority: 100
    production_paths:
      - include: engine/**
        exclude: [engine/**/*_test.go, engine/**/testdata/**]
    test_paths: [engine/**/*_test.go, engine/**/testdata/**]
    packages: [./engine/...]
    owner_docs: [docs/architecture/runtime/query-engine.md]
    risks: [contract, concurrency]
    focused_packages: [./engine/...]
  tool-runtime:
    priority: 100
    production_paths:
      - include: tools/**
        exclude: [tools/**/*_test.go, tools/**/testdata/**]
    test_paths: [tools/**/*_test.go, tools/**/testdata/**]
    packages: [./tools]
    owner_docs: [docs/architecture/capabilities/tool-registry.md]
    risks: [contract]
    focused_packages: [./tools]
  cli-entrypoint:
    priority: 100
    production_paths:
      - include: cmd/eino-agent/**
        exclude: [cmd/eino-agent/**/*_test.go, cmd/eino-agent/**/testdata/**]
    test_paths: [cmd/eino-agent/**/*_test.go, cmd/eino-agent/**/testdata/**]
    packages: [./cmd/eino-agent/...]
    owner_docs: [docs/architecture/platform/entrypoints-and-transports.md]
    risks: [contract, terminal]
    focused_packages: [./cmd/eino-agent/...]
  tui-adapter:
    priority: 100
    production_paths:
      - include: internal/tui/**
        exclude: [internal/tui/**/*_test.go, internal/tui/**/testdata/**]
    test_paths: [internal/tui/**/*_test.go, internal/tui/**/testdata/**]
    packages: [./internal/tui]
    owner_docs: [docs/architecture/tui/README.md]
    risks: [terminal, fuzz]
    focused_packages: [./internal/tui]
  acp-adapter:
    priority: 100
    production_paths:
      - include: server/acp/**
        exclude: [server/acp/**/*_test.go, server/acp/**/testdata/**]
    test_paths: [server/acp/**/*_test.go, server/acp/**/testdata/**]
    packages: [./server/acp]
    owner_docs: [docs/architecture/platform/acp-adapter.md]
    risks: [contract, concurrency]
    focused_packages: [./server/acp]
  mcp-adapter:
    priority: 100
    production_paths:
      - include: server/mcp/**
        exclude: [server/mcp/**/*_test.go, server/mcp/**/testdata/**]
    test_paths: [server/mcp/**/*_test.go, server/mcp/**/testdata/**]
    packages: [./server/mcp]
    owner_docs: [docs/architecture/capabilities/mcp.md]
    risks: [contract]
    focused_packages: [./server/mcp]
  build-metadata:
    priority: 100
    production_paths:
      - include: internal/buildinfo/**
        exclude: [internal/buildinfo/**/*_test.go]
    test_paths: [internal/buildinfo/**/*_test.go]
    packages: [./internal/buildinfo]
    owner_docs: [docs/architecture/code-map.md]
    risks: []
    focused_packages: [./internal/buildinfo]
  repository-tooling:
    priority: 50
    production_paths:
      - include: scripts/**
        exclude: [scripts/**/*_test.go, scripts/**/testdata/**]
    test_paths: [scripts/**/*_test.go, scripts/**/testdata/**]
    packages: [./scripts/...]
    owner_docs: [docs/contributing/verification.md]
    risks: []
    focused_packages: [./scripts/...]

risk_packs:
  contract:
    target: test-contract
    platforms: [all]
  concurrency:
    target: test-race
    platforms: [all]
  terminal:
    target: test-pty
    platforms: [unix]
  fuzz:
    target: test-fuzz-smoke
    platforms: [all]

change_classes:
  governance:
    priority: 200
    paths:
      - quality/**
      - scripts/iteration/**
      - .agents/**
      - .codex/**
      - .githooks/**
      - .github/workflows/**
      - Makefile
    targets: [docs-check-ci, test]
    focused_packages: [./scripts/iteration]
  dependency:
    priority: 150
    paths: [go.mod, go.sum, third_party/**, .golangci.yml, .golangci.v2.yml]
    targets: [test, build]
  documentation:
    priority: 120
    paths: ["**/*.md", docs/**]
    targets: [docs-check-ci]
  repository-metadata:
    priority: 100
    paths: [.gitignore]
    targets: [docs-check-ci]

boundaries:
  forbidden_production_edges: []
  flat_package_roots: [tools]
```

Run a policy-coverage test over `git ls-files -z`. It must classify every
tracked path except generated build output and must report all unknown paths in
one deterministic sorted diagnostic. The initial repository fixture must assert
that current tracked files produce zero unknown paths, including
`docs/migration/queue.yaml`, `docs/migration/manifest.yaml`, and the tracked
HTML/CSS migration demo assets.

- [ ] **Step 6: Run focused tests and verify green**

```bash
go test ./scripts/iteration -run '^(TestMatchPathPattern|TestLoadPolicy|TestPolicy)' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit the strict policy slice**

```bash
git add quality/iteration.yaml scripts/iteration/policy.go scripts/iteration/matcher.go scripts/iteration/policy_test.go scripts/iteration/matcher_test.go scripts/iteration/testdata
git commit -m "feat(quality): define strict iteration policy"
```

### Task 2: Resolve a canonical Git snapshot and diff digest

**Files:**

- Create: `scripts/iteration/git.go`
- Create: `scripts/iteration/git_test.go`

**Interfaces:**

- Produces: `resolveSnapshot(ctx, root, baseRef, head string, source GitSource)
  (GitSnapshot, error)`.
- `head == ""` means current `HEAD` plus tracked worktree changes; an explicit
  head means the committed tree at that object and requires a clean comparison.
- Preserves: untracked paths are never opened or hashed.

- [ ] **Step 1: Add a fake-source contract test**

Lock the interface and expected command-independent result:

```go
type GitSource interface {
	Resolve(ctx context.Context, rev string) (string, error)
	MergeBase(ctx context.Context, left, right string) (string, error)
	NameStatus(ctx context.Context, base, head string) ([]byte, error)
	BinaryDiff(ctx context.Context, base, head string) ([]byte, error)
	UntrackedCount(ctx context.Context) (int, error)
}

type GitSnapshot struct {
	BaseRef          string
	Base             string
	Head             string
	DiffDigest       string
	Changed          []GitChange
	OutsideUntracked int
}
```

The fake returns base `aaaa`, head `bbbb`, one `M\x00engine/query.go\x00`, and
binary bytes `patch`. Assert the digest equals
`sha256.Sum256([]byte("patch"))`, paths are normalized, and the fake's
untracked count is copied without reading names.

- [ ] **Step 2: Add rename, delete, and malformed NUL tests**

Use `R100\x00engine/old.go\x00engine/new.go\x00` and require both old and new
paths to be classified, with statuses `R100-from` and `R100-to`. A deletion
keeps its old path. Reject a truncated rename, absolute path, traversal, and
duplicate path/status record.

- [ ] **Step 3: Run the snapshot tests and verify red**

```bash
go test ./scripts/iteration -run '^(TestResolveSnapshot|TestParseNameStatus)' -count=1
```

Expected: FAIL because the snapshot implementation is absent.

- [ ] **Step 4: Implement the real Git source**

Use bounded `exec.CommandContext` calls with fixed argv, no shell:

```go
func (source commandGitSource) BinaryDiff(
	ctx context.Context,
	base string,
	head string,
) ([]byte, error) {
	args := []string{"diff", "--binary", "--full-index", "--no-ext-diff", base}
	if head != "" {
		args = append(args, head)
	}
	args = append(args, "--")
	return source.output(ctx, "git", args...)
}
```

For the default case resolve `HEAD`, then compute `merge-base <baseRef> HEAD`
and compare the base to the current tracked tree. For explicit `--head`, resolve
that object and compare `base..<head>`. Cap every Git output at 16 MiB and fail
closed on overflow. Reject an in-progress merge or rebase by checking Git's
resolved state paths before producing a plan.

- [ ] **Step 5: Add a real temporary-repository integration test**

Initialize a temporary repository, commit `engine/a.go`, branch, modify it,
add an unrelated untracked file, and assert:

- the base is the merge-base commit;
- only `engine/a.go` enters `Changed`;
- the untracked count is 1;
- changing untracked content does not change `DiffDigest`; and
- changing tracked content does change `DiffDigest`.

- [ ] **Step 6: Run focused tests and commit**

```bash
go test ./scripts/iteration -run '^(TestResolveSnapshot|TestParseNameStatus|TestCommandGitSource)' -count=1
git add scripts/iteration/git.go scripts/iteration/git_test.go
git commit -m "feat(quality): bind plans to tracked git diffs"
```

### Task 3: Classify paths and attach an accepted migration slice

**Files:**

- Create: `scripts/iteration/plan.go`
- Create: `scripts/iteration/plan_test.go`
- Modify: `scripts/migration_queue/main.go`
- Modify: `scripts/migration_queue/main_test.go`

**Interfaces:**

- Produces: `buildPlan(policy Policy, snapshot GitSnapshot, platform string,
  slice *SliceRef) (Plan, error)`.
- Produces: migration queue CLI `describe --slice-id <ID>` with schema
  `{"schema_version":1,"id":...,"state":...,"contract":...,"outcome":...}`.
- Consumes: the queue CLI through `SliceResolver`; it does not parse queue YAML
  inside `scripts/iteration`.

- [ ] **Step 1: Add classification tests for every accepted shape**

Create table fixtures for:

1. one documentation path;
2. one production path;
3. one test path;
4. engine plus ACP, expecting a sorted union of risks and targets;
5. `scripts/iteration/plan.go`, expecting the higher-priority governance class;
6. an unknown path, expecting a fail-closed diagnostic;
7. equal-priority module/class ownership of the same path, expecting ambiguity;
8. Unix and Windows plans, expecting `test-pty` selected on Unix and listed in
   `not_applicable` on Windows; and
9. duplicate matches inside one owner, expecting one result rather than an
   ambiguity.

Use explicit sorted slices as the oracle. Do not compare JSON snapshots whose
field ordering can conceal an unordered implementation.

- [ ] **Step 2: Implement priority resolution and union logic**

Use this owner candidate shape:

```go
type ownerCandidate struct {
	owner    string
	kind     PathKind
	priority int
	risks    []string
	docs     []string
	packages []string
	targets  []string
}

func chooseOwner(path string, candidates []ownerCandidate) (ownerCandidate, error) {
	if len(candidates) == 0 {
		return ownerCandidate{}, fmt.Errorf("unclassified path %q", path)
	}
	slices.SortFunc(candidates, func(left, right ownerCandidate) int {
		return cmp.Compare(right.priority, left.priority)
	})
	winner := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.priority != winner.priority {
			break
		}
		if candidate.owner != winner.owner || candidate.kind != winner.kind {
			return ownerCandidate{}, fmt.Errorf(
				"ambiguous path %q at priority %d: %s/%s and %s/%s",
				path, winner.priority, winner.owner, winner.kind,
				candidate.owner, candidate.kind,
			)
		}
	}
	return winner, nil
}
```

Deduplicate every union with a map, sort before returning, and never derive
ownership from imports or test names.

Build one sorted `FocusedCheck` per winning owner that has focused packages.
Module checks use `ModulePolicy.FocusedPackages`; class checks use
`ChangeClass.FocusedPackages`. Never collapse owner-to-package association into
one anonymous package list, because S2 records stable `focused/<owner>` gates.

The planner also adds stable base requirements. A documentation-only diff gets
`docs-check-ci`, `docs-check`, and `git-diff-check`. Every other classified diff
gets `fmt`, `lint`, `test`, `build`, `docs-check-ci`, `docs-check`, and
`git-diff-check`, plus the union of class targets and risk-pack targets.
`docs-check` is retained in the plan even when `.reference` is absent; S2 marks
that policy-proven environment boundary `not_applicable`. `git-diff-check` is a
built-in target implemented without a shell and therefore is exempt from the
Makefile-target existence check.

- [ ] **Step 3: Add migration-queue describe tests**

Add tests proving:

- an active `ready`, `queued`, or `blocked` slice returns one JSON object;
- a deferred ID is rejected as non-executable;
- a missing ID is rejected;
- `describe` without `--slice-id` is usage error 2; and
- existing `check`, `render`, and `print` output is unchanged.

- [ ] **Step 4: Implement the read-only queue lookup**

After the existing strict load and `validateQueue`, resolve only from
`queue.Slices`:

```go
type sliceDescription struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	State         string `json:"state"`
	Contract      string `json:"contract"`
	Outcome       string `json:"outcome"`
}
```

Encode with `json.NewEncoder(stdout)`. Do not expose gaps, blockers, or
promotion prose through the iteration evidence schema; those remain queue-owned
details reachable through `PLAN.md`.

- [ ] **Step 5: Implement `commandSliceResolver`**

Run this fixed command from the repository root:

```text
go run ./scripts/migration_queue --slice-id <ID> describe
```

Decode with `DisallowUnknownFields`, require one JSON value plus EOF, and copy
only `id`, `state`, `contract`, and `outcome` into `SliceRef`.

- [ ] **Step 6: Run focused tests and commit**

```bash
go test ./scripts/migration_queue ./scripts/iteration -run 'Describe|BuildPlan|ChooseOwner' -count=1
git add scripts/iteration/plan.go scripts/iteration/plan_test.go scripts/migration_queue/main.go scripts/migration_queue/main_test.go
git commit -m "feat(quality): classify diffs and link accepted slices"
```

### Task 4: Render stable plan and initial evidence views

**Files:**

- Create: `scripts/iteration/evidence.go`
- Create: `scripts/iteration/evidence_test.go`

**Interfaces:**

- Produces: `initialEvidence(plan Plan) Evidence`.
- Produces: `renderJSON(value any, writer io.Writer) error` and
  `renderPlanMarkdown(plan Plan, writer io.Writer) error`.
- Preserves: stable target names only; no raw argv or output.

- [ ] **Step 1: Add exact state and status tests**

For a non-empty plan with required targets `docs-check-ci`, `test-contract`,
and Windows-inapplicable `test-pty`, assert:

```go
Evidence{
	SchemaVersion: 1,
	State:         "changed",
	Gates: []GateEvidence{
		{Target: "docs-check-ci", Level: "merge", Status: GateBlocked},
		{Target: "test-contract", Level: "merge", Status: GateBlocked},
		{Target: "test-pty", Level: "merge", Status: GateNotApplicable},
	},
}
```

An empty diff produces `planned`. Reject any status outside the four constants
when decoding a persisted fixture for forward compatibility.

- [ ] **Step 2: Add privacy-marker rendering tests**

Create synthetic strings named `PROMPT_SECRET_MARKER`,
`SOURCE_SECRET_MARKER`, `TRANSCRIPT_SECRET_MARKER`, `ARGV_SECRET_MARKER`, and
`COMMAND_OUTPUT_SECRET_MARKER` only in fake source internals. Render plan and
evidence and assert none appears. The test must also assert that repository-
relative changed paths, target names, IDs, counts, and digests do appear.

- [ ] **Step 3: Implement deterministic JSON and Markdown output**

JSON uses `SetIndent("", "  ")` and one trailing newline. Markdown contains:

```markdown
# Change Plan

- Base: `<resolved commit>`
- Head: `<resolved commit>`
- Diff digest: `<sha256>`
- State: `changed`
- Outside-scope untracked count: `N`

## Changed owners

| Path | Status | Owner | Kind |
|---|---|---|---|

## Required checks

| Target | Status |
|---|---|
```

Escape pipe and newline characters even though validated repository paths
should not contain them. Do not render queue outcome prose into a hook-sized
summary; normal `change-plan` output may link the accepted contract path.

- [ ] **Step 4: Run tests and commit**

```bash
go test ./scripts/iteration -run 'Evidence|Render|Privacy' -count=1
git add scripts/iteration/evidence.go scripts/iteration/evidence_test.go
git commit -m "feat(quality): render diff-bound iteration evidence"
```

### Task 5: Expose the S1 CLI and Make wrappers

**Files:**

- Create: `scripts/iteration/main.go`
- Create: `scripts/iteration/main_test.go`
- Modify: `Makefile`

**Interfaces:**

- Produces CLI:
  `iteration [--policy path] [--base ref] [--head object] [--slice-id ID]
  [--format json|markdown] plan|evidence|policy-check`.
- Produces Make targets: `change-plan`, `change-evidence`, and
  `iteration-policy-check`.
- Preserves: `make verify` dependencies and all current target recipes.

- [ ] **Step 1: Add CLI behavior tests**

Use `run(args []string, stdout, stderr io.Writer, deps dependencies) int` and
fake dependencies. Assert:

- `plan` defaults to `origin/master` and Markdown;
- `--format json` emits the locked `Plan` schema;
- `evidence` emits blocked initial gates without executing a command;
- `--head` passes a committed-tree request to the Git source;
- `--slice-id` calls the resolver exactly once;
- `policy-check` validates policy and all tracked files but emits no plan;
- unknown command, format, or extra arg returns 2; and
- policy, Git, queue, or classification failures return 1 with one concise
  diagnostic and no partial stdout.

- [ ] **Step 2: Implement the thin command dispatcher**

Keep dependency construction in `defaultDependencies()` and domain work in the
files already created:

```go
type dependencies struct {
	git          GitSource
	slices       SliceResolver
	openRoot     func(string) (*os.Root, error)
	goos         string
	trackedFiles func(context.Context) ([]string, error)
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, defaultDependencies()))
}
```

`policy-check` walks `git ls-files -z`, validates every path through the same
classifier, and reports all failures sorted. It does not inspect file content
except the policy, owner docs, and Makefile required for strict validation.

- [ ] **Step 3: Add Make wrappers without changing `verify`**

Add variables and targets:

```make
ITERATION_BASE ?= origin/master
ITERATION_FORMAT ?= markdown
ITERATION_SLICE_ID ?=

change-plan:
	$(GO) run ./scripts/iteration --base $(ITERATION_BASE) --format $(ITERATION_FORMAT) $(if $(ITERATION_SLICE_ID),--slice-id $(ITERATION_SLICE_ID),) plan

change-evidence:
	$(GO) run ./scripts/iteration --base $(ITERATION_BASE) --format $(ITERATION_FORMAT) $(if $(ITERATION_SLICE_ID),--slice-id $(ITERATION_SLICE_ID),) evidence

iteration-policy-check:
	$(GO) run ./scripts/iteration policy-check
```

Add all three to `.PHONY`. Do not interpolate arbitrary shell fragments from a
policy field; these variables are contributor inputs and the Go command still
validates their values.

- [ ] **Step 4: Run CLI and wrapper tests**

```bash
go test ./scripts/iteration -run 'Run|PolicyCheck' -count=1
make iteration-policy-check
make change-plan ITERATION_FORMAT=json
make change-evidence ITERATION_FORMAT=json
```

Expected: all commands exit 0; evidence contains only `blocked` and
policy-proven `not_applicable` gates because S2 is not implemented.

- [ ] **Step 5: Commit the public S1 interface**

```bash
git add Makefile scripts/iteration/main.go scripts/iteration/main_test.go
git commit -m "feat(quality): expose change planning commands"
```

### Task 6: Wire portable governance checks and close S1

**Files:**

- Modify: `Makefile`
- Modify: `docs/contributing/verification.md`
- Modify: `docs/superpowers/plans/README.md`

**Interfaces:**

- `docs-check-ci` additionally consumes `iteration-policy-check`.
- `docs-check` keeps its current reference-dependent manifest behavior and also
  runs the portable policy check.
- Documentation explicitly says a plan is risk selection, not passing proof.

- [ ] **Step 1: Add the policy check to both docs targets**

Append `$(GO) run ./scripts/iteration policy-check` after the existing docs,
queue, and manifest checks in `docs-check` and `docs-check-ci`. Do not replace
`migration_manifest.go check` with `check-ledger`; local reference availability
remains the only difference between those targets.

- [ ] **Step 2: Document the contributor flow**

In `docs/contributing/verification.md`, add one concise section with this exact
decision boundary:

```text
make change-plan selects owners and required checks for the current tracked
diff. make change-evidence reports their current status. Neither command runs a
gate in S1, and blocked is not success. The migration queue orders accepted
product slices; the quality plan selects evidence for the diff. They are linked
only by an optional accepted slice_id.
```

Link the design and this plan. Use stable symbols and repository paths, not
line-number anchors.

- [ ] **Step 3: Run self-review scans**

```bash
rg -n 'TBD|TODO|FIXME|implement later|some test|add tests' docs/superpowers/plans/2026-08-08-iteration-quality-s1-policy-planner.md | rg -v 'rg -n'
rg -n 'GatePass|GateFail|GateBlocked|GateNotApplicable|diff_digest|slice_id' docs/superpowers/plans/2026-08-08-iteration-quality-s*.md
git diff --check
```

Expected: the incomplete-work scan has no actionable hit; shared type and field
names are consistent.

- [ ] **Step 4: Run focused and final repository gates**

```bash
go test ./scripts/iteration ./scripts/migration_queue -count=1
make docs-check
make fmt
make lint
make test
make build
git diff --check
```

Expected: every command passes. If `.reference` is intentionally unavailable,
run and report `make docs-check-ci` separately; do not call the
reference-dependent result a pass.

- [ ] **Step 5: Commit S1 documentation and final wiring**

```bash
git add Makefile docs/contributing/verification.md docs/superpowers/plans/README.md
git commit -m "docs(quality): define S1 planning workflow"
```

- [ ] **Step 6: Prepare the S1 PR**

The PR body must state: user problem, `project-native` decision, no product
runtime change, policy/schema compatibility, fail-closed unknown-path behavior,
rollback by removing policy/tool/wrappers, focused test results, all four local
Make gates, docs result, and the fact that S1 produces no passing verification
evidence.
