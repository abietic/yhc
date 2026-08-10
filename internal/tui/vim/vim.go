// Package vim implements a basic vim-style modal text input for the TUI.
// It provides Normal, Insert, and Visual modes with core vim keybindings
// integrated into the Bubble Tea model lifecycle.
package vim

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Mode represents the current vim editing mode.
type Mode int

const (
	ModeNormal Mode = iota
	ModeInsert
	ModeVisual
)

func (m Mode) String() string {
	switch m {
	case ModeNormal:
		return "NORMAL"
	case ModeInsert:
		return "INSERT"
	case ModeVisual:
		return "VISUAL"
	default:
		return "UNKNOWN"
	}
}

// Model is a vim-style text input model for Bubble Tea.
type Model struct {
	mode       Mode
	buffer     []rune
	cursor     int
	visualMark int
	yankBuf    string
	enabled    bool
}

// New creates a new vim Model in disabled state.
func New() Model {
	return Model{
		mode:   ModeNormal,
		buffer: make([]rune, 0),
	}
}

// Enable activates vim mode.
func (m *Model) Enable() {
	m.enabled = true
	m.mode = ModeNormal
}

// Disable deactivates vim mode (passes through keys as normal input).
func (m *Model) Disable() {
	m.enabled = false
	m.mode = ModeInsert
}

// IsEnabled returns whether vim mode is active.
func (m Model) IsEnabled() bool { return m.enabled }

// Mode returns the current mode.
func (m Model) GetMode() Mode { return m.mode }

// Value returns the current buffer text.
func (m Model) Value() string { return string(m.buffer) }

// SetValue replaces the buffer contents.
func (m *Model) SetValue(s string) {
	m.buffer = []rune(s)
	if m.cursor > len(m.buffer) {
		m.cursor = len(m.buffer)
	}
}

// Cursor returns the current cursor position.
func (m Model) Cursor() int { return m.cursor }

// SetCursor moves the logical rune cursor, clamped to the current buffer.
func (m *Model) SetCursor(position int) {
	if position < 0 {
		position = 0
	}
	if position > len(m.buffer) {
		position = len(m.buffer)
	}
	m.cursor = position
}

// Update processes a key message and returns the updated model and any
// commands to execute. Returns true if the key was consumed.
func (m *Model) Update(msg tea.KeyPressMsg) (consumed bool, cmd tea.Cmd) {
	if !m.enabled {
		return false, nil
	}

	switch m.mode {
	case ModeNormal:
		return m.updateNormal(msg)
	case ModeInsert:
		return m.updateInsert(msg)
	case ModeVisual:
		return m.updateVisual(msg)
	}
	return false, nil
}

func (m *Model) updateNormal(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	switch msg.String() {
	case "i":
		m.mode = ModeInsert
		return true, nil
	case "I":
		m.mode = ModeInsert
		m.cursor = lineStart(m.buffer, m.cursor)
		return true, nil
	case "a":
		m.mode = ModeInsert
		if m.cursor < len(m.buffer) {
			m.cursor++
		}
		return true, nil
	case "A":
		m.mode = ModeInsert
		m.cursor = lineEnd(m.buffer, m.cursor)
		return true, nil
	case "o":
		m.mode = ModeInsert
		end := lineEnd(m.buffer, m.cursor)
		m.buffer = append(m.buffer[:end], append([]rune{'\n'}, m.buffer[end:]...)...)
		m.cursor = end + 1
		return true, nil
	case "O":
		m.mode = ModeInsert
		start := lineStart(m.buffer, m.cursor)
		m.buffer = append(m.buffer[:start], append([]rune{'\n'}, m.buffer[start:]...)...)
		m.cursor = start
		return true, nil
	case "v":
		m.mode = ModeVisual
		m.visualMark = m.cursor
		return true, nil
	case "h", "left":
		if m.cursor > 0 {
			m.cursor--
		}
		return true, nil
	case "l", "right":
		if m.cursor < len(m.buffer)-1 {
			m.cursor++
		}
		return true, nil
	case "j", "down":
		m.cursor = nextLine(m.buffer, m.cursor)
		return true, nil
	case "k", "up":
		m.cursor = prevLine(m.buffer, m.cursor)
		return true, nil
	case "w":
		m.cursor = nextWord(m.buffer, m.cursor)
		return true, nil
	case "b":
		m.cursor = prevWord(m.buffer, m.cursor)
		return true, nil
	case "0":
		m.cursor = lineStart(m.buffer, m.cursor)
		return true, nil
	case "$":
		m.cursor = lineEnd(m.buffer, m.cursor)
		if m.cursor > 0 && m.cursor == len(m.buffer) {
			m.cursor--
		}
		return true, nil
	case "x":
		if m.cursor < len(m.buffer) {
			m.yankBuf = string(m.buffer[m.cursor : m.cursor+1])
			m.buffer = append(m.buffer[:m.cursor], m.buffer[m.cursor+1:]...)
			if m.cursor >= len(m.buffer) && m.cursor > 0 {
				m.cursor--
			}
		}
		return true, nil
	case "d":
		// dd handled by waiting for next key (simplified: delete line)
		return true, nil
	case "y":
		// yy handled by waiting for next key (simplified: yank line)
		return true, nil
	case "p":
		if m.yankBuf != "" {
			pos := m.cursor + 1
			if pos > len(m.buffer) {
				pos = len(m.buffer)
			}
			runes := []rune(m.yankBuf)
			m.buffer = append(m.buffer[:pos], append(runes, m.buffer[pos:]...)...)
			m.cursor = pos + len(runes) - 1
		}
		return true, nil
	case "G":
		m.cursor = len(m.buffer) - 1
		if m.cursor < 0 {
			m.cursor = 0
		}
		return true, nil
	case "g":
		// gg: go to beginning (simplified)
		m.cursor = 0
		return true, nil
	}
	return true, nil
}

func (m *Model) updateInsert(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = ModeNormal
		if m.cursor > 0 && m.cursor >= len(m.buffer) {
			m.cursor--
		}
		return true, nil
	case "backspace":
		if m.cursor > 0 {
			m.buffer = append(m.buffer[:m.cursor-1], m.buffer[m.cursor:]...)
			m.cursor--
		}
		return true, nil
	case "enter":
		m.buffer = append(m.buffer[:m.cursor], append([]rune{'\n'}, m.buffer[m.cursor:]...)...)
		m.cursor++
		return true, nil
	default:
		if msg.Text != "" {
			for _, r := range msg.Text {
				m.buffer = append(m.buffer[:m.cursor], append([]rune{r}, m.buffer[m.cursor:]...)...)
				m.cursor++
			}
			return true, nil
		}
	}
	return false, nil
}

func (m *Model) updateVisual(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = ModeNormal
		return true, nil
	case "h", "left":
		if m.cursor > 0 {
			m.cursor--
		}
		return true, nil
	case "l", "right":
		if m.cursor < len(m.buffer)-1 {
			m.cursor++
		}
		return true, nil
	case "j", "down":
		m.cursor = nextLine(m.buffer, m.cursor)
		return true, nil
	case "k", "up":
		m.cursor = prevLine(m.buffer, m.cursor)
		return true, nil
	case "y":
		start, end := m.selectionRange()
		m.yankBuf = string(m.buffer[start:end])
		m.mode = ModeNormal
		return true, nil
	case "d", "x":
		start, end := m.selectionRange()
		m.yankBuf = string(m.buffer[start:end])
		m.buffer = append(m.buffer[:start], m.buffer[end:]...)
		m.cursor = start
		if m.cursor >= len(m.buffer) && m.cursor > 0 {
			m.cursor = len(m.buffer) - 1
		}
		m.mode = ModeNormal
		return true, nil
	}
	return true, nil
}

func (m Model) selectionRange() (int, int) {
	start, end := m.visualMark, m.cursor+1
	if start > end {
		start, end = m.cursor, m.visualMark+1
	}
	if end > len(m.buffer) {
		end = len(m.buffer)
	}
	return start, end
}

// Selection returns the currently selected text in visual mode.
func (m Model) Selection() string {
	if m.mode != ModeVisual {
		return ""
	}
	start, end := m.selectionRange()
	return string(m.buffer[start:end])
}

// DeleteLine deletes the current line (for dd in normal mode).
func (m *Model) DeleteLine() {
	start := lineStart(m.buffer, m.cursor)
	end := lineEnd(m.buffer, m.cursor)
	if end < len(m.buffer) {
		end++ // include newline
	}
	m.yankBuf = string(m.buffer[start:end])
	m.buffer = append(m.buffer[:start], m.buffer[end:]...)
	m.cursor = start
	if m.cursor >= len(m.buffer) && m.cursor > 0 {
		m.cursor = len(m.buffer) - 1
	}
}

// YankLine yanks the current line (for yy in normal mode).
func (m *Model) YankLine() {
	start := lineStart(m.buffer, m.cursor)
	end := lineEnd(m.buffer, m.cursor)
	if end < len(m.buffer) {
		end++
	}
	m.yankBuf = string(m.buffer[start:end])
}

// --- Movement helpers ---

func lineStart(buf []rune, pos int) int {
	for pos > 0 && buf[pos-1] != '\n' {
		pos--
	}
	return pos
}

func lineEnd(buf []rune, pos int) int {
	for pos < len(buf) && buf[pos] != '\n' {
		pos++
	}
	return pos
}

func nextLine(buf []rune, pos int) int {
	col := pos - lineStart(buf, pos)
	end := lineEnd(buf, pos)
	if end >= len(buf) {
		return pos
	}
	nextStart := end + 1
	nextEnd := lineEnd(buf, nextStart)
	lineLen := nextEnd - nextStart
	target := nextStart + col
	if col > lineLen {
		target = nextEnd
	}
	if target >= len(buf) {
		return len(buf) - 1
	}
	return target
}

func prevLine(buf []rune, pos int) int {
	col := pos - lineStart(buf, pos)
	start := lineStart(buf, pos)
	if start == 0 {
		return pos
	}
	prevEnd := start - 1
	prevStart := lineStart(buf, prevEnd)
	lineLen := prevEnd - prevStart
	target := prevStart + col
	if col > lineLen {
		target = prevEnd
	}
	return target
}

func nextWord(buf []rune, pos int) int {
	n := len(buf)
	if pos >= n-1 {
		return pos
	}
	pos++
	for pos < n && !isWordChar(buf[pos]) {
		pos++
	}
	for pos < n && isWordChar(buf[pos]) {
		pos++
	}
	if pos > n-1 {
		pos = n - 1
	}
	return pos
}

func prevWord(buf []rune, pos int) int {
	if pos <= 0 {
		return 0
	}
	pos--
	for pos > 0 && !isWordChar(buf[pos]) {
		pos--
	}
	for pos > 0 && isWordChar(buf[pos-1]) {
		pos--
	}
	return pos
}

func isWordChar(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') || r == '_'
}

// StatusLine returns a vim-style mode indicator for the status bar.
func (m Model) StatusLine() string {
	if !m.enabled {
		return ""
	}
	var parts []string
	parts = append(parts, "-- "+m.mode.String()+" --")
	return strings.Join(parts, " ")
}
