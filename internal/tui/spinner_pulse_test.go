package tui

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
)

func TestSpinnerBreathIntensitySequence(t *testing.T) {
	want := []float64{0, 0.25, 0.5, 0.75, 1, 0.75, 0.5, 0.25}
	for tick, expected := range want {
		if got := spinnerBreathIntensity(tick); got != expected {
			t.Fatalf("spinnerBreathIntensity(%d) = %v, want %v", tick, got, expected)
		}
		if got := spinnerBreathIntensity(tick - spinnerBreathTicks); got != expected {
			t.Fatalf("negative wrapped intensity at %d = %v, want %v", tick, got, expected)
		}
	}
	if got, want := spinnerBreathIntensity(spinnerBreathTicks), spinnerBreathIntensity(0); got != want {
		t.Fatalf("cycle wrap intensity = %v, want %v", got, want)
	}
}

func TestSpinnerPulseTruecolorInterpolation(t *testing.T) {
	styles := StylesForTheme(ThemePolarNight)
	cases := []struct {
		tick int
		want string
	}{
		{tick: 0, want: "#566180"},
		{tick: 1, want: "#548290"},
		{tick: 2, want: "#53A2A1"},
		{tick: 3, want: "#51C3B1"},
		{tick: 4, want: "#4FE3C1"},
		{tick: 7, want: "#548290"},
		{tick: 8, want: "#566180"},
		{tick: -1, want: "#548290"},
	}
	for _, tc := range cases {
		style := spinnerPulseStyle(styles.Subtle, styles.AssistantPrefix, tc.tick)
		if got := styleForegroundString(style); got != tc.want {
			t.Fatalf("tick %d foreground = %q, want %q", tc.tick, got, tc.want)
		}
	}
}

func TestSpinnerPulseANSIFallback(t *testing.T) {
	styles := StylesForTheme(ThemeDarkAnsi)
	for tick := 0; tick < spinnerBreathTicks; tick++ {
		style := spinnerPulseStyle(styles.Subtle, styles.AssistantPrefix, tick)
		foreground := styleForegroundString(style)
		if strings.HasPrefix(foreground, "#") {
			t.Fatalf("tick %d ANSI fallback produced truecolor %q", tick, foreground)
		}
		want := styleForegroundString(styles.AssistantPrefix)
		if spinnerBreathIntensity(tick) < 0.5 {
			want = styleForegroundString(styles.Subtle)
		}
		if foreground != want {
			t.Fatalf("tick %d ANSI foreground = %q, want %q", tick, foreground, want)
		}
	}
}

func TestSpinnerPulseUsesEveryThemeSemanticEndpoint(t *testing.T) {
	themes := []ThemeName{
		ThemePolarNight,
		ThemeDaybreak,
		ThemeDarkAnsi,
		ThemeLightAnsi,
		ThemeSnowy,
		ThemeAubergine,
	}
	for _, theme := range themes {
		t.Run(string(theme), func(t *testing.T) {
			styles := StylesForTheme(theme)
			peaks := map[string]lipgloss.Style{
				"main":    styles.AssistantPrefix,
				"stalled": styles.SpinnerStalled,
				"tool":    styles.ToolRunning,
			}
			for name, peak := range peaks {
				if got, want := spinnerPulseStyle(styles.Subtle, peak, 0).GetForeground(), styles.Subtle.GetForeground(); got != want {
					t.Fatalf("%s low endpoint = %v, want Subtle %v", name, got, want)
				}
				if got, want := spinnerPulseStyle(styles.Subtle, peak, 4).GetForeground(), peak.GetForeground(); got != want {
					t.Fatalf("%s peak endpoint = %v, want caller peak %v", name, got, want)
				}

				app := New(Config{Resumed: true, Model: "test-model", ReducedMotion: true})
				app.styles = styles
				if got, want := app.spinnerPulseIcon(peak, 3), peak.Render(spinnerGlyph()); got != want {
					t.Fatalf("%s reduced-motion icon = %q, want caller peak %q", name, got, want)
				}
			}
			if theme == ThemeDarkAnsi || theme == ThemeLightAnsi {
				for _, tick := range []int{0, 2, 4, 6} {
					if foreground := styleForegroundString(spinnerPulseStyle(styles.Subtle, styles.AssistantPrefix, tick)); strings.HasPrefix(foreground, "#") {
						t.Fatalf("tick %d emitted truecolor %q", tick, foreground)
					}
				}
			}
		})
	}
}

func TestReducedMotionSpinnerIconUsesStaticPeakStyle(t *testing.T) {
	app := New(Config{Resumed: true, Model: "test-model", ReducedMotion: true})
	want := app.styles.AssistantPrefix.Render(spinnerGlyph())
	for counter := -spinnerBreathTicks; counter <= spinnerBreathTicks; counter++ {
		if got := app.spinnerPulseIcon(app.styles.AssistantPrefix, counter); got != want {
			t.Fatalf("reduced-motion icon at tick %d = %q, want %q", counter, got, want)
		}
	}
}

func TestRenderSpinnerUsesBreathingIdentityGlyph(t *testing.T) {
	app := New(Config{Resumed: true, Model: "test-model"})
	app.running = true
	app.spinnerState = SpinnerState{Mode: SpinnerThinking, StartTime: time.Now()}

	for counter := 0; counter < spinnerBreathTicks; counter++ {
		app.spinnerCount = counter
		plain := stripANSIForTest(app.renderSpinner())
		if !strings.Contains(plain, assistantIdentityGlyph+" Thinking…") {
			t.Fatalf("tick %d spinner = %q, want identity glyph before unchanged verb", counter, plain)
		}
		assertNoBorrowedSpinnerGlyph(t, plain)
	}
}

func TestRenderSpinnerStalledAndInlineIconsUseBreathingGlyph(t *testing.T) {
	app := New(Config{Resumed: true, Model: "test-model"})
	app.running = true
	app.spinnerState = SpinnerState{
		Mode:          SpinnerThinking,
		StartTime:     time.Now().Add(-time.Minute),
		LastEventTime: time.Now().Add(-stallThreshold - 3*time.Second),
	}
	stalled := stripANSIForTest(app.renderSpinner())
	if !strings.Contains(stalled, assistantIdentityGlyph+" ") || !strings.Contains(stalled, "(waiting)") {
		t.Fatalf("stalled spinner = %q, want identity glyph and waiting text", stalled)
	}
	assertNoBorrowedSpinnerGlyph(t, stalled)

	app.activeTools["tool-1"] = &inlineToolEntry{name: "Read", startTime: time.Now()}
	app.activeTools["tool-2"] = &inlineToolEntry{name: "Grep", startTime: time.Now()}
	app.activeToolsOrder = []string{"tool-1", "tool-2"}
	tree := stripANSIForTest(app.renderTaskTree())
	if !strings.Contains(tree, "✦ Read") || !strings.Contains(tree, "✦ Grep") {
		t.Fatalf("inline tree = %q, want filled-star running icons", tree)
	}
	assertNoBorrowedSpinnerGlyph(t, tree)

	remote := &taskEntry{taskID: "agent-1"}
	if got := stripANSIForTest(app.renderTaskEntryIcon(remote, 3)); got != assistantIdentityGlyph {
		t.Fatalf("remote task icon = %q, want %q", got, assistantIdentityGlyph)
	}
}

func TestSpinnerPulseGolden(t *testing.T) {
	styles := StylesForTheme(ThemePolarNight)
	var got strings.Builder
	got.WriteString("# P19.2.1 polar-night spinner: Subtle -> AssistantPrefix\n")
	for tick := 0; tick < spinnerBreathTicks; tick++ {
		foreground := styleForegroundString(spinnerPulseStyle(styles.Subtle, styles.AssistantPrefix, tick))
		fmt.Fprintf(&got, "tick %d %s %s\n", tick, spinnerGlyph(), foreground)
	}

	want, err := os.ReadFile("testdata/spinner_pulse.golden")
	if err != nil {
		t.Fatalf("read spinner pulse golden: %v", err)
	}
	if got.String() != string(want) {
		t.Fatalf("spinner pulse golden mismatch:\n--- got ---\n%s\n--- want ---\n%s", got.String(), want)
	}
}

func assertNoBorrowedSpinnerGlyph(t *testing.T, value string) {
	t.Helper()
	for _, glyph := range []string{"·", "✢", "*", "✶", "✻", "✽"} {
		if strings.Contains(value, glyph) {
			t.Fatalf("spinner output still contains borrowed glyph %q: %q", glyph, value)
		}
	}
}
