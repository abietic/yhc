package tui

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/internal/tui/terminalcap"
)

func assertSameThemeColors(t *testing.T, component string, got, want Styles) {
	t.Helper()
	checks := []struct {
		name      string
		got, want tuiColor
	}{
		{"Header", got.Header.GetForeground(), want.Header.GetForeground()},
		{"StatusBar", got.StatusBar.GetForeground(), want.StatusBar.GetForeground()},
		{"UserPrefix", got.UserPrefix.GetForeground(), want.UserPrefix.GetForeground()},
		{"UserMessageBlock", got.UserMessageBlock.GetBackground(), want.UserMessageBlock.GetBackground()},
		{"AssistantPrefix", got.AssistantPrefix.GetForeground(), want.AssistantPrefix.GetForeground()},
		{"SystemMessage", got.SystemMessage.GetForeground(), want.SystemMessage.GetForeground()},
		{"ToolSuccess", got.ToolSuccess.GetForeground(), want.ToolSuccess.GetForeground()},
		{"ToolError", got.ToolError.GetForeground(), want.ToolError.GetForeground()},
		{"ToolRunning", got.ToolRunning.GetForeground(), want.ToolRunning.GetForeground()},
		{"EditorPrompt", got.EditorPrompt.GetForeground(), want.EditorPrompt.GetForeground()},
		{"DialogBorder", got.DialogBorder.GetBorderTopForeground(), want.DialogBorder.GetBorderTopForeground()},
		{"DialogTitle", got.DialogTitle.GetForeground(), want.DialogTitle.GetForeground()},
		{"DialogHelp", got.DialogHelp.GetForeground(), want.DialogHelp.GetForeground()},
		{"DialogInputSurface", got.DialogInputSurface.GetBackground(), want.DialogInputSurface.GetBackground()},
		{"DialogInputBorder", got.DialogInputBorder.GetBorderTopForeground(), want.DialogInputBorder.GetBorderTopForeground()},
		{"DialogInputBorderFocused", got.DialogInputBorderFocused.GetBorderTopForeground(), want.DialogInputBorderFocused.GetBorderTopForeground()},
		{"DialogInputText", got.DialogInputText.GetForeground(), want.DialogInputText.GetForeground()},
		{"DialogInputPlaceholder", got.DialogInputPlaceholder.GetForeground(), want.DialogInputPlaceholder.GetForeground()},
		{"DialogInputCursorForeground", got.DialogInputCursor.GetForeground(), want.DialogInputCursor.GetForeground()},
		{"DialogInputCursorBackground", got.DialogInputCursor.GetBackground(), want.DialogInputCursor.GetBackground()},
		{"Subtle", got.Subtle.GetForeground(), want.Subtle.GetForeground()},
		{"Dim", got.Dim.GetForeground(), want.Dim.GetForeground()},
		{"Error", got.Error.GetForeground(), want.Error.GetForeground()},
		{"Warning", got.Warning.GetForeground(), want.Warning.GetForeground()},
		{"HintBorder", got.HintBorder.GetBorderTopForeground(), want.HintBorder.GetBorderTopForeground()},
		{"Placeholder", got.Placeholder.GetForeground(), want.Placeholder.GetForeground()},
		{"ClawdBody", got.ClawdBody.GetForeground(), want.ClawdBody.GetForeground()},
		{"ClawdFill", got.ClawdFill.GetBackground(), want.ClawdFill.GetBackground()},
		{"SpinnerShimmer", got.SpinnerShimmer.GetForeground(), want.SpinnerShimmer.GetForeground()},
		{"SpinnerStalled", got.SpinnerStalled.GetForeground(), want.SpinnerStalled.GetForeground()},
		{"Element", got.Element.GetBackground(), want.Element.GetBackground()},
		{"DiffAdded", got.DiffAdded.GetBackground(), want.DiffAdded.GetBackground()},
		{"DiffRemoved", got.DiffRemoved.GetBackground(), want.DiffRemoved.GetBackground()},
	}
	for _, check := range checks {
		if check.got != check.want {
			t.Errorf("%s: %s color = %v, want %v", component, check.name, check.got, check.want)
		}
	}
}

func TestApplyThemePropagatesToAllComponents(t *testing.T) {
	t.Setenv("EINO_THEME", "")
	app := New(Config{Resumed: true})

	app.dialog.Show("Bash", `{"command":"go test ./..."}`, "project", make(chan PermissionResponse, 1))
	app.help.Show(app.commandRegistry)

	if _, err := app.threadViews.activate("thread-b", engine.ThreadModeLiveAttach); err != nil {
		t.Fatalf("activate thread-b: %v", err)
	}
	if _, err := app.threadViews.activate(fallbackLeaderThreadID, engine.ThreadModeLiveAttach); err != nil {
		t.Fatalf("reactivate leader: %v", err)
	}
	inactive := app.threadViews.views["thread-b"]
	if inactive == nil || inactive.Chat == nil || inactive.Search == nil {
		t.Fatal("inactive thread view missing chat/search")
	}

	if err := app.applyTheme("light"); err != nil {
		t.Fatalf("applyTheme(light): %v", err)
	}
	want := StylesForTheme(ThemeLight)

	assertSameThemeColors(t, "app", app.styles, want)
	assertSameThemeColors(t, "chat", app.chat.styles, want)
	assertSameThemeColors(t, "permission dialog", app.dialog.styles, want)
	assertSameThemeColors(t, "resume dialog", app.resume.styles, want)
	assertSameThemeColors(t, "help", app.help.styles, want)
	assertSameThemeColors(t, "search", app.search.styles, want)
	assertSameThemeColors(t, "message selector", app.msgSelector.styles, want)
	assertSameThemeColors(t, "MCP approval", app.mcpApproval.styles, want)
	assertSameThemeColors(t, "model picker", app.modelPicker.styles, want)
	assertSameThemeColors(t, "background tasks", app.backgroundTasks.styles, want)
	assertSameThemeColors(t, "MCP settings", app.mcpSettings.styles, want)
	assertSameThemeColors(t, "agent wizard", app.agentWizard.styles, want)
	assertSameThemeColors(t, "teams", app.teamsPanel.styles, want)
	assertSameThemeColors(t, "command palette", app.commandPalette.styles, want)
	assertSameThemeColors(t, "plan dialog", app.planDialog.styles, want)
	assertSameThemeColors(t, "question dialog", app.questionDialog.styles, want)
	assertSameThemeColors(t, "agent picker", app.agentPicker.styles, want)
	assertSameThemeColors(t, "expand search", app.expandSearch.styles, want)
	assertSameThemeColors(t, "permission queue", app.permQueue.prompt.styles, want)
	assertSameThemeColors(t, "inactive thread chat", inactive.Chat.styles, want)
	assertSameThemeColors(t, "inactive thread search", inactive.Search.styles, want)

	if _, err := app.threadViews.activate("thread-later", engine.ThreadModeLiveAttach); err != nil {
		t.Fatalf("activate thread-later: %v", err)
	}
	later := app.threadViews.views["thread-later"]
	assertSameThemeColors(t, "late thread chat", later.Chat.styles, want)
	assertSameThemeColors(t, "late thread search", later.Search.styles, want)
}

func TestChatSetStylesInvalidatesFrozenRenderCache(t *testing.T) {
	chat := NewChatView(StylesForTheme(ThemeDark))
	chat.SetSize(80, 24)
	chat.AppendUser("frozen message content")

	chat.Render(80, 24)
	key := chat.historyCacheKey(chat.items[0])
	frozen := chat.renderCache[key]
	if frozen == nil || !frozen.frozen {
		t.Fatalf("expected frozen render-cache entry, got %+v", frozen)
	}
	chat.Render(80, 24)
	if chat.renderCache[key] != frozen {
		t.Fatal("expected frozen cache hit before theme switch")
	}

	chat.viewDirty = false
	chat.SetStyles(StylesForTheme(ThemeLight))
	if !chat.viewDirty {
		t.Fatal("SetStyles must dirty the viewport cache")
	}
	chat.Render(80, 24)
	rerendered := chat.renderCache[key]
	if rerendered == frozen {
		t.Fatal("frozen cache entry survived theme switch")
	}
	if rerendered.themeGen != chat.themeGen {
		t.Fatalf("cache theme generation = %d, want %d", rerendered.themeGen, chat.themeGen)
	}
}

func TestApplyThemeRestylesRenderedOutput(t *testing.T) {
	t.Setenv("EINO_THEME", "")
	caps := terminalcap.Capabilities{Color: terminalcap.ColorTrueColor}
	app := New(Config{Resumed: true, TerminalCaps: &caps})
	app.width = 80
	app.height = 24
	app.updateLayout()
	app.chat.AppendUser("styled user message")

	chatBefore := app.chat.Render(78, 24)
	app.dialog.Show("Bash", `{"command":"go test ./..."}`, "project", make(chan PermissionResponse, 1))
	dialogBefore := app.dialog.Overlay("", 78, 24)
	app.help.Show(app.commandRegistry)
	helpBefore := strings.Join(app.help.lines, "\n")

	if err := app.applyTheme("light"); err != nil {
		t.Fatalf("applyTheme(light): %v", err)
	}

	chatAfter := app.chat.Render(78, 24)
	dialogAfter := app.dialog.Overlay("", 78, 24)
	helpAfter := strings.Join(app.help.lines, "\n")
	if chatAfter == chatBefore {
		t.Fatal("chat render unchanged after theme switch")
	}
	if dialogAfter == dialogBefore {
		t.Fatal("open permission dialog unchanged after theme switch")
	}
	if helpAfter == helpBefore {
		t.Fatal("open help content was not rebuilt after theme switch")
	}

	darkBar, _ := userMessagePanelRunStyles(StylesForTheme(ThemeDark))
	lightBar, _ := userMessagePanelRunStyles(StylesForTheme(ThemeLight))
	darkBrand := darkBar.Render(userMessageBarGlyph)
	lightBrand := lightBar.Render(userMessageBarGlyph)
	if !strings.Contains(chatBefore, darkBrand) || !strings.Contains(chatAfter, lightBrand) {
		t.Fatal("chat did not render the expected theme brand colors")
	}
	if strings.Contains(chatAfter, darkBrand) {
		t.Fatal("chat still renders the previous theme brand color")
	}
}

func TestThemePrecedenceEnvStartupVsExplicitChoice(t *testing.T) {
	t.Setenv("EINO_THEME", "light")
	caps := terminalcap.Capabilities{Color: terminalcap.ColorTrueColor}

	if got := ResolveThemeForCapabilities("snowy", caps); got != ThemeLight {
		t.Fatalf("startup theme = %q, want env theme %q", got, ThemeLight)
	}
	if got, err := ResolveExplicitTheme("dark"); err != nil || got != ThemeDark {
		t.Fatalf("explicit theme = %q, %v; want %q", got, err, ThemeDark)
	}

	app := New(Config{Resumed: true, TerminalCaps: &caps})
	assertSameThemeColors(t, "startup", app.styles, StylesForTheme(ThemeLight))
	if err := app.applyTheme("dark"); err != nil {
		t.Fatalf("applyTheme(dark): %v", err)
	}
	assertSameThemeColors(t, "explicit dark", app.styles, StylesForTheme(ThemeDark))
	if err := app.applyTheme("snowy"); err != nil {
		t.Fatalf("applyTheme(snowy): %v", err)
	}
	assertSameThemeColors(t, "explicit snowy", app.styles, StylesForTheme(ThemeSnowy))
}

func TestResolveThemeForCapabilitiesIgnoresInvalidStartupValues(t *testing.T) {
	t.Setenv("EINO_THEME", "not-a-theme")
	caps := terminalcap.Capabilities{Color: terminalcap.ColorTrueColor}
	if got := ResolveThemeForCapabilities("light", caps); got != ThemeLight {
		t.Fatalf("invalid env fallback = %q, want config theme %q", got, ThemeLight)
	}
	if got := ResolveThemeForCapabilities("also-invalid", caps); got != ThemeDark {
		t.Fatalf("invalid startup fallback = %q, want auto theme %q", got, ThemeDark)
	}
}

func TestP401StartupCompatibilityAliasesPreservePolarity(t *testing.T) {
	cases := []struct {
		name   string
		env    string
		config string
		color  terminalcap.ColorProfile
		want   ThemeName
	}{
		{
			name:  "environment dark daltonized",
			env:   "dark-daltonized",
			color: terminalcap.ColorTrueColor,
			want:  ThemePolarNight,
		},
		{
			name:   "configuration light daltonized truecolor",
			config: "light-daltonized",
			color:  terminalcap.ColorTrueColor,
			want:   ThemeDaybreak,
		},
		{
			name:   "configuration light daltonized ansi16",
			config: "light-daltonized",
			color:  terminalcap.ColorANSI16,
			want:   ThemeDaybreak,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("EINO_THEME", test.env)
			caps := terminalcap.Capabilities{Color: test.color}
			resolution := resolveStartupThemeForCapabilities(test.config, caps)
			if resolution.theme != test.want {
				t.Fatalf("startup theme = %q, want %q", resolution.theme, test.want)
			}
			if len(resolution.diagnostics) != 0 {
				t.Fatalf("compatibility alias produced diagnostics: %#v", resolution.diagnostics)
			}
		})
	}
}

func TestP401StartupDiagnosticsPreservePrecedenceWithoutPrefixInference(t *testing.T) {
	caps := terminalcap.Capabilities{Color: terminalcap.ColorTrueColor}

	t.Setenv("EINO_THEME", "not-a-theme")
	resolution := resolveStartupThemeForCapabilities("light-daltonized", caps)
	if resolution.theme != ThemeDaybreak || resolution.source != startupThemeSourceConfig {
		t.Fatalf("invalid env fallback = %#v, want config daybreak", resolution)
	}
	if len(resolution.diagnostics) != 1 ||
		resolution.diagnostics[0].source != startupThemeSourceEnvironment {
		t.Fatalf("invalid env diagnostics = %#v", resolution.diagnostics)
	}

	t.Setenv("EINO_THEME", "")
	resolution = resolveStartupThemeForCapabilities("light-made-up", caps)
	if resolution.theme != ThemePolarNight ||
		resolution.source != startupThemeSourceTerminal {
		t.Fatalf("prefix-like unknown theme = %#v, want terminal fallback", resolution)
	}
	if len(resolution.diagnostics) != 1 ||
		resolution.diagnostics[0].source != startupThemeSourceConfig {
		t.Fatalf("unknown config diagnostics = %#v", resolution.diagnostics)
	}
}

func TestP401CompatibilityAliasesRemainStartupOnly(t *testing.T) {
	for _, input := range []string{"dark-daltonized", "light-daltonized"} {
		if _, err := ResolveExplicitTheme(input); err == nil {
			t.Fatalf("ResolveExplicitTheme(%q) accepted a startup-only alias", input)
		}
	}
}

func TestP401StartupDiagnosticBoundsUntrustedValue(t *testing.T) {
	for _, value := range []string{
		strings.Repeat("a", maxStartupThemeDiagnosticRunes) + "private-tail",
		strings.Repeat("界", maxStartupThemeDiagnosticRunes) + "private-tail",
	} {
		bounded := boundedStartupThemeDiagnosticValue(value)
		if got := utf8.RuneCountInString(bounded); got != maxStartupThemeDiagnosticRunes {
			t.Fatalf("bounded startup diagnostic rune count = %d, want %d", got, maxStartupThemeDiagnosticRunes)
		}
		if strings.Contains(bounded, "private-tail") || !strings.HasSuffix(bounded, "…") {
			t.Fatalf("bounded startup diagnostic = %q", bounded)
		}
		message := (startupThemeDiagnostic{
			source: startupThemeSourceConfig,
			value:  value,
		}).message(ThemePolarNight)
		if strings.Contains(message, "private-tail") {
			t.Fatalf("startup diagnostic exposed truncated tail: %q", message)
		}
	}
}

func TestP401AppInitDeliversSafeStartupThemeDiagnosticsOnce(t *testing.T) {
	t.Setenv("EINO_THEME", "bad\x1b[31m-theme")
	caps := terminalcap.Capabilities{Color: terminalcap.ColorTrueColor}
	base := time.Unix(40_000, 0)
	clock := &notificationTestClock{now: base}
	app := New(Config{
		Theme:             "also-invalid",
		Resumed:           true,
		ReducedMotion:     true,
		TerminalCaps:      &caps,
		NotificationNow:   clock.time,
		NotificationAfter: clock.after,
	})
	if app.styles.theme != ThemePolarNight {
		t.Fatalf("fallback theme = %q, want %q", app.styles.theme, ThemePolarNight)
	}

	initCmd := app.Init()
	if initCmd == nil {
		t.Fatal("startup diagnostics did not schedule App.Update delivery")
	}
	message := initCmd()
	if batch, ok := message.(tea.BatchMsg); ok {
		if len(batch) != 1 || batch[0] == nil {
			t.Fatalf("startup diagnostics batch = %#v", batch)
		}
		message = batch[0]()
	}
	if _, ok := message.(startupThemeDiagnosticsMsg); !ok {
		t.Fatalf("startup diagnostic message = %T", message)
	}
	app.Update(message)

	active := app.notifications.Active()
	if len(active) != 2 {
		t.Fatalf("startup notifications = %#v", active)
	}
	joined := active[0].Message + "\n" + active[1].Message
	for _, want := range []string{"EINO_THEME", "config theme", "polar-night"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("startup diagnostics missing %q: %q", want, joined)
		}
	}
	if strings.Contains(joined, "\x1b") {
		t.Fatalf("startup diagnostics contain raw escape bytes: %q", joined)
	}
	if !strings.Contains(joined, `\x1b`) {
		t.Fatalf("startup diagnostics did not quote escape bytes: %q", joined)
	}

	if second := app.Init(); second != nil {
		if repeated, ok := second().(tea.BatchMsg); ok && len(repeated) > 0 {
			t.Fatalf("startup diagnostics scheduled more than once: %#v", repeated)
		}
	}
}

func TestApplyThemeRejectsUnsupportedNameWithoutMutation(t *testing.T) {
	t.Setenv("EINO_THEME", "")
	app := New(Config{Resumed: true})
	if err := app.applyTheme("light"); err != nil {
		t.Fatalf("applyTheme(light): %v", err)
	}
	before := app.styles

	if err := app.applyTheme("not-a-theme"); err == nil {
		t.Fatal("unsupported theme was accepted")
	}
	assertSameThemeColors(t, "rejected theme", app.styles, before)
}
