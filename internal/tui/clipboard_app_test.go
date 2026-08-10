package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/internal/tui/keybindings"
)

func p273SuccessfulClipboardService() *ClipboardService {
	return p273Service(
		context.Background(),
		&p273RecordingWriter{},
		clipboardEnvironment{goos: "darwin"},
		func(name string) (string, error) { return "/fixed/" + name, nil },
		func(context.Context, clipboardNativeCommand, []byte) error { return nil },
		time.Second,
	)
}

func p273LatestNotification(t *testing.T, app *App) Notification {
	t.Helper()
	active := app.notifications.Active()
	if len(active) == 0 {
		t.Fatal("expected clipboard notification")
	}
	return active[len(active)-1]
}

func p273FinishClipboardCmd(t *testing.T, app *App, cmd tea.Cmd) clipboardResultMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("clipboard request returned nil command")
	}
	result, ok := cmd().(clipboardResultMsg)
	if !ok {
		t.Fatalf("clipboard command result = %T", cmd())
	}
	_, _ = app.Update(result)
	return result
}

func TestP273ClipboardAllCallerResultsAreTypedAndTruthful(t *testing.T) {
	callers := []ClipboardCaller{
		ClipboardCallerChatSelection,
		ClipboardCallerExpandSelection,
		ClipboardCallerKeyboardSelection,
		ClipboardCallerActionCopy,
	}
	for _, caller := range callers {
		t.Run(clipboardCallerTestName(caller), func(t *testing.T) {
			app := New(Config{Chooser: func(int) int { return 0 }})
			app.SetClipboardService(p273SuccessfulClipboardService())
			cmd := app.requestClipboardCopy(caller, "caller payload")
			result := p273FinishClipboardCmd(t, app, cmd)
			if result.caller != caller ||
				result.sourceBytes != len("caller payload") ||
				result.terminal != clipboardTerminalSequenceWritten ||
				result.native != clipboardNativeSucceeded {
				t.Fatalf("typed result = %#v", result)
			}
			if app.clipboardPending != nil {
				t.Fatalf("caller %v retained pending state", caller)
			}
			notification := p273LatestNotification(t, app)
			if notification.Severity != NotifyInfo ||
				notification.Message != "Copied to the system clipboard." {
				t.Fatalf("success notification = %#v", notification)
			}
		})
	}
}

func clipboardCallerTestName(caller ClipboardCaller) string {
	switch caller {
	case ClipboardCallerChatSelection:
		return "chat-selection"
	case ClipboardCallerExpandSelection:
		return "expand-selection"
	case ClipboardCallerKeyboardSelection:
		return "keyboard-selection"
	case ClipboardCallerActionCopy:
		return "action-copy"
	default:
		return "unknown"
	}
}

func TestP273ClipboardBusyAndOversizedAdmissionKeepPendingIdentity(t *testing.T) {
	app := New(Config{Chooser: func(int) int { return 0 }})
	app.SetClipboardService(p273SuccessfulClipboardService())
	first := app.requestClipboardCopy(ClipboardCallerChatSelection, "first")
	if first == nil || app.clipboardPending == nil {
		t.Fatal("first clipboard request was not admitted")
	}
	pending := *app.clipboardPending

	if second := app.requestClipboardCopy(ClipboardCallerActionCopy, "second"); second != nil {
		t.Fatal("busy clipboard request started a command")
	}
	if app.clipboardPending == nil || *app.clipboardPending != pending {
		t.Fatalf("busy request changed pending identity: %#v", app.clipboardPending)
	}
	notification := p273LatestNotification(t, app)
	if notification.Severity != NotifyWarning ||
		notification.Message != "A clipboard copy is already in progress." {
		t.Fatalf("busy notification = %#v", notification)
	}

	app = New(Config{Chooser: func(int) int { return 0 }})
	app.SetClipboardService(p273SuccessfulClipboardService())
	oversized := strings.Repeat("x", clipboardMaxSourceBytes+1)
	if cmd := app.requestClipboardCopy(ClipboardCallerKeyboardSelection, oversized); cmd != nil {
		t.Fatal("oversized clipboard request started a command")
	}
	if app.clipboardPending != nil {
		t.Fatalf("oversized request became pending: %#v", app.clipboardPending)
	}
	notification = p273LatestNotification(t, app)
	if notification.Severity != NotifyWarning ||
		notification.Message !=
			"Clipboard payload exceeds the 256 KiB (262,144-byte) limit; no transport started." {
		t.Fatalf("oversized notification = %#v", notification)
	}
}

func TestP273ClipboardStaleIDOrCallerCannotClearPendingOrNotify(t *testing.T) {
	app := New(Config{Chooser: func(int) int { return 0 }})
	app.SetClipboardService(p273SuccessfulClipboardService())
	cmd := app.requestClipboardCopy(ClipboardCallerChatSelection, "payload")
	if cmd == nil || app.clipboardPending == nil {
		t.Fatal("clipboard request was not admitted")
	}
	pending := *app.clipboardPending
	success := clipboardResultMsg{
		requestID:   pending.id,
		caller:      pending.caller,
		sourceBytes: len("payload"),
		terminal:    clipboardTerminalSequenceWritten,
		native:      clipboardNativeSucceeded,
	}
	staleID := success
	staleID.requestID++
	app.handleClipboardResult(staleID)
	staleCaller := success
	staleCaller.caller = ClipboardCallerActionCopy
	app.handleClipboardResult(staleCaller)
	if app.clipboardPending == nil || *app.clipboardPending != pending {
		t.Fatalf("stale result changed pending state: %#v", app.clipboardPending)
	}
	if app.notifications.Count() != 0 {
		t.Fatalf("stale result emitted notification: %#v", app.notifications.Active())
	}

	app.handleClipboardResult(success)
	if app.clipboardPending != nil {
		t.Fatal("matching result did not clear pending state")
	}
	if got := p273LatestNotification(t, app).Message; got != "Copied to the system clipboard." {
		t.Fatalf("matching feedback = %q", got)
	}
}

func TestP273ClipboardDegradedResultsNeverClaimCopySuccess(t *testing.T) {
	tests := []struct {
		name     string
		terminal clipboardTerminalOutcome
		native   clipboardNativeOutcome
		failure  clipboardFailureCategory
		severity NotificationSeverity
		contains string
	}{
		{
			name:     "SSH unconfirmed",
			terminal: clipboardTerminalSequenceWritten,
			native:   clipboardNativeSkippedSSH,
			severity: NotifyWarning,
			contains: "acceptance is not confirmed",
		},
		{
			name:     "native unavailable",
			terminal: clipboardTerminalSequenceWritten,
			native:   clipboardNativeUnavailable,
			failure:  clipboardFailureNativeUnavailable,
			severity: NotifyWarning,
			contains: "native helper unavailable",
		},
		{
			name:     "native timeout",
			terminal: clipboardTerminalSequenceWritten,
			native:   clipboardNativeTimedOut,
			failure:  clipboardFailureNativeTimeout,
			severity: NotifyWarning,
			contains: "native helper timed out",
		},
		{
			name:     "native failure",
			terminal: clipboardTerminalSequenceWritten,
			native:   clipboardNativeFailed,
			failure:  clipboardFailureNativeFailed,
			severity: NotifyWarning,
			contains: "native helper failed",
		},
		{
			name:     "terminal failure",
			terminal: clipboardTerminalFailed,
			failure:  clipboardFailureOutputFailed,
			severity: NotifyError,
			contains: "terminal output write failed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app := New(Config{Chooser: func(int) int { return 0 }})
			app.clipboardPending = &clipboardPendingRequest{
				id:     7,
				caller: ClipboardCallerActionCopy,
			}
			app.handleClipboardResult(clipboardResultMsg{
				requestID: 7,
				caller:    ClipboardCallerActionCopy,
				terminal:  test.terminal,
				native:    test.native,
				failure:   test.failure,
			})
			notification := p273LatestNotification(t, app)
			if notification.Severity != test.severity ||
				!strings.Contains(notification.Message, test.contains) {
				t.Fatalf("degraded notification = %#v", notification)
			}
			if strings.Contains(strings.ToLower(notification.Message), "copied") {
				t.Fatalf("degraded result claimed copy success: %q", notification.Message)
			}
		})
	}
}

func TestP273ClipboardCancellationClearsPendingWithoutLateFeedback(t *testing.T) {
	app := New(Config{Chooser: func(int) int { return 0 }})
	app.clipboardPending = &clipboardPendingRequest{
		id:     9,
		caller: ClipboardCallerExpandSelection,
	}
	app.handleClipboardResult(clipboardResultMsg{
		requestID: 9,
		caller:    ClipboardCallerExpandSelection,
		native:    clipboardNativeCancelled,
		failure:   clipboardFailureCancelled,
	})
	if app.clipboardPending != nil {
		t.Fatal("cancelled result did not clear pending state")
	}
	if app.notifications.Count() != 0 {
		t.Fatalf("cancelled result emitted feedback: %#v", app.notifications.Active())
	}

	ctx, cancel := context.WithCancel(context.Background())
	app = New(Config{Chooser: func(int) int { return 0 }})
	app.SetClipboardService(p273Service(
		ctx,
		&p273RecordingWriter{},
		clipboardEnvironment{goos: "darwin"},
		func(name string) (string, error) { return "/fixed/" + name, nil },
		func(context.Context, clipboardNativeCommand, []byte) error { return nil },
		time.Second,
	))
	cmd := app.requestClipboardCopy(ClipboardCallerActionCopy, "payload")
	result, ok := cmd().(clipboardResultMsg)
	if !ok || result.native != clipboardNativeSucceeded {
		t.Fatalf("pre-cancel result = %#v", result)
	}
	cancel()
	_, _ = app.Update(result)
	if app.clipboardPending != nil {
		t.Fatal("late result after cancellation did not clear pending state")
	}
	if app.notifications.Count() != 0 {
		t.Fatalf(
			"late result after cancellation emitted feedback: %#v",
			app.notifications.Active(),
		)
	}
}

func p273PrepareChatSelection(t *testing.T, app *App) (row, start, end int) {
	t.Helper()
	app.chat.AppendUser("clipboard target")
	_ = app.View()
	projection := app.chat.currentViewportProjection()
	if projection == nil {
		t.Fatal("chat projection is unavailable")
	}
	for row, descriptor := range projection.rows {
		if descriptor.kind != chatViewportRowTranscript {
			continue
		}
		metadata, _, ok := app.chat.selectionMetadata(
			descriptor.itemIdx,
			descriptor.lineInItem,
		)
		if !ok {
			continue
		}
		start, end, ok := selectionRowCellBounds(metadata)
		if ok && end > start {
			app.selection.startForChat(start, row, app.chat)
			app.selection.updateForChat(end, row, app.chat)
			return row, start, end
		}
	}
	t.Fatal("no selectable transcript row")
	return 0, 0, 0
}

func TestP273ChatAndExpandMouseCopyRetainVisibleSelection(t *testing.T) {
	t.Run("chat", func(t *testing.T) {
		app := newTestApp(80, 24)
		app.SetClipboardService(p273SuccessfulClipboardService())
		row, _, end := p273PrepareChatSelection(t, app)
		_, cmd := app.Update(tuiMouseMsg{
			X:      end,
			Y:      app.layout.chatRect.Y + row,
			Button: tea.MouseLeft,
			Action: mouseActionRelease,
		})
		if cmd == nil || !app.selection.HasSelection() {
			t.Fatalf(
				"chat copy cmd=%v selection=%#v",
				cmd != nil,
				app.selection,
			)
		}
		p273FinishClipboardCmd(t, app, cmd)
		if !app.selection.HasSelection() {
			t.Fatal("chat copy result cleared visible selection")
		}
	})

	t.Run("expand", func(t *testing.T) {
		app := newTestApp(80, 24)
		app.SetClipboardService(p273SuccessfulClipboardService())
		app.state = StateExpand
		app.expandLines = []string{"clipboard target"}
		app.selection.HandleExpandMouse(tuiMouseMsg{
			Button: tea.MouseLeft,
			Action: mouseActionPress,
		}, 0, 0, app.expandLines)
		app.selection.HandleExpandMouse(tuiMouseMsg{
			Action: mouseActionMotion,
		}, 9, 0, app.expandLines)
		_, cmd := app.Update(tuiMouseMsg{
			X:      9,
			Y:      0,
			Button: tea.MouseLeft,
			Action: mouseActionRelease,
		})
		if cmd == nil || !app.selection.HasExpandSelection() {
			t.Fatalf(
				"expand copy cmd=%v selection=%#v",
				cmd != nil,
				app.selection,
			)
		}
		p273FinishClipboardCmd(t, app, cmd)
		if !app.selection.HasExpandSelection() {
			t.Fatal("expand copy result cleared visible selection")
		}
	})
}

func TestP273KeyboardCopyClearsOnlyAfterAdmission(t *testing.T) {
	t.Run("admitted", func(t *testing.T) {
		app := newTestApp(80, 24)
		app.SetClipboardService(p273SuccessfulClipboardService())
		row, _, end := p273PrepareChatSelection(t, app)
		app.selection.finishForChat(end, row, app.chat)
		handled, cmd := app.handleKeyAction(keybindings.ActionSelectionCopy, tea.KeyPressMsg{})
		if !handled || cmd == nil {
			t.Fatalf("keyboard admitted handled=%v cmd=%v", handled, cmd != nil)
		}
		if app.selection.HasSelection() {
			t.Fatal("admitted keyboard copy retained selection")
		}
		p273FinishClipboardCmd(t, app, cmd)
	})

	t.Run("busy", func(t *testing.T) {
		app := newTestApp(80, 24)
		app.SetClipboardService(p273SuccessfulClipboardService())
		row, _, end := p273PrepareChatSelection(t, app)
		app.selection.finishForChat(end, row, app.chat)
		app.clipboardPending = &clipboardPendingRequest{
			id:     1,
			caller: ClipboardCallerActionCopy,
		}
		handled, cmd := app.handleKeyAction(keybindings.ActionSelectionCopy, tea.KeyPressMsg{})
		if !handled || cmd != nil {
			t.Fatalf("keyboard busy handled=%v cmd=%v", handled, cmd != nil)
		}
		if !app.selection.HasSelection() {
			t.Fatal("busy keyboard copy cleared selection")
		}
	})
}

func TestP273ActionCopyDispatchReturnsClipboardCommandWithoutSuccessToast(t *testing.T) {
	app := newTestApp(80, 24)
	app.SetClipboardService(p273SuccessfulClipboardService())
	registry := commands.NewRegistry()
	err := registry.Register(&commands.Command{
		Name:        "copy-fixture",
		Entrypoints: commands.EntrypointsTUI,
		Execute: func(context.Context, *commands.CommandContext) (*commands.CommandResult, error) {
			return &commands.CommandResult{
				Action: commands.ActionCopy,
				Data:   map[string]any{"text": "committed assistant result"},
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("register copy fixture: %v", err)
	}
	app.commandRegistry = registry

	cmd := app.sendSlashCommand("/copy-fixture")
	if cmd == nil || app.clipboardPending == nil ||
		app.clipboardPending.caller != ClipboardCallerActionCopy {
		t.Fatalf(
			"ActionCopy cmd=%v pending=%#v",
			cmd != nil,
			app.clipboardPending,
		)
	}
	if app.notifications.Count() != 0 {
		t.Fatalf(
			"ActionCopy announced success before result: %#v",
			app.notifications.Active(),
		)
	}
	p273FinishClipboardCmd(t, app, cmd)
	if got := p273LatestNotification(t, app).Message; got != "Copied to the system clipboard." {
		t.Fatalf("ActionCopy result feedback = %q", got)
	}
}

func TestP273ActionCopyInvalidPayloadUsesUnifiedAdmission(t *testing.T) {
	tests := []struct {
		name string
		data map[string]any
	}{
		{
			name: "empty string",
			data: map[string]any{"text": ""},
		},
		{
			name: "non-string",
			data: map[string]any{"text": 42},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app := newTestApp(80, 24)
			app.SetClipboardService(p273SuccessfulClipboardService())
			registry := commands.NewRegistry()
			err := registry.Register(&commands.Command{
				Name:        "copy-invalid",
				Entrypoints: commands.EntrypointsTUI,
				Execute: func(
					context.Context,
					*commands.CommandContext,
				) (*commands.CommandResult, error) {
					return &commands.CommandResult{
						Action: commands.ActionCopy,
						Data:   test.data,
					}, nil
				},
			})
			if err != nil {
				t.Fatalf("register copy fixture: %v", err)
			}
			app.commandRegistry = registry

			if cmd := app.sendSlashCommand("/copy-invalid"); cmd != nil {
				t.Fatal("invalid ActionCopy payload started clipboard command")
			}
			if app.clipboardPending != nil {
				t.Fatalf(
					"invalid ActionCopy payload became pending: %#v",
					app.clipboardPending,
				)
			}
			notification := p273LatestNotification(t, app)
			if notification.Severity != NotifyWarning ||
				notification.Message !=
					"Clipboard payload is empty; no transport started." {
				t.Fatalf("invalid ActionCopy notification = %#v", notification)
			}
			for _, item := range app.chat.Items() {
				if _, ok := item.(*SystemMessage); ok {
					t.Fatalf(
						"invalid ActionCopy appended legacy system feedback: %#v",
						app.chat.Items(),
					)
				}
			}
		})
	}
}
