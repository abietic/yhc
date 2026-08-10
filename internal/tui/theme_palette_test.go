package tui

import (
	"strings"
	"testing"

	"github.com/abietic/yhc/internal/tui/terminalcap"
)

var supportedThemeNames = []ThemeName{
	ThemePolarNight,
	ThemeDaybreak,
	ThemeDarkAnsi,
	ThemeLightAnsi,
	ThemeSnowy,
	ThemeAubergine,
}

// TestThemeAliasesCanonicalize covers the P19.0.2 canonical theme IDs and the
// one-release dark/light input aliases across explicit and startup resolution.
func TestThemeAliasesCanonicalize(t *testing.T) {
	t.Run("explicit", func(t *testing.T) {
		cases := map[string]ThemeName{
			"polar-night": ThemePolarNight,
			"daybreak":    ThemeDaybreak,
			"dark":        ThemePolarNight,
			"light":       ThemeDaybreak,
			" Dark ":      ThemePolarNight,
			"LIGHT":       ThemeDaybreak,
			"dark-ansi":   ThemeDarkAnsi,
			"light-ansi":  ThemeLightAnsi,
			"snowy":       ThemeSnowy,
			"aubergine":   ThemeAubergine,
		}
		for input, want := range cases {
			got, err := ResolveExplicitTheme(input)
			if err != nil {
				t.Errorf("ResolveExplicitTheme(%q): unexpected error %v", input, err)
				continue
			}
			if got != want {
				t.Errorf("ResolveExplicitTheme(%q) = %q, want %q", input, got, want)
			}
		}
		if _, err := ResolveExplicitTheme("nope"); err == nil {
			t.Error("ResolveExplicitTheme(nope): expected error")
		}
	})

	t.Run("startup env and config", func(t *testing.T) {
		caps := terminalcap.Capabilities{Color: terminalcap.ColorTrueColor}

		t.Setenv("EINO_THEME", "dark")
		if got := ResolveThemeForCapabilities("snowy", caps); got != ThemePolarNight {
			t.Errorf("env dark: got %q, want %q", got, ThemePolarNight)
		}
		t.Setenv("EINO_THEME", "light")
		if got := ResolveThemeForCapabilities("snowy", caps); got != ThemeDaybreak {
			t.Errorf("env light: got %q, want %q", got, ThemeDaybreak)
		}
		t.Setenv("EINO_THEME", "polar-night")
		if got := ResolveThemeForCapabilities("", caps); got != ThemePolarNight {
			t.Errorf("env polar-night: got %q, want %q", got, ThemePolarNight)
		}

		t.Setenv("EINO_THEME", "")
		if got := ResolveThemeForCapabilities("dark", caps); got != ThemePolarNight {
			t.Errorf("config dark: got %q, want %q", got, ThemePolarNight)
		}
		if got := ResolveThemeForCapabilities("light", caps); got != ThemeDaybreak {
			t.Errorf("config light: got %q, want %q", got, ThemeDaybreak)
		}
		if got := ResolveThemeForCapabilities("daybreak", caps); got != ThemeDaybreak {
			t.Errorf("config daybreak: got %q, want %q", got, ThemeDaybreak)
		}
	})

	t.Run("auto-detect defaults", func(t *testing.T) {
		t.Setenv("EINO_THEME", "")
		truecolor := terminalcap.Capabilities{Color: terminalcap.ColorTrueColor}
		if got := ResolveThemeForCapabilities("", truecolor); got != ThemePolarNight {
			t.Errorf("truecolor default = %q, want %q", got, ThemePolarNight)
		}
		ansi256 := terminalcap.Capabilities{Color: terminalcap.ColorANSI256}
		if got := ResolveThemeForCapabilities("", ansi256); got != ThemePolarNight {
			t.Errorf("ansi256 default = %q, want %q", got, ThemePolarNight)
		}
		ansi16 := terminalcap.Capabilities{Color: terminalcap.ColorANSI16}
		if got := ResolveThemeForCapabilities("", ansi16); got != ThemeDarkAnsi {
			t.Errorf("non-truecolor default = %q, want %q", got, ThemeDarkAnsi)
		}
	})
}

// TestThemeAliasesPaletteEquivalence pins getPalette/StylesForTheme accepting
// canonical and legacy inputs equivalently.
func TestThemeAliasesPaletteEquivalence(t *testing.T) {
	if got, want := getPalette(ThemeName("dark")), getPalette(ThemePolarNight); got != want {
		t.Errorf("getPalette(dark) = %+v, want polar-night palette %+v", got, want)
	}
	if got, want := getPalette(ThemeName("light")), getPalette(ThemeDaybreak); got != want {
		t.Errorf("getPalette(light) = %+v, want daybreak palette %+v", got, want)
	}
	// Styles is not comparable; spot-check the semantic colors it carries.
	legacyPairs := [][2]ThemeName{
		{"dark", ThemePolarNight},
		{"light", ThemeDaybreak},
	}
	for _, pair := range legacyPairs {
		legacy, canonical := StylesForTheme(pair[0]), StylesForTheme(pair[1])
		if got, want := legacy.UserPrefix.GetForeground(), canonical.UserPrefix.GetForeground(); got != want {
			t.Errorf("StylesForTheme(%s).UserPrefix foreground = %v, want %v", pair[0], got, want)
		}
		if got, want := legacy.AuroraSky.GetForeground(), canonical.AuroraSky.GetForeground(); got != want {
			t.Errorf("StylesForTheme(%s).AuroraSky foreground = %v, want %v", pair[0], got, want)
		}
		if got, want := legacy.Selection.GetBackground(), canonical.Selection.GetBackground(); got != want {
			t.Errorf("StylesForTheme(%s).Selection background = %v, want %v", pair[0], got, want)
		}
		if got, want := legacy.Element.GetBackground(), canonical.Element.GetBackground(); got != want {
			t.Errorf("StylesForTheme(%s).Element background = %v, want %v", pair[0], got, want)
		}
	}
}

// TestRevontuliExactPalettes pins the P19.0.2 Revontuli values, including the
// accepted aurora retones for snowy and aubergine.
func TestRevontuliExactPalettes(t *testing.T) {
	type want struct {
		brand      string
		permission string
		green      string
		red        string
		warning    string
		auroraSky  string
		selection  string
		element    string
		enoBody    string
		enoOutline string
	}
	cases := map[ThemeName]want{
		ThemePolarNight: {
			brand: "#4FE3C1", permission: "#A78BFA",
			green: "#8ADE8A", red: "#F27E93", warning: "#F2C66D",
			auroraSky: "#7CD4F7", selection: "#24314F", element: "#1C2740",
			enoBody: "#4FE3C1", enoOutline: "#2FA88D",
		},
		ThemeDaybreak: {
			brand: "#087A66", permission: "#6845D4",
			green: "#237A2F", red: "#B93A5E", warning: "#8A6300",
			auroraSky: "#146B9A", selection: "#D8D2C2", element: "#E2DED3",
			enoBody: "#087A66", enoOutline: "#0A6E5C",
		},
		ThemeDarkAnsi: {
			brand: "14", permission: "13",
			green: "2", red: "1", warning: "3",
			auroraSky: "12", selection: "8", element: "8",
			enoBody: "14", enoOutline: "6",
		},
		ThemeLightAnsi: {
			brand: "6", permission: "5",
			green: "2", red: "1", warning: "3",
			auroraSky: "4", selection: "7", element: "7",
			enoBody: "6", enoOutline: "6",
		},
		ThemeSnowy: {
			brand: "#0A6E5C", permission: "#5F3DC0",
			green: "#1E6B2C", red: "#A82847", warning: "#7D5E00",
			auroraSky: "#0F5E8C", selection: "#EDEAE0", element: "#EDEAE0",
			enoBody: "#0A6E5C", enoOutline: "#07493B",
		},
		ThemeAubergine: {
			brand: "#C9A0DC", permission: "#9B8EC4",
			green: "#7BC67B", red: "#E06C75", warning: "#D4A76A",
			auroraSky: "#9B8EC4", selection: "#2E1E3E", element: "#2E1E3E",
			enoBody: "#C9A0DC", enoOutline: "#9B7BB8",
		},
	}
	for _, name := range supportedThemeNames {
		w, ok := cases[name]
		if !ok {
			t.Fatalf("%s: missing exact-palette expectations", name)
		}
		p := getPalette(name)
		checks := map[string]struct {
			got  tuiColor
			want string
		}{
			"brand":      {p.brand, w.brand},
			"permission": {p.permission, w.permission},
			"green":      {p.green, w.green},
			"red":        {p.red, w.red},
			"warning":    {p.warning, w.warning},
			"auroraSky":  {p.auroraSky, w.auroraSky},
			"selection":  {p.selection, w.selection},
			"element":    {p.element, w.element},
			"enoBody":    {p.enoBody, w.enoBody},
			"enoOutline": {p.enoOutline, w.enoOutline},
		}
		for field, c := range checks {
			if tuiColorString(c.got) != c.want {
				t.Errorf("%s: %s = %q, want %q", name, field, c.got, c.want)
			}
		}
	}
}

// TestCanonicalRevontuliOwnedSurfaces pins the canonical palette values that
// are not foreground accents. The TUI deliberately has no bg0 field because
// it does not paint the user's global terminal background.
func TestCanonicalRevontuliOwnedSurfaces(t *testing.T) {
	cases := map[ThemeName]map[string]string{
		ThemePolarNight: {
			"border": "#2C3A5E", "hintBorder": "#1E2942",
			"subtle": "#566180", "inactive": "#8B95AE", "placeholder": "#566180",
			"userMsgBg": "#151D31", "selection": "#24314F", "element": "#1C2740",
			"shimmer": "#7CD4F7", "stalled": "#C2455C",
			"diffAddedBg": "#133127", "diffRemovedBg": "#3A1E2B",
			"diffAddedDimBg": "#0E251E", "diffRemovedDimBg": "#2C1620",
			"diffAddedWord": "#8ADE8A", "diffRemovedWord": "#F27E93",
		},
		ThemeDaybreak: {
			"border": "#AAAAAA", "hintBorder": "#CCCCCC",
			"subtle": "#98A0AF", "inactive": "#5F6879", "placeholder": "#98A0AF",
			"userMsgBg": "#E9E4D8", "selection": "#D8D2C2", "element": "#E2DED3",
			"shimmer": "#146B9A", "stalled": "#A82847",
			"diffAddedBg": "#D9EFDD", "diffRemovedBg": "#F5DDE1",
			"diffAddedDimBg": "#C9E5CF", "diffRemovedDimBg": "#EDD2D9",
			"diffAddedWord": "#237A2F", "diffRemovedWord": "#B93A5E",
		},
	}
	for name, want := range cases {
		got := allPaletteColors(getPalette(name))
		for field, wantValue := range want {
			if gotValue := tuiColorString(got[field]); gotValue != wantValue {
				t.Errorf("%s: %s = %q, want %q", name, field, gotValue, wantValue)
			}
		}
	}
}

// TestRevontuliStylesWiring keeps the palette-to-Styles mapping for the four
// Revontuli fields under test.
func TestRevontuliStylesWiring(t *testing.T) {
	for _, name := range supportedThemeNames {
		palette := getPalette(name)
		styles := StylesForTheme(name)
		if got := styles.AuroraSky.GetForeground(); got != palette.auroraSky {
			t.Errorf("%s: AuroraSky foreground = %v, want %q", name, got, palette.auroraSky)
		}
		if got := styles.Selection.GetBackground(); got != palette.selection {
			t.Errorf("%s: Selection background = %v, want %q", name, got, palette.selection)
		}
		if got := styles.Element.GetBackground(); got != palette.element {
			t.Errorf("%s: Element background = %v, want %q", name, got, palette.element)
		}
		if got := styles.EnoBody.GetForeground(); got != palette.enoBody {
			t.Errorf("%s: EnoBody foreground = %v, want %q", name, got, palette.enoBody)
		}
		if got := styles.EnoOutline.GetForeground(); got != palette.enoOutline {
			t.Errorf("%s: EnoOutline foreground = %v, want %q", name, got, palette.enoOutline)
		}
		if got := styles.DialogInputSurface.GetBackground(); got != palette.element {
			t.Errorf("%s: DialogInputSurface background = %v, want %q", name, got, palette.element)
		}
		if got := styles.DialogInputBorder.GetBorderTopForeground(); got != palette.border {
			t.Errorf("%s: DialogInputBorder = %v, want %q", name, got, palette.border)
		}
		if got := styles.DialogInputBorderFocused.GetBorderTopForeground(); got != palette.permission {
			t.Errorf("%s: DialogInputBorderFocused = %v, want %q", name, got, palette.permission)
		}
		if got := styles.DialogInputText.GetForeground(); got != palette.permission {
			t.Errorf("%s: DialogInputText foreground = %v, want %q", name, got, palette.permission)
		}
		if got := styles.DialogInputPlaceholder.GetForeground(); got != palette.placeholder {
			t.Errorf("%s: DialogInputPlaceholder = %v, want %q", name, got, palette.placeholder)
		}
		if got := styles.DialogInputCursor.GetBackground(); got != palette.element {
			t.Errorf("%s: DialogInputCursor pre-reversal background = %v, want %q", name, got, palette.element)
		}
		if got := styles.DialogInputCursor.GetForeground(); got != palette.brand {
			t.Errorf("%s: DialogInputCursor pre-reversal foreground = %v, want %q", name, got, palette.brand)
		}
	}
}

// TestRevontuliANSIFallback pins the ANSI-16 degradation: teal→cyan,
// sky→blue, violet→magenta, success→green, warning→yellow, error→red, with
// no truecolor values anywhere in either ANSI palette.
func TestRevontuliANSIFallback(t *testing.T) {
	cases := []struct {
		name       ThemeName
		wantBrand  string
		wantSky    string
		wantViolet string
	}{
		{ThemeDarkAnsi, "14", "12", "13"},
		{ThemeLightAnsi, "6", "4", "5"},
	}
	for _, tc := range cases {
		name := tc.name
		palette := getPalette(name)
		accents := map[string]tuiColor{
			"brand (teal→cyan)":       palette.brand,
			"auroraSky (sky→blue)":    palette.auroraSky,
			"permission (violet→mag)": palette.permission,
			"green (success)":         palette.green,
			"warning":                 palette.warning,
			"red (error)":             palette.red,
		}
		wantAccents := map[string]string{
			"brand (teal→cyan)":       tc.wantBrand,
			"auroraSky (sky→blue)":    tc.wantSky,
			"permission (violet→mag)": tc.wantViolet,
			"green (success)":         "2",
			"warning":                 "3",
			"red (error)":             "1",
		}
		for field, value := range accents {
			if tuiColorString(value) != wantAccents[field] {
				t.Errorf("%s: %s = %q, want ANSI %q", name, field, value, wantAccents[field])
			}
		}

		for field, value := range allPaletteColors(palette) {
			if strings.HasPrefix(tuiColorString(value), "#") {
				t.Errorf("%s: %s = %q, ANSI palette must not contain truecolor", name, field, value)
			}
		}
	}
}

func allPaletteColors(p colorPalette) map[string]tuiColor {
	return map[string]tuiColor{
		"brand": p.brand, "permission": p.permission, "border": p.border,
		"green": p.green, "red": p.red, "warning": p.warning,
		"subtle": p.subtle, "inactive": p.inactive, "hintBorder": p.hintBorder,
		"placeholder": p.placeholder, "userMsgBg": p.userMsgBg,
		"shimmer": p.shimmer, "stalled": p.stalled,
		"clawdBody": p.clawdBody, "clawdBg": p.clawdBg,
		"auroraSky": p.auroraSky, "selection": p.selection, "element": p.element,
		"enoBody": p.enoBody, "enoOutline": p.enoOutline,
		"diffAddedBg": p.diffAddedBg, "diffRemovedBg": p.diffRemovedBg,
		"diffAddedDimBg": p.diffAddedDimBg, "diffRemovedDimBg": p.diffRemovedDimBg,
		"diffAddedWord": p.diffAddedWord, "diffRemovedWord": p.diffRemovedWord,
	}
}
