package acp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/config"
)

func TestP291ACPCreateAndRestoreUseConfiguredProfile(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	t.Setenv("HOME", home)
	for _, name := range []string{
		"PROV", "PROV_MODEL", "PROV_API_KEY", "PROV_BASE_URL",
		"PROV_FALLBACK_MODEL", "PROV_MODEL_PROFILE",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("OPENAI_ACP_PORTFOLIO_KEY", "acp-profile-secret")
	if err := os.MkdirAll(filepath.Dir(config.UserConfigPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	settings := `{
		"model_profile":"primary",
		"provider_accounts":{"openai-main":{
			"provider":"openai",
			"base_url":"https://acp-profile.example/v1",
			"auth":{"kind":"env","name":"OPENAI_ACP_PORTFOLIO_KEY"}
		}},
		"model_profiles":{"primary":{
			"account":"openai-main",
			"api_model":"gpt-4o"
		}}
	}`
	if err := os.WriteFile(config.UserConfigPath(), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}

	agent, err := NewAgent(Config{CWD: project})
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	created, err := agent.createEngineWithSessionMCP(
		context.Background(),
		acpsdk.SessionId("p291-create"),
		nil,
		project,
	)
	if err != nil {
		t.Fatalf("ACP create path failed: %v", err)
	}
	assertP291ACPRoute(t, created)
	created.Close()

	restored, err := agent.createEngineForSessionWithConstructor(
		"p291-restore",
		project,
		engine.NewRestoreStagingQueryEngine,
	)
	if err != nil {
		t.Fatalf("ACP restore path failed: %v", err)
	}
	assertP291ACPRoute(t, restored)
	restored.Close()
}

func assertP291ACPRoute(t *testing.T, queryEngine *engine.QueryEngine) {
	t.Helper()
	inventory := queryEngine.ModelInventory()
	if inventory.Default != "primary" ||
		len(inventory.Entries) != 1 ||
		inventory.Entries[0].Selector != "primary" ||
		queryEngine.GetModelName() != "primary" {
		t.Fatalf(
			"ACP configured inventory = %#v, active=%q",
			inventory,
			queryEngine.GetModelName(),
		)
	}
	snapshot, err := queryEngine.DiagnosticsSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status.Provider.Value != "agenticopenai" ||
		snapshot.Status.Model.Value != "gpt-4o" {
		t.Fatalf("ACP configured route = %#v/%#v", snapshot.Status.Provider, snapshot.Status.Model)
	}
	if !snapshot.Config.CredentialConfigured.Value {
		t.Fatal("ACP configured route lost credential state")
	}
	if strings.Contains(snapshot.Config.Endpoint.Value, "acp-profile-secret") {
		t.Fatalf("ACP endpoint exposed credential: %q", snapshot.Config.Endpoint.Value)
	}
}
