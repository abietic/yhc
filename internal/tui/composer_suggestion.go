package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/services"
)

type composerSuggestionState struct {
	Text       string
	Feedback   string
	ThreadID   string
	Revision   uint64
	QueryID    uint64
	Generation uint64
}

type composerSuggestionRequest struct {
	Engine     *engine.QueryEngine
	ThreadID   string
	Revision   uint64
	QueryID    uint64
	Generation uint64
	Cancel     context.CancelFunc
	Manual     bool
}

type composerSuggestionMsg struct {
	Engine     *engine.QueryEngine
	ThreadID   string
	Revision   uint64
	QueryID    uint64
	Generation uint64
	Result     services.PromptSuggestionResult
}

func (a *App) composerSuggestionInputEligible() bool {
	if a == nil || a.state != StateChat || a.running || !a.isLeaderThreadView() ||
		a.inputMode != InputNormal || a.textarea.Value() != "" ||
		a.composerInputBlocked() || a.composerImageLoadPending != nil ||
		a.externalEditorActive || a.historySearch.Active ||
		a.suppressingHistoryHints() || !a.chat.Following() {
		return false
	}
	return len(a.commandHints) == 0 && len(a.fileHints) == 0 &&
		len(a.mentionHints) == 0
}

func (a *App) composerSuggestionEligible() bool {
	return a.composerSuggestionInputEligible() && a.permissionMode() != permission.ModePlan
}

func (a *App) requestComposerSuggestion() tea.Cmd {
	return a.startComposerSuggestion(false)
}

func (a *App) requestManualComposerSuggestion() tea.Cmd {
	if !a.composerSuggestionInputEligible() || a.engine == nil {
		a.showNotification("Request a next-step suggestion from the idle leader conversation with an empty input.", NotifyInfo)
		return nil
	}
	return a.startComposerSuggestion(true)
}

func (a *App) startComposerSuggestion(manual bool) tea.Cmd {
	if a == nil || a.engine == nil || !a.composerSuggestionInputEligible() ||
		(!manual && !a.composerSuggestionEligible()) {
		return nil
	}
	if request := a.composerSuggestionRequest; request != nil {
		if manual {
			request.Manual = true
			a.composerSuggestion.Feedback = "Thinking of a next step... Esc to cancel"
			a.updateLayout()
		}
		return nil
	}
	a.dismissComposerSuggestion()
	a.composerSuggestionSerial++
	if a.composerSuggestionSerial == 0 {
		a.composerSuggestionSerial++
	}
	ctx, cancel := context.WithCancel(context.Background())
	request := &composerSuggestionRequest{
		Engine: a.engine, ThreadID: a.activeThreadViewID(),
		Revision: a.composerRevision, QueryID: a.queryID,
		Generation: a.composerSuggestionSerial, Cancel: cancel, Manual: manual,
	}
	a.composerSuggestionRequest = request
	a.composerSuggestion = composerSuggestionState{
		ThreadID: request.ThreadID, Revision: request.Revision,
		QueryID: request.QueryID, Generation: request.Generation,
	}
	if manual {
		a.composerSuggestion.Feedback = "Thinking of a next step... Esc to cancel"
	}
	a.updateLayout()
	return func() tea.Msg {
		result, _ := request.Engine.GeneratePromptSuggestionResult(ctx)
		return composerSuggestionMsg{
			Engine: request.Engine, ThreadID: request.ThreadID, Revision: request.Revision,
			QueryID: request.QueryID, Generation: request.Generation, Result: result,
		}
	}
}

func (a *App) handleComposerSuggestion(msg composerSuggestionMsg) {
	if a == nil {
		return
	}
	request := a.composerSuggestionRequest
	if request == nil || request.Engine != msg.Engine ||
		request.ThreadID != msg.ThreadID || request.Revision != msg.Revision ||
		request.QueryID != msg.QueryID || request.Generation != msg.Generation {
		return
	}
	if request.Cancel != nil {
		request.Cancel()
	}
	a.composerSuggestionRequest = nil
	if !a.composerSuggestionInputEligible() ||
		msg.Engine != a.engine || msg.ThreadID != a.activeThreadViewID() ||
		msg.Revision != a.composerRevision || msg.QueryID != a.queryID {
		return
	}
	result := msg.Result
	suggestion := ""
	if result.Status == services.PromptSuggestionAvailable {
		switch {
		case !a.composerSuggestionEligible():
			result = services.PromptSuggestionResult{Status: services.PromptSuggestionSuppressed, Reason: "plan_mode"}
		case services.FilterSuggestion(strings.TrimSpace(result.Suggestion)) != "":
			result = services.PromptSuggestionResult{Status: services.PromptSuggestionFiltered, Reason: "invalid_text"}
		default:
			suggestion = strings.TrimSpace(result.Suggestion)
		}
	}
	feedback := ""
	if request.Manual {
		feedback = composerSuggestionFeedback(result)
	}
	a.composerSuggestion = composerSuggestionState{
		Text: suggestion, Feedback: feedback,
		ThreadID: msg.ThreadID, Revision: msg.Revision,
		QueryID: msg.QueryID, Generation: msg.Generation,
	}
}

func (a *App) visibleComposerSuggestion() string {
	if a == nil || !a.composerSuggestionEligible() {
		return ""
	}
	suggestion := a.composerSuggestion
	if suggestion.Text == "" || suggestion.ThreadID != a.activeThreadViewID() ||
		suggestion.Revision != a.composerRevision ||
		suggestion.QueryID != a.queryID {
		return ""
	}
	return suggestion.Text
}

func (a *App) hasComposerSuggestionActivity() bool {
	return a != nil && (a.composerSuggestionRequest != nil ||
		a.composerSuggestion.Text != "" || a.composerSuggestion.Feedback != "")
}

func (a *App) dismissComposerSuggestion() {
	if a == nil {
		return
	}
	if request := a.composerSuggestionRequest; request != nil && request.Cancel != nil {
		request.Cancel()
	}
	a.composerSuggestionRequest = nil
	a.composerSuggestion = composerSuggestionState{}
}

func (a *App) acceptComposerSuggestion() tea.Cmd {
	suggestion := a.visibleComposerSuggestion()
	if suggestion == "" {
		return nil
	}
	before := a.captureComposerUndoEntry()
	a.dismissComposerSuggestion()
	a.textarea.SetValue(suggestion)
	a.reconcileComposerElements(before.Text, a.textarea.Value())
	a.markComposerChanged()
	a.recordComposerUndo(before)
	a.syncInputModeFromText()
	return a.ensureMentionIndex()
}

func (a *App) renderComposerSuggestionFeedback() string {
	state := a.composerSuggestion
	if state.Feedback == "" || !a.composerSuggestionInputEligible() ||
		state.ThreadID != a.activeThreadViewID() || state.Revision != a.composerRevision ||
		state.QueryID != a.queryID {
		return ""
	}
	profile := a.renderEnvironment.normalized().profile
	lines := profile.wrap(state.Feedback, max(a.layout.width-6, 1), false)
	for i := range lines {
		lines[i] = a.styles.Subtle.Render(" " + lines[i])
	}
	return strings.Join(lines, "\n")
}

func composerSuggestionFeedback(result services.PromptSuggestionResult) string {
	switch result.Status {
	case services.PromptSuggestionAvailable:
		return "Next-step suggestion ready. Tab or Right to use; Enter dismisses."
	case services.PromptSuggestionSuppressed:
		switch result.Reason {
		case "disabled":
			return "Next-step suggestions are disabled. Enable prompt_suggestions and start a new session."
		case "no_context":
			return "No conversation context yet. Send a message first."
		case "early_conversation":
			return "Next-step suggestions need at least two assistant messages."
		case "plan_mode":
			return "Next-step suggestions are paused in Plan mode."
		case "pending_permission":
			return "Answer the pending permission request before asking for a suggestion."
		case "goal_active":
			return "Next-step suggestions are paused while a Goal is active."
		case "busy":
			return "Wait for the current turn to finish before asking for a suggestion."
		case "in_flight":
			return "A suggestion request is still finishing. Try again shortly."
		default:
			return "Next-step suggestions are unavailable in this session."
		}
	case services.PromptSuggestionEmpty:
		return "No next step suggested. You can write your own."
	case services.PromptSuggestionFiltered:
		return "No suitable next-step suggestion was returned."
	case services.PromptSuggestionTimedOut:
		return "The suggestion request timed out. Try again."
	case services.PromptSuggestionCancelled:
		return "The suggestion request was cancelled."
	case services.PromptSuggestionStale:
		return "The suggestion was discarded because the model route changed. Try again."
	default:
		return "The suggestion request failed. Try again."
	}
}
