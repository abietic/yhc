package tui

import (
	"math/rand"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// ChoiceFunc returns an index in [0, n). It is injectable so session choices
// and animation selection remain deterministic in tests.
type ChoiceFunc func(n int) int

func randomChoice() ChoiceFunc {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	return func(n int) int {
		if n <= 1 {
			return 0
		}
		return rng.Intn(n)
	}
}

func chooseIndex(choose ChoiceFunc, n int) int {
	if n <= 1 || choose == nil {
		return 0
	}
	i := choose(n)
	if i < 0 {
		i = -i
	}
	return i % n
}

// MascotPose is the Eno pose union. Celebrate replaces the legacy arms-up
// pose in click animations; blink, happy, and sleep are reserved static poses.
type MascotPose int

const (
	PoseDefault MascotPose = iota
	PoseLookLeft
	PoseLookRight
	PoseBlink
	PoseHappy
	PoseCelebrate
	PoseSleep
)

const (
	MascotWidth         = 15
	MascotHeight        = 6
	MascotFrameDuration = 60 * time.Millisecond
)

type enoTone int

const (
	enoToneTransparent enoTone = iota
	enoToneOutline
	enoToneBody
	enoToneFace
	enoToneSparkle
	enoToneSubtle
)

// enoGlyphTones is deliberately global across poses: one glyph never changes
// semantic tone between frames.
var enoGlyphTones = map[rune]enoTone{
	'▟': enoToneOutline, '▙': enoToneOutline, '▜': enoToneOutline, '▌': enoToneOutline,
	'▐': enoToneOutline, '▗': enoToneOutline, '▖': enoToneOutline, '▝': enoToneOutline,
	'▛': enoToneOutline, '▘': enoToneOutline,
	'█': enoToneBody,
	'●': enoToneFace, '▄': enoToneFace, '◡': enoToneFace, '─': enoToneFace,
	'✧': enoToneSparkle, '✦': enoToneSparkle,
	'z': enoToneSubtle,
}

// enoPoses is the fixed 15x6 Eno pose sheet.
var enoPoses = map[MascotPose][MascotHeight]string{
	PoseDefault: {
		"  ▟▙       ▟▙  ",
		"  ▜██▙   ▟██▌  ",
		" ▗███████████▖ ",
		" ▐██●█████●██▌ ",
		"▟██████●██████▙",
		"   ▝▜█████▛▘   ",
	},
	PoseLookLeft: {
		"  ▟▙       ▟▙  ",
		"  ▜██▙   ▟██▌  ",
		" ▗███████████▖ ",
		" ▐█●█████●███▌ ",
		"▟██████●██████▙",
		"   ▝▜█████▛▘   ",
	},
	PoseLookRight: {
		"  ▟▙       ▟▙  ",
		"  ▜██▙   ▟██▌  ",
		" ▗███████████▖ ",
		" ▐███●█████●█▌ ",
		"▟██████●██████▙",
		"   ▝▜█████▛▘   ",
	},
	PoseBlink: {
		"  ▟▙       ▟▙  ",
		"  ▜██▙   ▟██▌  ",
		" ▗███████████▖ ",
		" ▐██▄█████▄██▌ ",
		"▟██████●██████▙",
		"   ▝▜█████▛▘   ",
	},
	PoseHappy: {
		"  ▟▙       ▟▙  ",
		"  ▜██▙   ▟██▌  ",
		" ▗███████████▖ ",
		" ▐██◡█████◡██▌ ",
		"▟██████●██████▙",
		"   ▝▜█████▛▘   ",
	},
	PoseCelebrate: {
		"✧   ▟▙ ✦ ▟▙   ✧",
		"  ▜██▙   ▟██▌  ",
		" ▗███████████▖ ",
		" ▐██✦█████✦██▌ ",
		"▟██████●██████▙",
		"   ▝▜█████▛▘   ",
	},
	PoseSleep: {
		"  ▟▙       ▟▙ z",
		"  ▜██▙   ▟██▌ z",
		" ▗███████████▖ ",
		" ▐██─█████─██▌ ",
		"▟██████●██████▙",
		"   ▝▜█████▛▘   ",
	},
}

// enoTones contains only foreground styles. Face cells are intentionally
// absent so eyes and nose inherit the terminal's foreground and background.
type enoTones struct {
	body    lipgloss.Style
	outline lipgloss.Style
	sparkle lipgloss.Style
	subtle  lipgloss.Style
}

func (s Styles) enoTones() enoTones {
	return enoTones{
		body:    s.EnoBody,
		outline: s.EnoOutline,
		sparkle: lipgloss.NewStyle().Foreground(s.DialogTitle.GetForeground()),
		subtle:  s.Subtle,
	}
}

func (t enoTones) styleFor(tone enoTone) lipgloss.Style {
	switch tone {
	case enoToneBody:
		return t.body
	case enoToneOutline:
		return t.outline
	case enoToneSparkle:
		return t.sparkle
	case enoToneSubtle:
		return t.subtle
	default:
		return lipgloss.Style{}
	}
}

// RenderMascot returns the 15x6 Eno artwork using Polar Night tones.
func RenderMascot(pose MascotPose) [MascotHeight]string {
	return renderMascotStyled(pose, StylesForTheme(ThemePolarNight).enoTones())
}

func renderMascotStyled(pose MascotPose, tones enoTones) [MascotHeight]string {
	art, ok := enoPoses[pose]
	if !ok {
		art = enoPoses[PoseDefault]
	}
	var rendered [MascotHeight]string
	for i, row := range art {
		rendered[i] = renderEnoRow(row, tones)
	}
	return rendered
}

func renderEnoRow(row string, tones enoTones) string {
	var rendered strings.Builder
	runTone := enoToneTransparent
	var run []rune
	flush := func() {
		if len(run) == 0 {
			return
		}
		if runTone == enoToneTransparent || runTone == enoToneFace {
			// Lip Gloss v2 leaves empty styles unwrapped. Reset explicitly so
			// face and whitespace cells cannot inherit the preceding semantic
			// foreground from the terminal stream.
			rendered.WriteString("\x1b[0m")
			rendered.WriteString(string(run))
		} else {
			rendered.WriteString(tones.styleFor(runTone).Render(string(run)))
		}
		run = run[:0]
	}
	for _, glyph := range row {
		tone, ok := enoGlyphTones[glyph]
		if !ok {
			tone = enoToneTransparent
		}
		if tone != runTone {
			flush()
			runTone = tone
		}
		run = append(run, glyph)
	}
	flush()
	return rendered.String()
}

// Frame is one animation frame. Offset 1 crouches Eno inside a fixed six-row
// viewport, shifting the art down and clipping only the chin row.
type Frame struct {
	Pose   MascotPose
	Offset int
}

func hold(pose MascotPose, offset, n int) []Frame {
	frames := make([]Frame, n)
	for i := range frames {
		frames[i] = Frame{Pose: pose, Offset: offset}
	}
	return frames
}

func mergeFrames(groups ...[]Frame) []Frame {
	var frames []Frame
	for _, group := range groups {
		frames = append(frames, group...)
	}
	return frames
}

var jumpWave = mergeFrames(
	hold(PoseDefault, 1, 2),
	hold(PoseCelebrate, 0, 3),
	hold(PoseDefault, 0, 1),
	hold(PoseDefault, 1, 2),
	hold(PoseCelebrate, 0, 3),
	hold(PoseDefault, 0, 1),
)

var lookAround = mergeFrames(
	hold(PoseLookRight, 0, 5),
	hold(PoseLookLeft, 0, 5),
	hold(PoseDefault, 0, 1),
)

var clickAnimations = [][]Frame{jumpWave, lookAround}

type mascotSequenceKind int

const (
	mascotSequenceNone mascotSequenceKind = iota
	mascotSequenceClick
	mascotSequenceIdle
)

type MascotAnimator struct {
	choose   ChoiceFunc
	sequence []Frame
	kind     mascotSequenceKind
	index    int
}

func NewMascotAnimator(choose ...ChoiceFunc) *MascotAnimator {
	var chooser ChoiceFunc
	if len(choose) > 0 {
		chooser = choose[0]
	}
	if chooser == nil {
		chooser = randomChoice()
	}
	return &MascotAnimator{choose: chooser, index: -1}
}

func (m *MascotAnimator) Active() bool {
	return m != nil && m.index >= 0 && m.index < len(m.sequence)
}

func (m *MascotAnimator) CurrentFrame() Frame {
	if !m.Active() {
		return Frame{Pose: PoseDefault}
	}
	return m.sequence[m.index]
}

func (m *MascotAnimator) CurrentPose() MascotPose { return m.CurrentFrame().Pose }
func (m *MascotAnimator) CurrentOffset() int      { return m.CurrentFrame().Offset }

// TriggerAnimation starts a click animation. An active click keeps ownership;
// an idle sequence is replaced and continues on its already-pending frame tick.
func (m *MascotAnimator) TriggerAnimation() tea.Cmd {
	if m == nil || (m.Active() && m.kind == mascotSequenceClick) {
		return nil
	}
	preemptingIdle := m.Active()
	m.sequence = clickAnimations[chooseIndex(m.choose, len(clickAnimations))]
	m.kind = mascotSequenceClick
	m.index = 0
	if preemptingIdle {
		return nil
	}
	return MascotTick()
}

// TriggerIdle starts an idle sequence only when no animation owns the mascot.
func (m *MascotAnimator) TriggerIdle(sequence []Frame) tea.Cmd {
	if m == nil || m.Active() || len(sequence) == 0 {
		return nil
	}
	m.sequence = sequence
	m.kind = mascotSequenceIdle
	m.index = 0
	return MascotTick()
}

func (m *MascotAnimator) IdleActive() bool {
	return m.Active() && m.kind == mascotSequenceIdle
}

func (m *MascotAnimator) Tick() tea.Cmd {
	if !m.Active() {
		return nil
	}
	m.index++
	if m.index >= len(m.sequence) {
		m.Stop()
		return nil
	}
	return MascotTick()
}

func (m *MascotAnimator) Stop() {
	if m == nil {
		return
	}
	m.sequence = nil
	m.kind = mascotSequenceNone
	m.index = -1
}

type mascotTickMsg struct{}

func MascotTick() tea.Cmd {
	return tea.Tick(MascotFrameDuration, func(time.Time) tea.Msg { return mascotTickMsg{} })
}
