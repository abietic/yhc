package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/config"
)

func TestP291CLIConfiguredProfileReachesSharedRuntime(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, name := range []string{
		"PROV", "PROV_MODEL", "PROV_API_KEY", "PROV_BASE_URL",
		"PROV_FALLBACK_MODEL", "PROV_MODEL_PROFILE",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("OPENAI_PORTFOLIO_TEST_KEY", "cli-profile-secret")
	if err := os.MkdirAll(filepath.Dir(config.UserConfigPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	settings := `{
		"model_profile":"primary",
		"provider_accounts":{"openai-main":{
			"provider":"openai",
			"base_url":"https://cli-profile.example/v1",
			"auth":{"kind":"env","name":"OPENAI_PORTFOLIO_TEST_KEY"}
		}},
		"model_profiles":{"primary":{
			"account":"openai-main",
			"api_model":"gpt-4o"
		}}
	}`
	if err := os.WriteFile(config.UserConfigPath(), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	engineConfig, resolved, _, err := buildEngineConfig(context.Background(), runtimeFlags{}, &stderr)
	if err != nil {
		t.Fatalf("configured CLI startup failed: %v\nstderr=%s", err, stderr.String())
	}
	if resolved.Provider != "agenticopenai" ||
		resolved.Model != "gpt-4o" ||
		resolved.BaseURL != "https://cli-profile.example/v1" {
		t.Fatalf("configured CLI route = %#v", resolved)
	}
	if resolved.APIKey != "" || !resolved.CredentialConfigured {
		t.Fatalf("configured CLI retained or lost credential state: %#v", resolved)
	}
	if engineConfig.Model != "gpt-4o" {
		t.Fatalf("engine model = %q", engineConfig.Model)
	}
	if !strings.Contains(
		stderr.String(),
		"Selected model profile primary (agenticopenai:gpt-4o)",
	) {
		t.Fatalf("CLI startup omitted selected profile: %s", stderr.String())
	}
	if strings.Contains(stderr.String(), "cli-profile-secret") {
		t.Fatalf("CLI startup output exposed credential: %s", stderr.String())
	}
}

func TestP292LegacyCLIStartupDoesNotClaimNamedProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, name := range []string{
		"PROV", "PROV_MODEL", "PROV_API_KEY", "PROV_BASE_URL",
		"PROV_FALLBACK_MODEL", "PROV_MODEL_PROFILE",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("OPENAI_API_KEY", "legacy-cli-secret")
	t.Setenv("OPENAI_MODEL", "gpt-4o")

	var stderr bytes.Buffer
	_, _, _, err := buildEngineConfig(
		context.Background(),
		runtimeFlags{},
		&stderr,
	)
	if err != nil {
		t.Fatalf("legacy CLI startup failed: %v\nstderr=%s", err, stderr.String())
	}
	if strings.Contains(stderr.String(), "Selected model profile") {
		t.Fatalf("legacy startup claimed a named profile: %s", stderr.String())
	}
	if strings.Contains(stderr.String(), "legacy-cli-secret") {
		t.Fatalf("legacy startup exposed credential: %s", stderr.String())
	}
}

func TestP291RuntimeCommandsExposeModelProfileFlag(t *testing.T) {
	root := newRootCommand()
	if root.Flags().Lookup("model-profile") == nil {
		t.Fatal("root command is missing --model-profile")
	}
	for _, commandPath := range [][]string{{"exec"}, {"resume"}, {"serve", "acp"}, {"goal", "run"}} {
		command, _, err := root.Find(commandPath)
		if err != nil {
			t.Fatalf("find %v: %v", commandPath, err)
		}
		if command.Flags().Lookup("model-profile") == nil {
			t.Fatalf("%v is missing --model-profile", commandPath)
		}
	}
}
