package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/abietic/yhc/engine"
)

func chooseBusyInput(t *testing.T, app *App, mode engine.PromptDeliveryMode) tea.Cmd {
	t.Helper()
	if cmd := app.sendMessage(); cmd != nil || !app.hasDialog(StateInputDelivery) {
		t.Fatal("busy submission did not ask for an explicit delivery choice")
	}
	if mode == engine.PromptDeliverySteer {
		app.handleInputDeliveryKey(tea.KeyPressMsg{Code: tea.KeyTab})
	}
	return app.handleInputDeliveryKey(tea.KeyPressMsg{Code: tea.KeyEnter})
}

func TestInputDeliveryChoicePreservesDraftAndRecordsMode(t *testing.T) {
	for _, mode := range []engine.PromptDeliveryMode{engine.PromptDeliverySteer, engine.PromptDeliveryNextTurn} {
		t.Run(string(mode), func(t *testing.T) {
			app, eng := newQueueTestApp(t)
			app.running = true
			cancelled := false
			app.cancelFn = func() { cancelled = true }
			app.textarea.SetValue("please use the new requirement")
			cmd := chooseBusyInput(t, app, mode)
			if app.textarea.Value() == "" || cancelled {
				t.Fatal("choice changed the draft or interrupted tools")
			}
			settleComposerAdmission(t, app, cmd)
			items, err := eng.QueuedPromptInputs()
			if err != nil || len(items) != 1 || items[0].Delivery != mode {
				t.Fatalf("accepted = %#v, %v", items, err)
			}
			if app.textarea.Value() != "" || len(app.inputDeliveryReceipts) != 1 || app.inputDeliveryReceipts[0].status != "pending" {
				t.Fatal("acceptance projection is missing")
			}
			app.openInputQueue()
			frame := ansi.Strip(app.inputDeliveryOverlay("", 80, 16, true))
			if !strings.Contains(frame, promptDeliveryLabel(mode)) || !strings.Contains(frame, "pending") {
				t.Fatalf("queue frame = %s", frame)
			}
		})
	}
}

func TestInputDeliveryChoiceCancellationAndFences(t *testing.T) {
	for _, change := range []string{"escape", "draft", "engine", "task"} {
		t.Run(change, func(t *testing.T) {
			app, eng := newQueueTestApp(t)
			app.running = true
			app.textarea.SetValue("draft retained")
			app.sendMessage()
			switch change {
			case "escape":
				app.handleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
			case "draft":
				app.textarea.SetValue("new draft")
				app.markComposerChanged()
			case "task":
				app.queryID++
				app.handleInputDeliveryKey(tea.KeyPressMsg{Code: tea.KeyEnter})
			case "engine":
				other, _ := newQueueTestApp(t)
				app.SetEngine(other.engine)
				app.handleInputDeliveryKey(tea.KeyPressMsg{Code: tea.KeyEnter})
			}
			if app.hasDialog(StateInputDelivery) || app.textarea.Value() == "" || len(eng.QueuedUserInputs()) != 0 {
				t.Fatal("dismissed choice submitted or discarded a draft")
			}
		})
	}
}

func TestInputQueueEditWithdrawAndClaimBoundary(t *testing.T) {
	app, eng := newQueueTestApp(t)
	app.running = true
	app.textarea.SetValue("edit this before claim")
	settleComposerAdmission(t, app, chooseBusyInput(t, app, engine.PromptDeliveryNextTurn))
	app.openInputQueue()
	app.handleInputQueueKey(tea.KeyPressMsg{Code: 'e'})
	if app.textarea.Value() != "edit this before claim" || app.hasDialog(StateInputQueue) || len(eng.QueuedUserInputs()) != 0 || app.inputDeliveryReceipts[0].status != "editing" {
		t.Fatal("pending edit did not atomically restore the composer")
	}
	app.textarea.SetValue("edited requirement")
	settleComposerAdmission(t, app, chooseBusyInput(t, app, engine.PromptDeliveryNextTurn))
	app.openInputQueue()
	app.handleInputQueueKey(tea.KeyPressMsg{Code: 'd'})
	if len(eng.QueuedUserInputs()) != 0 || app.inputDeliveryReceipts[len(app.inputDeliveryReceipts)-1].status != "withdrawn" {
		t.Fatal("withdrawal lacked exact engine acceptance")
	}
	app.popDialog(StateInputQueue)
	app.textarea.SetValue("claimed input")
	settleComposerAdmission(t, app, chooseBusyInput(t, app, engine.PromptDeliveryNextTurn))
	app.openInputQueue()
	claimed, ok, err := eng.ClaimNextRuntimeItem()
	if err != nil || !ok {
		t.Fatalf("claim = %v, %v", ok, err)
	}
	app.handleInputQueueKey(tea.KeyPressMsg{Code: 'd'})
	app.handleInputQueueKey(tea.KeyPressMsg{Code: 'e'})
	statuses, statusErr := eng.QueuedPromptDeliveryStatuses([]string{claimed.ID})
	if statusErr != nil || statuses[claimed.ID] != engine.PromptDeliveryProcessing || len(eng.RuntimeItems()) != 0 || app.textarea.Value() != "" {
		t.Fatal("stale pending view mutated claimed work")
	}
	rows := app.currentInputDeliveryReceipts()
	if rows[len(rows)-1].status != "claimed" {
		t.Fatalf("ownership was not refreshed: %#v", rows)
	}
}

func TestInputDeliverySettlingAfterTaskEndWakesQueue(t *testing.T) {
	app, eng := newQueueTestApp(t)
	app.running = true
	app.textarea.SetValue("accepted after task finished")
	cmd := chooseBusyInput(t, app, engine.PromptDeliveryNextTurn)
	msg := cmd()
	app.running = false
	wake := app.handleComposerAdmissionSettled(msg.(composerAdmissionSettledMsg))
	if wake == nil || len(eng.QueuedUserInputs()) != 1 || app.textarea.Value() != "" {
		t.Fatal("late acceptance was stranded after the old event stream closed")
	}
}

func TestInputDeliveryStaleEngineAcceptanceCancelsOriginalOwner(t *testing.T) {
	app, original := newQueueTestApp(t)
	app.running = true
	app.textarea.SetValue("do not strand this in the previous session")
	cmd := chooseBusyInput(t, app, engine.PromptDeliveryNextTurn)
	msg := cmd()
	other, replacement := newQueueTestApp(t)
	_ = other
	app.SetEngine(replacement)
	app.handleComposerAdmissionSettled(msg.(composerAdmissionSettledMsg))
	if len(original.QueuedUserInputs()) != 0 || len(replacement.QueuedUserInputs()) != 0 || app.textarea.Value() == "" || app.composerAdmissionPending != nil {
		t.Fatal("stale acceptance stranded work or changed the replacement session")
	}
}

func TestInputDeliveryClaimBeforeAcceptanceDoesNotPromoteChat(t *testing.T) {
	app, eng := newQueueTestApp(t)
	app.running = true
	app.textarea.SetValue("wait for actual start")
	cmd := chooseBusyInput(t, app, engine.PromptDeliveryNextTurn)
	msg := cmd().(composerAdmissionSettledMsg)
	claimed, ok, err := eng.ClaimNextRuntimeItem()
	if err != nil || !ok {
		t.Fatalf("claim=%v,%v", ok, err)
	}
	app.handleComposerAdmissionSettled(msg)
	if len(app.chat.Items()) != 0 {
		t.Fatal("a claim without a start event was promoted to chat")
	}
	app.handleEngineEvent(engine.QueryEvent{Type: engine.EventCommandLifecycle, CommandLifecycle: &engine.CommandLifecycleEvent{CommandUUID: claimed.ID, Phase: engine.CommandLifecycleStarted}})
	app.handleEngineEvent(engine.QueryEvent{Type: engine.EventCommandLifecycle, CommandLifecycle: &engine.CommandLifecycleEvent{CommandUUID: claimed.ID, Phase: engine.CommandLifecycleStarted}})
	users := 0
	for _, item := range app.chat.Items() {
		if _, ok := item.(*UserMessage); ok {
			users++
		}
	}
	if users != 1 {
		t.Fatalf("started input appeared %d times", users)
	}
}

func TestInputDeliveryResponsiveGolden(t *testing.T) {
	var got strings.Builder
	for _, size := range []struct{ w, h int }{{40, 16}, {80, 24}, {120, 28}, {180, 32}, {40, 7}} {
		app, _ := newQueueTestApp(t)
		app.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
		app.running = true
		app.textarea.SetValue("界面 delivery draft")
		app.openInputDeliveryChoice()
		choice := ansi.Strip(app.inputDeliveryOverlay("", size.w, size.h, false))
		if !strings.Contains(choice, "next turn") || !strings.Contains(choice, "Enter") || !strings.Contains(choice, "Esc") {
			t.Fatalf("choice lost controls at %dx%d: %s", size.w, size.h, choice)
		}
		fmt.Fprintf(&got, "== %dx%d choice ==\n%s\n", size.w, size.h, normalizeInputDeliveryFrame(choice))
		app.inputDeliveryReceipts = []inputDeliveryReceipt{{engine: app.engine, threadID: app.leaderThreadViewID(), item: threadQueuedInput{ID: "queued-1", Content: "界面 \x1b[31munsafe control", Delivery: engine.PromptDeliveryNextTurn}, status: "pending"}}
		queue := ansi.Strip(app.inputDeliveryOverlay("", size.w, size.h, true))
		for _, frame := range []string{choice, queue} {
			for _, line := range strings.Split(frame, "\n") {
				if ansi.StringWidth(line) > size.w {
					t.Fatalf("frame overflow at width %d", size.w)
				}
			}
		}
		fmt.Fprintf(&got, "== %dx%d queue ==\n%s\n", size.w, size.h, normalizeInputDeliveryFrame(queue))
	}
	path := filepath.Join("testdata", "input_delivery.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil || string(want) != got.String() {
		t.Fatalf("input delivery golden mismatch: %v\n%s", err, got.String())
	}
}

func normalizeInputDeliveryFrame(frame string) string {
	lines := strings.Split(frame, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}

func TestInputDeliveryReceiptsStayWithTheirThread(t *testing.T) {
	app, eng := newQueueTestApp(t)
	app.rememberInputDelivery(threadQueuedInput{ID: "previous-thread", Content: "private previous draft", State: engine.RuntimeItemPending})
	app.recordInputDeliveryStarted("previous-thread")
	app.rebindLeaderThreadView(eng.ThreadID() + "-restored")
	if len(app.currentInputDeliveryReceipts()) != 0 {
		t.Fatal("previous thread receipt leaked into restored view")
	}
	app.rememberInputDelivery(threadQueuedInput{ID: "previous-thread", Content: "current draft", State: engine.RuntimeItemPending})
	app.promoteEarlyInputDelivery("previous-thread")
	if len(app.chat.Items()) != 0 {
		t.Fatal("previous thread start event promoted current input")
	}
}
