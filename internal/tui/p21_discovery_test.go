package tui

import (
	"context"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine/commands"
)

func p21DiscoveryRegistry(t *testing.T) *commands.Registry {
	t.Helper()
	registry := commands.NewRegistry()
	commands.RegisterDefaults(registry)
	for _, name := range []string{"review", "commit"} {
		if err := registry.Register(&commands.Command{
			Name:        name,
			Description: "Run the bundled " + name + " workflow",
			Kind:        commands.CommandKindPromptWorkflow,
			Source:      "bundled:workflows",
			Trust:       commands.CommandTrustBundled,
			Execute: func(context.Context, *commands.CommandContext) (*commands.CommandResult, error) {
				return &commands.CommandResult{}, nil
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	return registry
}

func paletteCommandNames(items []commandPaletteItem) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.command.Name)
	}
	return names
}

func paletteAllCommandNames(items []*commands.Command) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Name)
	}
	return names
}

func TestP21CommandPaletteEmptyQueryUsesRecentThenPrimary(t *testing.T) {
	registry := p21DiscoveryRegistry(t)
	palette := NewCommandPalette(defaultStyles())
	palette.Show(registry)

	wantPrimary := []string{
		"new", "compact", "sessions", "model", "plan", "permissions",
		"status", "diff", "files", "agents", "review", "commit",
	}
	if got := paletteCommandNames(palette.filtered); !reflect.DeepEqual(got, wantPrimary) {
		t.Fatalf("empty palette = %v, want %v", got, wantPrimary)
	}
	for _, item := range palette.filtered {
		if item.section != commandPaletteSectionSuggested {
			t.Fatalf("%s section = %q, want Suggested", item.command.Name, item.section)
		}
	}

	palette.RecordRecent("clear")
	palette.RecordRecent("/status")
	palette.RecordRecent("theme")
	palette.RecordRecent("STATUS")
	if want := []string{"status", "theme", "clear"}; !reflect.DeepEqual(palette.recent, want) {
		t.Fatalf("recent = %v, want %v", palette.recent, want)
	}
	palette.Show(registry)
	got := paletteCommandNames(palette.filtered)
	want := append(
		[]string{"status", "theme", "clear"},
		"new", "compact", "sessions", "model", "plan", "permissions",
		"diff", "files", "agents", "review", "commit",
	)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recent + primary = %v, want %v", got, want)
	}
	if len(got) > 15 {
		t.Fatalf("empty palette has %d entries, want at most 15", len(got))
	}

	other := NewCommandPalette(defaultStyles())
	other.Show(registry)
	if len(other.recent) != 0 {
		t.Fatalf("recent commands leaked across palette instances: %v", other.recent)
	}
}

func TestP21CommandPaletteTypedQuerySearchesSecondaryMetadata(t *testing.T) {
	registry := p21DiscoveryRegistry(t)
	palette := NewCommandPalette(defaultStyles())

	tests := []struct {
		query string
		want  string
	}{
		{query: "theme", want: "theme"},
		{query: "reset", want: "clear"},
		{query: "color", want: "theme"},
	}
	for _, tt := range tests {
		palette.Show(registry)
		palette.query = tt.query
		palette.applyFilter()
		found := false
		for _, item := range palette.filtered {
			if item.command.Name == tt.want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("query %q did not find %q in %v", tt.query, tt.want, paletteCommandNames(palette.filtered))
		}
	}
}

func TestP21CommandPaletteActiveTurnProjectsExactFour(t *testing.T) {
	registry := p21DiscoveryRegistry(t)
	palette := NewCommandPalette(defaultStyles())
	palette.RecordRecent("theme")
	palette.ShowFor(registry, &commands.CommandContext{
		Environment: commands.CommandEnvironment{
			Phase: commands.CommandPhaseActiveTurn,
		},
	})

	want := []string{"agent", "team", "queue", "keybindings"}
	if got := paletteAllCommandNames(palette.all); !reflect.DeepEqual(got, want) {
		t.Fatalf("active command snapshot = %v, want %v", got, want)
	}
	if len(palette.filtered) != 0 {
		t.Fatalf("idle-only recent leaked into active empty palette: %v", paletteCommandNames(palette.filtered))
	}

	palette.query = "agent"
	palette.applyFilter()
	if got := paletteCommandNames(palette.filtered); len(got) == 0 || got[0] != "agent" {
		t.Fatalf("active search = %v, want agent first", got)
	}
}

func TestP21CommandPaletteSectionsAndDynamicOriginAreVisible(t *testing.T) {
	registry := p21DiscoveryRegistry(t)
	if err := registry.Register(&commands.Command{
		Name:        "plugin:inspect",
		Description: "Inspect configured extension state",
		Kind:        commands.CommandKindPromptWorkflow,
		Source:      "plugin:example",
		Trust:       commands.CommandTrustConfigured,
		Execute: func(context.Context, *commands.CommandContext) (*commands.CommandResult, error) {
			return &commands.CommandResult{}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	palette := NewCommandPalette(defaultStyles())
	palette.Show(registry)
	plain := stripANSIForTest(palette.Overlay("", 100, 40))
	if !strings.Contains(plain, commandPaletteSectionSuggested) ||
		strings.Contains(plain, commandPaletteSectionRecent) {
		t.Fatalf("empty section headings are not truthful:\n%s", plain)
	}

	palette.RecordRecent("theme")
	palette.Show(registry)
	plain = stripANSIForTest(palette.Overlay("", 100, 40))
	recentIndex := strings.Index(plain, commandPaletteSectionRecent)
	suggestedIndex := strings.Index(plain, commandPaletteSectionSuggested)
	if recentIndex < 0 || suggestedIndex <= recentIndex {
		t.Fatalf("recent/suggested headings missing or out of order:\n%s", plain)
	}

	palette.query = "plugin:inspect"
	palette.applyFilter()
	if len(palette.filtered) != 1 {
		t.Fatalf("plugin search = %v", paletteCommandNames(palette.filtered))
	}
	line := stripANSIForTest(palette.renderItem(
		palette.environment.normalized().profile,
		palette.filtered[0],
		false,
		70,
	))
	if !strings.Contains(line, "[plugin:example / configured]") {
		t.Fatalf("dynamic command origin is not visible: %q", line)
	}
}

func TestP21HelpOverlayGroupsPrimaryAndSecondaryCommands(t *testing.T) {
	registry := p21DiscoveryRegistry(t)
	help := NewHelpOverlay(defaultStyles(), nil)
	plain := stripANSIForTest(strings.Join(help.buildContent(registry, nil), "\n"))

	last := -1
	for _, category := range commands.CommandCategoriesInDisplayOrder() {
		index := strings.Index(plain, "\n  "+string(category)+"\n")
		if index <= last {
			t.Fatalf("help category %q missing or out of order:\n%s", category, plain)
		}
		last = index
	}
	for _, name := range []string{"/new", "/clear", "/review", "/theme"} {
		if !strings.Contains(plain, name) {
			t.Fatalf("help hid reachable command %q:\n%s", name, plain)
		}
	}
}

func TestP21PaletteSelectionRechecksAdmissionBeforeRecording(t *testing.T) {
	t.Run("stale phase", func(t *testing.T) {
		app := newTestApp(100, 30)
		app.openCommandPalette()
		app.commandPalette.query = "status"
		app.commandPalette.applyFilter()
		if got := paletteCommandNames(app.commandPalette.filtered); len(got) == 0 || got[0] != "status" {
			t.Fatalf("status search = %v, want status first", got)
		}

		app.running = true
		handled, cmd := app.handleActiveDialogKey(tea.KeyPressMsg{Code: tea.KeyEnter})
		if !handled || cmd != nil {
			t.Fatalf("selection handled=%v cmd=%v", handled, cmd)
		}
		if len(app.commandPalette.recent) != 0 {
			t.Fatalf("stale selection was recorded: %v", app.commandPalette.recent)
		}
		plain := stripANSIForTest(app.chat.RenderAllExpanded(100))
		if !strings.Contains(
			plain,
			"/status is unavailable: command is available only while no request is running.",
		) {
			t.Fatalf("stale phase did not produce the precise denial:\n%s", plain)
		}
	})

	t.Run("invalid no-argument selection", func(t *testing.T) {
		app := newTestApp(100, 30)
		if err := app.commandRegistry.Register(&commands.Command{
			Name:          "needs-arg",
			Description:   "Requires an argument",
			Category:      commands.CommandCategoryRuntime,
			DiscoveryTier: commands.DiscoveryTierPrimary,
			DisplayOrder:  5,
			PhaseScope:    commands.PhaseScopeIdleOnly,
			Args: []commands.ArgDef{{
				Name:     "target",
				Required: true,
			}},
			Execute: func(context.Context, *commands.CommandContext) (*commands.CommandResult, error) {
				return &commands.CommandResult{}, nil
			},
		}); err != nil {
			t.Fatal(err)
		}
		app.openCommandPalette()
		app.commandPalette.query = "needs-arg"
		app.commandPalette.applyFilter()
		app.handleActiveDialogKey(tea.KeyPressMsg{Code: tea.KeyEnter})
		if len(app.commandPalette.recent) != 0 {
			t.Fatalf("invalid selection was recorded: %v", app.commandPalette.recent)
		}
	})

	t.Run("dismissed", func(t *testing.T) {
		app := newTestApp(100, 30)
		app.openCommandPalette()
		app.handleActiveDialogKey(tea.KeyPressMsg{Code: tea.KeyEsc})
		if len(app.commandPalette.recent) != 0 {
			t.Fatalf("dismissed palette changed recents: %v", app.commandPalette.recent)
		}
	})
}
