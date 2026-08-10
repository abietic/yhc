package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/internal/tui/attachments"
)

type (
	startQueuedInputMsg      struct{}
	startGoalContinuationMsg struct{}
)

type runtimeInputReadyMsg struct {
	engine *engine.QueryEngine
}

type goalContinuationReadyMsg struct {
	engine *engine.QueryEngine
}

func threadQueuedInputFromSnapshot(snapshot engine.QueuedPromptSnapshot) threadQueuedInput {
	return threadQueuedInput{
		ID:          snapshot.ID,
		Content:     snapshot.Display,
		Parts:       cloneQueuedPromptParts(snapshot.Parts),
		EnqueuedAt:  snapshot.EnqueuedAt,
		State:       snapshot.State,
		Unavailable: snapshot.Unavailable,
	}
}

func (a *App) scheduleNextQueuedInput() tea.Cmd {
	if a.engine == nil {
		return nil
	}
	for _, item := range a.engine.RuntimeItems() {
		if item.Kind != engine.RuntimeItemGoalContinuation {
			return func() tea.Msg { return startQueuedInputMsg{} }
		}
	}
	return nil
}

func (a *App) scheduleNextGoalContinuation() tea.Cmd {
	if a.engine == nil {
		return nil
	}
	for _, item := range a.engine.RuntimeItems() {
		if item.Kind == engine.RuntimeItemGoalContinuation {
			return func() tea.Msg { return startGoalContinuationMsg{} }
		}
	}
	return nil
}

func (a *App) scheduleNextRuntimeWork() tea.Cmd {
	if next := a.scheduleNextQueuedInput(); next != nil {
		return next
	}
	return a.scheduleNextGoalContinuation()
}

func (a *App) startNextGoalContinuation() tea.Cmd {
	if a.engine == nil || a.running {
		return nil
	}
	item, ok, err := a.engine.ClaimNextGoalContinuation()
	if err != nil {
		a.showNotification("Goal continuation was not claimed: "+err.Error(), NotifyError)
		return nil
	}
	if !ok {
		return nil
	}
	a.showToast("Continuing active Goal")
	return a.startEngineGoalContinuation(item)
}

func (a *App) waitForGoalContinuation() tea.Cmd {
	if a == nil || a.engine == nil {
		return nil
	}
	eng := a.engine
	ready := eng.SubscribeGoalContinuations()
	if ready == nil {
		return nil
	}
	return func() tea.Msg {
		<-ready
		return goalContinuationReadyMsg{engine: eng}
	}
}

func (a *App) startNextQueuedInput() tea.Cmd {
	if a.engine == nil || a.running {
		return nil
	}
	item, ok, err := a.engine.ClaimNextRuntimeItem()
	if err != nil {
		a.showNotification("Runtime input was not claimed: "+err.Error(), NotifyError)
		return nil
	}
	if !ok {
		return nil
	}
	if item.Kind == engine.RuntimeItemUserPrompt && item.UserPrompt != nil {
		preview, _ := a.consumeQueuedPreview(a.leaderThreadViewID(), item.ID)
		display := item.UserPrompt.Display
		if preview.ID != "" {
			display = preview.Content
		}
		a.appendQueuedUserToThread(a.leaderThreadViewID(), display, nil)
		a.showToast("Starting queued input")
	} else {
		a.showNotification(runtimeInputNotice(item), NotifyWarning)
	}
	return a.startEngineRuntimeItem(item)
}

func (a *App) waitForRuntimeInput() tea.Cmd {
	if a == nil || a.engine == nil {
		return nil
	}
	eng := a.engine
	ready := eng.SubscribeRuntimeItems()
	if ready == nil {
		return nil
	}
	return func() tea.Msg {
		<-ready
		return runtimeInputReadyMsg{engine: eng}
	}
}

func (a *App) hydrateQueuedInputPreview() {
	if a == nil || a.engine == nil {
		return
	}
	snapshots, err := a.engine.QueuedPromptInputs()
	if err != nil {
		a.queuedInputPreview = nil
		a.showNotification("Queued input projection is unavailable", NotifyWarning)
		return
	}
	hydrated := make([]threadQueuedInput, 0, len(snapshots))
	for _, queued := range snapshots {
		hydrated = append(hydrated, threadQueuedInputFromSnapshot(queued))
	}
	a.queuedInputPreview = hydrated
}

func runtimeInputNotice(item engine.RuntimeItem) string {
	switch item.Kind {
	case engine.RuntimeItemAgentNotification:
		return "Background agent completed; continuing the model"
	case engine.RuntimeItemAgentMessage:
		return "Agent message received; continuing the model"
	case engine.RuntimeItemAsyncRewake:
		return "Background hook requested model attention"
	case engine.RuntimeItemPermissionDecision:
		return "Permission decision accepted; resuming the Graph"
	default:
		return "Runtime input received; continuing the model"
	}
}

func (a *App) consumeQueuedPreview(threadID, inputID string) (threadQueuedInput, bool) {
	if a == nil || strings.TrimSpace(inputID) == "" {
		return threadQueuedInput{}, false
	}
	if threadID == "" {
		threadID = a.activeThreadViewID()
	}
	if threadID == a.activeThreadViewID() {
		for i, queued := range a.queuedInputPreview {
			if queued.ID == inputID {
				a.queuedInputPreview = append(a.queuedInputPreview[:i], a.queuedInputPreview[i+1:]...)
				return queued, true
			}
		}
		return threadQueuedInput{}, false
	}
	if a.threadViews == nil || a.threadViews.views[threadID] == nil {
		return threadQueuedInput{}, false
	}
	view := a.threadViews.views[threadID]
	for i, queued := range view.QueuePreview {
		if queued.ID == inputID {
			view.QueuePreview = append(view.QueuePreview[:i], view.QueuePreview[i+1:]...)
			return queued, true
		}
	}
	return threadQueuedInput{}, false
}

func (a *App) appendQueuedUserToThread(threadID, content string, elements []threadComposerElement) {
	if strings.TrimSpace(content) == "" {
		return
	}
	if threadID == a.activeThreadViewID() {
		a.chat.AppendUserWithComposer(content, elements)
		return
	}
	if a.threadViews != nil {
		if view := a.threadViews.views[threadID]; view != nil && view.Chat != nil {
			view.Chat.AppendUserWithComposer(content, elements)
		}
	}
}

func (a *App) handleQueuedCommandStarted(threadID, inputID string) {
	preview, ok := a.consumeQueuedPreview(threadID, inputID)
	if !ok {
		return
	}
	a.appendQueuedUserToThread(threadID, preview.Content, nil)
}

func (a *App) renderQueuedInputRows() string {
	if len(a.queuedInputPreview) == 0 {
		return ""
	}
	const maxVisible = 3
	start := max(0, len(a.queuedInputPreview)-maxVisible)
	lines := make([]string, 0, min(maxVisible, len(a.queuedInputPreview))+1)
	if start > 0 {
		lines = append(lines, a.styles.Subtle.Render(fmt.Sprintf(" %d earlier queued", start)))
	}
	for _, queued := range a.queuedInputPreview[start:] {
		content := strings.Join(strings.Fields(queued.Content), " ")
		line := fmt.Sprintf(" queued %-8s %s", shortQueueID(queued.ID), content)
		line = contentEllipsize(
			a.renderEnvironment.profile,
			line,
			max(20, a.width-8),
			0,
			"…",
		)
		lines = append(lines, a.styles.Subtle.Render(line))
	}
	return strings.Join(lines, "\n")
}

func shortQueueID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func (a *App) handleQueueSlashCommand(value string) {
	fields := strings.Fields(strings.TrimSpace(value))
	action := "list"
	target := "last"
	if len(fields) > 1 {
		action = strings.ToLower(fields[1])
	}
	if len(fields) > 2 {
		target = fields[2]
	}

	switch action {
	case "list":
		a.chat.AppendSystem(a.queueSummary())
	case "edit":
		if !a.isLeaderThreadView() {
			queued, ok := a.findQueuedPreview(target)
			if !ok || !a.cancelQueuedPreview(queued) {
				a.showNotification("Queued input is already processing", NotifyWarning)
				return
			}
			a.consumeQueuedPreview(a.activeThreadViewID(), queued.ID)
			a.restoreComposerHistoryEntry(queued.Content, nil)
			a.inputMode = InputNormal
			a.syncInputModeFromText()
			a.showToast("Queued input restored for editing")
			return
		}
		if strings.TrimSpace(a.textarea.Value()) != "" ||
			len(a.composerElements) != 0 ||
			a.composerImageLoadPending != nil ||
			a.composerAdmissionPending != nil {
			a.showNotification("Clear the composer and wait for pending operations before editing queued input", NotifyWarning)
			return
		}
		queued, ok := a.findQueuedPreview(target)
		if !ok {
			a.showNotification("Queued input not found", NotifyWarning)
			return
		}
		if a.engine == nil {
			a.showNotification("Queue is unavailable without an engine", NotifyError)
			return
		}
		if err := a.validateQueuedPromptEdit(queued); err != nil {
			a.showNotification(err.Error(), NotifyWarning)
			return
		}
		draft, edited, err := a.engine.EditQueuedPrompt(queued.ID)
		if err != nil {
			a.showNotification("Queued input was not restored", NotifyError)
			return
		}
		if !edited {
			a.showNotification("Queued input is already processing", NotifyWarning)
			return
		}
		a.consumeQueuedPreview(a.activeThreadViewID(), queued.ID)
		if err := a.restoreQueuedPromptDraft(draft); err != nil {
			clearQueuedPromptDraftBytes(&draft)
			a.showNotification("Queued input was removed but could not be restored", NotifyError)
			return
		}
		clearQueuedPromptDraftBytes(&draft)
		a.inputMode = InputNormal
		a.syncInputModeFromText()
		a.showToast("Queued input restored for editing")
	case "remove", "cancel":
		if strings.EqualFold(target, "all") {
			removed := 0
			for len(a.queuedInputPreview) > 0 {
				queued := a.queuedInputPreview[len(a.queuedInputPreview)-1]
				if !a.cancelQueuedPreview(queued) {
					break
				}
				a.consumeQueuedPreview(a.activeThreadViewID(), queued.ID)
				removed++
			}
			a.showToast(fmt.Sprintf("Removed %d queued inputs", removed))
			return
		}
		queued, ok := a.findQueuedPreview(target)
		if !ok || !a.cancelQueuedPreview(queued) {
			a.showNotification("Queued input not found or already processing", NotifyWarning)
			return
		}
		a.consumeQueuedPreview(a.activeThreadViewID(), queued.ID)
		a.showToast("Queued input removed")
	default:
		a.chat.AppendSystem("Usage: /queue [list|edit <id|last>|remove <id|last|all>]")
	}
}

func (a *App) validateQueuedPromptEdit(queued threadQueuedInput) error {
	imageCount := 0
	totalBytes := a.composerPayloadBytes()
	for _, part := range queued.Parts {
		if part.Kind != engine.QueuedPromptPartImage {
			continue
		}
		imageCount++
		if part.Image == nil || part.Image.SizeBytes <= 0 {
			return fmt.Errorf("queued input media is unavailable")
		}
		if part.Image.SizeBytes > int64(attachments.MaxAttachmentBytes) {
			return fmt.Errorf("queued image exceeds the 5 MiB composer limit")
		}
		totalBytes += int(part.Image.SizeBytes)
	}
	if imageCount > maxThreadComposerElements {
		return fmt.Errorf("queued input exceeds the composer element limit")
	}
	if totalBytes > maxComposerPayloadBytes {
		return fmt.Errorf("queued input exceeds the 10 MiB composer draft limit")
	}
	return nil
}

func (a *App) queueSummary() string {
	if len(a.queuedInputPreview) == 0 {
		return "No queued input for this thread."
	}
	lines := []string{fmt.Sprintf("Queued input (%d):", len(a.queuedInputPreview))}
	for _, queued := range a.queuedInputPreview {
		lines = append(lines, fmt.Sprintf("%s  %s", shortQueueID(queued.ID), strings.Join(strings.Fields(queued.Content), " ")))
	}
	return strings.Join(lines, "\n")
}

func (a *App) findQueuedPreview(target string) (threadQueuedInput, bool) {
	if len(a.queuedInputPreview) == 0 {
		return threadQueuedInput{}, false
	}
	if target == "" || strings.EqualFold(target, "last") {
		return a.queuedInputPreview[len(a.queuedInputPreview)-1], true
	}
	for _, queued := range a.queuedInputPreview {
		if queued.ID == target || strings.HasPrefix(queued.ID, target) {
			return queued, true
		}
	}
	return threadQueuedInput{}, false
}

func (a *App) cancelQueuedPreview(queued threadQueuedInput) bool {
	if a.isLeaderThreadView() {
		if a.engine == nil {
			return false
		}
		cancelled, err := a.engine.CancelQueuedPrompt(queued.ID)
		return err == nil && cancelled
	}
	if a.taskExplorerSnapshotSource == nil ||
		a.taskExplorerActionProvider == nil ||
		strings.TrimSpace(queued.ID) == "" ||
		strings.TrimSpace(queued.AgentID) == "" ||
		queued.Generation <= 0 ||
		strings.TrimSpace(queued.BoardID) == "" {
		return false
	}
	snapshot := a.taskExplorerSnapshotSource()
	requestID := uuid.NewString()
	result := a.taskExplorerActionProvider(
		engine.TaskExplorerActionRequest{
			RequestID:       requestID,
			BoardID:         queued.BoardID,
			BoardRevision:   queued.BoardRevision,
			RuntimeRevision: snapshot.Revision.Runtime,
			AgentID:         queued.AgentID,
			Generation:      queued.Generation,
			MessageID:       queued.ID,
			Action:          engine.TaskExplorerActionCancelInput,
		},
	)
	return result.RequestID == requestID &&
		result.BoardID == queued.BoardID &&
		result.BoardRevision == queued.BoardRevision &&
		result.AgentID == queued.AgentID &&
		result.Generation == queued.Generation &&
		result.MessageID == queued.ID &&
		result.Action == engine.TaskExplorerActionCancelInput &&
		result.Conflict == "" &&
		result.Outcome == "input_cancelled"
}

func (a *App) restoreQueuedPromptDraft(draft engine.QueuedPromptDraft) error {
	if a == nil {
		return fmt.Errorf("composer is unavailable")
	}
	var text strings.Builder
	elements := make([]threadComposerElement, 0, len(draft.Parts))
	restoredMedia := make(map[string]*composerDraftImage)
	for index := range draft.Parts {
		part := &draft.Parts[index]
		switch part.Kind {
		case engine.QueuedPromptPartText:
			text.WriteString(part.Text)
		case engine.QueuedPromptPartImage:
			if part.Image == nil || len(part.Image.Data) == 0 || part.Image.MIMEType == "" {
				clearDraftMedia(restoredMedia)
				return fmt.Errorf("queued image is unavailable")
			}
			placeholder := nextImagePlaceholderForElements(elements)
			start := utf8.RuneCountInString(text.String())
			text.WriteString(placeholder)
			a.nextComposerElementID++
			id := fmt.Sprintf("image-%d", a.nextComposerElementID)
			data := part.Image.Data
			part.Image.Data = nil
			elements = append(elements, threadComposerElement{
				ID: id, Kind: composerElementKindImage, Label: placeholder,
				Name: "queued image", MIMEType: part.Image.MIMEType,
				Start: start, End: start + utf8.RuneCountInString(placeholder),
			})
			restoredMedia[id] = &composerDraftImage{
				MIMEType: part.Image.MIMEType,
				Data:     data,
				Detail:   part.Image.Detail,
			}
		default:
			clearDraftMedia(restoredMedia)
			return fmt.Errorf("queued input has unsupported part kind %q", part.Kind)
		}
	}
	if len(elements) > maxThreadComposerElements {
		clearDraftMedia(restoredMedia)
		return fmt.Errorf("queued input exceeds the composer element limit")
	}
	if a.draftMedia == nil {
		a.draftMedia = make(map[string]*composerDraftImage)
	}
	for id, image := range restoredMedia {
		a.draftMedia[id] = image
	}
	a.textarea.SetValue(text.String())
	a.textarea.CursorEnd()
	a.composerElements = elements
	a.composerUndo = nil
	a.markComposerChanged()
	return nil
}

func nextImagePlaceholderForElements(elements []threadComposerElement) string {
	used := make(map[string]struct{}, len(elements))
	for _, element := range elements {
		if element.Kind == composerElementKindImage {
			used[element.Label] = struct{}{}
		}
	}
	for number := 1; ; number++ {
		candidate := fmt.Sprintf("[Image #%d]", number)
		if _, exists := used[candidate]; !exists {
			return candidate
		}
	}
}

func clearQueuedPromptDraftBytes(draft *engine.QueuedPromptDraft) {
	if draft == nil {
		return
	}
	for index := range draft.Parts {
		if draft.Parts[index].Image != nil {
			clearBytes(draft.Parts[index].Image.Data)
		}
	}
}

func clearDraftMedia(media map[string]*composerDraftImage) {
	for id, image := range media {
		if image != nil {
			clearBytes(image.Data)
		}
		delete(media, id)
	}
}
