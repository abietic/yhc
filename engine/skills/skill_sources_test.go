package skills

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDefaultSkillsSourcePrecedenceAndBundles(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	project := filepath.Join(root, "project")
	t.Setenv("HOME", home)

	writeSkill(t, filepath.Join(home, ".claude", "skills", "same.md"), "---\nname: same\n---\nuser claude")
	writeSkill(t, filepath.Join(home, ".agents", "skills", "same.md"), "---\nname: same\n---\nuser agents")
	writeSkill(t, filepath.Join(project, ".claude", "skills", "same.md"), "---\nname: same\n---\nproject claude")
	writeSkill(t, filepath.Join(project, ".agents", "skills", "same.md"), "---\nname: same\n---\nproject agents")
	writeSkill(t, filepath.Join(project, ".agents", "skills", "bundle", "SKILL.md"), "---\nargument-hint: <target>\nuser-invocable: false\ndisable-model-invocation: true\nargs:\n  - name: target\n    required: true\n---\nrun {{target}}")
	writeSkill(t, filepath.Join(project, ".agents", "skills", "bundle", "references", "ignored.md"), "---\nname: should-not-load\n---\nignored")

	registry, err := LoadDefaultSkills(project)
	if err != nil {
		t.Fatal(err)
	}
	same, ok := registry.Get("same")
	if !ok || !strings.Contains(same.Content, "project agents") || same.Source != "project" {
		t.Fatalf("precedence skill = %#v", same)
	}
	bundle, ok := registry.Get("bundle")
	if !ok || bundle.ArgumentHint != "<target>" || bundle.UserInvocable == nil || *bundle.UserInvocable || !bundle.DisableModelInvocation {
		t.Fatalf("bundle metadata = %#v", bundle)
	}
	if _, ok := registry.Get("should-not-load"); ok {
		t.Fatal("bundle support file was registered as a skill")
	}
	if rendered, err := bundle.Render(map[string]string{"target": "repo"}); err != nil || rendered != "run repo" {
		t.Fatalf("bundle render = %q, %v", rendered, err)
	}
}

func TestSkillGetReturnsIndependentCloneAndProjectScopeUsesBothSources(t *testing.T) {
	registry := NewSkillRegistry()
	registry.Register(&Skill{Name: "runtime", Content: "keep", Source: "runtime"})
	registry.Register(&Skill{Name: "old-project", Content: "discard", Source: "project"})
	value := true
	registry.Register(&Skill{Name: "clone", Content: "original", UserInvocable: &value})

	got, ok := registry.Get("clone")
	if !ok {
		t.Fatal("clone skill missing")
	}
	got.Content = "changed"
	*got.UserInvocable = false
	again, _ := registry.Get("clone")
	if again.Content != "original" || again.UserInvocable == nil || !*again.UserInvocable {
		t.Fatalf("Get exposed live state: %#v", again)
	}

	project := t.TempDir()
	writeSkill(t, filepath.Join(project, ".claude", "skills", "same.md"), "---\nname: same\n---\nclaude")
	writeSkill(t, filepath.Join(project, ".agents", "skills", "same.md"), "---\nname: same\n---\nagents")
	scoped, err := registry.ForProjectDirectory(project)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := scoped.Get("old-project"); ok {
		t.Fatal("old project generation leaked into scoped registry")
	}
	if skill, ok := scoped.Get("runtime"); !ok || skill.Content != "keep" {
		t.Fatalf("non-project skill = %#v", skill)
	}
	if skill, ok := scoped.Get("same"); !ok || !strings.Contains(skill.Content, "agents") {
		t.Fatalf("scoped project precedence = %#v", skill)
	}
}

func TestLoadSkillBundleRootSkipsSupportMarkdown(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, filepath.Join(root, "SKILL.md"), "---\nname: bundle-root\n---\nmain")
	writeSkill(t, filepath.Join(root, "references", "support.md"), "support")
	registry := NewSkillRegistry()
	if err := registry.LoadFromDirectory(root); err != nil {
		t.Fatal(err)
	}
	if got := registry.List(); len(got) != 1 || got[0].Name != "bundle-root" {
		t.Fatalf("bundle root loaded support docs: %#v", got)
	}
}
