package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/internal/tui/keybindings"
	"github.com/abietic/yhc/tools"
)

// PlanApprovalResponse represents the user's decision on a plan.
type PlanApprovalResponse int

const (
	PlanApprove PlanApprovalResponse = iota
	PlanReject
	PlanFeedback
)

type planOption struct {
	label      string
	response   PermissionResponse
	targetMode permission.Mode
	confirmed  bool
}

type planDialogFocus uint8

const (
	planFocusReview planDialogFocus = iota
	planFocusActions
	planFocusFeedback
	planFocusBypassConfirmation
)

const (
	planFeedbackNoColorCaret     = "▏"
	planFeedbackNoColorCaretRune = '▏'
)

func (f planDialogFocus) String() string {
	switch f {
	case planFocusReview:
		return "Review"
	case planFocusActions:
		return "Actions"
	case planFocusFeedback:
		return "Feedback"
	case planFocusBypassConfirmation:
		return "Confirm bypass"
	default:
		return "Review"
	}
}

type planViewportState struct {
	offset int
	height int
	total  int
}

func (v *planViewportState) setMetrics(total, height int) {
	v.total = max(0, total)
	v.height = max(1, height)
	v.clamp()
}

func (v *planViewportState) maxOffset() int {
	return max(0, v.total-v.height)
}

func (v *planViewportState) clamp() {
	v.offset = min(max(0, v.offset), v.maxOffset())
}

func (v *planViewportState) scroll(lines int) {
	v.offset += lines
	v.clamp()
}

func (v *planViewportState) page(direction int) {
	v.scroll(direction * max(1, v.height-1))
}

type planDialogGeometry struct {
	outer     layoutRect
	review    layoutRect
	actions   []layoutRect
	feedback  layoutRect
	bypassNo  layoutRect
	bypassYes layoutRect
}

type planEditorIdentity struct {
	threadID     string
	requestID    string
	planPath     string
	planRevision uint64
	generation   uint64
}

type planEditorPresentationSnapshot struct {
	focus          planDialogFocus
	selectedIdx    int
	viewportOffset int
	feedback       textEditorSnapshot
	feedbackUndo   []textEditorSnapshot
}

type planEditorFinishedMsg struct {
	identity         planEditorIdentity
	presentation     planEditorPresentationSnapshot
	terminalReleased bool
	err              error
}

// PlanDialog displays a plan approval view matching the reference layout.
// Shows the plan in a dashed-border area with approval options below,
// including an inline feedback input on the rejection option.
// Mirrors ExitPlanModePermissionRequest.tsx from the reference.
type PlanDialog struct {
	visible            bool
	plan               string
	planPath           string
	ownerThreadID      string
	requestID          string
	planRevision       uint64
	reviewedPlanDigest string
	responseCh         chan<- PermissionResponse
	styles             Styles
	md                 *StreamingMarkdown
	focus              planDialogFocus
	selectedIdx        int
	viewport           planViewportState
	geometry           planDialogGeometry
	feedbackEditor     textarea.Model
	feedbackUndo       []textEditorSnapshot
	keybindResolver    *keybindings.Resolver
	options            []planOption
	planResult         *engine.PlanApprovalDecision
	bypassConfirmYes   bool
	editorGeneration   uint64
	activeEditor       uint64
	editorActive       bool
	feedbackNoColor    bool
	environment        RenderEnvironment
}

func NewPlanDialog(styles Styles) *PlanDialog {
	return newPlanDialog(styles, keybindings.NewResolver(), false, false)
}

func newPlanDialog(
	styles Styles,
	resolver *keybindings.Resolver,
	reducedMotion bool,
	noColor bool,
) *PlanDialog {
	if resolver == nil {
		resolver = keybindings.NewResolver()
	}
	editor := newBoundedTextarea(
		"Describe what should change...",
		80,
		1,
		0,
		nil,
		reducedMotion,
	)
	editor.Blur()
	dialog := &PlanDialog{
		styles:          styles,
		environment:     defaultRenderEnvironment(styles),
		md:              &StreamingMarkdown{},
		feedbackEditor:  editor,
		feedbackNoColor: noColor,
		keybindResolver: resolver,
	}
	dialog.applyFeedbackStyles()
	return dialog
}

func (d *PlanDialog) SetStyles(styles Styles) {
	d.SetRenderEnvironment(d.environment.withStyles(styles))
}

func (d *PlanDialog) SetRenderEnvironment(env RenderEnvironment) {
	d.environment = env.normalized()
	d.styles = d.environment.styles
	d.applyFeedbackStyles()
}

// Show displays the plan approval dialog.
func (d *PlanDialog) Show(
	threadID, sessionID, agentID string,
	approval *engine.PlanApprovalRequest,
	responseCh chan<- PermissionResponse,
) {
	d.visible = true
	d.responseCh = responseCh
	d.focus = planFocusReview
	d.selectedIdx = 0
	d.viewport = planViewportState{}
	d.geometry = planDialogGeometry{}
	d.feedbackEditor.Reset()
	d.feedbackEditor.Blur()
	d.feedbackUndo = nil
	d.keybindResolver.ResetPending()
	d.md.Reset()
	d.ownerThreadID = normalizeThreadViewID(threadID)
	d.requestID = ""
	d.planRevision = 0
	d.activeEditor = 0
	d.editorActive = false
	d.planResult = nil
	d.bypassConfirmYes = false

	returnMode := permission.ModeDefault
	if approval != nil {
		returnMode = planReturnModeOrDefault(approval.ReturnMode)
		d.requestID = approval.RequestID
		d.planRevision = approval.PlanRevision
	}
	if approval != nil && strings.TrimSpace(approval.PlanFileIdentity) != "" {
		d.planPath = approval.PlanFileIdentity
		data, digest, err := engine.ReadPlanReviewSnapshot(d.planPath)
		if err == nil {
			d.plan = string(data)
			d.reviewedPlanDigest = digest
		} else {
			d.plan = ""
			d.reviewedPlanDigest = ""
		}
	} else {
		d.plan = tools.GetPlan(sessionID, agentID)
		d.planPath = tools.GetPlanFilePath(sessionID, agentID)
		d.reviewedPlanDigest = engine.PlanBytesDigest([]byte(d.plan))
	}
	d.options = buildPlanOptions(returnMode)
}

// IsVisible returns whether the dialog is currently shown.
func (d *PlanDialog) IsVisible() bool {
	return d.visible
}

// Feedback returns the user-provided feedback text.
func (d *PlanDialog) Feedback() string {
	if d == nil {
		return ""
	}
	return d.feedbackEditor.Value()
}

// ReviewedPlanDigest returns the exact bytes shown by the current dialog.
func (d *PlanDialog) ReviewedPlanDigest() string {
	if d == nil {
		return ""
	}
	return d.reviewedPlanDigest
}

// ApprovalTarget returns the explicit target selected for this response.
func (d *PlanDialog) ApprovalTarget() (permission.Mode, bool) {
	if d == nil || d.selectedIdx < 0 || d.selectedIdx >= len(d.options) {
		return permission.ModePlan, false
	}
	option := d.options[d.selectedIdx]
	return option.targetMode, option.confirmed
}

// PlanResult is the explicit Plan-only terminal intent. It is deliberately
// independent from the generic permission channel and retained feedback draft.
func (d *PlanDialog) PlanResult() *engine.PlanApprovalDecision {
	if d == nil || d.planResult == nil {
		return nil
	}
	result := *d.planResult
	return &result
}

// HandleKey processes key events. The bypass confirmation owns the keyboard
// while active: its routing runs before paging, review, action, feedback,
// editor, and generic branches, and every unmatched key is an exact no-op.
func (d *PlanDialog) HandleKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	if d.focus == planFocusBypassConfirmation {
		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("up", "k", "down", "j", "tab", "shift+tab"))):
			d.bypassConfirmYes = !d.bypassConfirmYes
		case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
			d.focus = planFocusActions
		case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
			if d.bypassConfirmYes {
				d.respondPlan(engine.PlanApprovalApprove, permission.ModeBypassPermissions, true, "")
				return true, nil
			}
			d.focus = planFocusActions
		}
		return false, nil
	}

	switch {
	case key.Matches(msg, key.NewBinding(key.WithKeys("pgup"))):
		d.viewport.page(-1)
		return false, nil
	case key.Matches(msg, key.NewBinding(key.WithKeys("pgdown"))):
		d.viewport.page(1)
		return false, nil
	}

	if d.focus == planFocusFeedback {
		return d.handleFeedbackKey(msg)
	}

	switch {
	case key.Matches(msg, key.NewBinding(key.WithKeys("tab", "shift+tab"))):
		if d.focus == planFocusReview {
			d.focus = planFocusActions
		} else {
			d.focus = planFocusReview
		}
	case key.Matches(msg, key.NewBinding(key.WithKeys("ctrl+g"))):
		return false, d.editInEditor()
	case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
		d.respondPlan(engine.PlanApprovalCancel, permission.ModePlan, false, "")
		return true, nil
	case d.focus == planFocusReview:
		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("up", "k"))):
			d.viewport.scroll(-1)
		case key.Matches(msg, key.NewBinding(key.WithKeys("down", "j"))):
			d.viewport.scroll(1)
		case key.Matches(msg, key.NewBinding(key.WithKeys("home"))):
			d.viewport.offset = 0
		case key.Matches(msg, key.NewBinding(key.WithKeys("end"))):
			d.viewport.offset = d.viewport.maxOffset()
		case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
			d.focus = planFocusActions
		}
	case d.focus == planFocusActions:
		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("up", "k"))):
			if d.selectedIdx > 0 {
				d.selectedIdx--
			}
		case key.Matches(msg, key.NewBinding(key.WithKeys("down", "j"))):
			if d.selectedIdx < len(d.options)-1 {
				d.selectedIdx++
			}
		case key.Matches(msg, key.NewBinding(key.WithKeys("home"))):
			d.selectedIdx = 0
		case key.Matches(msg, key.NewBinding(key.WithKeys("end"))):
			d.selectedIdx = max(0, len(d.options)-1)
		case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
			if d.selectedIdx < 0 || d.selectedIdx >= len(d.options) {
				return false, nil
			}
			if d.options[d.selectedIdx].targetMode == permission.ModePlan {
				d.focus = planFocusFeedback
				d.keybindResolver.ResetPending()
				return false, d.feedbackEditor.Focus()
			}
			if d.options[d.selectedIdx].targetMode == permission.ModeBypassPermissions {
				d.focus = planFocusBypassConfirmation
				d.bypassConfirmYes = false
				d.geometry.bypassNo = layoutRect{}
				d.geometry.bypassYes = layoutRect{}
				return false, nil
			}
			d.respondPlan(engine.PlanApprovalApprove, d.options[d.selectedIdx].targetMode, false, "")
			return true, nil
		}
	default:
		d.focus = planFocusReview
	}
	return false, nil
}

func (d *PlanDialog) handleFeedbackKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	if key.Matches(msg, key.NewBinding(key.WithKeys("esc"))) {
		d.feedbackEditor.Blur()
		d.keybindResolver.ResetPending()
		d.focus = planFocusActions
		return false, nil
	}

	resolution := d.keybindResolver.ResolveEvent(
		msg,
		keybindings.ContextChat,
	)
	switch resolution.Kind {
	case keybindings.ResolutionChordStarted,
		keybindings.ResolutionChordCancelled:
		return false, nil
	case keybindings.ResolutionMatch:
		switch resolution.Action {
		case keybindings.ActionChatSubmit:
			if strings.TrimSpace(d.Feedback()) == "" {
				d.feedbackEditor.Blur()
				d.focus = planFocusActions
				return false, nil
			}
			d.keybindResolver.ResetPending()
			d.respondPlan(engine.PlanApprovalRevise, permission.ModePlan, false, strings.TrimSpace(d.Feedback()))
			return true, nil
		case keybindings.ActionChatNewline:
			before := captureTextEditorSnapshot(d.feedbackEditor)
			d.feedbackEditor.InsertRune('\n')
			d.feedbackUndo = recordTextEditorUndo(
				d.feedbackUndo,
				before,
				d.feedbackEditor.Value(),
			)
			return false, nil
		case keybindings.ActionChatUndo:
			d.undoFeedback()
			return false, nil
		case keybindings.ActionHistoryPrevious,
			keybindings.ActionHistoryNext,
			keybindings.ActionChatImagePaste:
			return false, d.updateFeedback(msg)
		default:
			return false, nil
		}
	default:
		return false, d.updateFeedback(msg)
	}
}

func (d *PlanDialog) updateFeedback(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	d.feedbackEditor, d.feedbackUndo, cmd = updateTextEditor(
		d.feedbackEditor,
		d.feedbackUndo,
		msg,
	)
	return cmd
}

func (d *PlanDialog) undoFeedback() {
	if len(d.feedbackUndo) == 0 {
		return
	}
	index := len(d.feedbackUndo) - 1
	entry := d.feedbackUndo[index]
	d.feedbackUndo = d.feedbackUndo[:index]
	restoreTextEditorSnapshot(&d.feedbackEditor, entry)
}

func (d *PlanDialog) feedbackFocused() bool {
	return d != nil &&
		d.visible &&
		d.focus == planFocusFeedback
}

// Update forwards non-key editor messages, including clipboard paste results
// and cursor ticks, only while Feedback owns input.
func (d *PlanDialog) Update(msg tea.Msg) tea.Cmd {
	if !d.feedbackFocused() {
		return nil
	}
	return d.updateFeedback(msg)
}

func (d *PlanDialog) feedbackCursorOffset() int {
	if d == nil {
		return 0
	}
	return captureTextEditorSnapshot(d.feedbackEditor).CursorOffset
}

// HandleMouse routes pointer input only through the geometry published by the
// most recent render. The App owns the modal non-leakage boundary. While the
// bypass confirmation owns input, only its current-frame No and Yes hitboxes
// act; wheel, motion, release, and clicks elsewhere are exact no-ops.
func (d *PlanDialog) HandleMouse(msg tuiMouseMsg) {
	if d.focus == planFocusBypassConfirmation {
		if msg.Button != tea.MouseLeft || msg.Action != mouseActionPress {
			return
		}
		if planRectContains(d.geometry.bypassNo, msg.X, msg.Y) {
			d.focus = planFocusActions
			return
		}
		if planRectContains(d.geometry.bypassYes, msg.X, msg.Y) {
			d.respondPlan(
				engine.PlanApprovalApprove,
				permission.ModeBypassPermissions,
				true,
				"",
			)
		}
		return
	}
	switch msg.Button {
	case tea.MouseWheelUp:
		if planRectContains(d.geometry.review, msg.X, msg.Y) {
			d.viewport.scroll(-3)
		}
	case tea.MouseWheelDown:
		if planRectContains(d.geometry.review, msg.X, msg.Y) {
			d.viewport.scroll(3)
		}
	case tea.MouseLeft:
		if msg.Action != mouseActionPress {
			return
		}
		if planRectContains(d.geometry.review, msg.X, msg.Y) {
			d.focus = planFocusReview
			d.keybindResolver.ResetPending()
			d.feedbackEditor.Blur()
			return
		}
		if planRectContains(d.geometry.feedback, msg.X, msg.Y) {
			d.focus = planFocusFeedback
			d.keybindResolver.ResetPending()
			d.feedbackEditor.Focus()
			return
		}
		for index, rect := range d.geometry.actions {
			if planRectContains(rect, msg.X, msg.Y) {
				d.selectedIdx = index
				d.focus = planFocusActions
				d.keybindResolver.ResetPending()
				d.feedbackEditor.Blur()
				return
			}
		}
	}
}

func planRectContains(rect layoutRect, x, y int) bool {
	return rect.Width > 0 &&
		rect.Height > 0 &&
		x >= rect.X &&
		x < rect.X+rect.Width &&
		y >= rect.Y &&
		y < rect.Y+rect.Height
}

func buildPlanOptions(returnMode permission.Mode) []planOption {
	returnMode = planReturnModeOrDefault(returnMode)
	targets := engine.PlanApprovalTargetModes(returnMode)
	options := make([]planOption, 0, len(targets)+1)
	for _, target := range targets {
		label := "Approve with previous permissions (" + string(target) + ")"
		switch target {
		case permission.ModeAcceptEdits:
			label = "Approve and auto-accept edits"
		case permission.ModeBypassPermissions:
			label = "Approve and bypass permissions"
		}
		options = append(options, planOption{label: label, response: PermissionAllow, targetMode: target})
	}
	return append(options, planOption{
		label:      "Request changes and keep planning",
		response:   PermissionDeny,
		targetMode: permission.ModePlan,
	})
}

func (d *PlanDialog) respondPlan(outcome engine.PlanApprovalOutcome, target permission.Mode, confirmed bool, feedback string) {
	d.planResult = &engine.PlanApprovalDecision{RequestID: d.requestID, PlanRevision: d.planRevision, Outcome: outcome, ReviewedPlanDigest: d.reviewedPlanDigest, TargetMode: target, Confirmed: confirmed, Feedback: feedback}
	if outcome == engine.PlanApprovalApprove {
		d.respond(PermissionAllow)
		return
	}
	d.respond(PermissionDeny)
}

func (d *PlanDialog) respond(resp PermissionResponse) {
	d.feedbackEditor.Blur()
	d.keybindResolver.ResetPending()
	d.editorActive = false
	d.activeEditor = 0
	if d.responseCh != nil {
		d.responseCh <- resp
		d.responseCh = nil
	}
	d.visible = false
}

// ForceClose denies and closes the dialog.
func (d *PlanDialog) ForceClose() {
	d.respondPlan(engine.PlanApprovalCancel, permission.ModePlan, false, "")
}

// dismissWithoutResponse hides presentation while its owner retains the
// runtime request. The coordinator owns the detached response channel.
func (d *PlanDialog) dismissWithoutResponse() {
	d.feedbackEditor.Blur()
	d.keybindResolver.ResetPending()
	d.editorActive = false
	d.activeEditor = 0
	d.responseCh = nil
	d.visible = false
}

// editInEditor launches the configured external editor on the exact
// engine-owned Plan file. The callback carries both runtime identity and the
// presentation snapshot so stale results cannot mutate a newer approval.
func (d *PlanDialog) editInEditor() tea.Cmd {
	if d == nil || d.editorActive {
		return nil
	}
	d.editorGeneration++
	d.activeEditor = d.editorGeneration
	identity := planEditorIdentity{
		threadID:     d.ownerThreadID,
		requestID:    d.requestID,
		planPath:     d.planPath,
		planRevision: d.planRevision,
		generation:   d.activeEditor,
	}
	presentation := d.captureEditorPresentation()
	command, err := externalEditorCommand(d.planPath)
	if err != nil {
		d.editorActive = false
		return func() tea.Msg {
			return planEditorFinishedMsg{
				identity:     identity,
				presentation: presentation,
				err:          err,
			}
		}
	}
	d.editorActive = true
	return tea.ExecProcess(command, func(err error) tea.Msg {
		return planEditorFinishedMsg{
			identity:         identity,
			presentation:     presentation,
			terminalReleased: true,
			err:              err,
		}
	})
}

func (d *PlanDialog) captureEditorPresentation() planEditorPresentationSnapshot {
	if d == nil {
		return planEditorPresentationSnapshot{}
	}
	return planEditorPresentationSnapshot{
		focus:          d.focus,
		selectedIdx:    d.selectedIdx,
		viewportOffset: d.viewport.offset,
		feedback:       captureTextEditorSnapshot(d.feedbackEditor),
		feedbackUndo:   append([]textEditorSnapshot(nil), d.feedbackUndo...),
	}
}

func (d *PlanDialog) restoreEditorPresentation(
	snapshot planEditorPresentationSnapshot,
) {
	d.focus = snapshot.focus
	if d.focus > planFocusFeedback {
		d.focus = planFocusReview
	}
	d.selectedIdx = max(0, min(snapshot.selectedIdx, len(d.options)-1))
	d.viewport.offset = max(0, snapshot.viewportOffset)
	restoreTextEditorSnapshot(&d.feedbackEditor, snapshot.feedback)
	d.feedbackUndo = append([]textEditorSnapshot(nil), snapshot.feedbackUndo...)
	if d.focus == planFocusFeedback {
		d.feedbackEditor.Focus()
	} else {
		d.feedbackEditor.Blur()
	}
	d.geometry = planDialogGeometry{}
}

func (d *PlanDialog) editorIdentityMatches(identity planEditorIdentity) bool {
	return d != nil &&
		d.visible &&
		d.ownerThreadID == identity.threadID &&
		d.requestID == identity.requestID &&
		d.planRevision == identity.planRevision &&
		d.planPath == identity.planPath &&
		d.activeEditor == identity.generation &&
		identity.generation != 0
}

func (d *PlanDialog) applyEditorResult(
	message planEditorFinishedMsg,
) (bool, error) {
	if !d.editorIdentityMatches(message.identity) {
		return false, nil
	}
	d.editorActive = false
	d.activeEditor = 0
	if message.err != nil {
		return true, message.err
	}
	if err := d.reloadPlan(); err != nil {
		return true, err
	}
	d.restoreEditorPresentation(message.presentation)
	return true, nil
}

func (d *PlanDialog) EditorActive() bool {
	return d != nil && d.editorActive
}

// ReloadPlan re-reads the plan from disk without resetting presentation state.
func (d *PlanDialog) ReloadPlan() {
	_ = d.reloadPlan()
}

func (d *PlanDialog) reloadPlan() error {
	data, digest, err := engine.ReadPlanReviewSnapshot(d.planPath)
	if err != nil {
		return err
	}
	d.plan = string(data)
	d.reviewedPlanDigest = digest
	d.md.Reset()
	return nil
}

func (d *PlanDialog) applyFeedbackStyles() {
	focused := textarea.StyleState{
		Base:        d.styles.DialogInputSurface,
		CursorLine:  d.styles.DialogInputSurface,
		EndOfBuffer: d.styles.DialogInputSurface,
		Placeholder: d.styles.DialogInputPlaceholder,
		Prompt:      d.styles.DialogInputText,
		Text:        d.styles.DialogInputText,
	}
	blurred := focused
	blurred.Text = d.styles.DialogInputText.Faint(true)
	styles := d.feedbackEditor.Styles()
	styles.Focused = focused
	styles.Blurred = blurred
	styles.Cursor.Color = d.styles.DialogInputCursor.GetForeground()
	d.feedbackEditor.SetStyles(styles)
	if d.feedbackEditor.Focused() {
		d.feedbackEditor.Focus()
	} else {
		d.feedbackEditor.Blur()
	}
}

func planReturnModeOrDefault(mode permission.Mode) permission.Mode {
	switch mode {
	case permission.ModeDefault,
		permission.ModeAcceptEdits,
		permission.ModeBypassPermissions,
		permission.ModeDontAsk,
		permission.ModeAuto,
		permission.ModeBubble:
		return mode
	default:
		return permission.ModeDefault
	}
}

// Overlay renders the Plan review above a sticky action and help region.
func (d *PlanDialog) Overlay(base string, width, height int) string {
	d.geometry = planDialogGeometry{}
	if width <= 0 || height <= 0 {
		return ""
	}
	profile := d.environment.normalized().profile
	contentWidth := width - 4
	if contentWidth < 20 {
		contentWidth = max(1, width-2)
	}

	dim := d.styles.DialogHelp
	bold := d.styles.Bold
	planColor := d.styles.DialogTitle
	compact := height < 22
	feedbackActive := d.focus == planFocusFeedback

	var lines []string
	if !compact || !feedbackActive {
		lines = append(lines, dim.Render(strings.Repeat("─", width)))
	}
	lines = append(
		lines,
		" "+d.styles.DialogTitle.Bold(true).Render("Ready to implement?"),
	)
	if !compact {
		lines = append(lines, "")
		lines = append(
			lines,
			" "+planColor.Render(
				"The agent has written a plan. Review it before choosing how to proceed.",
			),
		)
		lines = append(lines, "")
	}

	reviewLabel := d.styles.Subtle.Render("Review")
	if d.focus == planFocusReview {
		reviewLabel = d.styles.DialogTitle.Bold(true).Render("Review")
	}
	borderWidth := max(1, contentWidth-profile.width("Review")-2)
	lines = append(
		lines,
		" "+reviewLabel+" "+dim.Render(strings.Repeat("╌", borderWidth)),
	)

	feedbackRows := 0
	projectedFeedbackStart := -1
	projectedFeedbackEnd := -1
	if feedbackActive {
		feedbackRows = planFeedbackEditorRows(height)
	}
	afterReview := 1 + len(d.options) + 1 // review border, actions, footer
	if !compact {
		afterReview += 3 // action/footer spacing and editor/path footer
	}
	if feedbackActive {
		afterReview += feedbackRows + 2 // editor rows and rounded border
		if !compact {
			afterReview++ // feedback spacing
		}
	}
	minReviewHeight := 3
	if compact && feedbackActive {
		minReviewHeight = 1
	}
	planHeight := max(minReviewHeight, height-len(lines)-afterReview)

	reviewY := len(lines)
	planLines := d.renderPlanMarkdown(contentWidth-4, planHeight)
	for index := range planHeight {
		planLine := ""
		if index < len(planLines) {
			planLine = planLines[index]
		}
		projected := modalProjectLine(
			profile,
			planLine,
			max(1, contentWidth-4),
			2,
		)
		lines = append(lines, "  "+projected)
	}
	d.geometry.review = layoutRect{
		X:      2,
		Y:      reviewY,
		Width:  max(1, contentWidth-4),
		Height: planHeight,
	}

	lines = append(lines, " "+dim.Render(strings.Repeat("╌", contentWidth-2)))
	if !compact {
		lines = append(lines, "")
	}

	d.geometry.actions = nil
	if d.focus == planFocusBypassConfirmation {
		lines = append(lines, d.renderBypassConfirmation(width, len(lines))...)
	} else {
		d.geometry.actions = make([]layoutRect, 0, len(d.options))
		for i, opt := range d.options {
			prefix := "   "
			label := fmt.Sprintf("%d. %s", i+1, opt.label)
			if i == d.selectedIdx && d.focus == planFocusActions {
				prefix = " ❯ "
				label = d.styles.ToolSuccess.Bold(true).Render(label)
			}
			d.geometry.actions = append(d.geometry.actions, layoutRect{
				X: 0, Y: len(lines), Width: width, Height: 1,
			})
			lines = append(
				lines,
				modalProjectLine(profile, prefix+label, max(1, width), 0),
			)
		}
	}

	if feedbackActive {
		if !compact {
			lines = append(lines, "")
		}
		d.feedbackEditor.SetWidth(max(1, width-8))
		d.feedbackEditor.SetHeight(feedbackRows)
		editorBorder := d.styles.DialogInputBorder
		if d.feedbackEditor.Focused() {
			editorBorder = d.styles.DialogInputBorderFocused
		}
		editorY := len(lines)
		projectedFeedbackStart = editorY
		editorLines := strings.Split(
			editorBorder.Render(d.feedbackEditorView()),
			"\n",
		)
		editorWidth := 0
		for _, line := range editorLines {
			rendered := modalProjectLine(
				profile,
				"   "+line,
				max(1, width),
				0,
			)
			editorWidth = max(editorWidth, profile.width(rendered))
			lines = append(lines, rendered)
		}
		projectedFeedbackEnd = len(lines)
		d.geometry.feedback = layoutRect{
			X:      3,
			Y:      editorY,
			Width:  min(max(1, width-3), max(1, editorWidth-3)),
			Height: len(editorLines),
		}
	}

	if !compact {
		lines = append(lines, "")
	}
	footer := d.planFooter(bold, dim)
	lines = append(
		lines,
		" "+modalProjectLine(profile, footer, max(1, width-1), 1),
	)
	if !compact {
		editorPrefix := strings.Join([]string{
			fmt.Sprintf(
				"ctrl-g edit in %s",
				bold.Render(externalEditorDisplayName()),
			),
			dim.Render("·"),
		}, " ")
		editorPrefixWidth := profile.measure(editorPrefix, 1)
		pathStart := 2 + editorPrefixWidth
		pathWidth := max(1, width-pathStart)
		editorFooter := editorPrefix + " " + dim.Render(
			modalTruncatePath(profile, d.planPath, pathWidth, pathStart),
		)
		lines = append(
			lines,
			" "+modalProjectLine(
				profile,
				editorFooter,
				max(1, width-1),
				1,
			),
		)
	}

	for index := range lines {
		if index >= projectedFeedbackStart && index < projectedFeedbackEnd {
			continue
		}
		lines[index] = modalProjectLine(profile, lines[index], width, 0)
	}
	view, geometry := modalTopProjectedFrame(profile, lines, height)
	d.geometry.outer = geometry.outer
	d.geometry.review = modalClipRect(d.geometry.review, height)
	d.geometry.feedback = modalClipRect(d.geometry.feedback, height)
	for index := range d.geometry.actions {
		d.geometry.actions[index] = modalClipRect(d.geometry.actions[index], height)
	}
	if d.focus == planFocusBypassConfirmation {
		// The confirmation frame publishes only its outer geometry plus the
		// current-frame No and Yes hitboxes. Review, feedback, and the
		// underlying action rows stay rendered but expose no hitboxes.
		d.geometry.review = layoutRect{}
		d.geometry.feedback = layoutRect{}
		d.geometry.actions = nil
		d.geometry.bypassNo = modalClipRect(d.geometry.bypassNo, height)
		d.geometry.bypassYes = modalClipRect(d.geometry.bypassYes, height)
	}
	return view
}

func (d *PlanDialog) renderBypassConfirmation(width, startY int) []string {
	profile := d.environment.normalized().profile
	lines := []string{modalProjectLine(
		profile,
		"   "+d.styles.Warning.Bold(true).Render(
			"Bypass permissions disables all tool permission checks.",
		),
		max(1, width),
		0,
	)}
	for index, option := range []struct {
		label string
		yes   bool
	}{
		{label: "No, return to actions"},
		{label: "Yes, bypass permissions", yes: true},
	} {
		hitbox := layoutRect{X: 0, Y: startY + 1 + index, Width: max(1, width), Height: 1}
		if option.yes {
			d.geometry.bypassYes = hitbox
		} else {
			d.geometry.bypassNo = hitbox
		}
		prefix := "   "
		label := d.styles.Subtle.Render(option.label)
		if d.bypassConfirmYes == option.yes {
			prefix = " ❯ "
			label = d.styles.DialogTitle.Bold(true).Render(option.label)
		}
		lines = append(
			lines,
			modalProjectLine(profile, prefix+label, max(1, width), 0),
		)
	}
	return lines
}

// feedbackEditorView keeps the editing model authoritative. A no-color final
// frame strips Bubbles' reverse-video distinction, so render a copy with a
// one-cell caret projection instead. The copied editor is one column narrower
// in every no-color frame, including blink-hidden and blurred frames, which
// keeps text and geometry stable as the caret appears and disappears.
func (d *PlanDialog) feedbackEditorView() string {
	editor := d.feedbackEditor
	rendered := editor.View()
	if !d.feedbackNoColor {
		return rendered
	}
	editor.SetWidth(max(1, editor.Width()-1))
	if !editor.Focused() || !sgrEnablesReverseVideo(rendered) {
		return editor.View()
	}

	caretOffset := d.feedbackCursorOffset()
	value := []rune(editor.Value())
	if len(value) == 0 {
		editor.Placeholder = ""
		value = []rune(planFeedbackNoColorCaret + d.feedbackEditor.Placeholder)
		caretOffset = 0
	} else {
		caretOffset = min(max(0, caretOffset), len(value))
		value = append(value, 0)
		copy(value[caretOffset+1:], value[caretOffset:])
		value[caretOffset] = planFeedbackNoColorCaretRune
		caretOffset++
	}
	editor.SetValue(string(value))
	setTextareaRuneCursor(&editor, caretOffset)
	editor.Blur()
	return editor.View()
}

func sgrEnablesReverseVideo(rendered string) bool {
	for {
		start := strings.Index(rendered, "\x1b[")
		if start < 0 {
			return false
		}
		rendered = rendered[start+2:]
		end := strings.IndexByte(rendered, 'm')
		if end < 0 {
			return false
		}
		for parameter := range strings.FieldsFuncSeq(
			rendered[:end],
			func(r rune) bool { return r == ';' || r == ':' },
		) {
			if parameter == "7" {
				return true
			}
		}
		rendered = rendered[end+1:]
	}
}

func planFeedbackEditorRows(height int) int {
	switch {
	case height >= 36:
		return 5
	case height >= 22:
		return 3
	default:
		return 1
	}
}

func (d *PlanDialog) planFooter(bold, dim lipgloss.Style) string {
	start := 0
	end := 0
	if d.viewport.total > 0 {
		start = d.viewport.offset + 1
		end = min(d.viewport.total, d.viewport.offset+d.viewport.height)
	}
	position := fmt.Sprintf("%d-%d/%d", start, end, d.viewport.total)
	switch d.focus {
	case planFocusActions:
		return strings.Join([]string{
			bold.Render("Actions"),
			dim.Render(position),
			dim.Render("↑/↓ choose · enter select · tab review · pgup/pgdn scroll · esc cancel"),
		}, " · ")
	case planFocusBypassConfirmation:
		return strings.Join([]string{
			bold.Render("Confirm bypass"),
			dim.Render(position),
			dim.Render("↑/↓ choose · enter select · esc back"),
		}, " · ")
	case planFocusFeedback:
		hints := joinKeyHints(
			keyHint(
				d.feedbackShortcut(
					keybindings.ActionChatSubmit,
					"enter",
				),
				"send",
			),
			keyHint(
				d.feedbackShortcut(
					keybindings.ActionChatNewline,
					"ctrl+j",
				),
				"newline",
			),
			keyHint(
				d.feedbackShortcut(
					keybindings.ActionChatUndo,
					"ctrl+z",
				),
				"undo",
			),
			keyHint("esc", "actions"),
		)
		return strings.Join([]string{
			bold.Render("Feedback"),
			dim.Render(position),
			dim.Render(hints),
		}, " · ")
	default:
		return strings.Join([]string{
			bold.Render("Review"),
			dim.Render(position),
			dim.Render("↑/↓ scroll · pgup/pgdn page · home/end · tab actions · ctrl-g edit"),
		}, " · ")
	}
}

func (d *PlanDialog) feedbackShortcut(
	action keybindings.Action,
	fallback string,
) string {
	if d.keybindResolver == nil {
		return fallback
	}
	return d.keybindResolver.GetKeyForAction(
		keybindings.ContextChat,
		action,
	)
}

func (d *PlanDialog) renderPlanMarkdown(width, maxLines int) []string {
	if d.plan == "" {
		d.viewport.setMetrics(1, maxLines)
		return []string{
			d.styles.Placeholder.Render(
				"No plan found. Please write your plan to the plan file first.",
			),
		}
	}

	rendered := d.md.renderWithEnvironment(d.plan, width, d.environment)
	allLines := strings.Split(rendered, "\n")
	d.viewport.setMetrics(len(allLines), maxLines)

	visible := allLines[d.viewport.offset:]
	if len(visible) > maxLines {
		visible = visible[:maxLines]
	}

	return visible
}
