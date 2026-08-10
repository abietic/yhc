package terminalcap

import (
	"strings"
	"testing"

	"github.com/abietic/yhc/internal/identity"
)

func TestProbePlatformDegradationMatrix(t *testing.T) {
	tests := []struct {
		name string
		opts ProbeOptions
		want Capabilities
	}{
		{
			name: "macOS iTerm",
			opts: ProbeOptions{GOOS: "darwin", StdinTTY: true, StdoutTTY: true, MouseRequested: true, Env: map[string]string{
				"TERM": "xterm-256color", "TERM_PROGRAM": "iTerm.app", "COLORTERM": "truecolor",
			}},
			want: Capabilities{Terminal: "iterm2", Interactive: true, EnhancedKeys: true, FocusReporting: true, Color: ColorTrueColor, Hyperlinks: true, Images: ImageITerm, Mouse: true, BracketedPaste: true, SuspendResume: true},
		},
		{
			name: "Linux VTE",
			opts: ProbeOptions{GOOS: "linux", StdinTTY: true, StdoutTTY: true, MouseRequested: true, Env: map[string]string{
				"TERM": "xterm-256color", "VTE_VERSION": "7600",
			}},
			want: Capabilities{Terminal: "vte", Interactive: true, FocusReporting: true, Color: ColorANSI256, Hyperlinks: true, Images: ImageNone, Mouse: true, BracketedPaste: true, SuspendResume: true},
		},
		{
			name: "Windows Terminal",
			opts: ProbeOptions{GOOS: "windows", StdinTTY: true, StdoutTTY: true, MouseRequested: true, Env: map[string]string{
				"TERM": "xterm-256color", "WT_SESSION": "session",
			}},
			want: Capabilities{Terminal: "windows-terminal", Interactive: true, EnhancedKeys: true, FocusReporting: true, Color: ColorTrueColor, Hyperlinks: true, Images: ImageNone, Mouse: true, BracketedPaste: true, SuspendResume: false},
		},
		{
			name: "conservative SSH",
			opts: ProbeOptions{GOOS: "linux", StdinTTY: true, StdoutTTY: true, MouseRequested: true, Env: map[string]string{
				"TERM": "xterm-256color", "SSH_CONNECTION": "client server", "SSH_TTY": "/dev/pts/2",
			}},
			want: Capabilities{Terminal: "xterm-256color", Interactive: true, SSH: true, FocusReporting: true, Color: ColorANSI256, Images: ImageNone, Mouse: true, BracketedPaste: true, SuspendResume: true},
		},
		{
			name: "dumb non interactive",
			opts: ProbeOptions{GOOS: "linux", StdinTTY: false, StdoutTTY: false, MouseRequested: true, Env: map[string]string{
				"TERM": "dumb", "NO_COLOR": "1",
			}},
			want: Capabilities{Terminal: "dumb", Color: ColorNone, Images: ImageNone},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Probe(tt.opts)
			if got.Terminal != tt.want.Terminal || got.Interactive != tt.want.Interactive ||
				got.SSH != tt.want.SSH || got.EnhancedKeys != tt.want.EnhancedKeys ||
				got.FocusReporting != tt.want.FocusReporting || got.Color != tt.want.Color ||
				got.Hyperlinks != tt.want.Hyperlinks || got.Images != tt.want.Images ||
				got.Mouse != tt.want.Mouse || got.BracketedPaste != tt.want.BracketedPaste ||
				got.SuspendResume != tt.want.SuspendResume {
				t.Fatalf("Probe() = %#v, want matching %#v", got, tt.want)
			}
			if got.EnhancedKeysInUse {
				t.Fatal("Bubble Tea v1 compatibility mode must not enable enhanced keys")
			}
		})
	}
}

func TestProbeMouseAndColorOverrides(t *testing.T) {
	caps := Probe(ProbeOptions{
		GOOS: "linux", StdinTTY: true, StdoutTTY: true, MouseRequested: true,
		Env: map[string]string{
			"TERM": "xterm-256color", "COLORTERM": "truecolor",
			"NO_COLOR": "1", "EINO_AGENT_DISABLE_MOUSE": "true",
		},
	})
	if caps.Color != ColorNone || caps.Mouse {
		t.Fatalf("overrides not honored: %#v", caps)
	}
}

func TestProbeMouseEnvironmentCanonicalWinsLegacy(t *testing.T) {
	pair := identity.RuntimeEnvDisableMouse.Pair()
	tests := []struct {
		name      string
		canonical *string
		legacy    *string
		wantMouse bool
	}{
		{name: "canonical only", canonical: environmentString("true")},
		{name: "legacy only", legacy: environmentString("true")},
		{name: "both prefer canonical", canonical: environmentString("false"), legacy: environmentString("true"), wantMouse: true},
		{name: "present empty canonical blocks legacy", canonical: environmentString(""), legacy: environmentString("true"), wantMouse: true},
		{name: "invalid canonical blocks legacy", canonical: environmentString("invalid"), legacy: environmentString("true"), wantMouse: true},
		{name: "neither", wantMouse: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := map[string]string{"TERM": "xterm-256color"}
			if test.canonical != nil {
				env[pair.Canonical] = *test.canonical
			}
			if test.legacy != nil {
				env[pair.Legacy] = *test.legacy
			}
			caps := Probe(ProbeOptions{
				GOOS: "linux", StdinTTY: true, StdoutTTY: true,
				MouseRequested: true,
				Env:            env,
			})
			if caps.Mouse != test.wantMouse {
				t.Fatalf("mouse = %t, want %t", caps.Mouse, test.wantMouse)
			}
		})
	}
}

func environmentString(value string) *string { return &value }

func TestFocusStateFailsClosedUntilBlur(t *testing.T) {
	focus := NewFocusState(true)
	if focus.Status() != FocusUnknown || focus.ExternalNotificationsAllowed() {
		t.Fatal("unknown focus must suppress external notifications")
	}
	focus.SetFocused(false)
	if focus.Status() != FocusBlurred || !focus.ExternalNotificationsAllowed() {
		t.Fatal("observed blur should allow external notifications")
	}
	focus.SetFocused(true)
	if focus.Status() != FocusFocused || focus.ExternalNotificationsAllowed() {
		t.Fatal("focused terminal should suppress external notifications")
	}
	focus.Reset()
	if focus.Status() != FocusUnknown || focus.ExternalNotificationsAllowed() {
		t.Fatal("reset focus should return to fail-closed unknown state")
	}
	unsupported := NewFocusState(false)
	unsupported.SetFocused(false)
	if unsupported.ExternalNotificationsAllowed() {
		t.Fatal("unsupported focus reporting must never allow external notifications")
	}
}

func TestSummaryMakesDegradationExplicit(t *testing.T) {
	caps := Probe(ProbeOptions{
		GOOS: "windows", StdinTTY: true, StdoutTTY: true, MouseRequested: false,
		Env: map[string]string{"WT_SESSION": "session"},
	})
	summary := caps.Summary(FocusUnknown)
	for _, want := range []string{
		"windows (local, interactive)",
		"Enhanced keys:   available; compatibility mode active",
		"Focus reporting: requested; state=unknown",
		"Mouse:           disabled",
		"Suspend/resume:  no",
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q:\n%s", want, summary)
		}
	}
}
