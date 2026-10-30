package prefetch

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/compact"
	"github.com/cloudwego/eino/schema"
)

func TestMemoryPrefetchCollectMissingUsesExactVisibleAttachment(t *testing.T) {
	store := compact.NewMemoryStore(t.TempDir(), 10)
	if err := store.Add(compact.MemoryEntry{Category: "fact", Content: "independent retained memory", CreatedAt: time.Unix(1, 0)}); err != nil {
		t.Fatal(err)
	}
	p := NewMemoryPrefetch(store, 100)
	query := []*schema.Message{{Role: schema.User, Content: "Inspect the fixture."}}
	p.Start(query)
	attachment := p.Collect()[0]
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
		{"existing duplicates", []*schema.Message{attachment, attachment}, 0},
		{"absent", query, 1},
		{"different content", []*schema.Message{copyWith(func(m *schema.Message) { m.Content += " changed" })}, 1},
		{"ordinary system text", []*schema.Message{{Role: schema.System, Content: attachment.Content}}, 1},
		{"user quotation", []*schema.Message{copyWith(func(m *schema.Message) { m.Role = schema.User })}, 1},
		{"assistant quotation", []*schema.Message{copyWith(func(m *schema.Message) { m.Role = schema.Assistant })}, 1},
		{"non-meta system", []*schema.Message{copyWith(func(m *schema.Message) { m.Extra["is_meta"] = false })}, 1},
		{"other attachment kind", []*schema.Message{copyWith(func(m *schema.Message) { m.Extra["attachment_kind"] = "file" })}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before, err := json.Marshal(tc.history)
			if err != nil {
				t.Fatal(err)
			}
			if got := p.CollectMissing(tc.history); len(got) != tc.want {
				t.Fatalf("missing attachments=%d, want %d", len(got), tc.want)
			}
			after, err := json.Marshal(tc.history)
			if err != nil || string(before) != string(after) {
				t.Fatalf("history mutated: err=%v", err)
			}
			if got := p.Collect(); len(got) != 1 || got[0] != attachment {
				t.Fatal("filtering consumed or replaced the completed prefetch result")
			}
			if got := p.CollectMissing(tc.history); len(got) != tc.want {
				t.Fatal("filtering must depend on visible history, not earlier collection")
			}
		})
	}
	if got := (&MemoryPrefetch{}).CollectMissing(nil); got != nil {
		t.Fatal("zero-value collection must remain empty")
	}
}

func TestMemoryPrefetchCollectMissingRestoresAfterCompaction(t *testing.T) {
	store := compact.NewMemoryStore(t.TempDir(), 10)
	if err := store.Add(compact.MemoryEntry{Category: "fact", Content: "preserved fixture memory"}); err != nil {
		t.Fatal(err)
	}
	p := NewMemoryPrefetch(store, 100)
	query := []*schema.Message{{Role: schema.User, Content: "Inspect the fixture."}}
	p.Start(query)
	attachment := p.Collect()[0]
	history := append(query, attachment)
	if len(p.CollectMissing(history)) != 0 {
		t.Fatal("visible attachment was redelivered")
	}
	boundary := &schema.Message{Role: schema.System, Content: "retained summary", Extra: map[string]any{"subtype": "compact_boundary"}}
	history = append(history, boundary)
	active := compact.GetMessagesAfterCompactBoundary(history)
	if len(active) != 1 || active[0] != boundary {
		t.Fatal("fixture did not remove the memory attachment from active context")
	}
	if got := p.CollectMissing(active); len(got) != 1 || got[0] != attachment {
		t.Fatal("compacted-away memory must remain eligible in the same query")
	}
	if len(p.CollectMissing([]*schema.Message{attachment})) != 0 {
		t.Fatal("redelivery must not erase the ability to recognize visible memory")
	}
}

func TestMemoryPrefetchCollectMissingDeliversChangedSnapshot(t *testing.T) {
	store := compact.NewMemoryStore(t.TempDir(), 10)
	if err := store.Add(compact.MemoryEntry{Category: "fact", Content: "earlier fact", CreatedAt: time.Unix(1, 0)}); err != nil {
		t.Fatal(err)
	}
	first := NewMemoryPrefetch(store, 100)
	query := []*schema.Message{{Role: schema.User, Content: "Inspect the fixture."}}
	first.Start(query)
	history := first.Collect()
	oldContent := history[0].Content
	if err := store.Add(compact.MemoryEntry{Category: "decision", Content: "later decision", CreatedAt: time.Unix(2, 0)}); err != nil {
		t.Fatal(err)
	}
	next := NewMemoryPrefetch(store, 100)
	next.Start(query)
	got := next.CollectMissing(history)
	if len(got) != 1 || !strings.Contains(got[0].Content, "later decision") || !strings.Contains(got[0].Content, "earlier fact") {
		t.Fatal("a changed snapshot must remain eligible even when some entries are already visible")
	}
	if history[0].Content != oldContent {
		t.Fatal("changed snapshot rewrote earlier history")
	}
}
