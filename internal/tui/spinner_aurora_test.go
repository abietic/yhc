package tui

import (
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
)

// auroraTestPhases are the three gradient stops plus both segment midpoints.
var auroraTestPhases = []float64{0, 0.25, 0.5, 0.75, 1}

func TestAuroraShimmerTruecolorStops(t *testing.T) {
	for _, theme := range []ThemeName{ThemePolarNight, ThemeDaybreak, ThemeSnowy, ThemeAubergine} {
		t.Run(string(theme), func(t *testing.T) {
			styles := StylesForTheme(theme)
			brandRGB, ok := truecolorRGB(styleForegroundString(styles.AssistantPrefix))
			if !ok {
				t.Fatalf("%s brand foreground is not truecolor", theme)
			}
			skyRGB, ok := truecolorRGB(styleForegroundString(styles.AuroraSky))
			if !ok {
				t.Fatalf("%s auroraSky foreground is not truecolor", theme)
			}
			violetRGB, ok := truecolorRGB(styleForegroundString(styles.DialogTitle))
			if !ok {
				t.Fatalf("%s permission foreground is not truecolor", theme)
			}

			hex := func(rgb [3]uint8) string {
				return fmt.Sprintf("#%02X%02X%02X", rgb[0], rgb[1], rgb[2])
			}
			want := map[float64]string{
				0:    styleForegroundString(styles.AssistantPrefix),
				0.25: hex(interpolateRGB(brandRGB, skyRGB, 0.5)),
				0.5:  styleForegroundString(styles.AuroraSky),
				0.75: hex(interpolateRGB(skyRGB, violetRGB, 0.5)),
				1:    styleForegroundString(styles.DialogTitle),
			}
			for _, phase := range auroraTestPhases {
				if got := tuiColorString(auroraShimmerColor(styles, phase)); got != want[phase] {
					t.Fatalf("%s phase %.2f = %q, want %q", theme, phase, got, want[phase])
				}
			}
			if got := tuiColorString(auroraShimmerColor(styles, -1)); got != want[0] {
				t.Fatalf("%s low clamp = %q, want %q", theme, got, want[0])
			}
			if got := tuiColorString(auroraShimmerColor(styles, 2)); got != want[1] {
				t.Fatalf("%s high clamp = %q, want %q", theme, got, want[1])
			}
		})
	}
}

func TestAuroraShimmerANSIStaysFlatSpinnerShimmer(t *testing.T) {
	for _, theme := range []ThemeName{ThemeDarkAnsi, ThemeLightAnsi} {
		t.Run(string(theme), func(t *testing.T) {
			styles := StylesForTheme(theme)
			want := styleForegroundString(styles.SpinnerShimmer)
			for _, phase := range auroraTestPhases {
				got := tuiColorString(auroraShimmerColor(styles, phase))
				if strings.HasPrefix(got, "#") {
					t.Fatalf("%s phase %.2f constructed truecolor %q", theme, phase, got)
				}
				if got != want {
					t.Fatalf("%s phase %.2f = %q, want flat SpinnerShimmer %q", theme, phase, got, want)
				}
				rendered := RenderShimmerText(
					"Shimmer",
					15,
					lipgloss.Color(styleForegroundString(styles.AssistantPrefix)),
					lipgloss.Color(got),
				)
				if strings.Contains(rendered, "38;2;") {
					t.Fatalf("%s phase %.2f emitted a truecolor escape: %q", theme, phase, rendered)
				}
			}
		})
	}
}

func TestAuroraShimmerPhaseSinglePeriod(t *testing.T) {
	// Thinking keeps the 3s delay before shimmer starts.
	for _, elapsed := range []float64{0, 1.5, 2.9} {
		if got := spinnerShimmerPhase(elapsed, SpinnerThinking); got != 0 {
			t.Fatalf("thinking phase at %.1fs = %v, want 0 during delay", elapsed, got)
		}
	}

	// One shared 2.4s sine period for every mode; the old tool-use 1s
	// exception is gone.
	for _, elapsed := range []float64{0.4, 1.2, 2.9, 4.4} {
		responding := spinnerShimmerPhase(elapsed, SpinnerResponding)
		if got := spinnerShimmerPhase(elapsed, SpinnerToolUse); got != responding {
			t.Fatalf("tool-use phase at %.1fs = %v, want shared period %v", elapsed, got, responding)
		}
		if elapsed >= thinkingShimmerDelay {
			if got := spinnerShimmerPhase(elapsed, SpinnerThinking); got != responding {
				t.Fatalf("thinking phase at %.1fs = %v, want shared period %v", elapsed, got, responding)
			}
		}
	}

	// The period is exactly 2.4s and sine-shaped over it.
	for _, elapsed := range []float64{0.3, 1.1, 2.2, 7.9} {
		got := spinnerShimmerPhase(elapsed, SpinnerResponding)
		want := (math.Sin(elapsed*math.Pi*2/spinnerShimmerPeriod) + 1) / 2
		if got != want {
			t.Fatalf("phase at %.1fs = %v, want %v", elapsed, got, want)
		}
		wrapped := spinnerShimmerPhase(elapsed+spinnerShimmerPeriod, SpinnerResponding)
		if math.Abs(wrapped-got) > 1e-9 {
			t.Fatalf("phase at %.1fs does not repeat after 2.4s: %v vs %v", elapsed, wrapped, got)
		}
	}
}

func TestSpinnerWaitingUsesAuroraSkyThenStalledToken(t *testing.T) {
	newWaitingApp := func(lastEvent time.Time) *App {
		app := New(Config{Resumed: true, Model: "test-model"})
		app.styles = StylesForTheme(ThemePolarNight)
		app.running = true
		app.spinnerState = SpinnerState{
			Mode:          SpinnerThinking,
			StartTime:     time.Now(),
			LastEventTime: lastEvent,
		}
		return app
	}

	// Early stall (intensity <= 0.5) renders the waiting verb in AuroraSky.
	// Keep almost the whole one-second early-stall window available because
	// race instrumentation can make renderSpinner itself take hundreds of
	// milliseconds.
	early := newWaitingApp(time.Now().Add(-stallThreshold - time.Millisecond))
	wantEarly := "  " + early.spinnerPulseIcon(early.styles.AuroraSky, early.spinnerCount) +
		" " + early.styles.AuroraSky.Render("Thinking… (waiting)")
	if got := early.renderSpinner(); got != wantEarly {
		t.Fatalf("early stall spinner = %q, want AuroraSky waiting line %q", got, wantEarly)
	}

	// Full stall (intensity > 0.5) renders the waiting verb in SpinnerStalled.
	full := newWaitingApp(time.Now().Add(-stallThreshold - 3*time.Second))
	wantFull := "  " + full.spinnerPulseIcon(full.styles.SpinnerStalled, full.spinnerCount) +
		" " + full.styles.SpinnerStalled.Render("Thinking… (waiting)")
	if got := full.renderSpinner(); got != wantFull {
		t.Fatalf("full stall spinner = %q, want SpinnerStalled waiting line %q", got, wantFull)
	}
}

func TestReducedMotionSpinnerShimmerFlatBrandVerb(t *testing.T) {
	app := New(Config{Resumed: true, Model: "test-model", ReducedMotion: true})
	app.styles = StylesForTheme(ThemePolarNight)
	app.running = true
	app.spinnerState = SpinnerState{
		Mode:      SpinnerThinking,
		StartTime: time.Now().Add(-100 * time.Millisecond),
	}

	want := "  " + app.spinnerPulseIcon(app.styles.AssistantPrefix, 0) +
		" " + app.styles.AssistantPrefix.Render("Thinking...")
	// The flat brand verb never shimmers: the whole line is identical at
	// every tick, with no glimmer segmentation or phase-dependent color.
	for counter := 0; counter <= 2*spinnerBreathTicks; counter++ {
		app.spinnerCount = counter
		if got := app.renderSpinner(); got != want {
			t.Fatalf("reduced-motion spinner at tick %d = %q, want flat brand line %q", counter, got, want)
		}
	}
}

func TestRenderAuroraShimmerTextUsesDeterministicPhase(t *testing.T) {
	styles := StylesForTheme(ThemePolarNight)
	baseColor := lipgloss.Color(styleForegroundString(styles.AssistantPrefix))
	for _, phase := range []float64{0, 0.5, 1} {
		want := RenderShimmerText("Responding…", 15, baseColor, auroraShimmerColor(styles, phase))
		got := renderAuroraShimmerText("Responding…", 15, styles, phase)
		if got != want {
			t.Fatalf("phase %.2f render = %q, want %q", phase, got, want)
		}
		if plain := stripANSIForTest(got); plain != "Responding…" {
			t.Fatalf("phase %.2f visible text = %q, want unchanged verb", phase, plain)
		}
	}
}

func TestRenderSpinnerAuroraGolden(t *testing.T) {
	var got strings.Builder
	got.WriteString("# P19.2.2 aurora verb shimmer: brand teal -> AuroraSky -> permission violet\n")
	for _, theme := range []ThemeName{ThemePolarNight, ThemeDaybreak, ThemeDarkAnsi, ThemeLightAnsi} {
		styles := StylesForTheme(theme)
		for _, phase := range auroraTestPhases {
			fmt.Fprintf(
				&got,
				"%s phase %.2f %s\n",
				theme,
				phase,
				tuiColorString(auroraShimmerColor(styles, phase)),
			)
		}
	}

	want, err := os.ReadFile("testdata/spinner_aurora.golden")
	if err != nil {
		t.Fatalf("read spinner aurora golden: %v", err)
	}
	if got.String() != string(want) {
		t.Fatalf("spinner aurora golden mismatch:\n--- got ---\n%s\n--- want ---\n%s", got.String(), want)
	}
}
