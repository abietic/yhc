package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeModelSummaryGenerator struct {
	provider string
	model    string
	prompt   string
	result   string
	err      error
}

func (f *fakeModelSummaryGenerator) Generate(_ context.Context, prompt string) (string, error) {
	f.prompt = prompt
	return f.result, f.err
}

func (f *fakeModelSummaryGenerator) Identity() (string, string) {
	return f.provider, f.model
}

func TestParseCodexAgentMessageUsesFinalCompletedMessage(t *testing.T) {
	t.Parallel()

	data := []byte("{\"type\":\"thread.started\",\"thread_id\":\"test\"}\n" +
		"{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"first\"}}\n" +
		"{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"final\"}}\n")
	got, err := parseCodexAgentMessage(data)
	if err != nil {
		t.Fatalf("parseCodexAgentMessage() error = %v", err)
	}
	if got != "final" {
		t.Fatalf("parseCodexAgentMessage() = %q, want final", got)
	}
}

func TestCodexSummaryGeneratorUsesReadOnlyCLIAndInputOnlyPrompt(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	command := filepath.Join(dir, "codex-fixture")
	script := strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		"for expected in --ephemeral --json --ignore-user-config --ignore-rules --skip-git-repo-check; do",
		"  case \" $* \" in *\" $expected \"*) ;; *) exit 41 ;; esac",
		"done",
		"case \"$*\" in *\" --sandbox read-only \"*) ;; *) exit 42 ;; esac",
		"case \"$*\" in *\" -m gpt-5.6-luna \"*) ;; *) exit 43 ;; esac",
		"case \"$*\" in *\"-C\"*) ;; *) exit 44 ;; esac",
		"input=$(cat)",
		"case \"$input\" in *\"<reference_sync_input>\"*\"evidence\"*) ;; *) exit 45 ;; esac",
		"printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"## Codex\\nEvidence-backed summary\"}}'",
	}, "\n")
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	generator := &codexModelSummaryGenerator{
		command:   command,
		modelName: "gpt-5.6-luna",
		workDir:   dir,
		environment: []string{
			"PATH=/usr/bin:/bin",
		},
	}
	got, err := generator.Generate(context.Background(), "evidence")
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if got != "## Codex\nEvidence-backed summary" {
		t.Fatalf("Generate() = %q", got)
	}
	provider, modelName := generator.Identity()
	if provider != summaryBackendCodex || modelName != "gpt-5.6-luna" {
		t.Fatalf("Identity() = %s:%s", provider, modelName)
	}
}

func TestShouldStripCodexEnvironmentVariable(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"ANTHROPIC_BASE_URL",
		"ANTHROPIC_API_KEY",
		"DEEPSEEK_API_KEY",
		"OPENAI_API_KEY",
		"PROV_MODEL",
		"CLAUDE_CODE_SUBAGENT_MODEL",
		"YHC_REFERENCE_SYNC_MODEL",
		"CODEX_THREAD_ID",
	} {
		if !shouldStripCodexEnvironmentVariable(name) {
			t.Errorf("shouldStripCodexEnvironmentVariable(%q) = false", name)
		}
	}
	for _, name := range []string{"CODEX_HOME", "PATH", "HOME"} {
		if shouldStripCodexEnvironmentVariable(name) {
			t.Errorf("shouldStripCodexEnvironmentVariable(%q) = true", name)
		}
	}
}

func TestGenerateModelSummariesUsesModelOutputAndEvidencePrompt(t *testing.T) {
	t.Parallel()

	generator := &fakeModelSummaryGenerator{
		provider: "agenticopenai",
		model:    "summary-model",
		result:   "## 变更结论\n新增了受保护的同步边界。",
	}
	results := []syncResult{{
		Repository:     "codex",
		Status:         "pending_summary",
		Before:         "1111111",
		After:          "2222222",
		CommitCount:    1,
		ShortStat:      "1 file changed, 3 insertions(+)",
		DiffStat:       "sync.go | 3 +++",
		ChangeDiff:     "diff --git a/sync.go b/sync.go\n+guard",
		DiffBytes:      46,
		CommitSubjects: []string{"2222222\t2026-08-26\tadd guard"},
	}}

	err := generateModelSummaries(
		context.Background(),
		t.TempDir(),
		ModelSummary{MaxInputBytes: 2048, TimeoutSeconds: 1},
		results,
		func(context.Context, string) (modelSummaryGenerator, error) {
			return generator, nil
		},
	)
	if err != nil {
		t.Fatalf("generateModelSummaries() error = %v", err)
	}
	if results[0].SummaryStatus != "generated" {
		t.Fatalf("summary status = %q, want generated", results[0].SummaryStatus)
	}
	if results[0].ModelSummary != generator.result {
		t.Fatalf("model summary = %q, want %q", results[0].ModelSummary, generator.result)
	}
	if results[0].SummaryProvider != "agenticopenai" || results[0].SummaryModel != "summary-model" {
		t.Fatalf("summary identity = %s:%s", results[0].SummaryProvider, results[0].SummaryModel)
	}
	if len(generator.prompt) > 2048 {
		t.Fatalf("model prompt length = %d, want <= 2048", len(generator.prompt))
	}
	for _, want := range []string{
		"codex",
		"1111111..2222222",
		"<commit_subjects>",
		"<diff>",
		"不可信的参考材料",
		"不要把上游变更自动写成 YHC backlog",
	} {
		if !strings.Contains(generator.prompt, want) {
			t.Errorf("model prompt missing %q:\n%s", want, generator.prompt)
		}
	}
}

func TestBuildModelSummaryPromptBoundsCompleteInput(t *testing.T) {
	t.Parallel()

	result := syncResult{
		Repository:     "opencode",
		Before:         "1111111",
		After:          "2222222",
		CommitCount:    1,
		ShortStat:      "1 file changed, 1 insertion(+)",
		DiffStat:       "file.go | 1 +",
		ChangeDiff:     strings.Repeat("x", 4096),
		CommitSubjects: []string{"2222222\t2026-08-27\tlarge change"},
	}
	full := buildModelSummaryPrompt(result)
	maxBytes := len(full) - 128
	bounded := buildModelSummaryPromptWithLimit(result, maxBytes)
	if len(bounded) > maxBytes {
		t.Fatalf("bounded prompt length = %d, want <= %d", len(bounded), maxBytes)
	}
	if !strings.HasSuffix(bounded, "\n</diff>\n") {
		t.Fatalf("bounded prompt lost diff closing boundary: %q", bounded[len(bounded)-32:])
	}
	if !strings.Contains(bounded, "[content truncated before model analysis]") {
		t.Fatalf("bounded prompt missing truncation marker")
	}
}

func TestGenerateModelSummariesMarksFailureWithoutFallback(t *testing.T) {
	t.Parallel()

	results := []syncResult{{
		Repository: "codex",
		Status:     "pending_summary",
	}}
	err := generateModelSummaries(
		context.Background(),
		t.TempDir(),
		ModelSummary{MaxInputBytes: 1024, TimeoutSeconds: 1},
		results,
		func(context.Context, string) (modelSummaryGenerator, error) {
			return &fakeModelSummaryGenerator{err: errors.New("provider unavailable")}, nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "provider unavailable") {
		t.Fatalf("generateModelSummaries() error = %v, want provider failure", err)
	}
	if results[0].SummaryStatus != "failed" {
		t.Fatalf("summary status = %q, want failed", results[0].SummaryStatus)
	}
	if results[0].ModelSummary != "" {
		t.Fatalf("unexpected fallback summary %q", results[0].ModelSummary)
	}
}

func TestFinalizePreparedUpdatesBlocksMergeWithoutModelSummary(t *testing.T) {
	t.Parallel()

	results := []syncResult{{
		Repository:    "codex",
		Status:        "pending_summary",
		SummaryStatus: "failed",
		SummaryError:  "provider unavailable",
	}}
	finalizePreparedUpdates([]Repository{{ID: "codex", Path: "missing"}}, t.TempDir(), results)
	if results[0].Status != "blocked_summary" {
		t.Fatalf("status = %q, want blocked_summary", results[0].Status)
	}
	if !strings.Contains(results[0].Detail, "provider unavailable") {
		t.Fatalf("detail = %q, want model failure", results[0].Detail)
	}
}

func TestTruncateTextPreservesLimitAndMarksInput(t *testing.T) {
	t.Parallel()

	value, truncated := truncateText("前缀\n"+strings.Repeat("x", 64), 24)
	if !truncated {
		t.Fatal("truncateText() did not report truncation")
	}
	if len(value) > 24 {
		t.Fatalf("truncated length = %d, want <= 24", len(value))
	}
	if !strings.Contains(value, "content truncated") {
		t.Fatalf("truncated value missing marker: %q", value)
	}
}

func TestRenderUpdateMemoryLabelsModelAnalysis(t *testing.T) {
	t.Parallel()

	data := renderUpdateMemory(syncResult{
		Repository:      "codex",
		Before:          "1111111",
		After:           "2222222",
		CommitCount:     1,
		SummaryStatus:   "generated",
		SummaryProvider: "agenticopenai",
		SummaryModel:    "summary-model",
		ModelSummary:    "## 变更结论\n新增同步保护。",
		ChangeDiff:      "diff --git ...",
		DiffBytes:       16,
		DiffStat:        "sync.go | 1 +",
		CommitSubjects:  []string{"commit title"},
	})
	text := string(data)
	for _, want := range []string{
		"## Model analysis",
		"agenticopenai:summary-model",
		"新增同步保护",
		"## Commit metadata (evidence only)",
		"commit title",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("renderUpdateMemory() missing %q:\n%s", want, text)
		}
	}
}
