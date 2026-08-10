// Package terminalcap centralizes terminal feature detection for the TUI.
package terminalcap

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"golang.org/x/term"

	"github.com/abietic/yhc/internal/identity"
)

// ColorProfile describes the highest color level the terminal advertises.
type ColorProfile string

const (
	ColorNone      ColorProfile = "none"
	ColorANSI16    ColorProfile = "ansi16"
	ColorANSI256   ColorProfile = "ansi256"
	ColorTrueColor ColorProfile = "truecolor"
)

// ImageProtocol identifies an inline-image protocol advertised by the terminal.
type ImageProtocol string

const (
	ImageNone  ImageProtocol = "none"
	ImageITerm ImageProtocol = "iterm2"
	ImageKitty ImageProtocol = "kitty"
)

// ProbeOptions supplies deterministic inputs to Probe.
type ProbeOptions struct {
	GOOS           string
	Env            map[string]string
	StdinTTY       bool
	StdoutTTY      bool
	MouseRequested bool
}

// Capabilities is the immutable terminal capability snapshot used at startup.
// Runtime focus observations live in FocusState because they change over time.
type Capabilities struct {
	Platform string
	Terminal string

	Interactive       bool
	SSH               bool
	Multiplexer       string
	EnhancedKeys      bool
	EnhancedKeysInUse bool
	FocusReporting    bool
	Color             ColorProfile
	Hyperlinks        bool
	Images            ImageProtocol
	Mouse             bool
	BracketedPaste    bool
	SuspendResume     bool
}

// Current probes the current process environment and terminal descriptors.
func Current(mouseRequested bool) Capabilities {
	env := make(map[string]string)
	for _, pair := range os.Environ() {
		key, value, ok := strings.Cut(pair, "=")
		if ok {
			env[key] = value
		}
	}
	return Probe(ProbeOptions{
		GOOS:           runtime.GOOS,
		Env:            env,
		StdinTTY:       term.IsTerminal(int(os.Stdin.Fd())),
		StdoutTTY:      term.IsTerminal(int(os.Stdout.Fd())),
		MouseRequested: mouseRequested,
	})
}

// Probe derives a conservative capability snapshot from platform, TTY, and
// terminal environment facts. Features that require an active response are
// enabled only when their input parser can consume that response safely.
func Probe(opts ProbeOptions) Capabilities {
	env := opts.Env
	if env == nil {
		env = map[string]string{}
	}
	goos := opts.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}

	termName := detectTerminal(env)
	termType := strings.ToLower(env["TERM"])
	dumb := termType == "dumb"
	interactive := opts.StdinTTY && opts.StdoutTTY && !dumb
	multiplexer := ""
	switch {
	case env["TMUX"] != "" || env["TMUX_PANE"] != "":
		multiplexer = "tmux"
	case env["STY"] != "":
		multiplexer = "screen"
	}

	caps := Capabilities{
		Platform:       goos,
		Terminal:       termName,
		Interactive:    interactive,
		SSH:            env["SSH_CONNECTION"] != "" || env["SSH_TTY"] != "",
		Multiplexer:    multiplexer,
		Color:          detectColor(env, termName, dumb),
		BracketedPaste: interactive,
		SuspendResume:  interactive && goos != "windows",
	}

	// Bubble Tea v1 does not fully parse Kitty CSI-u reports, so record support
	// without enabling the protocol. This avoids corrupt input over SSH/xterm.js.
	caps.EnhancedKeys = multiplexer == "tmux" || oneOf(termName,
		"iterm2", "kitty", "wezterm", "ghostty", "windows-terminal")
	caps.EnhancedKeysInUse = false

	// DECSET 1004 is safe to request from normal interactive terminals. Support
	// becomes trustworthy only after FocusMsg/BlurMsg is observed.
	caps.FocusReporting = interactive
	caps.Hyperlinks = interactive && oneOf(termName,
		"iterm2", "kitty", "wezterm", "ghostty", "vscode", "warp",
		"windows-terminal", "vte", "konsole")
	switch termName {
	case "iterm2":
		caps.Images = ImageITerm
	case "kitty", "wezterm", "ghostty":
		caps.Images = ImageKitty
	default:
		caps.Images = ImageNone
	}
	caps.Mouse = interactive && opts.MouseRequested && !envTruthy(runtimeEnvironmentValue(env, identity.RuntimeEnvDisableMouse))
	return caps
}

func runtimeEnvironmentValue(env map[string]string, name identity.RuntimeEnvName) string {
	pair := name.Pair()
	if value, present := env[pair.Canonical]; present {
		return value
	}
	return env[pair.Legacy]
}

func detectTerminal(env map[string]string) string {
	if env["KITTY_WINDOW_ID"] != "" {
		return "kitty"
	}
	if env["WT_SESSION"] != "" {
		return "windows-terminal"
	}
	program := strings.ToLower(env["TERM_PROGRAM"])
	switch {
	case program == "apple_terminal":
		return "apple-terminal"
	case program == "iterm.app":
		return "iterm2"
	case program == "wezterm":
		return "wezterm"
	case program == "vscode":
		return "vscode"
	case strings.Contains(program, "ghostty"):
		return "ghostty"
	case strings.Contains(program, "warp"):
		return "warp"
	}
	if env["VTE_VERSION"] != "" {
		return "vte"
	}
	if env["KONSOLE_VERSION"] != "" {
		return "konsole"
	}
	termType := strings.ToLower(env["TERM"])
	for _, candidate := range []string{"kitty", "wezterm", "ghostty", "alacritty"} {
		if strings.Contains(termType, candidate) {
			return candidate
		}
	}
	if termType == "dumb" {
		return "dumb"
	}
	if termType != "" {
		return termType
	}
	return "unknown"
}

func detectColor(env map[string]string, terminalName string, dumb bool) ColorProfile {
	if dumb || env["NO_COLOR"] != "" {
		return ColorNone
	}
	colorTerm := strings.ToLower(env["COLORTERM"])
	if colorTerm == "truecolor" || colorTerm == "24bit" || oneOf(terminalName,
		"iterm2", "kitty", "wezterm", "ghostty", "warp", "windows-terminal") {
		return ColorTrueColor
	}
	if strings.Contains(strings.ToLower(env["TERM"]), "256color") {
		return ColorANSI256
	}
	return ColorANSI16
}

func oneOf(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if value == candidate {
			return true
		}
	}
	return false
}

func envTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// Summary returns stable, user-facing diagnostics for /terminal.
func (c Capabilities) Summary(focus FocusStatus) string {
	location := "local"
	if c.SSH {
		location = "ssh"
	}
	interactive := "non-interactive"
	if c.Interactive {
		interactive = "interactive"
	}
	enhanced := "unavailable"
	if c.EnhancedKeys {
		enhanced = "available; compatibility mode active"
	}
	focusText := "unavailable"
	if c.FocusReporting {
		focusText = "requested; state=" + string(focus)
	}
	mouse := "disabled"
	if c.Mouse {
		mouse = "cell-motion"
	}
	multiplexer := "none"
	if c.Multiplexer != "" {
		multiplexer = c.Multiplexer
	}
	lines := []string{
		"Terminal Capabilities",
		fmt.Sprintf("  Platform:        %s (%s, %s)", c.Platform, location, interactive),
		fmt.Sprintf("  Terminal:        %s", c.Terminal),
		fmt.Sprintf("  Multiplexer:     %s", multiplexer),
		fmt.Sprintf("  Color:           %s", c.Color),
		fmt.Sprintf("  Enhanced keys:   %s", enhanced),
		fmt.Sprintf("  Focus reporting: %s", focusText),
		fmt.Sprintf("  Hyperlinks:      %s", yesNo(c.Hyperlinks)),
		fmt.Sprintf("  Inline images:   %s", c.Images),
		fmt.Sprintf("  Mouse:           %s", mouse),
		fmt.Sprintf("  Bracketed paste: %s", yesNo(c.BracketedPaste)),
		fmt.Sprintf("  Suspend/resume:  %s", yesNo(c.SuspendResume)),
	}
	return strings.Join(lines, "\n")
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}
