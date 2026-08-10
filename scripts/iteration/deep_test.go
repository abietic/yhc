package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDeepSelectionUsesFixedOrderAndSkipsDocumentation(t *testing.T) {
	policy := deepTestPolicy()
	plan := verificationPlan("a")
	plan.Modules = []string{"engine-runtime", "tui-adapter"}
	plan.Risks = []string{"terminal", "contract", "fuzz"}
	got, err := selectedDeepTargets(plan, policy)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"check-boundaries",
		"test-fault-injection",
		"test-fuzz-deep",
		"test-e2e-deep",
		"test-pty-deep",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selectedDeepTargets() = %#v, want %#v", got, want)
	}

	documentation := plan
	documentation.Modules = nil
	documentation.Risks = nil
	documentation.Changed = []ChangedPath{{
		Path: "docs/README.md", Status: "M", Owner: "documentation", Kind: PathClass,
	}}
	documentation.ChangeClasses = []string{"documentation"}
	got, err = selectedDeepTargets(documentation, policy)
	if err != nil || len(got) != 0 {
		t.Fatalf("documentation deep targets = %#v, %v", got, err)
	}
}

func TestRunDeepStopsAtFirstFailureAndRetryKeepsIntake(t *testing.T) {
	root := t.TempDir()
	plan := verificationPlan("b")
	store := newFileDeepIntakeStore(root)
	logPath := filepath.ToSlash(filepath.Join(
		"build",
		"iteration",
		plan.DiffDigest,
		"logs",
		"test-fault-injection.log",
	))
	runner := &recordingTargetRunner{results: map[string]RunResult{
		"test-fault-injection": {
			Status: GateFail, DurationMillis: 2, FailureLogPath: logPath,
		},
	}}
	result, err := runDeep(
		context.Background(),
		root,
		plan,
		deepTestPolicy(),
		"windows",
		runner,
		store,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(runner.calls, []string{"check-boundaries", "test-fault-injection"}) {
		t.Fatalf("deep calls = %#v", runner.calls)
	}
	if result.Intake == nil || result.Intake.Status != GateFail ||
		result.Intake.Target != "test-fault-injection" {
		t.Fatalf("deep result = %#v", result)
	}
	if len(result.Report.Gates) != 2 || result.Report.Gates[0].Status != GatePass ||
		result.Report.Gates[0].DurationMillis != 0 {
		t.Fatalf("persistable deep report = %#v", result.Report)
	}

	retry := &recordingTargetRunner{}
	retried, err := runDeep(
		context.Background(),
		root,
		plan,
		deepTestPolicy(),
		"darwin",
		retry,
		store,
	)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, err := canonicalJSON(result)
	if err != nil {
		t.Fatal(err)
	}
	retryJSON, err := canonicalJSON(retried)
	if err != nil {
		t.Fatal(err)
	}
	if len(retry.calls) != 0 || string(retryJSON) != string(firstJSON) ||
		!reflect.DeepEqual(retried, result) {
		t.Fatalf("retry = %#v, calls = %#v", retried, retry.calls)
	}
}

func TestRunDeepMarksWindowsPTYNotApplicableAndContinues(t *testing.T) {
	plan := verificationPlan("c")
	plan.Risks = []string{"terminal"}
	runner := &recordingTargetRunner{}
	result, err := runDeep(
		context.Background(),
		t.TempDir(),
		plan,
		deepTestPolicy(),
		"windows",
		runner,
		newMemoryDeepIntakeStore(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(runner.calls, []string{"check-boundaries", "test-e2e-deep"}) {
		t.Fatalf("windows calls = %#v", runner.calls)
	}
	gate := deepGateFor(result.Report, "test-pty-deep")
	if gate == nil || gate.Status != GateNotApplicable || result.Intake != nil {
		t.Fatalf("windows deep result = %#v", result)
	}
}

func TestDeepIntakeStoreIsStrictConfinedAndImmutable(t *testing.T) {
	digest := strings.Repeat("d", 64)
	intake := DeepIntake{
		SchemaVersion: 1,
		DiffDigest:    digest,
		Target:        "test-fault-injection",
		Status:        GateBlocked,
		Platform:      "linux",
	}
	t.Run("invalid digest", func(t *testing.T) {
		store := newFileDeepIntakeStore(t.TempDir())
		invalid := intake
		invalid.DiffDigest = "../escape"
		if err := store.Write(invalid); err == nil {
			t.Fatal("invalid digest accepted")
		}
	})
	t.Run("first result cannot be replaced", func(t *testing.T) {
		store := newFileDeepIntakeStore(t.TempDir())
		if err := store.Write(intake); err != nil {
			t.Fatal(err)
		}
		next := intake
		next.Status = GateFail
		if err := store.Write(next); err == nil {
			t.Fatal("existing intake replaced")
		}
		got, exists, err := store.Load(digest)
		if err != nil || !exists || !reflect.DeepEqual(got, intake) {
			t.Fatalf("stored intake = %#v, %v, %v", got, exists, err)
		}
	})
	t.Run("symlink target", func(t *testing.T) {
		root := t.TempDir()
		directory := filepath.Join(root, "build", "iteration", digest)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "outside.json"), filepath.Join(directory, "deep-intake.json")); err != nil {
			t.Fatal(err)
		}
		if err := newFileDeepIntakeStore(root).Write(intake); err == nil {
			t.Fatal("symlink intake accepted")
		}
	})
	t.Run("second JSON value", func(t *testing.T) {
		root := t.TempDir()
		directory := filepath.Join(root, "build", "iteration", digest)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(intake)
		if err != nil {
			t.Fatal(err)
		}
		data = append(append(data, '\n'), []byte("{}\n")...)
		if err := os.WriteFile(filepath.Join(directory, "deep-intake.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := newFileDeepIntakeStore(root).Load(digest); err == nil {
			t.Fatal("second JSON value accepted")
		}
	})
}

func TestDeepIntakeNeverCopiesFailureLogContent(t *testing.T) {
	root := t.TempDir()
	plan := verificationPlan("e")
	markers := []string{
		"PROMPT_SECRET_MARKER",
		"RESPONSE_SECRET_MARKER",
		"TRANSCRIPT_SECRET_MARKER",
		"CREDENTIAL_SECRET_MARKER",
		"ENVIRONMENT_SECRET_MARKER",
		"SOURCE_SECRET_MARKER",
	}
	logPath := filepath.ToSlash(filepath.Join(
		"build",
		"iteration",
		plan.DiffDigest,
		"logs",
		"test-fault-injection.log",
	))
	fullLog := filepath.Join(root, filepath.FromSlash(logPath))
	if err := os.MkdirAll(filepath.Dir(fullLog), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullLog, []byte(strings.Join(markers, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &recordingTargetRunner{results: map[string]RunResult{
		"test-fault-injection": {
			Status: GateFail, DurationMillis: 1, FailureLogPath: logPath,
		},
	}}
	if _, err := runDeep(
		context.Background(),
		root,
		plan,
		deepTestPolicy(),
		"linux",
		runner,
		newFileDeepIntakeStore(root),
	); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "build", "iteration", plan.DiffDigest, "deep-intake.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range markers {
		if strings.Contains(string(data), marker) {
			t.Fatalf("deep intake leaked %q", marker)
		}
	}
}

func deepTestPolicy() Policy {
	return Policy{RiskPacks: map[string]RiskPack{
		"contract": {
			DeepTargets: []string{"test-fault-injection", "test-e2e-deep"},
		},
		"terminal": {
			DeepTargets: []string{"test-pty-deep", "test-e2e-deep"},
		},
		"fuzz": {
			DeepTargets: []string{"test-fuzz-deep"},
		},
	}}
}

type memoryDeepIntakeStore struct {
	intake *DeepIntake
}

func newMemoryDeepIntakeStore() *memoryDeepIntakeStore {
	return &memoryDeepIntakeStore{}
}

func (store *memoryDeepIntakeStore) Load(string) (DeepIntake, bool, error) {
	if store.intake == nil {
		return DeepIntake{}, false, nil
	}
	return *store.intake, true, nil
}

func (store *memoryDeepIntakeStore) Write(intake DeepIntake) error {
	if store.intake != nil {
		return errDeepIntakeExists
	}
	store.intake = &intake
	return nil
}

func deepGateFor(report DeepReport, target string) *GateEvidence {
	for index := range report.Gates {
		if report.Gates[index].Target == target {
			return &report.Gates[index]
		}
	}
	return nil
}
