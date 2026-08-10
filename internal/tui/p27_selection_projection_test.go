package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
)

type p271Item struct{ text string }

func (i p271Item) Render(int, Styles) string { return i.text }
func (i p271Item) Finished() bool            { return true }
func (i p271Item) Version() uint64           { return 1 }
func (i p271Item) NoSelectPrefix() int       { return 0 }
func (i p271Item) renderSelection(ctx HistoryRenderContext) selectionAnnotatedRender {
	annotated, ok := selectionAnnotateVisibleRows(
		ctx.normalized().displayCellProfile(),
		i.text,
		0,
	)
	return selectionAnnotatedRender{rendered: annotated, annotated: ok}
}

func p271StickyChat() *ChatView {
	chat := NewChatView(defaultStyles())
	chat.SetSize(80, 4)
	chat.AppendUser("sticky prompt")
	chat.appendChatItem(p271Item{text: "resource row\nfollowing row\nthird row\nfourth row"})
	chat.offsetIdx = 1
	chat.restoreAway()
	return chat
}

func TestP271StickyFinalFrameSelectionIdentity(t *testing.T) {
	chat := p271StickyChat()
	frame := stripANSIForTest(chat.Render(80, 4))
	if got := strings.Split(frame, "\n"); len(got) != 4 || !strings.Contains(got[0], "sticky prompt") || strings.TrimSpace(got[1]) != "resource row" || strings.TrimSpace(got[2]) != "following row" {
		t.Fatalf("frame = %#v, want sticky/resource/following final frame", got)
	}
	p := chat.currentViewportProjection()
	if p == nil || p.rows[0].kind != chatViewportRowSticky || p.rows[1] != (chatViewportRow{kind: chatViewportRowTranscript, itemIdx: 1, lineInItem: 0}) {
		t.Fatalf("published projection = %#v", p)
	}
	if chat.viewportPosToItemPoint(0, 0) != nil || chat.ItemPointToViewportRow(1, 0) != 1 {
		t.Fatal("sticky chrome must not map to content and final row 1 must round-trip")
	}
	selection := &Selection{}
	selection.startForChat(0, 1, chat)
	selection.updateForChat(len("resource row"), 1, chat)
	selection.finishForChat(len("resource row"), 1, chat)
	if got := selection.ExtractTextFromChat(chat); got != "resource row" {
		t.Fatalf("extracted = %q, want resource row", got)
	}
	app := newTestApp(80, 24)
	app.chat, app.selection = chat, selection
	highlighted := app.applyViewportHighlight(frame)
	if highlighted == frame || strings.TrimSpace(strings.Split(stripANSIForTest(highlighted), "\n")[1]) != "resource row" {
		t.Fatalf("highlight did not paint the exact visible resource row: %q", highlighted)
	}
	plainLines, highlightedLines := strings.Split(frame, "\n"), strings.Split(highlighted, "\n")
	if highlightedLines[0] != plainLines[0] || highlightedLines[1] == plainLines[1] {
		t.Fatalf("highlight crossed final-frame row roles: plain=%q highlighted=%q", plainLines, highlightedLines)
	}
	first := chat.currentViewportProjection()
	chat.Render(80, 4)
	if chat.currentViewportProjection() != first {
		t.Fatal("exact cache hit did not reuse the published projection")
	}
	chat.viewDirty = true
	if chat.currentViewportProjection() != nil {
		t.Fatal("dirty frame reused prior projection")
	}
	chat.Render(80, 4)
	if chat.currentViewportProjection().frameGen == first.frameGen {
		t.Fatal("rebuilt frame did not advance projection generation")
	}
}

func TestP271RowKindsAndClamping(t *testing.T) {
	chat := p271StickyChat()
	chat.Render(80, 4)
	p := chat.currentViewportProjection()
	if p.rows[0].kind != chatViewportRowSticky || p.rows[1].kind != chatViewportRowTranscript {
		t.Fatalf("sticky row matrix = %#v", p.rows)
	}
	if chat.currentViewportProjection().rows[3].kind != chatViewportRowPill {
		t.Fatal("pill must replace the underlying final row role")
	}
	s := &Selection{}
	s.startForChat(0, 1, chat)
	s.updateForChat(0, 3, chat)
	if s.focus == nil || s.focus.itemIdx != 1 || s.focus.lineInItem != 1 {
		t.Fatalf("drag into chrome did not clamp to visible transcript: %#v", s.focus)
	}

	short := NewChatView(defaultStyles())
	short.SetSize(80, 4)
	short.appendChatItem(p271Item{text: "one"})
	short.viewDirty = true
	short.Render(80, 4)
	for _, row := range short.currentViewportProjection().rows[:3] {
		if row.kind != chatViewportRowPadding {
			t.Fatalf("short frame row kind = %v, want padding", row.kind)
		}
	}
}

func TestP271ProjectionInvalidatesOnResizeAndUnicode(t *testing.T) {
	chat := NewChatView(defaultStyles())
	chat.SetSize(80, 4)
	styled := "\x1b[31m中🙂e\u0301\x1b[0m"
	chat.appendChatItem(p271Item{text: styled})
	chat.Render(80, 4)
	before := chat.currentViewportProjection()
	if point := chat.nearestSelectableViewportPoint(999, 3); point == nil || point.col != selectionLineCells(chat.environment.profile, styled) {
		t.Fatalf("cell clamp split Unicode row: %#v", point)
	}
	if got := stripANSIForTest(selectionSliceCells(chat.environment.profile, styled, 0, 4)); got != "中🙂" {
		t.Fatalf("profile cell slice split ANSI/CJK/emoji/combining content: %q", got)
	}
	chat.SetSize(40, 4)
	if chat.currentViewportProjection() != nil {
		t.Fatal("resize did not invalidate projection")
	}
	chat.Render(40, 4)
	if after := chat.currentViewportProjection(); after.width != 40 || after.contentGen == before.contentGen {
		t.Fatalf("resize projection = %#v, before = %#v", after, before)
	}
}

func TestP271FrameAndContentGenerationMatrix(t *testing.T) {
	empty := NewChatView(defaultStyles())
	empty.SetSize(80, 4)
	empty.Render(80, 4)
	first := empty.currentViewportProjection()
	empty.Render(80, 4)
	if empty.currentViewportProjection() != first {
		t.Fatal("exact empty render did not reuse its projection")
	}

	chat := NewChatView(defaultStyles())
	chat.SetSize(80, 4)
	for i := 0; i < 8; i++ {
		chat.appendChatItem(p271Item{text: "line"})
	}
	chat.invalidateContent()
	chat.Render(80, 4)
	before := chat.currentViewportProjection()
	chat.ScrollUp(1)
	if chat.currentViewportProjection() != nil {
		t.Fatal("scroll reused the prior frame projection before the next render")
	}
	chat.Render(80, 4)
	afterScroll := chat.currentViewportProjection()
	if afterScroll.frameGen <= before.frameGen || afterScroll.contentGen != before.contentGen {
		t.Fatalf("scroll generations = frame %d/%d content %d/%d", afterScroll.frameGen, before.frameGen, afterScroll.contentGen, before.contentGen)
	}
	chat.SetSize(80, 5)
	chat.Render(80, 5)
	afterHeight := chat.currentViewportProjection()
	if afterHeight.contentGen != afterScroll.contentGen {
		t.Fatal("height-only reframe advanced content generation")
	}
}

func TestP271PartialFirstItemAndOffscreenInverseClipping(t *testing.T) {
	partial := NewChatView(defaultStyles())
	partial.SetSize(80, 2)
	partial.appendChatItem(p271Item{text: "line 0\nline 1\nline 2\nline 3"})
	partial.Render(80, 2)
	projection := partial.currentViewportProjection()
	if got := projection.rows[0]; got != (chatViewportRow{kind: chatViewportRowTranscript, itemIdx: 0, lineInItem: 2}) {
		t.Fatalf("partial first row = %#v, want item 0 line 2", got)
	}
	if partial.ItemPointToViewportRow(0, 2) != 0 || partial.viewportPosToItemPoint(0, 0).lineInItem != 2 {
		t.Fatal("partial first item did not round-trip through the published frame")
	}

	chat := NewChatView(defaultStyles())
	chat.SetSize(80, 4)
	for i := 0; i < 8; i++ {
		chat.appendChatItem(p271Item{text: "line"})
	}
	chat.ScrollToTop()
	chat.Render(80, 4)
	if got := chat.ItemPointToViewportRow(7, 0); got != 4 {
		t.Fatalf("below-viewport point = %d, want exact height sentinel", got)
	}
	chat.ScrollDown(3)
	chat.Render(80, 4)
	if got := chat.ItemPointToViewportRow(0, 0); got != -1 {
		t.Fatalf("above-viewport point = %d, want -1", got)
	}
	if got := chat.ItemPointToViewportRow(7, 0); got != 4 {
		t.Fatalf("below-viewport point after scroll = %d, want exact height sentinel", got)
	}
	selection := &Selection{
		anchor: &selItemPoint{itemIdx: 0, lineInItem: 0, col: 0},
		focus:  &selItemPoint{itemIdx: 7, lineInItem: 0, col: 4},
	}
	startRow, _, endRow, _, ok := selection.GetViewportHighlightRange(chat)
	if !ok || startRow != 0 || endRow != 3 {
		t.Fatalf("offscreen highlight clip = %d..%d/%v, want 0..3", startRow, endRow, ok)
	}
}

func TestP271ProjectionChromeAndReleaseBoundaries(t *testing.T) {
	chat := NewChatView(defaultStyles())
	chat.SetSize(80, 5)
	chat.appendChatItem(p271Item{text: "first"})
	chat.appendChatItem(p271Item{text: "second"})
	for i := 0; i < 4; i++ {
		chat.appendChatItem(p271Item{text: "overflow"})
	}
	chat.invalidateContent()
	chat.Render(80, 5)
	p := chat.currentViewportProjection()
	gap := -1
	for row, descriptor := range p.rows {
		if descriptor.kind == chatViewportRowItemGap {
			gap = row
		}
	}
	if gap < 0 || chat.viewportPosToItemPoint(0, gap) != nil {
		t.Fatalf("item gap must be nonselectable: %#v", p.rows)
	}
	if chat.nearestSelectableViewportPoint(0, -99) == nil || chat.nearestSelectableViewportPoint(0, 999) == nil {
		t.Fatal("active drag outside the viewport did not clamp")
	}
	s := &Selection{anchor: &selItemPoint{itemIdx: 0, lineInItem: 0, col: 0}, focus: &selItemPoint{itemIdx: 0, lineInItem: 0, col: 4}}
	if !s.HandleMouseForChat(tuiMouseMsg{Button: tea.MouseLeft, Action: mouseActionRelease}, 0, 0, chat) {
		t.Fatal("completed multi-click selection release was not consumed")
	}

	chat.ScrollToTop()
	chat.Render(80, 5)
	p = chat.currentViewportProjection()
	if p.rows[len(p.rows)-1].kind != chatViewportRowPill || chat.viewportPosToItemPoint(0, len(p.rows)-1) != nil {
		t.Fatalf("pill must own the final row: %#v", p.rows)
	}
}

func TestP271AgentLinkSelectionSuppressesActionAndClickStillOpens(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "test")
	app := newTestApp(72, 20)
	app.chat.AppendOrUpdateTool("spawn-agent", "Agent", `{"description":"inspect runtime"}`)
	app.chat.UpdateToolResult("spawn-agent", "Agent", "background Agent started")
	app.chat.UpdateAgentToolTrace("spawn-agent", agentToolTrace{
		AgentID: "agent-1", ExecutionKey: engine.RuntimeExecutionKey{
			AgentID: "agent-1", Generation: 1,
		}, IdentityResolved: true,
		Status: "running", Summary: "Inspecting runtime state",
		StartedAt: time.Now().Add(-time.Second), UpdatedAt: time.Now(),
	})
	app.backgroundTasks.SetExplorerSnapshotProvider(func() engine.TaskExplorerSnapshot {
		return engine.TaskExplorerSnapshot{Available: true, BoardID: "p27", Revision: engine.TaskExplorerRevision{Board: 1, Runtime: 1}, Executions: []engine.TaskExplorerExecution{{Key: engine.RuntimeExecutionKey{AgentID: "agent-1", Generation: 1}, Description: "inspect runtime", Status: "running", Phase: engine.TaskExplorerExecutionRunning, AllowedActions: []engine.TaskExplorerAction{engine.TaskExplorerActionInspect}}}}
	})
	app.backgroundTasks.SetDetailProvider(func(agentID string) (engine.AgentDetailSnapshot, bool) {
		return engine.AgentDetailSnapshot{
			Revision: 1,
			Agent: engine.RuntimeAgentSnapshot{
				AgentID: "agent-1", Generation: 1,
				Description: "inspect runtime", Status: "running",
			},
			Thread: engine.RuntimeThreadSnapshot{Status: engine.RuntimeThreadRunning},
		}, agentID == "agent-1"
	})
	_ = app.View()
	projection := app.chat.currentViewportProjection()
	linkRow := -1
	for row := range projection.rows {
		if key, ok := app.chat.AgentTraceTargetAtViewportRow(row); ok &&
			key == (engine.RuntimeExecutionKey{AgentID: "agent-1", Generation: 1}) {
			linkRow = row
			break
		}
	}
	if linkRow < 0 {
		t.Fatalf("published Agent link row not found: %#v", projection.rows)
	}

	descriptor := projection.rows[linkRow]
	app.selection.anchor = &selItemPoint{itemIdx: descriptor.itemIdx, lineInItem: descriptor.lineInItem, col: 0}
	app.selection.focus = &selItemPoint{itemIdx: descriptor.itemIdx, lineInItem: descriptor.lineInItem, col: 4}
	_, _ = app.Update(tuiMouseMsg{
		X: 4, Y: app.layout.chatRect.Y + linkRow,
		Button: tea.MouseLeft, Action: mouseActionRelease,
	})
	if app.state != StateChat || app.backgroundTasks.Visible() {
		t.Fatalf("non-empty selection triggered Agent action: state=%v visible=%v", app.state, app.backgroundTasks.Visible())
	}

	app.selection.Clear()
	click := tuiMouseMsg{
		X: 4, Y: app.layout.chatRect.Y + linkRow,
		Button: tea.MouseLeft, Action: mouseActionPress,
	}
	_, _ = app.Update(click)
	click.Action = mouseActionRelease
	_, _ = app.Update(click)
	if app.state != StateBackgroundTasks || app.backgroundTasks.detailAgent != "agent-1" {
		t.Fatalf("plain Agent link click did not open detail: state=%v panel=%#v", app.state, app.backgroundTasks)
	}
}

func TestP271PillPressOwnsOverlayBeforeSelection(t *testing.T) {
	app := newTestApp(80, 24)
	for i := 0; i < 20; i++ {
		app.chat.AppendSystem("overflow row")
	}
	_ = app.View()
	app.chat.ScrollUp(3)
	_ = app.View()
	projection := app.chat.currentViewportProjection()
	if projection == nil || !projection.pill.visible || projection.rows[projection.pill.row].kind != chatViewportRowPill {
		t.Fatalf("published pill projection = %#v", projection)
	}
	_, _ = app.Update(tuiMouseMsg{
		X:      projection.pill.start,
		Y:      app.layout.chatRect.Y + projection.pill.row,
		Button: tea.MouseLeft,
		Action: mouseActionPress,
	})
	if !app.chat.Following() || app.selection.anchor != nil || app.selection.IsDragging() {
		t.Fatalf("pill press leaked into selection: following=%v selection=%#v", app.chat.Following(), app.selection)
	}
}
