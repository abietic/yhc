package main

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestBuildPlanClassifiesDocumentation(t *testing.T) {
	plan, err := buildPlan(testPlanningPolicy(), testSnapshot(
		GitChange{Status: "M", Path: "docs/contributing/verification.md"},
	), "darwin", nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPlanSlices(t, plan, planExpectation{
		changed: []ChangedPath{{
			Path: "docs/contributing/verification.md", Status: "M", Owner: "documentation", Kind: PathClass,
		}},
		classes: []string{"documentation"},
		targets: []string{"docs-check", "docs-check-ci", "git-diff-check"},
	})
}

func TestBuildPlanRequiresBoundaryCheckOnlyForNonDocumentation(t *testing.T) {
	documentation, err := buildPlan(testPlanningPolicy(), testSnapshot(
		GitChange{Status: "M", Path: "docs/contributing/verification.md"},
	), "linux", nil)
	if err != nil {
		t.Fatal(err)
	}
	if has(documentation.RequiredTargets, "check-boundaries") {
		t.Fatalf("documentation targets = %#v", documentation.RequiredTargets)
	}
	production, err := buildPlan(testPlanningPolicy(), testSnapshot(
		GitChange{Status: "M", Path: "engine/query.go"},
	), "linux", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !has(production.RequiredTargets, "check-boundaries") {
		t.Fatalf("production targets = %#v", production.RequiredTargets)
	}
}

func TestBuildPlanClassifiesProductionAndTestPaths(t *testing.T) {
	tests := []struct {
		name string
		path string
		kind PathKind
	}{
		{name: "production", path: "engine/query.go", kind: PathProduction},
		{name: "test", path: "engine/query_test.go", kind: PathTest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := buildPlan(testPlanningPolicy(), testSnapshot(
				GitChange{Status: "M", Path: test.path},
			), "linux", nil)
			if err != nil {
				t.Fatal(err)
			}
			assertPlanSlices(t, plan, planExpectation{
				changed: []ChangedPath{{Path: test.path, Status: "M", Owner: "engine-runtime", Kind: test.kind}},
				modules: []string{"engine-runtime"},
				risks:   []string{"concurrency", "contract"},
				docs:    []string{"docs/architecture/runtime/query-engine.md"},
				focused: []FocusedCheck{{Owner: "engine-runtime", Packages: []string{"./engine/..."}}},
				targets: []string{"build", "check-boundaries", "docs-check", "docs-check-ci", "fmt", "git-diff-check", "lint", "test", "test-contract", "test-race"},
			})
		})
	}
}

func TestBuildPlanUnionsOwnersRisksAndTargets(t *testing.T) {
	plan, err := buildPlan(testPlanningPolicy(), testSnapshot(
		GitChange{Status: "M", Path: "engine/query.go"},
		GitChange{Status: "M", Path: "server/acp/agent.go"},
	), "darwin", nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPlanSlices(t, plan, planExpectation{
		changed: []ChangedPath{
			{Path: "engine/query.go", Status: "M", Owner: "engine-runtime", Kind: PathProduction},
			{Path: "server/acp/agent.go", Status: "M", Owner: "acp-adapter", Kind: PathProduction},
		},
		modules: []string{"acp-adapter", "engine-runtime"},
		risks:   []string{"concurrency", "contract"},
		docs: []string{
			"docs/architecture/platform/acp-adapter.md",
			"docs/architecture/runtime/query-engine.md",
		},
		focused: []FocusedCheck{
			{Owner: "acp-adapter", Packages: []string{"./server/acp"}},
			{Owner: "engine-runtime", Packages: []string{"./engine/..."}},
		},
		targets: []string{"build", "check-boundaries", "docs-check", "docs-check-ci", "fmt", "git-diff-check", "lint", "test", "test-contract", "test-race"},
	})
}

func TestBuildPlanHigherPriorityClassWins(t *testing.T) {
	plan, err := buildPlan(testPlanningPolicy(), testSnapshot(
		GitChange{Status: "M", Path: "scripts/iteration/plan.go"},
	), "darwin", nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPlanSlices(t, plan, planExpectation{
		changed: []ChangedPath{{
			Path: "scripts/iteration/plan.go", Status: "M", Owner: "governance", Kind: PathClass,
		}},
		classes: []string{"governance"},
		focused: []FocusedCheck{{Owner: "governance", Packages: []string{"./scripts/iteration"}}},
		targets: []string{"build", "check-boundaries", "docs-check", "docs-check-ci", "fmt", "git-diff-check", "lint", "test"},
	})
}

func TestBuildPlanFailsClosed(t *testing.T) {
	_, err := buildPlan(testPlanningPolicy(), testSnapshot(
		GitChange{Status: "A", Path: "unknown/file.xyz"},
	), "linux", nil)
	if err == nil || !strings.Contains(err.Error(), `unclassified path "unknown/file.xyz"`) {
		t.Fatalf("buildPlan() error = %v", err)
	}
}

func TestBuildPlanRejectsEqualPriorityOwners(t *testing.T) {
	policy := testPlanningPolicy()
	policy.Modules["other-engine"] = ModulePolicy{
		Priority:        100,
		ProductionPaths: []PathRule{{Include: "engine/**"}},
		Packages:        []string{"./engine/..."},
		OwnerDocs:       []string{"docs/architecture/code-map.md"},
		FocusedPackages: []string{"./engine/..."},
	}
	_, err := buildPlan(policy, testSnapshot(
		GitChange{Status: "M", Path: "engine/query.go"},
	), "linux", nil)
	if err == nil || !strings.Contains(err.Error(), "ambiguous path") {
		t.Fatalf("buildPlan() error = %v", err)
	}
}

func TestBuildPlanMarksPlatformExclusionNotApplicable(t *testing.T) {
	plan, err := buildPlan(testPlanningPolicy(), testSnapshot(
		GitChange{Status: "M", Path: "cmd/yhc/main.go"},
	), "windows", nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPlanSlices(t, plan, planExpectation{
		changed: []ChangedPath{{
			Path: "cmd/yhc/main.go", Status: "M", Owner: "cli-entrypoint", Kind: PathProduction,
		}},
		modules:       []string{"cli-entrypoint"},
		risks:         []string{"contract", "terminal"},
		docs:          []string{"docs/architecture/platform/entrypoints-and-transports.md"},
		focused:       []FocusedCheck{{Owner: "cli-entrypoint", Packages: []string{"./cmd/yhc/..."}}},
		targets:       []string{"build", "check-boundaries", "docs-check", "docs-check-ci", "fmt", "git-diff-check", "lint", "test", "test-contract", "test-pty"},
		notApplicable: []string{"test-pty"},
	})
}

func TestRepositoryPolicyClassifiesLegacyCLIDeletion(t *testing.T) {
	root := openPolicyRoot(t, "../..")
	policy, err := loadPolicy(root, "quality/iteration.yaml")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := buildPlan(policy, testSnapshot(
		GitChange{Status: "D", Path: "cmd/eino-agent/main.go"},
	), "linux", nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPlanSlices(t, plan, planExpectation{
		changed: []ChangedPath{{
			Path: "cmd/eino-agent/main.go", Status: "D", Owner: "cli-entrypoint", Kind: PathProduction,
		}},
		modules: []string{"cli-entrypoint"},
		risks:   []string{"contract", "e2e", "terminal"},
		docs:    []string{"docs/architecture/platform/entrypoints-and-transports.md"},
		focused: []FocusedCheck{{Owner: "cli-entrypoint", Packages: []string{"./cmd/yhc/..."}}},
		targets: []string{"build", "check-boundaries", "docs-check", "docs-check-ci", "fmt", "git-diff-check", "lint", "test", "test-contract", "test-e2e", "test-pty"},
	})
}

func TestRepositoryPolicyClassifiesDesktopOwners(t *testing.T) {
	root := openPolicyRoot(t, "../..")
	policy, err := loadPolicy(root, "quality/iteration.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		path    string
		owner   string
		kind    PathKind
		focused []FocusedCheck
		risks   []string
	}{
		{
			name:    "app server",
			path:    "server/appserver/server.go",
			owner:   "app-server-adapter",
			kind:    PathProduction,
			focused: []FocusedCheck{{Owner: "app-server-adapter", Packages: []string{"./server/appserver"}}},
			risks:   []string{"concurrency", "contract", "desktop", "e2e"},
		},
		{
			name:    "web UI",
			path:    "internal/webui/assets/app.mjs",
			owner:   "webui-adapter",
			kind:    PathProduction,
			focused: []FocusedCheck{{Owner: "webui-adapter", Packages: []string{"./internal/webui"}}},
			risks:   []string{"desktop"},
		},
		{
			name:    "desktop workbench test",
			path:    "desktop/test/state.test.mjs",
			owner:   "desktop-workbench",
			kind:    PathTest,
			focused: []FocusedCheck{{Owner: "desktop-workbench", Packages: []string{"./cmd/yhc/cmd"}}},
			risks:   []string{"desktop"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := buildPlan(policy, testSnapshot(GitChange{Status: "M", Path: tc.path}), "linux", nil)
			if err != nil {
				t.Fatal(err)
			}
			assertPlanSlices(t, plan, planExpectation{
				changed: []ChangedPath{{Path: tc.path, Status: "M", Owner: tc.owner, Kind: tc.kind}},
				modules: []string{tc.owner},
				risks:   tc.risks,
				docs:    []string{"docs/superpowers/specs/2026-08-13-yhc-desktop-workbench-forward-port-design.md"},
				focused: tc.focused,
				targets: desktopTargets(tc.risks),
			})
		})
	}
}

func desktopTargets(risks []string) []string {
	targets := []string{"build", "check-boundaries", "desktop-check", "docs-check", "docs-check-ci", "fmt", "git-diff-check", "lint", "test"}
	for _, risk := range risks {
		switch risk {
		case "contract":
			targets = append(targets, "test-contract")
		case "concurrency":
			targets = append(targets, "test-race")
		case "e2e":
			targets = append(targets, "test-e2e")
		}
	}
	sort.Strings(targets)
	return targets
}

func TestBuildPlanDeduplicatesMatchesWithinOwner(t *testing.T) {
	policy := testPlanningPolicy()
	module := policy.Modules["engine-runtime"]
	module.ProductionPaths = append(module.ProductionPaths, PathRule{Include: "engine/*.go"})
	policy.Modules["engine-runtime"] = module
	plan, err := buildPlan(policy, testSnapshot(
		GitChange{Status: "M", Path: "engine/query.go"},
	), "linux", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changed) != 1 || len(plan.Modules) != 1 || len(plan.FocusedChecks) != 1 {
		t.Fatalf("duplicate owner results: %#v", plan)
	}
}

func TestChooseOwnerDeduplicatesOneOwnerAndRejectsTie(t *testing.T) {
	candidate := ownerCandidate{owner: "engine-runtime", kind: PathProduction, priority: 100}
	winner, err := chooseOwner("engine/query.go", []ownerCandidate{candidate, candidate})
	if err != nil || winner.owner != candidate.owner {
		t.Fatalf("chooseOwner duplicate = %#v, %v", winner, err)
	}
	_, err = chooseOwner("engine/query.go", []ownerCandidate{
		candidate,
		{owner: "other-engine", kind: PathProduction, priority: 100},
	})
	if err == nil || !strings.Contains(err.Error(), "ambiguous path") {
		t.Fatalf("chooseOwner tie error = %v", err)
	}
}

func TestDecodeSliceRefStrictSchema(t *testing.T) {
	valid := `{"schema_version":1,"id":"P1.0","state":"ready","contract":"plans/p1.md","outcome":"Ship P1."}`
	got, err := decodeSliceRef(strings.NewReader(valid), "P1.0")
	if err != nil {
		t.Fatal(err)
	}
	want := &SliceRef{ID: "P1.0", State: "ready", Contract: "plans/p1.md", Outcome: "Ship P1."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decodeSliceRef() = %#v, want %#v", got, want)
	}

	for _, test := range []struct {
		name  string
		input string
		id    string
	}{
		{"unknown field", strings.TrimSuffix(valid, "}") + `,"gaps":[]}`, "P1.0"},
		{"second value", valid + `{}`, "P1.0"},
		{"wrong schema", strings.Replace(valid, `"schema_version":1`, `"schema_version":2`, 1), "P1.0"},
		{"wrong id", valid, "P2.0"},
		{"deferred state", strings.Replace(valid, `"state":"ready"`, `"state":"deferred"`, 1), "P1.0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeSliceRef(strings.NewReader(test.input), test.id); err == nil {
				t.Fatal("decodeSliceRef accepted invalid input")
			}
		})
	}
}

type planExpectation struct {
	changed       []ChangedPath
	modules       []string
	classes       []string
	risks         []string
	docs          []string
	focused       []FocusedCheck
	targets       []string
	notApplicable []string
}

func assertPlanSlices(t *testing.T, plan Plan, want planExpectation) {
	t.Helper()
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"changed", plan.Changed, want.changed},
		{"modules", plan.Modules, want.modules},
		{"change classes", plan.ChangeClasses, want.classes},
		{"risks", plan.Risks, want.risks},
		{"owner docs", plan.OwnerDocs, want.docs},
		{"focused checks", plan.FocusedChecks, want.focused},
		{"required targets", plan.RequiredTargets, want.targets},
		{"not applicable", plan.NotApplicable, want.notApplicable},
	}
	for _, check := range checks {
		if !reflect.DeepEqual(check.got, check.want) {
			t.Errorf("%s = %#v, want %#v", check.name, check.got, check.want)
		}
	}
}

func testSnapshot(changes ...GitChange) GitSnapshot {
	return GitSnapshot{
		BaseRef:          "origin/master",
		Base:             strings.Repeat("a", 40),
		Head:             strings.Repeat("b", 40),
		DiffDigest:       strings.Repeat("c", 64),
		Changed:          changes,
		OutsideUntracked: 2,
	}
}

func testPlanningPolicy() Policy {
	return Policy{
		Version:    1,
		Repository: "github.com/abietic/yhc",
		Modules: map[string]ModulePolicy{
			"engine-runtime": {
				Priority:        100,
				ProductionPaths: []PathRule{{Include: "engine/**", Exclude: []string{"engine/**/*_test.go"}}},
				TestPaths:       []string{"engine/**/*_test.go"},
				Packages:        []string{"./engine/..."},
				OwnerDocs:       []string{"docs/architecture/runtime/query-engine.md"},
				Risks:           []string{"contract", "concurrency"},
				FocusedPackages: []string{"./engine/..."},
			},
			"acp-adapter": {
				Priority:        100,
				ProductionPaths: []PathRule{{Include: "server/acp/**"}},
				Packages:        []string{"./server/acp"},
				OwnerDocs:       []string{"docs/architecture/platform/acp-adapter.md"},
				Risks:           []string{"contract", "concurrency"},
				FocusedPackages: []string{"./server/acp"},
			},
			"cli-entrypoint": {
				Priority:        100,
				ProductionPaths: []PathRule{{Include: "cmd/yhc/**"}},
				Packages:        []string{"./cmd/yhc/..."},
				OwnerDocs:       []string{"docs/architecture/platform/entrypoints-and-transports.md"},
				Risks:           []string{"contract", "terminal"},
				FocusedPackages: []string{"./cmd/yhc/..."},
			},
			"repository-tooling": {
				Priority:        50,
				ProductionPaths: []PathRule{{Include: "scripts/**"}},
				Packages:        []string{"./scripts/..."},
				OwnerDocs:       []string{"docs/contributing/verification.md"},
				FocusedPackages: []string{"./scripts/..."},
			},
		},
		RiskPacks: map[string]RiskPack{
			"contract":    {Target: "test-contract", Platforms: []string{"all"}},
			"concurrency": {Target: "test-race", Platforms: []string{"all"}},
			"terminal":    {Target: "test-pty", Platforms: []string{"unix"}},
		},
		ChangeClasses: map[string]ChangeClass{
			"governance": {
				Priority:        200,
				Paths:           []string{"scripts/iteration/**"},
				Targets:         []string{"test"},
				FocusedPackages: []string{"./scripts/iteration"},
			},
			"documentation": {
				Priority: 120,
				Paths:    []string{"docs/**"},
				Targets:  []string{"docs-check-ci"},
			},
		},
	}
}
