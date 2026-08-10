package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/abietic/yhc/engine/model"
)

func TestCredentialStore_EncryptDecryptRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "credentials.enc")
	passphrase := "test-passphrase-secure-123"

	// Create store and add credentials.
	store := NewCredentialStoreWithPath(storePath, passphrase)
	store.Set(model.ProviderAnthropic, "sk-ant-api123-secret-key")
	store.Set(model.ProviderOpenAI, "sk-openai-key-456")
	store.SetWithBaseURL(model.ProviderDeepSeek, "sk-deep-789", "https://custom.deepseek.com")

	// Save to disk.
	if err := store.Save(); err != nil {
		t.Fatalf("Save() error: %v", err)
		return
	}

	// Verify the file exists and is not plaintext.
	data, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("reading store file: %v", err)
		return
	}
	if len(data) == 0 {
		t.Fatal("store file is empty")
	}
	// Should not contain the plaintext API keys.
	for _, key := range []string{"sk-ant-api123", "sk-openai-key", "sk-deep-789"} {
		if containsBytes(data, []byte(key)) {
			t.Errorf("store file contains plaintext key %q", key)
		}
	}

	// Load into a new store instance.
	store2 := NewCredentialStoreWithPath(storePath, passphrase)
	if err := store2.Load(); err != nil {
		t.Fatalf("Load() error: %v", err)
		return
	}

	// Verify all credentials round-tripped correctly.
	entry, ok := store2.Get(model.ProviderAnthropic)
	if !ok {
		t.Fatal("expected Anthropic credential to exist")
	}
	if entry.APIKey != "sk-ant-api123-secret-key" {
		t.Errorf("Anthropic API key = %q, want sk-ant-api123-secret-key", entry.APIKey)
	}

	entry, ok = store2.Get(model.ProviderOpenAI)
	if !ok {
		t.Fatal("expected OpenAI credential to exist")
	}
	if entry.APIKey != "sk-openai-key-456" {
		t.Errorf("OpenAI API key = %q, want sk-openai-key-456", entry.APIKey)
	}

	entry, ok = store2.Get(model.ProviderDeepSeek)
	if !ok {
		t.Fatal("expected DeepSeek credential to exist")
	}
	if entry.APIKey != "sk-deep-789" {
		t.Errorf("DeepSeek API key = %q, want sk-deep-789", entry.APIKey)
	}
	if entry.BaseURL != "https://custom.deepseek.com" {
		t.Errorf("DeepSeek BaseURL = %q, want https://custom.deepseek.com", entry.BaseURL)
	}
}

func TestCredentialStore_WrongPassphrase(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "credentials.enc")

	// Save with one passphrase.
	store := NewCredentialStoreWithPath(storePath, "correct-passphrase")
	store.Set(model.ProviderOpenAI, "sk-secret")
	if err := store.Save(); err != nil {
		t.Fatalf("Save() error: %v", err)
		return
	}

	// Try to load with wrong passphrase.
	store2 := NewCredentialStoreWithPath(storePath, "wrong-passphrase")
	err := store2.Load()
	if err == nil {
		t.Fatal("expected error when loading with wrong passphrase")
		return
	}
}

func TestCredentialStore_NonExistentFile(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "nonexistent", "credentials.enc")

	store := NewCredentialStoreWithPath(storePath, "passphrase")
	err := store.Load()
	// Should succeed (empty store) when file doesn't exist.
	if err != nil {
		t.Fatalf("Load() should return nil for nonexistent file, got: %v", err)
		return
	}
	if !store.IsLoaded() {
		t.Error("expected IsLoaded() = true after loading nonexistent file")
	}
}

func TestCredentialStore_EmptyPassphrase(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "credentials.enc")

	store := NewCredentialStoreWithPath(storePath, "")
	store.Set(model.ProviderOpenAI, "sk-key")

	err := store.Save()
	if err == nil {
		t.Fatal("expected error when saving with empty passphrase")
		return
	}
}

func TestCredentialStore_Delete(t *testing.T) {
	store := NewCredentialStoreWithPath("", "pass")
	store.Set(model.ProviderOpenAI, "sk-test")
	store.Set(model.ProviderAnthropic, "sk-ant")

	store.Delete(model.ProviderOpenAI)

	_, ok := store.Get(model.ProviderOpenAI)
	if ok {
		t.Error("expected OpenAI credential to be deleted")
	}

	_, ok = store.Get(model.ProviderAnthropic)
	if !ok {
		t.Error("expected Anthropic credential to still exist")
	}
}

func TestCredentialStore_Providers(t *testing.T) {
	store := NewCredentialStoreWithPath("", "pass")
	store.Set(model.ProviderOpenAI, "sk-1")
	store.Set(model.ProviderAnthropic, "sk-2")
	store.Set(model.ProviderGoogle, "sk-3")

	providers := store.Providers()
	if len(providers) != 3 {
		t.Errorf("expected 3 providers, got %d", len(providers))
	}
}

func TestCredentialStore_ResolveAPIKey_StoreFirst(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "env-key-123")

	store := NewCredentialStoreWithPath("", "pass")
	store.Set(model.ProviderOpenAI, "store-key-456")

	// Store should take precedence over env.
	key := store.ResolveAPIKey(model.ProviderOpenAI)
	if key != "store-key-456" {
		t.Errorf("expected store key, got %q", key)
	}
}

func TestCredentialStore_ResolveAPIKey_FallbackToEnv(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "env-key-789")

	store := NewCredentialStoreWithPath("", "pass")
	// Don't set any credential in the store.

	key := store.ResolveAPIKey(model.ProviderOpenAI)
	if key != "env-key-789" {
		t.Errorf("expected env key, got %q", key)
	}
}

func TestMaskedAPIKey(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", "(not set)"},
		{"ab", "****"},
		{"abcd", "****"},
		{"sk-very-long-api-key-12345", "...2345"},
		{"12345", "...2345"},
	}
	for _, tt := range tests {
		got := MaskedAPIKey(tt.input)
		if got != tt.want {
			t.Errorf("MaskedAPIKey(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestEncryptDecrypt_DataIntegrity(t *testing.T) {
	// Test various data sizes.
	testCases := []string{
		"",
		"short",
		"medium length test data for encryption verification",
		string(make([]byte, 1024)),  // 1KB
		string(make([]byte, 10240)), // 10KB
	}

	passphrase := "test-passphrase"
	for i, plaintext := range testCases {
		encrypted, err := encryptData([]byte(plaintext), passphrase)
		if err != nil {
			t.Fatalf("case %d: encryptData error: %v", i, err)
			return
		}

		decrypted, err := decryptData(encrypted, passphrase)
		if err != nil {
			t.Fatalf("case %d: decryptData error: %v", i, err)
			return
		}

		if string(decrypted) != plaintext {
			t.Errorf("case %d: decrypted data doesn't match original (len %d vs %d)", i, len(decrypted), len(plaintext))
		}
	}
}

func TestEncryptDecrypt_DifferentCiphertextsPerCall(t *testing.T) {
	// Due to random salt and nonce, same plaintext should produce different ciphertexts.
	plaintext := []byte("same input data")
	passphrase := "same passphrase"

	ct1, err := encryptData(plaintext, passphrase)
	if err != nil {
		t.Fatal(err)
		return
	}
	ct2, err := encryptData(plaintext, passphrase)
	if err != nil {
		t.Fatal(err)
		return
	}

	if string(ct1) == string(ct2) {
		t.Error("two encryptions of same data should produce different ciphertexts")
	}
}

// containsBytes checks if haystack contains needle.
func containsBytes(haystack, needle []byte) bool {
	for i := 0; i <= len(haystack)-len(needle); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
