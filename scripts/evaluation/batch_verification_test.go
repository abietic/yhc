package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func batchControls(t *testing.T, root string) []verificationControl {
	t.Helper()
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		return strings.ReplaceAll(string(data), "\r\n", "\n")
	}
	replace := func(source, old, next string) string {
		if strings.Count(source, old) != 1 {
			t.Fatalf("batch mutation target must occur once: %q", old)
		}
		return strings.Replace(source, old, next, 1)
	}
	pool := read("controls/pool.go")
	unordered := replace(pool, "out := make([]int, len(input))", "out := make([]int, 0, len(input))\n\tvar results sync.Mutex")
	unordered = replace(unordered, "out[i] = value", "results.Lock()\n\t\t\t\tout = append(out, value)\n\t\t\t\tresults.Unlock()")
	early := replace(pool, "\twg.Wait()", `	joined := make(chan struct{})
	go func() { wg.Wait(); close(joined) }()
	select {
	case <-joined:
	case <-ctx.Done():
		return nil, firstErr
	}`)
	return []verificationControl{
		{name: "pool", source: pool, correct: true},
		{name: "semaphore", source: read("controls/semaphore.go"), correct: true},
		{name: "sequential", source: read("fixture/batch.go"), expectedFailure: "TestBatchConcurrentProgressAndOrder"},
		{name: "unbounded", source: replace(pool, "workers := min(limit, len(input))", "workers := len(input)"), expectedFailure: "TestBatchLimit"},
		{name: "completion-order", source: unordered, expectedFailure: "TestBatchConcurrentProgressAndOrder"},
		{name: "no-cancel", source: replace(pool, "firstErr = err; cancel()", "firstErr = err"), expectedFailure: "TestBatchErrorCancelsAndJoins"},
		{name: "early-return", source: early, expectedFailure: "TestBatchErrorCancelsAndJoins"},
		{name: "discard-error", source: replace(pool, "return nil, firstErr", "return nil, context.Canceled"), expectedFailure: "TestBatchErrorCancelsAndJoins"},
		{name: "mutate-input", source: replace(pool, "return out, nil", "for i := range input { input[i] = 0 }; return out, nil"), expectedFailure: "TestBatchInputPreserved"},
	}
}

func TestBoundedMapControlSources(t *testing.T) {
	root, err := commandRoot()
	if err != nil {
		t.Fatal(err)
	}
	for _, control := range batchControls(t, filepath.Join(root, "testdata", "bounded-map-v1")) {
		if _, err := parser.ParseFile(token.NewFileSet(), "batch.go", control.source, parser.AllErrors); err != nil {
			t.Fatalf("control %s is invalid: %v", control.name, err)
		}
	}
}

func TestBoundedMapVerificationCalibration(t *testing.T) {
	if os.Getenv("EINO_VERIFICATION_CALIBRATION") != "1" {
		t.Skip("opt-in trusted offline concurrency calibration; no model calls")
	}
	root, err := commandRoot()
	if err != nil {
		t.Fatal(err)
	}
	root = filepath.Join(root, "testdata", "bounded-map-v1")
	for _, control := range batchControls(t, root) {
		t.Run(control.name, func(t *testing.T) {
			verdict := runVerificationFixtureTests(t, root, "batch.go", "batch_test.go", control.source, true)
			if control.correct {
				if verdict.state != "passed" || verdict.passed != 7 {
					t.Fatalf("correct control rejected: %#v", verdict)
				}
			} else if verdict.state != "failed" || !slices.Contains(verdict.failedTests, control.expectedFailure) {
				t.Fatalf("missing targeted defect detection: %#v, want %s", verdict, control.expectedFailure)
			}
			t.Logf("state=%s passing_tests=%d failing_tests=%v", verdict.state, verdict.passed, verdict.failedTests)
		})
		if t.Failed() {
			return
		}
	}
	t.Run("public-example-misses-sequential-execution", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join(root, "fixture", "batch.go"))
		if err != nil {
			t.Fatal(err)
		}
		verdict := runVerificationFixtureTests(t, root, "batch.go", "batch_test.go", string(data), false)
		if verdict.state != "passed" || verdict.passed != 1 {
			t.Fatalf("weak public example did not pass: %#v", verdict)
		}
	})
}
