package tui

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/abietic/yhc/engine/permission"
)

var (
	hardcodedHexColorRE = regexp.MustCompile(`#[0-9A-Fa-f]{6}`)
	literalColorCtorRE  = regexp.MustCompile(`lipgloss\.Color\(\s*"[^"]+"\s*\)`)
)

func TestNoHardcodedColorsOutsideTheme(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") || path == "theme.go" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for lineIndex, line := range strings.Split(string(data), "\n") {
			if hardcodedHexColorRE.MatchString(line) || literalColorCtorRE.MatchString(line) {
				t.Errorf("%s:%d: hardcoded color outside theme.go: %s",
					path, lineIndex+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan production TUI source: %v", err)
	}
}

func TestSemanticStatusColorsFollowThemeTokens(t *testing.T) {
	for _, theme := range supportedThemeNames {
		t.Run(string(theme), func(t *testing.T) {
			styles := StylesForTheme(theme)
			if got, want := styles.ToolRunning.GetForeground(), styles.AssistantPrefix.GetForeground(); got != want {
				t.Errorf("ToolRunning foreground = %v, want brand %v", got, want)
			}
			if got := styles.ToolRunning.GetForeground(); got == styles.Warning.GetForeground() {
				t.Errorf("ToolRunning foreground = warning %v; amber must remain warning-only", got)
			}

			cases := []struct {
				name     string
				severity ErrorSeverity
				want     tuiColor
			}{
				{"info", SeverityInfo, styles.ToolRunning.GetForeground()},
				{"warning", SeverityWarning, styles.Warning.GetForeground()},
				{"error", SeverityError, styles.Error.GetForeground()},
			}
			for _, tc := range cases {
				if got := errorTitleStyle(tc.severity, styles).GetForeground(); got != tc.want {
					t.Errorf("%s title foreground = %v, want %v", tc.name, got, tc.want)
				}
			}
		})
	}
}

func TestHeaderModeBadgesUseSemanticTokens(t *testing.T) {
	styles := StylesForTheme(ThemePolarNight)
	app := New(Config{Model: "test-model", Resumed: true})
	app.styles = styles

	cases := []struct {
		name      string
		inputMode InputMode
		permMode  permission.Mode
		badge     string
		want      string
	}{
		{"shell", InputShell, permission.ModeDefault, "[SHELL]", styles.Warning.Bold(true).Render("[SHELL]")},
		{"plan", InputNormal, permission.ModePlan, "[PLAN]", styles.AuroraSky.Bold(true).Render("[PLAN]")},
		{"yolo", InputNormal, permission.ModeBypassPermissions, "[YOLO]", styles.Error.Bold(true).Render("[YOLO]")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app.inputMode = tc.inputMode
			app.permMode = tc.permMode
			rendered := app.renderHeader()
			if !strings.Contains(rendered, tc.want) {
				t.Fatalf("%s badge does not use semantic token: rendered=%q want fragment=%q", tc.badge, rendered, tc.want)
			}
		})
	}
}

func TestSemanticColorGolden(t *testing.T) {
	var got strings.Builder
	for _, theme := range []ThemeName{ThemePolarNight, ThemeDaybreak, ThemeDarkAnsi} {
		styles := StylesForTheme(theme)
		fmt.Fprintf(&got, "[%s]\n", theme)

		cases := []struct {
			label string
			text  string
			style lipgloss.Style
		}{
			{"shell", "[SHELL]", styles.Warning.Bold(true)},
			{"plan", "[PLAN]", styles.AuroraSky.Bold(true)},
			{"yolo", "[YOLO]", styles.Error.Bold(true)},
			{"running", "●", styles.ToolRunning},
			{"info-title", "Info", errorTitleStyle(SeverityInfo, styles)},
			{"warning-title", "Warning", errorTitleStyle(SeverityWarning, styles)},
			{"error-title", "Error", errorTitleStyle(SeverityError, styles)},
		}
		for _, tc := range cases {
			rendered := tc.style.Render(tc.text)
			fmt.Fprintf(&got, "%s=%q\n", tc.label, lastSGRBefore(rendered, tc.text, false))
		}
		got.WriteByte('\n')
	}

	want, err := os.ReadFile("testdata/semantic_colors.golden")
	if err != nil {
		t.Fatalf("read semantic color golden: %v\n--- got ---\n%s", err, got.String())
	}
	if strings.TrimSpace(got.String()) != strings.TrimSpace(string(want)) {
		t.Fatalf("semantic color golden mismatch:\n--- got ---\n%s--- want ---\n%s", got.String(), want)
	}
}
