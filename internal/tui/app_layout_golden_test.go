package tui

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/commands"
)

func TestAppLayoutCurrentOutputGolden(t *testing.T) {
	t.Parallel()

	app := New(Config{Resumed: true, Model: "test-model"})
	app.width = 72
	app.height = 24
	app.state = StateChat
	app.sessionStart = time.Time{}
	app.statusLineHook = func(_, _ string) (string, string) {
		return "  default · thread:main", "test-model"
	}
	app.chat.AppendUser("Inspect the layout contract.")
	app.chat.AppendOrUpdateAssistant("The chat remains visible while activity, hints, editor, and status share the terminal.")
	app.chat.FinishAssistant()
	app.inputMode = InputCommand
	app.textarea.SetValue("/")
	app.commandHints = []*commands.Command{
		{Name: "agent", Description: "switch agent thread"},
		{Name: "compact", Description: "compact conversation"},
		{Name: "model", Description: "select model"},
	}
	app.commandHintIdx = 1
	app.activeTools = map[string]*inlineToolEntry{
		"tool-1": {name: "Read", description: "internal/tui/app.go", startTime: time.Now()},
		"tool-2": {name: "Grep", description: "layout regions", startTime: time.Now()},
	}
	app.activeToolsOrder = []string{"tool-1", "tool-2"}
	app.updateLayout()

	var got strings.Builder
	got.WriteString("== base ==\n")
	got.WriteString(normalizeAppLayoutGolden(app.renderView()))
	got.WriteString("\n== permission ==\n")
	app.dialog.Show("Bash", `{"command":"go test ./..."}`, "project", make(chan PermissionResponse, 1))
	app.state = StatePermission
	got.WriteString(normalizeAppLayoutGolden(app.renderView()))

	want, err := os.ReadFile("testdata/app_layout.golden")
	if err != nil {
		t.Fatalf("read app layout golden: %v", err)
	}
	if strings.TrimSpace(got.String()) != strings.TrimSpace(string(want)) {
		t.Fatalf("app layout golden mismatch:\n--- got ---\n%s\n--- want ---\n%s", got.String(), want)
	}
}

func normalizeAppLayoutGolden(rendered string) string {
	lines := strings.Split(stripANSIForTest(rendered), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}
