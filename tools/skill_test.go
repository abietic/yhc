package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
