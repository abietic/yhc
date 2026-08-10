package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGlobToolBasicPattern(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "hello.go"), []byte("package main\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "world.go"), []byte("package main\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hello\n"), 0o644)

	glob := GlobTool()
	if glob.Execute == nil {
		t.Fatal("GlobTool has no Execute function")
		return
	}

	result, err := glob.Execute(`{"pattern": "*.go", "path": "` + dir + `"}`)
	if err != nil {
		t.Fatalf("GlobTool failed: %v", err)
		return
	}
	if !strings.Contains(result, "hello.go") {
		t.Errorf("expected result to contain hello.go, got: %s", result)
	}
	if !strings.Contains(result, "world.go") {
		t.Errorf("expected result to contain world.go, got: %s", result)
	}
	if strings.Contains(result, "readme.txt") {
		t.Errorf("should not contain readme.txt when pattern is *.go, got: %s", result)
	}
}

func TestGlobToolRecursivePattern(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "sub")
	_ = os.MkdirAll(subDir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "top.go"), []byte("package main\n"), 0o644)
	_ = os.WriteFile(filepath.Join(subDir, "nested.go"), []byte("package sub\n"), 0o644)

	glob := GlobTool()

	result, err := glob.Execute(`{"pattern": "**/*.go", "path": "` + dir + `"}`)
	if err != nil {
		t.Fatalf("GlobTool recursive failed: %v", err)
		return
	}
	if !strings.Contains(result, "top.go") {
		t.Errorf("expected result to contain top.go, got: %s", result)
	}
	if !strings.Contains(result, "nested.go") {
		t.Errorf("expected result to contain nested.go, got: %s", result)
	}
}

func TestGlobToolNoMatches(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello\n"), 0o644)

	glob := GlobTool()

	result, err := glob.Execute(`{"pattern": "*.xyz", "path": "` + dir + `"}`)
	if err != nil {
		t.Fatalf("GlobTool no matches should not error: %v", err)
		return
	}
	if !strings.Contains(result, "No files found") {
		t.Errorf("expected 'No files found', got: %s", result)
	}
}

func TestGlobToolPatternRequired(t *testing.T) {
	glob := GlobTool()
	_, err := glob.Execute(`{"path": "/tmp"}`)
	if err == nil {
		t.Error("expected error when pattern is empty")
	}
}

func TestGlobToolInvalidPath(t *testing.T) {
	glob := GlobTool()
	_, err := glob.Execute(`{"pattern": "*.go", "path": "/nonexistent_path_xyz_123"}`)
	if err == nil {
		t.Error("expected error for non-existent path")
	}
}

func TestGlobToolNotADirectory(t *testing.T) {
	f, err := os.CreateTemp("", "eino-agent-glob-test-*.txt")
	if err != nil {
		t.Fatal(err)
		return
	}
	defer func() { _ = os.Remove(f.Name()) }()
	_ = f.Close()

	glob := GlobTool()
	_, err = glob.Execute(`{"pattern": "*.go", "path": "` + f.Name() + `"}`)
	if err == nil {
		t.Error("expected error when path is not a directory")
	}
}

func TestGlobToolSortedOutput(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "charlie.go"), []byte("x\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "alpha.go"), []byte("x\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "bravo.go"), []byte("x\n"), 0o644)

	glob := GlobTool()

	result, err := glob.Execute(`{"pattern": "*.go", "path": "` + dir + `"}`)
	if err != nil {
		t.Fatalf("GlobTool sorted failed: %v", err)
		return
	}

	lines := strings.Split(strings.TrimSpace(result), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines, got %d: %s", len(lines), result)
	}

	// Verify lexical ordering
	for i := 1; i < len(lines); i++ {
		if lines[i] < lines[i-1] {
			t.Errorf("results not sorted: %q appears after %q", lines[i], lines[i-1])
		}
	}
}

func TestGlobToolRegistered(t *testing.T) {
	reg := NewRegistry()
	RegisterDefaults(reg)
	_, ok := reg.Get("Glob")
	if !ok {
		t.Error("Glob tool not found in registry")
	}
}

func TestGlobToolExcludesGitDir(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	_ = os.MkdirAll(gitDir, 0o755)
	_ = os.WriteFile(filepath.Join(gitDir, "config"), []byte("git config\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644)

	glob := GlobTool()

	result, err := glob.Execute(`{"pattern": "**/*", "path": "` + dir + `"}`)
	if err != nil {
		t.Fatalf("GlobTool git exclusion failed: %v", err)
		return
	}
	if strings.Contains(result, ".git") {
		t.Errorf("should not contain .git files, got: %s", result)
	}
	if !strings.Contains(result, "main.go") {
		t.Errorf("expected to contain main.go, got: %s", result)
	}
}
