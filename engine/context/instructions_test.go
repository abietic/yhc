package promptctx

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- InstructionDiscovery tests ---

func TestInstructionDiscovery_BasicAGENTSmd(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	// Create AGENTS.md in the project root
	agentsMd := filepath.Join(tmp, "AGENTS.md")
	if err := os.WriteFile(agentsMd, []byte("# Project Rules\nUse Go."), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	d := NewInstructionDiscovery(nil)
	files, err := d.Discover(tmp)
	if err != nil {
		t.Fatal(err)
		return
	}

	found := false
	for _, f := range files {
		if f.Path == agentsMd {
			found = true
			if f.Filename != "AGENTS.md" {
				t.Fatalf("expected Filename=AGENTS.md, got %q", f.Filename)
			}
			if !strings.Contains(f.Content, "Use Go.") {
				t.Fatalf("unexpected content: %q", f.Content)
			}
			if f.ScopeRoot != tmp {
				t.Fatalf("expected ScopeRoot=%q, got %q", tmp, f.ScopeRoot)
			}
		}
	}
	if !found {
		t.Fatalf("expected to find AGENTS.md, got files: %+v", files)
	}
}

func TestInstructionDiscovery_BothAGENTSandCLAUDE(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	// Create both AGENTS.md and CLAUDE.md
	if err := os.WriteFile(filepath.Join(tmp, "AGENTS.md"), []byte("agents rules"), 0o644); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(tmp, "CLAUDE.md"), []byte("claude rules"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	d := NewInstructionDiscovery(nil)
	files, err := d.Discover(tmp)
	if err != nil {
		t.Fatal(err)
		return
	}

	foundAgents := false
	foundClaude := false
	for _, f := range files {
		if f.Filename == "AGENTS.md" {
			foundAgents = true
		}
		if f.Filename == "CLAUDE.md" {
			foundClaude = true
		}
	}
	if !foundAgents {
		t.Fatal("expected to find AGENTS.md")
	}
	if !foundClaude {
		t.Fatal("expected to find CLAUDE.md")
	}
}

func TestInstructionDiscovery_DeeperTakesPrecedence(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	// Create hierarchy:
	// tmp/AGENTS.md (shallow = lower priority)
	// tmp/packages/myapp/AGENTS.md (deeper = higher priority)
	if err := os.WriteFile(filepath.Join(tmp, "AGENTS.md"), []byte("root rules"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	projectDir := filepath.Join(tmp, "packages", "myapp")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(projectDir, "AGENTS.md"), []byte("myapp rules"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	d := NewInstructionDiscovery(nil)
	files, err := d.Discover(projectDir)
	if err != nil {
		t.Fatal(err)
		return
	}

	// Files should be in priority order: shallowest first, deepest last.
	var agentsFiles []InstructionFile
	for _, f := range files {
		if f.Filename == "AGENTS.md" {
			agentsFiles = append(agentsFiles, f)
		}
	}

	if len(agentsFiles) < 2 {
		t.Fatalf("expected at least 2 AGENTS.md files, got %d", len(agentsFiles))
	}

	// Last (deepest) should be the project-level one with higher depth.
	last := agentsFiles[len(agentsFiles)-1]
	if !strings.Contains(last.Content, "myapp rules") {
		t.Fatalf("expected last (highest priority) to be myapp rules, got %q", last.Content)
	}

	// First (shallowest) should be the root one.
	first := agentsFiles[0]
	if !strings.Contains(first.Content, "root rules") {
		t.Fatalf("expected first (lowest priority) to be root rules, got %q", first.Content)
	}

	// Deeper file should have higher depth value.
	if last.Depth <= first.Depth {
		t.Fatalf("expected deeper file to have higher Depth, got first.Depth=%d, last.Depth=%d", first.Depth, last.Depth)
	}
}

func TestInstructionDiscovery_DotClaudeSubdir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	// Create .claude/AGENTS.md
	dotClaude := filepath.Join(tmp, ".claude")
	if err := os.MkdirAll(dotClaude, 0o755); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(dotClaude, "AGENTS.md"), []byte("dot-claude agents"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	d := NewInstructionDiscovery(nil)
	files, err := d.Discover(tmp)
	if err != nil {
		t.Fatal(err)
		return
	}

	found := false
	for _, f := range files {
		if strings.Contains(f.Path, ".claude/AGENTS.md") {
			found = true
			if !strings.Contains(f.Content, "dot-claude agents") {
				t.Fatalf("unexpected content: %q", f.Content)
			}
		}
	}
	if !found {
		t.Fatalf("expected to find .claude/AGENTS.md, got files: %+v", files)
	}
}

func TestInstructionDiscovery_EmptyFileSkipped(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	if err := os.WriteFile(filepath.Join(tmp, "AGENTS.md"), []byte("   \n  \t  "), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	d := NewInstructionDiscovery(nil)
	files, err := d.Discover(tmp)
	if err != nil {
		t.Fatal(err)
		return
	}

	for _, f := range files {
		if f.Filename == "AGENTS.md" {
			t.Fatal("expected empty AGENTS.md to be skipped")
		}
	}
}

func TestInstructionDiscovery_MissingFilesGraceful(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	projectDir := filepath.Join(tmp, "empty-project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}

	d := NewInstructionDiscovery(nil)
	files, err := d.Discover(projectDir)
	if err != nil {
		t.Fatal(err)
		return
	}
	if len(files) != 0 {
		t.Fatalf("expected 0 files for empty project, got %d: %+v", len(files), files)
	}
}

func TestInstructionDiscovery_PermissionErrors(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	// Create a file then make it unreadable
	agentsMd := filepath.Join(tmp, "AGENTS.md")
	if err := os.WriteFile(agentsMd, []byte("secret rules"), 0o644); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.Chmod(agentsMd, 0o000); err != nil {
		t.Skip("cannot change file permissions on this platform")
	}
	defer os.Chmod(agentsMd, 0o644) //nolint:errcheck // restore for cleanup

	d := NewInstructionDiscovery(nil)
	files, err := d.Discover(tmp)
	if err != nil {
		t.Fatal(err)
		return
	}

	// Should gracefully skip unreadable files
	for _, f := range files {
		if f.Path == agentsMd {
			t.Fatal("expected unreadable file to be skipped")
		}
	}
}

func TestInstructionDiscovery_CLAUDELocalMd(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	if err := os.WriteFile(filepath.Join(tmp, "CLAUDE.local.md"), []byte("local overrides"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	d := NewInstructionDiscovery(nil)
	files, err := d.Discover(tmp)
	if err != nil {
		t.Fatal(err)
		return
	}

	found := false
	for _, f := range files {
		if f.Filename == "CLAUDE.local.md" {
			found = true
			if !strings.Contains(f.Content, "local overrides") {
				t.Fatalf("unexpected content: %q", f.Content)
			}
		}
	}
	if !found {
		t.Fatal("expected to find CLAUDE.local.md")
	}
}

// --- Scope tracking tests ---

func TestInstructionDiscovery_FilesForPath(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	// Create hierarchy:
	// tmp/AGENTS.md (scope: tmp/)
	// tmp/packages/myapp/AGENTS.md (scope: tmp/packages/myapp/)
	if err := os.WriteFile(filepath.Join(tmp, "AGENTS.md"), []byte("root rules"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	myappDir := filepath.Join(tmp, "packages", "myapp")
	if err := os.MkdirAll(myappDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(myappDir, "AGENTS.md"), []byte("myapp rules"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	d := NewInstructionDiscovery(nil)
	if _, err := d.Discover(myappDir); err != nil {
		t.Fatal(err)
		return
	}

	// A file in myapp/src/ should be covered by both AGENTS.md files
	targetFile := filepath.Join(myappDir, "src", "main.go")
	matching := d.FilesForPath(targetFile)

	if len(matching) < 2 {
		t.Fatalf("expected at least 2 matching files for %s, got %d: %+v", targetFile, len(matching), matching)
	}

	// A file directly in tmp/ should only be covered by the root AGENTS.md
	rootFile := filepath.Join(tmp, "README.md")
	rootMatching := d.FilesForPath(rootFile)

	foundRoot := false
	for _, f := range rootMatching {
		if strings.Contains(f.Content, "root rules") {
			foundRoot = true
		}
		if strings.Contains(f.Content, "myapp rules") {
			t.Fatal("root-level file should not be covered by myapp rules")
		}
	}
	if !foundRoot {
		t.Fatal("expected root file to be covered by root AGENTS.md")
	}
}

func TestInstructionDiscovery_FilesForPath_ExactScopeRoot(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	if err := os.WriteFile(filepath.Join(tmp, "AGENTS.md"), []byte("rules"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	d := NewInstructionDiscovery(nil)
	if _, err := d.Discover(tmp); err != nil {
		t.Fatal(err)
		return
	}

	// A file exactly at the scope root should match
	matching := d.FilesForPath(filepath.Join(tmp, "file.go"))
	found := false
	for _, f := range matching {
		if f.Filename == "AGENTS.md" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected file at scope root to match")
	}
}

// --- Dynamic refresh tests ---

func TestInstructionDiscovery_HasChanged_NoChange(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	if err := os.WriteFile(filepath.Join(tmp, "AGENTS.md"), []byte("rules"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	d := NewInstructionDiscovery(nil)
	if _, err := d.Discover(tmp); err != nil {
		t.Fatal(err)
		return
	}

	if d.HasChanged() {
		t.Fatal("expected no change immediately after discovery")
	}
}

func TestInstructionDiscovery_HasChanged_FileModified(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	agentsMd := filepath.Join(tmp, "AGENTS.md")
	if err := os.WriteFile(agentsMd, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	d := NewInstructionDiscovery(nil)
	if _, err := d.Discover(tmp); err != nil {
		t.Fatal(err)
		return
	}

	// Modify the file (change size to ensure different stat)
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(agentsMd, []byte("v2-updated-content"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	if !d.HasChanged() {
		t.Fatal("expected change to be detected after file modification")
	}
}

func TestInstructionDiscovery_HasChanged_FileDeleted(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	agentsMd := filepath.Join(tmp, "AGENTS.md")
	if err := os.WriteFile(agentsMd, []byte("rules"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	d := NewInstructionDiscovery(nil)
	if _, err := d.Discover(tmp); err != nil {
		t.Fatal(err)
		return
	}

	if err := os.Remove(agentsMd); err != nil {
		t.Fatal(err)
		return
	}

	if !d.HasChanged() {
		t.Fatal("expected change to be detected after file deletion")
	}
}

func TestInstructionDiscovery_HasChanged_NewFileCreated(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	// Start with just CLAUDE.md
	if err := os.WriteFile(filepath.Join(tmp, "CLAUDE.md"), []byte("claude"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	d := NewInstructionDiscovery(nil)
	if _, err := d.Discover(tmp); err != nil {
		t.Fatal(err)
		return
	}

	// Now create AGENTS.md
	if err := os.WriteFile(filepath.Join(tmp, "AGENTS.md"), []byte("agents"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	if !d.HasChanged() {
		t.Fatal("expected change to be detected when new file is created")
	}
}

func TestInstructionDiscovery_Refresh(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	agentsMd := filepath.Join(tmp, "AGENTS.md")
	if err := os.WriteFile(agentsMd, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	d := NewInstructionDiscovery(nil)
	if _, err := d.Discover(tmp); err != nil {
		t.Fatal(err)
		return
	}

	// No change — refresh should return false.
	changed, err := d.Refresh("")
	if err != nil {
		t.Fatal(err)
		return
	}
	if changed {
		t.Fatal("expected no refresh when nothing changed")
	}

	// Modify file
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(agentsMd, []byte("v2-new-content"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	// Refresh should detect the change and reload.
	changed, err = d.Refresh("")
	if err != nil {
		t.Fatal(err)
		return
	}
	if !changed {
		t.Fatal("expected refresh to detect change")
	}

	// Verify updated content
	files := d.GetActiveFiles()
	found := false
	for _, f := range files {
		if f.Filename == "AGENTS.md" {
			found = true
			if !strings.Contains(f.Content, "v2-new-content") {
				t.Fatalf("expected refreshed content, got %q", f.Content)
			}
		}
	}
	if !found {
		t.Fatal("expected AGENTS.md in refreshed files")
	}
}

func TestInstructionDiscovery_Refresh_WithExplicitCWD(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	// Initial discover in subdir
	subdir := filepath.Join(tmp, "sub")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(subdir, "AGENTS.md"), []byte("sub rules"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	d := NewInstructionDiscovery(nil)
	if _, err := d.Discover(subdir); err != nil {
		t.Fatal(err)
		return
	}

	// Modify and refresh with explicit CWD
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(subdir, "AGENTS.md"), []byte("updated sub rules"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	changed, err := d.Refresh(subdir)
	if err != nil {
		t.Fatal(err)
		return
	}
	if !changed {
		t.Fatal("expected refresh to detect change")
	}
}

// --- Prompt assembly tests ---

func TestAssemblePrompt_BasicAssembly(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	if err := os.WriteFile(filepath.Join(tmp, "AGENTS.md"), []byte("project rules"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	d := NewInstructionDiscovery(nil)
	if _, err := d.Discover(tmp); err != nil {
		t.Fatal(err)
		return
	}

	assembly := AssemblePrompt("You are an AI assistant.", d, "Always be concise.")
	rendered := assembly.Render()

	// Verify all three components are present
	if !strings.Contains(rendered, "You are an AI assistant.") {
		t.Fatal("expected base prompt in rendered output")
	}
	if !strings.Contains(rendered, "project rules") {
		t.Fatal("expected instruction content in rendered output")
	}
	if !strings.Contains(rendered, "Always be concise.") {
		t.Fatal("expected custom instructions in rendered output")
	}
}

func TestAssemblePrompt_PriorityOrder(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	if err := os.WriteFile(filepath.Join(tmp, "AGENTS.md"), []byte("INSTRUCTION_MARKER"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	d := NewInstructionDiscovery(nil)
	if _, err := d.Discover(tmp); err != nil {
		t.Fatal(err)
		return
	}

	assembly := AssemblePrompt("BASE_MARKER", d, "CUSTOM_MARKER")
	rendered := assembly.Render()

	baseIdx := strings.Index(rendered, "BASE_MARKER")
	instrIdx := strings.Index(rendered, "INSTRUCTION_MARKER")
	customIdx := strings.Index(rendered, "CUSTOM_MARKER")

	if baseIdx == -1 || instrIdx == -1 || customIdx == -1 {
		t.Fatalf("missing components in rendered: %q", rendered)
	}

	// Order should be: base < instructions < custom
	if baseIdx >= instrIdx || instrIdx >= customIdx {
		t.Fatalf("expected priority order base(%d) < instructions(%d) < custom(%d)", baseIdx, instrIdx, customIdx)
	}
}

func TestAssemblePrompt_NoDiscovery(t *testing.T) {
	assembly := AssemblePrompt("Base prompt.", nil, "")
	rendered := assembly.Render()

	if !strings.Contains(rendered, "Base prompt.") {
		t.Fatalf("expected base prompt, got %q", rendered)
	}
}

func TestAssemblePrompt_EmptyBase(t *testing.T) {
	assembly := AssemblePrompt("", nil, "Custom only.")
	rendered := assembly.Render()

	if !strings.Contains(rendered, "Custom only.") {
		t.Fatalf("expected custom instructions, got %q", rendered)
	}
}

func TestAssemblePromptForPath_FiltersByScope(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	// Create hierarchy with two AGENTS.md at different levels
	if err := os.WriteFile(filepath.Join(tmp, "AGENTS.md"), []byte("root rules"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	subDir := filepath.Join(tmp, "subproject")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(subDir, "AGENTS.md"), []byte("sub rules"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	d := NewInstructionDiscovery(nil)
	if _, err := d.Discover(subDir); err != nil {
		t.Fatal(err)
		return
	}

	// File in subproject should see both
	subAssembly := AssemblePromptForPath("Base.", d, "", filepath.Join(subDir, "main.go"))
	subRendered := subAssembly.Render()
	if !strings.Contains(subRendered, "root rules") {
		t.Fatal("expected root rules for subproject file")
	}
	if !strings.Contains(subRendered, "sub rules") {
		t.Fatal("expected sub rules for subproject file")
	}

	// File at root level should only see root rules
	rootAssembly := AssemblePromptForPath("Base.", d, "", filepath.Join(tmp, "README.md"))
	rootRendered := rootAssembly.Render()
	if !strings.Contains(rootRendered, "root rules") {
		t.Fatal("expected root rules for root-level file")
	}
	if strings.Contains(rootRendered, "sub rules") {
		t.Fatal("root-level file should NOT see sub rules")
	}
}

func TestAssemblePrompt_RenderWithHeader(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	if err := os.WriteFile(filepath.Join(tmp, "AGENTS.md"), []byte("rules"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	d := NewInstructionDiscovery(nil)
	if _, err := d.Discover(tmp); err != nil {
		t.Fatal(err)
		return
	}

	assembly := AssemblePrompt("Base.", d, "")
	withHeader := assembly.RenderWithHeader()

	if !strings.Contains(withHeader, "instructions OVERRIDE") {
		t.Fatal("expected instruction override header")
	}
}

func TestAssemblePrompt_RenderWithHeader_NoInstructions(t *testing.T) {
	assembly := AssemblePrompt("Base prompt.", nil, "")
	withHeader := assembly.RenderWithHeader()

	if strings.Contains(withHeader, "instructions OVERRIDE") {
		t.Fatal("expected no header when there are no instruction files")
	}
	if !strings.Contains(withHeader, "Base prompt.") {
		t.Fatal("expected base prompt")
	}
}

func TestAssemblePrompt_Labels(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	if err := os.WriteFile(filepath.Join(tmp, "AGENTS.md"), []byte("rules"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	d := NewInstructionDiscovery(nil)
	if _, err := d.Discover(tmp); err != nil {
		t.Fatal(err)
		return
	}

	assembly := AssemblePrompt("Base.", d, "Custom.")
	labels := assembly.Labels()

	if len(labels) != 3 {
		t.Fatalf("expected 3 labels, got %d: %v", len(labels), labels)
	}
	if labels[0] != "base_system_prompt" {
		t.Fatalf("expected first label to be base_system_prompt, got %q", labels[0])
	}
	if !strings.HasPrefix(labels[1], "instruction:") {
		t.Fatalf("expected second label to start with instruction:, got %q", labels[1])
	}
	if labels[2] != "custom_instructions" {
		t.Fatalf("expected third label to be custom_instructions, got %q", labels[2])
	}
}

// --- Helper function tests ---

func TestPathIsUnder(t *testing.T) {
	tests := []struct {
		target string
		root   string
		want   bool
	}{
		{"/a/b/c/file.go", "/a/b", true},
		{"/a/b/c", "/a/b/c", true}, // exact match
		{"/a/b", "/a/b/c", false},  // parent is not under child
		{"/a/b-other/file.go", "/a/b", false},
		{"/x/y/z", "/a/b", false},
		{"", "/a", false},
		{"/a/b", "", false},
	}

	for _, tt := range tests {
		got := pathIsUnder(tt.target, tt.root)
		if got != tt.want {
			t.Errorf("pathIsUnder(%q, %q) = %v, want %v", tt.target, tt.root, got, tt.want)
		}
	}
}

func TestCollectAncestorDirs(t *testing.T) {
	dirs := collectAncestorDirs("/a/b/c/d")

	// Should be root-to-leaf order: /, /a, /a/b, /a/b/c
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

func TestCountPathComponents(t *testing.T) {
	tests := []struct {
		path string
		want int
	}{
		{"/", 0},
		{".", 0},
		{"/a", 1},
		{"/a/b", 2},
		{"/a/b/c", 3},
		{"/a/b/c/d/e", 5},
	}

	for _, tt := range tests {
		got := countPathComponents(tt.path)
		if got != tt.want {
			t.Errorf("countPathComponents(%q) = %d, want %d", tt.path, got, tt.want)
		}
	}
}

// --- Integration-style tests ---

func TestInstructionDiscovery_FullHierarchy(t *testing.T) {
	tmp := t.TempDir()

	// Set up fake home
	fakeHome := filepath.Join(tmp, "fakehome")
	if err := os.MkdirAll(filepath.Join(fakeHome, ".claude"), 0o755); err != nil {
		t.Fatal(err)
		return
	}
	t.Setenv("HOME", fakeHome)
	if err := os.WriteFile(filepath.Join(fakeHome, ".claude", "CLAUDE.md"), []byte("user global"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	// Create a realistic repo structure:
	// tmp/repo/AGENTS.md (monorepo root)
	// tmp/repo/CLAUDE.md (monorepo root)
	// tmp/repo/packages/lib/AGENTS.md (package-level)
	repoRoot := filepath.Join(tmp, "repo")
	pkgDir := filepath.Join(repoRoot, "packages", "lib")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "AGENTS.md"), []byte("monorepo agents"), 0o644); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "CLAUDE.md"), []byte("monorepo claude"), 0o644); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "AGENTS.md"), []byte("package agents"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	d := NewInstructionDiscovery(nil)
	files, err := d.Discover(pkgDir)
	if err != nil {
		t.Fatal(err)
		return
	}

	// Should find: user CLAUDE.md, repo AGENTS.md, repo CLAUDE.md, package AGENTS.md
	if len(files) < 4 {
		t.Fatalf("expected at least 4 files, got %d: %+v", len(files), files)
	}

	// Verify ordering: user (shallowest) first, package (deepest) last
	firstSource := files[0].Source
	if firstSource != "user" {
		t.Fatalf("expected first file to be user source, got %q (path: %s)", firstSource, files[0].Path)
	}

	lastFile := files[len(files)-1]
	if !strings.Contains(lastFile.Content, "package agents") {
		t.Fatalf("expected last file (highest priority) to be package AGENTS.md, got %q", lastFile.Content)
	}
}

func TestInstructionDiscovery_RulesDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	// Create .claude/rules/ with multiple .md files
	rulesDir := filepath.Join(tmp, ".claude", "rules")
	if err := os.MkdirAll(rulesDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(rulesDir, "01-style.md"), []byte("style rules"), 0o644); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(rulesDir, "02-testing.md"), []byte("testing rules"), 0o644); err != nil {
		t.Fatal(err)
		return
	}
	// Non-md files should be skipped
	if err := os.WriteFile(filepath.Join(rulesDir, "notes.txt"), []byte("ignore me"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	d := NewInstructionDiscovery(nil)
	files, err := d.Discover(tmp)
	if err != nil {
		t.Fatal(err)
		return
	}

	ruleCount := 0
	for _, f := range files {
		if f.Source == "project-rule" {
			ruleCount++
		}
	}
	if ruleCount != 2 {
		t.Fatalf("expected 2 rule files, got %d", ruleCount)
	}
}

func TestInstructionDiscovery_Symlinks(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	// Create a real file and a symlink to it
	realDir := filepath.Join(tmp, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(realDir, "AGENTS.md"), []byte("real rules"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	// Create symlink
	linkDir := filepath.Join(tmp, "linked")
	if err := os.MkdirAll(linkDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}
	linkPath := filepath.Join(linkDir, "AGENTS.md")
	if err := os.Symlink(filepath.Join(realDir, "AGENTS.md"), linkPath); err != nil {
		t.Skip("symlinks not supported on this platform")
	}

	d := NewInstructionDiscovery(nil)
	files, err := d.Discover(linkDir)
	if err != nil {
		t.Fatal(err)
		return
	}

	found := false
	for _, f := range files {
		if f.Path == linkPath {
			found = true
			if !strings.Contains(f.Content, "real rules") {
				t.Fatalf("expected symlinked content, got %q", f.Content)
			}
		}
	}
	if !found {
		t.Fatalf("expected to find symlinked AGENTS.md, got: %+v", files)
	}
}

func TestInstructionDiscovery_DeeplyNestedRepo(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "no-home"))

	// Create a deeply nested path with AGENTS.md at multiple levels
	levels := []string{"a", "b", "c", "d", "e"}
	currentPath := tmp
	for i, level := range levels {
		currentPath = filepath.Join(currentPath, level)
		if err := os.MkdirAll(currentPath, 0o755); err != nil {
			t.Fatal(err)
			return
		}
		// Put AGENTS.md at levels 0, 2, 4
		if i%2 == 0 {
			content := fmt.Sprintf("level %d rules", i)
			if err := os.WriteFile(filepath.Join(currentPath, "AGENTS.md"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
				return
			}
		}
	}

	d := NewInstructionDiscovery(nil)
	files, err := d.Discover(currentPath)
	if err != nil {
		t.Fatal(err)
		return
	}

	// Should find AGENTS.md files at levels 0, 2, 4 (3 files)
	agentsCount := 0
	for _, f := range files {
		if f.Filename == "AGENTS.md" {
			agentsCount++
		}
	}
	if agentsCount != 3 {
		t.Fatalf("expected 3 AGENTS.md files, got %d", agentsCount)
	}

	// Verify depth ordering: last should have highest depth
	var agentsFiles []InstructionFile
	for _, f := range files {
		if f.Filename == "AGENTS.md" {
			agentsFiles = append(agentsFiles, f)
		}
	}
	for i := 1; i < len(agentsFiles); i++ {
		if agentsFiles[i].Depth <= agentsFiles[i-1].Depth {
			t.Fatalf("expected increasing depth, got %d <= %d at index %d",
				agentsFiles[i].Depth, agentsFiles[i-1].Depth, i)
		}
	}
}
