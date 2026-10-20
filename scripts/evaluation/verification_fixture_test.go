package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type verificationVerdict struct {
	state       string
	code        string
	passed      int
	failedTests []string
}

// Calibration must distinguish a failed test from a broken test invocation.
func TestVerificationVerdictRejectsInvalidExecution(t *testing.T) {
	tests := []struct {
		name, output, wantState, wantCode string
		exitCode                          int
		interrupted, truncated            bool
	}{
		{name: "compile", output: `{"Action":"fail","Package":"fixture"}`, exitCode: 1, wantState: "incomplete", wantCode: "test_evidence_missing"},
		{name: "zero tests", output: `{"Action":"pass","Package":"fixture"}`, wantState: "incomplete", wantCode: "test_evidence_missing"},
		{name: "skipped", output: "{\"Action\":\"run\",\"Package\":\"fixture\",\"Test\":\"TestCase\"}\n{\"Action\":\"skip\",\"Package\":\"fixture\",\"Test\":\"TestCase\"}\n{\"Action\":\"pass\",\"Package\":\"fixture\"}", wantState: "incomplete", wantCode: "test_evidence_missing"},
		{name: "malformed", output: `not-json`, exitCode: 1, wantState: "incomplete", wantCode: "test_events_invalid"},
		{name: "timeout", output: testEvents("fail"), exitCode: 1, interrupted: true, wantState: "incomplete", wantCode: "execution_interrupted"},
		{name: "truncated", output: testEvents("fail"), exitCode: 1, truncated: true, wantState: "incomplete", wantCode: "output_truncated"},
		{name: "test failure", output: testEvents("fail"), exitCode: 1, wantState: "failed", wantCode: "test_failed"},
		{name: "test pass", output: testEvents("pass"), wantState: "passed", wantCode: "tests_passed"},
		{name: "exit mismatch", output: testEvents("fail"), wantState: "incomplete", wantCode: "exit_status_mismatch"},
		{name: "unterminated", output: `{"Action":"run","Package":"fixture","Test":"TestCase"}`, exitCode: 1, wantState: "incomplete", wantCode: "test_events_incomplete"},
		{name: "unstarted", output: `{"Action":"fail","Package":"fixture","Test":"TestCase"}`, exitCode: 1, wantState: "incomplete", wantCode: "test_events_invalid"},
		{name: "foreign package", output: strings.ReplaceAll(testEvents("pass"), "fixture", "other"), wantState: "incomplete", wantCode: "test_events_invalid"},
		{name: "duplicate package terminal", output: testEvents("pass") + "\n" + `{"Action":"pass","Package":"fixture"}`, wantState: "incomplete", wantCode: "test_events_invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := verificationTestVerdict([]byte(test.output), test.exitCode, test.interrupted, test.truncated)
			if got.state != test.wantState || got.code != test.wantCode {
				t.Fatalf("verdict = %#v, want %s/%s", got, test.wantState, test.wantCode)
			}
		})
	}
}

func testEvents(terminal string) string {
	return strings.Join([]string{
		`{"Action":"run","Package":"fixture","Test":"TestCase"}`,
		fmt.Sprintf(`{"Action":%q,"Package":"fixture","Test":"TestCase"}`, terminal),
		fmt.Sprintf(`{"Action":%q,"Package":"fixture"}`, terminal),
	}, "\n")
}

func verificationTestVerdict(output []byte, exitCode int, interrupted, truncated bool) verificationVerdict {
	incomplete := func(code string) verificationVerdict { return verificationVerdict{state: "incomplete", code: code} }
	if interrupted {
		return incomplete("execution_interrupted")
	}
	if truncated {
		return incomplete("output_truncated")
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	running := make(map[string]bool)
	packages := make(map[string]string)
	passed, failed := 0, 0
	var failedTests []string
	for {
		var event struct{ Action, Package, Test string }
		err := decoder.Decode(&event)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || event.Action == "" {
			return incomplete("test_events_invalid")
		}
		if event.Test == "" {
			if event.Action == "pass" || event.Action == "fail" {
				if event.Package != "fixture" || packages[event.Package] != "" {
					return incomplete("test_events_invalid")
				}
				packages[event.Package] = event.Action
			}
			continue
		}
		if event.Package != "fixture" {
			return incomplete("test_events_invalid")
		}
		key := event.Package + "/" + event.Test
		switch event.Action {
		case "run":
			if _, found := running[key]; found {
				return incomplete("test_events_invalid")
			}
			running[key] = true
		case "pass", "fail", "skip":
			if !running[key] {
				return incomplete("test_events_invalid")
			}
			running[key] = false
			if event.Action == "pass" {
				passed++
			} else if event.Action == "fail" {
				failed++
				failedTests = append(failedTests, event.Test)
			}
		}
	}
	for _, active := range running {
		if active {
			return incomplete("test_events_incomplete")
		}
	}
	if passed+failed == 0 {
		return incomplete("test_evidence_missing")
	}
	if len(packages) != 1 {
		return incomplete("test_events_incomplete")
	}
	if exitCode != 0 && failed > 0 && packages["fixture"] == "fail" {
		return verificationVerdict{state: "failed", code: "test_failed", passed: passed, failedTests: failedTests}
	}
	if exitCode != 0 || failed > 0 || packages["fixture"] != "pass" {
		return incomplete("exit_status_mismatch")
	}
	return verificationVerdict{state: "passed", code: "tests_passed", passed: passed}
}

type verificationControl struct {
	name, source    string
	correct         bool
	expectedFailure string
}

// The controls are private evaluator inputs. Only fixture/ and task.md belong
// in a live agent workspace; controls/ and grader/ must remain unavailable.
func keyedStoreControls(t *testing.T, root string) []verificationControl {
	t.Helper()
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	mutex := read("controls/mutex.go")
	channel := read("controls/channel.go")
	replace := func(source, old, next string) string {
		if strings.Count(source, old) != 1 {
			t.Fatalf("mutation target must occur once: %q", old)
		}
		return strings.Replace(source, old, next, 1)
	}
	updateHeader := "func (store *Store) Update(key string, transform func([]byte) []byte) []byte {\n\t"
	updateLock := updateHeader + "entry := store.find(key)\n\tentry.mu.Lock()\n\tdefer entry.mu.Unlock()"
	coarse := replace(mutex, "type Store struct {", "type Store struct {\n\tglobal sync.Mutex")
	// Add serialization without removing the original per-key synchronization.
	coarse = replace(coarse, updateHeader, updateHeader+"store.global.Lock()\n\tdefer store.global.Unlock()\n\t")
	noExclusion := replace(mutex, updateLock, updateHeader+"entry := store.find(key)")
	leak := replace(mutex, updateLock, updateHeader+"entry := store.find(key)\n\tentry.mu.Lock()")
	leak = replace(leak, "entry.value = bytes.Clone(next)", "entry.value = bytes.Clone(next)\n\tentry.mu.Unlock()")
	return []verificationControl{
		{name: "mutex", source: mutex, correct: true},
		{name: "channel", source: channel, correct: true},
		{name: "coarse-lock", source: coarse, expectedFailure: "TestDifferentKeyProgress"},
		{name: "no-exclusion", source: noExclusion, expectedFailure: "TestSameKeyExclusionAndQueue"},
		{name: "borrowed-input", source: replace(mutex, "transform(bytes.Clone(entry.value))", "transform(entry.value)"), expectedFailure: "TestPanicRollbackAndRelease"},
		{name: "borrowed-transform-result", source: replace(mutex, "entry.value = bytes.Clone(next)", "entry.value = next"), expectedFailure: "TestTransformResultDetached"},
		{name: "borrowed-update-return", source: replace(mutex, "entry.value = bytes.Clone(next)\n\treturn bytes.Clone(entry.value)", "entry.value = bytes.Clone(next)\n\treturn entry.value"), expectedFailure: "TestUpdateReturnDetached"},
		{name: "borrowed-read-return", source: replace(mutex, "defer entry.mu.Unlock()\n\treturn bytes.Clone(entry.value)", "defer entry.mu.Unlock()\n\treturn entry.value"), expectedFailure: "TestGetReturnDetached"},
		{name: "panic-lock-leak", source: leak, expectedFailure: "TestPanicRollbackAndRelease"},
		{name: "initial-buggy-fixture", source: read("fixture/store.go"), expectedFailure: "TestDifferentKeyProgress"},
	}
}

func TestKeyedStoreVerificationCalibration(t *testing.T) {
	if os.Getenv("EINO_VERIFICATION_CALIBRATION") != "1" {
		t.Skip("opt-in offline Go fixture calibration; no model calls")
	}
	root, err := commandRoot()
	if err != nil {
		t.Fatal(err)
	}
	root = filepath.Join(root, "testdata", "keyed-store-v1")
	controls := keyedStoreControls(t, root)
	for _, control := range controls {
		t.Run(control.name, func(t *testing.T) {
			verdict := runKeyedStoreTests(t, root, control.source, true)
			want := "failed"
			if control.correct {
				want = "passed"
			}
			if verdict.state != want {
				t.Fatalf("calibration verdict = %#v, want %s", verdict, want)
			}
			if control.correct && verdict.passed != 7 {
				t.Fatalf("correct control executed %d passing tests, want 7", verdict.passed)
			}
			if !control.correct && !strings.Contains("\n"+strings.Join(verdict.failedTests, "\n")+"\n", "\n"+control.expectedFailure+"\n") {
				t.Fatalf("control failed tests %v, missing targeted check %s", verdict.failedTests, control.expectedFailure)
			}
			t.Logf("state=%s passing_tests=%d failing_tests=%v", verdict.state, verdict.passed, verdict.failedTests)
		})
		if t.Failed() {
			// Reject a broken oracle before crediting any mutant detection.
			return
		}
	}
	t.Run("final-state-only-misses-coarse-lock", func(t *testing.T) {
		verdict := runKeyedStoreTests(t, root, controls[2].source, false)
		if verdict.state != "passed" || verdict.passed != 1 {
			t.Fatalf("weak public tests should miss complete serialization: %#v", verdict)
		}
	})
}

func TestKeyedStoreControlSources(t *testing.T) {
	root, err := commandRoot()
	if err != nil {
		t.Fatal(err)
	}
	root = filepath.Join(root, "testdata", "keyed-store-v1")
	for _, control := range keyedStoreControls(t, root) {
		t.Run(control.name, func(t *testing.T) {
			if _, err := parser.ParseFile(token.NewFileSet(), "store.go", control.source, parser.AllErrors); err != nil {
				t.Fatalf("invalid control source: %v", err)
			}
		})
	}
}

func runKeyedStoreTests(t *testing.T, sourceRoot, source string, includeOracle bool) verificationVerdict {
	t.Helper()
	directory := t.TempDir()
	for _, name := range []string{"go.mod", "store_test.go"} {
		data, err := os.ReadFile(filepath.Join(sourceRoot, "fixture", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(directory, "store.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	if includeOracle {
		data, err := os.ReadFile(filepath.Join(sourceRoot, "grader", "contract_test.go"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "contract_test.go"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	command := exec.Command("go", "test", "-race", "-json", "-count=1", "-timeout=8s", "./...")
	command.Dir = directory
	command.Env = isolatedVerificationGoEnv()
	stdout, stderr := &boundedBuffer{limit: 128 << 10}, &boundedBuffer{limit: 32 << 10}
	command.Stdout, command.Stderr = stdout, stderr
	runErr := runOwnedCommand(ctx, command)
	interrupted := runErr != nil && errorCode(runErr) != "process_failed"
	code := 0
	if runErr != nil {
		var exited *exec.ExitError
		if errors.As(runErr, &exited) {
			code = exited.ExitCode()
			interrupted = interrupted || code < 0
		} else {
			interrupted = true
		}
	}
	verdict := verificationTestVerdict(stdout.Bytes(), code, interrupted, stdout.truncated || stderr.truncated)
	if verdict.state == "incomplete" {
		t.Logf("fixture invocation did not yield a test verdict: %s", verdict.code)
	}
	return verdict
}

func isolatedVerificationGoEnv() []string {
	// This executes only repository-owned calibration code, not submitted agent
	// tests. It is process-owned, not an OS sandbox for untrusted candidates.
	allowed := []string{"PATH", "HOME", "USERPROFILE", "SYSTEMROOT", "SystemRoot", "TEMP", "TMP", "TMPDIR", "GOCACHE", "GOROOT", "CC", "CXX"}
	env := make([]string, 0, len(allowed)+4)
	for _, key := range allowed {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return append(env, "GOTOOLCHAIN=local", "GOWORK=off", "GOPROXY=off", "GOSUMDB=off")
}
