package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestAppDialogStackRestoresUnderlyingMode(t *testing.T) {
	t.Parallel()

	app := newTestApp(80, 24)
	app.search.Show()
	app.state = StateSearch
	app.updateLayout()

	app.openHelpOverlay()
	if app.state != StateHelp || app.baseState() != StateSearch || app.dialogs.Len() != 1 {
		t.Fatalf("help stack = state:%v base:%v depth:%d", app.state, app.baseState(), app.dialogs.Len())
	}
	app.pushDialog(StateModelPicker)
	if app.state != StateModelPicker || app.dialogs.Len() != 2 {
		t.Fatalf("nested stack = state:%v depth:%d", app.state, app.dialogs.Len())
	}

	app.popDialog(StateModelPicker)
	if app.state != StateHelp || app.dialogs.Len() != 1 {
		t.Fatalf("closing top did not reveal help: state:%v depth:%d", app.state, app.dialogs.Len())
	}
	app.handleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if app.state != StateSearch || app.dialogs.Len() != 0 {
		t.Fatalf("closing help did not restore search: state:%v depth:%d", app.state, app.dialogs.Len())
	}
}

func TestAppDialogStackPermissionRestoresSearch(t *testing.T) {
	t.Parallel()

	app := newTestApp(80, 24)
	app.search.Show()
	app.state = StateSearch
	response := make(chan PermissionResponse, 1)
	app.dialog.Show("Read", `{}`, "", response)
	app.pushDialog(StatePermission)

	app.handleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if app.state != StateSearch || app.dialogs.Len() != 0 {
		t.Fatalf("permission close lost underlying search: state:%v depth:%d", app.state, app.dialogs.Len())
	}
	select {
	case got := <-response:
		if got != PermissionDeny {
			t.Fatalf("permission response = %v, want deny", got)
		}
	default:
		t.Fatal("permission dialog did not respond")
	}
}

func TestAppDialogStackBlocksMouseFromUnderlyingChat(t *testing.T) {
	t.Parallel()

	app := newTestApp(80, 16)
	for i := 0; i < 20; i++ {
		app.chat.AppendSystem("history row")
	}
	app.chat.ScrollUp(3)
	beforeIdx, beforeLine := app.chat.offsetIdx, app.chat.offsetLine
	app.openHelpOverlay()

	updateAppSilent(app, tuiMouseMsg{Button: tea.MouseWheelDown, X: 2, Y: 2})
	if app.chat.offsetIdx != beforeIdx || app.chat.offsetLine != beforeLine {
		t.Fatalf("modal mouse leaked to chat: before=%d/%d after=%d/%d",
			beforeIdx, beforeLine, app.chat.offsetIdx, app.chat.offsetLine)
	}
}

func TestAppDialogStackCtrlCClosesPassiveTopOnly(t *testing.T) {
	t.Parallel()

	app := newTestApp(80, 24)
	app.search.Show()
	app.state = StateSearch
	app.openHelpOverlay()

	app.handleKey(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if app.state != StateSearch || app.dialogs.Len() != 0 || app.quitting {
		t.Fatalf("ctrl+c did not close passive top only: state=%v depth=%d quitting=%v",
			app.state, app.dialogs.Len(), app.quitting)
	}
}

func TestAppDialogStackCanCancelCoveredAsyncDialog(t *testing.T) {
	t.Parallel()

	app := newTestApp(80, 24)
	app.state = StateSearch
	app.pushDialog(StatePermission)
	app.pushDialog(StateHelp)

	if !app.removeDialog(StatePermission) {
		t.Fatal("covered permission dialog was not removed")
	}
	if app.state != StateHelp || app.dialogs.Len() != 1 || app.baseState() != StateSearch {
		t.Fatalf("covered removal corrupted stack: state=%v base=%v depth=%d",
			app.state, app.baseState(), app.dialogs.Len())
	}
	app.popDialog(StateHelp)
	if app.state != StateSearch {
		t.Fatalf("rewired return state = %v, want search", app.state)
	}
}

func TestEveryModalStateUsesDialogStackBoundary(t *testing.T) {
	t.Parallel()

	states := []AppState{
		StatePermission, StateResume, StateHelp,
		StateBypassConfirm, StateMCPApproval, StateModelPicker,
		StateBackgroundTasks, StateMCPSettings, StateAgentWizard, StateTeams,
		StateCommandPalette, StatePlanApproval, StateAskUser, StateAgentPicker,
	}
	for _, state := range states {
		if !isDialogState(state) {
			t.Errorf("state %v is missing from dialog boundary", state)
		}
	}
	for _, state := range []AppState{StateWelcome, StateChat, StateExpand, StateTaskPanel, StateSearch, StateMessageSelect} {
		if isDialogState(state) {
			t.Errorf("base state %v was classified as modal", state)
		}
	}
}
