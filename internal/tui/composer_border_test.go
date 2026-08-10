package tui

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/abietic/yhc/engine/permission"
)

func TestComposerBorderFollowsCurrentMode(t *testing.T) {
	for _, theme := range []ThemeName{ThemePolarNight, ThemeDaybreak, ThemeDarkAnsi} {
		t.Run(string(theme), func(t *testing.T) {
			app := newTestApp(80, 24)
			app.styles = StylesForTheme(theme)

			cases := []struct {
				name      string
				inputMode InputMode
				permMode  permission.Mode
				want      tuiColor
			}{
				{"default", InputNormal, permission.ModeDefault, app.styles.AssistantPrefix.GetForeground()},
				{"plan", InputNormal, permission.ModePlan, app.styles.AuroraSky.GetForeground()},
				{"shell", InputShell, permission.ModeDefault, app.styles.Warning.GetForeground()},
				{"bypass", InputNormal, permission.ModeBypassPermissions, app.styles.Error.GetForeground()},
				{"shell-over-plan", InputShell, permission.ModePlan, app.styles.Warning.GetForeground()},
				{"shell-over-bypass", InputShell, permission.ModeBypassPermissions, app.styles.Warning.GetForeground()},
			}
			for _, tc := range cases {
				app.inputMode = tc.inputMode
				app.permMode = tc.permMode

				if got := app.composerBorderColor(); fmt.Sprint(got) != fmt.Sprint(tc.want) {
					t.Errorf("%s color = %v, want %v", tc.name, got, tc.want)
				}
				rendered := app.renderEditor()
				gotSGR := lastSGRBefore(rendered, "╭", false)
				wantSGR := lastSGRBefore(
					lipgloss.NewStyle().Foreground(tc.want).Render("╭"),
					"╭",
					false,
				)
				if gotSGR != wantSGR {
					t.Errorf("%s border SGR = %q, want %q", tc.name, gotSGR, wantSGR)
				}
			}
		})
	}
}

func TestComposerBorderTracksModeOnNextFrame(t *testing.T) {
	app := newTestApp(80, 24)
	app.styles = StylesForTheme(ThemePolarNight)

	steps := []struct {
		name      string
		inputMode InputMode
		permMode  permission.Mode
		want      tuiColor
	}{
		{"default", InputNormal, permission.ModeDefault, app.styles.AssistantPrefix.GetForeground()},
		{"plan", InputNormal, permission.ModePlan, app.styles.AuroraSky.GetForeground()},
		{"shell", InputShell, permission.ModePlan, app.styles.Warning.GetForeground()},
		{"bypass", InputNormal, permission.ModeBypassPermissions, app.styles.Error.GetForeground()},
	}
	var previousSGR string
	for _, step := range steps {
		app.inputMode = step.inputMode
		app.permMode = step.permMode

		got := lastSGRBefore(app.renderEditor(), "╭", false)
		want := lastSGRBefore(
			lipgloss.NewStyle().Foreground(step.want).Render("╭"),
			"╭",
			false,
		)
		if got != want {
			t.Fatalf("%s next-frame border SGR = %q, want %q", step.name, got, want)
		}
		if previousSGR != "" && got == previousSGR {
			t.Fatalf("%s next-frame border SGR remained %q", step.name, got)
		}
		previousSGR = got
	}
}

func TestComposerBorderPreservesGeometry(t *testing.T) {
	viewports := []struct {
		name          string
		width         int
		height        int
		reducedMotion bool
	}{
		{"compact", 48, 18, true},
		{"standard", 80, 24, false},
		{"wide", 150, 24, false},
	}
	modes := []struct {
		input InputMode
		perm  permission.Mode
	}{
		{InputNormal, permission.ModePlan},
		{InputShell, permission.ModeDefault},
		{InputNormal, permission.ModeBypassPermissions},
	}
	for _, theme := range []ThemeName{ThemePolarNight, ThemeDaybreak, ThemeDarkAnsi} {
		for _, viewport := range viewports {
			app := newTestApp(viewport.width, viewport.height)
			app.styles = StylesForTheme(theme)
			app.reducedMotion = viewport.reducedMotion
			app.textarea.SetValue("Explain the next change.")
			app.inputMode = InputNormal
			app.permMode = permission.ModeDefault
			baseline := app.renderEditor()
			baselineWidth := lipgloss.Width(baseline)
			baselineHeight := lipgloss.Height(baseline)

			for _, mode := range modes {
				app.inputMode = mode.input
				app.permMode = mode.perm
				rendered := app.renderEditor()
				if got := lipgloss.Width(rendered); got != baselineWidth {
					t.Errorf("%s/%s width = %d, want %d", theme, viewport.name, got, baselineWidth)
				}
				if got := lipgloss.Height(rendered); got != baselineHeight {
					t.Errorf("%s/%s height = %d, want %d", theme, viewport.name, got, baselineHeight)
				}
			}
		}
	}
}

func TestComposerBorderGolden(t *testing.T) {
	var got strings.Builder
	for _, theme := range []ThemeName{ThemePolarNight, ThemeDaybreak, ThemeDarkAnsi} {
		app := newTestApp(80, 24)
		app.styles = StylesForTheme(theme)
		fmt.Fprintf(&got, "[%s]\n", theme)

		cases := []struct {
			name      string
			inputMode InputMode
			permMode  permission.Mode
		}{
			{"default", InputNormal, permission.ModeDefault},
			{"plan", InputNormal, permission.ModePlan},
			{"shell", InputShell, permission.ModeDefault},
			{"bypass", InputNormal, permission.ModeBypassPermissions},
		}
		for _, tc := range cases {
			app.inputMode = tc.inputMode
			app.permMode = tc.permMode
			rendered := app.renderEditor()
			fmt.Fprintf(
				&got,
				"%s=%q width=%d height=%d\n",
				tc.name,
				lastSGRBefore(rendered, "╭", false),
				lipgloss.Width(rendered),
				lipgloss.Height(rendered),
			)
		}
		got.WriteByte('\n')
	}

	path := "testdata/composer_border.golden"
	actual := strings.TrimSpace(got.String()) + "\n"
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(actual), 0o600); err != nil {
			t.Fatalf("update composer border golden: %v", err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read composer border golden: %v\n--- got ---\n%s", err, actual)
	}
	if actual != string(want) {
		t.Fatalf("composer border golden mismatch:\n--- got ---\n%s--- want ---\n%s", actual, want)
	}
}

func BenchmarkRenderEditorModeBorder(b *testing.B) {
	app := newTestApp(120, 40)
	app.styles = StylesForTheme(ThemePolarNight)
	app.textarea.SetValue("Explain the next change.")
	modes := []struct {
		name  string
		input InputMode
		perm  permission.Mode
	}{
		{"default", InputNormal, permission.ModeDefault},
		{"plan", InputNormal, permission.ModePlan},
		{"shell", InputShell, permission.ModeDefault},
		{"bypass", InputNormal, permission.ModeBypassPermissions},
	}
	for _, mode := range modes {
		b.Run(mode.name, func(b *testing.B) {
			app.inputMode = mode.input
			app.permMode = mode.perm
			b.ReportAllocs()
			for range b.N {
				app.renderEditor()
			}
		})
	}
}
