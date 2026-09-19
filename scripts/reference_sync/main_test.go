package main

import (
	"strings"
	"testing"
)

func TestValidateConfigRejectsFrozenReferenceUpdates(t *testing.T) {
	t.Parallel()

	for _, id := range []string{"claude-code-ripe", "grok-bot-0.18-reconstructed"} {
		t.Run(id, func(t *testing.T) {
			t.Parallel()

			cfg := Config{
				Version:   1,
				MemoryDir: ".sync-memory",
				Repositories: []Repository{
					{
						ID:       id,
						Path:     id,
						Remote:   "https://github.com/example/reference.git",
						Upstream: "origin/main",
						Sync:     "enabled",
					},
				},
			}

			err := validateConfig(cfg)
			if err == nil || !strings.Contains(err.Error(), "must remain frozen") {
				t.Fatalf("validateConfig() error = %v, want frozen-reference error", err)
			}
		})
	}
}

func TestValidateConfigRejectsEscapingPath(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Version:   1,
		MemoryDir: ".sync-memory",
		Repositories: []Repository{
			{
				ID:       "codex",
				Path:     "../codex",
				Remote:   "https://github.com/openai/codex.git",
				Upstream: "origin/main",
				Sync:     "enabled",
			},
		},
	}

	err := validateConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "relative path") {
		t.Fatalf("validateConfig() error = %v, want relative-path error", err)
	}
}

func TestValidateConfigRequiresSupportedSummaryBackend(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{
			name: "unknown model summary backend",
			cfg: Config{
				Version:      1,
				ModelSummary: ModelSummary{Backend: "deepseek", Model: "deepseek-v4-flash"},
			},
			want: "model_summary.backend",
		},
		{
			name: "codex model is required",
			cfg: Config{
				Version:      1,
				ModelSummary: ModelSummary{Backend: summaryBackendCodex},
			},
			want: "model_summary.model",
		},
		{
			name: "post update codex model is required",
			cfg: Config{
				Version:         1,
				SubagentSummary: SubagentSummary{Backend: summaryBackendCodex},
			},
			want: "subagent_summary.model",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateConfig(tc.cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validateConfig() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestNormalizeRemote(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"git@github.com:openai/codex.git":       "github.com/openai/codex",
		"ssh://git@github.com/openai/codex.git": "github.com/openai/codex",
		"https://github.com/openai/codex.git/":  "github.com/openai/codex",
	}
	for input, want := range tests {
		if got := normalizeRemote(input); got != want {
			t.Errorf("normalizeRemote(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestParseAheadBehind(t *testing.T) {
	t.Parallel()

	ahead, behind, err := parseAheadBehind("3\t7\n")
	if err != nil {
		t.Fatalf("parseAheadBehind() error = %v", err)
	}
	if ahead != 3 || behind != 7 {
		t.Fatalf("parseAheadBehind() = %d/%d, want 3/7", ahead, behind)
	}
}

func TestRenderUpdateMemoryIncludesChangeSummary(t *testing.T) {
	t.Parallel()

	data := renderUpdateMemory(syncResult{
		Repository:  "codex",
		Remote:      "https://github.com/openai/codex.git",
		Upstream:    "origin/main",
		Status:      "updated",
		Before:      "1111111",
		After:       "2222222",
		CommitCount: 4,
		ShortStat:   "4 files changed, 10 insertions(+), 2 deletions(-)",
		CommitSubjects: []string{
			"1111111\t2026-08-25\tfirst change",
			"2222222\t2026-08-25\tlast change",
		},
		DiffStat: "file.go | 12 +++++++++---",
	})

	text := string(data)
	for _, want := range []string{
		"# Reference update: codex",
		"1111111..2222222",
		"4 files changed, 10 insertions(+), 2 deletions(-)",
		"first change",
		"file.go | 12 +++++++++---",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("renderUpdateMemory() missing %q in:\n%s", want, text)
		}
	}
}
