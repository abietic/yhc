package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	oldHome, hadHome := os.LookupEnv("HOME")
	oldEnv := map[string]*string{}
	for _, key := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GOOGLE_API_KEY", "GEMINI_API_KEY", "DEEPSEEK_API_KEY", "CUSTOM_API_KEY"} {
		if value, ok := os.LookupEnv(key); ok {
			v := value
			oldEnv[key] = &v
		} else {
			oldEnv[key] = nil
		}
		_ = os.Unsetenv(key)
	}
	if err := os.Setenv("HOME", home); err != nil {
		t.Fatal(err)
		return ""
	}
	t.Cleanup(func() {
		if hadHome {
			_ = os.Setenv("HOME", oldHome)
		} else {
			_ = os.Unsetenv("HOME")
		}
		for key, old := range oldEnv {
			if old != nil {
				_ = os.Setenv(key, *old)
			} else {
				_ = os.Unsetenv(key)
			}
		}
	})
	return home
}

func TestCredentialStoreLoadSaveListAndDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "credentials.json")
	store := NewCredentialStore(path)
	if err := store.Load(); err != nil {
		t.Fatalf("missing file load should succeed: %v", err)
		return
	}
	if err := store.Set(nil); err == nil {
		t.Fatal("nil credential should fail")
		return
	}
	if err := store.Set(&Credential{Key: "missing-provider"}); err == nil {
		t.Fatal("empty provider should fail")
		return
	}

	expiry := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	if err := store.Set(&Credential{Provider: "openai", Key: "openai-key"}); err != nil {
		t.Fatal(err)
		return
	}
	if err := store.Set(&Credential{Provider: "anthropic", Key: "anthropic-key", ExpiresAt: expiry, Scopes: []string{"profile"}, Meta: map[string]string{"org": "test"}}); err != nil {
		t.Fatal(err)
		return
	}
	if got := store.List(); !reflect.DeepEqual(got, []string{"anthropic", "openai"}) {
		t.Fatalf("providers not sorted: %#v", got)
	}
	if store.IsExpired("anthropic") || store.IsExpired("missing") {
		t.Fatal("future or missing credentials should not be expired")
	}
	if err := store.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
		return
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
		return
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("credential file mode = %o", info.Mode().Perm())
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatal(err)
		return
	} else if info.Mode().Perm() != 0o700 {
		t.Fatalf("credential dir mode = %o", info.Mode().Perm())
	}

	reloaded := NewCredentialStore(path)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
		return
	}
	cred := reloaded.Get("anthropic")
	if cred == nil || cred.Key != "anthropic-key" || cred.Meta["org"] != "test" || len(cred.Scopes) != 1 {
		t.Fatalf("credential did not round trip: %#v", cred)
		return
	}

	reloaded.Delete("openai")
	if got := reloaded.List(); !reflect.DeepEqual(got, []string{"anthropic"}) {
		t.Fatalf("delete failed: %#v", got)
	}
}

func TestCredentialOriginIsStableAndRotationSensitive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	store := NewCredentialStore(path)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(&Credential{Provider: "openai", Key: "key-1"}); err != nil {
		t.Fatal(err)
	}
	first := store.Get("openai")
	if first == nil || first.OriginID == "" || first.OriginRevision != 1 {
		t.Fatalf("first origin = %#v", first)
	}
	first.Meta = map[string]string{"caller": "mutation"}
	if got := store.Get("openai"); got.Meta["caller"] != "" {
		t.Fatalf("Get returned mutable store ownership: %#v", got)
	}
	if err := store.Set(&Credential{Provider: "openai", Key: "key-1"}); err != nil {
		t.Fatal(err)
	}
	same := store.Get("openai")
	if same.OriginID != first.OriginID || same.OriginRevision != first.OriginRevision {
		t.Fatalf("same secret changed origin: first=%#v same=%#v", first, same)
	}
	if err := store.Set(&Credential{Provider: "openai", Key: "key-2"}); err != nil {
		t.Fatal(err)
	}
	rotated := store.Get("openai")
	if rotated.OriginID == first.OriginID || rotated.OriginRevision != 2 {
		t.Fatalf("rotated origin = %#v, first = %#v", rotated, first)
	}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	reloaded := NewCredentialStore(path)
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveNamedCredentialOrigin(reloaded, "openai")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Secret != "key-2" ||
		resolved.OriginID != rotated.OriginID+"/r2" {
		t.Fatalf("resolved credential = %#v", resolved)
	}
}

func TestCredentialStoreLoadMalformedAndNilMap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	if err := os.WriteFile(path, []byte("{bad json"), 0o600); err != nil {
		t.Fatal(err)
		return
	}
	if err := NewCredentialStore(path).Load(); err == nil {
		t.Fatal("malformed credentials should fail")
		return
	}

	if err := os.WriteFile(path, []byte("null"), 0o600); err != nil {
		t.Fatal(err)
		return
	}
	store := NewCredentialStore(path)
	if err := store.Load(); err != nil {
		t.Fatalf("null map should load as empty store: %v", err)
		return
	}
	if got := store.List(); len(got) != 0 {
		t.Fatalf("expected empty store, got %#v", got)
	}
}

func TestResolveAPIKeyPrecedenceAndProviderDetection(t *testing.T) {
	home := isolateHome(t)
	if got := DefaultCredentialPath(); got != filepath.Join(home, ".claude", "credentials.json") {
		t.Fatalf("DefaultCredentialPath = %q", got)
	}
	if _, err := ResolveAPIKey(); err == nil || !strings.Contains(err.Error(), "no API key found") {
		t.Fatalf("expected missing key error, got %v", err)
		return
	}

	if err := os.MkdirAll(filepath.Dir(DefaultCredentialPath()), 0o700); err != nil {
		t.Fatal(err)
		return
	}
	store := NewCredentialStore(DefaultCredentialPath())
	if err := store.Set(&Credential{Provider: "anthropic", Key: "stored-anthropic"}); err != nil {
		t.Fatal(err)
		return
	}
	if err := store.Save(); err != nil {
		t.Fatal(err)
		return
	}
	if got, err := ResolveAPIKey(); err != nil || got != "stored-anthropic" {
		t.Fatalf("ResolveAPIKey stored = %q err=%v", got, err)
		return
	}
	if err := os.Setenv("ANTHROPIC_API_KEY", "env-anthropic"); err != nil {
		t.Fatal(err)
		return
	}
	if got, err := ResolveAPIKey(); err != nil || got != "env-anthropic" {
		t.Fatalf("ResolveAPIKey env = %q err=%v", got, err)
		return
	}

	cases := map[string]string{
		"claude-sonnet": "anthropic",
		"GPT-4o":        "openai",
		"o3-mini":       "openai",
		"o4-mini":       "openai",
		"gemini-pro":    "google",
		"deepseek-chat": "deepseek",
		"unknown":       "anthropic",
	}
	for model, provider := range cases {
		if got := DetectProvider(model); got != provider {
			t.Fatalf("DetectProvider(%q) = %q want %q", model, got, provider)
		}
	}
}

func TestResolveModelAPIKeyProviderEnvAndStoreFallback(t *testing.T) {
	isolateHome(t)
	envCases := []struct {
		model string
		env   string
		value string
	}{
		{"gpt-4o", "OPENAI_API_KEY", "openai-env"},
		{"gemini-pro", "GOOGLE_API_KEY", "google-env"},
		{"deepseek-chat", "DEEPSEEK_API_KEY", "deepseek-env"},
		{"custom-model", "ANTHROPIC_API_KEY", "anthropic-env"},
	}
	for _, tc := range envCases {
		if err := os.Setenv(tc.env, tc.value); err != nil {
			t.Fatal(err)
			return
		}
		got, err := ResolveModelAPIKey(tc.model)
		if err != nil || got != tc.value {
			t.Fatalf("ResolveModelAPIKey(%q) env = %q err=%v", tc.model, got, err)
			return
		}
		_ = os.Unsetenv(tc.env)
	}

	store := NewCredentialStore(DefaultCredentialPath())
	for provider, key := range map[string]string{
		"openai":   "openai-stored",
		"google":   "google-stored",
		"deepseek": "deepseek-stored",
	} {
		if err := store.Set(&Credential{Provider: provider, Key: key}); err != nil {
			t.Fatal(err)
			return
		}
	}
	if err := store.Save(); err != nil {
		t.Fatal(err)
		return
	}
	if got, err := ResolveModelAPIKey("gpt-4o"); err != nil || got != "openai-stored" {
		t.Fatalf("stored openai key = %q err=%v", got, err)
		return
	}
	if got, err := ResolveModelAPIKey("gemini-pro"); err != nil || got != "google-stored" {
		t.Fatalf("stored google key = %q err=%v", got, err)
		return
	}
	if got, err := ResolveModelAPIKey("deepseek-chat"); err != nil || got != "deepseek-stored" {
		t.Fatalf("stored deepseek key = %q err=%v", got, err)
		return
	}
	if _, err := ResolveModelAPIKey("claude-sonnet"); err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Fatalf("missing anthropic key should mention env guidance, got %v", err)
		return
	}
}

func TestResolveNamedCredential(t *testing.T) {
	store := NewCredentialStore(filepath.Join(t.TempDir(), "credentials.json"))
	const secret = "named-secret-must-not-leak"
	if err := store.Set(&Credential{Provider: "work", Key: secret}); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(&Credential{
		Provider:  "expired",
		Key:       secret,
		ExpiresAt: time.Now().Add(-time.Second),
	}); err != nil {
		t.Fatal(err)
	}

	got, err := resolveNamedCredential(store, " work ")
	if err != nil || got != secret {
		t.Fatalf("resolve named credential = %q err=%v", got, err)
	}
	for _, name := range []string{"", "missing", "expired"} {
		if _, err := resolveNamedCredential(store, name); err == nil {
			t.Fatalf("resolve named credential %q should fail", name)
		} else if strings.Contains(err.Error(), secret) {
			t.Fatalf("resolve named credential %q leaked secret: %v", name, err)
		}
	}
}

func TestResolveNamedCredentialFromDefaultStore(t *testing.T) {
	isolateHome(t)
	store := NewCredentialStore(DefaultCredentialPath())
	if err := store.Set(&Credential{Provider: "work", Key: "named-secret"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveNamedCredential("work")
	if err != nil || got != "named-secret" {
		t.Fatalf("ResolveNamedCredential = %q err=%v", got, err)
	}
}

func TestExpiredCredentials(t *testing.T) {
	store := NewCredentialStore(filepath.Join(t.TempDir(), "credentials.json"))
	_ = store.Set(&Credential{Provider: "expired", Key: "x", ExpiresAt: time.Now().Add(-time.Second)})
	_ = store.Set(&Credential{Provider: "no-expiry", Key: "x"})
	if !store.IsExpired("expired") {
		t.Fatal("expired credential should be expired")
	}
	if store.IsExpired("no-expiry") {
		t.Fatal("credential without expiry should not be expired")
	}
}

func TestCredentialWireShape(t *testing.T) {
	cred := Credential{
		Provider: "anthropic",
		Key:      "key",
		Scopes:   []string{"profile"},
		Meta:     map[string]string{"source": "test"},
	}
	data, err := json.Marshal(cred)
	if err != nil {
		t.Fatal(err)
		return
	}
	for _, want := range []string{`"provider":"anthropic"`, `"key":"key"`, `"scopes":["profile"]`, `"meta":{"source":"test"}`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("credential JSON missing %s: %s", want, data)
		}
	}
}
