package skills

import "testing"

func TestSkillArgumentCompletionMetadataAndIsolation(t *testing.T) {
	skill, err := ParseSkillData("/project/inspect/SKILL.md", []byte("---\nname: inspect\nargs:\n  - name: mode\n    choices: [quick, thorough]\n    default: quick\n  - name: target\n    completion: directories\n    suggest: true\n---\nInspect {{target}} using {{mode}}"))
	if err != nil {
		t.Fatal(err)
	}
	registry := NewSkillRegistry()
	registry.Register(skill)
	skill.Args[0].Choices[0] = "changed"
	snapshot := registry.Snapshot()
	if snapshot.Skills[0].Args[0].Choices[0] != "quick" {
		t.Fatal("registration did not detach choices")
	}
	snapshot.Skills[0].Args[0].Choices[0] = "changed again"
	if registry.Snapshot().Skills[0].Args[0].Choices[0] != "quick" {
		t.Fatal("snapshot did not detach choices")
	}
	if _, err := ParseSkillData("/project/unsafe/SKILL.md", []byte("---\nargs:\n  - name: target\n    completion: shell-script\n---\nbody")); err == nil {
		t.Fatal("unknown source accepted")
	}
}
