package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/commands"
)

type sideQuestionRequest struct {
	engine   *engine.QueryEngine
	id, view string
	cancel   context.CancelFunc
	palette  *commandPaletteSubmission
}

type sideQuestionResultMsg struct {
	request *sideQuestionRequest
}

// Opening and editing the question do not call a model or alter the main turn.
func (a *App) openSideQuestion(question string, palette *commandPaletteSubmission) tea.Cmd {
	a.dismissSideQuestion()
	a.dismissComposerSuggestion()
	a.sideQuestionDialog.Show(question)
	a.sideQuestionPalette = palette
	a.pushDialog(StateSideQuestion)
	if strings.TrimSpace(question) != "" {
		return a.askSideQuestion(question)
	}
	return nil
}

func (a *App) askSideQuestion(question string) tea.Cmd {
	if a.engine == nil || !a.isLeaderThreadView() {
		a.sideQuestionDialog.SetFeedback("Side questions require the leader conversation.")
		return nil
	}
	snapshot, err := a.engine.BeginSideQuestion(question)
	if err != nil {
		a.sideQuestionDialog.SetFeedback(err.Error())
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	request := &sideQuestionRequest{engine: a.engine, id: snapshot.ID, view: a.activeThreadViewID(), cancel: cancel, palette: a.sideQuestionPalette}
	a.sideQuestionRequest = request
	a.sideQuestionDialog.SetSnapshot(snapshot)
	return func() tea.Msg {
		_, _ = request.engine.RunSideQuestion(ctx, request.id)
		return sideQuestionResultMsg{request: request}
	}
}

func (a *App) handleSideQuestionResult(msg sideQuestionResultMsg) {
	request := a.sideQuestionRequest
	if request == nil || request != msg.request {
		return
	}
	request.cancel()
	a.sideQuestionRequest = nil
	if request.engine != a.engine || request.view != a.activeThreadViewID() || !a.hasDialog(StateSideQuestion) {
		return
	}
	snapshot, ok := request.engine.SideQuestion(request.id)
	if !ok {
		a.dismissSideQuestion()
		return
	}
	a.sideQuestionDialog.SetSnapshot(snapshot)
	a.sideQuestionAnswerID = snapshot.ID
	if snapshot.Status == engine.SideQuestionAnswered {
		a.commitCommandPaletteLocalSubmission(request.palette)
		a.sideQuestionPalette = nil
	}
}

func (a *App) handleSideQuestionKey(msg tea.KeyPressMsg) tea.Cmd {
	action, question := a.sideQuestionDialog.HandleKey(msg)
	switch action {
	case sideQuestionDialogAsk:
		return a.askSideQuestion(question)
	case sideQuestionDialogCancel:
		a.dismissSideQuestion()
	case sideQuestionDialogStage:
		if a.inputMode != InputNormal && strings.TrimSpace(a.textarea.Value()) != "" {
			a.sideQuestionDialog.SetFeedback("Use a normal message draft before adding the answer.")
			return nil
		}
		// The component's question is presentation only. Stage the engine's final
		// snapshot so a stale widget can never supply or reinterpret model text.
		snapshot, ok := a.engine.SideQuestion(a.sideQuestionAnswerID)
		if !ok || snapshot.Status != engine.SideQuestionAnswered {
			a.sideQuestionDialog.SetFeedback("The answer is no longer available; ask again.")
			return nil
		}
		body := fmt.Sprintf("Side question context (captured %s):\nQuestion: %s\nAnswer: %s", snapshot.StartedAt.Format("15:04:05"), snapshot.Question, snapshot.Answer)
		before := a.composerRevision
		a.popDialog(StateSideQuestion)
		prefix := ""
		if a.textarea.Value() != "" {
			prefix = "\n\n"
		}
		a.textarea.CursorEnd()
		cmd := a.handleComposerPaste(tea.PasteMsg{Content: prefix + body})
		if before == a.composerRevision {
			a.pushDialog(StateSideQuestion)
			a.sideQuestionDialog.SetFeedback("The draft could not accept this answer; free space and retry.")
			return nil
		}
		a.sideQuestionDialog.Close()
		a.sideQuestionPalette = nil
		a.sideQuestionAnswerID = ""
		return cmd
	}
	return nil
}

func (a *App) dismissSideQuestion() {
	if a == nil {
		return
	}
	if request := a.sideQuestionRequest; request != nil {
		request.engine.CancelSideQuestion(request.id)
		request.cancel()
		a.sideQuestionRequest = nil
	}
	a.sideQuestionPalette = nil
	a.sideQuestionAnswerID = ""
	if a.sideQuestionDialog != nil {
		a.sideQuestionDialog.Close()
	}
	a.removeDialog(StateSideQuestion)
}

func (a *App) dispatchSideQuestion(value string, cmdCtx *commands.CommandContext, palette *commandPaletteSubmission, preserveDraft bool) tea.Cmd {
	result, err := a.commandRegistry.Dispatch(context.Background(), commands.EntrypointTUI, cmdCtx, value)
	if err != nil {
		a.showNotification(err.Error(), NotifyWarning)
		return nil
	}
	if result == nil || result.Action != commands.ActionSideQuestion {
		reason := "Side question is unavailable."
		if result != nil && result.Output != "" {
			reason = result.Output
		}
		a.showNotification(reason, NotifyWarning)
		return nil
	}
	if !preserveDraft {
		a.clearInputAfterSubmit("")
	}
	question, _ := result.Data["question"].(string)
	return a.openSideQuestion(question, palette)
}
