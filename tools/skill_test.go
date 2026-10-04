package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/skills"
)

func TestInitSkillsMakesProjectSkillsDiscoverableAndInvokable(t *testing.T) {
	previous := DefaultSkillRegistry
	t.Cleanup(func() { DefaultSkillRegistry = previous })

	dir := t.TempDir()
	skillsDir := filepath.Join(dir, ".claude", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}
	content := "---\nname: greet-project\ndescription: Greet a project member\nargs:\n  - name: person\n    required: true\n---\nHello {{person}}.\n"
	if err := os.WriteFile(filepath.Join(skillsDir, "greet.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	if err := InitSkills(dir); err != nil {
		t.Fatal(err)
		return
	}
	tool := SkillTool()
	if !strings.Contains(tool.Info.Desc, "greet-project") {
		t.Fatalf("Skill tool description does not advertise loaded skill: %q", tool.Info.Desc)
	}
	result, err := tool.Execute(`{"skill":"greet-project","arguments":{"person":"Theo"}}`)
	if err != nil {
		t.Fatal(err)
		return
	}
	if strings.TrimSpace(result) != "Hello Theo." {
		t.Fatalf("unexpected expanded skill: %q", result)
	}
}

func TestSkillModelInvocationFlags(t *testing.T) {
	registry := skills.NewSkillRegistry()
	registry.Register(&skills.Skill{Name: "manual-only", Content: "manual body", DisableModelInvocation: true})
	hidden := false
	registry.Register(&skills.Skill{Name: "automatic", Content: "automatic body", UserInvocable: &hidden})
	description := skillToolDescription(registry)
	if strings.Contains(description, "manual-only") || !strings.Contains(description, "automatic") {
		t.Fatalf("model discovery = %q", description)
	}
	if _, err := executeSkillWithRegistry(`{"skill":"manual-only"}`, registry); err == nil {
		t.Fatal("model invoked disabled skill")
	}
	if output, err := executeSkillWithRegistry(`{"skill":"automatic"}`, registry); err != nil || output != "automatic body" {
		t.Fatalf("automatic skill = %q %v", output, err)
	}
}
