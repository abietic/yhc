package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGrepToolBasicSearch(t *testing.T) {
	// Create a temp directory with test files
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "hello.go"), []byte("package main\n\nfunc hello() {\n\treturn\n}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "world.go"), []byte("package main\n\nfunc world() {\n\treturn\n}\n"), 0o644)

	grep := GrepTool()
	if grep.Execute == nil {
		t.Fatal("GrepTool has no Execute function")
		return
	}

	// Test files_with_matches mode (default)
	result, err := grep.Execute(`{"pattern": "func", "path": "` + dir + `"}`)
	if err != nil {
		t.Fatalf("GrepTool failed: %v", err)
		return
	}
	if !strings.Contains(result, "hello.go") {
		t.Errorf("expected result to contain hello.go, got: %s", result)
	}
	if !strings.Contains(result, "world.go") {
		t.Errorf("expected result to contain world.go, got: %s", result)
	}
}

func TestGrepToolContentMode(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "test.txt"), []byte("line one\nline two\nline three\n"), 0o644)

	grep := GrepTool()

	result, err := grep.Execute(`{"pattern": "two", "path": "` + dir + `", "output_mode": "content"}`)
	if err != nil {
		t.Fatalf("GrepTool content mode failed: %v", err)
		return
	}
	if !strings.Contains(result, "two") {
		t.Errorf("expected result to contain 'two', got: %s", result)
	}
}

func TestGrepToolCountMode(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "test.txt"), []byte("apple\napple\norange\napple\n"), 0o644)

	grep := GrepTool()

	result, err := grep.Execute(`{"pattern": "apple", "path": "` + dir + `", "output_mode": "count"}`)
	if err != nil {
		t.Fatalf("GrepTool count mode failed: %v", err)
		return
	}
	if !strings.Contains(result, "3") {
		t.Errorf("expected result to contain count '3', got: %s", result)
	}
}

func TestGrepToolNoMatches(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello world\n"), 0o644)

	grep := GrepTool()

	result, err := grep.Execute(`{"pattern": "nonexistent_xyz", "path": "` + dir + `"}`)
	if err != nil {
		t.Fatalf("GrepTool no matches should not error: %v", err)
		return
	}
	if !strings.Contains(result, "No files found") {
		t.Errorf("expected 'No files found', got: %s", result)
	}
}

func TestGrepToolCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "test.txt"), []byte("Hello World\n"), 0o644)

	grep := GrepTool()

	// Case sensitive - should not match
	result, err := grep.Execute(`{"pattern": "hello", "path": "` + dir + `"}`)
	if err != nil {
		t.Fatalf("GrepTool failed: %v", err)
		return
	}
	if strings.Contains(result, "test.txt") {
		t.Errorf("case-sensitive search should not match 'Hello' with 'hello'")
	}

	// Case insensitive - should match
	result, err = grep.Execute(`{"pattern": "hello", "path": "` + dir + `", "-i": true}`)
	if err != nil {
		t.Fatalf("GrepTool case-insensitive failed: %v", err)
		return
	}
	if !strings.Contains(result, "test.txt") {
		t.Errorf("case-insensitive search should match 'Hello' with 'hello', got: %s", result)
	}
}

func TestGrepToolGlobFilter(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "hello.go"), []byte("func main() {}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("func main() {}\n"), 0o644)

	grep := GrepTool()

	result, err := grep.Execute(`{"pattern": "func", "path": "` + dir + `", "glob": "*.go"}`)
	if err != nil {
		t.Fatalf("GrepTool glob filter failed: %v", err)
		return
	}
	if !strings.Contains(result, "hello.go") {
		t.Errorf("expected hello.go in results, got: %s", result)
	}
	if strings.Contains(result, "hello.txt") {
		t.Errorf("should not contain hello.txt when glob is *.go, got: %s", result)
	}
}

func TestGrepToolPatternRequired(t *testing.T) {
	grep := GrepTool()
	_, err := grep.Execute(`{"path": "/tmp"}`)
	if err == nil {
		t.Error("expected error when pattern is empty")
	}
}

func TestGrepToolInvalidPath(t *testing.T) {
	grep := GrepTool()
	_, err := grep.Execute(`{"pattern": "test", "path": "/nonexistent_path_xyz_123"}`)
	if err == nil {
		t.Error("expected error for non-existent path")
	}
}

func TestGrepToolHeadLimit(t *testing.T) {
	dir := t.TempDir()
	// Create many files
	for i := 0; i < 10; i++ {
		name := filepath.Join(dir, "file"+strings.Repeat("x", i)+".txt")
		_ = os.WriteFile(name, []byte("searchterm\n"), 0o644)
	}

	grep := GrepTool()

	result, err := grep.Execute(`{"pattern": "searchterm", "path": "` + dir + `", "head_limit": 3}`)
	if err != nil {
		t.Fatalf("GrepTool head_limit failed: %v", err)
		return
	}
	// Count file entries in result
	lines := strings.Split(strings.TrimSpace(result), "\n")
	// First line is "Found N files (limit: 3)" header
	fileCount := 0
	for _, line := range lines {
		if strings.HasSuffix(line, ".txt") {
			fileCount++
		}
	}
	if fileCount > 3 {
		t.Errorf("expected at most 3 files with head_limit=3, got %d files in: %s", fileCount, result)
	}
}

func TestGrepToolRegistered(t *testing.T) {
	reg := NewRegistry()
	RegisterDefaults(reg)
	_, ok := reg.Get("Grep")
	if !ok {
		t.Error("Grep tool not found in registry")
	}
}
