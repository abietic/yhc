package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/internal/tui/terminalcap"
)

func interactiveTerminalCaps() terminalcap.Capabilities {
	return terminalcap.Capabilities{
		Platform:       "linux",
		Terminal:       "wezterm",
		Interactive:    true,
		EnhancedKeys:   true,
		FocusReporting: true,
		Color:          terminalcap.ColorTrueColor,
		Hyperlinks:     true,
		Images:         terminalcap.ImageKitty,
		Mouse:          true,
		BracketedPaste: true,
		SuspendResume:  true,
	}
}

func TestFocusLifecycleControlsExternalNotifications(t *testing.T) {
	caps := interactiveTerminalCaps()
	focus := terminalcap.NewFocusState(true)
	app := New(Config{Resumed: true, TerminalCaps: &caps, FocusState: focus})

	if focus.ExternalNotificationsAllowed() {
		t.Fatal("unknown startup focus should suppress external notifications")
	}
	app.Update(tea.BlurMsg{})
	if !focus.ExternalNotificationsAllowed() {
		t.Fatal("blur event should allow external notifications")
	}
	app.Update(tea.FocusMsg{})
	if focus.ExternalNotificationsAllowed() {
		t.Fatal("focus event should suppress external notifications")
	}
	_, cmd := app.Update(tea.ResumeMsg{})
	if cmd == nil || focus.Status() != terminalcap.FocusUnknown {
		t.Fatalf("resume did not reset focus/reinitialize terminal: status=%s cmd=%v", focus.Status(), cmd)
	}
}

func TestTerminalCommandReportsEffectiveCapabilities(t *testing.T) {
	caps := interactiveTerminalCaps()
	focus := terminalcap.NewFocusState(true)
	focus.SetFocused(false)
	app := New(Config{Resumed: true, TerminalCaps: &caps, FocusState: focus})
	if cmd := app.sendSlashCommand("/terminal"); cmd != nil {
		t.Fatal("/terminal should be a synchronous local diagnostic")
	}
	raw := app.chat.RenderAllRaw(100)
	for _, want := range []string{
		"Terminal Capabilities",
		"state=blurred",
		"Inline images:   kitty",
		"Display Cell Profile",
		"Unicode:         17.0.0",
		"Ambiguous width: narrow",
		"Tabs:            4-cell stops; rectangle-origin aware",
		"Terminal/font:   not inferred",
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("/terminal output missing %q:\n%s", want, raw)
		}
	}
}

func TestSuspendCommandAndResumePreserveModelState(t *testing.T) {
	caps := interactiveTerminalCaps()
	app := New(Config{Resumed: true, TerminalCaps: &caps})
	app.textarea.SetValue("/suspend")
	cmd := app.sendSlashCommand("/suspend")
	if cmd == nil {
		t.Fatal("/suspend did not return a Bubble Tea suspend command")
	}
	suspendMsg := cmd()
	if _, ok := suspendMsg.(tea.SuspendMsg); !ok {
		t.Fatalf("/suspend command returned %T, want tea.SuspendMsg", suspendMsg)
	}
	beforeState := app.state
	_, resumeCmd := app.Update(tea.ResumeMsg{})
	if resumeCmd == nil || app.state != beforeState {
		t.Fatalf("resume changed model state: before=%v after=%v cmd=%v", beforeState, app.state, resumeCmd)
	}
	app.textarea.SetValue("after resume")
	app.Update(tea.FocusMsg{})
	if app.textarea.Value() != "after resume" {
		t.Fatalf("focus lifecycle mutated composer: %q", app.textarea.Value())
	}
}

func TestSuspendDegradesWhenUnsupported(t *testing.T) {
	caps := interactiveTerminalCaps()
	caps.Platform = "windows"
	caps.SuspendResume = false
	app := New(Config{Resumed: true, TerminalCaps: &caps})
	if cmd := app.sendSlashCommand("/suspend"); cmd != nil {
		t.Fatal("unsupported suspend should not emit a process-control command")
	}
	if raw := app.chat.RenderAllRaw(100); !strings.Contains(raw, "Suspend/resume is unavailable") {
		t.Fatalf("unsupported suspend did not explain degradation:\n%s", raw)
	}
}

func TestCtrlDEOFRequestsCleanQuit(t *testing.T) {
	app := New(Config{Resumed: true})
	_, cmd := app.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if cmd == nil || !app.quitting {
		t.Fatalf("Ctrl+D did not request clean quit: quitting=%v cmd=%v", app.quitting, cmd)
	}
	quitMsg := cmd()
	if _, ok := quitMsg.(tea.QuitMsg); !ok {
		t.Fatalf("Ctrl+D returned %T, want tea.QuitMsg", quitMsg)
	}
}
