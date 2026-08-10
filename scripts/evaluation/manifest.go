package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"
)

const (
	supportedScenario       = "localized-write-fix/v1"
	maxStructuredInputBytes = 32 << 10
)

type scenarioManifest struct {
	SchemaVersion int               `json:"schema_version"`
	ID            string            `json:"id"`
	Version       int               `json:"version"`
	Fixture       fixtureManifest   `json:"fixture"`
	Task          taskManifest      `json:"task"`
	Execution     executionManifest `json:"execution"`
	Expected      expectedManifest  `json:"expected"`
	Grader        graderManifest    `json:"grader"`
	Provider      providerManifest  `json:"provider"`
}

type fixtureManifest struct {
	Root     string   `json:"root"`
	Files    []string `json:"files"`
	MaxFiles int      `json:"max_files"`
	MaxBytes int64    `json:"max_bytes"`
}

type taskManifest struct {
	Prompt string `json:"prompt"`
}

type executionManifest struct {
	Entrypoint       string   `json:"entrypoint"`
	PermissionMode   string   `json:"permission_mode"`
	Tools            []string `json:"tools"`
	Provider         string   `json:"provider"`
	Model            string   `json:"model"`
	ProviderCalls    int      `json:"provider_calls"`
	ModelTurns       int      `json:"model_turns"`
	ToolCalls        int      `json:"tool_calls"`
	TimeoutMillis    int      `json:"timeout_milliseconds"`
	StdoutBytes      int      `json:"stdout_bytes"`
	StderrBytes      int      `json:"stderr_bytes"`
	ArtifactMaxBytes int64    `json:"artifact_max_bytes"`
}

type expectedManifest struct {
	Addition      string `json:"addition"`
	ContentSHA256 string `json:"content_sha256"`
	AssistantText string `json:"assistant_text"`
}

type graderManifest struct {
	PublicCommand []string `json:"public_command"`
	HiddenFile    string   `json:"hidden_file"`
	TimeoutMillis int      `json:"timeout_milliseconds"`
}

type providerManifest struct {
	Script     string `json:"script"`
	FakeAPIKey string `json:"fake_api_key"`
}

type providerStep struct {
	Kind         string `json:"kind"`
	CallID       string `json:"call_id"`
	RelativePath string `json:"relative_path"`
	Content      string `json:"content"`
	Text         string `json:"text"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
}

type providerScript struct {
	SchemaVersion int            `json:"schema_version"`
	Steps         []providerStep `json:"steps"`
}

func loadScenario(root, scenario string) (scenarioManifest, providerScript, error) {
	if scenario != supportedScenario {
		return scenarioManifest{}, providerScript{}, fail("scenario_unsupported", nil)
	}
	scenarioRoot := filepath.Join(root, "testdata", "localized-write-fix-v1")
	var manifest scenarioManifest
	if err := decodeClosedFile(filepath.Join(scenarioRoot, "scenario.json"), &manifest); err != nil {
		return scenarioManifest{}, providerScript{}, fail("manifest_invalid", err)
	}
	if err := validateManifest(manifest); err != nil {
		return scenarioManifest{}, providerScript{}, fail("manifest_invalid", err)
	}
	var script providerScript
	if err := decodeClosedFile(filepath.Join(scenarioRoot, filepath.FromSlash(manifest.Provider.Script)), &script); err != nil {
		return scenarioManifest{}, providerScript{}, fail("provider_script_invalid", err)
	}
	if err := validateProviderScript(script, manifest); err != nil {
		return scenarioManifest{}, providerScript{}, fail("provider_script_invalid", err)
	}
	return manifest, script, nil
}

func decodeClosedFile(path string, dst any) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("input is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxStructuredInputBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxStructuredInputBytes {
		return errors.New("input exceeds byte limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func validateManifest(manifest scenarioManifest) error {
	if manifest.SchemaVersion != 1 || manifest.ID != "localized-write-fix" || manifest.Version != 1 {
		return errors.New("unsupported scenario identity")
	}
	if manifest.Fixture.Root != "fixture" || manifest.Fixture.MaxFiles != len(manifest.Fixture.Files) ||
		manifest.Fixture.MaxFiles < 1 || manifest.Fixture.MaxFiles > 32 ||
		manifest.Fixture.MaxBytes < 1 || manifest.Fixture.MaxBytes > 64<<10 {
		return errors.New("invalid fixture bounds")
	}
	if err := validateUniqueRelativePaths(manifest.Fixture.Files); err != nil {
		return fmt.Errorf("fixture files: %w", err)
	}
	if strings.TrimSpace(manifest.Task.Prompt) == "" || len(manifest.Task.Prompt) > 4096 {
		return errors.New("invalid task prompt")
	}
	execution := manifest.Execution
	if execution.Entrypoint != "headless.exec" || execution.PermissionMode != "acceptEdits" ||
		len(execution.Tools) != 1 || execution.Tools[0] != "Write" ||
		execution.Provider != "openai" || execution.Model != "gpt-4o" ||
		execution.ProviderCalls != 3 || execution.ModelTurns != 3 || execution.ToolCalls != 2 ||
		execution.TimeoutMillis < 1 || execution.TimeoutMillis > 30_000 ||
		execution.StdoutBytes < 1 || execution.StdoutBytes > 64<<10 ||
		execution.StderrBytes < 1 || execution.StderrBytes > 64<<10 ||
		execution.ArtifactMaxBytes < 1 || execution.ArtifactMaxBytes > 1<<20 {
		return errors.New("invalid execution contract")
	}
	if !safeRelativePath(manifest.Expected.Addition) || len(manifest.Expected.ContentSHA256) != 64 ||
		strings.TrimSpace(manifest.Expected.AssistantText) == "" {
		return errors.New("invalid expected result")
	}
	if len(manifest.Grader.PublicCommand) != 3 || strings.Join(manifest.Grader.PublicCommand, "\x00") != "go\x00test\x00./..." ||
		!safeRelativePath(manifest.Grader.HiddenFile) || manifest.Grader.TimeoutMillis != 60_000 {
		return errors.New("invalid grader contract")
	}
	if !safeRelativePath(manifest.Provider.Script) || manifest.Provider.FakeAPIKey == "" || len(manifest.Provider.FakeAPIKey) > 128 {
		return errors.New("invalid provider contract")
	}
	return nil
}

func validateProviderScript(script providerScript, manifest scenarioManifest) error {
	if script.SchemaVersion != 1 || len(script.Steps) != 3 {
		return errors.New("script must contain three versioned steps")
	}
	wantKinds := []string{"outside_write", "contained_write", "final"}
	seenCallIDs := map[string]struct{}{}
	input, output := 0, 0
	for index, step := range script.Steps {
		if step.Kind != wantKinds[index] || step.InputTokens != 1 || step.OutputTokens != 1 {
			return fmt.Errorf("step %d violates the frozen sequence", index)
		}
		input += step.InputTokens
		output += step.OutputTokens
		if index < 2 {
			if step.CallID == "" || len(step.CallID) > 64 {
				return errors.New("invalid provider call ID")
			}
			if _, duplicate := seenCallIDs[step.CallID]; duplicate {
				return errors.New("duplicate provider call ID")
			}
			seenCallIDs[step.CallID] = struct{}{}
		}
	}
	if script.Steps[0].RelativePath != "" || script.Steps[0].Content == "" || script.Steps[0].Text != "" ||
		script.Steps[1].RelativePath != manifest.Expected.Addition || script.Steps[1].Content == "" || script.Steps[1].Text != "" ||
		script.Steps[2].CallID != "" || script.Steps[2].RelativePath != "" || script.Steps[2].Content != "" ||
		script.Steps[2].Text != manifest.Expected.AssistantText ||
		input != 3 || output != 3 {
		return errors.New("provider steps do not match the scenario")
	}
	return nil
}

func validateUniqueRelativePaths(paths []string) error {
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if !safeRelativePath(path) {
			return fmt.Errorf("unsafe path %q", path)
		}
		canonical := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
		if _, duplicate := seen[canonical]; duplicate {
			return fmt.Errorf("duplicate path %q", path)
		}
		seen[canonical] = struct{}{}
	}
	return nil
}

func safeRelativePath(path string) bool {
	if path == "" || path == "." || filepath.IsAbs(path) || strings.ContainsRune(path, '\x00') || strings.Contains(path, "\\") {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	return clean == path && clean != ".." && !strings.HasPrefix(clean, "../")
}

func materializeFixture(scenarioRoot, destination string, fixture fixtureManifest) error {
	source := filepath.Join(scenarioRoot, filepath.FromSlash(fixture.Root))
	sourceRoot, err := os.OpenRoot(source)
	if err != nil {
		return fail("fixture_invalid", err)
	}
	defer sourceRoot.Close()
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return fail("fixture_invalid", err)
	}
	destinationRoot, err := os.OpenRoot(destination)
	if err != nil {
		return fail("fixture_invalid", err)
	}
	defer destinationRoot.Close()
	declared := append([]string(nil), fixture.Files...)
	sort.Strings(declared)
	actual := make([]string, 0, len(declared))
	err = fs.WalkDir(sourceRoot.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if path == "." {
			if !info.IsDir() {
				return errors.New("fixture root is not a directory")
			}
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("fixture path %q is not a regular file or directory", path)
		}
		if info.IsDir() {
			return nil
		}
		actual = append(actual, path)
		return nil
	})
	if err != nil {
		return fail("fixture_invalid", err)
	}
	sort.Strings(actual)
	if strings.Join(actual, "\x00") != strings.Join(declared, "\x00") {
		return fail("fixture_invalid", fmt.Errorf("declared files %v differ from materialized files %v", declared, actual))
	}
	var total int64
	for _, relative := range declared {
		name := filepath.FromSlash(relative)
		info, err := sourceRoot.Lstat(name)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fail("fixture_invalid", err)
		}
		data, err := sourceRoot.ReadFile(name)
		if err != nil {
			return fail("fixture_invalid", err)
		}
		total += int64(len(data))
		if total > fixture.MaxBytes {
			return fail("fixture_invalid", errors.New("fixture exceeds declared byte bound"))
		}
		if directory := pathpkg.Dir(relative); directory != "." {
			if err := destinationRoot.MkdirAll(filepath.FromSlash(directory), 0o700); err != nil {
				return fail("fixture_invalid", err)
			}
		}
		if err := destinationRoot.WriteFile(name, data, 0o600); err != nil {
			return fail("fixture_invalid", err)
		}
	}
	return nil
}
