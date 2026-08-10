package tui

import (
	"fmt"
	"image/color"
	"strconv"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type tuiColor = color.Color

func tuiColorString(value tuiColor) string {
	switch value := value.(type) {
	case nil, lipgloss.NoColor:
		return ""
	case ansi.BasicColor:
		return strconv.Itoa(int(value))
	case ansi.IndexedColor:
		return strconv.Itoa(int(value))
	case color.RGBA:
		return fmt.Sprintf("#%02X%02X%02X", value.R, value.G, value.B)
	default:
		red, green, blue, alpha := value.RGBA()
		if alpha == 0 {
			return ""
		}
		return fmt.Sprintf("#%02X%02X%02X", uint8(red>>8), uint8(green>>8), uint8(blue>>8))
	}
}

const (
	systemIdentityGlyph    = "\u2727" // ✧ Revontuli outline star for system voice.
	assistantIdentityGlyph = "\u2726" // ✦ Revontuli filled star for agent voice.
)

// Styles holds all the lipgloss styles for the TUI.
type Styles struct {
	// theme identifies the canonical palette that produced this value. It is
	// intentionally private: App owns theme selection and passes Styles to
	// renderers instead of exposing another mutable theme authority.
	theme ThemeName

	// Layout
	Header    lipgloss.Style
	StatusBar lipgloss.Style

	// Messages
	UserPrefix       lipgloss.Style
	UserContent      lipgloss.Style
	UserMessageBlock lipgloss.Style // background tint for user messages
	AssistantPrefix  lipgloss.Style
	AssistantContent lipgloss.Style
	SystemMessage    lipgloss.Style

	// Tools
	ToolHeader  lipgloss.Style
	ToolBody    lipgloss.Style
	ToolSuccess lipgloss.Style
	ToolError   lipgloss.Style
	ToolRunning lipgloss.Style
	ToolName    lipgloss.Style

	// Editor
	EditorBorder lipgloss.Style
	EditorPrompt lipgloss.Style

	// Dialog
	DialogBorder lipgloss.Style
	DialogTitle  lipgloss.Style
	DialogHelp   lipgloss.Style
	// Dialog input styles are separate from the main composer because modal
	// editors own a raised surface and explicit idle/focused borders.
	DialogInputSurface       lipgloss.Style
	DialogInputBorder        lipgloss.Style
	DialogInputBorderFocused lipgloss.Style
	DialogInputText          lipgloss.Style
	DialogInputPlaceholder   lipgloss.Style
	DialogInputCursor        lipgloss.Style

	// General
	Subtle      lipgloss.Style
	Dim         lipgloss.Style // secondary/faint text (lighter than Subtle)
	Bold        lipgloss.Style
	Error       lipgloss.Style
	Warning     lipgloss.Style
	HintBorder  lipgloss.Style // command hint overlay border
	Placeholder lipgloss.Style // empty-state placeholder text
	ClawdBody   lipgloss.Style // semantic Clawd body/limb color
	ClawdFill   lipgloss.Style // body color on the semantic terminal background

	// Spinner shimmer colors (for interpolation in renderSpinner)
	SpinnerShimmer lipgloss.Style // brighter variant for shimmer pulse
	SpinnerStalled lipgloss.Style // error-tinted for stall indication

	// Revontuli semantic tokens. P19.0.1 introduced the four identity fields;
	// P19.3.0 added the independent raised-element surface.
	AuroraSky  lipgloss.Style // sky accent
	Selection  lipgloss.Style // selected-row surface
	Element    lipgloss.Style // raised element surface (for example inline code)
	EnoBody    lipgloss.Style // Eno mascot body tone
	EnoOutline lipgloss.Style // Eno mascot outline tone

	// Selection/highlighting
	Selected  lipgloss.Style // reverse video for selected items
	Highlight lipgloss.Style // bold for search match highlights

	// Diff colors (for future diff rendering)
	DiffAdded       lipgloss.Style // line-level added background
	DiffRemoved     lipgloss.Style // line-level removed background
	DiffAddedDim    lipgloss.Style // dimmed added background
	DiffRemovedDim  lipgloss.Style // dimmed removed background
	DiffAddedWord   lipgloss.Style // word-level added highlight
	DiffRemovedWord lipgloss.Style // word-level removed highlight

	// Scrollbar
	ScrollThumb tuiColor
	ScrollTrack tuiColor
}

func defaultStyles() Styles {
	return StylesForTheme(ThemePolarNight)
}
