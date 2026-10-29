package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/skills"
)

func TestSkillToolIncludesBundleSourceForRelativeFiles(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "独立技能 with spaces")
	if err := os.MkdirAll(filepath.Join(bundle, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(bundle, "SKILL.md")
	if err := os.WriteFile(source, []byte("---\nname: contract-check\nargs:\n  - name: target\n    required: true\n---\nRead references/contract.txt before checking {{target}}.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "references", "contract.txt"), []byte("fixture contract"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := skills.NewSkillRegistry()
	if err := registry.LoadFromDirectory(bundle); err != nil {
		t.Fatal(err)
	}
	ctx := WithSkillRegistry(context.Background(), registry)
	output, err := SkillToolForRegistry(registry).ExecuteCtx(ctx, `{"skill":"contract-check","arguments":{"target":"example"}}`)
	if err != nil {
		t.Fatal(err)
	}
	want := "Skill: contract-check\nSource: " + source + "\nBase directory: " + bundle + "\n\nRead references/contract.txt before checking example."
	if strings.TrimSpace(output) != want {
		t.Fatalf("skill invocation has no complete source context: %q", output)
	}
	base := ""
	for _, line := range strings.Split(output, "\n") {
		if directory, ok := strings.CutPrefix(line, "Base directory: "); ok {
			base = directory
		}
	}
	data, err := os.ReadFile(filepath.Join(base, "references", "contract.txt"))
	if err != nil || string(data) != "fixture contract" {
		t.Fatalf("relative support file cannot be resolved from invocation: %q, %v", data, err)
	}
}
