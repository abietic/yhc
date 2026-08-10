package tui

import (
	"fmt"
	"math"
	"strconv"
	"testing"
)

// TestAuroraDesignSwatchContrastGate guards the P19.0.2 design-reference floor:
// every accent foreground plus the inactive text-side token must reach 4.5:1
// against the canonical Polar Night (#0B0F1A) and Daybreak (#F5F2EC) bg0
// design swatches.
//
// These are design-swatch checks only. The TUI does not paint the user's
// global terminal background, so this test explicitly does not claim any
// contrast guarantee against the terminal default foreground/background.
// Hint/placeholder text, borders, and the stalled spinner state are outside
// the accepted design-swatch floor and are intentionally not covered here.
func TestAuroraDesignSwatchContrastGate(t *testing.T) {
	const minRatio = 4.5

	cases := []struct {
		theme  ThemeName
		swatch string // canonical bg0 design swatch
	}{
		{ThemePolarNight, "#0B0F1A"},
		{ThemeDaybreak, "#F5F2EC"},
	}

	for _, tc := range cases {
		t.Run(string(tc.theme), func(t *testing.T) {
			p := getPalette(tc.theme)
			foregrounds := map[string]tuiColor{
				"brand":       p.brand,
				"permission":  p.permission,
				"auroraSky":   p.auroraSky,
				"green":       p.green,
				"red":         p.red,
				"warning":     p.warning,
				"inactive":    p.inactive,
				"enoBody":     p.enoBody,
				"enoOutline":  p.enoOutline,
				"diffAdded":   p.diffAddedWord,
				"diffRemoved": p.diffRemovedWord,
			}
			for field, fg := range foregrounds {
				ratio, err := contrastRatio(tuiColorString(fg), tc.swatch)
				if err != nil {
					t.Errorf("%s: %v", field, err)
					continue
				}
				if ratio < minRatio {
					t.Errorf("%s %s / %s: contrast %.2f:1, want >= %.1f:1",
						tc.theme, field, tc.swatch, ratio, minRatio)
				}
			}
		})
	}
}

func TestP202DialogInputTextContrastGate(t *testing.T) {
	const minRatio = 4.5
	for _, theme := range []ThemeName{
		ThemePolarNight,
		ThemeDaybreak,
		ThemeSnowy,
		ThemeAubergine,
	} {
		t.Run(string(theme), func(t *testing.T) {
			palette := getPalette(theme)
			ratio, err := contrastRatio(
				tuiColorString(palette.permission),
				tuiColorString(palette.element),
			)
			if err != nil {
				t.Fatal(err)
			}
			if ratio < minRatio {
				t.Fatalf(
					"dialog input text %s / %s: contrast %.2f:1, want >= %.1f:1",
					palette.permission,
					palette.element,
					ratio,
					minRatio,
				)
			}
		})
	}
}

// contrastRatio computes the WCAG 2.x contrast ratio between two #RRGGBB
// sRGB colors.
func contrastRatio(fgHex, bgHex string) (float64, error) {
	fg, err := parseHexColor(fgHex)
	if err != nil {
		return 0, fmt.Errorf("foreground: %w", err)
	}
	bg, err := parseHexColor(bgHex)
	if err != nil {
		return 0, fmt.Errorf("background: %w", err)
	}
	l1, l2 := relativeLuminance(fg), relativeLuminance(bg)
	if l1 < l2 {
		l1, l2 = l2, l1
	}
	return (l1 + 0.05) / (l2 + 0.05), nil
}

func parseHexColor(hex string) ([3]float64, error) {
	var rgb [3]float64
	if len(hex) != 7 || hex[0] != '#' {
		return rgb, fmt.Errorf("invalid hex color %q", hex)
	}
	for i := 0; i < 3; i++ {
		v, err := strconv.ParseUint(hex[1+i*2:3+i*2], 16, 8)
		if err != nil {
			return rgb, fmt.Errorf("invalid hex color %q: %v", hex, err)
		}
		rgb[i] = float64(v) / 255
	}
	return rgb, nil
}

func relativeLuminance(rgb [3]float64) float64 {
	linear := func(c float64) float64 {
		if c <= 0.04045 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*linear(rgb[0]) + 0.7152*linear(rgb[1]) + 0.0722*linear(rgb[2])
}
