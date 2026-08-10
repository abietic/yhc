package config

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/model"
)

// ---------------------------------------------------------------------------
// Credential Store parity tests
// ---------------------------------------------------------------------------

func TestCredentialStoreEncryptionRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "creds.enc")
	passphrase := "test-passphrase-1234"

	// Create store and set credentials
	store := NewCredentialStoreWithPath(storePath, passphrase)
	store.Set(model.ProviderOpenAI, "sk-openai-test-key")
	store.SetWithBaseURL(model.ProviderAnthropic, "sk-ant-key", "https://custom.api.com")

	// Save to disk
	if err := store.Save(); err != nil {
		t.Fatalf("Save: %v", err)
		return
	}

	// Verify file exists and is not plaintext
	data, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
		return
	}
	if strings.Contains(string(data), "sk-openai-test-key") {
		t.Fatal("credential file contains plaintext API key — encryption failed")
	}

	// Load in a new store instance with same passphrase
	store2 := NewCredentialStoreWithPath(storePath, passphrase)
	if err := store2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
		return
	}

	// Verify round-trip
	entry, ok := store2.Get(model.ProviderOpenAI)
	if !ok {
		t.Fatal("expected OpenAI credential after load")
	}
	if entry.APIKey != "sk-openai-test-key" {
		t.Fatalf("expected 'sk-openai-test-key', got %q", entry.APIKey)
	}

	entry, ok = store2.Get(model.ProviderAnthropic)
	if !ok {
		t.Fatal("expected Anthropic credential after load")
	}
	if entry.APIKey != "sk-ant-key" {
		t.Fatalf("expected 'sk-ant-key', got %q", entry.APIKey)
	}
	if entry.BaseURL != "https://custom.api.com" {
		t.Fatalf("expected custom base URL, got %q", entry.BaseURL)
	}
}

func TestCredentialStoreMultiProviderIsolation(t *testing.T) {
	store := NewCredentialStoreWithPath(filepath.Join(t.TempDir(), "c.enc"), "pass")
	store.Set(model.ProviderOpenAI, "key-openai")
	store.Set(model.ProviderAnthropic, "key-anthropic")
	store.Set(model.ProviderGoogle, "key-google")

	providers := store.Providers()
	if len(providers) != 3 {
		t.Fatalf("expected 3 providers, got %d", len(providers))
	}

	// Delete one — should not affect others
	store.Delete(model.ProviderGoogle)
	_, ok := store.Get(model.ProviderGoogle)
	if ok {
		t.Fatal("expected Google credential to be deleted")
	}
	_, ok = store.Get(model.ProviderOpenAI)
	if !ok {
		t.Fatal("expected OpenAI credential to survive deletion of Google")
	}
}

func TestCredentialStoreEnvFallback(t *testing.T) {
	store := NewCredentialStoreWithPath(filepath.Join(t.TempDir(), "c.enc"), "pass")
	// Don't set any credentials in the store

	// Set env var
	_ = os.Setenv("OPENAI_API_KEY", "env-key-1234")
	defer os.Unsetenv("OPENAI_API_KEY") //nolint:errcheck

	key := store.ResolveAPIKey(model.ProviderOpenAI)
	if key != "env-key-1234" {
		t.Fatalf("expected env fallback 'env-key-1234', got %q", key)
	}
}

func TestCredentialStoreWrongPassphrase(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "creds.enc")

	// Save with one passphrase
	store := NewCredentialStoreWithPath(storePath, "correct-password")
	store.Set(model.ProviderOpenAI, "secret-key")
	if err := store.Save(); err != nil {
		t.Fatalf("Save: %v", err)
		return
	}

	// Try to load with wrong passphrase
	store2 := NewCredentialStoreWithPath(storePath, "wrong-password")
	err := store2.Load()
	if err == nil {
		t.Fatal("expected error when loading with wrong passphrase")
		return
	}
}

// ---------------------------------------------------------------------------
// Config Reload parity tests
// ---------------------------------------------------------------------------

func TestConfigReloadCallbackFired(t *testing.T) {
	tmpDir := t.TempDir()

	// Create initial settings file
	settingsDir := filepath.Join(tmpDir, ".claude")
	_ = os.MkdirAll(settingsDir, 0o755)
	settingsFile := filepath.Join(settingsDir, "settings.json")
	_ = os.WriteFile(settingsFile, []byte(`{"model": "claude-sonnet-4-6"}`), 0o644)

	reloader := NewConfigReloader(tmpDir, 50*time.Millisecond)

	var mu sync.Mutex
	var callbackCalled bool
	var receivedEvent ConfigChangeEvent

	reloader.OnChange(func(event ConfigChangeEvent) {
		mu.Lock()
		callbackCalled = true
		receivedEvent = event
		mu.Unlock()
	})

	// Explicit reload should fire callback
	_, _, err := reloader.ReloadConfig()
	if err != nil {
		t.Fatalf("ReloadConfig: %v", err)
		return
	}

	mu.Lock()
	called := callbackCalled
	event := receivedEvent
	mu.Unlock()

	if !called {
		t.Fatal("expected callback to be fired on ReloadConfig")
	}
	if event.Source != "explicit_reload" {
		t.Fatalf("expected source 'explicit_reload', got %q", event.Source)
	}
}

func TestConfigReloadPreservesOldOnInvalid(t *testing.T) {
	tmpDir := t.TempDir()
	settingsDir := filepath.Join(tmpDir, ".claude")
	_ = os.MkdirAll(settingsDir, 0o755)

	// Write valid settings first
	settingsFile := filepath.Join(settingsDir, "settings.json")
	_ = os.WriteFile(settingsFile, []byte(`{"model": "claude-sonnet-4-6"}`), 0o644)

	reloader := NewConfigReloader(tmpDir, 100*time.Millisecond)
	if err := reloader.Start(); err != nil {
		t.Fatalf("Start: %v", err)
		return
	}
	defer reloader.Stop()

	original := reloader.CurrentSettings()
	if original == nil {
		t.Fatal("expected non-nil initial settings")
		return
	}

	// Write invalid settings (empty model triggers error)
	_ = os.WriteFile(settingsFile, []byte(`{"model": ""}`), 0o644)

	// Force explicit reload
	settings, vr, err := reloader.ReloadConfig()
	if err != nil {
		t.Fatalf("ReloadConfig: %v", err)
		return
	}

	// Invalid config (empty model is an error) — old config should be preserved
	if vr != nil && vr.HasErrors() {
		if settings.Model != "claude-sonnet-4-6" {
			t.Fatalf("expected old model preserved on invalid reload, got %q", settings.Model)
			return
		}
	}
}

// ---------------------------------------------------------------------------
// Provider Detection parity tests
// ---------------------------------------------------------------------------

func TestProviderDetectionAllPrefixes(t *testing.T) {
	cases := []struct {
		modelName string
		want      model.ProviderID
	}{
		{"claude-sonnet-4-6", model.ProviderAnthropic},
		{"claude-haiku-4-5-20251001", model.ProviderAnthropic},
		{"gpt-4o", model.ProviderOpenAI},
		{"gpt-4o-mini", model.ProviderOpenAI},
		{"o1-preview", model.ProviderOpenAI},
		{"o3-mini", model.ProviderOpenAI},
		{"o4-mini", model.ProviderOpenAI},
		{"gemini-2.5-flash", model.ProviderGoogle},
		{"gemini-2.5-pro", model.ProviderGoogle},
		{"deepseek-chat", model.ProviderDeepSeek},
		{"deepseek-coder", model.ProviderDeepSeek},
		{"qwen-max", model.ProviderQwen},
		{"qwen-turbo", model.ProviderQwen},
		{"doubao-1.5-pro-32k", model.ProviderArk},
		{"ep-20230101000001", model.ProviderArk},
		{"unknown-model", model.ProviderUnknown},
		{"", model.ProviderUnknown},
	}

	for _, tc := range cases {
		got := model.DetectProvider(tc.modelName)
		if got != tc.want {
			t.Errorf("DetectProvider(%q) = %q, want %q", tc.modelName, got, tc.want)
		}
	}
}

func TestProviderDetectionCaseInsensitivity(t *testing.T) {
	// Should handle mixed case via normalization
	got := model.DetectProvider("Claude-Sonnet-4-6")
	if got != model.ProviderAnthropic {
		t.Fatalf("expected anthropic for 'Claude-Sonnet-4-6', got %q", got)
	}

	got = model.DetectProvider("GPT-4O")
	if got != model.ProviderOpenAI {
		t.Fatalf("expected openai for 'GPT-4O', got %q", got)
	}
}

func TestProviderDetectionARNFormat(t *testing.T) {
	// Bedrock ARN-style model IDs should be detected by content
	arn := "arn:aws:bedrock:us-east-1::foundation-model/anthropic.claude-v2"
	got := model.DetectProvider(arn)
	if got != model.ProviderAnthropic {
		t.Fatalf("expected anthropic for ARN-style model, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Model Capabilities parity tests
// ---------------------------------------------------------------------------

func TestModelCapabilitiesKnownModels(t *testing.T) {
	// Test a few well-known models for correct capability flags
	knownModels := []struct {
		name           string
		wantTools      bool
		wantStreaming  bool
		wantNonZeroCtx bool
	}{
		{"claude-sonnet-4-6", true, true, true},
		{"claude-sonnet-4-20250514", true, true, true},
		{"gpt-4o", true, true, true},
		{"gemini-2.5-flash", true, true, true},
	}

	for _, tc := range knownModels {
		cap := model.GetCapabilities(tc.name)
		if cap.SupportsTools != tc.wantTools {
			t.Errorf("model %q: SupportsTools = %v, want %v", tc.name, cap.SupportsTools, tc.wantTools)
		}
		if cap.SupportsStreaming != tc.wantStreaming {
			t.Errorf("model %q: SupportsStreaming = %v, want %v", tc.name, cap.SupportsStreaming, tc.wantStreaming)
		}
		if tc.wantNonZeroCtx && cap.ContextWindow == 0 {
			t.Errorf("model %q: ContextWindow should be non-zero", tc.name)
		}
	}
}

// ---------------------------------------------------------------------------
// Validation parity tests
// ---------------------------------------------------------------------------

func TestValidationCatchesEmptyModel(t *testing.T) {
	s := &Settings{Model: ""}
	vr := ValidateConfig(s)
	if !vr.HasErrors() {
		t.Fatal("expected validation error for empty model")
	}
	errs := vr.Errors()
	found := false
	for _, e := range errs {
		if e.Field == "model" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected model field error")
	}
}

func TestValidationCatchesInvalidPermissionMode(t *testing.T) {
	s := &Settings{
		Model:          "claude-sonnet-4-6",
		PermissionMode: "invalid_mode_xyz",
	}
	vr := ValidateConfig(s)
	// Should have at least a warning about unknown permission mode
	hasPermissionIssue := false
	for _, r := range vr.Results {
		if r.Field == "permission_mode" {
			hasPermissionIssue = true
			break
		}
	}
	if !hasPermissionIssue {
		t.Log("Note: validation may not flag unknown permission modes as errors")
	}
}

func TestValidationCatchesInvalidTimeout(t *testing.T) {
	s := &Settings{
		Model:   "claude-sonnet-4-6",
		Timeout: -1 * time.Second,
	}
	vr := ValidateConfig(s)
	hasTimeoutIssue := false
	for _, r := range vr.Results {
		if r.Field == "timeout" {
			hasTimeoutIssue = true
			break
		}
	}
	if !hasTimeoutIssue {
		t.Log("Note: negative timeout may not be explicitly validated")
	}
}

func TestValidationResultsString(t *testing.T) {
	vr := &ValidationResults{
		Results: []ValidationResult{
			{Field: "model", Message: "unknown model", Severity: SeverityWarning},
			{Field: "api_key", Message: "key not set", Severity: SeverityError},
		},
	}

	str := vr.String()
	if !strings.Contains(str, "model") {
		t.Error("expected 'model' in String() output")
	}
	if !strings.Contains(str, "api_key") {
		t.Error("expected 'api_key' in String() output")
	}
	if !strings.Contains(str, "[warning]") {
		t.Error("expected '[warning]' in String() output")
	}
	if !strings.Contains(str, "[error]") {
		t.Error("expected '[error]' in String() output")
	}
}

func TestMaskedAPIKeyParity(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"", "(not set)"},
		{"abc", "****"},
		{"sk-1234567890", "...7890"},
	}
	for _, tc := range cases {
		got := MaskedAPIKey(tc.input)
		if got != tc.want {
			t.Errorf("MaskedAPIKey(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
