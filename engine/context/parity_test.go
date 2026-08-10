package promptctx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// =============================================================================
// Parity Verification Tests — Context/Instructions Subsystem
//
// These tests verify behavioral parity with the reference implementation's
// instruction discovery, precedence rules, scope filtering, context refreshing,
// prompt assembly ordering, and edge case handling.
// =============================================================================

// --- InstructionDiscovery: File Variant Discovery ---

func TestParity_Discovery_AllFileVariants(t *testing.T) {
	// Verifies that InstructionDiscovery finds all recognized file variants:
	// AGENTS.md, CLAUDE.md, CLAUDE.local.md, and .claude/rules/*.md
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "fake-home"))

	// Create all variants at the CWD level
	writeFile(t, filepath.Join(tmp, "AGENTS.md"), "agents content")
	writeFile(t, filepath.Join(tmp, "CLAUDE.md"), "claude content")
	writeFile(t, filepath.Join(tmp, "CLAUDE.local.md"), "claude local content")

	// .claude/ subdirectory variants
	dotClaude := filepath.Join(tmp, ".claude")
	mustMkdirAll(t, dotClaude)
	writeFile(t, filepath.Join(dotClaude, "AGENTS.md"), "dot-claude agents")
	writeFile(t, filepath.Join(dotClaude, "CLAUDE.md"), "dot-claude claude")

	// .claude/rules/ variants
	rulesDir := filepath.Join(dotClaude, "rules")
	mustMkdirAll(t, rulesDir)
	writeFile(t, filepath.Join(rulesDir, "01-style.md"), "style rule")
	writeFile(t, filepath.Join(rulesDir, "02-safety.md"), "safety rule")

	d := NewInstructionDiscovery(nil)
	files, err := d.Discover(tmp)
	if err != nil {
		t.Fatal(err)
		return
	}

	// Collect discovered filenames
	found := map[string]bool{}
	for _, f := range files {
		found[filepath.Base(f.Path)] = true
	}

	expected := []string{"AGENTS.md", "CLAUDE.md", "CLAUDE.local.md", "01-style.md", "02-safety.md"}
	for _, name := range expected {
		if !found[name] {
			t.Errorf("expected to discover %q but it was not found", name)
		}
	}
}

// --- Precedence: Deeper file wins ---

func TestParity_Precedence_DeeperFileWins(t *testing.T) {
	// Reference behavior: files closer to CWD (deeper) have higher priority.
	// A 5-level hierarchy verifies that discovery returns files in priority order
	// (shallowest first, deepest last).
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "fake-home"))

	// Create a 5-level directory hierarchy
	levels := []string{tmp}
	for i := 1; i <= 4; i++ {
		levels = append(levels, filepath.Join(levels[i-1], "sub"))
	}
	for _, dir := range levels {
		mustMkdirAll(t, dir)
	}

	// Place AGENTS.md at each level
	for i, dir := range levels {
		writeFile(t, filepath.Join(dir, "AGENTS.md"), strings.Repeat("x", i+1))
	}

	// Discover from the deepest level
	deepest := levels[len(levels)-1]
	d := NewInstructionDiscovery(nil)
	files, err := d.Discover(deepest)
	if err != nil {
		t.Fatal(err)
		return
	}

	// Filter to just the AGENTS.md files we created
	var agentsFiles []InstructionFile
	for _, f := range files {
		if f.Filename == "AGENTS.md" {
			agentsFiles = append(agentsFiles, f)
		}
	}

	if len(agentsFiles) < 5 {
		t.Fatalf("expected at least 5 AGENTS.md files across hierarchy, got %d", len(agentsFiles))
	}

	// Verify priority ordering: depth must be non-decreasing (shallowest first)
	for i := 1; i < len(agentsFiles); i++ {
		if agentsFiles[i].Depth < agentsFiles[i-1].Depth {
			t.Errorf("priority ordering violated at index %d: depth %d < %d",
				i, agentsFiles[i].Depth, agentsFiles[i-1].Depth)
		}
	}

	// The last file (highest priority) must be from the deepest directory
	lastFile := agentsFiles[len(agentsFiles)-1]
	if lastFile.ScopeRoot != deepest {
		t.Errorf("expected highest-priority file from %q, got ScopeRoot=%q", deepest, lastFile.ScopeRoot)
	}
}

// --- Scope filtering: files only apply to paths under their containing directory ---

func TestParity_ScopeFiltering_PathRestriction(t *testing.T) {
	// Reference behavior: instruction files only apply to paths under their ScopeRoot.
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "fake-home"))

	// Create structure:
	//   /tmp/root/AGENTS.md         (scope: /tmp/root)
	//   /tmp/root/pkg/AGENTS.md     (scope: /tmp/root/pkg)
	rootDir := filepath.Join(tmp, "root")
	pkgDir := filepath.Join(rootDir, "pkg")
	otherDir := filepath.Join(rootDir, "other")
	mustMkdirAll(t, pkgDir)
	mustMkdirAll(t, otherDir)

	writeFile(t, filepath.Join(rootDir, "AGENTS.md"), "root instructions")
	writeFile(t, filepath.Join(pkgDir, "AGENTS.md"), "pkg instructions")

	d := NewInstructionDiscovery(nil)
	_, err := d.Discover(pkgDir)
	if err != nil {
		t.Fatal(err)
		return
	}

	// Files for a path under pkg should include both root and pkg instructions
	pkgFiles := d.FilesForPath(filepath.Join(pkgDir, "foo.go"))
	if len(pkgFiles) < 2 {
		t.Fatalf("expected at least 2 files for path under pkg, got %d", len(pkgFiles))
	}

	// Files for a path under "other" should include root but NOT pkg
	otherFiles := d.FilesForPath(filepath.Join(otherDir, "bar.go"))
	for _, f := range otherFiles {
		if f.ScopeRoot == pkgDir {
			t.Errorf("expected pkg instructions NOT to apply to 'other' directory, but found ScopeRoot=%q", f.ScopeRoot)
		}
	}
}

// --- ContextRefresher: refresh detects file changes ---

func TestParity_Refresher_DetectsFileChanges(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "fake-home"))

	agentsPath := filepath.Join(tmp, "AGENTS.md")
	writeFile(t, agentsPath, "original content")

	r := NewContextRefresher(ContextRefresherConfig{
		CWD:        tmp,
		BasePrompt: "base",
	})
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
		return
	}

	// Initial prompt should contain original content
	prompt := r.GetPrompt()
	if !strings.Contains(prompt, "original content") {
		t.Fatal("expected initial prompt to contain 'original content'")
	}

	// Modify the file (sleep to ensure different modtime)
	time.Sleep(50 * time.Millisecond)
	writeFile(t, agentsPath, "updated content")

	// Refresh should detect the change
	changed, err := r.Refresh()
	if err != nil {
		t.Fatal(err)
		return
	}
	if !changed {
		t.Fatal("expected Refresh() to report change after file modification")
	}

	prompt = r.GetPrompt()
	if !strings.Contains(prompt, "updated content") {
		t.Fatal("expected refreshed prompt to contain 'updated content'")
	}
}

// --- ContextRefresher: memory context loads .md files ---

func TestParity_Refresher_MemoryContextLoadsMdFiles(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "fake-home"))

	memoryDir := filepath.Join(tmp, "memory")
	mustMkdirAll(t, memoryDir)
	writeFile(t, filepath.Join(memoryDir, "context.md"), "remembered fact")
	writeFile(t, filepath.Join(memoryDir, "not-md.txt"), "should be ignored")

	r := NewContextRefresher(ContextRefresherConfig{
		CWD:        tmp,
		BasePrompt: "base",
		MemoryDir:  memoryDir,
	})
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
		return
	}

	entries := r.GetMemoryEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 memory entry (only .md), got %d", len(entries))
	}
	if !strings.Contains(entries[0].Content, "remembered fact") {
		t.Errorf("expected memory entry to contain 'remembered fact', got %q", entries[0].Content)
	}
}

// --- ContextRefresher: dynamic add/remove persists across refreshes ---

func TestParity_Refresher_DynamicContextPersistsAcrossRefresh(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "fake-home"))

	dynamicFile := filepath.Join(tmp, "dynamic.md")
	writeFile(t, dynamicFile, "dynamic content here")

	r := NewContextRefresher(ContextRefresherConfig{
		CWD:        tmp,
		BasePrompt: "base",
	})
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
		return
	}

	// Add dynamic context
	if err := r.AddContext(dynamicFile); err != nil {
		t.Fatal(err)
		return
	}

	// Verify it's present
	listed := r.ListContext()
	if len(listed) != 1 {
		t.Fatalf("expected 1 dynamic context entry, got %d", len(listed))
	}

	// Trigger refresh — dynamic context should persist
	_, err := r.Refresh()
	if err != nil {
		t.Fatal(err)
		return
	}

	listed = r.ListContext()
	if len(listed) != 1 {
		t.Fatalf("expected dynamic context to persist after refresh, got %d entries", len(listed))
	}

	// Remove dynamic context
	removed := r.RemoveContext(dynamicFile)
	if !removed {
		t.Fatal("expected RemoveContext to return true")
	}
	listed = r.ListContext()
	if len(listed) != 0 {
		t.Fatalf("expected 0 entries after removal, got %d", len(listed))
	}
}

// --- Prompt Assembly: ordering is correct ---

func TestParity_PromptAssembly_CorrectOrdering(t *testing.T) {
	// Reference behavior: system -> project -> memory -> skills -> tools -> dynamic
	// The ContextRefresher.assembleLocked() follows this exact order.
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "fake-home"))

	// Set up instruction file
	writeFile(t, filepath.Join(tmp, "AGENTS.md"), "PROJECT_INSTRUCTIONS")

	// Set up memory
	memDir := filepath.Join(tmp, "memory")
	mustMkdirAll(t, memDir)
	writeFile(t, filepath.Join(memDir, "mem.md"), "MEMORY_CONTENT")

	r := NewContextRefresher(ContextRefresherConfig{
		CWD:                tmp,
		BasePrompt:         "SYSTEM_PROMPT",
		MemoryDir:          memDir,
		CustomInstructions: "CUSTOM_INSTRUCTIONS",
	})

	// Set skills and tools before initialize
	r.SetSkills([]SkillDescription{{Name: "test-skill", Description: "SKILL_DESC"}})
	r.SetTools([]ToolInfo{{Name: "TestTool", Description: "TOOL_DESC"}})

	if err := r.Initialize(); err != nil {
		t.Fatal(err)
		return
	}

	prompt := r.GetPrompt()

	// Verify ordering: system < project < memory < skills < tools < custom
	sysIdx := strings.Index(prompt, "SYSTEM_PROMPT")
	projIdx := strings.Index(prompt, "PROJECT_INSTRUCTIONS")
	memIdx := strings.Index(prompt, "MEMORY_CONTENT")
	skillIdx := strings.Index(prompt, "SKILL_DESC")
	toolIdx := strings.Index(prompt, "TOOL_DESC")
	customIdx := strings.Index(prompt, "CUSTOM_INSTRUCTIONS")

	if sysIdx < 0 || projIdx < 0 || memIdx < 0 || skillIdx < 0 || toolIdx < 0 || customIdx < 0 {
		t.Fatalf("not all sections found in prompt:\n%s", prompt)
	}

	if sysIdx >= projIdx {
		t.Error("system prompt should come before project instructions")
	}
	if projIdx >= memIdx {
		t.Error("project instructions should come before memory")
	}
	if memIdx >= skillIdx {
		t.Error("memory should come before skills")
	}
	if skillIdx >= toolIdx {
		t.Error("skills should come before tools")
	}
	if toolIdx >= customIdx {
		t.Error("tools should come before custom instructions")
	}
}

// --- Edge case: permission denied files are skipped ---

func TestParity_EdgeCase_PermissionDeniedFilesSkipped(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("test requires non-root user")
	}
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "fake-home"))

	// Create an unreadable AGENTS.md
	unreadable := filepath.Join(tmp, "AGENTS.md")
	writeFile(t, unreadable, "secret")
	if err := os.Chmod(unreadable, 0o000); err != nil {
		t.Fatal(err)
		return
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o644) })

	// Create a readable CLAUDE.md
	writeFile(t, filepath.Join(tmp, "CLAUDE.md"), "readable content")

	d := NewInstructionDiscovery(nil)
	files, err := d.Discover(tmp)
	if err != nil {
		t.Fatal(err)
		return
	}

	// Should find CLAUDE.md but not the unreadable AGENTS.md
	for _, f := range files {
		if f.Path == unreadable {
			t.Error("expected unreadable AGENTS.md to be skipped")
		}
	}
}

// --- Edge case: binary files are skipped in dynamic context ---

func TestParity_EdgeCase_BinaryFilesSkippedInDynamicContext(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "fake-home"))

	// Create a binary file (contains null bytes)
	binaryFile := filepath.Join(tmp, "binary.dat")
	binaryData := []byte{0x00, 0x01, 0x02, 0xFF, 0x00, 0x03}
	if err := os.WriteFile(binaryFile, binaryData, 0o644); err != nil {
		t.Fatal(err)
		return
	}

	r := NewContextRefresher(ContextRefresherConfig{
		CWD:        tmp,
		BasePrompt: "base",
	})
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
		return
	}

	// Adding a binary file should fail
	err := r.AddContext(binaryFile)
	if err == nil {
		t.Fatal("expected error when adding binary file to context")
		return
	}
	if !strings.Contains(err.Error(), "binary") && !strings.Contains(err.Error(), "empty") {
		t.Errorf("expected error about binary/empty file, got: %v", err)
	}
}

// --- Edge case: files >1MB are truncated ---

func TestParity_EdgeCase_LargeFileTruncated(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "fake-home"))

	// Create a file larger than maxContextFileSize (1MB)
	largeFile := filepath.Join(tmp, "large.md")
	content := strings.Repeat("A", maxContextFileSize+1000)
	writeFile(t, largeFile, content)

	r := NewContextRefresher(ContextRefresherConfig{
		CWD:        tmp,
		BasePrompt: "base",
	})
	if err := r.Initialize(); err != nil {
		t.Fatal(err)
		return
	}

	// Add the large file as dynamic context
	err := r.AddContext(largeFile)
	if err != nil {
		t.Fatal(err)
		return
	}

	listed := r.ListContext()
	if len(listed) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(listed))
	}

	entry := listed[0]
	if !entry.Truncated {
		t.Error("expected large file to be marked as truncated")
	}
	if !strings.Contains(entry.Content, "[WARNING:") {
		t.Error("expected truncation warning in content")
	}
	// Content should be at most maxContextFileSize plus the warning
	baseContent := strings.TrimSuffix(entry.Content, entry.Content[strings.LastIndex(entry.Content, "\n\n[WARNING:"):])
	if len(baseContent) > maxContextFileSize {
		t.Errorf("expected content to be truncated to %d bytes, got %d", maxContextFileSize, len(baseContent))
	}
}

// --- HasChanged: detects new files appearing ---

func TestParity_HasChanged_DetectsNewFiles(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "fake-home"))

	// Initial discovery with no instruction files
	d := NewInstructionDiscovery(nil)
	_, err := d.Discover(tmp)
	if err != nil {
		t.Fatal(err)
		return
	}

	if d.HasChanged() {
		t.Error("expected no changes initially")
	}

	// Create a new AGENTS.md
	writeFile(t, filepath.Join(tmp, "AGENTS.md"), "new rules")

	if !d.HasChanged() {
		t.Error("expected HasChanged() to return true after new file appears")
	}
}

// --- HasChanged: detects file deletion ---

func TestParity_HasChanged_DetectsFileDeletion(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "fake-home"))

	agentsPath := filepath.Join(tmp, "AGENTS.md")
	writeFile(t, agentsPath, "some rules")

	d := NewInstructionDiscovery(nil)
	_, err := d.Discover(tmp)
	if err != nil {
		t.Fatal(err)
		return
	}

	if d.HasChanged() {
		t.Error("expected no changes after initial discovery")
	}

	// Delete the file
	_ = os.Remove(agentsPath)

	if !d.HasChanged() {
		t.Error("expected HasChanged() to return true after file deletion")
	}
}

// --- PromptAssembly: Render produces correct output ---

func TestParity_PromptAssembly_Render(t *testing.T) {
	assembly := &PromptAssembly{
		Components: []PromptComponent{
			{Label: "custom", Content: "custom instructions", Priority: PriorityUserInstructions, Source: "user"},
			{Label: "base", Content: "system base", Priority: PriorityDefault, Source: "system"},
			{Label: "project", Content: "project rules", Priority: PriorityProjectInstructions, Source: "project"},
		},
	}

	rendered := assembly.Render()

	// Verify ordering: base (0) < project (10) < custom (20)
	baseIdx := strings.Index(rendered, "system base")
	projIdx := strings.Index(rendered, "project rules")
	customIdx := strings.Index(rendered, "custom instructions")

	if baseIdx >= projIdx {
		t.Error("base should come before project")
	}
	if projIdx >= customIdx {
		t.Error("project should come before custom")
	}
}

// --- AssemblePromptForPath: scope filtering in assembly ---

func TestParity_AssemblePromptForPath_ScopeFilter(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "fake-home"))

	// Create root-level and sub-level instruction files
	writeFile(t, filepath.Join(tmp, "AGENTS.md"), "root-level")
	subDir := filepath.Join(tmp, "sub")
	mustMkdirAll(t, subDir)
	writeFile(t, filepath.Join(subDir, "AGENTS.md"), "sub-level")

	d := NewInstructionDiscovery(nil)
	_, err := d.Discover(subDir)
	if err != nil {
		t.Fatal(err)
		return
	}

	// Assemble for a path under sub/ — should include both
	assembly := AssemblePromptForPath("base", d, "", filepath.Join(subDir, "foo.go"))
	rendered := assembly.Render()
	if !strings.Contains(rendered, "sub-level") {
		t.Error("expected sub-level instructions for path under sub/")
	}
	if !strings.Contains(rendered, "root-level") {
		t.Error("expected root-level instructions for path under sub/ (inherited)")
	}

	// Assemble for a path under root but not sub/ — should only include root
	assembly2 := AssemblePromptForPath("base", d, "", filepath.Join(tmp, "other.go"))
	rendered2 := assembly2.Render()
	if !strings.Contains(rendered2, "root-level") {
		t.Error("expected root-level instructions for path under root")
	}
	if strings.Contains(rendered2, "sub-level") {
		t.Error("expected sub-level instructions NOT to apply outside sub/")
	}
}

// --- Empty content files are skipped ---

func TestParity_EmptyFilesSkipped(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "fake-home"))

	// Create empty and whitespace-only instruction files
	writeFile(t, filepath.Join(tmp, "AGENTS.md"), "")
	writeFile(t, filepath.Join(tmp, "CLAUDE.md"), "   \n\t  \n")
	writeFile(t, filepath.Join(tmp, "CLAUDE.local.md"), "real content")

	d := NewInstructionDiscovery(nil)
	files, err := d.Discover(tmp)
	if err != nil {
		t.Fatal(err)
		return
	}

	// Only CLAUDE.local.md with real content should be found
	for _, f := range files {
		if f.Filename == "AGENTS.md" {
			t.Error("empty AGENTS.md should be skipped")
		}
		if f.Filename == "CLAUDE.md" {
			t.Error("whitespace-only CLAUDE.md should be skipped")
		}
	}

	// At least one file should be discovered
	foundLocal := false
	for _, f := range files {
		if f.Filename == "CLAUDE.local.md" {
			foundLocal = true
		}
	}
	if !foundLocal {
		t.Error("expected CLAUDE.local.md with real content to be discovered")
	}
}

// =============================================================================
// Helper functions
// =============================================================================

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write %s: %v", path, err)
		return
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("failed to mkdir %s: %v", path, err)
		return
	}
}
