package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestSandboxUserConfigStrictAndProjectSandboxIsDiscardedBeforeDecode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(UserConfigPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(UserConfigPath(), []byte(`{"sandbox":{"guest_profile":"workspace-write"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	projectConfig := filepath.Join(project, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(projectConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	secret := "sandbox-secret-sentinel"
	if err := os.WriteFile(projectConfig, []byte(`{"model":"project","sandbox":{"guest_profile":false,"secret":"`+secret+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	sources, err := LoadConfigSources(project)
	if err != nil {
		t.Fatalf("project sandbox must be discarded before decode: %v", err)
	}
	if sources.Project.Sandbox != nil || sources.Effective.Sandbox == nil || sources.Effective.Sandbox.GuestProfile != "workspace-write" {
		t.Fatalf("sandbox authority = project:%#v effective:%#v", sources.Project.Sandbox, sources.Effective.Sandbox)
	}
	if len(sources.SandboxDiagnostics) != 1 || sources.SandboxDiagnostics[0].Code != "forbidden_project_sandbox_keys" || sources.SandboxDiagnostics[0].Source != "project" || strings.Contains(sources.SandboxDiagnostics[0].Message, secret) {
		t.Fatalf("sandbox diagnostic = %#v", sources.SandboxDiagnostics)
	}
	diagnosticJSON, err := json.Marshal(sources.SandboxDiagnostics[0])
	if err != nil || strings.Contains(string(diagnosticJSON), secret) || strings.Contains(string(diagnosticJSON), projectConfig) {
		t.Fatalf("sandbox diagnostic leaked project authority: %s, %v", diagnosticJSON, err)
	}
	localConfig := filepath.Join(project, ".claude", "settings.local.json")
	if err := os.WriteFile(localConfig, []byte(`{"sandbox":{"guest_profile":false,"secret":"`+secret+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	local, err := LoadProjectLocalConfig(project)
	if err != nil || local.Sandbox != nil {
		t.Fatalf("local sandbox must be discarded before decode: %#v, %v", local, err)
	}
	if err := os.WriteFile(UserConfigPath(), []byte(`{"sandbox":{"guest_profile":"workspace-write","secret":"`+secret+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadUserConfig(); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("strict user sandbox error leaked value: %v", err)
	}
	for name, settings := range map[string]string{
		"malformed":   `{"sandbox":{"guest_profile":false}}`,
		"unsupported": `{"sandbox":{"guest_profile":"not-supported"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(UserConfigPath(), []byte(settings), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadUserConfig(); err == nil || strings.Contains(err.Error(), "not-supported") {
				t.Fatalf("user sandbox validation error = %v", err)
			}
		})
	}
}

func TestSandboxRootsAndSelection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	valid := filepath.Join(t.TempDir(), "valid")
	if err := os.Mkdir(valid, 0o700); err != nil {
		t.Fatal(err)
	}
	canonicalValid, err := filepath.EvalSymlinks(valid)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "other")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	canonicalOther, err := filepath.EvalSymlinks(other)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(valid, link); err != nil {
		t.Fatal(err)
	}
	for name, root := range map[string]string{
		"relative": "relative",
		"missing":  filepath.Join(t.TempDir(), "missing"),
		"root":     "/",
		"home":     home,
		"system":   "/usr",
		"symlink":  link,
	} {
		t.Run(name, func(t *testing.T) {
			err := validateSandboxConfig(&SandboxConfig{ExtraReadRoots: []string{root}})
			if err == nil || strings.Contains(err.Error(), root) {
				t.Fatalf("invalid root error = %v", err)
			}
		})
	}
	config := &SandboxConfig{ExtraReadRoots: []string{canonicalOther, canonicalValid, canonicalValid}}
	if err := validateSandboxConfig(config); err != nil {
		t.Fatal(err)
	}
	selection, err := ResolveSandbox(SandboxSelectionInput{Config: config})
	if err != nil {
		t.Fatal(err)
	}
	wantRoots := []string{canonicalValid, canonicalOther}
	sort.Strings(wantRoots)
	if selection.GuestProfile != "workspace-write" || selection.Source != SandboxSelectionUserConfig || len(selection.ExtraReadRoots) != 2 || selection.ExtraReadRoots[0] != wantRoots[0] || selection.ExtraReadRoots[1] != wantRoots[1] {
		t.Fatalf("Darwin selection = %#v", selection)
	}
	selection, err = ResolveSandbox(SandboxSelectionInput{Config: &SandboxConfig{GuestProfile: "workspace-write"}, CLIProfile: "danger-full-access", CLIProfileSet: true})
	if err != nil || selection.GuestProfile != "danger-full-access" || selection.Source != SandboxSelectionCLI {
		t.Fatalf("CLI precedence = %#v, %v", selection, err)
	}
	selection, err = ResolveSandbox(SandboxSelectionInput{})
	if err != nil || selection.GuestProfile != "workspace-write" || selection.Source != SandboxSelectionDefault {
		t.Fatalf("safe default = %#v, %v", selection, err)
	}
}
