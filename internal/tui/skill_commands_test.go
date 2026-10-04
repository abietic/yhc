package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/commands"
)

func TestSkillCommandLoadedIntoTUIAndPaletteStagesArguments(t *testing.T) {
	root := t.TempDir()
	mustWriteTUIPluginFile(t, filepath.Join(root, ".agents", "skills", "check", "SKILL.md"), "---\nname: local-check\ndescription: Check package\nargument-hint: <package>\nargs:\n  - name: package\n    required: true\n---\nReview {{package}}.")
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{CWD: root, TranscriptDir: t.TempDir(), CommandEntrypoint: commands.EntrypointTUI, PluginDirs: []string{t.TempDir()}})
	t.Cleanup(eng.Close)
	app := prepareViewSizedApp(New(Config{Engine: eng, Resumed: true}))
	app.textarea.SetValue("/local-ch")
	app.syncInputModeFromText()
	if len(app.commandHints) != 1 || app.commandHints[0].Name != "skill:local-check" {
		t.Fatalf("skill hints = %#v", app.commandHints)
	}
	app.commandPalette.ShowFor(app.commandRegistry, app.commandCapabilityContext())
	app.commandPalette.query = "local-check"
	app.commandPalette.applyFilter()
	app.pushDialog(StateCommandPalette)
	_, cmd := app.handleActiveDialogKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || app.running || app.textarea.Value() != "/skill:local-check " || len(app.chat.items) != 0 {
		t.Fatalf("palette executed skill: draft=%q running=%v cmd=%v", app.textarea.Value(), app.running, cmd != nil)
	}
	if !strings.Contains(stripANSIForTest(app.renderEditor()), "<package>") {
		t.Fatal("missing argument ghost")
	}
	result, err := app.commandRegistry.Dispatch(context.Background(), commands.EntrypointTUI, app.commandCapabilityContext(), `/local-check "engine/commands"`)
	if err != nil || result.Action != commands.ActionPrompt || !strings.Contains(result.Output, "Review engine/commands.") {
		t.Fatalf("skill dispatch = %#v %v", result, err)
	}
}
