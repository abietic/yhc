package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

var releaseAndDebugTargets = []string{
	"build/linux-amd64/yhc",
	"build/darwin-amd64/yhc",
	"build/darwin-arm64/yhc",
	"build/windows-amd64/yhc.exe",
	"build/debug/yhc",
	"build/evaluation/yhc" + evaluationExecutableSuffix(),
}

var desktopBackendTargets = []string{
	"build/desktop/darwin-amd64/yhc",
	"build/desktop/darwin-arm64/yhc",
	"build/desktop/linux-amd64/yhc",
	"build/desktop/windows-amd64/yhc.exe",
}

func evaluationExecutableSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func TestYHCModuleCommandAndArtifactIdentity(t *testing.T) {
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	repositoryRoot := filepath.Clean(filepath.Join(workingDir, ".."))
	legacyCommand := "eino" + "-agent"

	goMod, err := os.ReadFile(filepath.Join(repositoryRoot, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	if firstLine := strings.SplitN(string(goMod), "\n", 2)[0]; firstLine != "module github.com/abietic/yhc" {
		t.Errorf("module declaration = %q", firstLine)
	}

	list := exec.Command(
		"go", "list", "-f",
		`{{if eq .Name "main"}}{{.ImportPath}}{{end}}`,
		"./cmd/...",
	)
	list.Dir = repositoryRoot
	output, err := list.CombinedOutput()
	if err != nil {
		t.Fatalf("list command packages: %v: %s", err, output)
	}
	var mainPackages []string
	for _, line := range strings.Split(string(output), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			mainPackages = append(mainPackages, line)
		}
	}
	if want := []string{"github.com/abietic/yhc/cmd/yhc"}; !slices.Equal(mainPackages, want) {
		t.Errorf("main command packages = %v, want %v", mainPackages, want)
	}

	if _, err := os.Stat(filepath.Join(repositoryRoot, "cmd", "yhc")); err != nil {
		t.Errorf("canonical command directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repositoryRoot, "cmd", legacyCommand)); !os.IsNotExist(err) {
		t.Errorf("legacy command directory remains: %v", err)
	}

	wantTargets := []string{
		"build/linux-amd64/yhc",
		"build/darwin-amd64/yhc",
		"build/darwin-arm64/yhc",
		"build/windows-amd64/yhc.exe",
		"build/debug/yhc",
		"build/evaluation/yhc" + evaluationExecutableSuffix(),
	}
	if !slices.Equal(releaseAndDebugTargets, wantTargets) {
		t.Errorf("release and debug targets = %v, want %v", releaseAndDebugTargets, wantTargets)
	}
	if _, err := exec.LookPath("make"); err != nil {
		t.Fatalf("make is required to validate canonical build targets: %v", err)
	}
	makefile := filepath.Join(repositoryRoot, "Makefile")
	fixtureRoot := t.TempDir()
	now := time.Now()
	createSourceFixture(t, fixtureRoot, "cmd", now.Add(-2*time.Hour))
	createArtifacts(t, fixtureRoot, now.Add(time.Hour))
	for _, target := range wantTargets {
		if code := runMakeQuestion(t, fixtureRoot, makefile, target); code != 0 {
			t.Errorf("canonical make target %s exited %d", target, code)
		}
	}

	for _, name := range []string{
		"Makefile",
		filepath.Join("quality", "iteration.yaml"),
		filepath.Join(".github", "workflows", "ci.yml"),
	} {
		content, err := os.ReadFile(filepath.Join(repositoryRoot, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		legacyModule := "github.com/yuhaichuan/" + legacyCommand
		if strings.Contains(string(content), legacyModule) ||
			strings.Contains(string(content), "cmd/"+legacyCommand) ||
			strings.Contains(string(content), "build/linux-amd64/"+legacyCommand) ||
			strings.Contains(string(content), "build/darwin-amd64/"+legacyCommand) ||
			strings.Contains(string(content), "build/darwin-arm64/"+legacyCommand) ||
			strings.Contains(string(content), "build/windows-amd64/"+legacyCommand+".exe") {
			t.Errorf("%s retains a legacy module, command, or release artifact", name)
		}
	}

	ignore, err := os.ReadFile(filepath.Join(repositoryRoot, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	for _, pattern := range []string{"**/.yhc", "**/.eino-agent", "/yhc"} {
		if !strings.Contains(string(ignore), pattern) {
			t.Errorf(".gitignore does not preserve %q", pattern)
		}
	}
}

func TestBuildDependencies(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Fatalf("make is required to validate build dependencies: %v", err)
	}

	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	makefile := filepath.Clean(filepath.Join(workingDir, "..", "Makefile"))
	if _, err := os.Stat(makefile); err != nil {
		t.Fatalf("locate repository Makefile: %v", err)
	}

	for _, sourceRoot := range []string{"cmd", "engine", "internal", "server", "tools"} {
		t.Run(sourceRoot, func(t *testing.T) {
			dir := t.TempDir()
			now := time.Now()
			source := createSourceFixture(t, dir, sourceRoot, now.Add(-2*time.Hour))
			createArtifacts(t, dir, now.Add(time.Hour))

			assertTargetsState(t, dir, makefile, 0)

			createArtifacts(t, dir, now.Add(-time.Hour))
			if err := os.WriteFile(source, []byte("package fixture\n\nconst updated = true\n"), 0o644); err != nil {
				t.Fatalf("update %s source: %v", sourceRoot, err)
			}
			assertTargetsState(t, dir, makefile, 1)
		})
	}
}

func TestDesktopBackendsRebuildWhenBuildIdentityChanges(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Makefile uses a POSIX shell")
	}
	if _, err := exec.LookPath("make"); err != nil {
		t.Fatalf("make is required to validate desktop build identity: %v", err)
	}

	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	makefile := filepath.Clean(filepath.Join(workingDir, "..", "Makefile"))
	fixtureRoot := t.TempDir()
	createSourceFixture(t, fixtureRoot, "cmd", time.Now().Add(-time.Hour))
	if err := os.MkdirAll(filepath.Join(fixtureRoot, "desktop"), 0o755); err != nil {
		t.Fatalf("mkdir desktop fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixtureRoot, "desktop", "package.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write desktop package fixture: %v", err)
	}

	journal := filepath.Join(fixtureRoot, "go-build.log")
	fakeGo := filepath.Join(fixtureRoot, "go")
	const fakeGoScript = `#!/bin/sh
set -eu
if [ "$#" -eq 2 ] && [ "$1" = "env" ]; then
	case "$2" in
		GOPATH) printf '%s\n' "${YHC_TEST_GOPATH:?}" ;;
		GOHOSTOS) printf '%s\n' "darwin" ;;
		GOHOSTARCH) printf '%s\n' "arm64" ;;
		*) exit 2 ;;
	esac
	exit 0
fi
output=""
previous=""
for argument in "$@"; do
	if [ "$previous" = "-o" ]; then
		output="$argument"
		break
	fi
	previous="$argument"
done
test -n "$output"
printf '%s\n' "$*" >>"${YHC_TEST_JOURNAL:?}"
mkdir -p "$(dirname "$output")"
printf '%s\n' artifact >"$output"
`
	if err := os.WriteFile(fakeGo, []byte(fakeGoScript), 0o700); err != nil {
		t.Fatalf("write fake go: %v", err)
	}

	runBuilds := func(commit, buildTime, modified string) {
		t.Helper()
		args := []string{
			"--no-print-directory",
			"--file", makefile,
			"GO=" + fakeGo,
			"NODE=true",
			"DESKTOP_VERSION=0.1.0",
			"BUILD_COMMIT=" + commit,
			"BUILD_TIME=" + buildTime,
			"BUILD_MODIFIED=" + modified,
		}
		args = append(args, desktopBackendTargets...)
		cmd := exec.Command("make", args...)
		cmd.Dir = fixtureRoot
		cmd.Env = append(os.Environ(),
			"YHC_TEST_GOPATH="+fixtureRoot,
			"YHC_TEST_JOURNAL="+journal,
		)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build desktop backends: %v: %s", err, output)
		}
	}

	firstCommit := strings.Repeat("1", 40)
	secondCommit := strings.Repeat("2", 40)
	runBuilds(firstCommit, "2026-08-20T04:36:10+08:00", "true")
	runBuilds(secondCommit, "2026-08-24T01:27:00+08:00", "false")

	data, err := os.ReadFile(journal)
	if err != nil {
		t.Fatalf("read build journal: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if got, want := len(lines), 2*len(desktopBackendTargets); got != want {
		t.Fatalf("desktop backend build count = %d, want %d; journal:\n%s", got, want, data)
	}
	for _, line := range lines[len(desktopBackendTargets):] {
		if !strings.Contains(line, "buildinfo.Commit="+secondCommit) ||
			!strings.Contains(line, "buildinfo.BuildTime=2026-08-24T01:27:00+08:00") ||
			!strings.Contains(line, "buildinfo.Modified=false") {
			t.Fatalf("second desktop build retained stale identity: %s", line)
		}
	}
}

func TestEvaluationBaselineRemainsOptIn(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Fatalf("make is required to validate evaluation isolation: %v", err)
	}
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	repositoryRoot := filepath.Clean(filepath.Join(workingDir, ".."))
	makefile := filepath.Join(repositoryRoot, "Makefile")
	cmd := exec.Command(
		"make", "--no-print-directory", "--file", makefile,
		"--dry-run", "--always-make",
		"GO=true", "GOFM=true", "GOLINT=true", "GOTEST=true", "DLV=true",
		"verify",
	)
	cmd.Dir = repositoryRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("render verify commands: %v: %s", err, out)
	}
	if strings.Contains(string(out), "scripts/evaluation") || strings.Contains(string(out), "eval-baseline") {
		t.Fatalf("verify admitted the opt-in evaluation target: %s", out)
	}

	workflowRoot := filepath.Join(repositoryRoot, ".github", "workflows")
	if err := filepath.Walk(workflowRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(data), "eval-baseline") || strings.Contains(string(data), "scripts/evaluation") {
			t.Fatalf("required workflow admitted opt-in evaluation through %s", filepath.Base(path))
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect required workflows: %v", err)
	}
}

func TestChangeEvidenceReadyMakeContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Makefile uses a POSIX shell")
	}
	if _, err := exec.LookPath("make"); err != nil {
		t.Fatalf("make is required to validate iteration evidence: %v", err)
	}

	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	repositoryRoot := filepath.Clean(filepath.Join(workingDir, ".."))
	makefile := filepath.Join(repositoryRoot, "Makefile")
	fixtureRoot := t.TempDir()
	journal := filepath.Join(fixtureRoot, "go-argv.log")
	fakeGo := filepath.Join(fixtureRoot, "go")
	const fakeGoScript = `#!/bin/sh
set -eu
if [ "$#" -eq 2 ] && [ "$1" = "env" ]; then
	case "$2" in
		GOPATH) printf '%s\n' "${EINO_TEST_GOPATH:?}" ;;
		GOHOSTOS) printf '%s\n' "darwin" ;;
		*) exit 2 ;;
	esac
	exit 0
fi
printf '%s\n' "$*" >>"${EINO_TEST_JOURNAL:?}"
exit "${EINO_TEST_GO_EXIT:-0}"
`
	if err := os.WriteFile(fakeGo, []byte(fakeGoScript), 0o700); err != nil {
		t.Fatalf("write fake go: %v", err)
	}

	runTarget := func(exitCode, sliceID string) ([]byte, error) {
		t.Helper()
		if err := os.WriteFile(journal, nil, 0o600); err != nil {
			t.Fatalf("reset journal: %v", err)
		}
		cmd := exec.Command(
			"make", "--no-print-directory", "--file", makefile,
			"GO="+fakeGo,
			"ITERATION_BASE=upstream/main",
			"ITERATION_FORMAT=json",
			"ITERATION_SLICE_ID="+sliceID,
			"change-evidence-ready",
		)
		cmd.Dir = repositoryRoot
		cmd.Env = append(os.Environ(),
			"EINO_TEST_GOPATH="+fixtureRoot,
			"EINO_TEST_JOURNAL="+journal,
			"EINO_TEST_GO_EXIT="+exitCode,
		)
		return cmd.CombinedOutput()
	}

	if out, err := runTarget("0", "s5-f0"); err != nil {
		t.Fatalf("ready evidence target failed: %v: %s", err, out)
	}
	argv, err := os.ReadFile(journal)
	if err != nil {
		t.Fatalf("read fake go journal: %v", err)
	}
	want := "run ./scripts/iteration --base upstream/main --head HEAD --format json --slice-id s5-f0 evidence --require-ready\n"
	if got := string(argv); got != want {
		t.Fatalf("change-evidence-ready argv = %q, want %q", got, want)
	}

	if out, err := runTarget("0", ""); err != nil {
		t.Fatalf("ready evidence target without slice failed: %v: %s", err, out)
	}
	argv, err = os.ReadFile(journal)
	if err != nil {
		t.Fatalf("read fake go journal without slice: %v", err)
	}
	want = "run ./scripts/iteration --base upstream/main --head HEAD --format json evidence --require-ready\n"
	if got := string(argv); got != want {
		t.Fatalf("change-evidence-ready argv without slice = %q, want %q", got, want)
	}

	if out, err := runTarget("19", "s5-f0"); err == nil {
		t.Fatalf("non-ready evidence target succeeded: %s", out)
	}
}

func createSourceFixture(t *testing.T, dir, sourceRoot string, dependencyTime time.Time) string {
	t.Helper()

	for _, root := range []string{"cmd", "engine", "internal", "server", "tools"} {
		if err := os.MkdirAll(filepath.Join(dir, root), 0o755); err != nil {
			t.Fatalf("mkdir source root %s: %v", root, err)
		}
	}

	source := filepath.Join(dir, sourceRoot, "fixture", "source.go")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatalf("mkdir source fixture: %v", err)
	}
	if err := os.WriteFile(source, []byte("package fixture\n"), 0o644); err != nil {
		t.Fatalf("write source fixture: %v", err)
	}

	for _, path := range []string{filepath.Join(dir, "go.mod"), filepath.Join(dir, "go.sum")} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte("fixture\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		if err := os.Chtimes(path, dependencyTime, dependencyTime); err != nil {
			t.Fatalf("set mtime for %s: %v", path, err)
		}
	}

	return source
}

func createArtifacts(t *testing.T, dir string, mtime time.Time) {
	t.Helper()
	for _, target := range releaseAndDebugTargets {
		path := filepath.Join(dir, target)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", target, err)
		}
		if err := os.WriteFile(path, []byte("artifact\n"), 0o755); err != nil {
			t.Fatalf("write artifact %s: %v", target, err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatalf("set mtime for %s: %v", target, err)
		}
	}
}

func assertTargetsState(t *testing.T, dir, makefile string, wantCode int) {
	t.Helper()
	for _, target := range releaseAndDebugTargets {
		if code := runMakeQuestion(t, dir, makefile, target); code != wantCode {
			t.Fatalf("make --question %s exited %d, want %d", target, code, wantCode)
		}
	}
}

func runMakeQuestion(t *testing.T, dir, makefile, target string) int {
	t.Helper()
	args := []string{
		"--no-print-directory",
		"--file", makefile,
		"--question",
		"GO=go",
		"GOFM=gofumpt",
		"GOLINT=golangci-lint",
		"GOTEST=gotestsum",
		"DLV=dlv",
		target,
	}
	cmd := exec.Command("make", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("make --question failed unexpectedly: %v, output: %s", err, string(out))
	}
	return exitErr.ExitCode()
}
