package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/commands"
)

func TestQueryEngineLoadsProjectSkillsForCommands(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, ".claude", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
		return
	}
	content := "---\nname: local-check\ndescription: Check this project\n---\nRun the local check.\n"
	if err := os.WriteFile(filepath.Join(skillsDir, "local-check.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	eng := NewQueryEngine(QueryEngineConfig{CWD: dir})
	t.Cleanup(eng.Close)
	if _, ok := eng.GetSkillRegistry().Get("local-check"); !ok {
		t.Fatal("project skill was not loaded into the engine registry")
	}

	result, err := eng.ensureCommandRegistry().Dispatch(
		context.Background(),
		commands.EntrypointPlain,
		&commands.CommandContext{CWD: dir, Engine: eng},
		"/skills",
	)
	if err != nil {
		t.Fatal(err)
		return
	}
	if result == nil || !strings.Contains(result.Output, "local-check") {
		t.Fatalf("/skills output does not include project skill: %#v", result)
		return
	}
}
