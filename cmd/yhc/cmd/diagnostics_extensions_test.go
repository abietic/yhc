package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type inspectionTestEnvelope struct {
	SchemaVersion int                          `json:"schema_version"`
	Operation     string                       `json:"operation"`
	Status        string                       `json:"status"`
	ExitCode      int                          `json:"exit_code"`
	Result        json.RawMessage              `json:"result"`
	Error         *administrationEnvelopeError `json:"error"`
}

func TestDiagnosticsCLIUsesProviderFreeRedactedSnapshot(t *testing.T) {
	project := prepareInspectionCLIProject(t)
	secret := "diagnostics-secret-value"
	t.Setenv("PROV", "openai")
	t.Setenv("PROV_API_KEY", secret)
	t.Setenv("PROV_BASE_URL", "https://user:"+secret+"@example.test/private?token="+secret+"#fragment")

	stdout, stderr, err := executeSessionCLI(
		context.Background(),
		t,
		"config", "show", "--output-format", "json",
	)
	if err != nil || stderr != "" {
		t.Fatalf("config show: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if strings.Contains(stdout, secret) || strings.Contains(stdout, "/private") ||
		strings.Contains(stdout, "token=") || strings.Contains(stdout, "user:") {
		t.Fatalf("config output exposed endpoint secret: %s", stdout)
	}
	envelope := decodeInspectionEnvelope(t, stdout)
	if envelope.SchemaVersion != administrationEnvelopeSchemaVersion ||
		envelope.Operation != "config.show" || envelope.Status != "completed" ||
		envelope.ExitCode != ExitSuccess {
		t.Fatalf("config envelope = %#v", envelope)
	}
	var configResult struct {
		Provider struct {
			State string `json:"state"`
			Value string `json:"value"`
		} `json:"provider"`
		Credential struct {
			State string `json:"state"`
			Value bool   `json:"value"`
		} `json:"credential_configured"`
		Endpoint struct {
			Value string `json:"value"`
		} `json:"endpoint"`
	}
	if err := json.Unmarshal(envelope.Result, &configResult); err != nil {
		t.Fatal(err)
	}
	if configResult.Provider.State != "known" || configResult.Provider.Value != "agenticopenai" ||
		!configResult.Credential.Value || configResult.Endpoint.Value != "https://example.test" {
		t.Fatalf("config result = %#v", configResult)
	}
	assertNoInspectionTranscript(t, project)

	t.Setenv("PROV", "unsupported-provider")
	stdout, stderr, err = executeSessionCLI(
		context.Background(),
		t,
		"doctor", "--output-format", "json",
	)
	if err != nil || stderr != "" {
		t.Fatalf("provider-invalid doctor: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	envelope = decodeInspectionEnvelope(t, stdout)
	var doctorResult struct {
		Checks []struct {
			ID      string `json:"id"`
			Outcome string `json:"outcome"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(envelope.Result, &doctorResult); err != nil {
		t.Fatal(err)
	}
	wantChecks := map[string]string{
		"provider.route":        "fail",
		"provider.connectivity": "skipped",
		"session.transcript":    "skipped",
	}
	for _, check := range doctorResult.Checks {
		if want, ok := wantChecks[check.ID]; ok {
			if check.Outcome != want {
				t.Fatalf("doctor check %s = %s, want %s", check.ID, check.Outcome, want)
			}
			delete(wantChecks, check.ID)
		}
	}
	if len(wantChecks) != 0 {
		t.Fatalf("doctor checks missing: %v", wantChecks)
	}
	assertNoInspectionTranscript(t, project)
}

func TestDoctorReportsInvalidSettingsWhileConfigShowFailsClosed(t *testing.T) {
	project := prepareInspectionCLIProject(t)
	settingsDir := filepath.Join(project, ".claude")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "settings.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := executeSessionCLI(
		context.Background(),
		t,
		"config", "show", "--output-format", "json",
	)
	if ExitCode(err) != ExitFailure || stderr != "" {
		t.Fatalf("invalid config: stdout=%q stderr=%q err=%v exit=%d", stdout, stderr, err, ExitCode(err))
	}
	configEnvelope := decodeInspectionEnvelope(t, stdout)
	if configEnvelope.Error == nil || configEnvelope.Error.Code != "config_error" {
		t.Fatalf("invalid config envelope = %#v", configEnvelope)
	}

	stdout, stderr, err = executeSessionCLI(
		context.Background(),
		t,
		"doctor", "--output-format", "json",
	)
	if err != nil || stderr != "" {
		t.Fatalf("doctor invalid settings: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	doctorEnvelope := decodeInspectionEnvelope(t, stdout)
	if !strings.Contains(string(doctorEnvelope.Result), `"id":"config.project"`) ||
		!strings.Contains(string(doctorEnvelope.Result), `"outcome":"fail"`) {
		t.Fatalf("doctor did not report invalid settings: %s", doctorEnvelope.Result)
	}
	assertNoInspectionTranscript(t, project)
}

func TestMCPCLIProjectsConfiguredUnprobedInventoryWithoutLaunching(t *testing.T) {
	project := prepareInspectionCLIProject(t)
	marker := filepath.Join(project, "launched")
	launcher := filepath.Join(project, "must-not-run.sh")
	if err := os.WriteFile(
		launcher,
		[]byte("#!/bin/sh\nprintf launched > \""+marker+"\"\n"),
		0o700,
	); err != nil {
		t.Fatal(err)
	}
	secret := "mcp-secret-value"
	config := `{"mcpServers":{` +
		`"active":{"command":` + quotedJSON(launcher) + `,"args":["` + secret + `"]},` +
		`"disabled":{"type":"http","url":"https://user:` + secret + `@example.test/path","disabled":true}` +
		`}}`
	if err := os.WriteFile(filepath.Join(project, ".mcp.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := executeSessionCLI(
		context.Background(),
		t,
		"mcp", "list", "--output-format", "json",
	)
	if err != nil || stderr != "" {
		t.Fatalf("mcp list: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if strings.Contains(stdout, secret) || strings.Contains(stdout, launcher) ||
		strings.Contains(stdout, "example.test") {
		t.Fatalf("mcp output exposed connection material: %s", stdout)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("mcp inspection launched configured command: %v", statErr)
	}
	envelope := decodeInspectionEnvelope(t, stdout)
	var inventory mcpInspectionOutput
	if err := json.Unmarshal(envelope.Result, &inventory); err != nil {
		t.Fatal(err)
	}
	if inventory.Revision != 1 || inventory.Source != "configuration" || len(inventory.Servers) != 2 ||
		inventory.Servers[0].Name != "active" || inventory.Servers[0].Status != "configured" ||
		inventory.Servers[1].Name != "disabled" || inventory.Servers[1].Status != "disabled" {
		t.Fatalf("MCP inventory = %#v", inventory)
	}
	for _, server := range inventory.Servers {
		if server.Source != "configuration" || server.Health != "unprobed" ||
			server.Diagnostic != "inspection_only_no_connection" || len(server.Tools) != 0 {
			t.Fatalf("MCP server = %#v", server)
		}
	}

	stdout, stderr, err = executeSessionCLI(
		context.Background(),
		t,
		"mcp", "get", "missing", "--output-format", "json",
	)
	if ExitCode(err) != ExitFailure || stderr != "" {
		t.Fatalf("mcp get missing: stdout=%q stderr=%q err=%v exit=%d", stdout, stderr, err, ExitCode(err))
	}
	missing := decodeInspectionEnvelope(t, stdout)
	if missing.Error == nil || missing.Error.Code != "not_found" {
		t.Fatalf("missing MCP envelope = %#v", missing)
	}
	assertNoInspectionTranscript(t, project)
}

func TestInspectionCLITextDistinguishesConfiguredFromRuntimeMCP(t *testing.T) {
	project := prepareInspectionCLIProject(t)
	if err := os.WriteFile(
		filepath.Join(project, ".mcp.json"),
		[]byte(`{"mcpServers":{"docs":{"type":"http","url":"https://example.test"}}}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := executeSessionCLI(context.Background(), t, "mcp", "list")
	if err != nil || stderr != "" {
		t.Fatalf("mcp list text: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if !strings.Contains(stdout, "MCP configured inventory generation 1") ||
		!strings.Contains(stdout, "health=unprobed; source=configuration") ||
		strings.Contains(stdout, "MCP runtime manager inventory") {
		t.Fatalf("configured MCP text = %q", stdout)
	}
}

func TestPluginsCLIUsesCandidateValidationAndAtomicInspectionReload(t *testing.T) {
	project := prepareInspectionCLIProject(t)
	pluginDir := filepath.Join(project, ".claude", "plugins", "sample")
	if err := os.MkdirAll(filepath.Join(pluginDir, "commands"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "commands", "audit.md"), []byte("audit prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(pluginDir, "plugin.json")
	if err := os.WriteFile(manifestPath, []byte(`{
		"name":"sample","version":"1.0.0",
		"commands":[{"name":"audit","filePath":"commands/audit.md"}]
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, operation := range []string{"list", "validate", "reload"} {
		stdout, stderr, err := executeSessionCLI(
			context.Background(),
			t,
			"plugins", operation, "--output-format", "json",
		)
		if err != nil || stderr != "" {
			t.Fatalf("plugins %s: stdout=%q stderr=%q err=%v", operation, stdout, stderr, err)
		}
		envelope := decodeInspectionEnvelope(t, stdout)
		var result pluginInspectionOutput
		if err := json.Unmarshal(envelope.Result, &result); err != nil {
			t.Fatal(err)
		}
		wantRevision := uint64(1)
		if operation == "reload" {
			wantRevision = 2
		}
		if result.Health != "valid" || result.ProcessScope != "inspection-host" ||
			result.Candidate.EnabledPlugins != 1 || result.Candidate.Commands != 3 ||
			result.LiveGeneration.Revision != wantRevision {
			t.Fatalf("plugins %s result = %#v", operation, result)
		}
	}

	if err := os.WriteFile(manifestPath, []byte(`{
		"name":"sample","commands":[{"name":"audit","filePath":"commands/missing.md"}]
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := executeSessionCLI(
		context.Background(),
		t,
		"plugins", "validate", "--output-format", "json",
	)
	if ExitCode(err) != ExitFailure || stderr != "" {
		t.Fatalf("invalid plugins: stdout=%q stderr=%q err=%v exit=%d", stdout, stderr, err, ExitCode(err))
	}
	invalid := decodeInspectionEnvelope(t, stdout)
	if invalid.Error == nil || invalid.Error.Code != "plugin_validation_error" || len(invalid.Result) == 0 {
		t.Fatalf("invalid plugin envelope = %#v", invalid)
	}
	var result pluginInspectionOutput
	if err := json.Unmarshal(invalid.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.Health != "invalid" || result.LiveGeneration.Revision != 0 ||
		len(result.Diagnostics) == 0 {
		t.Fatalf("invalid plugin result = %#v", result)
	}
	assertNoInspectionTranscript(t, project)
}

func TestInspectionCLIUsageCancellationAndUnsupportedMutations(t *testing.T) {
	prepareInspectionCLIProject(t)
	for _, args := range [][]string{
		{"config", "--output-format", "json"},
		{"mcp", "add", "unsafe", "--output-format", "json"},
		{"plugins", "install", "unsafe", "--output-format", "json"},
		{"plugins", "enable", "unsafe", "--output-format", "json"},
		{"config", "show", "--output-format", "json", "--provider", "openai"},
		{"doctor", "--output-format", "json", "--unknown"},
	} {
		stdout, stderr, err := executeSessionCLI(context.Background(), t, args...)
		if ExitCode(err) != ExitUsage || stderr != "" {
			t.Fatalf("usage %v: stdout=%q stderr=%q err=%v exit=%d", args, stdout, stderr, err, ExitCode(err))
		}
		envelope := decodeInspectionEnvelope(t, stdout)
		if envelope.Status != "failed" || envelope.ExitCode != ExitUsage ||
			envelope.Error == nil || envelope.Error.Code != "usage_error" {
			t.Fatalf("usage %v envelope = %#v", args, envelope)
		}
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	stdout, stderr, err := executeSessionCLI(
		cancelled,
		t,
		"doctor", "--output-format", "json",
	)
	if ExitCode(err) != ExitCancelled || stderr != "" {
		t.Fatalf("cancelled doctor: stdout=%q stderr=%q err=%v exit=%d", stdout, stderr, err, ExitCode(err))
	}
	envelope := decodeInspectionEnvelope(t, stdout)
	if envelope.Status != "cancelled" || envelope.ExitCode != ExitCancelled ||
		envelope.Error == nil || envelope.Error.Code != "cancelled" {
		t.Fatalf("cancelled envelope = %#v", envelope)
	}
}

func prepareInspectionCLIProject(t *testing.T) string {
	t.Helper()
	project := t.TempDir()
	home := t.TempDir()
	t.Chdir(project)
	t.Setenv("HOME", home)
	for _, name := range []string{
		"PROV", "PROV_MODEL", "PROV_API_KEY", "PROV_BASE_URL",
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GOOGLE_API_KEY", "GEMINI_API_KEY",
		"DEEPSEEK_API_KEY", "DASHSCOPE_API_KEY", "ARK_API_KEY", "VOLCENGINE_API_KEY",
	} {
		t.Setenv(name, "")
	}
	return project
}

func decodeInspectionEnvelope(t *testing.T, output string) inspectionTestEnvelope {
	t.Helper()
	var envelope inspectionTestEnvelope
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("decode inspection envelope: %v; output=%q", err, output)
	}
	return envelope
}

func assertNoInspectionTranscript(t *testing.T, project string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(project, ".eino-agent", "transcripts"))
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".jsonl") {
			t.Fatalf("inspection created transcript %s", entry.Name())
		}
	}
}

func quotedJSON(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}
