package prefetch

import (
	"encoding/json"
	"maps"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/compact"
	"github.com/abietic/yhc/engine/skills"
	"github.com/cloudwego/eino/schema"
)

func TestSkillPrefetchCollectMissingUsesExactVisibleAttachment(t *testing.T) {
	registry := skills.NewSkillRegistry()
	registry.Register(&skills.Skill{Name: "trace-lens", Content: "independent skill body"})
	query := []*schema.Message{{Role: schema.User, Content: "Use trace-lens."}}
	initial := NewSkillPrefetch(registry)
	initial.Start(query)
	attachment := initial.Collect()[0]
	copyWith := func(change func(*schema.Message)) *schema.Message {
		copied := *attachment
		copied.Extra = maps.Clone(attachment.Extra)
		change(&copied)
		return &copied
	}
	cases := []struct {
		name    string
		history []*schema.Message
		want    int
	}{
		{"already visible", []*schema.Message{nil, attachment}, 0},
		{"absent", query, 1},
		{"different content", []*schema.Message{copyWith(func(m *schema.Message) { m.Content += " changed" })}, 1},
		{"different skill", []*schema.Message{copyWith(func(m *schema.Message) { m.Extra["skill_name"] = "other" })}, 1},
		{"ordinary user quotation", []*schema.Message{{Role: schema.User, Content: attachment.Content}}, 1},
		{"non-meta user", []*schema.Message{copyWith(func(m *schema.Message) { m.Extra["is_meta"] = false })}, 1},
		{"other attachment kind", []*schema.Message{copyWith(func(m *schema.Message) { m.Extra["attachment_kind"] = "file" })}, 1},
		{"assistant quotation", []*schema.Message{copyWith(func(m *schema.Message) { m.Role = schema.Assistant })}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before, err := json.Marshal(tc.history)
			if err != nil {
				t.Fatal(err)
			}
			p := NewSkillPrefetch(registry)
			p.Start(query)
			if got := p.CollectMissing(tc.history); len(got) != tc.want {
				t.Fatalf("missing attachments=%d, want %d", len(got), tc.want)
			}
			after, err := json.Marshal(tc.history)
			if err != nil || string(before) != string(after) {
				t.Fatalf("history mutated: err=%v", err)
			}
			if got := p.Collect(); len(got) != 1 || got[0].Content != attachment.Content {
				t.Fatal("filtering mutated the completed discovery result")
			}
		})
	}
}

func TestSkillPrefetchCollectMissingCanRestoreAfterCompaction(t *testing.T) {
	registry := skills.NewSkillRegistry()
	registry.Register(&skills.Skill{Name: "trace-lens", Content: "trace-lens guidance"})
	query := []*schema.Message{{Role: schema.User, Content: "Use trace-lens."}}
	first := NewSkillPrefetch(registry)
	first.Start(query)
	history := append(query, first.Collect()...)
	boundary := &schema.Message{Role: schema.System, Content: "retained summary", Extra: map[string]any{"subtype": "compact_boundary"}}
	history = append(history, boundary)
	p := NewSkillPrefetch(registry)
	p.Start(history)
	active := compact.GetMessagesAfterCompactBoundary(history)
	if len(active) != 1 || active[0] != boundary {
		t.Fatal("fixture did not remove the earlier attachment from active context")
	}
	if got := p.CollectMissing(active); len(got) != 1 || got[0].Content != "trace-lens guidance" {
		t.Fatal("compacted-away guidance must remain eligible for delivery")
	}
	if got := p.CollectMissing(first.Collect()); len(got) != 0 {
		t.Fatal("collection must depend on current visibility, not sticky delivery state")
	}
}

func TestSkillPrefetchCollectMissingDeliversChangedGeneration(t *testing.T) {
	oldPath := filepath.Join(t.TempDir(), "old", "SKILL.md")
	newPath := filepath.Join(t.TempDir(), "new", "SKILL.md")
	for _, tc := range []struct {
		name, source, body string
	}{
		{"body", oldPath, "updated independent guidance"},
		{"source", newPath, "original independent guidance"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := skills.NewSkillRegistry()
			registry.Register(&skills.Skill{Name: "trace-lens", FilePath: oldPath, Content: "original independent guidance"})
			query := []*schema.Message{{Role: schema.User, Content: "Use trace-lens."}}
			first := NewSkillPrefetch(registry)
			first.Start(query)
			history := first.Collect()
			before := *history[0]
			registry.Register(&skills.Skill{Name: "trace-lens", FilePath: tc.source, Content: tc.body})
			next := NewSkillPrefetch(registry)
			next.Start(query)
			got := next.CollectMissing(history)
			if len(got) != 1 || !strings.Contains(got[0].Content, tc.source) || !strings.HasSuffix(got[0].Content, tc.body) {
				t.Fatal("changed skill source or body was suppressed")
			}
			if !reflect.DeepEqual(*history[0], before) {
				t.Fatal("generation refresh rewrote earlier history")
			}
		})
	}
}
