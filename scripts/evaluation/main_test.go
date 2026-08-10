package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestManifestIsClosedBoundedAndPathSafe(t *testing.T) {
	directory := privateTempDir(t)
	path := filepath.Join(directory, "input.json")
	for _, input := range []string{
		`{"schema_version":1,"unknown":true}`,
		`{"schema_version":1} {}`,
		strings.Repeat(" ", maxStructuredInputBytes+1),
	} {
		if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
			t.Fatal(err)
		}
		var manifest scenarioManifest
		if err := decodeClosedFile(path, &manifest); err == nil {
			t.Fatalf("accepted malformed input %q", input[:min(len(input), 80)])
		}
	}
	for _, unsafe := range []string{"", ".", "..", "../x", "a/../../x", "/tmp/x", `a\b`, "a/../b"} {
		if safeRelativePath(unsafe) {
			t.Fatalf("accepted unsafe path %q", unsafe)
		}
	}
	if !safeRelativePath("greet/decorate.go") {
		t.Fatal("rejected a canonical relative path")
	}
	if err := validateUniqueRelativePaths([]string{"a", "a"}); err == nil {
		t.Fatal("accepted a duplicate path")
	}
	if runtime.GOOS != "windows" {
		target := filepath.Join(directory, "target.json")
		if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(directory, "link.json")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		var manifest scenarioManifest
		if err := decodeClosedFile(link, &manifest); err == nil {
			t.Fatal("accepted a symlinked manifest")
		}
	}
}

func TestScenarioFixtureAndProviderValidation(t *testing.T) {
	sourceRoot, err := commandRoot()
	if err != nil {
		t.Fatal(err)
	}
	manifest, script, err := loadScenario(sourceRoot, supportedScenario)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Execution.ProviderCalls != 3 || len(script.Steps) != 3 {
		t.Fatalf("unexpected scenario contract: %#v %#v", manifest.Execution, script)
	}
	destination := filepath.Join(privateTempDir(t), "repo")
	scenarioRoot := filepath.Join(sourceRoot, "testdata", "localized-write-fix-v1")
	if err := materializeFixture(scenarioRoot, destination, manifest.Fixture); err != nil {
		t.Fatal(err)
	}
	if digest, err := treeDigest(destination); err != nil || digest == "" {
		t.Fatalf("materialized fixture digest=%q err=%v", digest, err)
	}

	badScript := script
	badScript.Steps = append([]providerStep(nil), script.Steps...)
	badScript.Steps[1].Kind = "final"
	if err := validateProviderScript(badScript, manifest); err == nil {
		t.Fatal("accepted provider step order drift")
	}
	badManifest := manifest
	badManifest.Fixture.Files = append([]string(nil), manifest.Fixture.Files...)
	badManifest.Fixture.Files = append(badManifest.Fixture.Files, manifest.Fixture.Files[0])
	badManifest.Fixture.MaxFiles++
	if err := validateManifest(badManifest); err == nil {
		t.Fatal("accepted duplicate fixture declaration")
	}

	fixtureRoot := filepath.Join(privateTempDir(t), "fixture")
	if err := os.MkdirAll(filepath.Join(fixtureRoot, "greet"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixtureRoot, "declared.go"), []byte("package fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixtureRoot, "extra.go"), []byte("package fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := fixtureManifest{Root: "fixture", Files: []string{"declared.go"}, MaxFiles: 1, MaxBytes: 1024}
	if err := materializeFixture(filepath.Dir(fixtureRoot), filepath.Join(filepath.Dir(fixtureRoot), "out"), fixture); errorCode(err) != "fixture_invalid" {
		t.Fatalf("undeclared fixture path error=%v", err)
	}
}

func TestBoundedBuffersAndMalformedEnvelope(t *testing.T) {
	buffer := &boundedBuffer{limit: 3}
	written, err := buffer.Write([]byte("abcdef"))
	if err != nil || written != 6 || string(buffer.Bytes()) != "abc" || !buffer.truncated {
		t.Fatalf("bounded buffer = %q truncated=%v written=%d err=%v", buffer.Bytes(), buffer.truncated, written, err)
	}
	for _, envelope := range []string{
		`{`,
		`{"schema_version":1,"status":"completed","output":"fixed","exit_code":1}`,
		`{"schema_version":1,"status":"completed","output":"fixed","exit_code":0,"unknown":true}`,
		`{"schema_version":1,"status":"completed","output":"fixed","exit_code":0} {}`,
	} {
		if err := validateHeadlessEnvelope([]byte(envelope), "fixed"); err == nil {
			t.Fatalf("accepted malformed envelope %q", envelope)
		}
	}
}

func TestReportPublicationIsPrivateRedactedAndNoReplace(t *testing.T) {
	directory := privateTempDir(t)
	report := sampleReport()
	path := filepath.Join(directory, "report.json")
	if err := publishReport(path, report, []string{"raw-secret"}, publicationOptions{}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > reportMaxBytes {
		t.Fatalf("report info=%v err=%v", info, err)
	}
	if err := publishReport(path, report, nil, publicationOptions{}); errorCode(err) != "report_collision" {
		t.Fatalf("collision error=%v", err)
	}
	if err := publishReport(filepath.Join(directory, "redacted.json"), sampleReportWithCode("raw-secret"), []string{"raw-secret"}, publicationOptions{}); errorCode(err) != "report_redaction_failed" {
		t.Fatalf("redaction error=%v", err)
	}

	racePath := filepath.Join(directory, "race.json")
	err = publishReport(racePath, report, nil, publicationOptions{beforeLink: func(parent string) error {
		target := filepath.Join(parent, filepath.Base(racePath))
		if writeErr := os.WriteFile(target, []byte("attacker-owned"), 0o600); writeErr != nil {
			return writeErr
		}
		return nil
	}})
	if errorCode(err) != "report_collision" {
		t.Fatalf("replacement race error=%v", err)
	}
	if data, readErr := os.ReadFile(racePath); readErr != nil || string(data) != "attacker-owned" {
		t.Fatalf("race target changed: %q err=%v", data, readErr)
	}

	oversized := sampleReport()
	for index := 0; index < 2000; index++ {
		oversized.Grade.Entrypoints = append(oversized.Grade.Entrypoints, entrypointGrade{
			Name: "oversized", State: "not_evaluated", Reason: strings.Repeat("x", 80),
		})
	}
	if err := publishReport(filepath.Join(directory, "oversized.json"), oversized, nil, publicationOptions{}); errorCode(err) != "report_too_large" {
		t.Fatalf("oversized report error=%v", err)
	}

	insecure := privateTempDir(t)
	if err := os.Chmod(insecure, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := publishReport(filepath.Join(insecure, "report.json"), report, nil, publicationOptions{}); errorCode(err) != "report_parent_invalid" {
		t.Fatalf("insecure parent error=%v", err)
	}
	if runtime.GOOS != "windows" {
		realParent := privateTempDir(t)
		linkParent := filepath.Join(privateTempDir(t), "parent-link")
		if err := os.Symlink(realParent, linkParent); err != nil {
			t.Fatal(err)
		}
		if err := publishReport(filepath.Join(linkParent, "report.json"), report, nil, publicationOptions{}); errorCode(err) != "report_parent_invalid" {
			t.Fatalf("symlink parent error=%v", err)
		}

		attackBase := privateTempDir(t)
		originalParent := filepath.Join(attackBase, "report-parent")
		movedParent := filepath.Join(attackBase, "report-parent-moved")
		attackerParent := filepath.Join(attackBase, "attacker-parent")
		if err := os.Mkdir(originalParent, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(attackerParent, 0o700); err != nil {
			t.Fatal(err)
		}
		parentRacePath := filepath.Join(originalParent, "report.json")
		err := publishReport(parentRacePath, report, nil, publicationOptions{beforeLink: func(parent string) error {
			if err := os.Rename(parent, movedParent); err != nil {
				return err
			}
			return os.Symlink(attackerParent, parent)
		}})
		if errorCode(err) != "report_parent_replaced" {
			t.Fatalf("parent replacement race error=%v", err)
		}
		for _, candidate := range []string{
			filepath.Join(attackerParent, "report.json"), filepath.Join(movedParent, "report.json"),
		} {
			if _, statErr := os.Lstat(candidate); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("parent replacement published %s: %v", candidate, statErr)
			}
		}

		validationBase := privateTempDir(t)
		validatedParent := filepath.Join(validationBase, "validated-parent")
		validatedParentMoved := filepath.Join(validationBase, "validated-parent-moved")
		replacementParent := filepath.Join(validationBase, "replacement-parent")
		if err := os.Mkdir(validatedParent, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(replacementParent, 0o700); err != nil {
			t.Fatal(err)
		}
		preOpenRacePath := filepath.Join(validatedParent, "report.json")
		err = publishReport(preOpenRacePath, report, nil, publicationOptions{afterValidation: func(parent string) error {
			if err := os.Rename(parent, validatedParentMoved); err != nil {
				return err
			}
			return os.Rename(replacementParent, parent)
		}})
		if errorCode(err) != "report_parent_replaced" {
			t.Fatalf("pre-open parent replacement error=%v", err)
		}
		for _, candidate := range []string{
			filepath.Join(validatedParent, "report.json"), filepath.Join(validatedParentMoved, "report.json"),
		} {
			if _, statErr := os.Lstat(candidate); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("pre-open replacement published %s: %v", candidate, statErr)
			}
		}
	}
}

func TestHarnessHasNoProductionRuntimeDependency(t *testing.T) {
	sourceRoot, err := commandRoot()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", ".")
	command.Dir = sourceRoot
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("list harness dependencies: %v: %s", err, output)
	}
	const (
		harnessImport      = "github.com/abietic/yhc/scripts/evaluation"
		ownedProcessImport = "github.com/abietic/yhc/scripts/internal/ownedprocess"
	)
	for _, dependency := range strings.Fields(string(output)) {
		if strings.HasPrefix(dependency, "github.com/abietic/yhc/") && dependency != harnessImport && dependency != ownedProcessImport {
			t.Fatalf("harness imported production package %q; the evaluated subject must remain an external binary", dependency)
		}
	}
}

func TestScriptedProviderRejectsAuthToolAndSequenceDrift(t *testing.T) {
	sourceRoot, err := commandRoot()
	if err != nil {
		t.Fatal(err)
	}
	manifest, script, err := loadScenario(sourceRoot, supportedScenario)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		authorize  bool
		tool       string
		input      any
		wantCode   string
		wantStatus int
	}{
		{name: "auth", tool: "Write", input: []any{}, wantCode: "provider_auth_drift", wantStatus: http.StatusBadRequest},
		{name: "tool", authorize: true, tool: "Read", input: []any{}, wantCode: "provider_tool_surface_drift", wantStatus: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider, err := newScriptedProvider(manifest, script, "/outside", "/repo/greet/decorate.go")
			if err != nil {
				t.Fatal(err)
			}
			status := sendProviderRequest(t, provider.URL(), manifest, test.authorize, test.tool, test.input)
			result := provider.CloseAndResult()
			if status != test.wantStatus || errorCode(result.Failure) != test.wantCode {
				t.Fatalf("status=%d failure=%v", status, result.Failure)
			}
		})
	}

	provider, err := newScriptedProvider(manifest, script, "/outside", "/repo/greet/decorate.go")
	if err != nil {
		t.Fatal(err)
	}
	if status := sendProviderRequest(t, provider.URL(), manifest, true, "Write", []any{}); status != http.StatusOK {
		t.Fatalf("first provider status=%d", status)
	}
	if status := sendProviderRequest(t, provider.URL(), manifest, true, "Write", []any{}); status != http.StatusBadRequest {
		t.Fatalf("missing denial status=%d", status)
	}
	if result := provider.CloseAndResult(); errorCode(result.Failure) != "outside_denial_missing" {
		t.Fatalf("sequence failure=%v", result.Failure)
	}
}

func TestCredentialArtifactScan(t *testing.T) {
	directory := privateTempDir(t)
	if err := os.WriteFile(filepath.Join(directory, "artifact"), []byte("prefix fixture-key suffix"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := scanCredentialSentinel("fixture-key", 1024, directory); errorCode(err) != "artifact_isolation_failed" {
		t.Fatalf("credential scan error=%v", err)
	}
}

func TestExternalDoubleReplayAndCleanupPrecedence(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and executes the public product binary")
	}
	if os.Getenv("EINO_EVAL_EXTERNAL_TEST") != "1" {
		t.Skip("opt-in external product-path evaluation; run with EINO_EVAL_EXTERNAL_TEST=1")
	}
	repositoryRoot := filepath.Clean(filepath.Join(mustWorkingDirectory(t), "..", ".."))
	buildRoot := privateTempDir(t)
	binary := filepath.Join(buildRoot, evaluationProductBinaryName())
	build := exec.Command("go", "build", "-o", binary, "./cmd/yhc")
	build.Dir = repositoryRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build product binary: %v: %s", err, output)
	}

	reportRoot := privateTempDir(t)
	reportPath := filepath.Join(reportRoot, "report.json")
	if err := evaluate(context.Background(), binary, supportedScenario, reportPath, defaultDependencies()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	var report evaluationReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if report.Harness != (harnessResult{State: "passed", Code: "double_replay_equal", Replays: 2}) ||
		report.Grade.Outcome.State != "passed" || report.Grade.Cleanup.State != "passed" ||
		report.Grade.Usage != (usageGrade{Coverage: "exact", InputTokens: 3, OutputTokens: 3, TotalTokens: 6}) ||
		len(report.Diagnostics.ReplayDurationMilliseconds) != 2 {
		t.Fatalf("unexpected report: %#v", report)
	}
	gradeData, err := canonicalGradeBytes(report.Grade)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{manifestPrompt(t), "fixture-key", "call-outside", "call-repair", repositoryRoot, buildRoot, reportRoot} {
		if bytes.Contains(data, []byte(forbidden)) || bytes.Contains(gradeData, []byte(forbidden)) {
			t.Fatalf("report leaked forbidden sentinel %q", forbidden)
		}
	}

	cleanupReportRoot := privateTempDir(t)
	cleanupReport := filepath.Join(cleanupReportRoot, "report.json")
	deps := defaultDependencies()
	deps.removeAll = func(path string) error {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
		return errors.New("injected cleanup failure")
	}
	err = evaluate(context.Background(), binary, supportedScenario, cleanupReport, deps)
	if errorCode(err) != "cleanup_failed" {
		t.Fatalf("cleanup precedence error=%v", err)
	}
	if _, statErr := os.Lstat(cleanupReport); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("cleanup failure published a report: %v", statErr)
	}
}

func TestEvaluationProductBinaryNameUsesYHC(t *testing.T) {
	want := "yhc"
	if runtime.GOOS == "windows" {
		want += ".exe"
	}
	if got := evaluationProductBinaryName(); got != want {
		t.Fatalf("evaluation product binary = %q, want %q", got, want)
	}
}

func evaluationProductBinaryName() string {
	name := "yhc"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

func sampleReport() evaluationReport {
	return evaluationReport{
		Kind: reportKind, SchemaVersion: 1, RunnerVersion: runnerVersion,
		Scenario: reportScenario{ID: "localized-write-fix", Version: 1, FixtureSHA256: strings.Repeat("a", 64), TaskSHA256: strings.Repeat("b", 64), BinarySHA256: strings.Repeat("c", 64)},
		Harness:  harnessResult{State: "passed", Code: "double_replay_equal", Replays: 2},
		Grade: canonicalGrade{
			Outcome: stateCode{State: "passed", Code: "task_succeeded"},
			Cleanup: stateCode{State: "passed", Code: "all_private_roots_removed"},
		},
		Diagnostics: reportDiagnostics{ReplayDurationMilliseconds: []int64{1, 2}},
	}
}

func sampleReportWithCode(code string) evaluationReport {
	report := sampleReport()
	report.Harness.Code = code
	return report
}

func privateTempDir(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	canonical, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(canonical, 0o700); err != nil {
		t.Fatal(err)
	}
	return canonical
}

func mustWorkingDirectory(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return directory
}

func sendProviderRequest(t *testing.T, url string, manifest scenarioManifest, authorize bool, tool string, input any) int {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"model": manifest.Execution.Model,
		"input": input,
		"tools": []map[string]string{{"type": "function", "name": tool}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, url+"/responses", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if authorize {
		request.Header.Set("Authorization", "Bearer "+manifest.Provider.FakeAPIKey)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	return response.StatusCode
}

func manifestPrompt(t *testing.T) string {
	t.Helper()
	root, err := commandRoot()
	if err != nil {
		t.Fatal(err)
	}
	manifest, _, err := loadScenario(root, supportedScenario)
	if err != nil {
		t.Fatal(err)
	}
	return manifest.Task.Prompt
}
