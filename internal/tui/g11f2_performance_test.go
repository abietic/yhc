package tui

import (
	"fmt"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
)

const (
	g11F2FrozenHistoryItems     = 10_000
	g11F2LiveSidebarRows        = 100
	g11F2FrozenSteadyP95Budget  = 20 * time.Millisecond
	g11F2SidebarSteadyP95Budget = 50 * time.Millisecond
)

type g11F2FrozenHistoryItem struct {
	id      string
	content string
	renders int
}

func (i *g11F2FrozenHistoryItem) ID() string      { return i.id }
func (i *g11F2FrozenHistoryItem) Version() uint64 { return 1 }
func (i *g11F2FrozenHistoryItem) Finished() bool  { return true }

func (i *g11F2FrozenHistoryItem) Render(HistoryRenderContext) string {
	i.renders++
	return i.content
}

func (i *g11F2FrozenHistoryItem) Raw(HistoryRenderContext) string {
	return i.content
}

func (i *g11F2FrozenHistoryItem) Height(HistoryRenderContext) int {
	return 1
}

func TestG11F2SteadyFrameKeepsFrozenHistorySegmentedAndViewportBounded(t *testing.T) {
	chat, items := g11F2LongFrozenHistory()
	chat.Render(120, 40)

	warmRenders := g11F2HistoryRenderCount(items)
	if warmRenders == 0 || warmRenders >= g11F2FrozenHistoryItems {
		t.Fatalf(
			"warm viewport rendered %d of %d frozen items; want a nonempty viewport-bounded subset",
			warmRenders,
			g11F2FrozenHistoryItems,
		)
	}
	if cacheEntries := len(chat.renderCache); cacheEntries > 64 {
		t.Fatalf(
			"warm viewport cached %d of %d frozen items; want O(viewport) work",
			cacheEntries,
			g11F2FrozenHistoryItems,
		)
	}

	chat.viewDirty = true
	chat.Render(120, 40)
	if steadyRenders := g11F2HistoryRenderCount(items); steadyRenders != warmRenders {
		t.Fatalf(
			"steady frame re-segmented frozen history: warm=%d steady=%d",
			warmRenders,
			steadyRenders,
		)
	}
}

func TestG11F2SteadyFramePerformanceBudgets(t *testing.T) {
	chat, _ := g11F2LongFrozenHistory()
	chat.Render(120, 40)
	assertPerformanceP95(
		t,
		"G11.F2 10K frozen-history steady dirty frame",
		g11F2FrozenSteadyP95Budget,
		80,
		func() {
			chat.viewDirty = true
			chat.Render(120, 40)
		},
	)

	app := g11F2LiveSidebarApp(t, g11F2LiveSidebarRows)
	assertPerformanceP95(
		t,
		"G11.F2 100 live-sidebar-row steady frame",
		g11F2SidebarSteadyP95Budget,
		80,
		func() {
			app.renderView()
		},
	)
}

func BenchmarkG11F2SteadyLongFrozenHistory(b *testing.B) {
	chat, _ := g11F2LongFrozenHistory()
	chat.Render(120, 40)
	b.ReportMetric(g11F2FrozenHistoryItems, "frozen-items")
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		chat.viewDirty = true
		chat.Render(120, 40)
	}
}

func BenchmarkG11F2SteadyManyLiveSidebarRows(b *testing.B) {
	app := g11F2LiveSidebarApp(b, g11F2LiveSidebarRows)
	b.ReportMetric(g11F2LiveSidebarRows, "live-rows")
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		app.renderView()
	}
}

func g11F2LongFrozenHistory() (*ChatView, []*g11F2FrozenHistoryItem) {
	chat := NewChatView(defaultStyles())
	chat.SetSize(120, 40)
	items := make([]*g11F2FrozenHistoryItem, 0, g11F2FrozenHistoryItems)
	for index := range g11F2FrozenHistoryItems {
		item := &g11F2FrozenHistoryItem{
			id:      fmt.Sprintf("g11f2-frozen-%05d", index),
			content: fmt.Sprintf("bounded frozen history %05d", index),
		}
		items = append(items, item)
		chat.AppendHistoryItem(item)
	}
	return chat, items
}

func g11F2HistoryRenderCount(items []*g11F2FrozenHistoryItem) int {
	total := 0
	for _, item := range items {
		total += item.renders
	}
	return total
}

func g11F2LiveSidebarApp(tb testing.TB, rows int) *App {
	tb.Helper()
	query := responsiveTestEngine(tb)
	app := New(Config{
		Engine:        query,
		Resumed:       true,
		Model:         "test-model",
		ReducedMotion: true,
	})
	explorer := p313ExplorerSnapshot()
	for index := range rows {
		execution := p313Execution(
			fmt.Sprintf("g11f2-live-%03d", index),
			1,
			engine.TaskExplorerExecutionRunning,
		)
		execution.Task = fmt.Sprintf("live sidebar row %03d", index)
		explorer.Executions = append(explorer.Executions, execution)
	}
	installP313ExplorerSnapshot(app, &explorer)
	updateAppSilent(app, tea.WindowSizeMsg{Width: 180, Height: 40})
	app.renderView()
	if app.layout.mode != layoutModeWide || app.layout.sidebarRect.Width == 0 {
		tb.Fatalf("G11.F2 sidebar fixture did not enter wide layout: %#v", app.layout)
	}
	return app
}
