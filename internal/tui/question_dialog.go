package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/tools"
)

// questionView tracks which view is active in the question dialog.
type questionView int

const (
	viewQuestion questionView = iota
	viewSubmit
)

// QuestionDialog displays a multi-question picker for AskUserQuestion.
// Features a tab bar for navigating questions, a submit review screen,
// and the ability to go back and change answers.
// Mirrors AskUserQuestionPermissionRequest.tsx from the reference.
type QuestionDialog struct {
	visible     bool
	questions   []tools.UserQuestion
	answers     map[string]string
	currentIdx  int          // current question/tab index
	view        questionView // which view is active
	optionIdx   int          // selected option index within current question
	submitIdx   int          // selected option on submit screen (0=submit, 1=cancel)
	otherText   string
	inOther     bool // whether typing in "Other" text input
	responseCh  chan<- PermissionResponse
	answerJSON  string
	styles      Styles
	environment RenderEnvironment
	geometry    modalFrameGeometry
	width       int
	height      int
}

func NewQuestionDialog(styles Styles) *QuestionDialog {
	return &QuestionDialog{
		styles:      styles,
		environment: defaultRenderEnvironment(styles),
		answers:     make(map[string]string),
	}
}

func (d *QuestionDialog) SetStyles(styles Styles) {
	d.SetRenderEnvironment(d.environment.withStyles(styles))
}

func (d *QuestionDialog) SetRenderEnvironment(env RenderEnvironment) {
	d.environment = env.normalized()
	d.styles = d.environment.styles
}

// Show displays the question dialog with parsed questions.
func (d *QuestionDialog) Show(inputJSON string, responseCh chan<- PermissionResponse) {
	d.visible = true
	d.responseCh = responseCh
	d.currentIdx = 0
	d.optionIdx = 0
	d.submitIdx = 0
	d.otherText = ""
	d.inOther = false
	d.view = viewQuestion
	d.answers = make(map[string]string)
	d.answerJSON = ""

	var params struct {
		Questions []tools.UserQuestion `json:"questions"`
	}
	if err := json.Unmarshal([]byte(inputJSON), &params); err == nil {
		d.questions = params.Questions
	}
}

func (d *QuestionDialog) IsVisible() bool { return d.visible }

func (d *QuestionDialog) AnswerJSON() string { return d.answerJSON }

func (d *QuestionDialog) allAnswered() bool {
	for _, q := range d.questions {
		if _, ok := d.answers[q.Question]; !ok {
			return false
		}
	}
	return true
}

// HandleKey processes key events.
func (d *QuestionDialog) HandleKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	if len(d.questions) == 0 {
		d.respond(PermissionDeny)
		return true, nil
	}

	// Global: tab/arrow to navigate tabs
	switch {
	case key.Matches(msg, key.NewBinding(key.WithKeys("tab", "right"))):
		if !d.inOther {
			d.nextTab()
			return false, nil
		}
	case key.Matches(msg, key.NewBinding(key.WithKeys("shift+tab", "left"))):
		if !d.inOther {
			d.prevTab()
			return false, nil
		}
	}

	if d.view == viewSubmit {
		return d.handleSubmitKey(msg)
	}

	if d.inOther {
		return d.handleOtherInput(msg)
	}

	return d.handleQuestionKey(msg)
}

func (d *QuestionDialog) nextTab() {
	totalTabs := len(d.questions) + 1
	d.currentIdx++
	if d.currentIdx >= totalTabs {
		d.currentIdx = 0
	}
	d.updateView()
}

func (d *QuestionDialog) prevTab() {
	totalTabs := len(d.questions) + 1
	d.currentIdx--
	if d.currentIdx < 0 {
		d.currentIdx = totalTabs - 1
	}
	d.updateView()
}

func (d *QuestionDialog) updateView() {
	if d.currentIdx >= len(d.questions) {
		d.view = viewSubmit
		d.submitIdx = 0
	} else {
		d.view = viewQuestion
		d.optionIdx = 0
		d.otherText = ""
		d.inOther = false
	}
}

func (d *QuestionDialog) handleQuestionKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	q := d.questions[d.currentIdx]
	numOptions := len(q.Options) + 2 // options + "Type something" + "Chat about this"

	switch {
	case key.Matches(msg, key.NewBinding(key.WithKeys("up", "k"))):
		if d.optionIdx > 0 {
			d.optionIdx--
		}
	case key.Matches(msg, key.NewBinding(key.WithKeys("down", "j"))):
		if d.optionIdx < numOptions-1 {
			d.optionIdx++
		}
	case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
		if d.optionIdx < len(q.Options) {
			d.answers[q.Question] = q.Options[d.optionIdx].Label
			if d.currentIdx < len(d.questions)-1 {
				d.currentIdx++
				d.optionIdx = 0
			} else {
				d.currentIdx = len(d.questions) // go to submit
				d.view = viewSubmit
				d.submitIdx = 0
			}
			return false, nil
		}
		if d.optionIdx == len(q.Options) {
			// "Type something"
			d.inOther = true
			d.otherText = ""
			return false, nil
		}
		// "Chat about this" — reject with no feedback (user will type in chat)
		d.respond(PermissionDeny)
		return true, nil
	case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
		d.respond(PermissionDeny)
		return true, nil
	}
	return false, nil
}

func (d *QuestionDialog) handleOtherInput(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	switch {
	case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
		if d.otherText != "" {
			d.answers[d.questions[d.currentIdx].Question] = d.otherText
			d.inOther = false
			if d.currentIdx < len(d.questions)-1 {
				d.currentIdx++
				d.optionIdx = 0
			} else {
				d.currentIdx = len(d.questions)
				d.view = viewSubmit
				d.submitIdx = 0
			}
		}
	case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
		d.inOther = false
	case key.Matches(msg, key.NewBinding(key.WithKeys("backspace"))):
		if len(d.otherText) > 0 {
			_, size := utf8.DecodeLastRuneInString(d.otherText)
			if size > 0 {
				d.otherText = d.otherText[:len(d.otherText)-size]
			}
		}
	default:
		if msg.Text != "" {
			d.otherText += msg.Text
		}
	}
	return false, nil
}

func (d *QuestionDialog) handleSubmitKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	switch {
	case key.Matches(msg, key.NewBinding(key.WithKeys("up", "k"))):
		if d.submitIdx > 0 {
			d.submitIdx--
		}
	case key.Matches(msg, key.NewBinding(key.WithKeys("down", "j"))):
		if d.submitIdx < 1 {
			d.submitIdx++
		}
	case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
		if d.submitIdx == 0 && d.allAnswered() {
			d.buildAnswerJSON()
			d.respond(PermissionAllow)
			return true, nil
		}
		// Cancel
		d.respond(PermissionDeny)
		return true, nil
	case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
		d.respond(PermissionDeny)
		return true, nil
	}
	return false, nil
}

func (d *QuestionDialog) buildAnswerJSON() {
	result := make(map[string]any)
	result["questions"] = d.questions
	result["answers"] = d.answers
	if data, err := json.Marshal(result); err == nil {
		d.answerJSON = string(data)
	}
}

func (d *QuestionDialog) respond(resp PermissionResponse) {
	if d.responseCh != nil {
		d.responseCh <- resp
		d.responseCh = nil
	}
	d.visible = false
}

func (d *QuestionDialog) ForceClose() {
	d.respond(PermissionDeny)
}

// dismissWithoutResponse hides presentation while its owner retains the
// runtime request. The coordinator owns the detached response channel.
func (d *QuestionDialog) dismissWithoutResponse() {
	d.responseCh = nil
	d.visible = false
}

// Overlay renders the question dialog as a full-width view.
func (d *QuestionDialog) Overlay(base string, width, height int) string {
	d.geometry = modalFrameGeometry{}
	d.width = width
	d.height = height

	if width <= 0 || height <= 0 {
		return ""
	}
	if len(d.questions) == 0 {
		return base
	}
	profile := d.environment.normalized().profile

	dim := d.styles.DialogHelp
	contentWidth := max(1, width-4)

	var lines []string

	// Top separator
	lines = append(lines, dim.Render(strings.Repeat("─", max(0, width))))

	// Tab bar
	lines = append(lines, d.renderTabBar())
	lines = append(lines, "")

	// Content area
	if d.view == viewSubmit {
		lines = append(lines, d.renderSubmitView(contentWidth)...)
	} else {
		lines = append(lines, d.renderQuestionView(contentWidth)...)
	}

	// Bottom separator + footer
	lines = append(lines, dim.Render(strings.Repeat("─", max(0, width))))
	if d.view == viewQuestion && d.currentIdx < len(d.questions) {
		footer := "Enter to select · Tab/Arrow keys to navigate · Esc to cancel"
		lines = append(lines, " "+dim.Render(footer))
	}

	view, geometry := modalTopFrame(profile, lines, width, height)
	d.geometry = geometry
	return view
}

func (d *QuestionDialog) renderTabBar() string {
	tabStyle := d.styles.Subtle
	activeTab := d.styles.DialogTitle.Bold(true)
	checkStyle := d.styles.ToolSuccess

	var tabs []string
	for i, q := range d.questions {
		label := q.Header
		if label == "" {
			label = fmt.Sprintf("Q%d", i+1)
		}

		_, answered := d.answers[q.Question]
		check := "☐"
		if answered {
			check = checkStyle.Render("☒")
		}

		if i == d.currentIdx && d.view == viewQuestion {
			tabs = append(tabs, check+" "+activeTab.Render(label))
		} else {
			tabs = append(tabs, check+" "+tabStyle.Render(label))
		}
	}

	// Submit tab
	submitLabel := "Submit"
	if d.view == viewSubmit {
		tabs = append(tabs, checkStyle.Render("✔")+" "+activeTab.Render(submitLabel))
	} else {
		tabs = append(tabs, "✔"+" "+tabStyle.Render(submitLabel))
	}

	arrows := d.styles.DialogHelp
	return " " + arrows.Render("←") + "  " + strings.Join(tabs, "  ") + "  " + arrows.Render("→")
}

func (d *QuestionDialog) renderQuestionView(width int) []string {
	q := d.questions[d.currentIdx]
	bold := d.styles.Bold
	dim := d.styles.DialogHelp
	selected := d.styles.ToolSuccess.Bold(true)
	check := d.styles.ToolSuccess

	var lines []string

	// Question text
	qLines := d.environment.normalized().profile.wrapAt(
		q.Question,
		max(1, width-4),
		false,
		1,
	)
	for _, l := range qLines {
		lines = append(lines, " "+bold.Render(l))
	}
	lines = append(lines, "")

	currentAnswer := d.answers[q.Question]

	// Options
	for i, opt := range q.Options {
		prefix := "  "
		label := fmt.Sprintf("%d. %s", i+1, opt.Label)
		if i == d.optionIdx && !d.inOther {
			prefix = "❯ "
			label = selected.Render(label)
		}

		if currentAnswer == opt.Label {
			label += " " + check.Render("✔")
		}
		lines = append(lines, " "+prefix+label)

		if opt.Description != "" {
			lines = append(lines, "      "+dim.Render(opt.Description))
		}
	}

	// "Type something" option
	otherIdx := len(q.Options)
	otherPrefix := "  "
	otherLabel := fmt.Sprintf("%d. Type something.", otherIdx+1)
	if d.optionIdx == otherIdx && !d.inOther {
		otherPrefix = "❯ "
		otherLabel = selected.Render(otherLabel)
	}
	lines = append(lines, " "+otherPrefix+otherLabel)

	// Other text input
	if d.inOther {
		lines = append(lines, "      "+d.styles.EditorPrompt.Render(d.otherText+"▊"))
	}

	// "Chat about this" option (below separator)
	lines = append(lines, dim.Render(" "+strings.Repeat("─", max(0, width-2))))
	chatIdx := otherIdx + 1
	chatPrefix := "  "
	chatLabel := fmt.Sprintf("%d. Chat about this", otherIdx+2)
	if d.optionIdx == chatIdx && !d.inOther {
		chatPrefix = "❯ "
		chatLabel = selected.Render(chatLabel)
	}
	_ = chatIdx
	lines = append(lines, " "+chatPrefix+chatLabel)

	return lines
}

func (d *QuestionDialog) renderSubmitView(width int) []string {
	bold := d.styles.Bold
	dim := d.styles.DialogHelp
	selected := d.styles.ToolSuccess.Bold(true)
	answerStyle := d.styles.AuroraSky

	var lines []string
	lines = append(lines, " "+bold.Render("Review your answers"))
	lines = append(lines, "")

	for _, q := range d.questions {
		qText := modalEllipsize(
			d.environment.normalized().profile,
			q.Question,
			max(1, width-4),
			3,
			"…",
		)
		answer, ok := d.answers[q.Question]
		if !ok {
			answer = "(not answered)"
		}
		lines = append(lines, " "+dim.Render("● ")+bold.Render(qText))
		lines = append(lines, "   → "+answerStyle.Render(answer))
	}

	lines = append(lines, "")
	lines = append(lines, " "+dim.Render("Ready to submit your answers?"))
	lines = append(lines, "")

	// Submit / Cancel options
	for i, label := range []string{"Submit answers", "Cancel"} {
		prefix := "  "
		renderedLabel := fmt.Sprintf("%d. %s", i+1, label)
		if i == d.submitIdx {
			prefix = "❯ "
			renderedLabel = selected.Render(renderedLabel)
		}
		lines = append(lines, " "+prefix+renderedLabel)
	}

	return lines
}
