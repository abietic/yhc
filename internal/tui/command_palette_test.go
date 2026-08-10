package tui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine/commands"
)

func testRegistry() *commands.Registry {
	r := commands.NewRegistry()
	_ = r.Register(&commands.Command{
		Name:        "help",
		Description: "Show help",
		Execute: func(context.Context, *commands.CommandContext) (*commands.CommandResult, error) {
			return &commands.CommandResult{Output: "help"}, nil
		},
	})
	_ = r.Register(&commands.Command{
		Name:        "clear",
		Aliases:     []string{"reset"},
		Description: "Clear conversation",
		Execute: func(context.Context, *commands.CommandContext) (*commands.CommandResult, error) {
			return &commands.CommandResult{Output: "cleared"}, nil
		},
	})
	_ = r.Register(&commands.Command{
		Name:        "compact",
		Description: "Compact history",
		Execute: func(context.Context, *commands.CommandContext) (*commands.CommandResult, error) {
			return &commands.CommandResult{Output: "compacted"}, nil
		},
	})
	_ = r.Register(&commands.Command{
		Name:        "model",
		Description: "Change model",
		Execute: func(context.Context, *commands.CommandContext) (*commands.CommandResult, error) {
			return &commands.CommandResult{Output: "model"}, nil
		},
	})
	_ = r.Register(&commands.Command{
		Name:        "resume",
		Description: "Resume a session",
		Execute: func(context.Context, *commands.CommandContext) (*commands.CommandResult, error) {
			return &commands.CommandResult{Output: "resume"}, nil
		},
	})
	return r
}

func TestCommandPaletteShow(t *testing.T) {
	styles := defaultStyles()
	p := NewCommandPalette(styles)

	if p.Visible() {
		t.Fatal("palette should not be visible before Show()")
	}

	reg := testRegistry()
	p.Show(reg)

	if !p.Visible() {
		t.Fatal("palette should be visible after Show()")
	}

	// Empty discovery shows primary commands only.
	if len(p.filtered) != 2 {
		t.Fatalf("expected 2 primary items with empty query, got %d", len(p.filtered))
	}

	// Cursor should be at 0
	if p.cursor != 0 {
		t.Fatalf("expected cursor at 0, got %d", p.cursor)
	}
}

type tuiEffortCapabilityStub struct {
	supported bool
}

func (s tuiEffortCapabilityStub) ReasoningEffortCapability(context.Context) (bool, string, error) {
	if s.supported {
		return true, "", nil
	}
	return false, "selected model has no compatible effort control", nil
}

func (tuiEffortCapabilityStub) ReasoningEffort() string { return "default" }

func TestP165aCommandPaletteUsesRuntimeEffortCapability(t *testing.T) {
	registry := commands.NewRegistry()
	commands.RegisterDefaults(registry)
	palette := NewCommandPalette(defaultStyles())
	containsEffort := func() bool {
		for _, item := range palette.filtered {
			if item.command.Name == "effort" {
				return true
			}
		}
		return false
	}

	palette.ShowFor(registry, &commands.CommandContext{
		Engine: tuiEffortCapabilityStub{supported: false},
	})
	if containsEffort() {
		t.Fatal("palette exposed /effort for an incompatible provider/model")
	}

	palette.ShowFor(registry, &commands.CommandContext{
		Engine: tuiEffortCapabilityStub{supported: true},
	})
	palette.HandleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune("effort"))})
	if !containsEffort() {
		t.Fatal("palette hid /effort for a compatible provider/model")
	}
}

func TestCommandPaletteFilterExactPrefix(t *testing.T) {
	styles := defaultStyles()
	p := NewCommandPalette(styles)
	p.Show(testRegistry())

	// Type "cl" - should match "clear" and "compact" (both have "c" prefix)
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune("cl"))})

	// Should filter down to "clear" (prefix match) only
	found := false
	for _, item := range p.filtered {
		if item.command.Name == "clear" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected 'clear' in filtered results for query 'cl'")
	}
}

func TestCommandPaletteFilterNoMatches(t *testing.T) {
	styles := defaultStyles()
	p := NewCommandPalette(styles)
	p.Show(testRegistry())

	// Type something that matches nothing
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune("zzz"))})

	if len(p.filtered) != 0 {
		t.Fatalf("expected 0 filtered items for 'zzz', got %d", len(p.filtered))
	}
}

func TestCommandPaletteNavigate(t *testing.T) {
	styles := defaultStyles()
	p := NewCommandPalette(styles)
	p.Show(testRegistry())

	// Initially at 0
	if p.cursor != 0 {
		t.Fatalf("expected cursor at 0, got %d", p.cursor)
	}

	// Move down
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if p.cursor != 1 {
		t.Fatalf("expected cursor at 1 after down, got %d", p.cursor)
	}

	// Move up wraps to end
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if p.cursor != 0 {
		t.Fatalf("expected cursor at 0 after up from 1, got %d", p.cursor)
	}

	// Move up from 0 wraps to end
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyUp})
	expected := len(p.filtered) - 1
	if p.cursor != expected {
		t.Fatalf("expected cursor at %d after wrap, got %d", expected, p.cursor)
	}
}

func TestCommandPaletteSelectCommand(t *testing.T) {
	styles := defaultStyles()
	p := NewCommandPalette(styles)
	p.Show(testRegistry())

	// Select first item with Enter
	selected, dismissed := p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !dismissed {
		t.Fatal("expected dismissed after Enter")
	}
	if selected == "" {
		t.Fatal("expected non-empty selected command")
	}
	if p.Visible() {
		t.Fatal("palette should be hidden after selection")
	}
}

func TestCommandPaletteEscDismiss(t *testing.T) {
	styles := defaultStyles()
	p := NewCommandPalette(styles)
	p.Show(testRegistry())

	selected, dismissed := p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if !dismissed {
		t.Fatal("expected dismissed after Esc")
	}
	if selected != "" {
		t.Fatalf("expected empty selection on Esc, got %q", selected)
	}
	if p.Visible() {
		t.Fatal("palette should be hidden after Esc")
	}
}

func TestCommandPaletteBackspace(t *testing.T) {
	styles := defaultStyles()
	p := NewCommandPalette(styles)
	p.Show(testRegistry())

	// Type "hel"
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune("hel"))})
	if p.query != "hel" {
		t.Fatalf("expected query 'hel', got %q", p.query)
	}

	// Backspace
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if p.query != "he" {
		t.Fatalf("expected query 'he' after backspace, got %q", p.query)
	}
}

func TestCommandPaletteFuzzyMatchAlias(t *testing.T) {
	styles := defaultStyles()
	p := NewCommandPalette(styles)
	p.Show(testRegistry())

	// Type "reset" — should match "clear" via its alias
	p.HandleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune("reset"))})

	found := false
	for _, item := range p.filtered {
		if item.command.Name == "clear" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected 'clear' in filtered results for query 'reset' (via alias)")
	}
}

func TestCommandPaletteOverlayRender(t *testing.T) {
	styles := defaultStyles()
	p := NewCommandPalette(styles)
	p.Show(testRegistry())

	// Just verify it doesn't panic and returns non-empty output
	base := "base view content\nsecond line"
	result := p.Overlay(base, 80, 24)
	if result == "" {
		t.Fatal("expected non-empty overlay result")
	}
}

func TestFuzzyScore(t *testing.T) {
	tests := []struct {
		target string
		query  string
		want   bool // true if score > 0 expected
	}{
		{"help", "help", true},    // exact
		{"help", "hel", true},     // prefix
		{"help", "lp", true},      // contains
		{"compact", "cpt", true},  // fuzzy sequence
		{"model", "xyz", false},   // no match
		{"clear", "", true},       // empty query always matches
		{"history", "hst", true},  // fuzzy
		{"history", "xyz", false}, // no match
	}

	for _, tt := range tests {
		score := fuzzyScore(tt.target, tt.query)
		if tt.want && score == 0 {
			t.Errorf("fuzzyScore(%q, %q) = 0, want > 0", tt.target, tt.query)
		}
		if !tt.want && score > 0 {
			t.Errorf("fuzzyScore(%q, %q) = %d, want 0", tt.target, tt.query, score)
		}
	}
}
