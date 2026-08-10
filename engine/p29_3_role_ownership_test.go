package engine

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestP293ExcludedModelOwnersRemainOutsideRoleRouting(t *testing.T) {
	engineDir := p293SourceEngineDir(t)
	excluded := []string{
		filepath.Join(engineDir, "compact"),
		filepath.Join(engineDir, "permission"),
		filepath.Join(engineDir, "services"),
		filepath.Join(engineDir, "tasks"),
		filepath.Join(engineDir, "provider", "reviewer.go"),
		filepath.Join(engineDir, "..", "tools", "webfetch.go"),
	}
	forbidden := []string{
		"RoleResolutionInput",
		"ResolveRoleCall",
		"RoleSummary",
		"ToolUseSummaryModel",
		"toolUseSummaryCall",
		"modelCallIdentity",
	}
	for _, root := range excluded {
		assertP293SourceTokensAbsent(t, root, forbidden)
	}
}

func TestP293RoleRoutingAddsNoAdaptiveHealthOwner(t *testing.T) {
	engineDir := p293SourceEngineDir(t)
	roleFiles := []string{
		filepath.Join(engineDir, "model_role.go"),
		filepath.Join(engineDir, "subagent_model_role.go"),
		filepath.Join(engineDir, "provider", "role_resolver.go"),
		filepath.Join(engineDir, "model", "reasoning_effort.go"),
	}
	forbidden := []string{
		"adaptiveHealth",
		"routeHealth",
	}
	for _, path := range roleFiles {
		assertP293SourceTokensAbsent(t, path, forbidden)
	}
	assertP293SourceTokensAbsent(
		t,
		engineDir,
		[]string{"SubagentModelFor"},
	)
}

func p293SourceEngineDir(t *testing.T) string {
	t.Helper()
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate P29.3 source gate")
	}
	return filepath.Dir(testFile)
}

func assertP293SourceTokensAbsent(
	t *testing.T,
	root string,
	forbidden []string,
) {
	t.Helper()
	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("inspect %s: %v", root, err)
	}
	check := func(path string) error {
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		source, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, token := range forbidden {
			if strings.Contains(string(source), token) {
				t.Errorf(
					"excluded P29.3 source %s contains %q",
					path,
					token,
				)
			}
		}
		return nil
	}
	if !info.IsDir() {
		if err := check(root); err != nil {
			t.Fatalf("scan %s: %v", root, err)
		}
		return
	}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		return check(path)
	})
	if err != nil {
		t.Fatalf("scan %s: %v", root, err)
	}
}
