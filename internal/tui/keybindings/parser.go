package keybindings

import (
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
)

// KeyPattern represents one keystroke in a possibly multi-step chord.
type KeyPattern struct {
	Key   string
	Ctrl  bool
	Alt   bool
	Shift bool
	Super bool
}

// ParseKeyPattern preserves the original convenience API. Invalid patterns
// return an empty value; config and resolver compilation use the strict parser.
func ParseKeyPattern(pattern string) KeyPattern {
	parsed, _ := ParseKeyPatternStrict(pattern)
	return parsed
}

// ParseKeyPatternStrict parses one keystroke such as ctrl+shift+k.
func ParseKeyPatternStrict(pattern string) (KeyPattern, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || strings.ContainsAny(pattern, " \t\r\n") {
		return KeyPattern{}, fmt.Errorf("invalid keystroke %q", pattern)
	}

	var parsed KeyPattern
	for _, raw := range strings.Split(strings.ToLower(pattern), "+") {
		part := strings.TrimSpace(raw)
		switch part {
		case "ctrl", "control":
			if parsed.Ctrl {
				return KeyPattern{}, fmt.Errorf("duplicate ctrl modifier in %q", pattern)
			}
			parsed.Ctrl = true
		case "alt", "meta", "opt", "option":
			if parsed.Alt {
				return KeyPattern{}, fmt.Errorf("duplicate alt modifier in %q", pattern)
			}
			parsed.Alt = true
		case "shift":
			if parsed.Shift {
				return KeyPattern{}, fmt.Errorf("duplicate shift modifier in %q", pattern)
			}
			parsed.Shift = true
		case "super", "cmd", "command":
			if parsed.Super {
				return KeyPattern{}, fmt.Errorf("duplicate super modifier in %q", pattern)
			}
			parsed.Super = true
		case "":
			return KeyPattern{}, fmt.Errorf("empty key segment in %q", pattern)
		default:
			if parsed.Key != "" {
				return KeyPattern{}, fmt.Errorf("multiple keys in keystroke %q", pattern)
			}
			parsed.Key = normalizeKeyName(part)
		}
	}
	if parsed.Key == "" {
		return KeyPattern{}, fmt.Errorf("keystroke %q has no key", pattern)
	}
	return parsed, nil
}

// ParseChord parses a whitespace-separated key chord.
func ParseChord(pattern string) ([]KeyPattern, error) {
	steps := strings.Fields(strings.TrimSpace(pattern))
	if len(steps) == 0 {
		return nil, fmt.Errorf("empty key pattern")
	}
	parsed := make([]KeyPattern, 0, len(steps))
	for _, step := range steps {
		key, err := ParseKeyPatternStrict(step)
		if err != nil {
			return nil, err
		}
		parsed = append(parsed, key)
	}
	return parsed, nil
}

// NormalizeKeyPattern canonicalizes aliases and modifier order for comparison.
func NormalizeKeyPattern(pattern string) (string, error) {
	chord, err := ParseChord(pattern)
	if err != nil {
		return "", err
	}
	parts := make([]string, len(chord))
	for i, step := range chord {
		parts[i] = step.String()
	}
	return strings.Join(parts, " "), nil
}

// MatchesKeyMsg reports whether this single step matches a Bubble Tea event.
func (kp KeyPattern) MatchesKeyMsg(msg tea.KeyPressMsg) bool {
	want := kp.String()
	got := canonicalKeyMsg(msg)
	if want == got {
		return true
	}

	// Terminals commonly encode Ctrl+M and Enter identically.
	return want == "enter" && (msg.Code == tea.KeyEnter || msg.Code == tea.KeyReturn)
}

// String returns the canonical representation of one keystroke.
func (kp KeyPattern) String() string {
	parts := make([]string, 0, 5)
	if kp.Ctrl {
		parts = append(parts, "ctrl")
	}
	if kp.Alt {
		parts = append(parts, "alt")
	}
	if kp.Shift {
		parts = append(parts, "shift")
	}
	if kp.Super {
		parts = append(parts, "super")
	}
	if kp.Key != "" {
		parts = append(parts, normalizeKeyName(kp.Key))
	}
	return strings.Join(parts, "+")
}

func canonicalKeyMsg(msg tea.KeyPressMsg) string {
	text := []rune(msg.Text)
	if len(text) == 1 {
		r := text[0]
		parts := make([]string, 0, 3)
		if msg.Mod.Contains(tea.ModAlt) {
			parts = append(parts, "alt")
		}
		if unicode.IsUpper(r) {
			parts = append(parts, "shift")
			r = unicode.ToLower(r)
		}
		parts = append(parts, strings.ToLower(string(r)))
		return strings.Join(parts, "+")
	}

	raw := strings.ToLower(strings.TrimSpace(msg.String()))
	if raw == "" {
		return ""
	}
	parsed, err := ParseKeyPatternStrict(raw)
	if err == nil {
		return parsed.String()
	}
	return normalizeKeyName(raw)
}

func normalizeKeyName(key string) string {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "esc":
		return "escape"
	case "pgup":
		return "pageup"
	case "pgdown", "pgdn":
		return "pagedown"
	case "return":
		return "enter"
	default:
		return strings.ToLower(strings.TrimSpace(key))
	}
}
