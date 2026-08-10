package tui

import (
	"strings"
	"testing"
)

type semanticHistoryFixture struct {
	id         string
	version    uint64
	finished   bool
	rich       string
	raw        string
	compact    string
	expanded   string
	transcript string
	renders    int
	selected   bool
	frame      uint64
	children   []HistoryItem
}

func (f *semanticHistoryFixture) ID() string      { return f.id }
func (f *semanticHistoryFixture) Version() uint64 { return f.version }
func (f *semanticHistoryFixture) Finished() bool  { return f.finished }

func (f *semanticHistoryFixture) Render(HistoryRenderContext) string {
	f.renders++
	return f.rich
}

func (f *semanticHistoryFixture) Raw(HistoryRenderContext) string { return f.raw }

func (f *semanticHistoryFixture) Height(ctx HistoryRenderContext) int {
	return historyRenderedHeight(renderHistoryItem(f, ctx))
}

func (f *semanticHistoryFixture) RenderCompact(HistoryRenderContext) string {
	return f.compact
}

func (f *semanticHistoryFixture) RenderExpanded(HistoryRenderContext) string {
	return f.expanded
}

func (f *semanticHistoryFixture) Expanded() bool { return f.selected }

func (f *semanticHistoryFixture) ToggleExpanded() bool {
	f.selected = !f.selected
	f.version++
	return f.selected
}

func (f *semanticHistoryFixture) ExpandedContent() (string, string) {
	return "fixture", f.expanded
}

func (f *semanticHistoryFixture) RenderTranscript(HistoryRenderContext) string {
	return f.transcript
}

func (f *semanticHistoryFixture) NestedHistoryItems() []HistoryItem {
	return append([]HistoryItem(nil), f.children...)
}

func (f *semanticHistoryFixture) Selectable() bool    { return true }
func (f *semanticHistoryFixture) NoSelectPrefix() int { return 2 }

func (f *semanticHistoryFixture) PrepareHistoryAnimation(frame uint64) {
	f.frame = frame
}

func (f *semanticHistoryFixture) HistoryAnimationVersion(frame uint64) uint64 {
	if f.finished {
		return f.version
	}
	return f.version + frame
}

func TestHistoryItemNativeContractAndProjections(t *testing.T) {
	child := &semanticHistoryFixture{id: "child", finished: true, rich: "child", raw: "child"}
	item := &semanticHistoryFixture{
		id:         "semantic-1",
		version:    3,
		finished:   true,
		rich:       "rich\nrow",
		raw:        "raw",
		compact:    "compact",
		expanded:   "expanded\nbody",
		transcript: "transcript",
		children:   []HistoryItem{child},
	}
	ctx := HistoryRenderContext{Width: 80, Styles: defaultStyles()}

	if got := renderHistoryItem(item, ctx); got != item.rich {
		t.Fatalf("rich render = %q", got)
	}
	ctx.Mode = HistoryRenderRaw
	if got := renderHistoryItem(item, ctx); got != item.raw {
		t.Fatalf("raw render = %q", got)
	}
	ctx.Mode = HistoryRenderCompact
	if got := renderHistoryItem(item, ctx); got != item.compact {
		t.Fatalf("compact render = %q", got)
	}
	ctx.Mode = HistoryRenderExpanded
	if got := renderHistoryItem(item, ctx); got != item.expanded {
		t.Fatalf("expanded render = %q", got)
	}
	if got := item.Height(ctx); got != 2 {
		t.Fatalf("expanded height = %d", got)
	}
	ctx.Mode = HistoryRenderTranscript
	if got := renderHistoryItem(item, ctx); got != item.transcript {
		t.Fatalf("transcript render = %q", got)
	}
	if got := item.NestedHistoryItems(); len(got) != 1 || got[0].ID() != "child" {
		t.Fatalf("nested items = %#v", got)
	}
}

func TestChatViewUsesStableHistoryIdentityCache(t *testing.T) {
	chat := NewChatView(defaultStyles())
	chat.SetSize(80, 8)
	item := &semanticHistoryFixture{
		id:       "stable-id",
		version:  1,
		finished: true,
		rich:     "cached row",
		raw:      "cached row",
	}
	chat.AppendHistoryItem(item)
	chat.Render(80, 8)
	if item.renders != 1 {
		t.Fatalf("initial renders = %d", item.renders)
	}

	chat.Render(80, 8)
	if item.renders != 1 {
		t.Fatalf("exact cache hit renders = %d", item.renders)
	}

	// Frozen output binds exact width because wrapping is geometry-sensitive.
	chat.SetSize(81, 8)
	chat.Render(81, 8)
	if item.renders != 2 {
		t.Fatalf("width invalidation renders = %d", item.renders)
	}
	if key := chat.historyCacheKeys[chat.items[0]]; key != item.ID() {
		t.Fatalf("cache key = %q", key)
	}
	if semantic := chat.HistoryItems(); len(semantic) != 1 || semantic[0] != item {
		t.Fatalf("history items = %#v", semantic)
	}

	item.version++
	chat.SetSize(81, 8)
	chat.Render(81, 8)
	if item.renders != 3 {
		t.Fatalf("version invalidation renders = %d", item.renders)
	}
}

func TestChatViewIsolatesDuplicateSemanticIDs(t *testing.T) {
	chat := NewChatView(defaultStyles())
	first := &semanticHistoryFixture{id: "duplicate", finished: true, rich: "first", raw: "first", expanded: "first"}
	second := &semanticHistoryFixture{id: "duplicate", finished: true, rich: "second", raw: "second", expanded: "second"}
	chat.AppendHistoryItem(first)
	chat.AppendHistoryItem(second)

	firstKey := chat.historyCacheKeys[chat.items[0]]
	secondKey := chat.historyCacheKeys[chat.items[1]]
	if firstKey == secondKey {
		t.Fatalf("duplicate cache keys = %q", firstKey)
	}
	if got := chat.RenderAllExpanded(80); !strings.Contains(got, "first") || !strings.Contains(got, "second") {
		t.Fatalf("expanded duplicate render = %q", got)
	}
}

func TestLegacyChatItemsAdaptToHistoryContract(t *testing.T) {
	styles := defaultStyles()
	items := []ChatItem{
		&UserMessage{content: "user"},
		&ThinkingMessage{content: "thinking", finished: true, version: 1},
		&AssistantMessage{content: "assistant", finished: true, version: 1},
		&SystemMessage{content: "system"},
		&CompactBoundaryMessage{stats: "stats"},
		&InterruptionMessage{},
		&CompactSummaryMessage{messageCount: 2},
		&HelpMessage{entries: []HelpEntry{{Name: "help", Desc: "show help"}}},
		&ToolGroupMessage{},
		&ToolMessage{name: "Read", status: ToolSuccess, version: 1},
		NewErrorMessage(ErrorEntry{Title: "error", Message: "details"}, styles),
		NewStreamingMessage(styles),
	}

	for i, legacy := range items {
		item := adaptChatItem("legacy", legacy)
		ctx := HistoryRenderContext{Width: 72, Styles: styles, Mode: HistoryRenderRich}
		rendered := renderHistoryItem(item, ctx)
		if got, want := item.Height(ctx), historyRenderedHeight(rendered); got != want {
			t.Fatalf("item %d (%T) height = %d, want %d", i, legacy, got, want)
		}
		if raw := item.Raw(ctx); strings.Contains(raw, "\x1b[") {
			t.Fatalf("item %d (%T) raw contains ANSI: %q", i, legacy, raw)
		}
	}
}

func TestExistingToolHistoryCapabilities(t *testing.T) {
	tool := &ToolMessage{
		toolCallID: "tool-1",
		name:       "Bash",
		input:      `{"command":"printf test"}`,
		output:     strings.Repeat("output line\n", 12),
		status:     ToolSuccess,
		version:    1,
	}
	chat := NewChatView(defaultStyles())
	chat.appendChatItem(tool)

	before := tool.Version()
	chat.ToggleExpand()
	if !tool.Expanded() || tool.Version() != before+1 {
		t.Fatalf("expanded=%v version=%d", tool.Expanded(), tool.Version())
	}
	if title, content, ok := chat.GetLastExpandableContent(); !ok || title != "Tool: Bash" || content != tool.output {
		t.Fatalf("expandable content = %q %q %v", title, content, ok)
	}

	expanded := chat.RenderAllExpanded(60)
	if !strings.Contains(expanded, "output line") || !tool.Expanded() {
		t.Fatalf("expanded render lost output/state: %q", expanded)
	}
	transcript := chat.RenderAllTranscript(60)
	if strings.Contains(transcript, "\x1b[") || !strings.Contains(transcript, "output line") {
		t.Fatalf("transcript = %q", transcript)
	}
	if tool.NoSelectPrefix() != 5 || !tool.Selectable() {
		t.Fatalf("selection capability = %d %v", tool.NoSelectPrefix(), tool.Selectable())
	}

	tool.status = ToolRunning
	tool.PrepareHistoryAnimation(7)
	if tool.spinnerCount != 7 || tool.HistoryAnimationVersion(7) != tool.Version()+7 {
		t.Fatalf("animation frame=%d version=%d", tool.spinnerCount, tool.HistoryAnimationVersion(7))
	}
}

func TestToolGroupExposesNestedSemanticItems(t *testing.T) {
	group := &ToolGroupMessage{
		tools: []*ToolMessage{
			{toolCallID: "read-1", name: "Read", output: "one", status: ToolSuccess, version: 1},
			{toolCallID: "grep-1", name: "Grep", output: "two", status: ToolSuccess, version: 1},
		},
		version: 1,
	}
	children := group.NestedHistoryItems()
	if len(children) != 2 || children[0].ID() == children[1].ID() {
		t.Fatalf("nested IDs = %#v", children)
	}
	ctx := HistoryRenderContext{Width: 60, Styles: defaultStyles(), Mode: HistoryRenderExpanded}
	if rendered := group.RenderExpanded(ctx); !strings.Contains(rendered, "one") || !strings.Contains(rendered, "two") {
		t.Fatalf("expanded group = %q", rendered)
	}
	adapted := adaptChatItem("group", group)
	nested, ok := adapted.(HistoryNestedItem)
	if !ok || len(nested.NestedHistoryItems()) != 2 {
		t.Fatalf("adapted nested capability = %T %#v", adapted, nested)
	}
}
