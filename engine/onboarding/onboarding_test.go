package onboarding

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/config"
)

func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	oldHome, hadHome := os.LookupEnv("HOME")
	oldAPIKey, hadAPIKey := os.LookupEnv("ANTHROPIC_API_KEY")
	if err := os.Setenv("HOME", home); err != nil {
		t.Fatal(err)
		return ""
	}
	if err := os.Unsetenv("ANTHROPIC_API_KEY"); err != nil {
		t.Fatal(err)
		return ""
	}
	t.Cleanup(func() {
		if hadHome {
			_ = os.Setenv("HOME", oldHome)
		} else {
			_ = os.Unsetenv("HOME")
		}
		if hadAPIKey {
			_ = os.Setenv("ANTHROPIC_API_KEY", oldAPIKey)
		} else {
			_ = os.Unsetenv("ANTHROPIC_API_KEY")
		}
	})
	return home
}

func TestCheckOnboardingNeededAndSteps(t *testing.T) {
	home := isolateHome(t)
	state := CheckOnboardingNeeded()
	if !state.IsFirstRun {
		t.Fatal("missing config directory should be first run")
	}
	if state.ConfigDir != filepath.Join(home, ".claude") {
		t.Fatalf("unexpected config dir: %q", state.ConfigDir)
	}
	for _, need := range []string{"config_directory", "settings", "api_key"} {
		if !contains(state.NeedsSetup, need) {
			t.Fatalf("expected setup need %q in %#v", need, state.NeedsSetup)
		}
	}

	steps := GetOnboardingSteps(state)
	if len(steps) != 4 {
		t.Fatalf("expected four onboarding steps, got %#v", steps)
	}
	if steps[0].ID != "api_key" || !steps[0].Required || steps[0].Completed {
		t.Fatalf("unexpected api key step: %#v", steps[0])
	}
	if steps[1].Completed || steps[2].Completed || steps[3].Completed {
		t.Fatalf("optional steps should not be completed without config: %#v", steps)
	}

	if err := SetupConfigDirectory(); err != nil {
		t.Fatalf("SetupConfigDirectory failed: %v", err)
		return
	}
	if err := SetupAPIKey("sk-ant-" + strings.Repeat("a", 24)); err != nil {
		t.Fatalf("SetupAPIKey failed: %v", err)
		return
	}
	if err := SetupDefaultModel("claude-test-model"); err != nil {
		t.Fatalf("SetupDefaultModel failed: %v", err)
		return
	}
	state = CheckOnboardingNeeded()
	if state.IsFirstRun || !state.HasAPIKey || !state.HasConfig || len(state.NeedsSetup) != 0 {
		t.Fatalf("expected completed setup state, got %#v", state)
	}
	steps = GetOnboardingSteps(state)
	if !steps[0].Completed || !steps[1].Completed || !steps[2].Completed || steps[3].Completed {
		t.Fatalf("unexpected completed step state: %#v", steps)
	}
}

func TestAPIKeyValidationStorageAndPrecedence(t *testing.T) {
	isolateHome(t)
	for _, bad := range []string{"", "sk-ant-short", "sk-other-" + strings.Repeat("a", 20)} {
		if err := ValidateAPIKey(bad); err == nil {
			t.Fatalf("expected invalid API key %q to fail", bad)
			return
		}
	}

	key := "sk-ant-" + strings.Repeat("b", 24)
	if err := SetupAPIKey(key); err != nil {
		t.Fatalf("SetupAPIKey failed: %v", err)
		return
	}
	if got := GetAPIKey(); got != key {
		t.Fatalf("GetAPIKey from credentials = %q", got)
	}

	credPath := filepath.Join(config.UserConfigDir(), "credentials.json")
	info, err := os.Stat(credPath)
	if err != nil {
		t.Fatal(err)
		return
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("credentials mode = %o", mode)
	}
	data, err := os.ReadFile(credPath)
	if err != nil {
		t.Fatal(err)
		return
	}
	var creds map[string]string
	if err := json.Unmarshal(data, &creds); err != nil {
		t.Fatal(err)
		return
	}
	if creds["anthropic_api_key"] != key {
		t.Fatalf("unexpected credentials JSON: %#v", creds)
	}

	envKey := "sk-ant-" + strings.Repeat("e", 24)
	if err := os.Setenv("ANTHROPIC_API_KEY", envKey); err != nil {
		t.Fatal(err)
		return
	}
	if got := GetAPIKey(); got != envKey {
		t.Fatalf("env key should take precedence, got %q", got)
	}
}

func TestModelAndPermissionSetupPersistUserConfig(t *testing.T) {
	isolateHome(t)
	if err := SetupDefaultModel(""); err == nil {
		t.Fatal("empty model should fail")
		return
	}
	if err := SetupDefaultModel("claude-custom"); err != nil {
		t.Fatalf("SetupDefaultModel failed: %v", err)
		return
	}
	cfg, err := config.LoadUserConfig()
	if err != nil {
		t.Fatal(err)
		return
	}
	if cfg.Model != "claude-custom" {
		t.Fatalf("model not persisted: %#v", cfg)
	}

	for _, mode := range []string{"", "danger"} {
		if err := SetupPermissionMode(mode); err == nil {
			t.Fatalf("permission mode %q should fail", mode)
			return
		}
	}
	if err := SetupPermissionMode("strict"); err != nil {
		t.Fatalf("SetupPermissionMode failed: %v", err)
		return
	}
	cfg, err = config.LoadUserConfig()
	if err != nil {
		t.Fatal(err)
		return
	}
	if cfg.Model != "claude-custom" || cfg.PermissionMode != "strict" {
		t.Fatalf("permission setup should preserve existing model and set mode: %#v", cfg)
	}
}

func TestCreateClaudeMdTemplate(t *testing.T) {
	project := t.TempDir()
	if err := CreateClaudeMdTemplate(""); err == nil {
		t.Fatal("empty project dir should fail")
		return
	}
	if err := CreateClaudeMdTemplate(project); err != nil {
		t.Fatalf("CreateClaudeMdTemplate failed: %v", err)
		return
	}
	path := filepath.Join(project, "CLAUDE.md")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
		return
	}
	text := string(content)
	for _, section := range []string{"# CLAUDE.md", "## Project Overview", "## Code Style", "go test ./..."} {
		if !strings.Contains(text, section) {
			t.Fatalf("template missing %q:\n%s", section, text)
		}
	}
	if err := CreateClaudeMdTemplate(project); err == nil {
		t.Fatal("existing CLAUDE.md should not be overwritten")
		return
	}
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
