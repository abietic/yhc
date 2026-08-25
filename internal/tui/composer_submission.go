package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
)

type composerAdmissionRequest struct {
	ID        uint64
	ThreadID  string
	Revision  uint64
	Busy      bool
	Display   string
	SafeText  string
	Elements  []threadComposerElement
	Cancel    context.CancelFunc
	Cancelled bool
}

type composerAdmissionSettledMsg struct {
	RequestID uint64
	ThreadID  string
	Revision  uint64
	Busy      bool
	Events    <-chan engine.QueryEvent
	Terminal  engine.Terminal
	Queued    engine.QueuedPromptSnapshot
	Err       error
}

func (a *App) composerInputBlocked() bool {
	return a != nil && a.composerAdmissionPending != nil
}

func (a *App) markComposerChanged() {
	if a == nil {
		return
	}
	a.dismissComposerSuggestion()
	a.composerRevision++
	if a.composerRevision == 0 {
		a.composerRevision++
	}
}

func (a *App) beginComposerAdmission(
	snapshot composerSubmissionSnapshot,
	busy bool,
) tea.Cmd {
	if a == nil || a.engine == nil {
		if a != nil {
			a.showNotification("Queue is unavailable without an engine", NotifyError)
		}
		return nil
	}
	if a.composerAdmissionPending != nil {
		a.showNotification("A composer submission is already being accepted", NotifyWarning)
		return nil
	}
	if a.composerImageLoadPending != nil {
		a.showNotification("Wait for the image attachment to finish loading", NotifyWarning)
		return nil
	}
	a.composerAdmissionSerial++
	if a.composerAdmissionSerial == 0 {
		a.composerAdmissionSerial++
	}
	ctx, cancel := context.WithCancel(context.Background())
	request := &composerAdmissionRequest{
		ID:       a.composerAdmissionSerial,
		ThreadID: a.activeThreadViewID(),
		Revision: a.composerRevision,
		Busy:     busy,
		Display:  snapshot.Display,
		SafeText: snapshot.SafeText,
		Elements: cloneThreadComposerElements(snapshot.Elements),
		Cancel:   cancel,
	}
	a.composerAdmissionPending = request
	eng := a.engine
	input := snapshot.Input
	display := snapshot.Display
	return func() tea.Msg {
		if busy {
			queued, err := eng.EnqueuePromptInput(ctx, display, input)
			return composerAdmissionSettledMsg{
				RequestID: request.ID,
				ThreadID:  request.ThreadID,
				Revision:  request.Revision,
				Busy:      true,
				Queued:    queued,
				Err:       err,
			}
		}
		events, terminal := eng.SubmitPromptInput(ctx, input)
		return composerAdmissionSettledMsg{
			RequestID: request.ID,
			ThreadID:  request.ThreadID,
			Revision:  request.Revision,
			Events:    events,
			Terminal:  terminal,
		}
	}
}

func (a *App) handleComposerAdmissionSettled(msg composerAdmissionSettledMsg) tea.Cmd {
	request := a.composerAdmissionPending
	if request == nil ||
		request.ID != msg.RequestID ||
		request.ThreadID != msg.ThreadID ||
		request.Revision != msg.Revision {
		if msg.Busy && msg.Queued.ID != "" && a.engine != nil {
			_, _ = a.engine.CancelQueuedPrompt(msg.Queued.ID)
		}
		return nil
	}
	a.composerAdmissionPending = nil
	if request.Cancel != nil && msg.Busy {
		request.Cancel()
	}
	if request.Cancelled {
		if msg.Busy && msg.Queued.ID != "" && a.engine != nil {
			cancelled, err := a.engine.CancelQueuedPrompt(msg.Queued.ID)
			if err == nil && cancelled {
				return nil
			}
			request.Cancelled = false
			a.showNotification(
				"Queued input was accepted before cancellation completed",
				NotifyWarning,
			)
		} else {
			return nil
		}
	}
	if msg.Err != nil {
		request.Cancel()
		a.showNotification(composerAdmissionFailureMessage(msg.Err, msg.Busy), NotifyError)
		return nil
	}
	if !msg.Busy && (msg.Terminal.Err != nil || msg.Terminal.Reason != "") {
		request.Cancel()
		message := "Prompt was not accepted"
		if msg.Terminal.Err != nil {
			message = composerAdmissionFailureMessage(msg.Terminal.Err, false)
		} else if msg.Terminal.Reason != "" {
			message = fmt.Sprintf("Prompt was not accepted: %s", msg.Terminal.Reason)
		}
		a.showNotification(message, NotifyError)
		return nil
	}
	if !msg.Busy && msg.Events == nil {
		request.Cancel()
		a.showNotification("Prompt was not accepted: engine returned no event stream", NotifyError)
		return nil
	}
	if request.ThreadID != a.activeThreadViewID() || request.Revision != a.composerRevision {
		if msg.Busy && msg.Queued.ID != "" && a.engine != nil {
			_, _ = a.engine.CancelQueuedPrompt(msg.Queued.ID)
		}
		request.Cancel()
		return nil
	}

	if msg.Busy {
		a.queuedInputPreview = cloneThreadQueuedInputs(append(
			a.queuedInputPreview,
			threadQueuedInputFromSnapshot(msg.Queued),
		))
		a.clearAcceptedComposer(request)
		a.showToast("Input queued")
		return a.scheduleSessionViewSave()
	}

	if a.state == StateWelcome {
		a.state = StateChat
		a.stopMascotIdle()
	}
	a.chat.ResetFollow()
	a.chat.AppendUserWithComposer(request.Display, request.Elements)
	a.clearAcceptedComposer(request)
	a.startAcceptedComposerQuery(msg.Events, request.Cancel)
	return tea.Batch(a.waitForEvent(), a.ensureSpinnerTick(), a.scheduleSessionViewSave())
}

func composerAdmissionFailureMessage(err error, busy bool) string {
	var validationErr *engine.PromptInputValidationError
	if errors.As(err, &validationErr) {
		return validationErr.Error()
	}
	var admissionErr *engine.PromptInputAdmissionError
	if errors.As(err, &admissionErr) {
		return admissionErr.Error()
	}
	if busy {
		return "Queued input could not be accepted"
	}
	return "Prompt could not be accepted"
}

func (a *App) startAcceptedComposerQuery(
	events <-chan engine.QueryEvent,
	cancel context.CancelFunc,
) {
	a.dismissComposerSuggestion()
	a.composerSuggestionTurnSeen = false
	a.commandPaletteSubmission = nil
	a.running = true
	a.spinnerCount = 0
	a.streamingCtx.Reset()
	a.thinkingInd.Stop()
	a.toolProgress.Reset()
	a.spinnerState = SpinnerState{
		Mode:      SpinnerThinking,
		StartTime: time.Now(),
	}
	a.queryID++
	a.eventChan = events
	a.cancelFn = cancel
}

func (a *App) clearAcceptedComposer(request *composerAdmissionRequest) {
	if a == nil || request == nil {
		return
	}
	a.clearInputAfterSubmit(request.SafeText)
	a.markComposerChanged()
	a.gcDraftMedia()
}

func (a *App) cancelComposerAdmission() bool {
	request := a.composerAdmissionPending
	if request == nil {
		return false
	}
	request.Cancelled = true
	if request.Cancel != nil {
		request.Cancel()
	}
	a.showToast("Composer submission cancelled; draft retained")
	return true
}

func clearBytes(data []byte) {
	for index := range data {
		data[index] = 0
	}
}

func (a *App) gcDraftMedia() {
	if a == nil || len(a.draftMedia) == 0 {
		return
	}
	reachable := make(map[string]struct{}, len(a.draftMedia))
	collectElements := func(elements []threadComposerElement) {
		for _, element := range elements {
			if element.Kind == composerElementKindImage && element.ID != "" {
				reachable[element.ID] = struct{}{}
			}
		}
	}
	collectUndo := func(entries []composerUndoEntry) {
		for _, entry := range entries {
			collectElements(entry.Elements)
		}
	}
	collectElements(a.composerElements)
	collectElements(a.draftElements)
	collectUndo(a.composerUndo)
	if a.threadViews != nil {
		activeID := a.activeThreadViewID()
		for threadID, view := range a.threadViews.views {
			if view == nil || threadID == activeID {
				continue
			}
			collectElements(view.ComposerElements)
			collectElements(view.Editor.HistoryDraftElements)
			collectUndo(view.Editor.Undo)
		}
	}
	for id, image := range a.draftMedia {
		if _, keep := reachable[id]; keep {
			continue
		}
		if image != nil {
			clearBytes(image.Data)
		}
		delete(a.draftMedia, id)
	}
}
