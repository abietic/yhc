package tui

import (
	"testing"

	"github.com/abietic/yhc/engine"
)

func TestThreadViewSwitchPreservesConversationViewState(t *testing.T) {
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{SessionID: "leader-thread", CWD: t.TempDir()})
	t.Cleanup(eng.Close)
	app := New(Config{Engine: eng, Resumed: true})
	app.width = 80
	app.height = 24
	app.updateLayout()

	for i := 0; i < 12; i++ {
		app.chat.AppendSystem("leader history")
	}
	app.chat.Render(80, 6)
	app.chat.ScrollToTop()
	app.chat.ScrollDown(2)
	leaderChat := app.chat
	leaderOffset := app.chat.offsetIdx
	leaderOffsetLine := app.chat.offsetLine

	app.textarea.SetValue("first line\nsecond line\nthird line")
	app.textarea.CursorUp()
	app.textarea.SetCursorColumn(3)
	app.history = []string{"old prompt"}
	app.historyIdx = 0
	app.draft = "history draft"
	app.composerElements = []threadComposerElement{{ID: "file-1", Kind: "file", Label: "main.go", Value: "main.go"}}
	app.queuedInputPreview = []threadQueuedInput{{ID: "queued-1", Content: "run tests after this"}}
	app.threadDetailTab = agentDetailTranscript
	app.selection.anchor = &selItemPoint{itemIdx: 0, lineInItem: 0, col: 1}
	app.selection.focus = &selItemPoint{itemIdx: 1, lineInItem: 0, col: 4}
	app.search.Show()
	app.search.input.SetValue("leader")
	app.search.query = "leader"
	app.search.UpdateMatches(app.chat.Items())
	app.state = StateSearch

	if err := app.switchThreadView("child-thread", engine.ThreadModeReplayOnly); err != nil {
		t.Fatal(err)
	}
	if got := app.activeThreadViewID(); got != "child-thread" {
		t.Fatalf("active thread = %q", got)
	}
	if app.chat == leaderChat || len(app.chat.Items()) != 0 || app.textarea.Value() != "" {
		t.Fatalf("new child view reused leader state: chat=%p leader=%p items=%d draft=%q", app.chat, leaderChat, len(app.chat.Items()), app.textarea.Value())
	}
	if app.state != StateChat || app.search.Visible() || app.selection.HasSelection() {
		t.Fatalf("new child surface inherited leader overlay state")
	}

	app.chat.AppendSystem("child history")
	app.textarea.SetValue("child draft")
	app.threadDetailTab = agentDetailOutput
	if err := app.switchThreadView("leader-thread", engine.ThreadModeLiveAttach); err != nil {
		t.Fatal(err)
	}
	if app.chat != leaderChat || len(app.chat.Items()) != 12 {
		t.Fatalf("leader chat was not restored: chat=%p leader=%p items=%d", app.chat, leaderChat, len(app.chat.Items()))
	}
	if app.chat.Following() || app.chat.offsetIdx != leaderOffset || app.chat.offsetLine != leaderOffsetLine {
		t.Fatalf("leader scroll = follow:%v offset:%d/%d, want false %d/%d", app.chat.Following(), app.chat.offsetIdx, app.chat.offsetLine, leaderOffset, leaderOffsetLine)
	}
	if got := app.textarea.Value(); got != "first line\nsecond line\nthird line" {
		t.Fatalf("leader draft = %q", got)
	}
	lineInfo := app.textarea.LineInfo()
	if app.textarea.Line() != 1 || lineInfo.StartColumn+lineInfo.ColumnOffset != 3 {
		t.Fatalf("leader cursor = line %d col %d", app.textarea.Line(), lineInfo.StartColumn+lineInfo.ColumnOffset)
	}
	if app.historyIdx != 0 || app.draft != "history draft" {
		t.Fatalf("history state = %d %q", app.historyIdx, app.draft)
	}
	if len(app.composerElements) != 1 || app.composerElements[0].ID != "file-1" ||
		len(app.queuedInputPreview) != 1 || app.queuedInputPreview[0].ID != "queued-1" {
		t.Fatalf("composer state = %#v queue=%#v", app.composerElements, app.queuedInputPreview)
	}
	if !app.selection.HasSelection() || !app.search.Visible() || app.search.Query() != "leader" || app.state != StateSearch {
		t.Fatalf("selection/search state was not restored")
	}
	if app.threadDetailTab != agentDetailTranscript {
		t.Fatalf("detail tab = %v", app.threadDetailTab)
	}
}

func TestThreadViewReplayNeverAutoDispatchesQueuedInput(t *testing.T) {
	app := New(Config{Resumed: true})
	if err := app.switchThreadView("child-thread", engine.ThreadModeReplayOnly); err != nil {
		t.Fatal(err)
	}
	app.queuedInputPreview = []threadQueuedInput{{ID: "queued-1", Content: "do not replay"}}
	if len(app.queuedInputPreview) != 1 {
		t.Fatalf("replay queue was mutated: %#v", app.queuedInputPreview)
	}

	if err := app.switchThreadView(fallbackLeaderThreadID, engine.ThreadModeLiveAttach); err != nil {
		t.Fatal(err)
	}
	if err := app.switchThreadView("child-thread", engine.ThreadModeLiveAttach); err != nil {
		t.Fatal(err)
	}
	if len(app.queuedInputPreview) != 1 {
		t.Fatalf("activation auto-dispatched queue: %#v", app.queuedInputPreview)
	}
}

func TestThreadViewSwitchSuspendsPlanPresentationWithoutResolvingRuntime(t *testing.T) {
	app := New(Config{Resumed: true})
	responseCh := make(chan PermissionResponse, 2)
	request := threadAttentionRequest{
		ID:         "plan-request",
		ThreadID:   app.activeThreadViewID(),
		Kind:       threadAttentionPlan,
		Tool:       "ExitPlanMode",
		SessionID:  "session",
		Source:     "callback",
		responseCh: responseCh,
	}
	if cmd := app.enqueueThreadAttention(request); cmd != nil {
		t.Fatal("plan approval unexpectedly scheduled a timeout")
	}
	if app.threadAttention.activeID != request.ID || !app.planDialog.IsVisible() {
		t.Fatal("plan approval was not presented")
	}
	uiResponseClosed := make(chan PermissionResponse, 1)
	stored, _ := app.threadAttention.get(request.ID)
	stored.uiResponse = uiResponseClosed
	app.textarea.SetValue("leader draft")

	if err := app.switchThreadView("child-thread", engine.ThreadModeReplayOnly); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-responseCh:
		t.Fatalf("thread switch resolved runtime with %v", got)
	default:
	}
	if app.threadAttention.activeID != "" || app.planDialog.IsVisible() || app.hasDialog(StatePlanApproval) {
		t.Fatal("thread switch left plan approval active")
	}
	select {
	case _, ok := <-uiResponseClosed:
		if ok {
			t.Fatal("thread switch injected a UI response while detaching")
		}
	default:
		t.Fatal("thread switch left the detached UI response waiter open")
	}
	if _, ok := app.threadAttention.get(request.ID); !ok {
		t.Fatal("thread switch removed the unresolved plan approval")
	}
	if _, ok := app.threadAttention.suppressed[request.ID]; ok {
		t.Fatal("thread switch suppressed the unresolved plan approval")
	}

	if err := app.switchThreadView(fallbackLeaderThreadID, engine.ThreadModeLiveAttach); err != nil {
		t.Fatal(err)
	}
	if app.textarea.Value() != "leader draft" {
		t.Fatalf("leader presentation was not captured before switch: %q", app.textarea.Value())
	}
	app.presentNextThreadAttention()
	if app.threadAttention.activeID != request.ID || !app.planDialog.IsVisible() {
		t.Fatal("returning to the owner did not re-present plan approval")
	}
	app.resolveThreadAttention(request.ID, PermissionAllow)
	if got := <-responseCh; got != PermissionAllow {
		t.Fatalf("explicit plan response = %v, want allow", got)
	}
	select {
	case duplicate := <-responseCh:
		t.Fatalf("duplicate terminal response = %v", duplicate)
	default:
	}
}

func TestThreadViewSwitchSuspendsPermissionAndQuestionPresentation(t *testing.T) {
	tests := []struct {
		name  string
		kind  threadAttentionKind
		input string
		state AppState
	}{
		{name: "permission", kind: threadAttentionPermission, input: `{"path":"main.go"}`, state: StatePermission},
		{name: "question", kind: threadAttentionQuestion, input: `{"questions":[{"question":"Continue?","header":"Choice","options":[{"label":"Yes","description":"Continue"}]}]}`, state: StateAskUser},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app := New(Config{Resumed: true})
			responseCh := make(chan PermissionResponse, 2)
			request := threadAttentionRequest{
				ID: "request-" + test.name, ThreadID: app.activeThreadViewID(),
				Kind: test.kind, Tool: "Tool", Input: test.input,
				Source: "callback", responseCh: responseCh,
			}
			app.enqueueThreadAttention(request)
			if app.threadAttention.activeID != request.ID || !app.hasDialog(test.state) {
				t.Fatalf("%s attention was not presented", test.name)
			}

			if err := app.switchThreadView("child-thread", engine.ThreadModeReplayOnly); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-responseCh:
				t.Fatalf("thread switch resolved %s attention with %v", test.name, got)
			default:
			}
			if app.threadAttention.activeID != "" || app.hasDialog(test.state) {
				t.Fatalf("thread switch left %s presentation active", test.name)
			}
			if _, ok := app.threadAttention.get(request.ID); !ok {
				t.Fatalf("thread switch removed %s runtime request", test.name)
			}

			if err := app.switchThreadView(fallbackLeaderThreadID, engine.ThreadModeLiveAttach); err != nil {
				t.Fatal(err)
			}
			app.presentNextThreadAttention()
			if app.threadAttention.activeID != request.ID || !app.hasDialog(test.state) {
				t.Fatalf("returning to owner did not re-present %s attention", test.name)
			}
			app.resolveThreadAttention(request.ID, PermissionDeny)
			if got := <-responseCh; got != PermissionDeny {
				t.Fatalf("explicit %s response = %v, want deny", test.name, got)
			}
		})
	}
}

func TestThreadViewStoreEvictsOnlyCleanReplayViews(t *testing.T) {
	store := newThreadViewStore(2, "leader", defaultStyles())
	if _, err := store.activate("replay-1", engine.ThreadModeReplayOnly); err != nil {
		t.Fatal(err)
	}
	if _, err := store.activate("leader", engine.ThreadModeLiveAttach); err != nil {
		t.Fatal(err)
	}
	if _, err := store.activate("replay-2", engine.ThreadModeEvictedTranscript); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.views["replay-1"]; ok {
		t.Fatal("old clean replay view was not evicted")
	}

	store = newThreadViewStore(2, "leader", defaultStyles())
	protected, err := store.activate("replay-1", engine.ThreadModeReplayOnly)
	if err != nil {
		t.Fatal(err)
	}
	protected.Editor.Draft = "unsent work"
	if _, err := store.activate("leader", engine.ThreadModeLiveAttach); err != nil {
		t.Fatal(err)
	}
	if _, err := store.activate("replay-2", engine.ThreadModeReplayOnly); err == nil {
		t.Fatal("capacity accepted by silently evicting an unsent draft")
	}
	if _, ok := store.views["replay-1"]; !ok {
		t.Fatal("protected replay view was evicted")
	}
}

func TestThreadViewLeaderRebindPreservesDefaultView(t *testing.T) {
	app := New(Config{Resumed: true})
	app.textarea.SetValue("leader draft")
	app.chat.AppendSystem("leader history")
	app.rebindLeaderThreadView("session-thread")

	if app.activeThreadViewID() != "session-thread" || app.threadViews.leaderThreadID != "session-thread" {
		t.Fatalf("leader IDs = active:%q leader:%q", app.activeThreadViewID(), app.threadViews.leaderThreadID)
	}
	if err := app.switchThreadView("child-thread", engine.ThreadModeLiveAttach); err != nil {
		t.Fatal(err)
	}
	if err := app.switchThreadView("session-thread", engine.ThreadModeLiveAttach); err != nil {
		t.Fatal(err)
	}
	if app.textarea.Value() != "leader draft" || len(app.chat.Items()) != 1 {
		t.Fatalf("rebound leader state = draft:%q items:%d", app.textarea.Value(), len(app.chat.Items()))
	}
}
