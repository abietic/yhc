package promptctx

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- ContextRefresher basic tests ---

func TestContextRefresher_Initialize(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	// Create AGENTS.md
	if err := os.WriteFile(filepath.Join(tmp, "AGENTS.md"), []byte("project rules"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	r := NewContextRefresher(ContextRefresherConfig{
		CWD:        tmp,
		BasePrompt: "You are an AI assistant.",
	})

	if err := r.Initialize(); err != nil {
		t.Fatal(err)
		return
	}

	prompt := r.GetPrompt()
	if !strings.Contains(prompt, "You are an AI assistant.") {
		t.Fatal("expected base prompt in output")
	}
	if !strings.Contains(prompt, "project rules") {
		t.Fatal("expected project instructions in output")
	}
	if r.LastAssemblyTime().IsZero() {
		t.Fatal("expected non-zero assembly time")
	}
}

func TestContextRefresher_CachedPromptNoRebuild(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	if err := os.WriteFile(filepath.Join(tmp, "AGENTS.md"), []byte("rules"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	r := NewContextRefresher(ContextRefresherConfig{
		CWD:        tmp,
		BasePrompt: "Base.",
	})
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
		return
	}

	firstTime := r.LastAssemblyTime()

	// Refresh without changes should not rebuild.
	changed, err := r.Refresh()
	if err != nil {
		t.Fatal(err)
		return
	}
	if changed {
		t.Fatal("expected no rebuild when nothing changed")
	}
	if !r.LastAssemblyTime().Equal(firstTime) {
		t.Fatal("assembly time should not change when nothing changed")
	}
}

func TestContextRefresher_RefreshDetectsInstructionChange(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	agentsMd := filepath.Join(tmp, "AGENTS.md")
	if err := os.WriteFile(agentsMd, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	r := NewContextRefresher(ContextRefresherConfig{
		CWD:        tmp,
		BasePrompt: "Base.",
	})
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
		return
	}

	if !strings.Contains(r.GetPrompt(), "v1") {
		t.Fatal("expected v1 in initial prompt")
	}

	// Modify file
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(agentsMd, []byte("v2-updated"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	changed, err := r.Refresh()
	if err != nil {
		t.Fatal(err)
		return
	}
	if !changed {
		t.Fatal("expected refresh to detect change")
	}
	if !strings.Contains(r.GetPrompt(), "v2-updated") {
		t.Fatal("expected v2-updated in refreshed prompt")
	}
}

func TestContextRefresher_HasChanged(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	agentsMd := filepath.Join(tmp, "AGENTS.md")
	if err := os.WriteFile(agentsMd, []byte("rules"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	r := NewContextRefresher(ContextRefresherConfig{
		CWD:        tmp,
		BasePrompt: "Base.",
	})
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
		return
	}

	if r.HasChanged() {
		t.Fatal("expected no change immediately after init")
	}

	// Modify
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(agentsMd, []byte("rules v2 changed"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	if !r.HasChanged() {
		t.Fatal("expected HasChanged to detect modification")
	}
}

// --- Memory context tests ---

func TestContextRefresher_MemoryContext(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	// Create memory directory with files
	memDir := filepath.Join(tmp, ".claude", "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(memDir, "preferences.md"), []byte("Use tabs for indentation."), 0o644); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(memDir, "context.md"), []byte("Working on project X."), 0o644); err != nil {
		t.Fatal(err)
		return
	}
	// Non-md files should be ignored
	if err := os.WriteFile(filepath.Join(memDir, "notes.txt"), []byte("ignore me"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	r := NewContextRefresher(ContextRefresherConfig{
		CWD:        tmp,
		BasePrompt: "Base.",
		MemoryDir:  memDir,
	})
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
		return
	}

	prompt := r.GetPrompt()
	if !strings.Contains(prompt, "Use tabs for indentation.") {
		t.Fatal("expected memory content in prompt")
	}
	if !strings.Contains(prompt, "Working on project X.") {
		t.Fatal("expected memory content in prompt")
	}
	if strings.Contains(prompt, "ignore me") {
		t.Fatal("non-md file should not be in prompt")
	}

	// Verify memory entries API
	entries := r.GetMemoryEntries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 memory entries, got %d", len(entries))
	}
}

func TestContextRefresher_MemoryContextRefresh(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	memDir := filepath.Join(tmp, "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(memDir, "note.md"), []byte("initial note"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	r := NewContextRefresher(ContextRefresherConfig{
		CWD:        tmp,
		BasePrompt: "Base.",
		MemoryDir:  memDir,
	})
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
		return
	}

	// Add new memory file
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(memDir, "new.md"), []byte("new memory"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	changed, err := r.Refresh()
	if err != nil {
		t.Fatal(err)
		return
	}
	if !changed {
		t.Fatal("expected refresh to detect new memory file")
	}
	if !strings.Contains(r.GetPrompt(), "new memory") {
		t.Fatal("expected new memory content in prompt")
	}
}

func TestContextRefresher_NoMemoryDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	r := NewContextRefresher(ContextRefresherConfig{
		CWD:        tmp,
		BasePrompt: "Base.",
		MemoryDir:  "", // No memory directory
	})
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
		return
	}

	// Should work without memory
	if !strings.Contains(r.GetPrompt(), "Base.") {
		t.Fatal("expected base prompt")
	}
	if strings.Contains(r.GetPrompt(), "Memory") {
		t.Fatal("expected no memory section when memoryDir is empty")
	}
}

// --- Skill context tests ---

func TestContextRefresher_SkillContext(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	r := NewContextRefresher(ContextRefresherConfig{
		CWD:        tmp,
		BasePrompt: "Base.",
	})
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
		return
	}

	// Set skills
	r.SetSkills([]SkillDescription{
		{Name: "autopilot", Description: "End-to-end task execution"},
		{Name: "debugging", Description: "Systematic bug diagnosis"},
	})

	// Force rebuild
	r.mu.Lock()
	r.assembleLocked()
	r.mu.Unlock()

	prompt := r.GetPrompt()
	if !strings.Contains(prompt, "autopilot") {
		t.Fatal("expected skill name in prompt")
	}
	if !strings.Contains(prompt, "End-to-end task execution") {
		t.Fatal("expected skill description in prompt")
	}
	if !strings.Contains(prompt, "debugging") {
		t.Fatal("expected second skill in prompt")
	}

	// Verify GetSkills
	skills := r.GetSkills()
	if len(skills) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(skills))
	}
}

// --- Tool context tests ---

func TestContextRefresher_ToolContext(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	r := NewContextRefresher(ContextRefresherConfig{
		CWD:        tmp,
		BasePrompt: "Base.",
	})
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
		return
	}

	// Set tools
	r.SetTools([]ToolInfo{
		{Name: "Read", Description: "Read a file from disk"},
		{Name: "Write", Description: "Write a file to disk"},
	})

	// Force rebuild
	r.mu.Lock()
	r.assembleLocked()
	r.mu.Unlock()

	prompt := r.GetPrompt()
	if !strings.Contains(prompt, "Read") {
		t.Fatal("expected tool name in prompt")
	}
	if !strings.Contains(prompt, "Read a file from disk") {
		t.Fatal("expected tool description in prompt")
	}
}

// --- Dynamic context tests ---

func TestContextRefresher_DynamicContextAdd(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	// Create a file to add as context
	contextFile := filepath.Join(tmp, "extra.md")
	if err := os.WriteFile(contextFile, []byte("Extra context content."), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	r := NewContextRefresher(ContextRefresherConfig{
		CWD:        tmp,
		BasePrompt: "Base.",
	})
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
		return
	}

	// Add context
	if err := r.AddContext(contextFile); err != nil {
		t.Fatal(err)
		return
	}

	// Force rebuild
	r.mu.Lock()
	r.assembleLocked()
	r.mu.Unlock()

	prompt := r.GetPrompt()
	if !strings.Contains(prompt, "Extra context content.") {
		t.Fatal("expected dynamic context in prompt")
	}

	// List context
	entries := r.ListContext()
	if len(entries) != 1 {
		t.Fatalf("expected 1 dynamic context entry, got %d", len(entries))
	}
	if entries[0].Path != contextFile {
		t.Fatalf("expected path %q, got %q", contextFile, entries[0].Path)
	}
}

func TestContextRefresher_DynamicContextRemove(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	contextFile := filepath.Join(tmp, "extra.md")
	if err := os.WriteFile(contextFile, []byte("Extra content."), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	r := NewContextRefresher(ContextRefresherConfig{
		CWD:        tmp,
		BasePrompt: "Base.",
	})
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
		return
	}

	if err := r.AddContext(contextFile); err != nil {
		t.Fatal(err)
		return
	}

	// Remove
	removed := r.RemoveContext(contextFile)
	if !removed {
		t.Fatal("expected RemoveContext to return true")
	}

	// Should be gone
	entries := r.ListContext()
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries after removal, got %d", len(entries))
	}

	// Remove again should return false
	removed = r.RemoveContext(contextFile)
	if removed {
		t.Fatal("expected RemoveContext to return false for absent entry")
	}
}

func TestContextRefresher_DynamicContextClear(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	for i := 0; i < 3; i++ {
		f := filepath.Join(tmp, fmt.Sprintf("ctx%d.md", i))
		if err := os.WriteFile(f, []byte(fmt.Sprintf("content %d", i)), 0o644); err != nil {
			t.Fatal(err)
			return
		}
	}

	r := NewContextRefresher(ContextRefresherConfig{
		CWD:        tmp,
		BasePrompt: "Base.",
	})
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
		return
	}

	for i := 0; i < 3; i++ {
		if err := r.AddContext(filepath.Join(tmp, fmt.Sprintf("ctx%d.md", i))); err != nil {
			t.Fatal(err)
			return
		}
	}

	if len(r.ListContext()) != 3 {
		t.Fatal("expected 3 entries before clear")
	}

	r.ClearDynamicContext()

	if len(r.ListContext()) != 0 {
		t.Fatal("expected 0 entries after clear")
	}
}

func TestContextRefresher_DynamicContextNonexistentFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	r := NewContextRefresher(ContextRefresherConfig{
		CWD:        tmp,
		BasePrompt: "Base.",
	})
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
		return
	}

	err := r.AddContext(filepath.Join(tmp, "nonexistent.md"))
	if err == nil {
		t.Fatal("expected error for nonexistent file")
		return
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected 'not found' error, got: %v", err)
	}
}

func TestContextRefresher_DynamicContextEmptyFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	emptyFile := filepath.Join(tmp, "empty.md")
	if err := os.WriteFile(emptyFile, []byte(""), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	r := NewContextRefresher(ContextRefresherConfig{
		CWD:        tmp,
		BasePrompt: "Base.",
	})
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
		return
	}

	err := r.AddContext(emptyFile)
	if err == nil {
		t.Fatal("expected error for empty file")
		return
	}
	if !strings.Contains(err.Error(), "empty or binary") {
		t.Fatalf("expected 'empty or binary' error, got: %v", err)
	}
}

// --- Priority ordering test ---

func TestContextRefresher_PromptOrdering(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	// Create project instructions
	if err := os.WriteFile(filepath.Join(tmp, "AGENTS.md"), []byte("PROJECT_MARKER"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	// Create memory dir
	memDir := filepath.Join(tmp, "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(memDir, "mem.md"), []byte("MEMORY_MARKER"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	// Create context file
	ctxFile := filepath.Join(tmp, "dynamic.md")
	if err := os.WriteFile(ctxFile, []byte("DYNAMIC_MARKER"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	r := NewContextRefresher(ContextRefresherConfig{
		CWD:                tmp,
		BasePrompt:         "BASE_MARKER",
		CustomInstructions: "CUSTOM_MARKER",
		MemoryDir:          memDir,
	})
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
		return
	}

	// Set skills and tools
	r.SetSkills([]SkillDescription{{Name: "SKILL_MARKER", Description: "test"}})
	r.SetTools([]ToolInfo{{Name: "TOOL_MARKER", Description: "test"}})

	// Add dynamic context
	if err := r.AddContext(ctxFile); err != nil {
		t.Fatal(err)
		return
	}

	// Rebuild
	r.mu.Lock()
	r.assembleLocked()
	r.mu.Unlock()

	prompt := r.GetPrompt()

	// Verify ordering: base < project < memory < skills < tools < dynamic < custom
	baseIdx := strings.Index(prompt, "BASE_MARKER")
	projIdx := strings.Index(prompt, "PROJECT_MARKER")
	memIdx := strings.Index(prompt, "MEMORY_MARKER")
	skillIdx := strings.Index(prompt, "SKILL_MARKER")
	toolIdx := strings.Index(prompt, "TOOL_MARKER")
	dynIdx := strings.Index(prompt, "DYNAMIC_MARKER")
	customIdx := strings.Index(prompt, "CUSTOM_MARKER")

	if baseIdx == -1 || projIdx == -1 || memIdx == -1 || skillIdx == -1 || toolIdx == -1 || dynIdx == -1 || customIdx == -1 {
		t.Fatalf("missing markers in prompt: base=%d proj=%d mem=%d skill=%d tool=%d dyn=%d custom=%d",
			baseIdx, projIdx, memIdx, skillIdx, toolIdx, dynIdx, customIdx)
	}

	if baseIdx >= projIdx {
		t.Fatalf("expected base(%d) < project(%d)", baseIdx, projIdx)
	}
	if projIdx >= memIdx {
		t.Fatalf("expected project(%d) < memory(%d)", projIdx, memIdx)
	}
	if memIdx >= skillIdx {
		t.Fatalf("expected memory(%d) < skill(%d)", memIdx, skillIdx)
	}
	if skillIdx >= toolIdx {
		t.Fatalf("expected skill(%d) < tool(%d)", skillIdx, toolIdx)
	}
	if toolIdx >= dynIdx {
		t.Fatalf("expected tool(%d) < dynamic(%d)", toolIdx, dynIdx)
	}
	if dynIdx >= customIdx {
		t.Fatalf("expected dynamic(%d) < custom(%d)", dynIdx, customIdx)
	}
}

// --- Edge case tests ---

func TestEdgeCase_NoGitRepo(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	// Create a project without any git repo
	projectDir := filepath.Join(tmp, "plain-project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(projectDir, "AGENTS.md"), []byte("no git rules"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	r := NewContextRefresher(ContextRefresherConfig{
		CWD:        projectDir,
		BasePrompt: "Base.",
	})

	// Should not panic or error
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
		return
	}

	prompt := r.GetPrompt()
	if !strings.Contains(prompt, "no git rules") {
		t.Fatal("expected discovery to work without git repo")
	}
}

func TestEdgeCase_DeeplyNested(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	// Create 12 levels of nesting
	current := tmp
	for i := 0; i < 12; i++ {
		current = filepath.Join(current, fmt.Sprintf("level%d", i))
		if err := os.MkdirAll(current, 0o755); err != nil {
			t.Fatal(err)
			return
		}
		// Put AGENTS.md at every 3rd level
		if i%3 == 0 {
			content := fmt.Sprintf("rules at level %d", i)
			if err := os.WriteFile(filepath.Join(current, "AGENTS.md"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
				return
			}
		}
	}

	r := NewContextRefresher(ContextRefresherConfig{
		CWD:        current,
		BasePrompt: "Base.",
	})

	// Should not panic
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
		return
	}

	prompt := r.GetPrompt()
	// Should find files at levels 0, 3, 6, 9 (4 files)
	if !strings.Contains(prompt, "rules at level 0") {
		t.Fatal("expected level 0 rules")
	}
	if !strings.Contains(prompt, "rules at level 9") {
		t.Fatal("expected level 9 rules")
	}
}

func TestEdgeCase_SymlinkLoop(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	// Create a symlink loop: a -> b -> a
	dirA := filepath.Join(tmp, "a")
	dirB := filepath.Join(tmp, "b")
	if err := os.MkdirAll(dirA, 0o755); err != nil {
		t.Fatal(err)
		return
	}

	// Create symlink loop: b -> a
	if err := os.Symlink(dirA, dirB); err != nil {
		t.Skip("symlinks not supported")
	}

	// Create a symlinked file that points to itself (broken)
	loopFile := filepath.Join(tmp, "loop.md")
	loopTarget := filepath.Join(tmp, "loop_target.md")
	if err := os.Symlink(loopTarget, loopFile); err != nil {
		t.Skip("symlinks not supported")
	}
	// loopTarget doesn't exist — this is a broken symlink

	// readContextFile should handle broken symlinks gracefully
	content, _ := readContextFile(loopFile)
	if content != "" {
		t.Fatal("expected empty content for broken symlink")
	}
}

func TestEdgeCase_PermissionDenied(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	// Create a readable AGENTS.md and an unreadable one
	if err := os.WriteFile(filepath.Join(tmp, "AGENTS.md"), []byte("readable"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	unreadableDir := filepath.Join(tmp, "restricted")
	if err := os.MkdirAll(unreadableDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}
	unreadableFile := filepath.Join(unreadableDir, "secret.md")
	if err := os.WriteFile(unreadableFile, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.Chmod(unreadableFile, 0o000); err != nil {
		t.Skip("cannot change file permissions")
	}
	defer os.Chmod(unreadableFile, 0o644) //nolint:errcheck // cleanup

	r := NewContextRefresher(ContextRefresherConfig{
		CWD:        tmp,
		BasePrompt: "Base.",
	})

	// Should not panic
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
		return
	}

	// Should still have readable content
	prompt := r.GetPrompt()
	if !strings.Contains(prompt, "readable") {
		t.Fatal("expected readable AGENTS.md content")
	}

	// Adding unreadable file as dynamic context should fail gracefully
	err := r.AddContext(unreadableFile)
	if err == nil {
		t.Fatal("expected error when adding unreadable file")
		return
	}
}

func TestEdgeCase_VeryLargeFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	// Create a file larger than 1MB
	largeContent := strings.Repeat("This is a line of content.\n", 50000) // ~1.3MB
	largeFile := filepath.Join(tmp, "large.md")
	if err := os.WriteFile(largeFile, []byte(largeContent), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	r := NewContextRefresher(ContextRefresherConfig{
		CWD:        tmp,
		BasePrompt: "Base.",
	})
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
		return
	}

	// Add as dynamic context
	if err := r.AddContext(largeFile); err != nil {
		t.Fatal(err)
		return
	}

	entries := r.ListContext()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	// Should be truncated
	if !entries[0].Truncated {
		t.Fatal("expected file to be marked as truncated")
	}
	if !strings.Contains(entries[0].Content, "WARNING: File was truncated") {
		t.Fatal("expected truncation warning")
	}

	// Content should be at most maxContextFileSize + warning text
	if len(entries[0].Content) > maxContextFileSize+200 {
		t.Fatalf("content too large: %d bytes", len(entries[0].Content))
	}
}

func TestEdgeCase_EmptyInstructionFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	// Empty file should be skipped
	if err := os.WriteFile(filepath.Join(tmp, "AGENTS.md"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
		return
	}
	// Whitespace-only file should also be skipped
	if err := os.WriteFile(filepath.Join(tmp, "CLAUDE.md"), []byte("   \n\t  \n  "), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	r := NewContextRefresher(ContextRefresherConfig{
		CWD:        tmp,
		BasePrompt: "Base.",
	})
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
		return
	}

	prompt := r.GetPrompt()
	// Should only have the base prompt, no instruction markers
	if strings.Contains(prompt, "Contents of") {
		t.Fatal("empty/whitespace files should not appear in prompt")
	}
}

func TestEdgeCase_BinaryFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	// Create a binary file (contains null bytes)
	binaryContent := []byte("hello\x00world\x00binary\x00data")
	binaryFile := filepath.Join(tmp, "binary.md")
	if err := os.WriteFile(binaryFile, binaryContent, 0o644); err != nil {
		t.Fatal(err)
		return
	}

	r := NewContextRefresher(ContextRefresherConfig{
		CWD:        tmp,
		BasePrompt: "Base.",
	})
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
		return
	}

	// Trying to add binary file as context should fail
	err := r.AddContext(binaryFile)
	if err == nil {
		t.Fatal("expected error for binary file")
		return
	}
	if !strings.Contains(err.Error(), "empty or binary") {
		t.Fatalf("expected 'empty or binary' error, got: %v", err)
	}
}

func TestEdgeCase_SymlinkToValidFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	// Create a real file and symlink to it
	realFile := filepath.Join(tmp, "real.md")
	if err := os.WriteFile(realFile, []byte("real content"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	linkFile := filepath.Join(tmp, "link.md")
	if err := os.Symlink(realFile, linkFile); err != nil {
		t.Skip("symlinks not supported")
	}

	r := NewContextRefresher(ContextRefresherConfig{
		CWD:        tmp,
		BasePrompt: "Base.",
	})
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
		return
	}

	// Adding symlink to valid file should work
	if err := r.AddContext(linkFile); err != nil {
		t.Fatal(err)
		return
	}

	entries := r.ListContext()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if !strings.Contains(entries[0].Content, "real content") {
		t.Fatal("expected real content through symlink")
	}
}

func TestEdgeCase_FileDeletedDuringSession(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	ctxFile := filepath.Join(tmp, "volatile.md")
	if err := os.WriteFile(ctxFile, []byte("volatile content"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	r := NewContextRefresher(ContextRefresherConfig{
		CWD:        tmp,
		BasePrompt: "Base.",
	})
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
		return
	}

	if err := r.AddContext(ctxFile); err != nil {
		t.Fatal(err)
		return
	}

	// Delete the file
	if err := os.Remove(ctxFile); err != nil {
		t.Fatal(err)
		return
	}

	// Dynamic context change detection should detect this
	r.mu.RLock()
	changed := r.dynamicContextChanged()
	r.mu.RUnlock()

	if !changed {
		t.Fatal("expected change detected when file is deleted")
	}

	// Refresh should remove the entry gracefully
	refreshed, err := r.Refresh()
	if err != nil {
		t.Fatal(err)
		return
	}
	if !refreshed {
		t.Fatal("expected refresh to detect deletion")
	}

	entries := r.ListContext()
	if len(entries) != 0 {
		t.Fatalf("expected deleted file to be removed from context, got %d entries", len(entries))
	}
}

func TestEdgeCase_UnicodeFilenames(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	// Create a file with unicode in the name
	unicodeFile := filepath.Join(tmp, "说明.md")
	if err := os.WriteFile(unicodeFile, []byte("Unicode content"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	r := NewContextRefresher(ContextRefresherConfig{
		CWD:        tmp,
		BasePrompt: "Base.",
	})
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
		return
	}

	// Adding unicode-named file should work
	if err := r.AddContext(unicodeFile); err != nil {
		t.Fatal(err)
		return
	}

	entries := r.ListContext()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
}

func TestEdgeCase_DirectoryAsContext(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	dirPath := filepath.Join(tmp, "adir")
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		t.Fatal(err)
		return
	}

	r := NewContextRefresher(ContextRefresherConfig{
		CWD:        tmp,
		BasePrompt: "Base.",
	})
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
		return
	}

	// Adding a directory should fail
	err := r.AddContext(dirPath)
	if err == nil {
		t.Fatal("expected error when adding directory as context")
		return
	}
}

// --- readContextFile unit tests ---

func TestReadContextFile_Normal(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "test.md")
	if err := os.WriteFile(f, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	content, truncated := readContextFile(f)
	if content != "hello world" {
		t.Fatalf("expected 'hello world', got %q", content)
	}
	if truncated {
		t.Fatal("expected not truncated")
	}
}

func TestReadContextFile_Empty(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "empty.md")
	if err := os.WriteFile(f, []byte(""), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	content, _ := readContextFile(f)
	if content != "" {
		t.Fatal("expected empty content for empty file")
	}
}

func TestReadContextFile_Binary(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "bin.dat")
	if err := os.WriteFile(f, []byte{0x00, 0x01, 0x02, 0xFF}, 0o644); err != nil {
		t.Fatal(err)
		return
	}

	content, _ := readContextFile(f)
	if content != "" {
		t.Fatal("expected empty content for binary file")
	}
}

func TestReadContextFile_Nonexistent(t *testing.T) {
	content, _ := readContextFile("/nonexistent/path/file.md")
	if content != "" {
		t.Fatal("expected empty content for nonexistent file")
	}
}

func TestReadContextFile_LargeFile(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "large.md")

	// Create a 2MB file
	large := strings.Repeat("x", 2*1024*1024)
	if err := os.WriteFile(f, []byte(large), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	content, truncated := readContextFile(f)
	if !truncated {
		t.Fatal("expected truncation for large file")
	}
	if len(content) > maxContextFileSize {
		t.Fatalf("expected content <= %d bytes, got %d", maxContextFileSize, len(content))
	}
}

// --- isTextContent unit tests ---

func TestIsTextContent_ValidText(t *testing.T) {
	if !isTextContent([]byte("Hello, world!")) {
		t.Fatal("expected valid text")
	}
}

func TestIsTextContent_UTF8(t *testing.T) {
	if !isTextContent([]byte("你好世界")) {
		t.Fatal("expected valid UTF-8 text")
	}
}

func TestIsTextContent_Empty(t *testing.T) {
	if isTextContent([]byte("")) {
		t.Fatal("expected false for empty content")
	}
}

func TestIsTextContent_NullBytes(t *testing.T) {
	if isTextContent([]byte("hello\x00world")) {
		t.Fatal("expected false for content with null bytes")
	}
}

func TestIsTextContent_InvalidUTF8(t *testing.T) {
	if isTextContent([]byte{0xFF, 0xFE, 0xFD}) {
		t.Fatal("expected false for invalid UTF-8")
	}
}

// --- SetMemoryDir tests ---

func TestContextRefresher_SetMemoryDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	r := NewContextRefresher(ContextRefresherConfig{
		CWD:        tmp,
		BasePrompt: "Base.",
	})
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
		return
	}

	// Initially no memory
	if len(r.GetMemoryEntries()) != 0 {
		t.Fatal("expected no memory entries initially")
	}

	// Create memory dir and set it
	memDir := filepath.Join(tmp, "mem")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(memDir, "note.md"), []byte("a note"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	r.SetMemoryDir(memDir)

	entries := r.GetMemoryEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 memory entry after SetMemoryDir, got %d", len(entries))
	}
	if !strings.Contains(entries[0].Content, "a note") {
		t.Fatalf("expected note content, got %q", entries[0].Content)
	}
}

// --- Concurrent access tests ---

func TestContextRefresher_ConcurrentAccess(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	if err := os.WriteFile(filepath.Join(tmp, "AGENTS.md"), []byte("rules"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	r := NewContextRefresher(ContextRefresherConfig{
		CWD:        tmp,
		BasePrompt: "Base.",
	})
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
		return
	}

	// Hammer it from multiple goroutines
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func(id int) {
			defer func() { done <- true }()
			for j := 0; j < 50; j++ {
				_ = r.GetPrompt()
				_ = r.HasChanged()
				_ = r.GetMemoryEntries()
				_ = r.GetSkills()
				_ = r.GetTools()
				_ = r.ListContext()
			}
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}

// --- Integration test: full workflow ---

func TestContextRefresher_FullWorkflow(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	// Setup project
	if err := os.WriteFile(filepath.Join(tmp, "AGENTS.md"), []byte("project rules"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	// Setup memory
	memDir := filepath.Join(tmp, ".claude", "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(memDir, "prefs.md"), []byte("prefer Go"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	// Create refresher
	r := NewContextRefresher(ContextRefresherConfig{
		CWD:                tmp,
		BasePrompt:         "You are helpful.",
		CustomInstructions: "Be concise.",
		MemoryDir:          memDir,
	})
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
		return
	}

	// Set skills and tools
	r.SetSkills([]SkillDescription{{Name: "tdd", Description: "Test-driven development"}})
	r.SetTools([]ToolInfo{{Name: "Bash", Description: "Execute commands"}})

	// Add dynamic context
	ctxFile := filepath.Join(tmp, "spec.md")
	if err := os.WriteFile(ctxFile, []byte("The spec says X."), 0o644); err != nil {
		t.Fatal(err)
		return
	}
	if err := r.AddContext(ctxFile); err != nil {
		t.Fatal(err)
		return
	}

	// Rebuild to include skills/tools/dynamic
	r.mu.Lock()
	r.assembleLocked()
	r.mu.Unlock()

	prompt := r.GetPrompt()

	// Verify all components are present
	checks := []struct {
		marker string
		desc   string
	}{
		{"You are helpful.", "base prompt"},
		{"project rules", "project instructions"},
		{"prefer Go", "memory"},
		{"tdd", "skill"},
		{"Bash", "tool"},
		{"The spec says X.", "dynamic context"},
		{"Be concise.", "custom instructions"},
	}

	for _, check := range checks {
		if !strings.Contains(prompt, check.marker) {
			t.Fatalf("expected %s (%q) in prompt", check.desc, check.marker)
		}
	}

	// Simulate turn: no changes -> no rebuild
	changed, err := r.Refresh()
	if err != nil {
		t.Fatal(err)
		return
	}
	if changed {
		t.Fatal("expected no change on first refresh")
	}

	// Simulate file change mid-session
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(tmp, "AGENTS.md"), []byte("updated project rules"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	// Next refresh detects the change
	changed, err = r.Refresh()
	if err != nil {
		t.Fatal(err)
		return
	}
	if !changed {
		t.Fatal("expected change detected")
	}
	if !strings.Contains(r.GetPrompt(), "updated project rules") {
		t.Fatal("expected updated content in prompt")
	}

	// Remove dynamic context
	r.RemoveContext(ctxFile)
	r.mu.Lock()
	r.assembleLocked()
	r.mu.Unlock()

	if strings.Contains(r.GetPrompt(), "The spec says X.") {
		t.Fatal("expected dynamic context to be removed")
	}
}
