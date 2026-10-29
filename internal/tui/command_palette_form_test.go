package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine/commands"
)

func TestCommandPaletteFormEnterEditsBeforeExecution(t *testing.T) {
	r := commands.NewRegistry()
	called := false
	if err := r.Register(&commands.Command{
		Name: "form-target", Description: "test parameters", Usage: "/form-target <value>",
		Args: []commands.ArgDef{{Name: "value", Type: "string", Required: true}},
		Execute: func(context.Context, *commands.CommandContext) (*commands.CommandResult, error) {
			called = true
			return &commands.CommandResult{}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	p := NewCommandPalette(defaultStyles())
	p.Show(r)
	p.query = "form-target"
	p.applyFilter()
	selected, dismissed := p.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if selected != "" || dismissed || !p.Visible() || called {
		t.Fatal("parameterized selection must open an editor without dispatching or closing")
	}
}

func openPaletteForm(t *testing.T, app *App, name string) {
	t.Helper()
	app.openCommandPalette()
	app.commandPalette.query = name
	app.commandPalette.applyFilter()
	app.handleActiveDialogKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if app.commandPalette.form == nil {
		t.Fatalf("/%s did not open a form", name)
	}
}

func TestCommandPaletteFormDraftPasteCancelAndDispatch(t *testing.T) {
	app := New(Config{})
	var got []string
	calls := 0
	if err := app.commandRegistry.Register(&commands.Command{Name: "form-execute", Args: []commands.ArgDef{{Name: "target", Required: true}, {Name: "count", Type: "int", Default: "2"}}, ExecutionOwner: commands.ExecutionOwnerEntrypoint, Execute: func(_ context.Context, ctx *commands.CommandContext) (*commands.CommandResult, error) {
		calls++
		got = append([]string(nil), ctx.Args...)
		return &commands.CommandResult{Output: "done"}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	app.textarea.SetValue("original draft")
	openPaletteForm(t, app, "form-execute")
	app.Update(tea.PasteMsg{Content: "中文 path 'quoted' \\end"})
	if app.textarea.Value() != "original draft" || calls != 0 {
		t.Fatal("form paste changed draft or executed")
	}
	app.handleActiveDialogKey(tea.KeyPressMsg{Code: tea.KeyF1})
	if !app.commandPalette.details {
		t.Fatal("missing details view")
	}
	app.handleActiveDialogKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if app.commandPalette.details || app.commandPalette.form == nil {
		t.Fatal("details Esc discarded form")
	}
	app.handleActiveDialogKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	app.handleActiveDialogKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if app.textarea.Value() != "original draft" || calls != 0 || len(app.commandPalette.recent) != 0 {
		t.Fatal("cancel altered draft or recent")
	}
	openPaletteForm(t, app, "form-execute")
	app.handleActiveDialogKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if calls != 0 || app.commandPalette.feedback == "" {
		t.Fatal("missing required value executed")
	}
	app.Update(tea.PasteMsg{Content: "bad\ninput"})
	if app.commandPalette.form.inputs[0].Value() != "" {
		t.Fatal("multiline paste accepted")
	}
	app.Update(tea.PasteMsg{Content: "中文 path 'quoted' \\end"})
	app.handleActiveDialogKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if calls != 1 || len(got) != 2 || got[0] != "中文 path 'quoted' \\end" || got[1] != "2" {
		t.Fatalf("dispatch calls=%d args=%#v", calls, got)
	}
	if len(app.commandPalette.recent) != 1 || app.commandPalette.recent[0] != "form-execute" {
		t.Fatal("successful result did not record recent")
	}
}

func TestCommandPaletteFormStaleTargetAndPhase(t *testing.T) {
	for _, change := range []string{"draft", "thread", "engine", "phase", "definition"} {
		t.Run(change, func(t *testing.T) {
			app := newG27EngineApp(t)
			called := false
			if err := app.commandRegistry.Register(&commands.Command{Name: "fenced", PhaseScope: commands.PhaseScopeIdleOnly, Args: []commands.ArgDef{{Name: "value", Default: "x"}}, Execute: func(context.Context, *commands.CommandContext) (*commands.CommandResult, error) {
				called = true
				return nil, nil
			}}); err != nil {
				t.Fatal(err)
			}
			app.textarea.SetValue("preserved")
			openPaletteForm(t, app, "fenced")
			switch change {
			case "draft":
				app.textarea.SetValue("new draft")
				app.markComposerChanged()
			case "thread":
				app.commandPaletteTarget.thread = "old-thread"
			case "engine":
				app.SetEngine(nil)
			case "phase":
				app.running = true
			case "definition":
				if err := app.commandRegistry.ReplacePluginCommands(nil); err != nil {
					t.Fatal(err)
				}
			}
			app.handleActiveDialogKey(tea.KeyPressMsg{Code: tea.KeyEnter})
			want := "preserved"
			if change == "draft" {
				want = "new draft"
			}
			if called || app.textarea.Value() != want || len(app.commandPalette.recent) != 0 {
				t.Fatalf("stale %s changed draft or dispatched", change)
			}
			if change == "phase" && app.commandPalette.feedback == "" {
				t.Fatal("phase loss has no reason")
			}
		})
	}
}

func TestCommandPaletteFormBranchesAndAvailability(t *testing.T) {
	app := New(Config{})
	openPaletteForm(t, app, "permissions")
	p := app.commandPalette
	p.form.inputs[0].SetValue("mode")
	p.refreshForm()
	if len(p.form.inputs) < 2 {
		t.Fatal("mode branch fields missing")
	}
	p.form.inputs[1].SetValue("plan")
	p.form.inputs[0].SetValue("rules")
	p.refreshForm()
	if len(p.form.inputs) > 1 && p.form.inputs[1].Value() == "plan" {
		t.Fatal("branch retained an unrelated value")
	}
	p.Close()
	app.popDialog(StateCommandPalette)
	app.running = true
	app.openCommandPalette()
	p.query = "model"
	p.applyFilter()
	if len(p.filtered) == 0 || p.filtered[0].command.Availability == commands.AvailabilitySupported {
		t.Fatal("busy search should explain unavailable commands")
	}
	app.handleActiveDialogKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !p.Visible() || p.feedback == "" || p.form != nil {
		t.Fatal("unavailable selection was admitted")
	}
}

func TestCommandPaletteFormResponsiveGolden(t *testing.T) {
	var snapshots []string
	for _, size := range [][2]int{{40, 12}, {40, 24}, {80, 24}, {120, 24}, {180, 24}} {
		width, height := size[0], size[1]
		app := New(Config{})
		if err := app.commandRegistry.Register(&commands.Command{Name: "form-golden", Source: "skill:project", SourcePath: ".agents/skills/review/SKILL.md", Trust: commands.CommandTrustConfigured, Kind: commands.CommandKindPromptWorkflow, Description: "Review a package", Args: []commands.ArgDef{{Name: "package", Required: true}, {Name: "mode", Default: "safe"}}, Completion: []commands.ArgumentCompletion{{}, {Choices: []string{"safe", "fast"}}}, Execute: func(context.Context, *commands.CommandContext) (*commands.CommandResult, error) { return nil, nil }}); err != nil {
			t.Fatal(err)
		}
		openPaletteForm(t, app, "form-golden")
		app.Update(tea.PasteMsg{Content: "engine/commands"})
		for _, details := range []bool{false, true} {
			app.commandPalette.details = details
			view := stripANSIForTest(app.commandPalette.Overlay("", width, height))
			for _, line := range strings.Split(view, "\n") {
				if app.renderEnvironment.profile.measure(line, 0) > width {
					t.Fatalf("form exceeded %d cells", width)
				}
			}
			if len(strings.Split(view, "\n")) > height {
				t.Fatal("form exceeded terminal height")
			}
			if !strings.Contains(view, "Esc") || !strings.Contains(view, "╰") {
				t.Fatalf("controls or bottom border clipped: %s", view)
			}
			if !details && (!strings.Contains(view, "Source:") || !strings.Contains(view, "Effect:") || !strings.Contains(view, "Model:") || !strings.Contains(view, "Enter")) {
				t.Fatalf("missing metadata or controls: %s", view)
			}
			rows := strings.Split(view, "\n")
			for i := range rows {
				rows[i] = strings.TrimRight(rows[i], " ")
			}
			snapshots = append(snapshots, fmt.Sprintf("== %dx%d details=%v ==\n%s", width, height, details, strings.Trim(strings.Join(rows, "\n"), "\n")))
		}
	}
	got := strings.Join(snapshots, "\n") + "\n"
	path := filepath.Join("testdata", "command_palette_form.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("command form golden differs:\n%s", got)
	}
}
