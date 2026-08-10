package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
		return
	}
}

func TestParseSkillFileWithFrontmatterAndFallbackName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "review.md")
	writeSkill(t, path, `---
name: code-review
description: Review code changes
tags: [quality, review]
args:
  - name: focus
    description: What to focus on
    required: true
---
Review with focus on {{focus}}.
`)

	skill, err := ParseSkillFile(path)
	if err != nil {
		t.Fatalf("ParseSkillFile failed: %v", err)
		return
	}
	if skill.Name != "code-review" || skill.Description != "Review code changes" {
		t.Fatalf("unexpected skill metadata: %#v", skill)
	}
	if len(skill.Tags) != 2 || skill.Tags[0] != "quality" || len(skill.Args) != 1 || !skill.Args[0].Required {
		t.Fatalf("unexpected tags/args: %#v", skill)
	}
	if !strings.Contains(skill.Content, "Review with focus") {
		t.Fatalf("unexpected skill body: %q", skill.Content)
	}
	if !filepath.IsAbs(skill.FilePath) {
		t.Fatalf("expected absolute file path, got %q", skill.FilePath)
	}

	noFrontmatter := filepath.Join(dir, "plain.md")
	writeSkill(t, noFrontmatter, "Plain body")
	plain, err := ParseSkillFile(noFrontmatter)
	if err != nil {
		t.Fatalf("ParseSkillFile without frontmatter failed: %v", err)
		return
	}
	if plain.Name != "plain" || plain.Content != "Plain body" {
		t.Fatalf("unexpected fallback skill: %#v", plain)
	}
}

func TestSkillRegistryLoadSearchAndInvoke(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, filepath.Join(dir, "nested", "test.md"), `---
name: test-skill
description: Run project checks
args:
  - name: target
    required: true
  - name: mode
    default: quick
---
Run {{mode}} checks for {{target}} and {{extra}}.
`)
	writeSkill(t, filepath.Join(dir, "skip.txt"), "not markdown")
	writeSkill(t, filepath.Join(dir, "bad.md"), "---\nname: [bad\n---\n")

	registry := NewSkillRegistry()
	if err := registry.LoadFromDirectory(dir); err != nil {
		t.Fatalf("LoadFromDirectory failed: %v", err)
		return
	}
	if _, ok := registry.Get("bad"); ok {
		t.Fatal("malformed skill should be skipped")
	}
	found := registry.Search("project")
	if len(found) != 1 || found[0].Name != "test-skill" {
		t.Fatalf("unexpected search results: %#v", found)
	}
	if _, err := registry.Invoke("test-skill", map[string]string{}); err == nil || !strings.Contains(err.Error(), "required argument") {
		t.Fatalf("expected required argument error, got %v", err)
		return
	}
	content, err := registry.Invoke("test-skill", map[string]string{"target": "repo", "extra": "report"})
	if err != nil {
		t.Fatalf("Invoke failed: %v", err)
		return
	}
	if !strings.Contains(content, "Run quick checks for repo and report.") {
		t.Fatalf("unexpected invoked content: %q", content)
	}
}

func TestSkillRegistryForProjectDirectoryReplacesOnlyProjectGeneration(t *testing.T) {
	registry := NewSkillRegistry()
	registry.Register(&Skill{
		Name:    "project-skill",
		Content: "dirty parent content",
		Source:  "project",
	})
	registry.Register(&Skill{
		Name:    "runtime-skill",
		Content: "runtime content",
		Source:  "runtime",
	})
	worktree := t.TempDir()
	writeSkill(
		t,
		filepath.Join(worktree, ".claude", "skills", "project.md"),
		"---\nname: project-skill\n---\ncommitted worktree content\n",
	)

	scoped, err := registry.ForProjectDirectory(worktree)
	if err != nil {
		t.Fatal(err)
	}
	project, ok := scoped.Get("project-skill")
	if !ok || !strings.Contains(project.Content, "committed worktree content") {
		t.Fatalf("project skill = %#v", project)
	}
	runtimeSkill, ok := scoped.Get("runtime-skill")
	if !ok || runtimeSkill.Content != "runtime content" {
		t.Fatalf("runtime skill = %#v", runtimeSkill)
	}
	original, _ := registry.Get("project-skill")
	if original.Content != "dirty parent content" {
		t.Fatalf("source registry mutated: %#v", original)
	}
}

func TestRegisterAndLoadDefaultSkillsPrecedence(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	project := filepath.Join(dir, "project")
	t.Setenv("HOME", home)

	writeSkill(t, filepath.Join(home, ".claude", "skills", "same.md"), `---
name: same
description: user desc
---
user body`)
	writeSkill(t, filepath.Join(project, ".claude", "skills", "same.md"), `---
name: same
description: project desc
---
project body`)

	registry, err := LoadDefaultSkills(project)
	if err != nil {
		t.Fatalf("LoadDefaultSkills failed: %v", err)
		return
	}
	skill, ok := registry.Get("same")
	if !ok {
		t.Fatal("expected skill loaded")
	}
	if skill.Description != "project desc" || !strings.Contains(skill.Content, "project body") {
		t.Fatalf("project skill should override user skill, got %#v", skill)
	}

	registry.Register(&Skill{Name: "manual", Description: "manual desc", Content: "manual"})
	if got, ok := registry.Get("manual"); !ok || got.Content != "manual" {
		t.Fatalf("Register failed, got %#v ok=%v", got, ok)
	}
}

func TestSkillSnapshotRetainsSourceAndMalformedFileDiagnostics(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(dir, "valid.md"),
		[]byte("---\nname: valid\ndescription: valid skill\n---\nbody"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, "invalid.md"),
		[]byte("---\nname: [\n---\nbody"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	registry := NewSkillRegistry()
	if err := registry.LoadFromDirectoryWithSource(dir, "project"); err != nil {
		t.Fatal(err)
	}
	snapshot := registry.Snapshot()
	if len(snapshot.Skills) != 1 ||
		snapshot.Skills[0].Name != "valid" ||
		snapshot.Skills[0].Source != "project" ||
		snapshot.Skills[0].Health != "available" {
		t.Fatalf("skill snapshot = %#v", snapshot.Skills)
	}
	if len(snapshot.Diagnostics) != 1 ||
		snapshot.Diagnostics[0].Source != "project" ||
		!strings.HasSuffix(snapshot.Diagnostics[0].FilePath, "invalid.md") {
		t.Fatalf("skill diagnostics = %#v", snapshot.Diagnostics)
	}

	snapshot.Skills[0].Name = "mutated"
	again := registry.Snapshot()
	if again.Skills[0].Name != "valid" {
		t.Fatalf("snapshot mutated live skill: %#v", again.Skills)
	}
}

func TestLoadFromDirectoryErrorsOnFilePath(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "file.md")
	writeSkill(t, filePath, "body")
	if err := NewSkillRegistry().LoadFromDirectory(filePath); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("expected not-a-directory error, got %v", err)
		return
	}
}

func TestParseSkillDataRetainsAttributionWithoutReopeningPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "authorized.md")
	skill, err := ParseSkillData(
		path,
		[]byte("---\nname: authorized\ndescription: Authorized bytes\n---\nBody."),
	)
	if err != nil {
		t.Fatal(err)
	}
	if skill.Name != "authorized" ||
		skill.Description != "Authorized bytes" ||
		skill.Content != "Body." ||
		skill.FilePath != path {
		t.Fatalf("parsed authorized skill = %#v", skill)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("ParseSkillData unexpectedly created or reopened %s: %v", path, err)
	}
}

func TestSkillRegistryMergeSnapshotAppliesOneCompleteBatch(t *testing.T) {
	registry := NewSkillRegistry()
	registry.Register(&Skill{Name: "existing", Content: "old"})
	registry.MergeSnapshot(Snapshot{
		Skills: []*Skill{
			{Name: "new", Content: "new"},
			{Name: "existing", Content: "replacement"},
		},
		Diagnostics: []Diagnostic{{
			Source:   "plugin",
			FilePath: "invalid.md",
			Message:  "invalid",
		}},
	})

	existing, ok := registry.Get("existing")
	if !ok || existing.Content != "replacement" ||
		existing.Source != "runtime" ||
		existing.Health != "available" {
		t.Fatalf("existing skill after merge = %#v ok=%v", existing, ok)
	}
	if skill, ok := registry.Get("new"); !ok || skill.Content != "new" {
		t.Fatalf("new skill after merge = %#v ok=%v", skill, ok)
	}
	snapshot := registry.Snapshot()
	if len(snapshot.Diagnostics) != 1 ||
		snapshot.Diagnostics[0].Source != "plugin" {
		t.Fatalf("merged diagnostics = %#v", snapshot.Diagnostics)
	}
}
