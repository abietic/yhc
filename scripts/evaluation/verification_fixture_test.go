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
		// These are repository-owned source templates, not submitted tests.
		return strings.ReplaceAll(string(data), "\r\n", "\n")
	}
	mutex := read("controls/mutex.go")
	channel := read("controls/channel.go")
	snapshot := read("controls/snapshot.go")
	replace := func(source, old, next string) string {
		if strings.Count(source, old) != 1 {
			t.Fatalf("mutation target must occur once: %q", old)
		}
		return strings.Replace(source, old, next, 1)
	}
	updateHeader := "func (store *Store) Update(key string, transform func([]byte) []byte) []byte {\n\t"
	updateLock := updateHeader + "entry := store.find(key)\n\tentry.mu.Lock()\n\tdefer entry.mu.Unlock()"
	getHeader := "func (store *Store) Get(key string) []byte {\n\t"
	queuedLookup := "store.mu.Lock()\n\tentry := store.items[key]\n\tif entry == nil {\n\t\tentry = &item{}\n\t\tstore.items[key] = entry\n\t}\n\tentry.mu.Lock()\n\tstore.mu.Unlock()\n\tdefer entry.mu.Unlock()"
	coarse := replace(mutex, "type Store struct {", "type Store struct {\n\tglobal sync.Mutex")
	// Add serialization without removing the original per-key synchronization.
	coarse = replace(coarse, updateHeader, updateHeader+"store.global.Lock()\n\tdefer store.global.Unlock()\n\t")
	noExclusion := replace(mutex, updateLock, updateHeader+"entry := store.find(key)")
	leak := replace(mutex, updateLock, updateHeader+"entry := store.find(key)\n\tentry.mu.Lock()")
	// Keep normal-path reads inside the key lock. Only the panic path should
	// leak it; reading entry.value after unlocking would add another fault.
	leak = replace(leak, "entry.value = bytes.Clone(next)\n\treturn bytes.Clone(entry.value)", "entry.value = bytes.Clone(next)\n\tresult := bytes.Clone(entry.value)\n\tentry.mu.Unlock()\n\treturn result")
	earlyAck := replace(mutex, `"sync"`, "\"sync\"\n\t\"time\"")
	earlyAck = replace(earlyAck, updateHeader, "func (store *Store) update(key string, transform func([]byte) []byte) []byte {\n\t")
	earlyAck += `
func (store *Store) Update(key string, transform func([]byte) []byte) []byte {
	type result struct { value []byte; panicked bool; cause any }
	done := make(chan result, 1)
	go func() {
		out := result{panicked: true}
		defer func() {
			if out.panicked { out.cause = recover() }
			done <- out
		}()
		out.value = store.update(key, transform)
		out.panicked = false
	}()
	select {
	case out := <-done:
		if out.panicked { panic(out.cause) }
		return out.value
	case <-time.After(500 * time.Millisecond):
		return nil
	}
}
`
	earlyPublish := replace(snapshot, "previous := bytes.Clone(entry.value)\n\tstore.mu.Unlock()\n\tnext := transform(previous)", `previous := bytes.Clone(entry.value)
	argument := bytes.Clone(previous)
	entry.value = argument
	store.mu.Unlock()
	defer func() {
		if cause := recover(); cause != nil {
			store.mu.Lock()
			entry.value = previous
			store.mu.Unlock()
			panic(cause)
		}
	}()
	next := transform(argument)`)
	return []verificationControl{
		{name: "mutex", source: mutex, correct: true},
		{name: "channel", source: channel, correct: true},
		{name: "snapshot", source: snapshot, correct: true},
		{name: "coarse-lock", source: coarse, expectedFailure: "TestDifferentKeyProgress"},
		{name: "queued-update-convoy", source: replace(mutex, updateLock, updateHeader+queuedLookup), expectedFailure: "TestSameKeyExclusionAndQueue"},
		{name: "queued-read-convoy", source: replace(mutex, getHeader+"entry := store.find(key)\n\tentry.mu.Lock()\n\tdefer entry.mu.Unlock()", getHeader+queuedLookup), expectedFailure: "TestSameKeyExclusionAndQueue"},
		{name: "no-exclusion", source: noExclusion, expectedFailure: "TestSameKeyExclusionAndQueue"},
		{name: "borrowed-input", source: replace(mutex, "transform(bytes.Clone(entry.value))", "transform(entry.value)"), expectedFailure: "TestPanicRollbackAndRelease"},
		{name: "borrowed-transform-result", source: replace(mutex, "entry.value = bytes.Clone(next)", "entry.value = next"), expectedFailure: "TestTransformResultDetached"},
		{name: "borrowed-update-return", source: replace(mutex, "entry.value = bytes.Clone(next)\n\treturn bytes.Clone(entry.value)", "entry.value = bytes.Clone(next)\n\treturn entry.value"), expectedFailure: "TestUpdateReturnDetached"},
		{name: "borrowed-read-return", source: replace(mutex, "defer entry.mu.Unlock()\n\treturn bytes.Clone(entry.value)", "defer entry.mu.Unlock()\n\treturn entry.value"), expectedFailure: "TestGetReturnDetached"},
		{name: "panic-lock-leak", source: leak, expectedFailure: "TestPanicRollbackAndRelease"},
		{name: "early-update-ack", source: earlyAck, expectedFailure: "TestUpdateWaitsForTransform"},
		{name: "early-input-publication", source: earlyPublish, expectedFailure: "TestBlockedTransformDoesNotPublishInput"},
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
			if control.correct && verdict.passed != 9 {
				t.Fatalf("correct control executed %d passing tests, want 9", verdict.passed)
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
		var source string
		for _, control := range controls {
			if control.name == "coarse-lock" {
				source = control.source
			}
		}
		if source == "" {
			t.Fatal("coarse-lock control is missing")
		}
		verdict := runKeyedStoreTests(t, root, source, false)
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
	crlfRoot := t.TempDir()
	for _, relative := range []string{"controls/mutex.go", "controls/channel.go", "controls/snapshot.go", "fixture/store.go"} {
		data, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(crlfRoot, relative)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		lf := bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
		if err := os.WriteFile(target, bytes.ReplaceAll(lf, []byte("\n"), []byte("\r\n")), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	expected := keyedStoreControls(t, root)
	for _, layout := range []struct{ name, root string }{{"checkout", root}, {"CRLF", crlfRoot}} {
		t.Run(layout.name, func(t *testing.T) {
			for index, control := range keyedStoreControls(t, layout.root) {
				t.Run(control.name, func(t *testing.T) {
					if control != expected[index] {
						t.Fatal("line endings changed the control source or expectations")
					}
					if _, err := parser.ParseFile(token.NewFileSet(), "store.go", control.source, parser.AllErrors); err != nil {
						t.Fatalf("invalid control source: %v", err)
					}
				})
			}
		})
	}
}

func TestKeyedStorePanicLeakControlNormalPath(t *testing.T) {
	root, err := commandRoot()
	if err != nil {
		t.Fatal(err)
	}
	root = filepath.Join(root, "testdata", "keyed-store-v1")
	var source string
	for _, control := range keyedStoreControls(t, root) {
		if control.name == "panic-lock-leak" {
			source = control.source
		}
	}
	const unlock = "\tentry.mu.Unlock()\n"
	if strings.Count(source, unlock) != 1 {
		t.Fatal("panic control must have one explicit normal-path unlock")
	}
	// Pause after unlocking so a later update deterministically replaces the
	// stored value before this update returns. Its own result must be preserved.
	source = strings.Replace(source, unlock, unlock+"\tkeyedStoreControlAfterUnlock()\n", 1)
	verdict := runKeyedStoreTests(t, root, source, false, keyedStoreNormalReturnProbe)
	if verdict.state != "passed" || verdict.passed != 2 {
		t.Fatalf("panic-only control broke normal-path results: %#v", verdict)
	}
}

const keyedStoreNormalReturnProbe = `package fixture

import (
	"bytes"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var keyedStoreControlAfterUnlock = func() {}

func TestNormalUpdateKeepsItsOwnResult(t *testing.T) {
	store := NewStore()
	paused, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var first atomic.Bool
	keyedStoreControlAfterUnlock = func() {
		if first.CompareAndSwap(false, true) {
			close(paused)
			<-release
		}
	}
	done := make(chan []byte, 1)
	go func() {
		done <- store.Update("one", func([]byte) []byte { return []byte("first") })
	}()
	select {
	case <-paused:
	case <-time.After(2 * time.Second):
		t.Fatal("first update did not reach the unlock barrier")
	}
	if got := store.Update("one", func([]byte) []byte { return []byte("second") }); !bytes.Equal(got, []byte("second")) {
		t.Fatalf("second update returned %q, want second", got)
	}
	unblock()
	select {
	case got := <-done:
		if !bytes.Equal(got, []byte("first")) {
			t.Fatalf("first update returned %q, want its own transform result", got)
		}
		got[0] = 'X'
	case <-time.After(2 * time.Second):
		t.Fatal("first update did not finish after release")
	}
	if got := store.Get("one"); !bytes.Equal(got, []byte("second")) {
		t.Fatalf("stored value = %q, want detached second result", got)
	}
}
`

func runKeyedStoreTests(t *testing.T, sourceRoot, source string, includeOracle bool, probes ...string) verificationVerdict {
	return runVerificationFixtureTests(t, sourceRoot, "store.go", "store_test.go", source, includeOracle, probes...)
}

// Only trusted repository-owned calibration sources may reach this runner.
// This process owner is not a sandbox for model-generated submissions.
func runVerificationFixtureTests(t *testing.T, sourceRoot, sourceName, publicTests, source string, includeOracle bool, probes ...string) verificationVerdict {
	t.Helper()
	directory := t.TempDir()
	for _, name := range []string{"go.mod", publicTests} {
		data, err := os.ReadFile(filepath.Join(sourceRoot, "fixture", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(directory, sourceName), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	for index, probe := range probes {
		if err := os.WriteFile(filepath.Join(directory, fmt.Sprintf("probe_%d_test.go", index)), []byte(probe), 0o600); err != nil {
			t.Fatal(err)
		}
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
