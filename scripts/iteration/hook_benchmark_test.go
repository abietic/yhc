package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type benchmarkCall struct {
	name  string
	args  []string
	dir   string
	input []byte
}
type fakeBenchmarkRunner struct {
	archive        []byte
	calls          []benchmarkCall
	failBash       bool
	fixture        string
	rejectBuildDir string
}

func (runner *fakeBenchmarkRunner) Run(_ context.Context, dir string, input []byte, name string, args ...string) ([]byte, error) {
	runner.calls = append(runner.calls, benchmarkCall{name: name, args: append([]string(nil), args...), dir: dir, input: append([]byte(nil), input...)})
	if name == "git" && len(args) > 0 && args[0] == "archive" {
		return runner.archive, nil
	}
	if name == "git" && len(args) > 1 && args[0] == "init" {
		runner.fixture = args[len(args)-1]
	}
	if name == "go" && len(args) > 0 && args[0] == "build" && dir == runner.rejectBuildDir {
		return nil, errors.New("caller build rejected")
	}
	if name == "bash" && runner.failBash {
		return nil, errors.New("hook failure")
	}
	return nil, nil
}

func benchmarkArchive(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	writer := tar.NewWriter(&data)
	if err := writer.WriteHeader(&tar.Header{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{"comment": "fixture"}}); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteHeader(&tar.Header{Name: "quality", Typeflag: tar.TypeDir, Mode: 0o700}); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteHeader(&tar.Header{Name: "quality/iteration.yaml", Mode: 0o600, Size: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(writer, "{}"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteHeader(&tar.Header{Name: ".codex", Typeflag: tar.TypeDir, Mode: 0o700}); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteHeader(&tar.Header{Name: ".codex/hooks", Typeflag: tar.TypeDir, Mode: 0o700}); err != nil {
		t.Fatal(err)
	}
	wrapper := hookWrapper("exec go -C \"$repository_root\" run ./scripts/iteration hook \"$@\"")
	if err := writer.WriteHeader(&tar.Header{Name: ".codex/hooks/iteration.sh", Mode: 0o700, Size: int64(len(wrapper))}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(wrapper); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestHookBenchmarkArchiveAcceptsDirectoriesAndRejectsUnsafeEntries(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "fixture")
	if err := extractBenchmarkArchive(benchmarkArchive(t), destination); err != nil {
		t.Fatalf("extract directory-shaped archive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, ".codex", "hooks", "iteration.sh")); err != nil {
		t.Fatal(err)
	}
	for _, header := range []tar.Header{
		{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "inside"},
		{Name: "../escape", Typeflag: tar.TypeReg, Size: 0},
	} {
		var data bytes.Buffer
		writer := tar.NewWriter(&data)
		if err := writer.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := extractBenchmarkArchive(data.Bytes(), filepath.Join(t.TempDir(), "fixture")); err == nil {
			t.Fatalf("accepted unsafe archive entry %#v", header)
		}
	}
	var duplicate bytes.Buffer
	writer := tar.NewWriter(&duplicate)
	for range 2 {
		if err := writer.WriteHeader(&tar.Header{Name: "directory", Typeflag: tar.TypeDir}); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := extractBenchmarkArchive(duplicate.Bytes(), filepath.Join(t.TempDir(), "fixture")); err == nil {
		t.Fatal("accepted duplicate target")
	}
	if err := validateHookWrappers(hookWrapper("exec same"), hookWrapper("exec same")); err == nil {
		t.Fatal("accepted equivalent wrappers")
	}
	if err := validateHookWrappers([]byte("different"), hookWrapper("exec candidate")); err == nil {
		t.Fatal("accepted changed prefix")
	}
	var oversize bytes.Buffer
	writer = tar.NewWriter(&oversize)
	if err := writer.WriteHeader(&tar.Header{Name: "large", Typeflag: tar.TypeReg, Size: hookBenchmarkMaxFileBytes + 1}); err != nil {
		t.Fatal(err)
	}
	if err := extractBenchmarkArchive(oversize.Bytes(), filepath.Join(t.TempDir(), "fixture")); err == nil {
		t.Fatal("accepted oversized archive entry")
	}
}

func benchmarkClock(values ...int64) func() time.Time {
	index := 0
	return func() time.Time { value := values[index]; index++; return time.UnixMilli(value) }
}

func TestHookBenchmarkReportsDeterministicAggregateAndBuildsOnce(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "credential_SHOULD_NOT_BUILD.go"), []byte("not valid Go"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeBenchmarkRunner{archive: benchmarkArchive(t), rejectBuildDir: root}
	report, err := runHookBenchmark(context.Background(), root, 5, runner, benchmarkClock(0, 1, 10, 13, 20, 25, 30, 37, 40, 49, 50, 52, 60, 64, 70, 78, 80, 91, 90, 105))
	if err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != 1 || report.Runs != 5 || len(report.Modes) != 2 || report.Modes[0].P50Millis != 5 || report.Modes[0].P95Millis != 9 || report.Modes[1].P50Millis != 8 || report.Modes[1].P95Millis != 15 {
		t.Fatalf("report = %#v", report)
	}
	builds, bashCalls := 0, []benchmarkCall{}
	for _, call := range runner.calls {
		if call.name == "go" && len(call.args) > 0 && call.args[0] == "build" {
			builds++
		}
		if call.name == "bash" {
			bashCalls = append(bashCalls, call)
		}
	}
	if builds != 1 || len(bashCalls) != 10 {
		t.Fatalf("builds=%d bash=%d", builds, len(bashCalls))
	}
	for _, call := range runner.calls {
		if call.name == "go" && call.dir != runner.fixture {
			t.Fatalf("candidate build dir = %q, fixture = %q", call.dir, runner.fixture)
		}
	}
	if bashCalls[0].args[0] == bashCalls[5].args[0] || bashCalls[0].args[1] != "session-start" || !bytes.Contains(bashCalls[0].input, []byte(`"hook_event_name":"SessionStart"`)) {
		t.Fatalf("adapter calls = %#v", bashCalls)
	}
}

func TestHookBenchmarkRejectsDirtyTrackedTreeAndLeavesUntrackedUntouched(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "UNTRACKED_HOOK_BENCHMARK_SECRET")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirty := dirtyBenchmarkRunner{fakeBenchmarkRunner: fakeBenchmarkRunner{archive: benchmarkArchive(t)}}
	if _, err := runHookBenchmark(context.Background(), root, 5, &dirty, benchmarkClock()); err == nil {
		t.Fatal("accepted dirty tree")
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "keep" {
		t.Fatalf("untracked file = %q, %v", data, err)
	}
}

type dirtyBenchmarkRunner struct{ fakeBenchmarkRunner }

func (runner *dirtyBenchmarkRunner) Run(ctx context.Context, dir string, input []byte, name string, args ...string) ([]byte, error) {
	if name == "git" && len(args) > 0 && args[0] == "status" {
		return []byte(" M tracked.go\n"), nil
	}
	return runner.fakeBenchmarkRunner.Run(ctx, dir, input, name, args...)
}

func TestHookBenchmarkCleansFixtureAndDoesNotEmitPrivacyMarkers(t *testing.T) {
	const marker = "HOOK_BENCHMARK_SECRET_MARKER"
	runner := &fakeBenchmarkRunner{archive: benchmarkArchive(t), failBash: true}
	if _, err := runHookBenchmark(context.Background(), t.TempDir(), 5, runner, benchmarkClock(0, 1)); err == nil {
		t.Fatal("accepted hook failure")
	}
	if runner.fixture == "" {
		t.Fatal("fixture was not created")
	}
	if _, err := os.Stat(filepath.Dir(runner.fixture)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary fixture remains: %v", err)
	}
	root := filepath.Join(t.TempDir(), marker)
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	deps := dependencies{root: root, benchmarkRunner: &fakeBenchmarkRunner{archive: benchmarkArchive(t)}, benchmarkClock: benchmarkClock(0, 1, 10, 12, 20, 23, 30, 34, 40, 45, 50, 56, 60, 67, 70, 78, 80, 89, 90, 100)}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"hook-benchmark", "--runs", "5", "--format", "json"}, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("run=%d stderr=%q", code, stderr.String())
	}
	var report HookBenchmarkReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil || len(report.Modes) != 2 {
		t.Fatalf("output=%q err=%v", stdout.String(), err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil || len(payload) != 3 || payload["schema_version"] == nil || payload["runs"] == nil || payload["modes"] == nil {
		t.Fatalf("output keys=%v err=%v", payload, err)
	}
	if report.Modes[0].Mode != "wrapper_go_run" || report.Modes[1].Mode != "wrapper_prebuilt_binary" || stderr.Len() != 0 {
		t.Fatalf("report=%#v stderr=%q", report, stderr.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), marker) {
		t.Fatalf("private output: %q %q", stdout.String(), stderr.String())
	}
}

func TestHookBenchmarkCommandGrammar(t *testing.T) {
	deps := dependencies{}
	for _, args := range [][]string{{"hook-benchmark"}, {"hook-benchmark", "--runs", "4", "--format", "json"}, {"hook-benchmark", "--runs", "5", "--format", "markdown"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr, deps); code != 2 {
			t.Fatalf("run(%v)=%d", args, code)
		}
	}
}

func TestHookBenchmarkCommandGrammarDoesNotEchoArguments(t *testing.T) {
	const marker = "CREDENTIAL_SHAPED_RUNS_MARKER"
	var stdout, stderr bytes.Buffer
	if code := run([]string{"hook-benchmark", "--runs", marker, "--format", "json"}, &stdout, &stderr, dependencies{}); code != 2 || stdout.Len() != 0 || strings.Contains(stderr.String(), marker) || strings.TrimSpace(stderr.String()) != "usage: iteration hook-benchmark --runs 5..100 --format json" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
