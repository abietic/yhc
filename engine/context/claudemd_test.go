package promptctx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDiscoverClaudeMds_ProjectRoot(t *testing.T) {
	tmp := t.TempDir()

	// Create CLAUDE.md in the project root
	claudeMd := filepath.Join(tmp, "CLAUDE.md")
	if err := os.WriteFile(claudeMd, []byte("# Project Rules\nUse Go."), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	files, err := DiscoverClaudeMdsWithCache(tmp, NewClaudeMdCache())
	if err != nil {
		t.Fatal(err)
		return
	}

	found := false
	for _, f := range files {
		if f.Path == claudeMd && f.Source == "project" {
			found = true
			if !strings.Contains(f.Content, "Use Go.") {
				t.Fatalf("unexpected content: %q", f.Content)
			}
		}
	}
	if !found {
		t.Fatalf("expected to find project CLAUDE.md, got files: %+v", files)
	}
}

func TestDiscoverClaudeMds_DotClaudeDir(t *testing.T) {
	tmp := t.TempDir()

	// Create .claude/CLAUDE.md
	dotClaude := filepath.Join(tmp, ".claude")
	if err := os.MkdirAll(dotClaude, 0o755); err != nil {
		t.Fatal(err)
		return
	}
	claudeMd := filepath.Join(dotClaude, "CLAUDE.md")
	if err := os.WriteFile(claudeMd, []byte("dot-claude rules"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	files, err := DiscoverClaudeMdsWithCache(tmp, NewClaudeMdCache())
	if err != nil {
		t.Fatal(err)
		return
	}

	found := false
	for _, f := range files {
		if f.Path == claudeMd && f.Source == "project-dot-claude" {
			found = true
			if !strings.Contains(f.Content, "dot-claude rules") {
				t.Fatalf("unexpected content: %q", f.Content)
			}
		}
	}
	if !found {
		t.Fatalf("expected to find .claude/CLAUDE.md, got files: %+v", files)
	}
}

func TestDiscoverClaudeMds_UserHome(t *testing.T) {
	tmp := t.TempDir()

	// Create a fake home directory
	fakeHome := filepath.Join(tmp, "fakehome")
	if err := os.MkdirAll(filepath.Join(fakeHome, ".claude"), 0o755); err != nil {
		t.Fatal(err)
		return
	}
	userClaudeMd := filepath.Join(fakeHome, ".claude", "CLAUDE.md")
	if err := os.WriteFile(userClaudeMd, []byte("user global rules"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	// Override HOME for this test
	t.Setenv("HOME", fakeHome)

	projectDir := filepath.Join(tmp, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}

	files, err := DiscoverClaudeMdsWithCache(projectDir, NewClaudeMdCache())
	if err != nil {
		t.Fatal(err)
		return
	}

	found := false
	for _, f := range files {
		if f.Path == userClaudeMd && f.Source == "user" {
			found = true
			if !strings.Contains(f.Content, "user global rules") {
				t.Fatalf("unexpected content: %q", f.Content)
			}
		}
	}
	if !found {
		t.Fatalf("expected to find user CLAUDE.md, got files: %+v", files)
	}
}

func TestDiscoverClaudeMds_ParentDirectories(t *testing.T) {
	tmp := t.TempDir()

	// Create a monorepo structure:
	// tmp/CLAUDE.md (parent)
	// tmp/packages/myapp/ (cwd)
	parentClaudeMd := filepath.Join(tmp, "CLAUDE.md")
	if err := os.WriteFile(parentClaudeMd, []byte("monorepo root rules"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	projectDir := filepath.Join(tmp, "packages", "myapp")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}

	// Also create project-level CLAUDE.md
	projectClaudeMd := filepath.Join(projectDir, "CLAUDE.md")
	if err := os.WriteFile(projectClaudeMd, []byte("myapp rules"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	files, err := DiscoverClaudeMdsWithCache(projectDir, NewClaudeMdCache())
	if err != nil {
		t.Fatal(err)
		return
	}

	// Should find both parent and project
	foundParent := false
	foundProject := false
	for _, f := range files {
		if f.Path == parentClaudeMd && f.Source == "parent" {
			foundParent = true
		}
		if f.Path == projectClaudeMd && f.Source == "project" {
			foundProject = true
		}
	}
	if !foundParent {
		t.Fatalf("expected to find parent CLAUDE.md, got files: %+v", files)
	}
	if !foundProject {
		t.Fatalf("expected to find project CLAUDE.md, got files: %+v", files)
	}
}

func TestDiscoverClaudeMds_PriorityOrder(t *testing.T) {
	tmp := t.TempDir()

	// Set up fake home
	fakeHome := filepath.Join(tmp, "fakehome")
	if err := os.MkdirAll(filepath.Join(fakeHome, ".claude"), 0o755); err != nil {
		t.Fatal(err)
		return
	}
	t.Setenv("HOME", fakeHome)
	if err := os.WriteFile(filepath.Join(fakeHome, ".claude", "CLAUDE.md"), []byte("user"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	// Create parent dir with CLAUDE.md
	parentDir := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(parentDir, "CLAUDE.md"), []byte("parent"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	// Create project dir
	projectDir := filepath.Join(parentDir, "packages", "app")
	if err := os.MkdirAll(filepath.Join(projectDir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".claude", "CLAUDE.md"), []byte("dot-claude"), 0o644); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(projectDir, "CLAUDE.md"), []byte("project"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	files, err := DiscoverClaudeMdsWithCache(projectDir, NewClaudeMdCache())
	if err != nil {
		t.Fatal(err)
		return
	}

	// Priority order (lowest to highest): user, parent, project-dot-claude, project
	// So in the returned slice: user first, project last
	if len(files) < 4 {
		t.Fatalf("expected at least 4 files, got %d: %+v", len(files), files)
	}

	// First should be user
	if files[0].Source != "user" {
		t.Fatalf("expected first file source to be 'user', got %q", files[0].Source)
	}
	// Last should be project (highest priority)
	last := files[len(files)-1]
	if last.Source != "project" {
		t.Fatalf("expected last file source to be 'project', got %q", last.Source)
	}
}

func TestDiscoverClaudeMds_MissingFilesGraceful(t *testing.T) {
	tmp := t.TempDir()

	// Empty directory — no CLAUDE.md files anywhere meaningful
	projectDir := filepath.Join(tmp, "empty-project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}

	// Override HOME to avoid picking up real user files
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	files, err := DiscoverClaudeMdsWithCache(projectDir, NewClaudeMdCache())
	if err != nil {
		t.Fatal(err)
		return
	}

	// Should not error, just return empty
	if len(files) != 0 {
		t.Fatalf("expected 0 files for empty project, got %d: %+v", len(files), files)
	}
}

func TestDiscoverClaudeMds_EmptyFileSkipped(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	// Create an empty CLAUDE.md
	claudeMd := filepath.Join(tmp, "CLAUDE.md")
	if err := os.WriteFile(claudeMd, []byte("   \n  "), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	files, err := DiscoverClaudeMdsWithCache(tmp, NewClaudeMdCache())
	if err != nil {
		t.Fatal(err)
		return
	}

	// Empty/whitespace-only files should be skipped
	if len(files) != 0 {
		t.Fatalf("expected 0 files (empty content), got %d: %+v", len(files), files)
	}
}

func TestDiscoverAgentsMds(t *testing.T) {
	tmp := t.TempDir()

	agentsDir := filepath.Join(tmp, ".claude", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}

	// Create agent definition files
	if err := os.WriteFile(filepath.Join(agentsDir, "coder.md"), []byte("---\nname: coder\n---\nYou are a coder."), 0o644); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "reviewer.md"), []byte("---\nname: reviewer\n---\nYou review code."), 0o644); err != nil {
		t.Fatal(err)
		return
	}
	// Non-md files should be skipped
	if err := os.WriteFile(filepath.Join(agentsDir, "notes.txt"), []byte("not an agent"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	files, err := DiscoverAgentsMdsWithCache(tmp, NewClaudeMdCache())
	if err != nil {
		t.Fatal(err)
		return
	}

	if len(files) != 2 {
		t.Fatalf("expected 2 agent files, got %d: %+v", len(files), files)
	}

	for _, f := range files {
		if f.Source != "agent" {
			t.Fatalf("expected source 'agent', got %q", f.Source)
		}
		if !strings.HasSuffix(f.Path, ".md") {
			t.Fatalf("expected .md file, got %q", f.Path)
		}
	}
}

func TestDiscoverAgentsMds_MissingDir(t *testing.T) {
	tmp := t.TempDir()

	// No .claude/agents/ directory
	files, err := DiscoverAgentsMdsWithCache(tmp, NewClaudeMdCache())
	if err != nil {
		t.Fatal(err)
		return
	}
	if len(files) != 0 {
		t.Fatalf("expected 0 files, got %d", len(files))
	}
}

func TestLoadClaudeMdContent(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	// Create CLAUDE.md
	if err := os.WriteFile(filepath.Join(tmp, "CLAUDE.md"), []byte("Always use gofmt."), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	content, err := LoadClaudeMdContentWithCache(tmp, NewClaudeMdCache())
	if err != nil {
		t.Fatal(err)
		return
	}

	if !strings.Contains(content, "Always use gofmt.") {
		t.Fatalf("expected content to include file contents, got %q", content)
	}
	if !strings.Contains(content, "instructions OVERRIDE") {
		t.Fatalf("expected content to include header, got %q", content)
	}
}

func TestLoadClaudeMdContent_Empty(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	content, err := LoadClaudeMdContentWithCache(tmp, NewClaudeMdCache())
	if err != nil {
		t.Fatal(err)
		return
	}
	if content != "" {
		t.Fatalf("expected empty content, got %q", content)
	}
}

func TestFormatClaudeMdContent(t *testing.T) {
	files := []ClaudeMdFile{
		{Path: "/home/user/.claude/CLAUDE.md", Content: "Global rule", Source: "user"},
		{Path: "/project/CLAUDE.md", Content: "Project rule", Source: "project"},
	}

	result := FormatClaudeMdContent(files)

	if !strings.Contains(result, "instructions OVERRIDE") {
		t.Fatal("expected header")
	}
	if !strings.Contains(result, "Global rule") {
		t.Fatal("expected user content")
	}
	if !strings.Contains(result, "Project rule") {
		t.Fatal("expected project content")
	}
	if !strings.Contains(result, "user's private global instructions") {
		t.Fatal("expected user description")
	}
	if !strings.Contains(result, "project instructions, checked into the codebase") {
		t.Fatal("expected project description")
	}
}

func TestFormatClaudeMdContent_EmptyFiles(t *testing.T) {
	result := FormatClaudeMdContent(nil)
	if result != "" {
		t.Fatalf("expected empty, got %q", result)
	}

	result = FormatClaudeMdContent([]ClaudeMdFile{
		{Path: "/x/CLAUDE.md", Content: "  \n  ", Source: "project"},
	})
	if result != "" {
		t.Fatalf("expected empty for whitespace-only, got %q", result)
	}
}

func TestComposeSystemPromptWithClaudeMd(t *testing.T) {
	base := "You are an assistant."
	claudeMd := "Always use tests."
	userCtx := map[string]string{"cwd": "/project"}
	sysCtx := map[string]string{"gitStatus": "clean"}

	result := ComposeSystemPromptWithClaudeMd(base, "Appendix.", claudeMd, userCtx, sysCtx)

	// Verify ordering: base, claudemd, user context, system context, appendix
	baseIdx := strings.Index(result, "You are an assistant.")
	claudeIdx := strings.Index(result, "Always use tests.")
	userIdx := strings.Index(result, "User context:")
	sysIdx := strings.Index(result, "System context:")
	appendIdx := strings.Index(result, "Appendix.")

	if baseIdx == -1 || claudeIdx == -1 || userIdx == -1 || sysIdx == -1 || appendIdx == -1 {
		t.Fatalf("missing expected sections in result: %q", result)
	}
	if baseIdx >= claudeIdx || claudeIdx >= userIdx || userIdx >= sysIdx || sysIdx >= appendIdx {
		t.Fatalf("sections not in expected order: base=%d, claude=%d, user=%d, sys=%d, append=%d",
			baseIdx, claudeIdx, userIdx, sysIdx, appendIdx)
	}
}

func TestClaudeMdCache_BasicGetSet(t *testing.T) {
	tmp := t.TempDir()
	cache := NewClaudeMdCache()

	filePath := filepath.Join(tmp, "test.md")
	if err := os.WriteFile(filePath, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	// Not in cache yet
	_, ok := cache.Get(filePath)
	if ok {
		t.Fatal("expected cache miss")
	}

	// Set cache
	cache.Set(filePath, "hello")

	// Now should hit
	content, ok := cache.Get(filePath)
	if !ok {
		t.Fatal("expected cache hit")
	}
	if content != "hello" {
		t.Fatalf("expected 'hello', got %q", content)
	}
}

func TestClaudeMdCache_InvalidatesOnModTime(t *testing.T) {
	tmp := t.TempDir()
	cache := NewClaudeMdCache()

	filePath := filepath.Join(tmp, "test.md")
	if err := os.WriteFile(filePath, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	cache.Set(filePath, "v1")

	// Verify cache hit
	content, ok := cache.Get(filePath)
	if !ok || content != "v1" {
		t.Fatal("expected cache hit with v1")
	}

	// Modify file (change both content and modtime)
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(filePath, []byte("v2-longer"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	// Should be stale now (size changed)
	_, ok = cache.Get(filePath)
	if ok {
		t.Fatal("expected cache miss after file modification")
	}
}

func TestClaudeMdCache_InvalidatesOnDelete(t *testing.T) {
	tmp := t.TempDir()
	cache := NewClaudeMdCache()

	filePath := filepath.Join(tmp, "test.md")
	if err := os.WriteFile(filePath, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	cache.Set(filePath, "hello")

	// Delete file
	if err := os.Remove(filePath); err != nil {
		t.Fatal(err)
		return
	}

	// Should return cache miss
	_, ok := cache.Get(filePath)
	if ok {
		t.Fatal("expected cache miss after file deletion")
	}
}

func TestClaudeMdCache_Clear(t *testing.T) {
	tmp := t.TempDir()
	cache := NewClaudeMdCache()

	filePath := filepath.Join(tmp, "test.md")
	if err := os.WriteFile(filePath, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	cache.Set(filePath, "hello")
	cache.Clear()

	_, ok := cache.Get(filePath)
	if ok {
		t.Fatal("expected cache miss after Clear()")
	}
}

func TestClaudeMdCache_Invalidate(t *testing.T) {
	tmp := t.TempDir()
	cache := NewClaudeMdCache()

	filePath := filepath.Join(tmp, "test.md")
	if err := os.WriteFile(filePath, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	cache.Set(filePath, "hello")
	cache.Invalidate(filePath)

	_, ok := cache.Get(filePath)
	if ok {
		t.Fatal("expected cache miss after Invalidate()")
	}
}

func TestCollectParentDirs(t *testing.T) {
	// Test with a known path structure
	dirs := collectParentDirs("/a/b/c/d")

	// Should be root-to-cwd order: /, /a, /a/b, /a/b/c
	expected := []string{"/", "/a", "/a/b", "/a/b/c"}
	if len(dirs) != len(expected) {
		t.Fatalf("expected %d dirs, got %d: %v", len(expected), len(dirs), dirs)
	}
	for i, d := range dirs {
		if d != expected[i] {
			t.Fatalf("dirs[%d] = %q, want %q", i, d, expected[i])
		}
	}
}

func TestSourceDescription(t *testing.T) {
	tests := []struct {
		source string
		want   string
	}{
		{"project", "(project instructions, checked into the codebase)"},
		{"project-dot-claude", "(project instructions, checked into the codebase)"},
		{"user", "(user's private global instructions for all projects)"},
		{"parent", "(project instructions from parent directory)"},
		{"agent", "(agent definition)"},
		{"unknown", ""},
	}

	for _, tt := range tests {
		got := sourceDescription(tt.source)
		if tt.want != "" && !strings.Contains(got, tt.want) {
			t.Errorf("sourceDescription(%q) = %q, want containing %q", tt.source, got, tt.want)
		}
		if tt.want == "" && got != "" {
			t.Errorf("sourceDescription(%q) = %q, want empty", tt.source, got)
		}
	}
}

func TestDiscoverClaudeMds_CacheReuse(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	filePath := filepath.Join(tmp, "CLAUDE.md")
	if err := os.WriteFile(filePath, []byte("cached content"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	cache := NewClaudeMdCache()

	// First call populates cache
	files1, err := DiscoverClaudeMdsWithCache(tmp, cache)
	if err != nil {
		t.Fatal(err)
		return
	}
	if len(files1) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files1))
	}

	// Second call should use cache
	files2, err := DiscoverClaudeMdsWithCache(tmp, cache)
	if err != nil {
		t.Fatal(err)
		return
	}
	if len(files2) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files2))
	}
	if files2[0].Content != "cached content" {
		t.Fatalf("expected cached content, got %q", files2[0].Content)
	}
}
