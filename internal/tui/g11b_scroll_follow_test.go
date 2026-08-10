package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/internal/tui/keybindings"
)

func g11bScrollableChat(t *testing.T) *ChatView {
	t.Helper()
	chat := NewChatView(defaultStyles())
	chat.SetSize(80, 6)
	for i := 0; i < 12; i++ {
		chat.AppendUser(fmt.Sprintf("row %d", i))
	}
	chat.Render(80, 6)
	return chat
}

func g11bAssertJumpPill(t *testing.T, chat *ChatView, wantLabel string) {
	t.Helper()
	pill := chat.pillModel()
	if !pill.visible || pill.label != wantLabel || pill.action != chatPillActionFollow {
		t.Fatalf("pill = %#v, want visible label %q and follow action", pill, wantLabel)
	}
	if plain := xansi.Strip(chat.Render(chat.width, chat.height)); !strings.Contains(plain, wantLabel+" ↓") {
		t.Fatalf("rendered pill missing %q: %q", wantLabel, plain)
	}
}

func TestG11BFollowStateSnapshotsEpochAndPill(t *testing.T) {
	chat := g11bScrollableChat(t)
	if chat.followState.appendEpoch != 12 {
		t.Fatalf("initial append epoch = %d, want 12", chat.followState.appendEpoch)
	}
	chat.ScrollUp(1)
	if chat.Following() || !chat.followState.baselineValid || chat.followState.unseen() != 0 {
		t.Fatalf("away state = %#v", chat.followState)
	}
	baseline := chat.followState.baselineEpoch
	chat.ScrollUp(1)
	chat.ScrollToTop()
	if chat.followState.baselineEpoch != baseline {
		t.Fatalf("repeated departure changed baseline: %d -> %d", baseline, chat.followState.baselineEpoch)
	}
	g11bAssertJumpPill(t, chat, "Jump to bottom")

	chat.AppendSystem("one")
	g11bAssertJumpPill(t, chat, "1 new message")
	chat.AppendHelp(nil)
	g11bAssertJumpPill(t, chat, "2 new messages")

	chat.ScrollToBottom()
	if !chat.Following() || chat.followState.baselineValid || chat.pillModel().visible {
		t.Fatalf("explicit bottom state = %#v pill=%#v", chat.followState, chat.pillModel())
	}
}

func TestG11BLiveAppendEpochAndNonAppendMutations(t *testing.T) {
	chat := NewChatView(defaultStyles())
	chat.withHydrationIntent(func() {
		chat.AppendUser("hydrated user")
		chat.AppendSystem("hydrated system")
		chat.appendHydratedHistoryItem(&semanticHistoryFixture{id: "hydrated", finished: true})
	})
	if chat.followState.appendEpoch != 0 {
		t.Fatalf("hydration advanced epoch: %d", chat.followState.appendEpoch)
	}

	chat.AppendUser("user")
	chat.AppendSystem("system")
	chat.AppendCompactBoundary("boundary")
	chat.AppendInterruption()
	chat.AppendCompactSummary(1)
	chat.AppendHelp(nil)
	chat.AppendHistoryItem(&semanticHistoryFixture{id: "live", finished: true})
	chat.AppendOrUpdateAssistant("assistant")
	chat.AppendOrUpdateAssistant("assistant update")
	chat.StreamThinkingDelta("thinking")
	chat.StreamThinkingDelta(" update")
	chat.AppendOrUpdateTool("tool", "Read", "{}")
	chat.AppendOrUpdateTool("tool", "Read", `{"path":"updated"}`)
	if got := chat.followState.appendEpoch; got != 10 {
		t.Fatalf("live append epoch = %d, want 10", got)
	}

	chat.FinishAssistant()
	chat.FinishThinking()
	chat.UpdateToolResult("tool", "Read", "done")
	chat.ToggleExpand()
	chat.SetStyles(defaultStyles())
	chat.Render(80, 8)
	before := chat.followState.appendEpoch
	chat.TruncateFrom(len(chat.items) - 1)
	chat.Reset()
	if chat.followState.appendEpoch != before || !chat.Following() || chat.followState.baselineValid {
		t.Fatalf("mutation/reset state = %#v, epoch before=%d", chat.followState, before)
	}

	chat.followState.appendEpoch = ^uint64(0)
	chat.AppendSystem("saturated")
	if chat.followState.appendEpoch != ^uint64(0) {
		t.Fatalf("append epoch wrapped: %d", chat.followState.appendEpoch)
	}
}

func TestG11BGroupingDoesNotRewriteUnseenEpoch(t *testing.T) {
	chat := g11bScrollableChat(t)
	chat.ScrollUp(1)
	chat.AppendToolStart("read", "Read", `{"file_path":"/tmp/a.go"}`)
	chat.UpdateToolResult("read", "Read", "package a")
	chat.AppendToolStart("grep", "Grep", `{"pattern":"TODO","path":"/tmp"}`)
	chat.UpdateToolResult("grep", "Grep", "Found 1 file")
	if got := chat.followState.unseen(); got != 2 {
		t.Fatalf("grouping rewrote unseen epoch: got %d, want 2", got)
	}
	g11bAssertJumpPill(t, chat, "2 new messages")
}

func TestG11BNoOpDepartureAndRestoredAway(t *testing.T) {
	empty := NewChatView(defaultStyles())
	empty.SetSize(80, 6)
	empty.ScrollUp(1)
	empty.ScrollToTop()
	empty.ScrollDown(0)
	empty.ScrollToItem(0)
	if !empty.Following() {
		t.Fatal("empty operations left follow")
	}

	zeroHeight := NewChatView(defaultStyles())
	zeroHeight.AppendUser("row")
	zeroHeight.ScrollUp(1)
	zeroHeight.ScrollToTop()
	zeroHeight.ScrollToItem(0)
	if !zeroHeight.Following() {
		t.Fatal("zero-height operations left follow")
	}

	nonScrollable := NewChatView(defaultStyles())
	nonScrollable.SetSize(80, 6)
	nonScrollable.AppendUser("row")
	nonScrollable.Render(80, 6)
	nonScrollable.ScrollUp(1)
	nonScrollable.ScrollToTop()
	nonScrollable.ScrollToItem(0)
	if !nonScrollable.Following() {
		t.Fatal("non-scrollable operations left follow")
	}

	chat := g11bScrollableChat(t)
	chat.ScrollUp(0)
	chat.ScrollUp(-1)
	chat.ScrollDown(0)
	chat.ScrollDown(-1)
	chat.ScrollToItem(-1)
	chat.ScrollToItem(len(chat.items))
	if !chat.Following() {
		t.Fatal("zero/negative distance or invalid target left follow")
	}
	chat.restoreAway()
	g11bAssertJumpPill(t, chat, "Jump to bottom")
}

func TestG11BPositiveScrollAndPillClickRestoreFollow(t *testing.T) {
	chat := g11bScrollableChat(t)
	chat.ScrollToTop()
	for steps := 0; !chat.Following() && steps < 100; steps++ {
		chat.ScrollDown(1)
	}
	if !chat.Following() {
		t.Fatal("positive downward scroll never reached exact bottom")
	}

	app := newTestApp(80, 24)
	app.chat.SetSize(app.layout.chatRect.Width, app.layout.chatHeight)
	for i := 0; i < 20; i++ {
		app.chat.AppendUser("message")
	}
	app.chat.Render(app.layout.chatRect.Width, app.layout.chatHeight)
	app.chat.ScrollToTop()
	app.chat.Render(app.layout.chatRect.Width, app.layout.chatHeight)
	x := app.layout.chatRect.Width / 2
	y := app.layout.chatRect.Y + app.layout.chatHeight - 1
	if !app.pillClickHits(x, app.layout.chatHeight-1) {
		t.Fatal("semantic pill model did not produce a clickable hitbox")
	}
	updateAppSilent(app, tuiMouseMsg{
		X: x, Y: y, Button: tea.MouseLeft, Action: mouseActionPress,
	})
	if !app.chat.Following() {
		t.Fatal("pill click did not restore follow")
	}
}

func TestG11BLinePageWheelTopItemAndSearchDepartures(t *testing.T) {
	tests := []struct {
		name   string
		depart func(*App)
	}{
		{
			name: "line",
			depart: func(app *App) {
				app.handleKeyAction(keybindings.ActionScrollLineUp, tea.KeyPressMsg{})
			},
		},
		{
			name: "page",
			depart: func(app *App) {
				app.handleKeyAction(keybindings.ActionScrollPageUp, tea.KeyPressMsg{})
			},
		},
		{
			name: "wheel",
			depart: func(app *App) {
				updateAppSilent(app, tuiMouseMsg{
					X: 1, Y: app.layout.chatRect.Y + 1,
					Button: tea.MouseWheelUp,
				})
			},
		},
		{
			name:   "top",
			depart: func(app *App) { app.chat.ScrollToTop() },
		},
		{
			name:   "item",
			depart: func(app *App) { app.chat.ScrollToItem(4) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := newTestApp(80, 24)
			app.chat.SetSize(app.layout.chatRect.Width, app.layout.chatHeight)
			for i := 0; i < 20; i++ {
				app.chat.AppendUser(fmt.Sprintf("row %d", i))
			}
			app.chat.Render(app.layout.chatRect.Width, app.layout.chatHeight)
			tt.depart(app)
			if app.chat.Following() || !app.chat.followState.baselineValid {
				t.Fatalf("departure state = %#v", app.chat.followState)
			}
			g11bAssertJumpPill(t, app.chat, "Jump to bottom")
		})
	}

	search := newTestApp(80, 24)
	search.chat.SetSize(search.layout.chatRect.Width, search.layout.chatHeight)
	for i := 0; i < 20; i++ {
		content := fmt.Sprintf("search row %d", i)
		if i == 4 {
			content += " needle"
		}
		search.chat.AppendUser(content)
	}
	search.chat.Render(search.layout.chatRect.Width, search.layout.chatHeight)
	search.openSearch()
	search.handleSearchKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune("needle"))})
	if search.search.MatchCount() != 1 || search.chat.Following() {
		t.Fatalf("search departure = matches:%d state:%#v", search.search.MatchCount(), search.chat.followState)
	}
	g11bAssertJumpPill(t, search.chat, "Jump to bottom")
}

func TestG11BJumpActionReachableAcrossWidths(t *testing.T) {
	for _, width := range []int{40, 80, 120, 150, 180} {
		t.Run(fmt.Sprintf("width-%d", width), func(t *testing.T) {
			app := newTestApp(width, 30)
			app.chat.SetSize(app.layout.chatRect.Width, app.layout.chatHeight)
			for i := 0; i < 30; i++ {
				app.chat.AppendUser("message")
			}
			app.chat.Render(app.layout.chatRect.Width, app.layout.chatHeight)
			app.chat.ScrollToTop()
			g11bAssertJumpPill(t, app.chat, "Jump to bottom")
			if !app.pillClickHits(app.layout.chatRect.Width/2, app.layout.chatHeight-1) {
				t.Fatalf("width %d has no reachable centered jump action", width)
			}
		})
	}
}

func TestG11BInMemoryThreadSwitchPreservesAwayState(t *testing.T) {
	app := New(Config{Resumed: true})
	updateAppSilent(app, tea.WindowSizeMsg{Width: 80, Height: 24})
	for i := 0; i < 20; i++ {
		app.chat.AppendSystem("history")
	}
	app.chat.Render(app.layout.chatRect.Width, app.layout.chatHeight)
	app.chat.ScrollToTop()
	leaderChat := app.chat
	baseline := app.chat.followState.baselineEpoch

	if err := app.switchThreadView("child", engine.ThreadModeReplayOnly); err != nil {
		t.Fatal(err)
	}
	if err := app.switchThreadView(fallbackLeaderThreadID, engine.ThreadModeLiveAttach); err != nil {
		t.Fatal(err)
	}
	if app.chat != leaderChat || app.chat.Following() ||
		!app.chat.followState.baselineValid || app.chat.followState.baselineEpoch != baseline {
		t.Fatalf("in-memory restore lost away state: same=%v state=%#v", app.chat == leaderChat, app.chat.followState)
	}
	g11bAssertJumpPill(t, app.chat, "Jump to bottom")
	if !app.pillClickHits(app.layout.chatRect.Width/2, app.layout.chatHeight-1) {
		t.Fatal("in-memory restored away view lost jump hitbox")
	}
}

func TestG11BDurableRestoreUsesCountFreeJumpAction(t *testing.T) {
	dir := t.TempDir()
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID: "g11b-durable", ThreadID: "leader", CWD: dir,
		TranscriptDir: filepath.Join(dir, "transcripts"),
	})
	t.Cleanup(eng.Close)
	messages := make([]*schema.Message, 0, 24)
	for i := 0; i < 12; i++ {
		messages = append(messages,
			&schema.Message{Role: schema.User, Content: fmt.Sprintf("durable prompt %d", i)},
			&schema.Message{Role: schema.Assistant, Content: fmt.Sprintf("durable answer %d", i)},
		)
	}
	eng.SetResumedMessages(messages)
	if err := session.SaveSessionViewState(eng.GetTranscriptDir(), eng.SessionID(), session.PersistedSessionViewState{
		SessionID: eng.SessionID(), ActiveThreadID: "leader",
		Threads: []session.PersistedThreadViewState{{ThreadID: "leader", Follow: false}},
	}); err != nil {
		t.Fatal(err)
	}

	app := New(Config{Engine: eng, Resumed: true})
	updateAppSilent(app, tea.WindowSizeMsg{Width: 80, Height: 24})
	if err := app.resetAndRestoreSessionViews(); err != nil {
		t.Fatal(err)
	}
	if len(app.chat.items) == 0 || app.chat.Following() || app.chat.followState.baselineValid {
		t.Fatalf("durable restored state = items:%d state:%#v", len(app.chat.items), app.chat.followState)
	}
	g11bAssertJumpPill(t, app.chat, "Jump to bottom")
	if !app.pillClickHits(app.layout.chatRect.Width/2, app.layout.chatHeight-1) {
		t.Fatal("durable restored away view lost jump hitbox")
	}
}

func TestG11BModalExpandAndSidebarPreemptPillClick(t *testing.T) {
	newAwayApp := func() (*App, tuiMouseMsg) {
		app := newTestApp(80, 24)
		app.chat.SetSize(app.layout.chatRect.Width, app.layout.chatHeight)
		for i := 0; i < 20; i++ {
			app.chat.AppendUser("message")
		}
		app.chat.Render(app.layout.chatRect.Width, app.layout.chatHeight)
		app.chat.ScrollToTop()
		return app, tuiMouseMsg{
			X:      app.layout.chatRect.Width / 2,
			Y:      app.layout.chatRect.Y + app.layout.chatHeight - 1,
			Button: tea.MouseLeft,
			Action: mouseActionPress,
		}
	}

	modal, click := newAwayApp()
	modal.openHelpOverlay()
	updateAppSilent(modal, click)
	if modal.chat.Following() {
		t.Fatal("modal click leaked to pill")
	}

	expanded, click := newAwayApp()
	expanded.state = StateExpand
	updateAppSilent(expanded, click)
	if expanded.chat.Following() {
		t.Fatal("expand click leaked to pill")
	}

	sidebar, click := newAwayApp()
	sidebar.layout.sidebarRect.X = click.X
	sidebar.layout.sidebarRect.Width = 1
	updateAppSilent(sidebar, click)
	if sidebar.chat.Following() {
		t.Fatal("sidebar click leaked to pill")
	}
}
