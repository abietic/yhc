package tui

import (
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
)

// SideQuestionDialogAction is a presentation-only request to the App. The App
// remains responsible for starting, cancelling, and staging engine state.
type SideQuestionDialogAction uint8

const (
	sideQuestionDialogNone SideQuestionDialogAction = iota
	sideQuestionDialogAsk
	sideQuestionDialogCancel
	sideQuestionDialogStage
)

const sideQuestionDialogMaxBytes = 4096

// SideQuestionDialog projects one engine-owned side question without issuing a
// provider request itself.
type SideQuestionDialog struct {
	visible     bool
	question    string
	feedback    string
	snapshot    engine.SideQuestionSnapshot
	scroll      int
	styles      Styles
	environment RenderEnvironment
	geometry    modalFrameGeometry
	answerBody  []string
	answerWidth int
}

func NewSideQuestionDialog(styles Styles) *SideQuestionDialog {
	return &SideQuestionDialog{styles: styles, environment: defaultRenderEnvironment(styles)}
}

func (d *SideQuestionDialog) SetRenderEnvironment(environment RenderEnvironment) {
	previous := d.environment.identity()
	d.environment = environment.normalized()
	d.styles = d.environment.styles
	if previous != d.environment.identity() {
		d.answerBody = nil
	}
}

// Show opens the one-line question editor. Explicit /btw arguments are still
// editable; the caller decides when to dispatch an Ask action.
func (d *SideQuestionDialog) Show(question string) {
	d.visible, d.scroll, d.feedback = true, 0, ""
	d.snapshot = engine.SideQuestionSnapshot{}
	d.answerBody = nil
	question, valid := sideQuestionDialogText(question)
	if !valid {
		d.question = ""
		d.feedback = "Question must be one line of at most 4096 valid UTF-8 bytes"
		return
	}
	d.question = question
}

func (d *SideQuestionDialog) SetSnapshot(snapshot engine.SideQuestionSnapshot) {
	d.snapshot = snapshot
	d.answerBody = nil
	d.feedback = ""
	if snapshot.Question != "" {
		d.question = snapshot.Question
	}
	d.scroll = 0
}

func (d *SideQuestionDialog) SetFeedback(feedback string) {
	d.feedback = sanitizeGenericHistoryText(feedback)
}

func (d *SideQuestionDialog) Close() {
	d.visible, d.scroll = false, 0
	d.snapshot = engine.SideQuestionSnapshot{}
	d.question, d.feedback = "", ""
	d.answerBody = nil
}

func (d *SideQuestionDialog) IsVisible() bool { return d.visible }

// HandlePaste accepts only a bounded, one-line value. It never lets terminal
// control bytes reach the final frame.
func (d *SideQuestionDialog) HandlePaste(value string) {
	if !d.visible || d.snapshot.Status != "" {
		return
	}
	value, valid := sideQuestionDialogText(value)
	if !valid || len(d.question)+len(value) > sideQuestionDialogMaxBytes {
		d.feedback = "Question must be one line of at most 4096 valid UTF-8 bytes"
		return
	}
	d.question += value
	d.feedback = ""
}

func (d *SideQuestionDialog) HandleKey(msg tea.KeyPressMsg) (SideQuestionDialogAction, string) {
	if !d.visible {
		return sideQuestionDialogNone, ""
	}
	switch d.snapshot.Status {
	case engine.SideQuestionPending, engine.SideQuestionRunning:
		if msg.String() == "esc" || msg.String() == "ctrl+c" {
			d.Close()
			return sideQuestionDialogCancel, ""
		}
		return sideQuestionDialogNone, ""
	case engine.SideQuestionAnswered:
		if d.handleAnswerScroll(msg.String()) {
			return sideQuestionDialogNone, ""
		}
		switch msg.String() {
		case "enter":
			return sideQuestionDialogStage, ""
		case "esc", "ctrl+c":
			d.Close()
			return sideQuestionDialogCancel, ""
		}
		return sideQuestionDialogNone, ""
	case engine.SideQuestionFailed, engine.SideQuestionCancelled:
		switch msg.String() {
		case "enter":
			if d.question == "" {
				return sideQuestionDialogNone, ""
			}
			return sideQuestionDialogAsk, d.question
		case "esc", "ctrl+c":
			d.Close()
			return sideQuestionDialogCancel, ""
		}
		return sideQuestionDialogNone, ""
	}

	switch msg.String() {
	case "enter":
		question := strings.TrimSpace(d.question)
		if question == "" {
			d.feedback = "Enter a question first"
			return sideQuestionDialogNone, ""
		}
		return sideQuestionDialogAsk, question
	case "esc", "ctrl+c":
		d.Close()
		return sideQuestionDialogCancel, ""
	case "backspace":
		if len(d.question) > 0 {
			_, size := utf8.DecodeLastRuneInString(d.question)
			d.question = d.question[:len(d.question)-size]
		}
		return sideQuestionDialogNone, ""
	case "ctrl+u":
		d.question, d.feedback = "", ""
		return sideQuestionDialogNone, ""
	}
	if msg.Text != "" {
		d.HandlePaste(msg.Text)
	}
	return sideQuestionDialogNone, ""
}

func (d *SideQuestionDialog) handleAnswerScroll(key string) bool {
	switch key {
	case "up", "k":
		d.scroll = max(0, d.scroll-1)
	case "down", "j":
		d.scroll++
	case "pgup":
		d.scroll = max(0, d.scroll-6)
	case "pgdown":
		d.scroll += 6
	case "home":
		d.scroll = 0
	case "end":
		d.scroll = 1 << 30
	default:
		return false
	}
	return true
}

func sideQuestionDialogText(value string) (string, bool) {
	if !utf8.ValidString(value) || strings.ContainsFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || isUnsafeDisplayCellRune(r)
	}) {
		return "", false
	}
	return value, len(value) <= sideQuestionDialogMaxBytes
}

// Overlay renders a terminal-safe bounded frame. The base is intentionally
// replaced by the top modal, matching the existing Question dialog contract.
func (d *SideQuestionDialog) Overlay(base string, width, height int) string {
	d.geometry = modalFrameGeometry{}
	if !d.visible {
		return base
	}
	if width <= 0 || height <= 0 {
		return ""
	}
	profile := d.environment.normalized().profile
	contentWidth := max(1, width-2)
	lines := []string{d.styles.DialogTitle.Bold(true).Render(" Side question")}
	lines = append(lines, d.styles.DialogHelp.Render(" "+modalEllipsize(profile, "One model call · no tools", contentWidth, 1, "…")))
	lines = append(lines, " "+d.styles.Subtle.Render("Question: ")+d.styles.DialogInputText.Render(modalEllipsize(profile, sideQuestionDialogLiteral(d.question), contentWidth-11, 11, "…")))

	status := d.statusLine()
	if status != "" {
		lines = append(lines, " "+status)
	}
	if d.snapshot.Status == engine.SideQuestionAnswered {
		lines = append(lines, d.answerLines(profile, contentWidth, height)...)
	}
	if d.feedback != "" {
		lines = append(lines, " "+d.styles.Warning.Render(modalEllipsize(profile, d.feedback, contentWidth, 1, "…")))
	}
	lines = append(lines, d.footer())
	view, geometry := modalTopFrame(profile, lines, width, height)
	d.geometry = geometry
	return view
}

func (d *SideQuestionDialog) statusLine() string {
	if d.snapshot.Status == "" {
		return d.styles.DialogHelp.Render(" Enter asks · Esc cancels")
	}
	status := string(d.snapshot.Status)
	if d.snapshot.AnswerTruncated {
		status += " (answer clipped)"
	}
	if d.snapshot.Reason != "" {
		status += ": " + sideQuestionDialogLiteral(d.snapshot.Reason)
	}
	if d.snapshot.Status == engine.SideQuestionPending {
		status += " · request accepted"
	}
	return d.styles.Subtle.Render(" Status: " + status)
}

func (d *SideQuestionDialog) answerLines(profile DisplayCellProfile, width, height int) []string {
	metadata := " Snapshot · " + itoa(d.snapshot.ContextEntries) + " items"
	if !d.snapshot.StartedAt.IsZero() {
		metadata = " As of " + d.snapshot.StartedAt.UTC().Format("15:04:05Z") + " · " + itoa(d.snapshot.ContextEntries) + " items"
		if width >= 78 {
			metadata = " As of " + d.snapshot.StartedAt.UTC().Format(time.RFC3339) + " · " + itoa(d.snapshot.ContextEntries) + " items"
		}
	}
	if d.snapshot.ContextTruncated {
		metadata += " · clipped"
	}
	lines := []string{d.styles.DialogHelp.Render(modalEllipsize(profile, metadata, width, 1, "…"))}
	if d.answerBody == nil || d.answerWidth != width {
		d.answerBody = profile.wrapAt(sideQuestionDialogLiteral(d.snapshot.Answer), max(1, width-2), false, 1)
		d.answerWidth = width
	}
	body := d.answerBody
	available := max(1, height-7)
	if d.feedback != "" {
		available = max(1, available-1)
	}
	maxOffset := max(0, len(body)-available)
	d.scroll = min(d.scroll, maxOffset)
	lines = append(lines, body[d.scroll:min(len(body), d.scroll+available)]...)
	if maxOffset > 0 {
		lines = append(lines, d.styles.DialogHelp.Render(" Answer scroll "+itoa(d.scroll+1)+"/"+itoa(maxOffset+1)))
	}
	return lines
}

func (d *SideQuestionDialog) footer() string {
	switch d.snapshot.Status {
	case engine.SideQuestionAnswered:
		return d.styles.DialogHelp.Render(" Enter add · Esc close · ↑↓ scroll")
	case engine.SideQuestionFailed, engine.SideQuestionCancelled:
		return d.styles.DialogHelp.Render(" Enter retries · Esc closes")
	case engine.SideQuestionPending, engine.SideQuestionRunning:
		return d.styles.DialogHelp.Render(" Esc cancels")
	default:
		return d.styles.DialogHelp.Render(" Enter asks · Esc cancels")
	}
}

func itoa(value int) string {
	return strconv.Itoa(value)
}

// sideQuestionDialogLiteral makes engine/user text inert before it enters the
// display profile: ANSI/OSC cannot execute and unsafe bidi/control runes are
// visibly replaced instead of changing terminal reading order.
func sideQuestionDialogLiteral(value string) string {
	value = sanitizeGenericHistoryText(value)
	return strings.Map(func(r rune) rune {
		if isUnsafeDisplayCellRune(r) {
			return unicode.ReplacementChar
		}
		return r
	}, value)
}
