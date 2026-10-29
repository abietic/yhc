package cmd

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	engineconfig "github.com/abietic/yhc/engine/config"
)

func TestConfigSettingsCLIUsesSharedScopesAndReset(t *testing.T) {
	project := prepareInspectionCLIProject(t)
	initial := executeConfigSettingsCLI(t, "settings")
	if initial.Scope != "user" || initial.Revision == "" || string(initial.Fields["max_turns"].StoredValue) != "null" {
		t.Fatalf("initial snapshot = %+v", initial)
	}
	if _, err := os.Stat(engineconfig.UserConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("inspection created a user settings file: %v", err)
	}
	user := executeConfigSettingsCLI(t, "set", "max_turns", "10")
	if user.Scope != "user" || string(user.Fields["max_turns"].EffectiveValue) != "10" || user.Revision == initial.Revision {
		t.Fatalf("user snapshot = %+v", user)
	}
	projectSettings := executeConfigSettingsCLI(t, "set", "max_turns", "0", "--scope", "project")
	if field := projectSettings.Fields["max_turns"]; string(field.EffectiveValue) != "0" || field.Source != "project" {
		t.Fatalf("explicit project zero = %+v", field)
	}
	effective, err := engineconfig.LoadEffectiveConfig(project)
	if err != nil || effective.MaxTurns != 0 {
		t.Fatalf("runtime did not load project zero: config=%+v err=%v", effective, err)
	}
	reset := executeConfigSettingsCLI(t, "reset", "max_turns", "--scope", "project")
	if field := reset.Fields["max_turns"]; string(field.StoredValue) != "null" || string(field.EffectiveValue) != "10" || field.Source != "user" {
		t.Fatalf("reset did not inherit user value = %+v", field)
	}
	effective, err = engineconfig.LoadEffectiveConfig(project)
	if err != nil || effective.MaxTurns != 10 {
		t.Fatalf("runtime did not load inherited user value: config=%+v err=%v", effective, err)
	}
	assertNoInspectionTranscript(t, project)
}

func TestConfigSettingsCLIRejectsInvalidWritesWithoutChangingSettings(t *testing.T) {
	prepareInspectionCLIProject(t)
	updated := executeConfigSettingsCLI(t, "set", "model", `"model with  spaces"`)
	if string(updated.Fields["model"].StoredValue) != `"model with  spaces"` {
		t.Fatalf("JSON string changed: %s", updated.Fields["model"].StoredValue)
	}
	before, err := os.ReadFile(engineconfig.UserConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"set", "model", "not-json"},
		{"set", "max_turns", "-1"},
		{"set", "permission_mode", `"bypassPermissions"`},
		{"set", "api_key", `"must-not-save"`},
		{"reset", "unknown"},
		{"set", "max_turns", "5", "--scope", "other"},
		{"settings", "--scope", "other"},
		{"set", "max_turns"},
		{"reset"},
	} {
		stdout, _, err := executeSessionCLI(t.Context(), t, append([]string{"config"}, args...)...)
		if err == nil || stdout != "" {
			t.Fatalf("invalid config %v: stdout=%q err=%v", args, stdout, err)
		}
		after, err := os.ReadFile(engineconfig.UserConfigPath())
		if err != nil || string(after) != string(before) {
			t.Fatalf("invalid config %v changed settings: err=%v", args, err)
		}
	}
}

func TestConfigSettingsCLIDoesNotExposeUnknownFieldsOrRequireProvider(t *testing.T) {
	prepareInspectionCLIProject(t)
	t.Setenv("PROV", "unsupported-provider")
	if err := os.MkdirAll(engineconfig.UserConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	const secret = "private-configuration-marker"
	if err := os.WriteFile(engineconfig.UserConfigPath(), []byte(`{"private_extension":"`+secret+`","max_turns":9}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := executeSessionCLI(t.Context(), t, "config", "settings")
	if err != nil || stderr != "" || strings.Contains(stdout, secret) {
		t.Fatalf("settings inspection: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	executeConfigSettingsCLI(t, "set", "max_turns", "8")
	contents, err := os.ReadFile(engineconfig.UserConfigPath())
	if err != nil || !strings.Contains(string(contents), secret) {
		t.Fatalf("unrelated configuration was not preserved: err=%v", err)
	}
}

func TestConfigSettingsCLICancellationDoesNotWrite(t *testing.T) {
	prepareInspectionCLIProject(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	stdout, _, err := executeSessionCLI(ctx, t, "config", "set", "max_turns", "8")
	if err == nil || stdout != "" {
		t.Fatalf("canceled settings write: stdout=%q err=%v", stdout, err)
	}
	if _, err := os.Stat(engineconfig.UserConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("canceled settings command wrote configuration: %v", err)
	}
}

func executeConfigSettingsCLI(t *testing.T, args ...string) engineconfig.WorkbenchSettingsSnapshot {
	t.Helper()
	stdout, stderr, err := executeSessionCLI(t.Context(), t, append([]string{"config"}, args...)...)
	if err != nil || stderr != "" {
		t.Fatalf("config %v: stdout=%q stderr=%q err=%v", args, stdout, stderr, err)
	}
	var snapshot engineconfig.WorkbenchSettingsSnapshot
	if err := json.Unmarshal([]byte(stdout), &snapshot); err != nil {
		t.Fatalf("decode config snapshot: %v; output=%q", err, stdout)
	}
	return snapshot
}
