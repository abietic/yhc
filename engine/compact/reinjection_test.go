package compact

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreatePostCompactFileAttachmentsBasic(t *testing.T) {
	state := []FileStateEntry{
		{Filename: "/tmp/a.go", Content: "package main", Timestamp: 100},
		{Filename: "/tmp/b.go", Content: "package b", Timestamp: 200},
		{Filename: "/tmp/c.go", Content: "package c", Timestamp: 50},
	}

	result := CreatePostCompactFileAttachments(state, 2, nil)
	if len(result) != 2 {
		t.Fatalf("expected 2 attachments, got %d", len(result))
	}
	// Most recent first
	if !strings.Contains(result[0].Content, "b.go") {
		t.Fatalf("expected most recent file first, got %q", result[0].Content)
	}
	if !strings.Contains(result[1].Content, "a.go") {
		t.Fatalf("expected second most recent file second, got %q", result[1].Content)
	}
}

func TestCreatePostCompactFileAttachmentsExcluded(t *testing.T) {
	state := []FileStateEntry{
		{Filename: "/tmp/plan.md", Content: "plan content", Timestamp: 300},
		{Filename: "/tmp/code.go", Content: "package main", Timestamp: 200},
	}

	excluded := map[string]bool{"/tmp/plan.md": true}
	result := CreatePostCompactFileAttachments(state, 5, excluded)
	if len(result) != 1 {
		t.Fatalf("expected 1 attachment after exclusion, got %d", len(result))
	}
	if !strings.Contains(result[0].Content, "code.go") {
		t.Fatal("expected code.go to be the remaining attachment")
	}
}

func TestCreatePostCompactFileAttachmentsEmpty(t *testing.T) {
	result := CreatePostCompactFileAttachments(nil, 5, nil)
	if result != nil {
		t.Fatalf("expected nil for empty state, got %v", result)
		return
	}
}

func TestCreatePostCompactFileAttachmentsTokenBudget(t *testing.T) {
	// Create entries with large content that exceeds per-file budget
	bigContent := strings.Repeat("x", PostCompactMaxTokensPerFile*4+100)
	state := []FileStateEntry{
		{Filename: "/tmp/big.go", Content: bigContent, Timestamp: 100},
	}

	result := CreatePostCompactFileAttachments(state, 5, nil)
	if len(result) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(result))
	}
	// Content should be truncated
	if !strings.Contains(result[0].Content, skillTruncationMarker) {
		t.Fatal("expected content to be truncated with marker")
	}
}

func TestCreatePlanAttachmentIfNeeded(t *testing.T) {
	// Create temp plan file
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.md")
	_ = os.WriteFile(planPath, []byte("# My Plan\n- Step 1\n- Step 2"), 0o644)

	result := CreatePlanAttachmentIfNeeded(planPath)
	if result == nil {
		t.Fatal("expected plan attachment")
		return
	}
	if !strings.Contains(result.Content, "My Plan") {
		t.Fatal("expected plan content in attachment")
	}
	if !strings.Contains(result.Content, "Step 1") {
		t.Fatal("expected plan steps in attachment")
	}
}

func TestCreatePlanAttachmentIfNeededNoFile(t *testing.T) {
	result := CreatePlanAttachmentIfNeeded("/nonexistent/plan.md")
	if result != nil {
		t.Fatal("expected nil for nonexistent plan file")
		return
	}
}

func TestCreatePlanAttachmentIfNeededEmptyPath(t *testing.T) {
	result := CreatePlanAttachmentIfNeeded("")
	if result != nil {
		t.Fatal("expected nil for empty path")
		return
	}
}

func TestCreateSkillAttachmentIfNeeded(t *testing.T) {
	skills := []SkillEntry{
		{SkillName: "test-skill", SkillPath: "/skills/test.md", Content: "# Test Skill\nDo stuff", InvokedAt: 100},
		{SkillName: "older-skill", SkillPath: "/skills/old.md", Content: "# Old Skill", InvokedAt: 50},
	}

	result := CreateSkillAttachmentIfNeeded(skills)
	if result == nil {
		t.Fatal("expected skill attachment")
		return
	}
	if !strings.Contains(result.Content, "test-skill") {
		t.Fatal("expected skill name in attachment")
	}
	if !strings.Contains(result.Content, "Do stuff") {
		t.Fatal("expected skill content in attachment")
	}
	// Most recent first
	testIdx := strings.Index(result.Content, "test-skill")
	oldIdx := strings.Index(result.Content, "older-skill")
	if testIdx > oldIdx {
		t.Fatal("expected most recent skill first")
	}
}

func TestCreateSkillAttachmentIfNeededEmpty(t *testing.T) {
	result := CreateSkillAttachmentIfNeeded(nil)
	if result != nil {
		t.Fatal("expected nil for empty skills")
		return
	}
}

func TestTruncateToTokens(t *testing.T) {
	short := "hello"
	if truncateToTokens(short, 100) != short {
		t.Fatal("short content should not be truncated")
	}

	long := strings.Repeat("x", 1000)
	truncated := truncateToTokens(long, 50) // ~200 chars budget
	if !strings.HasSuffix(truncated, skillTruncationMarker) {
		t.Fatal("expected truncation marker")
	}
	if len(truncated) > 250 {
		t.Fatalf("expected truncated length to be reasonable, got %d", len(truncated))
	}
}
