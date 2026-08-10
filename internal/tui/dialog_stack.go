package tui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/abietic/yhc/engine/commands"
)

// DialogAction is the result of a stackable dialog handling a key event.
type DialogAction interface {
	dialogAction()
}

type DialogActionNone struct{}

func (DialogActionNone) dialogAction() {}

type DialogActionClose struct{}

func (DialogActionClose) dialogAction() {}

type DialogActionResult struct {
	Value string
}

func (DialogActionResult) dialogAction() {}

type dialogActionDelegate struct {
	state AppState
}

func (dialogActionDelegate) dialogAction() {}

// StackableDialog is the common focus and overlay boundary for modal layers.
type StackableDialog interface {
	DialogID() string
	HandleDialogKey(msg tea.KeyPressMsg) DialogAction
	RenderOverlay(base string, width, height int) string
}

// DialogStack owns modal ordering. Only Front receives keyboard input while
// rendering applies layers back-to-front.
type DialogStack struct {
	stack     []StackableDialog
	graceTime time.Time
}

func (ds *DialogStack) Push(dialog StackableDialog) {
	ds.stack = append(ds.stack, dialog)
}

func (ds *DialogStack) PushWithGrace(dialog StackableDialog) {
	ds.stack = append(ds.stack, dialog)
	ds.graceTime = time.Now().Add(200 * time.Millisecond)
}

func (ds *DialogStack) Pop() {
	if len(ds.stack) > 0 {
		ds.stack = ds.stack[:len(ds.stack)-1]
	}
}

func (ds *DialogStack) PopByID(id string) {
	for i, dialog := range ds.stack {
		if dialog.DialogID() == id {
			ds.stack = append(ds.stack[:i], ds.stack[i+1:]...)
			return
		}
	}
}

func (ds *DialogStack) Front() StackableDialog {
	if len(ds.stack) == 0 {
		return nil
	}
	return ds.stack[len(ds.stack)-1]
}

func (ds *DialogStack) IsEmpty() bool { return len(ds.stack) == 0 }

func (ds *DialogStack) Contains(id string) bool {
	for _, dialog := range ds.stack {
		if dialog.DialogID() == id {
			return true
		}
	}
	return false
}

func (ds *DialogStack) HandleKey(msg tea.KeyPressMsg) (DialogAction, bool) {
	if len(ds.stack) == 0 {
		return nil, false
	}
	if !ds.graceTime.IsZero() && time.Now().Before(ds.graceTime) {
		return DialogActionNone{}, true
	}
	ds.graceTime = time.Time{}
	return ds.Front().HandleDialogKey(msg), true
}

func (ds *DialogStack) RenderOverlays(base string, width, height int) string {
	result := base
	for _, dialog := range ds.stack {
		result = dialog.RenderOverlay(result, width, height)
	}
	return result
}

func (ds *DialogStack) Len() int { return len(ds.stack) }

type dialogFrame struct {
	state       AppState
	returnState AppState
}

type appDialogStack struct {
	DialogStack
	frames []dialogFrame
}

func (s *appDialogStack) topFrame() (dialogFrame, bool) {
	if s == nil || len(s.frames) == 0 {
		return dialogFrame{}, false
	}
	return s.frames[len(s.frames)-1], true
}

func (s *appDialogStack) pushFrame(app *App, frame dialogFrame) {
	s.frames = append(s.frames, frame)
	s.DialogStack.Push(&appDialogLayer{app: app, state: frame.state})
}

func (s *appDialogStack) popFrame() (dialogFrame, bool) {
	frame, ok := s.topFrame()
	if !ok {
		return dialogFrame{}, false
	}
	s.frames = s.frames[:len(s.frames)-1]
	s.DialogStack.Pop()
	return frame, true
}

type appDialogLayer struct {
	app   *App
	state AppState
}

func (d *appDialogLayer) DialogID() string { return dialogStateID(d.state) }

func (d *appDialogLayer) HandleDialogKey(tea.KeyPressMsg) DialogAction {
	return dialogActionDelegate{state: d.state}
}

func (d *appDialogLayer) RenderOverlay(base string, _, _ int) string {
	return d.app.renderDialogState(d.state, base)
}

func dialogStateID(state AppState) string {
	return "app-state-" + fmt.Sprint(int(state))
}

func isDialogState(state AppState) bool {
	switch state {
	case StatePermission, StateResume, StateHelp,
		StateBypassConfirm, StateMCPApproval, StateModelPicker,
		StateBackgroundTasks, StateMCPSettings, StateAgentWizard, StateTeams,
		StateCommandPalette, StatePlanApproval, StateAskUser, StateAgentPicker:
		return true
	default:
		return false
	}
}

func (a *App) pushDialog(state AppState) {
	if a == nil || !isDialogState(state) {
		return
	}
	if a.dialogs == nil {
		a.dialogs = &appDialogStack{}
	}
	if top, ok := a.dialogs.topFrame(); ok && top.state == state {
		a.state = state
		return
	}
	returnState := a.state
	if isDialogState(returnState) {
		if _, ok := a.dialogs.topFrame(); !ok {
			returnState = StateChat
		}
	}
	a.dialogs.pushFrame(a, dialogFrame{state: state, returnState: returnState})
	a.state = state
}

func (a *App) popDialog(expected AppState) bool {
	if a == nil {
		return false
	}
	if a.dialogs != nil {
		if top, ok := a.dialogs.topFrame(); ok {
			if expected != top.state {
				return false
			}
			frame, _ := a.dialogs.popFrame()
			a.state = frame.returnState
			return true
		}
	}
	if a.state == expected && isDialogState(expected) {
		a.state = StateChat
		return true
	}
	return false
}

func (a *App) removeDialog(expected AppState) bool {
	if a == nil || a.dialogs == nil {
		return a.popDialog(expected)
	}
	for i := len(a.dialogs.frames) - 1; i >= 0; i-- {
		frame := a.dialogs.frames[i]
		if frame.state != expected {
			continue
		}
		wasTop := i == len(a.dialogs.frames)-1
		if i+1 < len(a.dialogs.frames) {
			a.dialogs.frames[i+1].returnState = frame.returnState
		}
		a.dialogs.frames = append(a.dialogs.frames[:i], a.dialogs.frames[i+1:]...)
		a.dialogs.DialogStack.stack = append(
			a.dialogs.DialogStack.stack[:i], a.dialogs.DialogStack.stack[i+1:]...,
		)
		if wasTop {
			a.state = frame.returnState
		}
		return true
	}
	return a.popDialog(expected)
}

func (a *App) activeDialogState() (AppState, bool) {
	if a != nil && a.dialogs != nil {
		if top, ok := a.dialogs.topFrame(); ok {
			return top.state, true
		}
	}
	if a != nil && isDialogState(a.state) {
		return a.state, true
	}
	return StateChat, false
}

func (a *App) hasDialog(state AppState) bool {
	if a != nil && a.dialogs != nil {
		for _, frame := range a.dialogs.frames {
			if frame.state == state {
				return true
			}
		}
	}
	return a != nil && a.state == state && isDialogState(state)
}

func (a *App) baseState() AppState {
	if a != nil && a.dialogs != nil && len(a.dialogs.frames) > 0 {
		return a.dialogs.frames[0].returnState
	}
	if a != nil && !isDialogState(a.state) {
		return a.state
	}
	return StateChat
}

func (a *App) renderActiveDialog(base string) string {
	if a != nil && a.dialogs != nil && len(a.dialogs.frames) > 0 {
		return a.dialogs.DialogStack.RenderOverlays(
			base, a.layout.overlayRect.Width, a.layout.overlayRect.Height,
		)
	}
	state, ok := a.activeDialogState()
	if !ok {
		return base
	}
	return a.renderDialogState(state, base)
}

func (a *App) renderDialogState(state AppState, base string) string {
	switch state {
	case StatePermission:
		return a.dialog.Overlay(base, a.layout.overlayRect.Width, a.layout.overlayRect.Height)
	case StateResume:
		return a.resume.Overlay(base, a.layout.overlayRect.Width, a.layout.overlayRect.Height)
	case StateHelp:
		return a.help.Overlay(base, a.layout.overlayRect.Width, a.layout.overlayRect.Height)
	case StateBypassConfirm:
		return a.bypassConfirmOverlay(base)
	case StateMCPApproval:
		return a.mcpApproval.Overlay(base, a.layout.overlayRect.Width, a.layout.overlayRect.Height)
	case StateModelPicker:
		return a.modelPicker.Overlay(base, a.layout.overlayRect.Width, a.layout.overlayRect.Height)
	case StateBackgroundTasks:
		return a.backgroundTasks.Overlay(base, a.layout.overlayRect.Width, a.layout.overlayRect.Height)
	case StateAgentWizard:
		return a.agentWizard.Overlay(base, a.layout.overlayRect.Width, a.layout.overlayRect.Height)
	case StateMCPSettings:
		return a.mcpSettings.Overlay(base, a.layout.overlayRect.Width, a.layout.overlayRect.Height)
	case StateTeams:
		return a.teamsPanel.Overlay(base, a.layout.overlayRect.Width, a.layout.overlayRect.Height)
	case StateCommandPalette:
		return a.commandPalette.Overlay(base, a.layout.overlayRect.Width, a.layout.overlayRect.Height)
	case StatePlanApproval:
		return a.planDialog.Overlay(base, a.layout.overlayRect.Width, a.layout.overlayRect.Height)
	case StateAskUser:
		return a.questionDialog.Overlay(base, a.layout.overlayRect.Width, a.layout.overlayRect.Height)
	case StateAgentPicker:
		return a.agentPicker.Overlay(base, a.layout.overlayRect.Width, a.layout.overlayRect.Height)
	default:
		return base
	}
}

func (a *App) handleActiveDialogKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	state, ok := a.activeDialogState()
	if !ok {
		return false, nil
	}
	if a.dialogs != nil && len(a.dialogs.frames) > 0 {
		action, consumed := a.dialogs.DialogStack.HandleKey(msg)
		if !consumed {
			return false, nil
		}
		switch action := action.(type) {
		case DialogActionNone:
			return true, nil
		case DialogActionClose:
			a.popDialog(state)
			return true, nil
		case DialogActionResult:
			a.popDialog(state)
			return true, nil
		case dialogActionDelegate:
			state = action.state
		default:
			return true, nil
		}
	}
	switch state {
	case StatePermission:
		done, cmd := a.dialog.HandleKey(msg)
		if done {
			a.captureActiveThreadAttentionResponseData()
			a.popDialog(state)
			if feedback := a.dialog.Feedback(); feedback != "" {
				a.chat.AppendSystem("User feedback: " + feedback)
			}
		}
		return true, cmd
	case StateMCPApproval:
		done, cmd := a.mcpApproval.HandleKey(msg)
		if done {
			a.popDialog(state)
		}
		return true, cmd
	case StatePlanApproval:
		done, cmd := a.planDialog.HandleKey(msg)
		if a.planDialog.EditorActive() {
			a.externalEditorActive = true
		}
		if done {
			a.captureActiveThreadAttentionResponseData()
			a.popDialog(state)
			if feedback := a.planDialog.Feedback(); feedback != "" {
				a.chat.AppendSystem("User feedback on plan: " + feedback)
			}
		}
		return true, cmd
	case StateAskUser:
		done, cmd := a.questionDialog.HandleKey(msg)
		if done {
			a.captureActiveThreadAttentionResponseData()
			a.popDialog(state)
		}
		return true, cmd
	case StateResume:
		selection, done, cmd := a.resume.handleKey(msg)
		if done {
			a.popDialog(state)
			if selection.valid() {
				return true, a.applySessionPickerSelection(selection)
			}
		}
		return true, cmd
	case StateHelp:
		if a.help.HandleKey(msg, a.height) {
			a.popDialog(state)
		}
		return true, nil
	case StateModelPicker:
		selectedModel, dismissed := a.modelPicker.HandleKey(msg)
		if dismissed {
			a.popDialog(state)
			if selectedModel != "" {
				a.applyModelSelection(selectedModel)
			}
		}
		return true, nil
	case StateAgentWizard:
		result, dismissed := a.agentWizard.HandleKey(msg)
		if dismissed {
			a.popDialog(state)
			if result != nil {
				a.saveAgentFromWizard(result)
			}
		}
		return true, nil
	case StateAgentPicker:
		threadID, selected, dismissed, cmd := a.agentPicker.HandleKey(msg)
		if dismissed {
			a.popDialog(state)
		}
		if selected {
			pageCmd, err := a.activateThreadByIDWithCmd(threadID)
			if err != nil {
				a.showNotification(err.Error(), NotifyError)
			}
			return true, tea.Batch(cmd, pageCmd, a.ensureSpinnerTick())
		}
		return true, cmd
	case StateBackgroundTasks:
		return true, a.handleBackgroundTasksDialogKey(msg)
	case StateTeams:
		return true, a.handleTeamsDialogKey(msg)
	case StateMCPSettings:
		dismissed, _ := a.mcpSettings.HandleKey(msg, a.height)
		if dismissed {
			a.popDialog(state)
		}
		return true, nil
	case StateCommandPalette:
		selected, dismissed := a.commandPalette.HandleKey(msg)
		if dismissed {
			a.popDialog(state)
			if selected != "" {
				// Re-admit after a possibly stale palette snapshot before
				// creating presentation provenance. Recent commits only after
				// the matching local or engine-owned result succeeds.
				if a.commandRegistry != nil {
					if cmd := a.commandRegistry.GetForContext(context.Background(), commands.EntrypointTUI, a.commandCapabilityContext(), selected); cmd != nil && cmd.ValidateArgs(nil) == nil {
						a.beginCommandPaletteSubmission(cmd.Name)
					}
				}
				a.textarea.SetValue("/" + selected)
				a.composerElements = nil
				a.inputMode = InputCommand
				return true, a.sendMessage()
			}
		}
		return true, nil
	case StateBypassConfirm:
		return true, a.handleBypassConfirmKey(msg)
	default:
		return false, nil
	}
}

func (a *App) handleBackgroundTasksDialogKey(msg tea.KeyPressMsg) tea.Cmd {
	wasDetail := a.backgroundTasks.subView == bgTaskViewOutput && a.backgroundTasks.detailAgent != ""
	previousTab := a.backgroundTasks.detailTab
	dismissed, cmd := a.backgroundTasks.HandleKeyWithCmd(msg, a.height)
	isDetail := a.backgroundTasks.subView == bgTaskViewOutput && a.backgroundTasks.detailAgent != ""
	switch {
	case wasDetail && isDetail:
		a.threadDetailTab = a.backgroundTasks.detailTab
	case wasDetail:
		a.threadDetailTab = previousTab
	case isDetail:
		a.backgroundTasks.detailTab = a.threadDetailTab
		a.backgroundTasks.rebuildAgentDetailLines()
	}
	if dismissed {
		a.popDialog(StateBackgroundTasks)
	}
	if a.syncAgentToolTraces() {
		return tea.Batch(cmd, a.ensureSpinnerTick())
	}
	return cmd
}

func (a *App) handleTeamsDialogKey(msg tea.KeyPressMsg) tea.Cmd {
	dismissed, cmd := a.teamsPanel.HandleKeyWithCmd(msg, a.height)
	if dismissed {
		a.popDialog(StateTeams)
	}
	if threadID := a.teamsPanel.takeSwitchThread(); threadID != "" {
		pageCmd, err := a.activateThreadByIDWithCmd(threadID)
		if err != nil {
			a.showNotification(err.Error(), NotifyError)
		}
		return tea.Batch(cmd, pageCmd, a.ensureSpinnerTick())
	}
	if a.syncAgentToolTraces() {
		return tea.Batch(cmd, a.ensureSpinnerTick())
	}
	return cmd
}

// closeActiveDialog handles Ctrl+C. Runtime interaction dialogs continue into
// the normal interrupt path after responding; passive overlays stop here.
func (a *App) closeActiveDialog() (handled, continueInterrupt bool) {
	state, ok := a.activeDialogState()
	if !ok {
		return false, false
	}
	switch state {
	case StatePermission:
		a.dialog.ForceClose()
		continueInterrupt = true
	case StateMCPApproval:
		a.mcpApproval.ForceClose()
		continueInterrupt = true
	case StatePlanApproval:
		a.planDialog.ForceClose()
		continueInterrupt = true
	case StateAskUser:
		a.questionDialog.ForceClose()
		continueInterrupt = true
	case StateResume:
		a.resume.Close()
	case StateHelp:
		a.help.Close()
	case StateModelPicker:
		a.modelPicker.Close()
	case StateBackgroundTasks:
		a.backgroundTasks.Close()
	case StateAgentWizard:
		a.agentWizard.Close()
	case StateMCPSettings:
		a.mcpSettings.Close()
	case StateTeams:
		a.teamsPanel.Close()
	case StateCommandPalette:
		a.commandPalette.Close()
	case StateAgentPicker:
		a.agentPicker.Close()
	case StateBypassConfirm:
	}
	if state == StatePermission || state == StatePlanApproval || state == StateAskUser {
		a.captureActiveThreadAttentionResponseData()
	}
	a.popDialog(state)
	return true, continueInterrupt
}
