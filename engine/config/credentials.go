package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/abietic/yhc/engine/model"
	"golang.org/x/crypto/pbkdf2"
)

// CredentialStore provides encrypted storage for provider API keys.
// Keys are encrypted with AES-256-GCM using a key derived from a passphrase
// via PBKDF2. The encrypted data is stored in ~/.claude/credentials.enc.
//
// If the encrypted store is unavailable (missing passphrase, corrupt file),
// the system falls back to environment variable resolution.
type CredentialStore struct {
	mu         sync.RWMutex
	storePath  string
	passphrase string
	entries    map[model.ProviderID]CredentialEntry
	loaded     bool
}

// CredentialEntry holds a single provider credential.
type CredentialEntry struct {
	// ProviderID identifies the provider this credential belongs to.
	ProviderID model.ProviderID `json:"provider_id"`
	// APIKey is the API key for this provider.
	APIKey string `json:"api_key"`
	// BaseURL is an optional custom base URL override.
	BaseURL string `json:"base_url,omitempty"`
}

// credentialFileData is the plaintext JSON structure that gets encrypted.
type credentialFileData struct {
	Version int                                  `json:"version"`
	Entries map[model.ProviderID]CredentialEntry `json:"entries"`
}

// PBKDF2 parameters for key derivation.
const (
	pbkdf2Iterations = 100000
	pbkdf2KeyLen     = 32 // AES-256
	saltSize         = 16
)

// credentialFileHeader: salt (16 bytes) || nonce (12 bytes) || ciphertext
const nonceSize = 12

// DefaultCredentialStorePath returns the default path for encrypted credentials.
func DefaultCredentialStorePath() string {
	return filepath.Join(UserConfigDir(), "credentials.enc")
}

// NewCredentialStore creates a credential store with the given passphrase.
// If passphrase is empty, the store will operate in read-only fallback mode
// (only environment variables will be used).
func NewCredentialStore(passphrase string) *CredentialStore {
	return &CredentialStore{
		storePath:  DefaultCredentialStorePath(),
		passphrase: passphrase,
		entries:    make(map[model.ProviderID]CredentialEntry),
	}
}

// NewCredentialStoreWithPath creates a credential store at a specific path.
func NewCredentialStoreWithPath(storePath, passphrase string) *CredentialStore {
	return &CredentialStore{
		storePath:  storePath,
		passphrase: passphrase,
		entries:    make(map[model.ProviderID]CredentialEntry),
	}
}

// Load reads and decrypts the credential store from disk.
// Returns nil if the file doesn't exist (empty store).
// Returns an error if the file exists but can't be decrypted.
func (cs *CredentialStore) Load() error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	data, err := os.ReadFile(cs.storePath)
	if err != nil {
		if os.IsNotExist(err) {
			cs.loaded = true
			return nil
		}
		return fmt.Errorf("reading credential store: %w", err)
	}

	if len(data) < saltSize+nonceSize+1 {
		return errors.New("credential store file is too short (corrupt)")
	}

	plaintext, err := decryptData(data, cs.passphrase)
	if err != nil {
		return fmt.Errorf("decrypting credential store: %w", err)
	}

	var fileData credentialFileData
	if err := json.Unmarshal(plaintext, &fileData); err != nil {
		return fmt.Errorf("parsing credential store: %w", err)
	}

	if fileData.Entries != nil {
		cs.entries = fileData.Entries
	} else {
		cs.entries = make(map[model.ProviderID]CredentialEntry)
	}
	cs.loaded = true
	return nil
}

// Save encrypts and writes the credential store to disk.
func (cs *CredentialStore) Save() error {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	if cs.passphrase == "" {
		return errors.New("cannot save credentials: no passphrase set")
	}

	fileData := credentialFileData{
		Version: 1,
		Entries: cs.entries,
	}

	plaintext, err := json.Marshal(fileData)
	if err != nil {
		return fmt.Errorf("marshaling credential store: %w", err)
	}

	ciphertext, err := encryptData(plaintext, cs.passphrase)
	if err != nil {
		return fmt.Errorf("encrypting credential store: %w", err)
	}

	// Ensure parent directory exists.
	dir := filepath.Dir(cs.storePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating credential store directory: %w", err)
	}

	// Write atomically via temp file.
	tmpPath := cs.storePath + ".tmp"
	if err := os.WriteFile(tmpPath, ciphertext, 0o600); err != nil {
		return fmt.Errorf("writing credential store: %w", err)
	}
	if err := os.Rename(tmpPath, cs.storePath); err != nil {
		_ = os.Remove(tmpPath) // best-effort cleanup
		return fmt.Errorf("renaming credential store: %w", err)
	}

	return nil
}

// Set stores a credential for the given provider.
func (cs *CredentialStore) Set(provider model.ProviderID, apiKey string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.entries[provider] = CredentialEntry{
		ProviderID: provider,
		APIKey:     apiKey,
	}
}

// SetWithBaseURL stores a credential with a custom base URL.
func (cs *CredentialStore) SetWithBaseURL(provider model.ProviderID, apiKey, baseURL string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.entries[provider] = CredentialEntry{
		ProviderID: provider,
		APIKey:     apiKey,
		BaseURL:    baseURL,
	}
}

// Get retrieves a credential for the given provider.
// Returns the entry and true if found, zero value and false otherwise.
func (cs *CredentialStore) Get(provider model.ProviderID) (CredentialEntry, bool) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	entry, ok := cs.entries[provider]
	return entry, ok
}

// Delete removes a credential for the given provider.
func (cs *CredentialStore) Delete(provider model.ProviderID) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	delete(cs.entries, provider)
}

// Providers returns a list of providers that have stored credentials.
func (cs *CredentialStore) Providers() []model.ProviderID {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	result := make([]model.ProviderID, 0, len(cs.entries))
	for p := range cs.entries {
		result = append(result, p)
	}
	return result
}

// IsLoaded returns whether the store has been loaded from disk.
func (cs *CredentialStore) IsLoaded() bool {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.loaded
}

// ResolveAPIKey returns the API key for a provider, checking the credential store
// first, then falling back to environment variables.
func (cs *CredentialStore) ResolveAPIKey(provider model.ProviderID) string {
	// Try encrypted store first.
	if entry, ok := cs.Get(provider); ok && entry.APIKey != "" {
		return entry.APIKey
	}

	// Fall back to environment variables.
	envCfg := model.GetProviderEnvConfig(provider)
	if envCfg == nil {
		return os.Getenv("PROV_API_KEY")
	}
	for _, envVar := range envCfg.APIKeyEnvVars {
		if v := os.Getenv(envVar); v != "" {
			return v
		}
	}
	return ""
}

// MaskedAPIKey returns a masked version of the API key for display.
// Shows only the last 4 characters.
func MaskedAPIKey(key string) string {
	if key == "" {
		return "(not set)"
	}
	if len(key) <= 4 {
		return "****"
	}
	return "..." + key[len(key)-4:]
}

// ---------------------------------------------------------------------------
// Encryption helpers (AES-256-GCM with PBKDF2 key derivation)
// ---------------------------------------------------------------------------

// deriveKey uses PBKDF2-SHA256 to derive an AES-256 key from a passphrase and salt.
func deriveKey(passphrase string, salt []byte) []byte {
	return pbkdf2.Key([]byte(passphrase), salt, pbkdf2Iterations, pbkdf2KeyLen, sha256.New)
}

// encryptData encrypts plaintext with AES-256-GCM using a PBKDF2-derived key.
// Output format: salt (16 bytes) || nonce (12 bytes) || ciphertext+tag
func encryptData(plaintext []byte, passphrase string) ([]byte, error) {
	// Generate random salt.
	salt := make([]byte, saltSize)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("generating salt: %w", err)
	}

	// Derive encryption key.
	key := deriveKey(passphrase, salt)

	// Create AES cipher.
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("creating cipher: %w", err)
	}

	// Create GCM mode.
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating GCM: %w", err)
	}

	// Generate random nonce.
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generating nonce: %w", err)
	}

	// Encrypt.
	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)

	// Assemble output: salt || nonce || ciphertext
	result := make([]byte, 0, saltSize+len(nonce)+len(ciphertext))
	result = append(result, salt...)
	result = append(result, nonce...)
	result = append(result, ciphertext...)

	return result, nil
}

// decryptData decrypts data encrypted by encryptData.
func decryptData(data []byte, passphrase string) ([]byte, error) {
	if len(data) < saltSize+nonceSize {
		return nil, errors.New("ciphertext too short")
	}

	salt := data[:saltSize]
	nonce := data[saltSize : saltSize+nonceSize]
	ciphertext := data[saltSize+nonceSize:]

	// Derive key.
	key := deriveKey(passphrase, salt)

	// Create cipher.
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("creating cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating GCM: %w", err)
	}

	// Decrypt.
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decryption failed (wrong passphrase?): %w", err)
	}

	return plaintext, nil
}
