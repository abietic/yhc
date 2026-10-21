package prefetch

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/abietic/yhc/engine/skills"
)

func TestSkillPrefetchIncludesSourceContext(t *testing.T) {
	source := filepath.Join(t.TempDir(), "bundle with spaces", "SKILL.md")
	registry := skills.NewSkillRegistry()
	registry.Register(&skills.Skill{Name: "contract-check", FilePath: source, Content: "Read references/contract.txt."})
	messages := NewSkillPrefetch(registry).findMatchingSkills("contract-check")
	if len(messages) != 1 {
		t.Fatalf("prefetched messages = %d", len(messages))
	}
	want := "Skill: contract-check\nSource: " + source + "\nBase directory: " + filepath.Dir(source) + "\n\nRead references/contract.txt."
	if messages[0].Content != want {
		t.Fatalf("prefetched skill has no complete source context: %q", messages[0].Content)
	}
}

func TestSkillPrefetchTruncationPreservesSourceAndUTF8(t *testing.T) {
	source := filepath.Join(t.TempDir(), "校验包", "SKILL.md")
	registry := skills.NewSkillRegistry()
	registry.Register(&skills.Skill{Name: "bounded", FilePath: source, Content: strings.Repeat("内容", 2000)})
	messages := NewSkillPrefetch(registry).findMatchingSkills("bounded")
	if len(messages) != 1 {
		t.Fatalf("prefetched messages = %d", len(messages))
	}
	content := messages[0].Content
	header := "Skill: bounded\nSource: " + source + "\nBase directory: " + filepath.Dir(source) + "\n\n"
	if !strings.HasPrefix(content, header) || !strings.HasSuffix(content, "\n...(truncated)") {
		t.Fatal("bounded prefetch lost its source or truncation marker")
	}
	if !utf8.ValidString(content) || len(content) > 4000+len("\n...(truncated)") {
		t.Fatalf("prefetch is invalid UTF-8 or exceeds its byte budget: %d", len(content))
	}
}

func TestSkillPrefetchSkipsSourceThatCannotFitBudget(t *testing.T) {
	registry := skills.NewSkillRegistry()
	registry.Register(&skills.Skill{Name: "oversized", FilePath: filepath.Join(string(filepath.Separator), strings.Repeat("x", 4000), "SKILL.md"), Content: "Read references/contract.txt."})
	if messages := NewSkillPrefetch(registry).findMatchingSkills("oversized"); len(messages) != 0 {
		t.Fatal("prefetch advertised a source that cannot fit its byte budget")
	}
}
