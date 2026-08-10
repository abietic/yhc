package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/provider"
	"github.com/abietic/yhc/internal/tui/attachments"
)

const queueTestPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="

func newQueueTestApp(t *testing.T) (*App, *engine.QueryEngine) {
	t.Helper()
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		CWD: t.TempDir(), TranscriptDir: t.TempDir(), SessionID: "queue-test", Model: "gpt-4o",
		ModelResolver: engine.ModelResolverFunc(func(modelSpec string) (provider.ResolvedConfig, error) {
			return provider.ResolvedConfig{Config: provider.Config{
				Provider: provider.ProviderAgenticOpenAI,
				Model:    modelSpec,
			}}, nil
		}),
		PromptCapabilityResolver: engine.DefaultPromptCapabilityResolver(),
	})
	t.Cleanup(eng.Close)
	return New(Config{Engine: eng}), eng
}

func settleComposerAdmission(t *testing.T, app *App, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("composer admission command is nil")
	}
	app.Update(cmd())
}

func TestBusyLeaderEnterQueuesWithoutInterrupting(t *testing.T) {
	app, eng := newQueueTestApp(t)
	app.running = true
	app.width = 120
	cancelled := false
	app.cancelFn = func() { cancelled = true }
	app.textarea.SetValue("run tests next")

	cmd := app.sendMessage()
	if cancelled || !app.running {
		t.Fatalf("busy submit interrupted active query: cancelled=%v running=%v", cancelled, app.running)
	}
	if app.textarea.Value() != "run tests next" || len(app.queuedInputPreview) != 0 {
		t.Fatalf("draft changed before engine acceptance: draft=%q preview=%#v", app.textarea.Value(), app.queuedInputPreview)
	}
	settleComposerAdmission(t, app, cmd)
	if app.textarea.Value() != "" || len(app.queuedInputPreview) != 1 {
		t.Fatalf("queue UI state: draft=%q preview=%#v", app.textarea.Value(), app.queuedInputPreview)
	}
	queued := eng.QueuedUserInputs()
	if len(queued) != 1 || queued[0].Prompt != "run tests next" {
		t.Fatalf("engine queue = %#v", queued)
	}
	for _, item := range app.chat.Items() {
		if _, ok := item.(*UserMessage); ok {
			t.Fatal("pending input was promoted to chat before processing")
		}
	}
	app.notifications.Clear()
	status := stripANSIForTest(app.renderStatus())
	if !strings.Contains(status, "queue") || !strings.Contains(status, "interrupt") {
		t.Fatalf("busy status did not advertise queue/cancel semantics: %q", status)
	}
}

func TestBusyQueuePreservesRichPasteAndImageSnapshot(t *testing.T) {
	app, eng := newQueueTestApp(t)
	app.running = true
	app.width = 120
	pasted := strings.Repeat("context", attachments.PasteThreshold)
	handlePaste(app, pasted)
	app.textarea.InsertRune(' ')
	if err := app.addComposerImage(
		"screen.png",
		"/tmp/screen.png",
		"image/png",
		queueTestPNGBase64,
	); err != nil {
		t.Fatal(err)
	}

	settleComposerAdmission(t, app, app.sendMessage())
	queued, err := eng.QueuedPromptInputs()
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 || len(queued[0].Parts) != 2 ||
		queued[0].Parts[0].Kind != engine.QueuedPromptPartText ||
		!strings.Contains(queued[0].Parts[0].Text, pasted) ||
		queued[0].Parts[1].Kind != engine.QueuedPromptPartImage {
		t.Fatalf("rich engine queue = %#v notification=%q draft=%q pending=%#v", queued, app.activeToast(), app.textarea.Value(), app.composerAdmissionPending)
	}
	if len(app.queuedInputPreview) != 1 || len(app.queuedInputPreview[0].Parts) != 2 {
		t.Fatalf("rich queue preview = %#v", app.queuedInputPreview)
	}
	if rows := app.renderQueuedInputRows(); !strings.Contains(rows, "queued") || !strings.Contains(rows, "Pasted Content") {
		t.Fatalf("queued rows = %q", rows)
	}
}

func TestTerminalClaimStartsQueuedInputAsNextTurn(t *testing.T) {
	app, eng := newQueueTestApp(t)
	queued, err := eng.EnqueueUserInput(engine.UserTurnInput{Display: "next turn", Prompt: "next turn full"})
	if err != nil {
		t.Fatal(err)
	}
	app.queuedInputPreview = []threadQueuedInput{{ID: queued.ID, Content: queued.Display}}
	app.running = false

	cmd := app.startNextQueuedInput()
	if cmd == nil || !app.running || len(eng.QueuedUserInputs()) != 0 || len(app.queuedInputPreview) != 0 {
		t.Fatalf("next turn start: cmd=%v running=%v engine=%#v preview=%#v", cmd != nil, app.running, eng.QueuedUserInputs(), app.queuedInputPreview)
	}
	items := app.chat.Items()
	if len(items) == 0 {
		t.Fatal("queued input was not promoted to chat")
	}
	user, ok := items[len(items)-1].(*UserMessage)
	if !ok || user.content != "next turn" {
		t.Fatalf("promoted user row = %#v", items[len(items)-1])
	}
}

func TestTerminalSchedulesQueuedTurnAfterOldEventStreamStops(t *testing.T) {
	app, eng := newQueueTestApp(t)
	queued, err := eng.EnqueueUserInput(engine.UserTurnInput{Display: "after terminal", Prompt: "after terminal"})
	if err != nil {
		t.Fatal(err)
	}
	app.queuedInputPreview = []threadQueuedInput{{ID: queued.ID, Content: queued.Display}}
	app.running = true
	oldQueryID := app.queryID

	schedule := app.handleEngineEvent(engine.QueryEvent{
		Type: engine.EventTerminal, TerminalInfo: &engine.Terminal{Reason: engine.TerminalCompleted},
	})
	if schedule == nil || app.running {
		t.Fatalf("terminal schedule=%v running=%v", schedule != nil, app.running)
	}
	message := schedule()
	if _, ok := message.(startQueuedInputMsg); !ok {
		t.Fatalf("terminal scheduled %T", message)
	}
	_, start := app.Update(message)
	if start == nil || !app.running || app.queryID != oldQueryID+1 {
		t.Fatalf("queued restart: cmd=%v running=%v queryID=%d", start != nil, app.running, app.queryID)
	}
}

func TestInterruptKeepsEventStreamAttachedUntilTerminalThenStartsQueue(t *testing.T) {
	app, eng := newQueueTestApp(t)
	queued, err := eng.EnqueueUserInput(engine.UserTurnInput{Display: "after cancel", Prompt: "after cancel"})
	if err != nil {
		t.Fatal(err)
	}
	app.queuedInputPreview = []threadQueuedInput{{ID: queued.ID, Content: queued.Display}}
	stream := make(chan engine.QueryEvent)
	app.eventChan = stream
	app.running = true
	cancelled := false
	app.cancelFn = func() { cancelled = true }

	app.handleInterrupt()
	if !cancelled || !app.running || app.eventChan != stream || app.cancelFn != nil {
		t.Fatalf("interrupt detached stream: cancelled=%v running=%v stream=%v cancelFn=%v", cancelled, app.running, app.eventChan == stream, app.cancelFn != nil)
	}
	if len(eng.QueuedUserInputs()) != 1 {
		t.Fatalf("interrupt changed pending queue: %#v", eng.QueuedUserInputs())
	}
	app.handleEngineEvent(engine.QueryEvent{Type: engine.EventUserInterruption})
	app.handleEngineEvent(engine.QueryEvent{Type: engine.EventUserInterruption})
	interruptions := 0
	for _, item := range app.chat.Items() {
		if _, ok := item.(*InterruptionMessage); ok {
			interruptions++
		}
	}
	if interruptions != 1 || !app.running {
		t.Fatalf("interruption projection: markers=%d running=%v", interruptions, app.running)
	}
	if schedule := app.handleEngineEvent(engine.QueryEvent{
		Type: engine.EventTerminal, TerminalInfo: &engine.Terminal{Reason: engine.TerminalAbortedStreaming},
	}); schedule == nil || app.running {
		t.Fatalf("cancel terminal did not schedule queue: schedule=%v running=%v", schedule != nil, app.running)
	}
}

func TestUnexpectedEventStreamCloseStillSchedulesPendingQueue(t *testing.T) {
	app, eng := newQueueTestApp(t)
	queued, err := eng.EnqueueUserInput(engine.UserTurnInput{Display: "recover close", Prompt: "recover close"})
	if err != nil {
		t.Fatal(err)
	}
	app.queuedInputPreview = []threadQueuedInput{{ID: queued.ID, Content: queued.Display}}
	app.running = true
	app.queryID = 7

	_, cmd := app.Update(eventsDoneMsg{queryID: 7})
	if cmd == nil || app.running || app.eventChan != nil {
		t.Fatalf("stream close fallback: cmd=%v running=%v channel=%v", cmd != nil, app.running, app.eventChan)
	}
}

func TestQueuedInputAndDraftSurviveThreadSwitch(t *testing.T) {
	app, eng := newQueueTestApp(t)
	queued, err := eng.EnqueueUserInput(engine.UserTurnInput{Display: "leader queued", Prompt: "leader queued full"})
	if err != nil {
		t.Fatal(err)
	}
	app.queuedInputPreview = []threadQueuedInput{{ID: queued.ID, Content: queued.Display}}
	app.textarea.SetValue("leader draft")
	leaderID := app.leaderThreadViewID()

	if err := app.switchThreadView("child-thread", engine.ThreadModeLiveAttach); err != nil {
		t.Fatal(err)
	}
	if len(app.queuedInputPreview) != 0 {
		t.Fatalf("leader queue leaked into child view: %#v", app.queuedInputPreview)
	}
	app.textarea.SetValue("child draft")
	if err := app.switchThreadView(leaderID, engine.ThreadModeLiveAttach); err != nil {
		t.Fatal(err)
	}
	if app.textarea.Value() != "leader draft" || len(app.queuedInputPreview) != 1 || app.queuedInputPreview[0].ID != queued.ID {
		t.Fatalf("leader state after restore: draft=%q queue=%#v", app.textarea.Value(), app.queuedInputPreview)
	}
	if err := app.switchThreadView("child-thread", engine.ThreadModeLiveAttach); err != nil {
		t.Fatal(err)
	}
	if app.textarea.Value() != "child draft" || len(app.queuedInputPreview) != 0 {
		t.Fatalf("child state after restore: draft=%q queue=%#v", app.textarea.Value(), app.queuedInputPreview)
	}
}

func TestQueueLifecyclePromotesExactlyOnePreview(t *testing.T) {
	app, eng := newQueueTestApp(t)
	queued, err := eng.EnqueueUserInput(engine.UserTurnInput{Display: "steer now", Prompt: "steer now"})
	if err != nil {
		t.Fatal(err)
	}
	app.queuedInputPreview = []threadQueuedInput{{ID: queued.ID, Content: queued.Display}}

	app.handleEngineEvent(engine.QueryEvent{
		RuntimeEventEnvelope: engine.RuntimeEventEnvelope{ThreadID: app.leaderThreadViewID()},
		Type:                 engine.EventCommandLifecycle,
		CommandLifecycle: &engine.CommandLifecycleEvent{
			CommandUUID: queued.ID, Phase: engine.CommandLifecycleStarted,
		},
	})
	if len(app.queuedInputPreview) != 0 {
		t.Fatalf("started preview remains: %#v", app.queuedInputPreview)
	}
	users := 0
	for _, item := range app.chat.Items() {
		if _, ok := item.(*UserMessage); ok {
			users++
		}
	}
	if users != 1 {
		t.Fatalf("promoted user rows = %d", users)
	}

	app.handleEngineEvent(engine.QueryEvent{
		Type: engine.EventAttachment,
		AttachmentMessage: &schema.Message{
			Role: schema.User, Content: "steer now",
			Extra: map[string]any{"attachment_kind": "queued_command", "command_uuid": queued.ID},
		},
	})
	for _, item := range app.chat.Items() {
		if system, ok := item.(*SystemMessage); ok && strings.Contains(system.content, "queued_command") {
			t.Fatal("queued attachment produced a duplicate generic marker")
		}
	}
}

func TestQueueEditAndRemoveOperateOnEngineOwnedInput(t *testing.T) {
	app, eng := newQueueTestApp(t)
	app.running = true
	app.textarea.SetValue("edit this later")
	settleComposerAdmission(t, app, app.sendMessage())
	queuedID := app.queuedInputPreview[0].ID
	app.running = false

	app.handleQueueSlashCommand("/queue edit last")
	if app.textarea.Value() != "edit this later" || len(app.queuedInputPreview) != 0 || len(eng.QueuedUserInputs()) != 0 {
		t.Fatalf("queue edit: draft=%q preview=%#v engine=%#v", app.textarea.Value(), app.queuedInputPreview, eng.QueuedUserInputs())
	}

	app.textarea.Reset()
	queued, err := eng.EnqueueUserInput(engine.UserTurnInput{Display: "remove", Prompt: "remove"})
	if err != nil {
		t.Fatal(err)
	}
	app.queuedInputPreview = []threadQueuedInput{{ID: queued.ID, Content: "remove"}}
	app.handleQueueSlashCommand("/queue remove " + shortQueueID(queued.ID))
	if len(app.queuedInputPreview) != 0 || len(eng.QueuedUserInputs()) != 0 {
		t.Fatalf("queue remove failed: preview=%#v engine=%#v original=%s", app.queuedInputPreview, eng.QueuedUserInputs(), queuedID)
	}
}

func TestQueueSlashCommandRemainsAvailableWhileRunning(t *testing.T) {
	app, _ := newQueueTestApp(t)
	app.running = true
	app.width = 120
	app.textarea.SetValue("queued from slash test")
	settleComposerAdmission(t, app, app.sendMessage())
	app.textarea.SetValue("/queue list")

	if cmd := app.sendMessage(); cmd != nil {
		t.Fatal("local queue command unexpectedly started engine work")
	}
	var summary, rejected bool
	for _, item := range app.chat.Items() {
		system, ok := item.(*SystemMessage)
		if !ok {
			continue
		}
		summary = summary || strings.Contains(system.content, "Queued input (1)")
		rejected = rejected || strings.Contains(system.content, "Cannot run command")
	}
	if !summary || rejected {
		t.Fatalf("running /queue path: summary=%v rejected=%v items=%#v", summary, rejected, app.chat.Items())
	}
}

func TestRuntimeInputSignalStartsPendingInputWhenTUIIsIdle(t *testing.T) {
	app, eng := newQueueTestApp(t)
	wait := app.waitForRuntimeInput()
	if wait == nil {
		t.Fatal("runtime input subscription is unavailable")
	}
	if _, err := eng.EnqueueUserInput(engine.UserTurnInput{
		Display: "idle wake", Prompt: "idle wake",
	}); err != nil {
		t.Fatal(err)
	}
	ready := wait()
	_, scheduled := app.Update(ready)
	if scheduled == nil {
		t.Fatal("runtime input signal did not schedule an idle turn")
	}
	batch, ok := scheduled().(tea.BatchMsg)
	if !ok || len(batch) == 0 {
		t.Fatalf("scheduled message = %#v", scheduled())
	}
	start, ok := batch[0]().(startQueuedInputMsg)
	if !ok {
		t.Fatalf("first scheduled message = %#v", batch[0]())
	}
	app.Update(start)
	if !app.running || len(eng.RuntimeItems()) != 0 {
		t.Fatalf("idle wake state: running=%v pending=%#v", app.running, eng.RuntimeItems())
	}
}

func TestNewAppHydratesDurableQueuedInputPreview(t *testing.T) {
	root := t.TempDir()
	config := engine.QueryEngineConfig{
		CWD: root, TranscriptDir: root, SessionID: "durable-queue-preview",
	}
	first := engine.NewQueryEngine(config)
	queued, err := first.EnqueueUserInput(engine.UserTurnInput{
		Display: "restored preview", Prompt: "restored full prompt",
	})
	if err != nil {
		t.Fatal(err)
	}
	first.Close()

	second := engine.NewQueryEngine(config)
	t.Cleanup(second.Close)
	app := New(Config{Engine: second, Resumed: true})
	if len(app.queuedInputPreview) != 1 ||
		app.queuedInputPreview[0].ID != queued.ID ||
		app.queuedInputPreview[0].Content != "restored preview" ||
		len(app.queuedInputPreview[0].Parts) != 1 ||
		app.queuedInputPreview[0].Parts[0].Text != "restored full prompt" {
		t.Fatalf("hydrated queue preview = %#v", app.queuedInputPreview)
	}
}
