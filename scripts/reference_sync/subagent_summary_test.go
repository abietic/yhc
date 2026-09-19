package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSummarizeUpdateFilesUsesOnlyNewUpdatesAndPersistsSummary(t *testing.T) {
	t.Parallel()

	memoryRoot := t.TempDir()
	updatePath := filepath.Join(memoryRoot, "updates", "20260826T010000000Z-codex.md")
	if err := os.MkdirAll(filepath.Dir(updatePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(updatePath, []byte("# Reference update: codex\n\n## Model analysis\n\n新增 MCP 连接代际保护。\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	generator := &fakeModelSummaryGenerator{
		provider: "agenticopenai",
		model:    "subagent-model",
		result:   "## 汇总\n本轮增加了 MCP 连接代际保护。",
	}
	now := time.Date(2026, 8, 26, 1, 0, 0, 0, time.UTC)
	result, err := summarizeUpdateFiles(
		context.Background(),
		t.TempDir(),
		memoryRoot,
		now,
		[]string{updatePath},
		SubagentSummary{MaxInputBytes: 4096, TimeoutSeconds: 1},
		func(context.Context, string, string) (modelSummaryGenerator, error) {
			return generator, nil
		},
	)
	if err != nil {
		t.Fatalf("summarizeUpdateFiles() error = %v", err)
	}
	if result.Status != "generated" || result.InputFiles != 1 {
		t.Fatalf("result = %#v", result)
	}
	if result.Provider != "agenticopenai" || result.Model != "subagent-model" {
		t.Fatalf("model identity = %s:%s", result.Provider, result.Model)
	}
	for _, want := range []string{
		"只读的 reference 更新汇总 subagent",
		"新增 MCP 连接代际保护",
		"不要把上游变化自动变成 YHC backlog",
	} {
		if !strings.Contains(generator.prompt, want) {
			t.Errorf("subagent prompt missing %q:\n%s", want, generator.prompt)
		}
	}
	if !strings.HasPrefix(result.Path, "summaries/") {
		t.Fatalf("summary path = %q", result.Path)
	}
	data, err := os.ReadFile(filepath.Join(memoryRoot, filepath.FromSlash(result.Path)))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## Subagent analysis", "本轮增加了 MCP 连接代际保护", "subagent-model"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("summary file missing %q:\n%s", want, data)
		}
	}
}

func TestSummarizeUpdateFilesPersistsFailureWithoutFallback(t *testing.T) {
	t.Parallel()

	memoryRoot := t.TempDir()
	updatePath := filepath.Join(memoryRoot, "updates", "one.md")
	if err := os.MkdirAll(filepath.Dir(updatePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(updatePath, []byte("update evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 26, 1, 0, 0, 0, time.UTC)
	result, err := summarizeUpdateFiles(
		context.Background(),
		t.TempDir(),
		memoryRoot,
		now,
		[]string{updatePath},
		SubagentSummary{MaxInputBytes: 4096, TimeoutSeconds: 1},
		func(context.Context, string, string) (modelSummaryGenerator, error) {
			return &fakeModelSummaryGenerator{err: errors.New("subagent unavailable")}, nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "subagent unavailable") {
		t.Fatalf("error = %v, want subagent failure", err)
	}
	if result.Status != "failed" || result.Error == "" {
		t.Fatalf("result = %#v", result)
	}
	data, readErr := os.ReadFile(filepath.Join(memoryRoot, filepath.FromSlash(result.Path)))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(data), "Post-update subagent analysis was not generated") {
		t.Fatalf("failure summary = %s", data)
	}
}

func TestSummarizeUpdateFilesSkipsWhenNoUpdates(t *testing.T) {
	t.Parallel()

	called := false
	result, err := summarizeUpdateFiles(
		context.Background(),
		t.TempDir(),
		t.TempDir(),
		time.Now().UTC(),
		nil,
		SubagentSummary{},
		func(context.Context, string, string) (modelSummaryGenerator, error) {
			called = true
			return nil, errors.New("must not initialize")
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "skipped_no_updates" || called {
		t.Fatalf("result = %#v, called = %t", result, called)
	}
}

func TestBuildUpdateSubagentPromptBoundsInput(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "updates", "large.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 256)), 0o600); err != nil {
		t.Fatal(err)
	}
	prompt, supplied, truncated, err := buildUpdateSubagentPrompt(root, []string{path}, 128)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated || supplied > 128 || len(prompt) > 128 {
		t.Fatalf("prompt length=%d supplied=%d truncated=%t", len(prompt), supplied, truncated)
	}
}

func TestRenderRunMemoryIncludesPostUpdateSubagent(t *testing.T) {
	t.Parallel()

	data := renderRunMemory(
		time.Date(2026, 8, 26, 1, 0, 0, 0, time.UTC),
		[]syncResult{{Repository: "codex", Policy: "enabled", Status: "updated", Before: "a", After: "b", CommitCount: 1, SummaryStatus: "generated"}},
		&postUpdateSummaryResult{Status: "generated", Provider: "agenticopenai", Model: "subagent-model", Path: "summaries/run.md", InputFiles: 1, InputBytes: 256},
	)
	text := string(data)
	for _, want := range []string{"## Post-update subagent summary", "agenticopenai:subagent-model", "summaries/run.md"} {
		if !strings.Contains(text, want) {
			t.Errorf("run memory missing %q:\n%s", want, text)
		}
	}
}
