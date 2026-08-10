package tui

import (
	"fmt"
	"os"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/abietic/yhc/internal/tui/terminalcap"
)

// ThemeName identifies a color theme.
type ThemeName string

const (
	// Canonical theme IDs (P19.0.2).
	ThemePolarNight ThemeName = "polar-night" // default dark (truecolor)
	ThemeDaybreak   ThemeName = "daybreak"    // light (truecolor)
	ThemeDarkAnsi   ThemeName = "dark-ansi"
	ThemeLightAnsi  ThemeName = "light-ansi"
	ThemeSnowy      ThemeName = "snowy"     // high-contrast light
	ThemeAubergine  ThemeName = "aubergine" // muted dark purple

	// Legacy Go symbol aliases, retained for one release. String inputs
	// "dark"/"light" are canonicalized separately below.
	ThemeDark  ThemeName = ThemePolarNight
	ThemeLight ThemeName = ThemeDaybreak
)

func normalizeThemeName(name string) ThemeName {
	return ThemeName(strings.ToLower(strings.TrimSpace(name)))
}

// canonicalThemeName maps the one-release legacy aliases to their canonical
// theme IDs. Canonical and unsupported names pass through unchanged.
func canonicalThemeName(name ThemeName) ThemeName {
	switch name {
	case "dark":
		return ThemePolarNight
	case "light":
		return ThemeDaybreak
	default:
		return name
	}
}

func isSupportedTheme(name ThemeName) bool {
	switch name {
	case ThemePolarNight, ThemeDaybreak, ThemeDarkAnsi, ThemeLightAnsi,
		ThemeSnowy, ThemeAubergine:
		return true
	default:
		return false
	}
}

type startupThemeSource string

const (
	startupThemeSourceEnvironment startupThemeSource = "environment"
	startupThemeSourceConfig      startupThemeSource = "config"
	startupThemeSourceTerminal    startupThemeSource = "terminal"
)

const maxStartupThemeDiagnosticRunes = 64

type startupThemeDiagnostic struct {
	source startupThemeSource
	value  string
}

func (d startupThemeDiagnostic) message(effective ThemeName) string {
	value := boundedStartupThemeDiagnosticValue(d.value)
	switch d.source {
	case startupThemeSourceEnvironment:
		return fmt.Sprintf(
			"Unsupported EINO_THEME value %q was ignored; startup selected %q after checking lower-precedence sources",
			value,
			effective,
		)
	default:
		return fmt.Sprintf(
			"Unsupported config theme %q was ignored; terminal capabilities selected %q",
			value,
			effective,
		)
	}
}

func boundedStartupThemeDiagnosticValue(value string) string {
	value = strings.TrimSpace(value)
	var bounded [maxStartupThemeDiagnosticRunes]rune
	count := 0
	for _, char := range value {
		if count == len(bounded) {
			bounded[len(bounded)-1] = '…'
			break
		}
		bounded[count] = char
		count++
	}
	return string(bounded[:count])
}

type startupThemeResolution struct {
	theme       ThemeName
	source      startupThemeSource
	diagnostics []startupThemeDiagnostic
}

// startupCompatibilityThemeName adapts the two verified Claude-compatible
// color-polarity names without claiming parity with their daltonized palettes.
// Keep this an explicit allowlist: arbitrary light-/dark-prefixed values must
// remain invalid and visible.
func startupCompatibilityThemeName(name ThemeName) ThemeName {
	switch name {
	case "dark-daltonized":
		return ThemePolarNight
	case "light-daltonized":
		return ThemeDaybreak
	default:
		return name
	}
}

func resolveStartupThemeValue(value string) (ThemeName, bool) {
	theme := canonicalThemeName(normalizeThemeName(value))
	theme = startupCompatibilityThemeName(theme)
	return theme, isSupportedTheme(theme)
}

// colorPalette holds all the semantic color values for a theme.
type colorPalette struct {
	brand       tuiColor
	permission  tuiColor
	border      tuiColor
	green       tuiColor
	red         tuiColor
	warning     tuiColor
	subtle      tuiColor
	inactive    tuiColor
	hintBorder  tuiColor
	placeholder tuiColor
	userMsgBg   tuiColor
	shimmer     tuiColor
	stalled     tuiColor
	clawdBody   tuiColor
	clawdBg     tuiColor

	// Revontuli schema fields. P19.0.2 owns their project-native values,
	// assigned explicitly per palette.
	auroraSky  tuiColor
	selection  tuiColor
	element    tuiColor
	enoBody    tuiColor
	enoOutline tuiColor

	// Diff
	diffAddedBg      tuiColor
	diffRemovedBg    tuiColor
	diffAddedDimBg   tuiColor
	diffRemovedDimBg tuiColor
	diffAddedWord    tuiColor
	diffRemovedWord  tuiColor
}

// polarNightPalette is the default dark theme (truecolor). Values come from
// the Polar Night token set in
// docs/migration/plans/p19-tui-revontuli-design/demo/themes.html.
var polarNightPalette = colorPalette{
	brand:       lipgloss.Color("#4FE3C1"), // auroraTeal
	permission:  lipgloss.Color("#A78BFA"), // auroraViolet
	border:      lipgloss.Color("#2C3A5E"),
	green:       lipgloss.Color("#8ADE8A"), // success
	red:         lipgloss.Color("#F27E93"), // error
	warning:     lipgloss.Color("#F2C66D"),
	subtle:      lipgloss.Color("#566180"), // subtle (gutters, hints, disabled)
	inactive:    lipgloss.Color("#8B95AE"), // muted
	hintBorder:  lipgloss.Color("#1E2942"), // borderSubtle
	placeholder: lipgloss.Color("#566180"), // subtle token (hints, placeholders)
	userMsgBg:   lipgloss.Color("#151D31"), // userBg
	shimmer:     lipgloss.Color("#7CD4F7"), // auroraSky; gradient sweep lands later
	stalled:     lipgloss.Color("#C2455C"),
	clawdBody:   lipgloss.Color("#D77757"), // preserved until P19.1
	clawdBg:     lipgloss.Color("#000000"), // preserved until P19.1

	auroraSky:  lipgloss.Color("#7CD4F7"),
	selection:  lipgloss.Color("#24314F"),
	element:    lipgloss.Color("#1C2740"),
	enoBody:    lipgloss.Color("#4FE3C1"), // auroraTeal
	enoOutline: lipgloss.Color("#2FA88D"),

	diffAddedBg:      lipgloss.Color("#133127"),
	diffRemovedBg:    lipgloss.Color("#3A1E2B"),
	diffAddedDimBg:   lipgloss.Color("#0E251E"),
	diffRemovedDimBg: lipgloss.Color("#2C1620"),
	diffAddedWord:    lipgloss.Color("#8ADE8A"),
	diffRemovedWord:  lipgloss.Color("#F27E93"),
}

// daybreakPalette is for light terminal backgrounds (truecolor). Values come
// from the Daybreak token set in
// docs/migration/plans/p19-tui-revontuli-design/demo/themes.html.
var daybreakPalette = colorPalette{
	brand:       lipgloss.Color("#087A66"), // auroraTeal
	permission:  lipgloss.Color("#6845D4"), // auroraViolet
	border:      lipgloss.Color("#AAAAAA"),
	green:       lipgloss.Color("#237A2F"), // success
	red:         lipgloss.Color("#B93A5E"), // error
	warning:     lipgloss.Color("#8A6300"),
	subtle:      lipgloss.Color("#98A0AF"), // subtle (gutters, hints, disabled)
	inactive:    lipgloss.Color("#5F6879"), // muted
	hintBorder:  lipgloss.Color("#CCCCCC"), // borderSubtle
	placeholder: lipgloss.Color("#98A0AF"), // subtle token (hints, placeholders)
	userMsgBg:   lipgloss.Color("#E9E4D8"), // userBg
	shimmer:     lipgloss.Color("#146B9A"), // auroraSky; gradient sweep lands later
	stalled:     lipgloss.Color("#A82847"),
	clawdBody:   lipgloss.Color("#D77757"), // preserved until P19.1
	clawdBg:     lipgloss.Color("#FFFFFF"), // preserved until P19.1

	auroraSky:  lipgloss.Color("#146B9A"),
	selection:  lipgloss.Color("#D8D2C2"),
	element:    lipgloss.Color("#E2DED3"),
	enoBody:    lipgloss.Color("#087A66"), // auroraTeal
	enoOutline: lipgloss.Color("#0A6E5C"),

	diffAddedBg:      lipgloss.Color("#D9EFDD"),
	diffRemovedBg:    lipgloss.Color("#F5DDE1"),
	diffAddedDimBg:   lipgloss.Color("#C9E5CF"),
	diffRemovedDimBg: lipgloss.Color("#EDD2D9"),
	diffAddedWord:    lipgloss.Color("#237A2F"),
	diffRemovedWord:  lipgloss.Color("#B93A5E"),
}

// darkAnsiPalette uses ANSI-16 colors for terminals without truecolor.
// Revontuli accents degrade to teal→cyan, sky→blue, violet→magenta,
// success→green, warning→yellow, error→red; no truecolor values.
var darkAnsiPalette = colorPalette{
	brand:       lipgloss.Color("14"), // bright cyan (auroraTeal)
	permission:  lipgloss.Color("13"), // bright magenta (auroraViolet)
	border:      lipgloss.Color("8"),  // bright black (gray)
	green:       lipgloss.Color("2"),  // green
	red:         lipgloss.Color("1"),  // red
	warning:     lipgloss.Color("3"),  // yellow
	subtle:      lipgloss.Color("8"),  // bright black
	inactive:    lipgloss.Color("7"),  // white (dim on dark bg)
	hintBorder:  lipgloss.Color("8"),  // bright black
	placeholder: lipgloss.Color("8"),  // bright black
	userMsgBg:   lipgloss.Color(""),   // no background in ANSI mode
	shimmer:     lipgloss.Color("14"), // flat bright cyan (gradient disabled)
	stalled:     lipgloss.Color("9"),  // bright red
	clawdBody:   lipgloss.Color("9"),  // bright red, preserved until P19.1
	clawdBg:     lipgloss.Color("0"),  // black, preserved until P19.1

	auroraSky:  lipgloss.Color("12"), // bright blue
	selection:  lipgloss.Color("8"),  // no user-msg surface in ANSI mode; reuse border
	element:    lipgloss.Color("8"),  // raised element surface
	enoBody:    lipgloss.Color("14"), // bright cyan
	enoOutline: lipgloss.Color("6"),  // cyan

	diffAddedBg:      lipgloss.Color(""),
	diffRemovedBg:    lipgloss.Color(""),
	diffAddedDimBg:   lipgloss.Color(""),
	diffRemovedDimBg: lipgloss.Color(""),
	diffAddedWord:    lipgloss.Color("2"), // green
	diffRemovedWord:  lipgloss.Color("1"), // red
}

// lightAnsiPalette uses ANSI-16 colors for light-background terminals.
var lightAnsiPalette = colorPalette{
	brand:       lipgloss.Color("6"), // cyan (auroraTeal)
	permission:  lipgloss.Color("5"), // magenta (auroraViolet)
	border:      lipgloss.Color("7"), // white/gray
	green:       lipgloss.Color("2"), // green
	red:         lipgloss.Color("1"), // red
	warning:     lipgloss.Color("3"), // yellow
	subtle:      lipgloss.Color("7"), // white (gray on light)
	inactive:    lipgloss.Color("8"), // bright black
	hintBorder:  lipgloss.Color("7"), // white
	placeholder: lipgloss.Color("8"), // bright black
	userMsgBg:   lipgloss.Color(""),  // no background
	shimmer:     lipgloss.Color("6"), // flat cyan (gradient disabled)
	stalled:     lipgloss.Color("9"), // bright red
	clawdBody:   lipgloss.Color("9"), // bright red, preserved until P19.1
	clawdBg:     lipgloss.Color("0"), // black, preserved until P19.1

	auroraSky:  lipgloss.Color("4"), // blue
	selection:  lipgloss.Color("7"), // no user-msg surface in ANSI mode; reuse border
	element:    lipgloss.Color("7"), // raised element surface
	enoBody:    lipgloss.Color("6"), // cyan
	enoOutline: lipgloss.Color("6"), // cyan

	diffAddedBg:      lipgloss.Color(""),
	diffRemovedBg:    lipgloss.Color(""),
	diffAddedDimBg:   lipgloss.Color(""),
	diffRemovedDimBg: lipgloss.Color(""),
	diffAddedWord:    lipgloss.Color("2"),
	diffRemovedWord:  lipgloss.Color("1"),
}

// snowyPalette is a high-contrast light theme (truecolor), retoned with the
// accepted aurora values.
var snowyPalette = colorPalette{
	brand:       lipgloss.Color("#0A6E5C"),
	permission:  lipgloss.Color("#5F3DC0"),
	border:      lipgloss.Color("#B0B0B0"),
	green:       lipgloss.Color("#1E6B2C"), // success
	red:         lipgloss.Color("#A82847"), // error
	warning:     lipgloss.Color("#7D5E00"),
	subtle:      lipgloss.Color("#696969"),
	inactive:    lipgloss.Color("#505050"),
	hintBorder:  lipgloss.Color("#D0D0D0"),
	placeholder: lipgloss.Color("#808080"),
	userMsgBg:   lipgloss.Color("#FAFAFA"),
	shimmer:     lipgloss.Color("#0F5E8C"),
	stalled:     lipgloss.Color("#8B1E3F"),
	clawdBody:   lipgloss.Color("#D77757"), // preserved until P19.1
	clawdBg:     lipgloss.Color("#FFFFFF"), // preserved until P19.1

	auroraSky:  lipgloss.Color("#0F5E8C"),
	selection:  lipgloss.Color("#EDEAE0"),
	element:    lipgloss.Color("#EDEAE0"),
	enoBody:    lipgloss.Color("#0A6E5C"), // brand teal
	enoOutline: lipgloss.Color("#07493B"),

	diffAddedBg:      lipgloss.Color("#D9EFDD"),
	diffRemovedBg:    lipgloss.Color("#F5DDE1"),
	diffAddedDimBg:   lipgloss.Color("#C9E5CF"),
	diffRemovedDimBg: lipgloss.Color("#EDD2D9"),
	diffAddedWord:    lipgloss.Color("#1E6B2C"),
	diffRemovedWord:  lipgloss.Color("#A82847"),
}

// auberginePalette is a muted dark purple theme (truecolor), retoned with the
// accepted aurora values while keeping its existing base.
var auberginePalette = colorPalette{
	brand:       lipgloss.Color("#C9A0DC"),
	permission:  lipgloss.Color("#9B8EC4"),
	border:      lipgloss.Color("#6B5B7B"),
	green:       lipgloss.Color("#7BC67B"),
	red:         lipgloss.Color("#E06C75"),
	warning:     lipgloss.Color("#D4A76A"),
	subtle:      lipgloss.Color("#5C4D6B"),
	inactive:    lipgloss.Color("#8B7B9B"),
	hintBorder:  lipgloss.Color("#4A3B5B"),
	placeholder: lipgloss.Color("#6B5B7B"),
	userMsgBg:   lipgloss.Color("#2E1E3E"),
	shimmer:     lipgloss.Color("#9B8EC4"),
	stalled:     lipgloss.Color("#943D5E"),
	clawdBody:   lipgloss.Color("#D77757"), // preserved until P19.1
	clawdBg:     lipgloss.Color("#000000"), // preserved until P19.1

	auroraSky:  lipgloss.Color("#9B8EC4"),
	selection:  lipgloss.Color("#2E1E3E"),
	element:    lipgloss.Color("#2E1E3E"),
	enoBody:    lipgloss.Color("#C9A0DC"),
	enoOutline: lipgloss.Color("#9B7BB8"),

	diffAddedBg:      lipgloss.Color("#1E3A2E"),
	diffRemovedBg:    lipgloss.Color("#3E1E2E"),
	diffAddedDimBg:   lipgloss.Color("#2E4A3E"),
	diffRemovedDimBg: lipgloss.Color("#4E2E3E"),
	diffAddedWord:    lipgloss.Color("#7BC67B"),
	diffRemovedWord:  lipgloss.Color("#E06C75"),
}

// getPalette returns the color palette for the given theme name. Canonical
// IDs and the legacy dark/light aliases resolve equivalently.
func getPalette(name ThemeName) colorPalette {
	switch canonicalThemeName(name) {
	case ThemeDaybreak:
		return daybreakPalette
	case ThemeDarkAnsi:
		return darkAnsiPalette
	case ThemeLightAnsi:
		return lightAnsiPalette
	case ThemeSnowy:
		return snowyPalette
	case ThemeAubergine:
		return auberginePalette
	default:
		// ThemePolarNight and the legacy ThemeDark alias.
		return polarNightPalette
	}
}

// ResolveTheme determines the effective theme from user config, environment,
// and terminal capability detection.
//
// Priority: 1) EINO_THEME env var, 2) config theme setting, 3) auto-detection.
func ResolveTheme(configTheme string) ThemeName {
	return ResolveThemeForCapabilities(configTheme, terminalcap.Current(false))
}

// ResolveExplicitTheme validates a runtime theme selection without consulting
// EINO_THEME or config. Environment and config choose only the startup theme;
// an explicit /theme choice owns the active theme for the rest of the process.
// Legacy dark/light inputs canonicalize to polar-night/daybreak.
func ResolveExplicitTheme(name string) (ThemeName, error) {
	theme := canonicalThemeName(normalizeThemeName(name))
	if !isSupportedTheme(theme) {
		return "", fmt.Errorf(
			"unsupported theme %q (choose polar-night, daybreak, dark-ansi, light-ansi, snowy, or aubergine; dark and light remain one-release aliases)",
			strings.TrimSpace(name),
		)
	}
	return theme, nil
}

// resolveStartupThemeForCapabilities resolves the effective startup theme and
// retains typed diagnostics for every invalid source that affected fallback.
func resolveStartupThemeForCapabilities(
	configTheme string,
	caps terminalcap.Capabilities,
) startupThemeResolution {
	var diagnostics []startupThemeDiagnostic
	if env := os.Getenv("EINO_THEME"); env != "" {
		if theme, ok := resolveStartupThemeValue(env); ok {
			return startupThemeResolution{
				theme:  theme,
				source: startupThemeSourceEnvironment,
			}
		}
		diagnostics = append(diagnostics, startupThemeDiagnostic{
			source: startupThemeSourceEnvironment,
			value:  env,
		})
	}

	if configTheme != "" {
		if theme, ok := resolveStartupThemeValue(configTheme); ok {
			return startupThemeResolution{
				theme:       theme,
				source:      startupThemeSourceConfig,
				diagnostics: diagnostics,
			}
		}
		diagnostics = append(diagnostics, startupThemeDiagnostic{
			source: startupThemeSourceConfig,
			value:  configTheme,
		})
	}

	theme := ThemePolarNight
	if caps.Color != terminalcap.ColorTrueColor && caps.Color != terminalcap.ColorANSI256 {
		theme = ThemeDarkAnsi
	}
	return startupThemeResolution{
		theme:       theme,
		source:      startupThemeSourceTerminal,
		diagnostics: diagnostics,
	}
}

// ResolveThemeForCapabilities selects a theme from an already-probed terminal
// snapshot, avoiding independent color heuristics across TUI components.
// Valid env/config values canonicalize to project theme IDs. Startup-only
// compatibility aliases preserve light/dark polarity; App.New owns the visible
// diagnostics retained by the typed resolver.
func ResolveThemeForCapabilities(configTheme string, caps terminalcap.Capabilities) ThemeName {
	return resolveStartupThemeForCapabilities(configTheme, caps).theme
}

// stylesFromPalette builds a Styles struct from a color palette.
func stylesFromPalette(p colorPalette) Styles {
	s := Styles{
		Header: lipgloss.NewStyle().
			Bold(true).
			Foreground(p.brand).
			PaddingLeft(1),
		StatusBar: lipgloss.NewStyle().
			Foreground(p.inactive).
			PaddingLeft(1),

		UserPrefix: lipgloss.NewStyle().
			Bold(true).
			Foreground(p.brand),
		UserContent: lipgloss.NewStyle().
			PaddingLeft(2),
		UserMessageBlock: lipgloss.NewStyle().
			Padding(0, 1),
		AssistantPrefix: lipgloss.NewStyle().
			Bold(true).
			Foreground(p.brand),
		AssistantContent: lipgloss.NewStyle().
			PaddingLeft(2),
		SystemMessage: lipgloss.NewStyle().
			Foreground(p.inactive).
			Italic(true).
			PaddingLeft(2),

		ToolHeader: lipgloss.NewStyle().
			PaddingLeft(2),
		ToolBody: lipgloss.NewStyle().
			Foreground(p.subtle).
			PaddingLeft(4),
		ToolSuccess: lipgloss.NewStyle().
			Foreground(p.green),
		ToolError: lipgloss.NewStyle().
			Foreground(p.red),
		ToolRunning: lipgloss.NewStyle().
			Foreground(p.brand),
		ToolName: lipgloss.NewStyle().
			Bold(true),

		EditorBorder: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(p.border).
			Padding(0, 1),
		EditorPrompt: lipgloss.NewStyle().
			Bold(true).
			Foreground(p.brand),

		DialogBorder: lipgloss.NewStyle().
			Border(lipgloss.Border{Top: "─"}).
			BorderForeground(p.permission).
			Padding(0, 1),
		DialogTitle: lipgloss.NewStyle().
			Bold(true).
			Foreground(p.permission),
		DialogHelp: lipgloss.NewStyle().
			Foreground(p.inactive),
		DialogInputSurface: lipgloss.NewStyle().
			Background(p.element),
		DialogInputBorder: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(p.border),
		DialogInputBorderFocused: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(p.permission),
		DialogInputText: lipgloss.NewStyle().
			Foreground(p.permission).
			Background(p.element),
		DialogInputPlaceholder: lipgloss.NewStyle().
			Foreground(p.placeholder).
			Background(p.element).
			Italic(true),
		DialogInputCursor: lipgloss.NewStyle().
			// Bubbles reverses the cursor style during rendering. Keep this
			// pre-reversal so the terminal cell has the brand background.
			Foreground(p.brand).
			Background(p.element),

		Subtle:  lipgloss.NewStyle().Foreground(p.subtle),
		Dim:     lipgloss.NewStyle().Foreground(p.inactive).Faint(true),
		Bold:    lipgloss.NewStyle().Bold(true),
		Error:   lipgloss.NewStyle().Foreground(p.red),
		Warning: lipgloss.NewStyle().Foreground(p.warning),
		HintBorder: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(p.hintBorder).
			Padding(0, 1),
		Placeholder: lipgloss.NewStyle().
			Foreground(p.placeholder).
			Italic(true),
		ClawdBody: lipgloss.NewStyle().Foreground(p.clawdBody),
		ClawdFill: lipgloss.NewStyle().Foreground(p.clawdBody).Background(p.clawdBg),

		SpinnerShimmer: lipgloss.NewStyle().Foreground(p.shimmer),
		SpinnerStalled: lipgloss.NewStyle().Foreground(p.stalled),

		AuroraSky:  lipgloss.NewStyle().Foreground(p.auroraSky),
		Selection:  lipgloss.NewStyle().Background(p.selection),
		Element:    lipgloss.NewStyle().Background(p.element),
		EnoBody:    lipgloss.NewStyle().Foreground(p.enoBody),
		EnoOutline: lipgloss.NewStyle().Foreground(p.enoOutline),

		Selected:  lipgloss.NewStyle().Reverse(true),
		Highlight: lipgloss.NewStyle().Bold(true),

		DiffAdded:       lipgloss.NewStyle().Background(p.diffAddedBg),
		DiffRemoved:     lipgloss.NewStyle().Background(p.diffRemovedBg),
		DiffAddedDim:    lipgloss.NewStyle().Background(p.diffAddedDimBg),
		DiffRemovedDim:  lipgloss.NewStyle().Background(p.diffRemovedDimBg),
		DiffAddedWord:   lipgloss.NewStyle().Foreground(p.diffAddedWord),
		DiffRemovedWord: lipgloss.NewStyle().Foreground(p.diffRemovedWord),
		ScrollThumb:     p.inactive,
		ScrollTrack:     p.hintBorder,
	}

	// Only apply user message background if it's set (ANSI themes skip it)
	if tuiColorString(p.userMsgBg) != "" {
		s.UserMessageBlock = s.UserMessageBlock.Background(p.userMsgBg)
	}

	return s
}

// StylesForTheme returns a Styles struct for the given theme name.
func StylesForTheme(name ThemeName) Styles {
	canonical := canonicalThemeName(name)
	styles := stylesFromPalette(getPalette(canonical))
	styles.theme = canonical
	return styles
}
