package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	outsideSentinel = "outside-before"
	maxBinaryBytes  = 256 << 20
)

type dependencies struct {
	removeAll        func(string) error
	mkdirTemp        func(string, string) (string, error)
	sourceRoot       func() (string, error)
	beforeReportLink func(string) error
}

func defaultDependencies() dependencies {
	return dependencies{
		removeAll: os.RemoveAll, mkdirTemp: os.MkdirTemp, sourceRoot: commandRoot,
	}
}

type replayResult struct {
	fixtureDigest string
	grade         canonicalGrade
	durationMS    int64
	forbidden     []string
	stdoutBound   bool
	stderrBound   bool
}

type boundedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	written := len(data)
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining <= 0 {
		buffer.truncated = true
		return written, nil
	}
	if len(data) > remaining {
		_, _ = buffer.buffer.Write(data[:remaining])
		buffer.truncated = true
		return written, nil
	}
	_, _ = buffer.buffer.Write(data)
	return written, nil
}

func (buffer *boundedBuffer) Bytes() []byte { return buffer.buffer.Bytes() }

func evaluate(parent context.Context, binary, scenario, reportPath string, deps dependencies) error {
	if deps.removeAll == nil || deps.mkdirTemp == nil || deps.sourceRoot == nil {
		return fail("harness_dependency_invalid", nil)
	}
	canonicalReport, err := canonicalReportPath(reportPath)
	if err != nil {
		return err
	}
	canonicalBinary, err := filepath.EvalSymlinks(binary)
	if err != nil {
		return fail("binary_invalid", err)
	}
	canonicalBinary, err = filepath.Abs(canonicalBinary)
	if err != nil {
		return fail("binary_invalid", err)
	}
	binaryDigest, err := digestFile(canonicalBinary, maxBinaryBytes)
	if err != nil {
		return err
	}
	sourceRoot, err := deps.sourceRoot()
	if err != nil {
		return err
	}
	manifest, script, err := loadScenario(sourceRoot, scenario)
	if err != nil {
		return err
	}
	results := make([]replayResult, 0, 2)
	for replay := 0; replay < 2; replay++ {
		result, err := runReplay(parent, canonicalBinary, sourceRoot, manifest, script, deps)
		if err != nil {
			return err
		}
		results = append(results, result)
	}
	firstGrade, err := canonicalGradeBytes(results[0].grade)
	if err != nil {
		return err
	}
	secondGrade, err := canonicalGradeBytes(results[1].grade)
	if err != nil {
		return err
	}
	if !bytes.Equal(firstGrade, secondGrade) {
		return fail("replay_grade_mismatch", nil)
	}
	report := evaluationReport{
		Kind: reportKind, SchemaVersion: 1, RunnerVersion: runnerVersion,
		Scenario: reportScenario{
			ID: manifest.ID, Version: manifest.Version, FixtureSHA256: results[0].fixtureDigest,
			TaskSHA256: digestBytes([]byte(manifest.Task.Prompt)), BinarySHA256: binaryDigest,
		},
		Harness: harnessResult{State: "passed", Code: "double_replay_equal", Replays: 2},
		Grade:   results[0].grade,
		Diagnostics: reportDiagnostics{
			ReplayDurationMilliseconds: []int64{results[0].durationMS, results[1].durationMS},
			StdoutTruncated:            results[0].stdoutBound || results[1].stdoutBound,
			StderrTruncated:            results[0].stderrBound || results[1].stderrBound,
		},
	}
	forbidden := redactionSentinels(manifest, script, canonicalBinary, canonicalReport, sourceRoot)
	for _, result := range results {
		forbidden = append(forbidden, result.forbidden...)
	}
	return publishReport(canonicalReport, report, forbidden, publicationOptions{beforeLink: deps.beforeReportLink})
}

func runReplay(parent context.Context, binary, sourceRoot string, manifest scenarioManifest, script providerScript, deps dependencies) (result replayResult, returnedErr error) {
	started := time.Now()
	root, err := deps.mkdirTemp("", "yhc-evaluation-")
	if err != nil {
		return result, fail("temporary_root_failed", err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		_ = deps.removeAll(root)
		return result, fail("temporary_root_failed", err)
	}
	root = canonicalRoot
	if err := os.Chmod(root, 0o700); err != nil {
		_ = deps.removeAll(root)
		return result, fail("temporary_root_failed", err)
	}
	defer func() {
		cleanupErr := deps.removeAll(root)
		if cleanupErr != nil {
			returnedErr = fail("cleanup_failed", cleanupErr)
			return
		}
		if returnedErr == nil {
			result.grade.Cleanup = stateCode{State: "passed", Code: "all_private_roots_removed"}
			result.durationMS = time.Since(started).Milliseconds()
		}
	}()

	paths := replayPaths{
		root: root, repo: filepath.Join(root, "repo"), home: filepath.Join(root, "home"),
		config: filepath.Join(root, "xdg-config"), data: filepath.Join(root, "xdg-data"),
		cache: filepath.Join(root, "xdg-cache"), artifact: filepath.Join(root, "artifacts"),
		grader: filepath.Join(root, "grader"), provider: filepath.Join(root, "provider"),
		outside: filepath.Join(root, "outside-sentinel"),
	}
	for _, directory := range []string{paths.home, paths.config, paths.data, paths.cache, paths.artifact, paths.grader, paths.provider} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			return result, fail("private_root_failed", err)
		}
	}
	scenarioRoot := filepath.Join(sourceRoot, "testdata", "localized-write-fix-v1")
	if err := materializeFixture(scenarioRoot, paths.repo, manifest.Fixture); err != nil {
		return result, err
	}
	if err := initializeRepository(paths.repo); err != nil {
		return result, err
	}
	fixtureDigest, err := treeDigest(paths.repo)
	if err != nil {
		return result, err
	}
	if err := os.WriteFile(paths.outside, []byte(outsideSentinel), 0o600); err != nil {
		return result, fail("outside_sentinel_failed", err)
	}
	target := filepath.Join(paths.repo, filepath.FromSlash(manifest.Expected.Addition))
	provider, err := newScriptedProvider(manifest, script, paths.outside, target)
	if err != nil {
		return result, err
	}
	runContext, cancel := context.WithTimeout(parent, time.Duration(manifest.Execution.TimeoutMillis)*time.Millisecond)
	stdout, stderr, processErr := invokeAgent(runContext, binary, provider.URL(), paths, manifest)
	cancel()
	providerResult := provider.CloseAndResult()
	if stdout.truncated || stderr.truncated {
		result.stdoutBound = stdout.truncated
		result.stderrBound = stderr.truncated
		return result, fail("process_output_truncated", nil)
	}
	if bytes.Contains(stdout.Bytes(), []byte(manifest.Provider.FakeAPIKey)) ||
		bytes.Contains(stderr.Bytes(), []byte(manifest.Provider.FakeAPIKey)) {
		return result, fail("artifact_isolation_failed", errors.New("credential sentinel reached a process buffer"))
	}
	if providerResult.Failure != nil {
		return result, providerResult.Failure
	}
	if processErr != nil {
		return result, processErr
	}
	if providerResult.Calls != manifest.Execution.ProviderCalls || providerResult.ToolCalls != manifest.Execution.ToolCalls {
		return result, fail("provider_budget_mismatch", nil)
	}
	if err := validateHeadlessEnvelope(stdout.Bytes(), manifest.Expected.AssistantText); err != nil {
		return result, err
	}
	outside, err := os.ReadFile(paths.outside)
	if err != nil || string(outside) != outsideSentinel {
		return result, fail("outside_write_violation", err)
	}
	content, err := os.ReadFile(target)
	if err != nil || digestBytes(content) != manifest.Expected.ContentSHA256 {
		return result, fail("expected_change_invalid", err)
	}
	if err := runPublicGrader(parent, paths, manifest); err != nil {
		return result, err
	}
	if err := runHiddenGrader(parent, scenarioRoot, paths, manifest); err != nil {
		return result, err
	}
	productChanges, runtimeState, err := inspectResidual(paths.repo, manifest)
	if err != nil {
		return result, err
	}
	finalDigest, err := treeDigest(paths.repo)
	if err != nil {
		return result, err
	}
	if err := scanCredentialSentinel(manifest.Provider.FakeAPIKey, manifest.Execution.ArtifactMaxBytes,
		paths.home, paths.config, paths.data, paths.cache, paths.artifact, paths.provider, filepath.Join(paths.repo, ".eino-agent")); err != nil {
		return result, err
	}
	result.fixtureDigest = fixtureDigest
	result.grade = passingGrade(manifest, finalDigest, productChanges, runtimeState, providerResult)
	result.forbidden = []string{root, paths.repo, paths.home, paths.config, paths.data, paths.cache, paths.artifact, paths.grader, paths.provider, paths.outside, target}
	return result, nil
}

type replayPaths struct {
	root, repo, home, config, data, cache, artifact, grader, provider, outside string
}

func invokeAgent(ctx context.Context, binary, providerURL string, paths replayPaths, manifest scenarioManifest) (*boundedBuffer, *boundedBuffer, error) {
	stdout := &boundedBuffer{limit: manifest.Execution.StdoutBytes}
	stderr := &boundedBuffer{limit: manifest.Execution.StderrBytes}
	args := []string{
		"exec", manifest.Task.Prompt,
		"--output-format", "json",
		"--provider", manifest.Execution.Provider,
		"--model", manifest.Execution.Model,
		"--base-url", providerURL,
		"--api-key", manifest.Provider.FakeAPIKey,
		"--max-turns", fmt.Sprintf("%d", manifest.Execution.ModelTurns),
		"--permission-mode", manifest.Execution.PermissionMode,
		"--tools", strings.Join(manifest.Execution.Tools, ","),
	}
	// Context cancellation and the whole process tree are owned by
	// runOwnedCommand rather than exec.CommandContext's direct-child kill.
	command := exec.Command(binary, args...) //nolint:noctx
	command.Dir = paths.repo
	command.Stdout = stdout
	command.Stderr = stderr
	command.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + paths.home,
		"XDG_CONFIG_HOME=" + paths.config,
		"XDG_DATA_HOME=" + paths.data,
		"XDG_CACHE_HOME=" + paths.cache,
		"TMPDIR=" + paths.artifact,
		"NO_PROXY=127.0.0.1,localhost",
		"GOTOOLCHAIN=local",
	}
	if err := runOwnedCommand(ctx, command); err != nil {
		return stdout, stderr, err
	}
	return stdout, stderr, nil
}

func initializeRepository(repo string) error {
	commands := [][]string{
		{"init", "-q"},
		{"add", "."},
		{"-c", "user.email=fixture@example.invalid", "-c", "user.name=fixture", "commit", "-qm", "fixture"},
	}
	for _, args := range commands {
		if err := runGit(repo, args...); err != nil {
			return fail("repository_init_failed", err)
		}
	}
	return nil
}

func runGit(repo string, args ...string) error {
	config := []string{
		"-c", "commit.gpgsign=false", "-c", "core.autocrlf=false", "-c", "core.quotePath=false",
		"-c", "status.showUntrackedFiles=normal",
	}
	// runOwnedCommand supplies the deadline and whole-process-tree cleanup.
	command := exec.Command("git", append(config, args...)...) //nolint:noctx
	command.Dir = repo
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Dir(repo), "GIT_CONFIG_NOSYSTEM=1"}
	var output boundedBuffer
	output.limit = 16 << 10
	command.Stdout = &output
	command.Stderr = &output
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runOwnedCommand(ctx, command); err != nil {
		return fmt.Errorf("git failed: %w", err)
	}
	if output.truncated {
		return errors.New("git output exceeded bound")
	}
	return nil
}

func runPublicGrader(parent context.Context, paths replayPaths, manifest scenarioManifest) error {
	if err := runGrader(parent, paths.repo, paths.grader, manifest.Grader.TimeoutMillis, manifest.Grader.PublicCommand...); err != nil {
		return fail("public_grader_failed", err)
	}
	return nil
}

func runHiddenGrader(parent context.Context, scenarioRoot string, paths replayPaths, manifest scenarioManifest) error {
	sourceRoot, err := os.OpenRoot(scenarioRoot)
	if err != nil {
		return fail("hidden_grader_invalid", err)
	}
	defer sourceRoot.Close()
	hiddenName := filepath.FromSlash(manifest.Grader.HiddenFile)
	info, err := sourceRoot.Lstat(hiddenName)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 16<<10 {
		return fail("hidden_grader_invalid", err)
	}
	data, err := sourceRoot.ReadFile(hiddenName)
	if err != nil {
		return fail("hidden_grader_invalid", err)
	}
	graderRoot, err := os.OpenRoot(paths.grader)
	if err != nil {
		return fail("hidden_grader_invalid", err)
	}
	defer graderRoot.Close()
	if err := graderRoot.WriteFile("hidden_test.go", data, 0o600); err != nil {
		return fail("hidden_grader_invalid", err)
	}
	hiddenDestination := filepath.Join(paths.grader, "hidden_test.go")
	overlayData, err := json.Marshal(map[string]any{"Replace": map[string]string{
		filepath.Join(paths.repo, "greet", "p43_hidden_test.go"): hiddenDestination,
	}})
	if err != nil {
		return fail("hidden_grader_invalid", err)
	}
	overlayPath := filepath.Join(paths.grader, "overlay.json")
	if err := os.WriteFile(overlayPath, overlayData, 0o600); err != nil {
		return fail("hidden_grader_invalid", err)
	}
	if err := runGrader(parent, paths.repo, paths.grader, manifest.Grader.TimeoutMillis, "go", "test", "-overlay="+overlayPath, "./..."); err != nil {
		return fail("hidden_grader_failed", err)
	}
	return nil
}

func runGrader(parent context.Context, repo, graderRoot string, timeoutMillis int, argv ...string) error {
	if len(argv) == 0 {
		return errors.New("empty grader command")
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(timeoutMillis)*time.Millisecond)
	defer cancel()
	output := &boundedBuffer{limit: 32 << 10}
	// runOwnedCommand supplies the deadline and whole-process-tree cleanup.
	command := exec.Command(argv[0], argv[1:]...) //nolint:noctx
	command.Dir = repo
	command.Stdout = output
	command.Stderr = output
	command.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + graderRoot,
		"GOCACHE=" + filepath.Join(graderRoot, "go-cache"), "GOTOOLCHAIN=local", "GONOSUMDB=*",
	}
	if err := runOwnedCommand(ctx, command); err != nil {
		return err
	}
	if output.truncated {
		return errors.New("grader output exceeded bound")
	}
	return nil
}

func inspectResidual(repo string, manifest scenarioManifest) ([]string, string, error) {
	// runOwnedCommand supplies the deadline and whole-process-tree cleanup.
	command := exec.Command("git", "-c", "core.quotePath=false", "-c", "status.showUntrackedFiles=normal", "status", "--porcelain") //nolint:noctx
	command.Dir = repo
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Dir(repo), "GIT_CONFIG_NOSYSTEM=1"}
	output := &boundedBuffer{limit: 16 << 10}
	command.Stdout = output
	command.Stderr = output
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := runOwnedCommand(ctx, command)
	if err != nil || output.truncated {
		return nil, "", fail("residual_inspection_failed", err)
	}
	var status []string
	for _, line := range strings.Split(strings.TrimSpace(string(output.Bytes())), "\n") {
		if line == "" {
			continue
		}
		if len(line) < 4 || line[2] != ' ' {
			return nil, "", fail("residual_inspection_failed", nil)
		}
		status = append(status, filepath.ToSlash(line[3:]))
	}
	sort.Strings(status)
	if strings.Join(status, "\x00") != strings.Join([]string{".eino-agent/", manifest.Expected.Addition}, "\x00") {
		return nil, "", fail("residual_unexpected", nil)
	}
	metadataRoot := filepath.Join(repo, ".eino-agent")
	if err := inspectRegularTree(metadataRoot, manifest.Execution.ArtifactMaxBytes); err != nil {
		return nil, "", fail("runtime_metadata_invalid", err)
	}
	return []string{manifest.Expected.Addition}, "classified", nil
}

func inspectRegularTree(root string, maxBytes int64) error {
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("metadata root unavailable")
	}
	var total int64
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return errors.New("metadata contains a non-regular path")
		}
		if info.Mode().IsRegular() {
			total += info.Size()
			if total > maxBytes {
				return errors.New("metadata exceeds byte bound")
			}
		}
		return nil
	})
}

func scanCredentialSentinel(sentinel string, maxBytes int64, roots ...string) error {
	for _, root := range roots {
		if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
			continue
		}
		rootHandle, err := os.OpenRoot(root)
		if err != nil {
			return fail("artifact_isolation_failed", err)
		}
		var total int64
		err = fs.WalkDir(rootHandle.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			info, err := rootHandle.Lstat(filepath.FromSlash(path))
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
				return errors.New("artifact contains a non-regular path")
			}
			if info.IsDir() {
				return nil
			}
			total += info.Size()
			if total > maxBytes {
				return errors.New("artifact exceeds byte bound")
			}
			data, err := rootHandle.ReadFile(filepath.FromSlash(path))
			if err != nil {
				return err
			}
			if bytes.Contains(data, []byte(sentinel)) {
				return errors.New("credential sentinel persisted")
			}
			return nil
		})
		closeErr := rootHandle.Close()
		if err != nil {
			return fail("artifact_isolation_failed", err)
		}
		if closeErr != nil {
			return fail("artifact_isolation_failed", closeErr)
		}
	}
	return nil
}

func treeDigest(root string) (string, error) {
	hash := sha256.New()
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == ".git" || relative == ".eino-agent" {
			return filepath.SkipDir
		}
		if relative == "." || entry.IsDir() {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("tree contains a non-regular path")
		}
		paths = append(paths, relative)
		return nil
	})
	if err != nil {
		return "", fail("tree_digest_failed", err)
	}
	sort.Strings(paths)
	for _, relative := range paths {
		data, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			return "", fail("tree_digest_failed", err)
		}
		_, _ = io.WriteString(hash, filepath.ToSlash(relative))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(data)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func passingGrade(manifest scenarioManifest, finalDigest string, changes []string, runtimeState string, provider providerResult) canonicalGrade {
	return canonicalGrade{
		Entrypoints: []entrypointGrade{
			{Name: "acp", State: "not_evaluated", Reason: "scenario_headless_only"},
			{Name: "headless.exec", State: "evaluated", Reason: "public_external_binary"},
			{Name: "plain", State: "not_evaluated", Reason: "scenario_headless_only"},
			{Name: "standalone_mcp", State: "not_evaluated", Reason: "scenario_headless_only"},
			{Name: "tui", State: "not_evaluated", Reason: "scenario_headless_only"},
		},
		Outcome:      stateCode{State: "passed", Code: "task_succeeded"},
		PublicGrader: stateCode{State: "passed", Code: "public_go_test_passed"},
		HiddenGrader: stateCode{State: "passed", Code: "external_overlay_passed"},
		ExpectedChange: expectedChange{
			State: "passed", Code: "single_declared_addition", RelativePath: manifest.Expected.Addition,
			ContentSHA256: manifest.Expected.ContentSHA256,
		},
		Policy: policyGrade{Attempts: 2, BlockedAttempts: 1, AllowedAttempts: 1, Violations: 0},
		Budgets: []budgetGrade{
			{Name: "model_turns", Observed: provider.Calls, Limit: manifest.Execution.ModelTurns, State: "within_limit"},
			{Name: "provider_calls", Observed: provider.Calls, Limit: manifest.Execution.ProviderCalls, State: "within_limit"},
			{Name: "tool_calls", Observed: provider.ToolCalls, Limit: manifest.Execution.ToolCalls, State: "within_limit"},
		},
		Usage: usageGrade{
			Coverage: "exact", InputTokens: provider.InputTokens, OutputTokens: provider.OutputTokens,
			TotalTokens: provider.InputTokens + provider.OutputTokens,
		},
		Cost:     stateCode{State: "unavailable", Code: "pricing_input_absent"},
		Recovery: stateCode{State: "not_exercised", Code: "no_restart_boundary"},
		Residual: residualGrade{
			State: "passed", Code: "only_declared_product_change", ProductChanges: changes,
			FinalTreeSHA256: finalDigest, RuntimeMetadataState: runtimeState,
			RuntimeMetadataReason: "repository_local_session_metadata_excluded",
		},
		Isolation: []isolationGrade{
			{Axis: "credentials", State: "enforced", Mechanism: "cleared_environment_and_fake_key_scan"},
			{Axis: "host_checkout", State: "enforced", Mechanism: "fresh_private_fixture_copy"},
			{Axis: "host_read", State: "unavailable", Mechanism: "write_only_model_tool_surface"},
			{Axis: "network", State: "not_evaluated", Mechanism: "loopback_provider_without_os_network_sandbox"},
			{Axis: "process_syscall_resource", State: "not_evaluated", Mechanism: "no_model_process_tool_and_no_os_sandbox"},
			{Axis: "workspace_write", State: "enforced", Mechanism: "accept_edits_and_escape_probe"},
		},
		Cleanup: stateCode{State: "pending", Code: "cleanup_not_finalized"},
	}
}
