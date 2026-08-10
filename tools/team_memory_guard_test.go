package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteRejectsTeamMemorySecretButAllowsOrdinaryFile(t *testing.T) {
	teamDir := filepath.Join(t.TempDir(), "team")
	t.Setenv("YHC_TEAM_MEMORY_DIR", teamDir)
	t.Setenv("YHC_DISABLE_AUTO_MEMORY", "")
	t.Setenv("YHC_SIMPLE", "")
	secret := "ghp_abcdefghijklmnopqrstuvwxyzABCDEFGHIJ"
	write := WriteTool()

	teamPath := filepath.Join(teamDir, "topic.md")
	input, _ := json.Marshal(map[string]string{"file_path": teamPath, "content": secret})
	if _, err := write.Execute(string(input)); err == nil || !strings.Contains(err.Error(), "shared team memory") {
		t.Fatalf("expected team secret rejection, got %v", err)
	}
	if _, err := os.Stat(teamPath); !os.IsNotExist(err) {
		t.Fatalf("rejected team file should not exist: %v", err)
	}

	ordinaryPath := filepath.Join(t.TempDir(), "ordinary.txt")
	input, _ = json.Marshal(map[string]string{"file_path": ordinaryPath, "content": secret})
	if _, err := write.Execute(string(input)); err != nil {
		t.Fatalf("ordinary file should retain existing behavior: %v", err)
	}
}

func TestEditRejectsSecretInFinalTeamMemoryContent(t *testing.T) {
	teamDir := filepath.Join(t.TempDir(), "team")
	t.Setenv("YHC_TEAM_MEMORY_DIR", teamDir)
	t.Setenv("YHC_DISABLE_AUTO_MEMORY", "")
	t.Setenv("YHC_SIMPLE", "")
	if err := os.MkdirAll(teamDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(teamDir, "topic.md")
	if err := os.WriteFile(path, []byte("safe content"), 0o644); err != nil {
		t.Fatal(err)
	}
	RecordFileRead(path, false)
	input, _ := json.Marshal(map[string]any{
		"file_path":  path,
		"old_string": "safe content",
		"new_string": "ghp_abcdefghijklmnopqrstuvwxyzABCDEFGHIJ",
	})
	if _, err := EditTool().Execute(string(input)); err == nil || !strings.Contains(err.Error(), "shared team memory") {
		t.Fatalf("expected edit rejection, got %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "safe content" {
		t.Fatalf("rejected edit changed file: %q err=%v", content, err)
	}
}
