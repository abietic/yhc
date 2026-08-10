package tui

import (
	"fmt"
	"os"
	"strings"
	"testing"

	xansi "github.com/charmbracelet/x/ansi"

	"github.com/abietic/yhc/internal/identity"
	"github.com/abietic/yhc/internal/tui/terminalcap"
)

func TestWelcomeWordmarkTrueColorGolden(t *testing.T) {
	var actual strings.Builder
	for _, theme := range []ThemeName{ThemePolarNight, ThemeDaybreak} {
		actual.WriteString("== " + string(theme) + " ==\n")
		fmt.Fprintf(&actual, "%q\n",
			renderWelcomeWordmark(StylesForTheme(theme), true))
	}

	want, err := os.ReadFile("testdata/welcome_wordmark.golden")
	if err != nil {
		t.Fatalf("read welcome wordmark golden: %v", err)
	}
	if actual.String() != string(want) {
		t.Fatalf("welcome wordmark golden mismatch:\n--- got ---\n%s--- want ---\n%s",
			actual.String(), want)
	}
}

func TestWelcomeWordmarkFallbacksStayFlat(t *testing.T) {
	for _, test := range []struct {
		name      string
		theme     ThemeName
		truecolor bool
	}{
		{
			name:  "truecolor-theme-on-reduced-color-terminal",
			theme: ThemePolarNight,
		},
		{
			name:      "ansi-theme-on-truecolor-terminal",
			theme:     ThemeDarkAnsi,
			truecolor: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			styles := StylesForTheme(test.theme)
			got := renderWelcomeWordmark(styles, test.truecolor)
			if want := styles.Header.Render(identity.ProductName); got != want {
				t.Fatalf("fallback = %q, want flat Header %q", got, want)
			}
			if strings.Count(got, "\x1b[") > 2 {
				t.Fatalf("fallback rendered per-cell color escapes: %q", got)
			}
		})
	}
}

func TestWelcomeWordmarkCoversResponsiveWelcomeTiers(t *testing.T) {
	caps := terminalcap.Capabilities{Color: terminalcap.ColorTrueColor}
	for _, viewport := range []struct {
		name          string
		width, height int
	}{
		{name: "compact-text", width: 56, height: 24},
		{name: "condensed-mascot", width: 69, height: 24},
		{name: "full-bordered", width: 80, height: 24},
		{name: "wide-full-bordered", width: 150, height: 24},
	} {
		t.Run(viewport.name, func(t *testing.T) {
			app := sizedWelcomeApp(viewport.width, viewport.height, Config{
				Theme:         string(ThemePolarNight),
				ReducedMotion: true,
				TerminalCaps:  &caps,
				Chooser:       func(int) int { return 0 },
			})
			wordmark := renderWelcomeWordmark(app.styles, true)
			if view := app.renderWelcome(); !strings.Contains(view, wordmark) {
				t.Fatalf("%s welcome omitted gradient wordmark: %q", viewport.name, view)
			}
			flat := app.styles.Header.Render(identity.ProductName)
			if got, want := xansi.StringWidth(wordmark), xansi.StringWidth(flat); got != want {
				t.Fatalf("%s wordmark width = %d, flat width = %d", viewport.name, got, want)
			}
		})
	}
}

func TestWelcomeWordmarkNoColorFinalFrameHasNoStyles(t *testing.T) {
	caps := terminalcap.Capabilities{Color: terminalcap.ColorNone}
	app := sizedWelcomeApp(80, 24, Config{
		Theme:        string(ThemePolarNight),
		TerminalCaps: &caps,
		Chooser:      func(int) int { return 0 },
	})
	view := app.renderView()
	if strings.Contains(view, "\x1b[") {
		t.Fatalf("no-color welcome contains terminal styles: %q", view)
	}
	if !strings.Contains(view, identity.ProductName) {
		t.Fatalf("no-color welcome lost wordmark text: %q", view)
	}
}

func TestWelcomeWordmarkUsesLiveThemeSnapshot(t *testing.T) {
	caps := terminalcap.Capabilities{Color: terminalcap.ColorTrueColor}
	app := sizedWelcomeApp(80, 24, Config{
		Theme:        string(ThemePolarNight),
		TerminalCaps: &caps,
		Chooser:      func(int) int { return 0 },
	})
	before := renderWelcomeWordmark(app.styles, true)
	if err := app.applyTheme(string(ThemeDaybreak)); err != nil {
		t.Fatalf("apply Daybreak: %v", err)
	}
	after := renderWelcomeWordmark(app.styles, true)
	if before == after {
		t.Fatal("runtime theme change retained stale welcome wordmark")
	}
	if view := app.renderWelcome(); !strings.Contains(view, after) {
		t.Fatalf("welcome did not use live Daybreak wordmark: %q", view)
	}
}

func BenchmarkRenderWelcomeWordmark(b *testing.B) {
	styles := StylesForTheme(ThemePolarNight)
	for b.Loop() {
		renderWelcomeWordmark(styles, true)
	}
}
