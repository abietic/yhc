package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunRestartArchivesFailureAndRequiresAllChecksAgain(t *testing.T) {
	deps, _, _ := testRunDependencies(t)
	withMergeTreeInspector(t, func(context.Context, string, Plan) error { return nil })
	plan := committedTestPlan(t, deps, "origin/master", "HEAD")
	store := newFileEvidenceStore(deps.root)
	seedFocusedEvidence(t, store, plan)
	if _, err := store.Record(plan, GateEvidence{
		Target: "test", Level: "merge", Status: GateFail, DurationMillis: 900000,
		FailureLogPath: "build/iteration/" + plan.DiffDigest + "/logs/test.log",
	}); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(deps.root, "build", "iteration", plan.DiffDigest, "logs", "test.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("original timeout\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(deps.root, "build", "iteration", plan.DiffDigest, "evidence.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out, diagnostic bytes.Buffer
	if code := run([]string{"--head", "HEAD", "--format", "json", "restart", "--reason", "environment_timeout"}, &out, &diagnostic, deps); code != 0 {
		t.Fatalf("restart = %d: %s", code, diagnostic.String())
	}
	evidence, err := decodeEvidence(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if evidence.State != "changed" || len(evidence.Gates) != 0 {
		t.Fatalf("restart reused old successful gates: %#v", evidence)
	}
	paths, err := filepath.Glob(filepath.Join(deps.root, "build", "iteration", "history", plan.DiffDigest, "*", "evidence.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("archived evidence paths = %v, %v", paths, err)
	}
	archived, err := os.ReadFile(paths[0])
	if err != nil || !bytes.Equal(archived, original) {
		t.Fatalf("original failure was modified: %v", err)
	}
	oldLog, err := os.ReadFile(filepath.Join(filepath.Dir(paths[0]), "logs", "test.log"))
	if err != nil || string(oldLog) != "original timeout\n" {
		t.Fatalf("original failure log lost: %q, %v", oldLog, err)
	}
	out.Reset()
	diagnostic.Reset()
	if code := run([]string{"--head", "HEAD", "evidence", "--require-ready"}, &out, &diagnostic, deps); code == 0 {
		t.Fatal("fresh verification attempt was accepted as ready")
	}
}

func TestRunVerificationAcceptsExplicitBoundedTestBudget(t *testing.T) {
	deps, git, _ := testRunDependencies(t)
	git.nameStatus = []byte("M\x00scripts/tool.go\x00")
	withMergeTreeInspector(t, func(context.Context, string, Plan) error { return nil })
	plan := committedTestPlan(t, deps, "origin/master", "HEAD")
	seedFocusedEvidence(t, newFileEvidenceStore(deps.root), plan)
	runner := newCommandTargetRunner(Plan{})
	testExecutions := 0
	runner.factory = func(_ context.Context, name string, args ...string) targetProcess {
		if name == "make" && len(args) > 0 && args[0] == "test" {
			testExecutions++
		}
		return &fakeProcess{}
	}
	deps.runnerFactory = func(plan Plan) TargetRunner {
		runner.UsePlan(plan)
		if err := runner.UseTestBudget(0, ""); err != nil {
			t.Fatal(err)
		}
		return runner
	}
	var out, diagnostic bytes.Buffer
	code := run([]string{"--head", "HEAD", "--format", "json", "verify", "--level", "merge", "--test-timeout", "45m", "--budget-reason", "host_contention"}, &out, &diagnostic, deps)
	if code != 0 || runner.testTimeout != 45*time.Minute || runner.budgetReason != "host_contention" {
		t.Fatalf("explicit test budget is unsupported: %d, %s", code, diagnostic.String())
	}
	evidence, err := decodeEvidence(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	gate := gateFor(evidence, "test", "merge")
	if gate == nil || gate.Status != GatePass || gate.TimeoutMillis != 2700000 || gate.BudgetReason != "host_contention" {
		t.Fatalf("budget was accepted without executing and recording test: %#v", gate)
	}
	for _, timeout := range []string{"45m", "", "30m"} {
		args := []string{"--head", "HEAD", "--format", "json", "verify", "--level", "merge"}
		if timeout != "" {
			args = append(args, "--test-timeout", timeout, "--budget-reason", "host_contention")
		}
		out.Reset()
		diagnostic.Reset()
		code := run(args, &out, &diagnostic, deps)
		if (code != 0) != (timeout == "30m") || testExecutions != 1 {
			t.Fatalf("reused budget %q = %d, test executions = %d: %s", timeout, code, testExecutions, diagnostic.String())
		}
		current, err := newFileEvidenceStore(deps.root).Load(plan)
		if err != nil {
			t.Fatal(err)
		}
		if got := gateFor(current, "test", "merge"); got == nil || *got != *gate {
			t.Fatal("reused test budget changed its evidence")
		}
	}
}

func TestRunMergeRejectsBudgetWithoutApplicableTest(t *testing.T) {
	deps, git, _ := testRunDependencies(t)
	policy := strings.Replace(validPolicyYAML, "  metadata:\n", "  documentation:\n    priority: 1\n    paths: [docs/**]\n    targets: [docs-check-ci]\n    focused_packages: []\n  metadata:\n", 1)
	if err := os.WriteFile(filepath.Join(deps.root, "quality", "iteration.yaml"), []byte(policy), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deps.root, "Makefile"), []byte("test:\ndocs-check-ci:\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git.nameStatus = []byte("M\x00docs/owner.md\x00")
	withMergeTreeInspector(t, func(context.Context, string, Plan) error { return nil })
	plan := committedTestPlan(t, deps, "origin/master", "HEAD")
	seedFocusedEvidence(t, newFileEvidenceStore(deps.root), plan)
	deps.runnerFactory = func(plan Plan) TargetRunner {
		runner := newCommandTargetRunner(plan)
		runner.beforeStart = func() { t.Fatal("inapplicable budget reached execution") }
		return runner
	}
	var out, diagnostic bytes.Buffer
	if code := run([]string{"--head", "HEAD", "verify", "--level", "merge", "--test-timeout", "45m", "--budget-reason", "host_contention"}, &out, &diagnostic, deps); code == 0 || !strings.Contains(diagnostic.String(), "requires an applicable merge test gate") {
		t.Fatalf("documentation-only budget = %d: %s", code, diagnostic.String())
	}
}

func TestRunFocusedRejectsTestBudgetBeforeExecution(t *testing.T) {
	deps, _, _ := testRunDependencies(t)
	deps.runnerFactory = func(Plan) TargetRunner { t.Fatal("ignored focused budget reached execution"); return nil }
	var out, diagnostic bytes.Buffer
	if code := run([]string{"verify", "--level", "focused", "--test-timeout", "45m", "--budget-reason", "host_contention"}, &out, &diagnostic, deps); code != 2 {
		t.Fatalf("focused budget = %d, %s", code, diagnostic.String())
	}
}

func TestRunRestartCannotInvalidateReadyEvidence(t *testing.T) {
	deps, _, _ := testRunDependencies(t)
	withMergeTreeInspector(t, func(context.Context, string, Plan) error { return nil })
	plan := committedTestPlan(t, deps, "origin/master", "HEAD")
	store := newFileEvidenceStore(deps.root)
	seedReadyEvidence(t, store, plan)
	for _, reason := range []string{"environment_timeout", "environment_repaired"} {
		var out, diagnostic bytes.Buffer
		if code := run([]string{"--head", "HEAD", "restart", "--reason", reason}, &out, &diagnostic, deps); code == 0 {
			t.Fatal("restart invalidated ready evidence")
		}
		if code := run([]string{"--head", "HEAD", "evidence", "--require-ready"}, &out, &diagnostic, deps); code != 0 {
			t.Fatalf("rejected restart changed ready evidence: %s", diagnostic.String())
		}
	}
}

func failedAttempt(t *testing.T) (*fileEvidenceStore, Plan, []byte) {
	t.Helper()
	store, plan := newFileEvidenceStore(t.TempDir()), storePlan()
	seedFocusedEvidence(t, store, plan)
	if _, err := store.Record(plan, GateEvidence{Target: "test", Level: "merge", Status: GateFail, DurationMillis: 900000}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(store.root, "build", "iteration", plan.DiffDigest, "evidence.json"))
	if err != nil {
		t.Fatal(err)
	}
	return store, plan, before
}

func TestRestartResumesJournalAtEachDirectoryMove(t *testing.T) {
	for _, phase := range []string{"before_archive", "before_publish", "after_publish"} {
		t.Run(phase, func(t *testing.T) {
			store, plan, original := failedAttempt(t)
			moves := 0
			store.move = func(root *os.Root, from, to string) error {
				moves++
				if (phase == "before_archive" && moves == 1) || (phase == "before_publish" && moves == 2) {
					return errors.New("injected interruption")
				}
				if err := root.Rename(from, to); err != nil {
					return err
				}
				if phase == "after_publish" && moves == 2 {
					return errors.New("injected interruption")
				}
				return nil
			}
			if _, err := store.Restart(plan, "environment_timeout"); err == nil {
				t.Fatal("interruption was ignored")
			}
			if err := rejectPendingRestart(store.root); err == nil {
				t.Fatal("verification may run with a pending restart")
			}
			if _, err := store.Restart(plan, "environment_repaired"); err == nil {
				t.Fatal("recovery changed the declared reason")
			}
			store.move = nil
			fresh, err := store.Restart(plan, "environment_timeout")
			if err != nil {
				t.Fatal(err)
			}
			if fresh.Attempt == nil || len(fresh.Gates) != 0 || fresh.State != "changed" {
				t.Fatalf("recovered attempt = %#v", fresh)
			}
			archived, err := os.ReadFile(filepath.Join(store.root, filepath.FromSlash(fresh.Attempt.PreviousPath), "evidence.json"))
			if err != nil || !bytes.Equal(original, archived) {
				t.Fatalf("archived original changed: %v", err)
			}
			if err := rejectPendingRestart(store.root); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRestartRerunsEveryFocusedAndMergeGate(t *testing.T) {
	store, plan, original := failedAttempt(t)
	withMergeTreeInspector(t, func(context.Context, string, Plan) error { return nil })
	fresh, err := store.Restart(plan, "environment_timeout")
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingTargetRunner{}
	if _, err := verify(t.Context(), store.root, VerifyOptions{Level: VerifyMerge, Plan: plan}, runner, store, func(context.Context) (Plan, error) { return plan, nil }); err == nil || len(runner.calls) != 0 {
		t.Fatal("merge reused previous focused success")
	}
	if _, err := verify(t.Context(), store.root, VerifyOptions{Level: VerifyFocused, Plan: plan}, runner, store, nil); err != nil {
		t.Fatal(err)
	}
	if got := runner.calls; !reflectStrings(got, focusedTargets(plan)) {
		t.Fatalf("focused targets = %v", got)
	}
	runner.calls = nil
	evidence, err := verify(t.Context(), store.root, VerifyOptions{Level: VerifyMerge, Plan: plan}, runner, store, func(context.Context) (Plan, error) { return plan, nil })
	if err != nil || evidence.State != "evidence_ready" {
		t.Fatalf("new attempt merge = %s, %v", evidence.State, err)
	}
	want := append([]string{"fmt"}, mergeTargets(plan)...)
	// docs-check is not applicable in this test-owned worktree.
	filtered := want[:0]
	for _, target := range want {
		if target != "docs-check" {
			filtered = append(filtered, target)
		}
	}
	if !reflectStrings(runner.calls, filtered) {
		t.Fatalf("merge targets = %v, want %v", runner.calls, filtered)
	}
	archived, err := os.ReadFile(filepath.Join(store.root, filepath.FromSlash(fresh.Attempt.PreviousPath), "evidence.json"))
	if err != nil || !bytes.Equal(archived, original) {
		t.Fatalf("passing fresh attempt rewrote original: %v", err)
	}
}

func TestRestartRejectsStalePlanUnsafeHistoryAndUnfailedEvidence(t *testing.T) {
	for _, scenario := range []string{"stale_head", "history_symlink", "no_failure", "wrong_reason"} {
		t.Run(scenario, func(t *testing.T) {
			store, plan, original := failedAttempt(t)
			reason := "environment_timeout"
			switch scenario {
			case "stale_head":
				plan.Head = strings.Repeat("f", 40)
			case "history_symlink":
				if err := os.Symlink(t.TempDir(), filepath.Join(store.root, "build", "iteration", "history")); err != nil {
					t.Fatal(err)
				}
			case "no_failure":
				store = newFileEvidenceStore(t.TempDir())
				seedFocusedEvidence(t, store, plan)
			case "wrong_reason":
				reason = "private arbitrary text"
			}
			if _, err := store.Restart(plan, reason); err == nil {
				t.Fatal("unsafe restart accepted")
			}
			if scenario != "no_failure" {
				current, err := os.ReadFile(filepath.Join(store.root, "build", "iteration", plan.DiffDigest, "evidence.json"))
				if err != nil || !bytes.Equal(current, original) {
					t.Fatalf("rejected restart modified original: %v", err)
				}
			}
		})
	}
}

func TestVerificationLockIsExclusiveAndNeverStolen(t *testing.T) {
	root := t.TempDir()
	release, err := acquireVerificationLock(root)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := acquireVerificationLock(root); err == nil {
		_ = other()
		t.Fatal("concurrent verification lock was stolen")
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	other, err := acquireVerificationLock(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := other(); err != nil {
		t.Fatal(err)
	}
}

func TestRunRestartRejectsDirtyTreeBeforeArchiving(t *testing.T) {
	deps, _, _ := testRunDependencies(t)
	withMergeTreeInspector(t, func(context.Context, string, Plan) error { return errors.New("dirty candidate") })
	assertRunFailure(t, []string{"restart", "--reason", "environment_repaired"}, deps)
	if _, err := os.Stat(filepath.Join(deps.root, "build", "iteration", "history")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dirty tree started archiving: %v", err)
	}
}

func TestRestartRefusesChangedArchiveDuringRecovery(t *testing.T) {
	store, plan, original := failedAttempt(t)
	moves := 0
	store.move = func(root *os.Root, from, to string) error {
		moves++
		if moves == 2 {
			return errors.New("interrupt before publish")
		}
		return root.Rename(from, to)
	}
	if _, err := store.Restart(plan, "environment_timeout"); err == nil {
		t.Fatal("interruption ignored")
	}
	paths, err := filepath.Glob(filepath.Join(store.root, "build", "iteration", "history", plan.DiffDigest, "*", "evidence.json"))
	if err != nil {
		t.Fatal(err)
	}
	var archive string
	for _, name := range paths {
		if !strings.HasPrefix(filepath.Base(filepath.Dir(name)), "pending-") {
			archive = name
		}
	}
	if archive == "" {
		t.Fatal("original was not archived")
	}
	changed := bytes.Replace(original, []byte(`"duration_ms":900000`), []byte(`"duration_ms":900001`), 1)
	if bytes.Equal(changed, original) {
		t.Fatal("fixture did not alter an evidence fact")
	}
	if err := os.WriteFile(archive, changed, 0o600); err != nil {
		t.Fatal(err)
	}
	store.move = nil
	if _, err := store.Restart(plan, "environment_timeout"); err == nil {
		t.Fatal("changed archive accepted")
	}
	if _, err := os.Stat(filepath.Join(store.root, "build", "iteration", plan.DiffDigest)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fresh evidence was published after an inconsistent archive: %v", err)
	}
}

func TestRunConcurrentRestartCannotTouchExecutingVerification(t *testing.T) {
	deps, git, _ := testRunDependencies(t)
	git.nameStatus = []byte("M\x00scripts/tool.go\x00")
	entered, finish := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-finish:
		default:
			close(finish)
		}
	})
	runner := newCommandTargetRunner(Plan{})
	runner.factory = func(context.Context, string, ...string) targetProcess {
		return &fakeProcess{wait: func() error { close(entered); <-finish; return nil }}
	}
	deps.runnerFactory = func(plan Plan) TargetRunner { runner.UsePlan(plan); return runner }
	done := make(chan int, 1)
	go func() {
		var out, diagnostic bytes.Buffer
		done <- run([]string{"verify", "--level", "focused"}, &out, &diagnostic, deps)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("verification did not enter process barrier")
	}
	otherDeps := deps
	otherGit := *git
	otherGit.resolveCalls = nil
	otherDeps.git = &otherGit
	var out, diagnostic bytes.Buffer
	if code := run([]string{"restart", "--reason", "environment_repaired"}, &out, &diagnostic, otherDeps); code != 1 || !strings.Contains(diagnostic.String(), "locked") {
		t.Fatalf("concurrent restart = %d: %s", code, diagnostic.String())
	}
	if _, err := os.Stat(filepath.Join(deps.root, "build", "iteration", "history")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("active verification was archived")
	}
	close(finish)
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("verification = %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("verification did not release its lock")
	}
}
