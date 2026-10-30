package tui

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
)

type inputDeliveryChoice struct {
	engine   *engine.QueryEngine
	threadID string
	revision uint64
	queryID  uint64
	mode     engine.PromptDeliveryMode
}

// Receipts are bounded, non-dispatchable projections. Ledger/transcript reads
// own pending, claimed and delivered; edit/withdraw require exact engine ACKs.
type inputDeliveryStart struct {
	engine   *engine.QueryEngine
	threadID string
	id       string
}

type inputDeliveryReceipt struct {
	threadID string
	engine   *engine.QueryEngine
	item     threadQueuedInput
	status   string
	action   string
	promoted bool
}

func promptDeliveryLabel(mode engine.PromptDeliveryMode) string {
	switch mode {
	case engine.PromptDeliverySteer:
		return "steer now"
	case engine.PromptDeliveryNextTurn:
		return "next turn"
	default:
		return "automatic"
	}
}

func (a *App) openInputDeliveryChoice() {
	a.dismissComposerSuggestion()
	a.inputDeliveryChoice = inputDeliveryChoice{
		engine: a.engine, threadID: a.activeThreadViewID(),
		revision: a.composerRevision, queryID: a.queryID, mode: engine.PromptDeliveryNextTurn,
	}
	a.pushDialog(StateInputDelivery)
}

func (a *App) handleInputDeliveryKey(msg tea.KeyPressMsg) tea.Cmd {
	if msg.String() == "esc" || msg.String() == "ctrl+c" {
		a.popDialog(StateInputDelivery)
		return nil
	}
	c := a.inputDeliveryChoice
	if c.engine != a.engine || c.threadID != a.activeThreadViewID() || c.revision != a.composerRevision || c.queryID != a.queryID {
		a.removeDialog(StateInputDelivery)
		a.showNotification("Draft changed; choose delivery again", NotifyWarning)
		return nil
	}
	switch msg.String() {
	case "tab", "shift+tab", "up", "down", "left", "right":
		if c.mode == engine.PromptDeliveryNextTurn {
			a.inputDeliveryChoice.mode = engine.PromptDeliverySteer
		} else {
			a.inputDeliveryChoice.mode = engine.PromptDeliveryNextTurn
		}
	case "enter":
		snapshot, err := a.captureComposerSubmission()
		if err != nil {
			a.showNotification(err.Error(), NotifyWarning)
			return nil
		}
		a.popDialog(StateInputDelivery)
		// Even if the active task just finished, preserve FIFO via idle claim.
		return a.beginComposerAdmissionWithDelivery(snapshot, true, c.mode)
	}
	return nil
}

func (a *App) rememberInputDelivery(item threadQueuedInput) {
	item = cloneThreadQueuedInputs([]threadQueuedInput{item})[0]
	for i := range a.inputDeliveryReceipts {
		r := &a.inputDeliveryReceipts[i]
		if r.current(a) && r.item.ID == item.ID {
			r.item = item
			return
		}
	}
	a.inputDeliveryReceipts = append(a.inputDeliveryReceipts, inputDeliveryReceipt{
		engine: a.engine, threadID: a.leaderThreadViewID(), item: item, status: string(item.State),
	})
	if len(a.inputDeliveryReceipts) > maxThreadQueuedInputPreviews {
		drop := 0
		for i, r := range a.inputDeliveryReceipts {
			if !r.current(a) || r.status != string(engine.PromptDeliveryPending) {
				drop = i
				break
			}
		}
		a.inputDeliveryReceipts = append(a.inputDeliveryReceipts[:drop], a.inputDeliveryReceipts[drop+1:]...)
	}
}

func (a *App) refreshInputDeliveryReceipts() {
	if a.engine == nil {
		return
	}
	ids := make([]string, 0, len(a.inputDeliveryReceipts))
	for _, r := range a.inputDeliveryReceipts {
		if r.current(a) {
			ids = append(ids, r.item.ID)
		}
	}
	statuses, err := a.engine.QueuedPromptDeliveryStatuses(ids)
	for i := range a.inputDeliveryReceipts {
		r := &a.inputDeliveryReceipts[i]
		if !r.current(a) {
			continue
		}
		if err != nil {
			r.status = "unavailable"
			continue
		}
		status := statuses[r.item.ID]
		if status == engine.PromptDeliveryUnknown && r.action != "" {
			r.status = r.action
			continue
		}
		r.status = string(status)
		if status == engine.PromptDeliveryDelivered {
			a.promoteEarlyInputDelivery(r.item.ID)
		}
	}
}

func (a *App) markInputDeliveryAction(id, status string) {
	for i := range a.inputDeliveryReceipts {
		r := &a.inputDeliveryReceipts[i]
		if r.current(a) && r.item.ID == id {
			r.status = status
			r.action = status
		}
	}
}

func (a *App) markInputDeliveryPromoted(id string) {
	for i := range a.inputDeliveryReceipts {
		r := &a.inputDeliveryReceipts[i]
		if r.current(a) && r.item.ID == id {
			r.promoted = true
		}
	}
}

func (a *App) inputDeliveryAction(id string) string {
	for _, r := range a.inputDeliveryReceipts {
		if r.current(a) && r.item.ID == id {
			return r.status
		}
	}
	return ""
}

func (a *App) inputDeliveryWasPromoted(id string) bool {
	for _, r := range a.inputDeliveryReceipts {
		if r.current(a) && r.item.ID == id {
			return r.promoted
		}
	}
	return false
}

func (a *App) promoteEarlyInputDelivery(id string) {
	for i := range a.inputDeliveryReceipts {
		r := &a.inputDeliveryReceipts[i]
		if r.current(a) && r.item.ID == id && !r.promoted &&
			(slices.Contains(a.inputDeliveryStarts, inputDeliveryStart{engine: a.engine, threadID: a.leaderThreadViewID(), id: id}) || r.status == string(engine.PromptDeliveryDelivered)) {
			a.consumeQueuedPreview(a.leaderThreadViewID(), id)
			a.appendQueuedUserToThread(a.leaderThreadViewID(), r.item.Content, nil)
			r.promoted = true
			return
		}
	}
}

func (a *App) currentInputDeliveryReceipts() []inputDeliveryReceipt {
	var rows []inputDeliveryReceipt
	for _, r := range a.inputDeliveryReceipts {
		if r.current(a) {
			rows = append(rows, r)
		}
	}
	return rows
}

func (a *App) openInputQueue() {
	if !a.isLeaderThreadView() || a.engine == nil {
		a.showNotification("Use /queue list, edit or remove for an Agent thread", NotifyWarning)
		return
	}
	a.hydrateQueuedInputPreview()
	a.refreshInputDeliveryReceipts()
	a.inputQueueSelection = max(0, len(a.currentInputDeliveryReceipts())-1)
	a.pushDialog(StateInputQueue)
}

func (a *App) handleInputQueueKey(msg tea.KeyPressMsg) {
	if msg.String() == "esc" || msg.String() == "ctrl+c" {
		a.popDialog(StateInputQueue)
		return
	}
	if !a.isLeaderThreadView() {
		a.removeDialog(StateInputQueue)
		return
	}
	// Keep the selected identity stable while refreshing authoritative ownership.
	rows := a.currentInputDeliveryReceipts()
	selected := ""
	if a.inputQueueSelection >= 0 && a.inputQueueSelection < len(rows) {
		selected = rows[a.inputQueueSelection].item.ID
	}
	a.hydrateQueuedInputPreview()
	a.refreshInputDeliveryReceipts()
	rows = a.currentInputDeliveryReceipts()
	for i, r := range rows {
		if r.item.ID == selected {
			a.inputQueueSelection = i
		}
	}
	a.inputQueueSelection = max(0, min(a.inputQueueSelection, len(rows)-1))
	switch msg.String() {
	case "up", "k":
		a.inputQueueSelection = max(0, a.inputQueueSelection-1)
	case "down", "j":
		a.inputQueueSelection = min(max(0, len(rows)-1), a.inputQueueSelection+1)
	case "e", "d", "delete", "backspace":
		if len(rows) == 0 {
			return
		}
		r := rows[a.inputQueueSelection]
		if r.status != string(engine.PromptDeliveryPending) {
			a.showNotification("Only pending input can be edited or withdrawn", NotifyWarning)
			return
		}
		action := "remove"
		if msg.String() == "e" {
			action = "edit"
		}
		a.handleQueueSlashCommand("/queue " + action + " " + r.item.ID)
		if action == "edit" && a.inputDeliveryAction(r.item.ID) == "editing" {
			a.popDialog(StateInputQueue)
		}
		a.refreshInputDeliveryReceipts()
	}
}

func (a *App) inputDeliveryOverlay(base string, width, height int, queue bool) string {
	profile := a.renderEnvironment.normalized().profile
	inner := max(1, min(88, width-6))
	var lines, help []string
	if !queue {
		lines = []string{a.styles.DialogTitle.Render("Delivery: " + promptDeliveryLabel(a.inputDeliveryChoice.mode))}
		for _, option := range []struct {
			mode  engine.PromptDeliveryMode
			title string
		}{
			{engine.PromptDeliverySteer, "Steer now"},
			{engine.PromptDeliveryNextTurn, "Next turn"},
		} {
			prefix := "  "
			if option.mode == a.inputDeliveryChoice.mode {
				prefix = "> "
			}
			lines = append(lines, prefix+option.title)

		}
		if a.inputDeliveryChoice.mode == engine.PromptDeliverySteer {
			lines = append(lines, "Next safe point; running tools finish.")
		} else {
			lines = append(lines, "Wait until the current task ends.")
		}
		help = []string{"Tab/Arrows choose | Enter submit", "Esc keep draft"}
	} else {
		lines = []string{a.styles.DialogTitle.Render("Input deliveries")}
		help = []string{"Up/Down | e edit | d withdraw", "Pending only | Esc close"}
		rows := a.currentInputDeliveryReceipts()
		visible := max(1, (height-len(help)-3)/2)
		start := max(0, a.inputQueueSelection-visible+1)
		if len(rows) == 0 {
			lines = append(lines, "No input deliveries in this view")
		}
		for i := start; i < min(len(rows), start+visible); i++ {
			r := rows[i]
			prefix := "  "
			if i == a.inputQueueSelection {
				prefix = "> "
			}
			text := strings.Join(strings.Fields(composerContextLabel(r.item.Content)), " ")
			lines = append(lines, fmt.Sprintf("%s%s [%s / %s]", prefix, shortQueueID(r.item.ID), promptDeliveryLabel(r.item.Delivery), r.status), "  "+text)
		}
	}
	lines = lines[:min(len(lines), max(1, height-len(help)-2))]
	lines = append(lines, help...)
	for i := range lines {
		lines[i] = modalProjectLine(profile, lines[i], inner, 0)
	}
	dialog := contentRenderStyleWidth(profile, a.styles.DialogBorder, inner+2, strings.Join(lines, "\n"))
	view, _ := modalCenteredOverlay(profile, base, dialog, width, height)
	return view
}

// Start events may precede the asynchronous acceptance callback. Retain only
// bounded identities so the later ACK can project the exact accepted display.
func (a *App) recordInputDeliveryStarted(id string) {
	if id == "" || slices.Contains(a.inputDeliveryStarts, inputDeliveryStart{engine: a.engine, threadID: a.leaderThreadViewID(), id: id}) {
		return
	}
	a.inputDeliveryStarts = append(a.inputDeliveryStarts, inputDeliveryStart{engine: a.engine, threadID: a.leaderThreadViewID(), id: id})
	if len(a.inputDeliveryStarts) > maxThreadQueuedInputPreviews {
		a.inputDeliveryStarts = a.inputDeliveryStarts[len(a.inputDeliveryStarts)-maxThreadQueuedInputPreviews:]
	}
}

func (r inputDeliveryReceipt) current(a *App) bool {
	return r.engine == a.engine && r.threadID == a.leaderThreadViewID()
}
