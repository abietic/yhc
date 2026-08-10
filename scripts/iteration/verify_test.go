package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type recordingTargetRunner struct {
	calls   []string
	results map[string]RunResult
	plans   []Plan
}

func (runner *recordingTargetRunner) Run(_ context.Context, _, _, target string) RunResult {
	runner.calls = append(runner.calls, target)
	if result, ok := runner.results[target]; ok {
		return result
	}
	return RunResult{Status: GatePass, DurationMillis: 1}
}

func (runner *recordingTargetRunner) UsePlan(plan Plan) {
	runner.plans = append(runner.plans, plan)
}

type recordingEvidenceStore struct {
	evidence map[string]Evidence
	records  []GateEvidence
}

func newRecordingEvidenceStore() *recordingEvidenceStore {
	return &recordingEvidenceStore{evidence: make(map[string]Evidence)}
}

func (store *recordingEvidenceStore) Load(plan Plan) (Evidence, error) {
	if evidence, ok := store.evidence[plan.DiffDigest]; ok {
		return evidence, nil
	}
	return Evidence{SchemaVersion: 1, Plan: plan, State: stateForPlan(plan)}, nil
}

func (store *recordingEvidenceStore) Record(plan Plan, next GateEvidence) (Evidence, error) {
	evidence, _ := store.Load(plan)
	store.records = append(store.records, next)
	replaced := false
	for index := range evidence.Gates {
		existing := evidence.Gates[index]
		if existing.Target == next.Target && existing.Level == next.Level {
			replaced = true
			if mayReplace(existing, next) {
				evidence.Gates[index] = next
			}
			break
		}
	}
	if !replaced {
		evidence.Gates = append(evidence.Gates, next)
	}
	evidence.Plan = plan
	store.evidence[plan.DiffDigest] = evidence
	return evidence, nil
}

func seedFocusedEvidence(t *testing.T, store EvidenceStore, plan Plan) {
	t.Helper()
	for _, target := range focusedTargets(plan) {
		if _, err := store.Record(plan, GateEvidence{
			Target: target, Level: string(VerifyFocused), Status: GatePass, DurationMillis: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func verificationPlan(digestByte string) Plan {
	return Plan{
		SchemaVersion: 1,
		Base:          strings.Repeat("a", 40),
		Head:          strings.Repeat("b", 40),
		DiffDigest:    strings.Repeat(digestByte, 64),
		Changed:       []ChangedPath{{Path: "engine/engine.go", Status: "M", Owner: "engine-runtime", Kind: PathProduction}},
		Modules:       []string{"engine-runtime"},
		Risks:         []string{"contract"},
		FocusedChecks: []FocusedCheck{{Owner: "engine-runtime", Packages: []string{"./engine/..."}}},
		RequiredTargets: []string{
			"fmt", "lint", "test", "build", "docs-check-ci", "docs-check", "git-diff-check", "test-contract",
		},
	}
}

func TestVerifyFocusedSelection(t *testing.T) {
	tests := []struct {
		name string
		plan Plan
		want []string
	}{
		{
			name: "engine and acp",
			plan: Plan{
				FocusedChecks:   []FocusedCheck{{Owner: "acp-adapter"}, {Owner: "engine-runtime"}},
				RequiredTargets: []string{"test-contract"},
			},
			want: []string{"focused/acp-adapter", "focused/engine-runtime", "test-contract"},
		},
		{
			name: "governance",
			plan: Plan{
				FocusedChecks: []FocusedCheck{{Owner: "governance"}},
				ChangeClasses: []string{"governance"},
			},
			want: []string{"focused/governance", "docs-check-ci"},
		},
		{
			name: "documentation",
			plan: Plan{
				Changed:       []ChangedPath{{Path: "docs/a.md", Owner: "documentation", Kind: PathClass}},
				ChangeClasses: []string{"documentation"},
			},
			want: []string{"docs-check-ci", "git-diff-check"},
		},
		{
			name: "contract and fuzz",
			plan: Plan{
				Risks:           []string{"contract", "fuzz"},
				RequiredTargets: []string{"test-contract", "test-fuzz-smoke"},
			},
			want: []string{"test-contract", "test-fuzz-smoke"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := focusedTargets(test.plan); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("focusedTargets() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestVerifyFocusedRunsInOrderAndStopsAtFirstFailure(t *testing.T) {
	plan := verificationPlan("1")
	plan.FocusedChecks = []FocusedCheck{{Owner: "acp-adapter"}, {Owner: "engine-runtime"}}
	runner := &recordingTargetRunner{results: map[string]RunResult{
		"focused/engine-runtime": {Status: GateFail, DurationMillis: 1},
	}}
	store := newRecordingEvidenceStore()
	evidence, err := verify(
		context.Background(), t.TempDir(), VerifyOptions{Level: VerifyFocused, Plan: plan},
		runner, store, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := []string{"focused/acp-adapter", "focused/engine-runtime"}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Fatalf("runner calls = %#v, want %#v", runner.calls, wantCalls)
	}
	if gate := gateFor(evidence, "test-contract", string(VerifyFocused)); gate == nil || gate.Status != GateBlocked || gate.DurationMillis != 0 {
		t.Fatalf("later target = %#v, want unexecuted blocked", gate)
	}
}

func TestVerifyMergeOrderAndOptionalReference(t *testing.T) {
	plan := verificationPlan("2")
	plan.RequiredTargets = append(plan.RequiredTargets, "test-race")
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".reference"), 0o700); err != nil {
		t.Fatal(err)
	}
	store := newRecordingEvidenceStore()
	seedFocusedEvidence(t, store, plan)
	runner := &recordingTargetRunner{}
	checks := 0
	withMergeTreeInspector(t, func(_ context.Context, _ string, got Plan) error {
		checks++
		if got.DiffDigest != plan.DiffDigest {
			t.Fatalf("tree check digest = %q", got.DiffDigest)
		}
		return nil
	})
	evidence, err := verify(
		context.Background(), root, VerifyOptions{Level: VerifyMerge, Plan: plan}, runner, store,
		func(context.Context) (Plan, error) { return plan, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"fmt", "lint", "test", "build", "docs-check-ci", "docs-check", "git-diff-check", "test-contract", "test-race"}
	if checks != 2 || !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("checks=%d calls=%#v, want %#v", checks, runner.calls, want)
	}
	if gate := gateFor(evidence, "head-tree-clean", string(VerifyMerge)); gate == nil || gate.Status != GatePass {
		t.Fatalf("clean gate = %#v", gate)
	}
}

func TestVerifyMergePromotesEquivalentFocusedCandidateAfterCommit(t *testing.T) {
	root := t.TempDir()
	working := verificationPlan("a")
	working.Head = working.Base
	committed := working
	committed.Head = strings.Repeat("d", 40)
	store := newFileEvidenceStore(root)
	seedFocusedEvidence(t, store, working)
	runner := &recordingTargetRunner{}
	withMergeTreeInspector(t, func(context.Context, string, Plan) error { return nil })

	evidence, err := verify(
		context.Background(), root,
		VerifyOptions{Level: VerifyMerge, Plan: committed}, runner, store,
		func(context.Context) (Plan, error) { return committed, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.State != "evidence_ready" || evidence.Plan.Head != committed.Head {
		t.Fatalf("committed evidence = %#v", evidence)
	}
	if len(runner.calls) == 0 || runner.calls[0] != "fmt" {
		t.Fatalf("merge gates were not executed after promotion: %#v", runner.calls)
	}
}

func TestVerifyMergeDoesNotPromoteFocusedCandidateBeforeCleanTreeCheck(t *testing.T) {
	root := t.TempDir()
	working := verificationPlan("b")
	working.Head = working.Base
	committed := working
	committed.Head = strings.Repeat("d", 40)
	store := newFileEvidenceStore(root)
	seedFocusedEvidence(t, store, working)
	runner := &recordingTargetRunner{}
	withMergeTreeInspector(t, func(context.Context, string, Plan) error {
		return errors.New("candidate tree is not clean")
	})

	if _, err := verify(
		context.Background(), root,
		VerifyOptions{Level: VerifyMerge, Plan: committed}, runner, store,
		func(context.Context) (Plan, error) { return committed, nil },
	); err == nil {
		t.Fatal("merge verification promoted a candidate before the clean-tree check")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("dirty promotion path dispatched gates: %#v", runner.calls)
	}
	if _, err := store.Load(working); err != nil {
		t.Fatalf("dirty promotion path changed source evidence: %v", err)
	}
	if _, err := store.Load(committed); err == nil {
		t.Fatal("dirty promotion path activated committed evidence")
	}
}

func TestVerifyMergeReferenceApplicability(t *testing.T) {
	plan := verificationPlan("3")
	t.Run("absent is not applicable without dispatch", func(t *testing.T) {
		store := newRecordingEvidenceStore()
		seedFocusedEvidence(t, store, plan)
		runner := &recordingTargetRunner{}
		withMergeTreeInspector(t, func(context.Context, string, Plan) error { return nil })
		evidence, err := verify(
			context.Background(), t.TempDir(), VerifyOptions{Level: VerifyMerge, Plan: plan}, runner, store,
			func(context.Context) (Plan, error) { return plan, nil },
		)
		if err != nil {
			t.Fatal(err)
		}
		if slicesContains(runner.calls, "docs-check") {
			t.Fatalf("docs-check dispatched: %#v", runner.calls)
		}
		if gate := gateFor(evidence, "docs-check", string(VerifyMerge)); gate == nil || gate.Status != GateNotApplicable {
			t.Fatalf("docs-check gate = %#v", gate)
		}
		for _, required := range []string{"docs-check-ci", "git-diff-check"} {
			if !slicesContains(runner.calls, required) {
				t.Fatalf("required target %q missing from %#v", required, runner.calls)
			}
		}
	})

	t.Run("inspection error blocks before dispatch", func(t *testing.T) {
		store := newRecordingEvidenceStore()
		seedFocusedEvidence(t, store, plan)
		runner := &recordingTargetRunner{}
		withMergeTreeInspector(t, func(context.Context, string, Plan) error { return nil })
		prior := lstatTargetPath
		lstatTargetPath = func(string) (os.FileInfo, error) { return nil, os.ErrPermission }
		t.Cleanup(func() { lstatTargetPath = prior })
		evidence, err := verify(
			context.Background(), t.TempDir(), VerifyOptions{Level: VerifyMerge, Plan: plan}, runner, store,
			func(context.Context) (Plan, error) { return plan, nil },
		)
		if err != nil {
			t.Fatal(err)
		}
		if slicesContains(runner.calls, "docs-check") || slicesContains(runner.calls, "git-diff-check") {
			t.Fatalf("post-block targets dispatched: %#v", runner.calls)
		}
		if gate := gateFor(evidence, "docs-check", string(VerifyMerge)); gate == nil || gate.Status != GateBlocked {
			t.Fatalf("docs-check gate = %#v", gate)
		}
	})
}

func TestVerifyMergeInvalidatesPreFormatDigest(t *testing.T) {
	before := verificationPlan("4")
	after := verificationPlan("5")
	store := newRecordingEvidenceStore()
	seedFocusedEvidence(t, store, before)
	runner := &recordingTargetRunner{}
	checks := 0
	withMergeTreeInspector(t, func(context.Context, string, Plan) error {
		checks++
		if checks == 2 {
			return errors.New("fmt changed tracked tree")
		}
		return nil
	})
	evidence, err := verify(
		context.Background(), t.TempDir(), VerifyOptions{Level: VerifyMerge, Plan: before}, runner, store,
		func(context.Context) (Plan, error) { return after, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(runner.calls, []string{"fmt"}) {
		t.Fatalf("runner calls = %#v", runner.calls)
	}
	if evidence.Plan.DiffDigest != after.DiffDigest {
		t.Fatalf("evidence digest = %q, want post-format %q", evidence.Plan.DiffDigest, after.DiffDigest)
	}
	if gate := gateFor(evidence, "fmt", string(VerifyMerge)); gate == nil || gate.Status != GatePass {
		t.Fatalf("carried fmt gate = %#v", gate)
	}
	if gate := gateFor(evidence, "head-tree-clean", string(VerifyMerge)); gate == nil || gate.Status != GateBlocked || gate.DurationMillis == 0 {
		t.Fatalf("post-format clean gate = %#v", gate)
	}
}

func TestVerifyMergeRequiresFocusedEvidenceAndCleanTree(t *testing.T) {
	plan := verificationPlan("6")
	t.Run("focused prerequisite", func(t *testing.T) {
		runner := &recordingTargetRunner{}
		_, err := verify(
			context.Background(), t.TempDir(), VerifyOptions{Level: VerifyMerge, Plan: plan}, runner,
			newRecordingEvidenceStore(), func(context.Context) (Plan, error) { return plan, nil },
		)
		if err == nil || !strings.Contains(err.Error(), "focused evidence") || len(runner.calls) != 0 {
			t.Fatalf("err=%v calls=%#v", err, runner.calls)
		}
	})

	t.Run("first clean check", func(t *testing.T) {
		store := newRecordingEvidenceStore()
		seedFocusedEvidence(t, store, plan)
		runner := &recordingTargetRunner{}
		withMergeTreeInspector(t, func(context.Context, string, Plan) error { return errors.New("dirty") })
		evidence, err := verify(
			context.Background(), t.TempDir(), VerifyOptions{Level: VerifyMerge, Plan: plan}, runner, store,
			func(context.Context) (Plan, error) { return plan, nil },
		)
		if err != nil {
			t.Fatal(err)
		}
		if len(runner.calls) != 0 {
			t.Fatalf("dirty tree dispatched %#v", runner.calls)
		}
		if gate := gateFor(evidence, "head-tree-clean", string(VerifyMerge)); gate == nil || gate.Status != GateBlocked || gate.DurationMillis == 0 {
			t.Fatalf("clean gate = %#v", gate)
		}
		if gate := gateFor(evidence, "fmt", string(VerifyMerge)); gate == nil || gate.Status != GateBlocked || gate.DurationMillis != 0 {
			t.Fatalf("fmt gate = %#v", gate)
		}
	})
}

func TestTargetApplicability(t *testing.T) {
	if status, run, err := targetApplicability(t.TempDir(), "docs-check", "linux"); err != nil || run || status != GateNotApplicable {
		t.Fatalf("absent reference = %q, %v, %v", status, run, err)
	}
	if status, run, err := targetApplicability(t.TempDir(), "test-pty", "windows"); err != nil || run || status != GateNotApplicable {
		t.Fatalf("windows PTY = %q, %v, %v", status, run, err)
	}
}

func TestCleanTreeRequiresCommittedTopicBranch(t *testing.T) {
	repository := t.TempDir()
	runTestGit(t, repository, "init", "-b", "master")
	runTestGit(t, repository, "config", "user.name", "Iteration Test")
	runTestGit(t, repository, "config", "user.email", "iteration@example.invalid")
	writeTestFile(t, repository, "tracked.txt", "base\n")
	runTestGit(t, repository, "add", "tracked.txt")
	runTestGit(t, repository, "commit", "-m", "base")
	head := strings.TrimSpace(runTestGit(t, repository, "rev-parse", "HEAD"))
	plan := Plan{Head: head}
	if err := cleanTree(context.Background(), repository, plan); err == nil || !strings.Contains(err.Error(), "non-master") {
		t.Fatalf("master cleanTree error = %v", err)
	}
	runTestGit(t, repository, "switch", "-c", "codex/test")
	writeTestFile(t, repository, "untracked.txt", "outside scope\n")
	if err := cleanTree(context.Background(), repository, plan); err != nil {
		t.Fatalf("untracked-only cleanTree = %v", err)
	}
	writeTestFile(t, repository, "tracked.txt", "dirty\n")
	if err := cleanTree(context.Background(), repository, plan); err == nil || !strings.Contains(err.Error(), "does not match HEAD") {
		t.Fatalf("dirty cleanTree error = %v", err)
	}
}

func withMergeTreeInspector(t *testing.T, replacement func(context.Context, string, Plan) error) {
	t.Helper()
	previous := inspectMergeTree
	inspectMergeTree = replacement
	t.Cleanup(func() { inspectMergeTree = previous })
}

func slicesContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
