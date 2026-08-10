package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeSliceResolver struct {
	result *SliceRef
	err    error
	calls  []string
}

func (resolver *fakeSliceResolver) Resolve(_ context.Context, id string) (*SliceRef, error) {
	resolver.calls = append(resolver.calls, id)
	return cloneSliceRef(resolver.result), resolver.err
}

func TestRunPlanDefaultsToOriginMasterAndMarkdown(t *testing.T) {
	deps, git, _ := testRunDependencies(t)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"plan"}, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("run(plan) = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "# Change Plan") {
		t.Fatalf("plan output = %q", stdout.String())
	}
	if git.mergeLeft != "origin/master" || git.resolveCalls[0] != "HEAD" {
		t.Fatalf("default git inputs = merge left %q, resolve calls %v", git.mergeLeft, git.resolveCalls)
	}
}

func TestRunPlanJSONUsesLockedSchema(t *testing.T) {
	deps, _, _ := testRunDependencies(t)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--format", "json", "plan"}, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("run(plan json) = %d, stderr = %q", code, stderr.String())
	}
	var plan Plan
	if err := json.Unmarshal(stdout.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.SchemaVersion != 1 || plan.PolicyVersion != 1 || len(plan.Changed) != 1 {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestRunMetricsUsesExplicitReadOnlyRoot(t *testing.T) {
	deps, _, _ := testRunDependencies(t)
	plan := verificationPlan("a")
	writeMetricsArtifact(t, deps.root, plan, Evidence{SchemaVersion: 1, Plan: plan, State: "changed", Gates: []GateEvidence{{
		Target: "test-contract", Level: string(VerifyFocused), Status: GatePass, DurationMillis: 1,
	}}})
	var stdout, stderr bytes.Buffer
	if code := run([]string{"metrics", "--root", "build/iteration", "--format", "json"}, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("run(metrics) = %d, stderr = %q", code, stderr.String())
	}
	var report MetricsReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil || report.State != "insufficient_samples" {
		t.Fatalf("metrics report = %#v, err = %v", report, err)
	}
}

func TestRunMetricsRootErrorsAreGenericAndPrivate(t *testing.T) {
	const marker = "METRICS_ROOT_SECRET_MARKER"
	for _, test := range []struct {
		name  string
		setup func(*testing.T, *dependencies)
		args  []string
	}{
		{"invalid root", func(_ *testing.T, _ *dependencies) {}, []string{"metrics", "--root", "../" + marker}},
		{"symlink root", func(t *testing.T, deps *dependencies) {
			if err := os.MkdirAll(filepath.Join(deps.root, "build"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("iteration", filepath.Join(deps.root, "build", marker)); err != nil {
				t.Fatal(err)
			}
		}, []string{"metrics", "--root", "build/" + marker}},
		{"unavailable root", func(_ *testing.T, deps *dependencies) { deps.root = filepath.Join(deps.root, marker) }, []string{"metrics"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			deps, _, _ := testRunDependencies(t)
			test.setup(t, &deps)
			var stdout, stderr bytes.Buffer
			if code := run(test.args, &stdout, &stderr, deps); code != 1 || stdout.Len() != 0 {
				t.Fatalf("run(%v) = %d, stdout = %q, stderr = %q", test.args, code, stdout.String(), stderr.String())
			}
			if strings.Contains(stderr.String(), marker) || strings.Count(strings.TrimSpace(stderr.String()), "\n") != 0 {
				t.Fatalf("private metrics diagnostic = %q", stderr.String())
			}
		})
	}
}

func TestRunUsageListsMetrics(t *testing.T) {
	deps, _, _ := testRunDependencies(t)
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr, deps); code != 2 || !strings.Contains(stderr.String(), "metrics") {
		t.Fatalf("usage code = %d, stderr = %q", code, stderr.String())
	}
}

func TestRunEvidenceLoadsPersistedStateWithoutExecutingGates(t *testing.T) {
	deps, git, _ := testRunDependencies(t)
	git.nameStatus = []byte("M\x00scripts/tool.go\x00")
	runner := &recordingTargetRunner{}
	deps.runnerFactory = func(Plan) TargetRunner { return runner }
	var verifyOutput, verifyError bytes.Buffer
	if code := run([]string{"--format", "json", "verify", "--level", "focused"}, &verifyOutput, &verifyError, deps); code != 0 {
		t.Fatalf("run(verify focused) = %d, stderr = %q", code, verifyError.String())
	}
	executed := len(runner.calls)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--format", "json", "evidence"}, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("run(evidence) = %d, stderr = %q", code, stderr.String())
	}
	evidence, err := decodeEvidence(bytes.NewReader(stdout.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if evidence.State != "focused_verified" || len(evidence.Gates) == 0 {
		t.Fatalf("evidence = %#v", evidence)
	}
	for _, gate := range evidence.Gates {
		if gate.Status != GatePass {
			t.Fatalf("persisted evidence gate = %#v", gate)
		}
	}
	if len(runner.calls) != executed {
		t.Fatalf("evidence executed gates: before %d, after %d", executed, len(runner.calls))
	}
}

func TestRunEvidenceRequireReadyAcceptsOnlyExactCommittedEvidence(t *testing.T) {
	deps, git, _ := testRunDependencies(t)
	git.resolved["feature"] = strings.Repeat("d", 40)
	plan := committedTestPlan(t, deps, "origin/master", "feature")
	store := newFileEvidenceStore(deps.root)
	seedReadyEvidence(t, store, plan)
	deps.storeFactory = func(string) EvidenceStore { return store }

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--head", "feature", "evidence", "--require-ready"}, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("require-ready exact evidence = %d, stderr = %q", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("require-ready success output = %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"--base", "other/master", "--head", "feature", "evidence", "--require-ready"}, &stdout, &stderr, deps); code != 1 {
		t.Fatalf("require-ready stale base = %d, stderr = %q", code, stderr.String())
	}

	git.resolved["feature"] = strings.Repeat("e", 40)
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"--head", "feature", "evidence", "--require-ready"}, &stdout, &stderr, deps); code != 1 {
		t.Fatalf("require-ready stale head = %d, stderr = %q", code, stderr.String())
	}
	git.resolved["feature"] = strings.Repeat("d", 40)
	git.binaryDiff = []byte("different patch")
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"--head", "feature", "evidence", "--require-ready"}, &stdout, &stderr, deps); code != 1 {
		t.Fatalf("require-ready stale digest = %d, stderr = %q", code, stderr.String())
	}
}

func TestRunEvidenceRequireReadyRejectsMissingBlockedMalformedAndDirty(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		deps, git, _ := testRunDependencies(t)
		git.resolved["feature"] = strings.Repeat("d", 40)
		assertRunFailure(t, []string{"--head", "feature", "evidence", "--require-ready"}, deps)
	})

	t.Run("blocked", func(t *testing.T) {
		deps, git, _ := testRunDependencies(t)
		git.resolved["feature"] = strings.Repeat("d", 40)
		plan := committedTestPlan(t, deps, "origin/master", "feature")
		store := newFileEvidenceStore(deps.root)
		seedFocusedEvidence(t, store, plan)
		deps.storeFactory = func(string) EvidenceStore { return store }
		assertRunFailure(t, []string{"--head", "feature", "evidence", "--require-ready"}, deps)
	})

	t.Run("malformed", func(t *testing.T) {
		deps, git, _ := testRunDependencies(t)
		git.resolved["feature"] = strings.Repeat("d", 40)
		plan := committedTestPlan(t, deps, "origin/master", "feature")
		store := newFileEvidenceStore(deps.root)
		seedReadyEvidence(t, store, plan)
		dataPath := filepath.Join(deps.root, "build", "iteration", plan.DiffDigest, "evidence.json")
		data, err := os.ReadFile(dataPath)
		if err != nil {
			t.Fatal(err)
		}
		data = bytes.Replace(data, []byte(`"state":"evidence_ready"`), []byte(`"state":"malformed"`), 1)
		if err := os.WriteFile(dataPath, data, 0o600); err != nil {
			t.Fatal(err)
		}
		deps.storeFactory = func(string) EvidenceStore { return store }
		assertRunFailure(t, []string{"--head", "feature", "evidence", "--require-ready"}, deps)
	})

	t.Run("dirty", func(t *testing.T) {
		deps, git, _ := testRunDependencies(t)
		git.resolved["feature"] = strings.Repeat("d", 40)
		git.trackedClean = false
		assertRunFailure(t, []string{"--head", "feature", "evidence", "--require-ready"}, deps)
	})
}

func TestRunEvidenceRequireReadyRequiresExplicitHead(t *testing.T) {
	deps, _, _ := testRunDependencies(t)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"evidence", "--require-ready"}, &stdout, &stderr, deps); code != 2 {
		t.Fatalf("require-ready without head = %d, stderr = %q", code, stderr.String())
	}
}

func committedTestPlan(t *testing.T, deps dependencies, baseRef, head string) Plan {
	t.Helper()
	repository, err := deps.openRoot(deps.root)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	policy, err := loadPolicy(repository, "quality/iteration.yaml")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := resolveSnapshot(context.Background(), deps.root, baseRef, head, deps.git)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := buildPlan(policy, snapshot, deps.goos, nil)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func seedReadyEvidence(t *testing.T, store EvidenceStore, plan Plan) {
	t.Helper()
	seedFocusedEvidence(t, store, plan)
	for _, target := range expectedTargets(plan, VerifyMerge) {
		if _, err := store.Record(plan, GateEvidence{Target: target, Level: string(VerifyMerge), Status: GatePass, DurationMillis: 1}); err != nil {
			t.Fatalf("record merge target %q: %v", target, err)
		}
	}
	evidence, err := store.Load(plan)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.State != "evidence_ready" {
		t.Fatalf("seeded evidence state = %q", evidence.State)
	}
}

func TestRunExplicitHeadUsesCommittedComparison(t *testing.T) {
	deps, git, _ := testRunDependencies(t)
	git.resolved["feature"] = strings.Repeat("d", 40)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--head", "feature", "plan"}, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("run(--head) = %d, stderr = %q", code, stderr.String())
	}
	if git.nameStatusHead != strings.Repeat("d", 40) || git.untrackedCalls != 0 {
		t.Fatalf("explicit head comparison = %q, untracked calls %d", git.nameStatusHead, git.untrackedCalls)
	}
}

func TestRunSliceIDResolvesExactlyOnce(t *testing.T) {
	deps, _, slices := testRunDependencies(t)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--slice-id", "P1.0", "--format", "json", "plan"}, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("run(--slice-id) = %d, stderr = %q", code, stderr.String())
	}
	if !reflectStrings(slices.calls, []string{"P1.0"}) {
		t.Fatalf("slice calls = %v", slices.calls)
	}
}

func TestRunPolicyCheckUsesInjectedTrackedFiles(t *testing.T) {
	deps, git, _ := testRunDependencies(t)
	deps.trackedFiles = func(context.Context) ([]string, error) {
		return []string{"Makefile", "scripts/tool.go"}, nil
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"policy-check"}, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("run(policy-check) = %d, stderr = %q", code, stderr.String())
	}
	if stdout.Len() != 0 || len(git.resolveCalls) != 0 {
		t.Fatalf("policy-check output = %q, git calls = %v", stdout.String(), git.resolveCalls)
	}

	deps.trackedFiles = func(context.Context) ([]string, error) {
		return []string{"Makefile", "unknown/file.xyz"}, nil
	}
	if code := run([]string{"policy-check"}, &stdout, &stderr, deps); code != 1 ||
		!strings.Contains(stderr.String(), "unknown/file.xyz") {
		t.Fatalf("run(policy-check unknown) = %d, stderr = %q", code, stderr.String())
	}
}

func TestRunBoundariesReportsBeforeFailingAndAllShowsBaseline(t *testing.T) {
	deps, git, _ := testRunDependencies(t)
	policy := strings.Replace(
		validPolicyYAML,
		"forbidden_production_edges: []",
		"forbidden_production_edges:\n    - from: [tooling]\n      to: [tooling]",
		1,
	)
	if err := os.WriteFile(filepath.Join(deps.root, "quality", "iteration.yaml"), []byte(policy), 0o600); err != nil {
		t.Fatal(err)
	}
	data := []byte(`package iteration
import _ "example/repository/scripts/sub"
`)
	base := git.mergeBase
	head := git.resolved["HEAD"]
	git.nameStatus = []byte("A\x00scripts/new.go\x00")
	trees := memoryTreeSource{
		base: {},
		head: {},
		"":   {"scripts/new.go": {Data: data}},
	}
	deps.tree = trees

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--format", "json", "boundaries"}, &stdout, &stderr, deps); code != 1 {
		t.Fatalf("run(boundaries) = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
	}
	var report BoundaryReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.ForbiddenNewEdges) != 1 || stderr.Len() != 0 {
		t.Fatalf("boundary report = %#v, stderr = %q", report, stderr.String())
	}
	if strings.Contains(stdout.String(), `"new_test_edges": null`) ||
		strings.Contains(stdout.String(), `"new_flat_package_violations": null`) {
		t.Fatalf("boundary report used null collections: %q", stdout.String())
	}

	trees[base] = trees[""]
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"boundaries", "--all", "--format", "json"}, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("run(boundaries --all) = %d, stderr = %q", code, stderr.String())
	}
	var diagnostic map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &diagnostic); err != nil {
		t.Fatal(err)
	}
	current, ok := diagnostic["current_production_edges"].([]any)
	if !ok || len(current) != 1 {
		t.Fatalf("boundary diagnostics = %#v", diagnostic)
	}
}

func TestRunDeepRendersFirstFailureAndReturnsOne(t *testing.T) {
	deps, _, _ := testRunDependencies(t)
	runner := &recordingTargetRunner{results: map[string]RunResult{
		"check-boundaries": {Status: GateBlocked, DurationMillis: 1},
	}}
	store := newMemoryDeepIntakeStore()
	deps.runnerFactory = func(Plan) TargetRunner { return runner }
	deps.deepStoreFactory = func(string) DeepIntakeStore { return store }

	var stdout, stderr bytes.Buffer
	if code := run([]string{"deep", "--format", "json"}, &stdout, &stderr, deps); code != 1 {
		t.Fatalf("run(deep) = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
	}
	var result DeepResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Intake == nil || result.Intake.Target != "check-boundaries" ||
		result.Intake.Status != GateBlocked || stderr.Len() != 0 {
		t.Fatalf("deep result = %#v, stderr = %q", result, stderr.String())
	}
}

func TestRunUsageErrorsReturnTwo(t *testing.T) {
	for _, args := range [][]string{
		{"unknown"},
		{"--format", "yaml", "plan"},
		{"plan", "extra"},
		{"policy-check", "extra"},
		{"verify"},
		{"verify", "--level", "deep"},
		{"hook"},
		{"hook", "SessionStart"},
		{"hook", "unknown"},
		{"boundaries", "--unknown"},
		{"deep", "--unknown"},
	} {
		deps, _, _ := testRunDependencies(t)
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr, deps); code != 2 {
			t.Errorf("run(%v) = %d, stderr = %q", args, code, stderr.String())
		}
		if stdout.Len() != 0 {
			t.Errorf("run(%v) wrote partial stdout %q", args, stdout.String())
		}
	}
}

func TestRunHookGrammarDelegatesToBoundedHandler(t *testing.T) {
	deps, _, _ := testRunDependencies(t)
	store := &recordingHookStore{}
	deps.hookInput = strings.NewReader(`{"session_id":"cli-session","cwd":"` + deps.root + `","hook_event_name":"SessionStart"}`)
	deps.hookStoreFactory = func(string) HookStateStore { return store }
	deps.branchName = func(context.Context, string) (string, error) { return "codex/feat/hook-cli", nil }

	var stdout, stderr bytes.Buffer
	if code := run([]string{"hook", "session-start"}, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("run(hook session-start) = %d, stderr = %q", code, stderr.String())
	}
	state := store.state("cli-session")
	if state.Branch != "codex/feat/hook-cli" || !state.Open {
		t.Fatalf("hook state = %#v", state)
	}
}

func TestRunHookRejectsMismatchedEventWithoutReflectingInput(t *testing.T) {
	deps, _, _ := testRunDependencies(t)
	deps.hookInput = strings.NewReader(`{"session_id":"session","cwd":"` + deps.root + `","hook_event_name":"Stop","prompt":"PROMPT_SECRET_MARKER"}`)
	deps.hookStoreFactory = func(root string) HookStateStore {
		return newFileHookStateStore(root)
	}
	deps.branchName = func(context.Context, string) (string, error) { return "feature", nil }

	var stdout, stderr bytes.Buffer
	if code := run([]string{"hook", "session-start"}, &stdout, &stderr, deps); code != 1 {
		t.Fatalf("run(hook mismatched) = %d, stderr = %q", code, stderr.String())
	}
	if stdout.Len() != 0 || strings.Contains(stderr.String(), "PROMPT_SECRET_MARKER") {
		t.Fatalf("run(hook mismatched) stdout = %q, stderr = %q", stdout.String(), stderr.String())
	}
}

func TestRunVerifyFocusedPersistsAndRendersEvidence(t *testing.T) {
	deps, git, _ := testRunDependencies(t)
	git.nameStatus = []byte("M\x00scripts/tool.go\x00")
	runner := &recordingTargetRunner{}
	deps.runnerFactory = func(Plan) TargetRunner { return runner }
	deps.storeFactory = func(root string) EvidenceStore { return newFileEvidenceStore(root) }

	var stdout, stderr bytes.Buffer
	code := run([]string{"--format", "json", "verify", "--level", "focused"}, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("run(verify focused) = %d, stderr = %q", code, stderr.String())
	}
	evidence, err := decodeEvidence(bytes.NewReader(stdout.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if evidence.State != "focused_verified" {
		t.Fatalf("evidence state = %q, want focused_verified", evidence.State)
	}
	if !reflectStrings(runner.calls, []string{"focused/tooling"}) {
		t.Fatalf("runner calls = %v", runner.calls)
	}
}

func TestRunVerifyFailureRendersEvidenceAndReturnsOne(t *testing.T) {
	deps, git, _ := testRunDependencies(t)
	git.nameStatus = []byte("M\x00scripts/tool.go\x00")
	runner := &recordingTargetRunner{results: map[string]RunResult{
		"focused/tooling": {Status: GateFail, DurationMillis: 1},
	}}
	deps.runnerFactory = func(Plan) TargetRunner { return runner }
	deps.storeFactory = func(root string) EvidenceStore { return newFileEvidenceStore(root) }

	var stdout, stderr bytes.Buffer
	code := run([]string{"--format", "json", "verify", "--level", "focused"}, &stdout, &stderr, deps)
	if code != 1 || stderr.Len() != 0 {
		t.Fatalf("run(verify focused failure) = %d, stderr = %q", code, stderr.String())
	}
	evidence, err := decodeEvidence(bytes.NewReader(stdout.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if evidence.State == "focused_verified" || len(evidence.Gates) != 1 ||
		evidence.Gates[0].Status != GateFail {
		t.Fatalf("failed evidence = %#v", evidence)
	}
}

func TestVerificationExitCodeRequiresEveryExpectedMergeTarget(t *testing.T) {
	plan := verificationPlan("f")
	complete := Evidence{Plan: plan, Gates: make([]GateEvidence, 0)}
	for _, target := range expectedTargets(plan, VerifyMerge) {
		complete.Gates = append(complete.Gates, GateEvidence{
			Target: target,
			Level:  string(VerifyMerge),
			Status: GatePass,
		})
	}
	if code := verificationExitCode(complete, VerifyMerge); code != 0 {
		t.Fatalf("complete merge exit = %d", code)
	}

	for _, status := range []GateStatus{GateFail, GateBlocked} {
		incomplete := complete
		incomplete.Gates = append([]GateEvidence(nil), complete.Gates...)
		gate := gateFor(incomplete, "lint", string(VerifyMerge))
		if gate == nil {
			t.Fatal("complete merge evidence has no lint gate")
		}
		gate.Status = status
		if code := verificationExitCode(incomplete, VerifyMerge); code != 1 {
			t.Fatalf("merge %s exit = %d", status, code)
		}
	}

	if code := verificationExitCode(Evidence{Plan: Plan{}}, VerifyFocused); code != 0 {
		t.Fatalf("empty focused plan exit = %d", code)
	}
}

func TestRunVerifyRequiresFactories(t *testing.T) {
	deps, _, _ := testRunDependencies(t)
	deps.runnerFactory = nil
	deps.storeFactory = nil
	assertRunFailure(t, []string{"verify", "--level", "focused"}, deps)
}

func TestRunEvidenceRequiresStoreFactory(t *testing.T) {
	deps, _, _ := testRunDependencies(t)
	deps.storeFactory = nil
	assertRunFailure(t, []string{"evidence"}, deps)
}

func TestRunOperationalFailuresReturnOneWithoutPartialOutput(t *testing.T) {
	t.Run("policy", func(t *testing.T) {
		deps, _, _ := testRunDependencies(t)
		assertRunFailure(t, []string{"--policy", "missing.yaml", "plan"}, deps)
	})
	t.Run("git", func(t *testing.T) {
		deps, git, _ := testRunDependencies(t)
		git.resolveErr = errors.New("unavailable")
		assertRunFailure(t, []string{"plan"}, deps)
	})
	t.Run("slice", func(t *testing.T) {
		deps, _, slices := testRunDependencies(t)
		slices.err = errors.New("not executable")
		assertRunFailure(t, []string{"--slice-id", "P1.0", "plan"}, deps)
	})
	t.Run("classification", func(t *testing.T) {
		deps, git, _ := testRunDependencies(t)
		git.nameStatus = []byte("M\x00unknown/file.xyz\x00")
		assertRunFailure(t, []string{"plan"}, deps)
	})
}

func assertRunFailure(t *testing.T, args []string, deps dependencies) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := run(args, &stdout, &stderr, deps); code != 1 {
		t.Fatalf("run(%v) = %d, stderr = %q", args, code, stderr.String())
	}
	if stdout.Len() != 0 || strings.Count(strings.TrimSpace(stderr.String()), "\n") != 0 {
		t.Fatalf("run(%v) stdout = %q, stderr = %q", args, stdout.String(), stderr.String())
	}
}

func testRunDependencies(t *testing.T) (dependencies, *fakeGitSource, *fakeSliceResolver) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "quality"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "quality", "iteration.yaml"), []byte(validPolicyYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("test:\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "owner.md"), []byte("# Owner\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git := &fakeGitSource{
		resolved:       map[string]string{"HEAD": strings.Repeat("b", 40)},
		mergeBase:      strings.Repeat("a", 40),
		nameStatus:     []byte("M\x00Makefile\x00"),
		binaryDiff:     []byte("patch"),
		untrackedCount: 0,
		trackedClean:   true,
	}
	slices := &fakeSliceResolver{result: &SliceRef{
		ID: "P1.0", State: "ready", Contract: "plans/p1.md", Outcome: "Ship P1.",
	}}
	return dependencies{
		git:          git,
		slices:       slices,
		root:         root,
		storeFactory: func(string) EvidenceStore { return newFileEvidenceStore(root) },
		openRoot: func(string) (*os.Root, error) {
			return os.OpenRoot(root)
		},
		goos: "linux",
		trackedFiles: func(context.Context) ([]string, error) {
			return []string{"Makefile", "scripts/tool.go"}, nil
		},
	}, git, slices
}

func reflectStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
