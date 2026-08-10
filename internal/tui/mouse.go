package tui

import tea "charm.land/bubbletea/v2"

type mouseAction uint8

const (
	mouseActionPress mouseAction = iota
	mouseActionRelease
	mouseActionMotion
)

// tuiMouseMsg is the presentation-facing mouse event. Bubble Tea v2 exposes
// distinct click, release, wheel, and motion messages; normalizing them once
// keeps selection and dialog routing independent of the terminal decoder.
type tuiMouseMsg struct {
	X, Y   int
	Button tea.MouseButton
	Action mouseAction
	Shift  bool
}

func (m tuiMouseMsg) String() string {
	return m.Mouse().String()
}

func (m tuiMouseMsg) Mouse() tea.Mouse {
	var mod tea.KeyMod
	if m.Shift {
		mod |= tea.ModShift
	}
	return tea.Mouse{X: m.X, Y: m.Y, Button: m.Button, Mod: mod}
}

func normalizeMouseMsg(msg tea.MouseMsg) tuiMouseMsg {
	if event, ok := msg.(tuiMouseMsg); ok {
		return event
	}
	mouse := msg.Mouse()
	event := tuiMouseMsg{
		X:      mouse.X,
		Y:      mouse.Y,
		Button: mouse.Button,
		Shift:  mouse.Mod.Contains(tea.ModShift),
	}
	switch msg.(type) {
	case tea.MouseReleaseMsg:
		event.Action = mouseActionRelease
	case tea.MouseMotionMsg:
		event.Action = mouseActionMotion
	default:
		event.Action = mouseActionPress
	}
	return event
}
