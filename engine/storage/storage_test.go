package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResultStorageStoreRetrievePreviewStatsAndCleanup(t *testing.T) {
	sessionDir := t.TempDir()
	storage := NewResultStorage(sessionDir)
	small := strings.Repeat("x", DefaultMaxInlineSize)
	if storage.ShouldStore(small) {
		t.Fatal("result at inline threshold should remain inline")
	}
	if stored, err := storage.Store("Read", small); err != nil || stored != nil {
		t.Fatalf("small result should not be stored, stored=%#v err=%v", stored, err)
		return
	}

	large := strings.Repeat("a", DefaultMaxPreviewSize/2) + "\n" + strings.Repeat("b", DefaultMaxInlineSize)
	stored, err := storage.Store("Grep", large)
	if err != nil {
		t.Fatalf("Store failed: %v", err)
		return
	}
	if stored == nil || stored.ToolName != "Grep" || stored.FullSize != len(large) {
		t.Fatalf("unexpected stored result: %#v", stored)
		return
	}
	if filepath.Dir(stored.FilePath) != filepath.Join(sessionDir, toolResultsSubdir) {
		t.Fatalf("unexpected storage path: %q", stored.FilePath)
	}
	content, err := os.ReadFile(stored.FilePath)
	if err != nil {
		t.Fatal(err)
		return
	}
	if string(content) != large {
		t.Fatal("stored file content mismatch")
	}
	if got, err := storage.Retrieve(stored.ID); err != nil || got != large {
		t.Fatalf("Retrieve = len %d err=%v", len(got), err)
		return
	}
	if preview, ok := storage.GetPreview(stored.ID); !ok || preview != stored.Preview {
		t.Fatalf("GetPreview = %q ok=%v want %q", preview, ok, stored.Preview)
	}
	if _, err := storage.Retrieve("missing"); err == nil {
		t.Fatal("missing retrieve should fail")
		return
	}

	stats := storage.Stats()
	if stats.Count != 1 || stats.TotalBytes != len(large) || stats.OldestEntry.IsZero() {
		t.Fatalf("unexpected stats: %#v", stats)
	}
	storage.mu.Lock()
	storage.results[stored.ID].CreatedAt = time.Now().Add(-2 * time.Hour)
	storage.mu.Unlock()
	if err := storage.Cleanup(time.Hour); err != nil {
		t.Fatalf("Cleanup failed: %v", err)
		return
	}
	if _, err := os.Stat(stored.FilePath); !os.IsNotExist(err) {
		t.Fatalf("stored file should be removed, err=%v", err)
	}
	if storage.Stats().Count != 0 {
		t.Fatalf("cleanup should remove index entry")
	}
}

func TestGeneratePreviewBreaksAtLineBoundary(t *testing.T) {
	content := "first line\nsecond line\nthird line"
	preview := GeneratePreview(content, len("first line\nsecond"))
	if preview != "first line\n[...22 more characters]" {
		t.Fatalf("unexpected preview: %q", preview)
	}
	if got := GeneratePreview("short", 10); got != "short" {
		t.Fatalf("short preview changed: %q", got)
	}
}

func TestToolResultHandlerProcessFormatAndTruncate(t *testing.T) {
	handler := NewToolResultHandler(t.TempDir(), 2)
	if got, err := handler.ProcessResult("Read", "small"); err != nil || got != "small" {
		t.Fatalf("small ProcessResult = %q err=%v", got, err)
		return
	}

	large := "alpha\n" + strings.Repeat("beta\n", DefaultMaxInlineSize/charsPerToken*charsPerToken)
	processed, err := handler.ProcessResult("Read", large)
	if err != nil {
		t.Fatalf("ProcessResult failed: %v", err)
		return
	}
	for _, want := range []string{"[Result stored - showing preview]", "alpha", "more characters stored on disk"} {
		if !strings.Contains(processed, want) {
			t.Fatalf("processed result missing %q:\n%s", want, processed)
		}
	}
	if stats := handler.storage.Stats(); stats.Count != 1 || stats.TotalBytes != len(large) {
		t.Fatalf("large result should be stored, stats=%#v", stats)
	}

	truncated := handler.TruncateResult("line1\nline2\nline3", 2)
	if !strings.Contains(truncated, defaultTruncateMessage) || !strings.Contains(truncated, "characters omitted") {
		t.Fatalf("truncate marker missing: %q", truncated)
	}
	if got := handler.TruncateResult("short", 10); got != "short" {
		t.Fatalf("short truncate changed: %q", got)
	}

	formatted := handler.FormatToolResult("Grep", strings.Repeat("x", handler.maxInlineChars()/4+1), true)
	if !strings.HasPrefix(formatted, "[ERROR] [Grep]\n") {
		t.Fatalf("formatted error/header mismatch: %q", formatted)
	}
	if got := handler.FormatToolResult("", "small", false); got != "small" {
		t.Fatalf("small format changed: %q", got)
	}
}

func TestProcessToolOutputDefaultAndFallback(t *testing.T) {
	old := DefaultToolResultHandler
	t.Cleanup(func() { DefaultToolResultHandler = old })

	DefaultToolResultHandler = nil
	short := "short"
	if got := ProcessToolOutput("Read", short); got != short {
		t.Fatalf("short fallback output changed: %q", got)
	}
	large := strings.Repeat("x", DefaultMaxInlineSize+1)
	fallback := ProcessToolOutput("Read", large)
	if !strings.Contains(fallback, defaultTruncateMessage) {
		t.Fatalf("fallback should truncate without initialized handler")
	}

	dir := t.TempDir()
	InitToolResultHandler(dir)
	if DefaultToolResultHandler == nil {
		t.Fatal("InitToolResultHandler did not initialize handler")
		return
	}
	out := ProcessToolOutput("Read", large)
	if !strings.Contains(out, "[Result stored - showing preview]") {
		t.Fatalf("initialized handler should store large output, got prefix %q", out[:min(len(out), 80)])
	}
	if entries, err := os.ReadDir(filepath.Join(dir, toolResultsSubdir)); err != nil || len(entries) != 1 {
		t.Fatalf("expected one stored output, entries=%#v err=%v", entries, err)
		return
	}
}

func TestProcessResultFallsBackToTruncationWhenStorageFails(t *testing.T) {
	fileAsDir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(fileAsDir, []byte("file"), 0o644); err != nil {
		t.Fatal(err)
		return
	}
	handler := NewToolResultHandler(fileAsDir, 1)
	large := strings.Repeat("line1\n", DefaultMaxInlineSize/charsPerToken*charsPerToken)
	out, err := handler.ProcessResult("Read", large)
	if err != nil {
		t.Fatalf("ProcessResult should absorb storage errors, got %v", err)
		return
	}
	if !strings.Contains(out, defaultTruncateMessage) {
		t.Fatalf("storage failure should fall back to truncation, got %q", out)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
