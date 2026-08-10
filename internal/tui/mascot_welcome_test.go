package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/permission"
)

func exactMascot(pose MascotPose) [MascotHeight]string {
	art := RenderMascot(pose)
	for i := range art {
		art[i] = xansi.Strip(art[i])
	}
	return art
}

func TestEnoExactPoses(t *testing.T) {
	tests := []struct {
		name string
		pose MascotPose
		want [MascotHeight]string
	}{
		{"default", PoseDefault, [MascotHeight]string{
			"  ▟▙       ▟▙  ",
			"  ▜██▙   ▟██▌  ",
			" ▗███████████▖ ",
			" ▐██●█████●██▌ ",
			"▟██████●██████▙",
			"   ▝▜█████▛▘   ",
		}},
		{"look-left", PoseLookLeft, [MascotHeight]string{
			"  ▟▙       ▟▙  ",
			"  ▜██▙   ▟██▌  ",
			" ▗███████████▖ ",
			" ▐█●█████●███▌ ",
			"▟██████●██████▙",
			"   ▝▜█████▛▘   ",
		}},
		{"look-right", PoseLookRight, [MascotHeight]string{
			"  ▟▙       ▟▙  ",
			"  ▜██▙   ▟██▌  ",
			" ▗███████████▖ ",
			" ▐███●█████●█▌ ",
			"▟██████●██████▙",
			"   ▝▜█████▛▘   ",
		}},
		{"blink", PoseBlink, [MascotHeight]string{
			"  ▟▙       ▟▙  ",
			"  ▜██▙   ▟██▌  ",
			" ▗███████████▖ ",
			" ▐██▄█████▄██▌ ",
			"▟██████●██████▙",
			"   ▝▜█████▛▘   ",
		}},
		{"happy", PoseHappy, [MascotHeight]string{
			"  ▟▙       ▟▙  ",
			"  ▜██▙   ▟██▌  ",
			" ▗███████████▖ ",
			" ▐██◡█████◡██▌ ",
			"▟██████●██████▙",
			"   ▝▜█████▛▘   ",
		}},
		{"celebrate", PoseCelebrate, [MascotHeight]string{
			"✧   ▟▙ ✦ ▟▙   ✧",
			"  ▜██▙   ▟██▌  ",
			" ▗███████████▖ ",
			" ▐██✦█████✦██▌ ",
			"▟██████●██████▙",
			"   ▝▜█████▛▘   ",
		}},
		{"sleep", PoseSleep, [MascotHeight]string{
			"  ▟▙       ▟▙ z",
			"  ▜██▙   ▟██▌ z",
			" ▗███████████▖ ",
			" ▐██─█████─██▌ ",
			"▟██████●██████▙",
			"   ▝▜█████▛▘   ",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exactMascot(tt.pose); got != tt.want {
				t.Fatalf("pose = %#v, want %#v", got, tt.want)
			}
			for _, line := range RenderMascot(tt.pose) {
				if width := xansi.StringWidth(xansi.Strip(line)); width != MascotWidth {
					t.Fatalf("row width = %d, want %d: %q", width, MascotWidth, xansi.Strip(line))
				}
			}
		})
	}
	if got := exactMascot(MascotPose(99)); got != exactMascot(PoseDefault) {
		t.Fatalf("unknown pose = %#v, want default", got)
	}
}

func TestEnoFramesAndTiming(t *testing.T) {
	if MascotFrameDuration != 60*time.Millisecond {
		t.Fatalf("frame duration = %s", MascotFrameDuration)
	}
	if len(jumpWave) != 12 {
		t.Fatalf("jump-wave frames = %d, want 12", len(jumpWave))
	}
	if len(lookAround) != 11 {
		t.Fatalf("look-around frames = %d, want 11", len(lookAround))
	}
	wantJump := append(append(append(append(append(
		hold(PoseDefault, 1, 2), hold(PoseCelebrate, 0, 3)...),
		hold(PoseDefault, 0, 1)...), hold(PoseDefault, 1, 2)...),
		hold(PoseCelebrate, 0, 3)...), hold(PoseDefault, 0, 1)...)
	for i := range wantJump {
		if jumpWave[i] != wantJump[i] {
			t.Fatalf("jump frame %d = %#v, want %#v", i, jumpWave[i], wantJump[i])
		}
	}
}

func TestEnoFaceCellsRenderAsTerminalDefault(t *testing.T) {
	for _, theme := range []ThemeName{
		ThemePolarNight, ThemeDaybreak, ThemeDarkAnsi,
		ThemeLightAnsi, ThemeSnowy, ThemeAubergine,
	} {
		tones := StylesForTheme(theme).enoTones()
		faceStyle := tones.styleFor(enoToneFace)
		_, noFaceForeground := faceStyle.GetForeground().(lipgloss.NoColor)
		_, noFaceBackground := faceStyle.GetBackground().(lipgloss.NoColor)
		if !noFaceForeground || !noFaceBackground {
			t.Fatalf("%s face style has explicit color: foreground=%v background=%v",
				theme, faceStyle.GetForeground(), faceStyle.GetBackground())
		}
		for name, style := range map[string]lipgloss.Style{
			"body": tones.body, "outline": tones.outline,
			"sparkle": tones.sparkle, "subtle": tones.subtle,
		} {
			if _, ok := style.GetBackground().(lipgloss.NoColor); !ok {
				t.Fatalf("%s %s tone paints background %v", theme, name, style.GetBackground())
			}
		}
		for pose := range enoPoses {
			art := renderMascotStyled(pose, tones)
			rendered := strings.Join(art[:], "\n")
			if strings.Contains(rendered, "\x1b[48;") {
				t.Fatalf("%s pose %d paints a background: %q", theme, pose, rendered)
			}
			for _, face := range []string{"●", "▄", "◡", "─"} {
				from := 0
				for {
					index := strings.Index(rendered[from:], face)
					if index < 0 {
						break
					}
					index += from
					if !strings.HasSuffix(rendered[:index], "\x1b[0m") {
						t.Errorf("%s pose %d face glyph %q does not start in terminal-default style: %q", theme, pose, face, rendered)
					}
					from = index + len(face)
				}
			}
		}
	}

	defaultArt := RenderMascot(PoseDefault)
	if rendered := strings.Join(defaultArt[:], "\n"); !strings.Contains(rendered, "\x1b[38;2;79;227;193m") {
		t.Fatalf("default pose missing EnoBody truecolor: %q", rendered)
	}
}

func TestEnoAnsiTones(t *testing.T) {
	for _, theme := range []ThemeName{ThemeDarkAnsi, ThemeLightAnsi} {
		tones := StylesForTheme(theme).enoTones()
		for name, color := range map[string]tuiColor{
			"body":    tones.body.GetForeground(),
			"outline": tones.outline.GetForeground(),
			"sparkle": tones.sparkle.GetForeground(),
			"subtle":  tones.subtle.GetForeground(),
		} {
			value := fmt.Sprint(color)
			if value == "" || strings.HasPrefix(value, "#") {
				t.Errorf("%s %s tone = %q, want ANSI-16", theme, name, value)
			}
		}
	}
}

func TestEnoReducedMotionStaticPose(t *testing.T) {
	app := sizedWelcomeApp(80, 24, Config{
		ReducedMotion: true,
		Fullscreen:    true,
		MouseEnabled:  true,
		Chooser:       func(int) int { return 0 },
	})
	bounds, ok := app.welcomeMascotBounds()
	if !ok {
		t.Fatal("mascot not visible at full welcome tier")
	}
	click := tuiMouseMsg{
		X: bounds.x, Y: bounds.y,
		Button: tea.MouseLeft, Action: mouseActionPress,
	}
	if cmd, handled := app.handleMascotMouse(click); cmd != nil || handled || app.mascotAnim.Active() {
		t.Fatal("reduced motion allowed mascot click animation")
	}
	if got, want := app.mascotFrameLines(), renderMascotStyled(PoseDefault, app.styles.enoTones()); got != want {
		t.Fatalf("reduced motion pose = %v, want static default %v", got, want)
	}
}

func TestMascotAnimatorStartsImmediatelyAndIgnoresActiveTriggers(t *testing.T) {
	anim := NewMascotAnimator(func(int) int { return 0 })
	if anim.Active() || anim.CurrentFrame() != (Frame{Pose: PoseDefault}) {
		t.Fatal("animator did not start idle")
	}
	if cmd := anim.TriggerAnimation(); cmd == nil {
		t.Fatal("first trigger did not schedule a tick")
		return
	}
	if got := anim.CurrentFrame(); got != jumpWave[0] {
		t.Fatalf("first visible frame = %#v, want %#v", got, jumpWave[0])
	}
	index := anim.index
	sequence := anim.sequence
	if cmd := anim.TriggerAnimation(); cmd != nil {
		t.Fatal("active trigger should be ignored")
		return
	}
	if anim.index != index || &anim.sequence[0] != &sequence[0] {
		t.Fatal("active trigger replaced animation state")
	}
	for anim.Active() {
		anim.Tick()
	}
	if got := anim.CurrentFrame(); got != (Frame{Pose: PoseDefault}) {
		t.Fatalf("final frame = %#v, want idle", got)
	}
}

func sizedWelcomeApp(width, height int, cfg Config) *App {
	app := New(cfg)
	app.width = width
	app.height = height
	app.updateLayout()
	return app
}

func TestMascotClickTriggerGuards(t *testing.T) {
	base := Config{Fullscreen: true, MouseEnabled: true, Chooser: func(int) int { return 0 }}
	app := sizedWelcomeApp(57, 20, base)
	bounds, ok := app.welcomeMascotBounds()
	if !ok {
		t.Fatal("visible condensed mascot has no bounds")
	}
	click := tuiMouseMsg{X: bounds.x, Y: bounds.y, Button: tea.MouseLeft, Action: mouseActionPress}
	if cmd, handled := app.handleMascotMouse(click); cmd == nil || !handled || !app.mascotAnim.Active() {
		t.Fatal("eligible click did not start animation")
		return
	}

	tests := []struct {
		name   string
		width  int
		mutate func(*App)
	}{
		{"mascot hidden", 56, nil},
		{"not fullscreen", 57, func(a *App) { a.fullscreen = false }},
		{"mouse disabled", 57, func(a *App) { a.mouseEnabled = false }},
		{"reduced motion", 57, func(a *App) { a.reducedMotion = true }},
		{"not welcome", 57, func(a *App) { a.state = StateChat }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := sizedWelcomeApp(tt.width, 20, base)
			if tt.mutate != nil {
				tt.mutate(a)
			}
			msg := tuiMouseMsg{X: 0, Y: 0, Button: tea.MouseLeft, Action: mouseActionPress}
			if b, visible := a.welcomeMascotBounds(); visible {
				msg.X, msg.Y = b.x, b.y
			}
			if cmd, handled := a.handleMascotMouse(msg); cmd != nil || handled || a.mascotAnim.Active() {
				t.Fatal("ineligible click started animation")
				return
			}
		})
	}
}

func TestWelcomeResponsiveWidthTiersAndHeightBudget(t *testing.T) {
	for _, width := range []int{40, 56, 57, 69, 70, 80, 100, 150} {
		for _, height := range []int{12, 20, 30} {
			t.Run(fmt.Sprintf("%dx%d", width, height), func(t *testing.T) {
				app := sizedWelcomeApp(width, height, Config{Chooser: func(int) int { return 0 }})
				view := app.renderView()
				plain := xansi.Strip(view)
				if got := lineCount(view); got > height {
					t.Fatalf("view height = %d, terminal height = %d\n%s", got, height, plain)
				}
				for i, line := range strings.Split(view, "\n") {
					if got := xansi.StringWidth(line); got > width {
						t.Fatalf("line %d width = %d, terminal width = %d: %q", i, got, width, xansi.Strip(line))
					}
				}
				hasMascot := strings.Contains(plain, "▝▜█████▛▘")
				hasWelcomeBorder := strings.Contains(strings.Split(plain, "\n")[0], "╭")
				switch welcomeTierFor(width, height) {
				case welcomeCompactText:
					if hasMascot || hasWelcomeBorder {
						t.Fatalf("compact text tier rendered mascot/border\n%s", plain)
					}
				case welcomeCondensedMascot:
					if !hasMascot || hasWelcomeBorder {
						t.Fatalf("condensed tier mismatch\n%s", plain)
					}
				case welcomeFullBordered:
					if !hasMascot || !hasWelcomeBorder {
						t.Fatalf("full tier mismatch\n%s", plain)
					}
				}
				if !strings.Contains(plain, "╭") && !strings.Contains(plain, "YHC") {
					t.Fatal("welcome content was clipped before editor/status")
				}
				if !strings.Contains(plain, "❯") || !strings.Contains(plain, "default") {
					t.Fatalf("editor/status missing\n%s", plain)
				}
			})
		}
	}

	tooSmall := sizedWelcomeApp(39, 20, Config{})
	if got := xansi.Strip(tooSmall.renderView()); !strings.Contains(strings.ToLower(got), "too small") {
		t.Fatalf("39-column view did not use too-small screen: %q", got)
	}
}

func TestEnoCondensedWelcomeBudgetsProjectPathBeforeDimensions(t *testing.T) {
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		CWD: "/Users/yuhaichuan/Code/repos/eino-agent",
	})
	defer eng.Close()

	for width := 57; width <= 61; width++ {
		t.Run(fmt.Sprintf("%d-columns", width), func(t *testing.T) {
			app := sizedWelcomeApp(width, 20, Config{
				Engine:  eng,
				Model:   "test-model",
				Chooser: func(int) int { return 0 },
			})
			for lineNumber, line := range strings.Split(app.renderView(), "\n") {
				if got := xansi.StringWidth(line); got > width {
					t.Fatalf("line %d width = %d, terminal width = %d: %q",
						lineNumber, got, width, xansi.Strip(line))
				}
			}
		})
	}
}

func TestEnoWelcomeResponsiveGolden(t *testing.T) {
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{CWD: "/workspace/eno-agent"})
	defer eng.Close()

	var golden strings.Builder
	for _, viewport := range []struct {
		name          string
		width, height int
	}{
		{name: "too-small", width: 39, height: 11},
		{name: "compact-text", width: 56, height: 24},
		{name: "condensed-mascot", width: 69, height: 24},
		{name: "full-bordered", width: 80, height: 24},
		{name: "wide-full-bordered", width: 150, height: 24},
	} {
		app := sizedWelcomeApp(viewport.width, viewport.height, Config{
			Engine:        eng,
			Model:         "test-model",
			ReducedMotion: true,
			Chooser:       func(int) int { return 0 },
		})
		app.welcomeGreeting = "Welcome back. What are we building?"
		app.welcomeTip = "Use /commands for available slash commands"

		golden.WriteString("== " + viewport.name + " ==\n")
		golden.WriteString(normalizeAppLayoutGolden(app.renderView()))
		golden.WriteString("\n")
	}

	path := "testdata/eno_welcome.golden"
	actual := golden.String()
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(actual), 0o600); err != nil {
			t.Fatalf("update Eno welcome golden: %v", err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Eno welcome golden: %v", err)
	}
	if actual != string(want) {
		t.Fatalf("Eno welcome golden mismatch:\n--- got ---\n%s\n--- want ---\n%s", actual, want)
	}
}

func TestWelcomeHeightElevenUsesStableTooSmallView(t *testing.T) {
	for _, width := range []int{39, 40, 56, 57, 69, 70, 80, 100} {
		t.Run(fmt.Sprintf("%dx11", width), func(t *testing.T) {
			app := sizedWelcomeApp(width, 11, Config{Chooser: func(int) int { return 0 }})
			view := app.renderView()
			plain := xansi.Strip(view)

			for _, want := range []string{
				"Terminal too small",
				fmt.Sprintf("Current: %d x 11", width),
				fmt.Sprintf("Minimum: %d x %d", minTermWidth, minTermHeight),
				"Please resize your terminal window.",
			} {
				if !strings.Contains(plain, want) {
					t.Fatalf("too-small view missing %q\n%s", want, plain)
				}
			}

			for i, line := range strings.Split(view, "\n") {
				if got := xansi.StringWidth(line); got > width {
					t.Fatalf("line %d width = %d, terminal width = %d: %q", i, got, width, xansi.Strip(line))
				}
			}

			for _, unexpected := range []string{"▝▜█████▛▘", "YHC", "╭", "❯", "default permissions"} {
				if strings.Contains(plain, unexpected) {
					t.Fatalf("too-small view rendered normal welcome UI marker %q\n%s", unexpected, plain)
				}
			}
			if second := app.renderView(); second != view {
				t.Fatalf("too-small view changed between renders\nfirst:\n%s\nsecond:\n%s", plain, xansi.Strip(second))
			}
		})
	}
}

func TestWelcomeDynamicStateAndSessionChoicesStayStable(t *testing.T) {
	choices := []int{2, 1, 0}
	choiceCalls := 0
	chooser := func(int) int {
		v := choices[min(choiceCalls, len(choices)-1)]
		choiceCalls++
		return v
	}
	project := t.TempDir() + "/项目"
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{CWD: project, PermissionMode: permission.ModePlan})
	defer eng.Close()
	app := sizedWelcomeApp(100, 20, Config{Engine: eng, Model: "provider:model-a", Chooser: chooser})
	greeting, tip := app.welcomeGreeting, app.welcomeTip
	first := xansi.Strip(app.renderWelcome())
	for _, want := range []string{"provider:model-a", "plan mode", "项目", "100x20", greeting, tip} {
		if !strings.Contains(first, want) {
			t.Fatalf("welcome missing %q\n%s", want, first)
		}
	}

	app.model = "provider:model-b"
	app.inputMode = InputShell
	app.width, app.height = 57, 12
	app.updateLayout()
	second := xansi.Strip(app.renderWelcome())
	for _, want := range []string{"model-b", "shell input", "57x12"} {
		if !strings.Contains(second, want) {
			t.Fatalf("updated welcome missing %q\n%s", want, second)
		}
	}
	if app.welcomeGreeting != greeting || app.welcomeTip != tip {
		t.Fatal("session greeting/tip changed across resize or state updates")
	}
	if choiceCalls != 2 {
		t.Fatalf("session chooser calls = %d, want exactly 2 before interaction", choiceCalls)
	}
}

func TestWelcomeTipsRotateAcrossInteractiveRestarts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	transcriptDir := t.TempDir()
	newApp := func(sessionID string) (*App, *engine.QueryEngine) {
		query := engine.NewQueryEngine(engine.QueryEngineConfig{
			SessionID: sessionID, CWD: t.TempDir(), TranscriptDir: transcriptDir,
			EnableLongSessionServices: true,
		})
		return New(Config{Engine: query}), query
	}

	first, firstEngine := newApp("first")
	firstTip := first.welcomeTip
	firstEngine.Close()
	second, secondEngine := newApp("second")
	defer secondEngine.Close()
	if second.welcomeTip == firstTip {
		t.Fatalf("welcome tip repeated across restart: %q", firstTip)
	}
	if _, err := os.Stat(filepath.Join(transcriptDir, "tip-history.json")); err != nil {
		t.Fatalf("tip history was not persisted: %v", err)
	}
}

func TestFirstRunWelcomeTipStaysPinned(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	transcriptDir := t.TempDir()
	query := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID: "first-run", CWD: t.TempDir(), TranscriptDir: transcriptDir,
		EnableLongSessionServices: true,
	})
	defer query.Close()
	app := New(Config{Engine: query})
	if !strings.HasPrefix(app.welcomeTip, "First run!") {
		t.Fatalf("persistent rotation replaced first-run guidance: %q", app.welcomeTip)
	}
	if _, err := os.Stat(filepath.Join(transcriptDir, "tip-history.json")); !os.IsNotExist(err) {
		t.Fatalf("hidden rotated tip was recorded during first run: %v", err)
	}
}

func TestActiveMascotStopsWhenResizeHidesIt(t *testing.T) {
	app := sizedWelcomeApp(57, 20, Config{Fullscreen: true, MouseEnabled: true, Chooser: func(int) int { return 0 }})
	app.mascotAnim.TriggerAnimation()
	if !app.mascotAnim.Active() {
		t.Fatal("animation did not start")
	}
	app.Update(tea.WindowSizeMsg{Width: 56, Height: 20})
	if app.mascotAnim.Active() {
		t.Fatal("hidden mascot retained active animation")
	}
	if _, cmd := app.Update(mascotTickMsg{}); cmd != nil {
		t.Fatal("hidden mascot tick scheduled another frame")
		return
	}
}
