package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine/commands"
)

// ---------------------------------------------------------------------------
// Command Palette parity tests
// ---------------------------------------------------------------------------

func registerPaletteTestCommand(reg *commands.Registry, name, description string) {
	_ = reg.Register(&commands.Command{
		Name:          name,
		Description:   description,
		DiscoveryTier: commands.DiscoveryTierPrimary,
		Execute: func(context.Context, *commands.CommandContext) (*commands.CommandResult, error) {
			return &commands.CommandResult{}, nil
		},
	})
}

func TestCommandPaletteOpenCloseLifecycle(t *testing.T) {
	styles := StylesForTheme(ThemeDark)
	palette := NewCommandPalette(styles)

	if palette.Visible() {
		t.Fatal("palette should not be visible initially")
	}

	reg := commands.NewRegistry()
	registerPaletteTestCommand(reg, "help", "Show help")
	registerPaletteTestCommand(reg, "clear", "Clear history")

	palette.Show(reg)
	if !palette.Visible() {
		t.Fatal("palette should be visible after Show()")
	}

	// Verify all commands loaded
	if len(palette.filtered) != 2 {
		t.Fatalf("expected 2 filtered items, got %d", len(palette.filtered))
	}

	palette.Close()
	if palette.Visible() {
		t.Fatal("palette should not be visible after Close()")
	}
}

func TestCommandPaletteFuzzyFilter(t *testing.T) {
	styles := StylesForTheme(ThemeDark)
	palette := NewCommandPalette(styles)

	reg := commands.NewRegistry()
	registerPaletteTestCommand(reg, "help", "List commands")
	registerPaletteTestCommand(reg, "history", "List sessions")
	registerPaletteTestCommand(reg, "model", "Set model")

	palette.Show(reg)

	// Type "hel" to filter — should match only help
	palette.HandleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune{'h'})})
	palette.HandleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune{'e'})})
	palette.HandleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune{'l'})})
	if len(palette.filtered) != 1 {
		t.Fatalf("expected 1 filtered result for 'hel', got %d", len(palette.filtered))
	}
	if palette.filtered[0].command.Name != "help" {
		t.Fatalf("expected 'help', got %q", palette.filtered[0].command.Name)
	}
}

func TestCommandPaletteExecuteOnEnter(t *testing.T) {
	styles := StylesForTheme(ThemeDark)
	palette := NewCommandPalette(styles)

	reg := commands.NewRegistry()
	registerPaletteTestCommand(reg, "help", "Show help")

	palette.Show(reg)

	selected, dismissed := palette.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !dismissed {
		t.Fatal("expected dismissed on Enter")
	}
	if selected != "help" {
		t.Fatalf("expected 'help' selected, got %q", selected)
	}
	if palette.Visible() {
		t.Fatal("palette should be closed after selection")
	}
}

func TestCommandPaletteEscDismisses(t *testing.T) {
	styles := StylesForTheme(ThemeDark)
	palette := NewCommandPalette(styles)

	reg := commands.NewRegistry()
	registerPaletteTestCommand(reg, "help", "Show help")
	palette.Show(reg)

	selected, dismissed := palette.HandleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !dismissed {
		t.Fatal("expected dismissed on Esc")
	}
	if selected != "" {
		t.Fatalf("expected no selection on Esc, got %q", selected)
	}
}

// ---------------------------------------------------------------------------
// Notification Stack parity tests
// ---------------------------------------------------------------------------

func TestNotificationStackPushAndOverflow(t *testing.T) {
	ns := NewNotificationStack()
	now := time.Unix(1_000, 0)

	ns.PushAt(now, "msg1", NotifyInfo)
	ns.PushAt(now, "msg2", NotifyWarning)
	ns.PushAt(now, "msg3", NotifyError)

	// maxVisible is 3 by default, so all should be visible
	if ns.Count() != 3 {
		t.Fatalf("expected 3 active, got %d", ns.Count())
	}

	// Push a 4th — oldest should be evicted
	ns.PushAt(now, "msg4", NotifySuccess)
	if ns.Count() != 3 {
		t.Fatalf("expected 3 active after overflow, got %d", ns.Count())
	}

	// Verify oldest was evicted (msg1 gone)
	active := ns.Active()
	for _, n := range active {
		if n.Message == "msg1" {
			t.Fatal("msg1 should have been evicted")
		}
	}
}

func TestNotificationStackAutoDismiss(t *testing.T) {
	ns := NewNotificationStack()
	now := time.Unix(1_000, 0)

	ns.PushWithDurationAt(now, "ephemeral", NotifyInfo, time.Millisecond)
	ns.PruneAt(now.Add(time.Millisecond))
	if ns.Count() != 0 {
		t.Fatalf("expected 0 active after auto-dismiss, got %d", ns.Count())
	}
}

func TestNotificationStackSeverityRendering(t *testing.T) {
	ns := NewNotificationStack()
	styles := StylesForTheme(ThemeDark)
	now := time.Unix(1_000, 0)

	ns.PushAt(now, "info msg", NotifyInfo)
	ns.PushAt(now, "success msg", NotifySuccess)
	ns.PushAt(now, "error msg", NotifyError)

	rendered := ns.Render(styles, 80)
	if rendered == "" {
		t.Fatal("expected non-empty render output")
	}
	// Verify all three messages appear in the render
	if !strings.Contains(rendered, "info msg") {
		t.Error("render should contain 'info msg'")
	}
	if !strings.Contains(rendered, "error msg") {
		t.Error("render should contain 'error msg'")
	}
}

// ---------------------------------------------------------------------------
// Error Display parity tests
// ---------------------------------------------------------------------------

func TestErrorDisplayClassification(t *testing.T) {
	tests := []struct {
		input    string
		category ErrorCategory
	}{
		{"rate limit exceeded", CategoryRateLimit},
		{"429 too many requests", CategoryRateLimit},
		{"unauthorized access", CategoryAuth},
		{"invalid api key provided", CategoryAuth},
		{"connection refused to host", CategoryNetwork},
		{"request timeout after 30s", CategoryNetwork},
		{"context length exceeded", CategoryModel},
		{"token limit reached", CategoryModel},
		{"missing config file", CategoryConfig},
		{"some random error", CategoryGeneral},
	}

	for _, tc := range tests {
		entry := ClassifyError(tc.input)
		if entry.Category != tc.category {
			t.Errorf("ClassifyError(%q) = %s, want %s", tc.input, entry.Category, tc.category)
		}
	}
}

func TestErrorDisplayCollapsibility(t *testing.T) {
	styles := StylesForTheme(ThemeDark)
	ed := NewErrorDisplay(styles)

	entry := ErrorEntry{
		Severity: SeverityWarning,
		Category: CategoryTool,
		Title:    "Tool Failed",
		Message:  "something went wrong",
		Details:  "line 1\nline 2\nline 3",
	}
	ed.AddError(entry)

	// Initially collapsed — verify toggle works
	if ed.expandedIdx[0] {
		t.Fatal("expected initially collapsed")
	}
	ed.ToggleDetails(0)
	if !ed.expandedIdx[0] {
		t.Fatal("expected expanded after toggle")
	}
	ed.ToggleDetails(0)
	if ed.expandedIdx[0] {
		t.Fatal("expected collapsed after second toggle")
	}
}

func TestErrorDisplaySeverityString(t *testing.T) {
	cases := []struct {
		sev  ErrorSeverity
		want string
	}{
		{SeverityInfo, "Info"},
		{SeverityWarning, "Warning"},
		{SeverityError, "Error"},
	}
	for _, tc := range cases {
		if got := tc.sev.String(); got != tc.want {
			t.Errorf("Severity(%d).String() = %q, want %q", tc.sev, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Spinner / Streaming Renderer parity tests
// ---------------------------------------------------------------------------

func TestSpinnerBreathCycleUsesFixedIdentityGlyph(t *testing.T) {
	if spinnerGlyph() != assistantIdentityGlyph {
		t.Fatalf("spinner glyph = %q, want identity glyph %q", spinnerGlyph(), assistantIdentityGlyph)
	}
	for _, counter := range []int{0, 8, 16, -8, -16} {
		if got := spinnerBreathIntensity(counter); got != 0 {
			t.Fatalf("spinnerBreathIntensity(%d) = %v, want 0", counter, got)
		}
	}
}

func TestSpinnerModeText(t *testing.T) {
	cases := []struct {
		mode SpinnerMode
		tool string
		want string
	}{
		{SpinnerThinking, "", "Thinking…"},
		{SpinnerResponding, "", "Responding…"},
		{SpinnerToolUse, "Bash", "Bash…"},
		{SpinnerToolUse, "", "Running tool…"},
	}

	for _, tc := range cases {
		state := &SpinnerState{Mode: tc.mode, ToolName: tc.tool, StartTime: time.Now()}
		if got := state.Text(); got != tc.want {
			t.Errorf("SpinnerState{mode=%d, tool=%q}.Text() = %q, want %q",
				tc.mode, tc.tool, got, tc.want)
		}
	}
}

func TestSpinnerShimmerPhaseDelay(t *testing.T) {
	// Reference: thinking mode has 3s delay before shimmer starts
	state := &SpinnerState{
		Mode:      SpinnerThinking,
		StartTime: time.Now(), // just started
	}
	phase := state.ShimmerPhase()
	if phase != 0 {
		t.Fatalf("expected 0 shimmer phase during thinking delay, got %f", phase)
	}
}

func TestSpinnerStallDetection(t *testing.T) {
	state := &SpinnerState{
		Mode:      SpinnerResponding,
		StartTime: time.Now().Add(-40 * time.Second), // started 40s ago
		// LastEventTime zero = uses StartTime
	}
	if !state.IsStalled() {
		t.Fatal("expected stalled after 40s with no events")
	}

	// Record an event — should no longer be stalled
	state.RecordEvent()
	if state.IsStalled() {
		t.Fatal("expected not stalled after RecordEvent()")
	}
}
