// Package auth provides credential management for API keys, OAuth tokens,
// and MCP server authentication. It supports persistence to disk with
// restrictive file permissions and environment-variable-based resolution.
package auth

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Credential represents a single authentication credential for an API provider
// or MCP server.
type Credential struct {
	Provider       string            `json:"provider"` // "anthropic", "openai", "mcp_server", etc.
	Key            string            `json:"key"`
	ExpiresAt      time.Time         `json:"expires_at,omitempty"`
	Scopes         []string          `json:"scopes,omitempty"`
	Meta           map[string]string `json:"meta,omitempty"`
	OriginID       string            `json:"origin_id,omitempty"`
	OriginRevision uint64            `json:"origin_revision,omitempty"`
}

// ResolvedNamedCredential is the immediate secret plus an optional opaque,
// rotation-sensitive local origin. Empty OriginID keeps ordinary model use
// available but disables private provider continuation.
type ResolvedNamedCredential struct {
	Secret   string
	OriginID string
}

// CredentialStore provides thread-safe storage and retrieval of credentials,
// backed by a JSON file on disk.
type CredentialStore struct {
	mu          sync.RWMutex
	credentials map[string]*Credential
	filePath    string
}

// NewCredentialStore creates a new CredentialStore that persists to filePath.
// The store is initially empty; call Load to read existing credentials from disk.
func NewCredentialStore(filePath string) *CredentialStore {
	return &CredentialStore{
		credentials: make(map[string]*Credential),
		filePath:    filePath,
	}
}

// Load reads credentials from disk. If the file does not exist, the store
// remains empty and no error is returned.
func (s *CredentialStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("auth: read credentials file: %w", err)
	}

	var creds map[string]*Credential
	if err := json.Unmarshal(data, &creds); err != nil {
		return fmt.Errorf("auth: parse credentials file: %w", err)
	}

	s.credentials = creds
	if s.credentials == nil {
		s.credentials = make(map[string]*Credential)
	}
	return nil
}

// Save persists credentials to disk with restrictive permissions (0600).
// The parent directory is created with mode 0700 if it does not exist.
func (s *CredentialStore) Save() error {
	s.mu.RLock()
	data, err := json.MarshalIndent(s.credentials, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("auth: marshal credentials: %w", err)
	}

	dir := filepath.Dir(s.filePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("auth: create credentials directory: %w", err)
	}

	if err := os.WriteFile(s.filePath, data, 0o600); err != nil {
		return fmt.Errorf("auth: write credentials file: %w", err)
	}
	return nil
}

// Get returns a credential by provider name. Returns nil if not found.
func (s *CredentialStore) Get(provider string) *Credential {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneCredential(s.credentials[provider])
}

// Set stores a credential. The provider field of the credential is used as the key.
func (s *CredentialStore) Set(cred *Credential) error {
	if cred == nil {
		return fmt.Errorf("auth: credential must not be nil")
	}
	if cred.Provider == "" {
		return fmt.Errorf("auth: credential provider must not be empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	stored := cloneCredential(cred)
	previous := s.credentials[cred.Provider]
	if previous != nil && previous.Key == cred.Key &&
		previous.OriginID != "" && previous.OriginRevision > 0 {
		stored.OriginID = previous.OriginID
		stored.OriginRevision = previous.OriginRevision
	} else {
		originID, err := newCredentialOriginID()
		if err != nil {
			return err
		}
		stored.OriginID = originID
		stored.OriginRevision = 1
		if previous != nil && previous.OriginRevision > 0 {
			stored.OriginRevision = previous.OriginRevision + 1
		}
	}
	s.credentials[cred.Provider] = stored
	return nil
}

func cloneCredential(credential *Credential) *Credential {
	if credential == nil {
		return nil
	}
	cloned := *credential
	cloned.Scopes = append([]string(nil), credential.Scopes...)
	if credential.Meta != nil {
		cloned.Meta = make(map[string]string, len(credential.Meta))
		for key, value := range credential.Meta {
			cloned.Meta[key] = value
		}
	}
	return &cloned
}

func newCredentialOriginID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("auth: generate credential origin: %w", err)
	}
	return fmt.Sprintf("local-%x", value[:]), nil
}

// Delete removes a credential by provider name.
func (s *CredentialStore) Delete(provider string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.credentials, provider)
}

// List returns all stored provider names in sorted order.
func (s *CredentialStore) List() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	names := make([]string, 0, len(s.credentials))
	for name := range s.credentials {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// IsExpired checks if a credential has expired. Returns false if the provider
// is not found or if the credential has no expiration set.
func (s *CredentialStore) IsExpired(provider string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cred, ok := s.credentials[provider]
	if !ok {
		return false
	}
	if cred.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().After(cred.ExpiresAt)
}

// ---------- API Key Resolution ----------

// ResolveAPIKey finds the API key using priority:
//  1. ANTHROPIC_API_KEY environment variable
//  2. Credential store (provider "anthropic")
//  3. Config file (~/.claude/credentials.json)
func ResolveAPIKey() (string, error) {
	// Priority 1: Environment variable.
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		return key, nil
	}

	// Priority 2 & 3: Credential store (which loads from the config file).
	store := NewCredentialStore(DefaultCredentialPath())
	if err := store.Load(); err != nil {
		return "", fmt.Errorf("auth: resolve API key: %w", err)
	}

	cred := store.Get("anthropic")
	if cred != nil && cred.Key != "" {
		return cred.Key, nil
	}

	return "", fmt.Errorf("auth: no API key found; set ANTHROPIC_API_KEY or add credentials to %s", DefaultCredentialPath())
}

// ResolveModelAPIKey resolves the API key for a specific model/provider.
// It checks provider-specific environment variables first, then falls back
// to the credential store.
func ResolveModelAPIKey(model string) (string, error) {
	provider := DetectProvider(model)

	// Check provider-specific environment variables.
	envKeys := providerEnvKeys(provider)
	for _, envKey := range envKeys {
		if key := os.Getenv(envKey); key != "" {
			return key, nil
		}
	}

	// Fall back to credential store.
	store := NewCredentialStore(DefaultCredentialPath())
	if err := store.Load(); err != nil {
		return "", fmt.Errorf("auth: resolve model API key: %w", err)
	}

	cred := store.Get(provider)
	if cred != nil && cred.Key != "" {
		return cred.Key, nil
	}

	return "", fmt.Errorf("auth: no API key found for provider %q (model %q); set %s or add credentials to %s",
		provider, model, strings.Join(envKeys, " or "), DefaultCredentialPath())
}

// ResolveNamedCredential resolves an exact, user-owned credential reference.
// The returned secret is intended only for immediate client construction and
// must not be retained in runtime snapshots, diagnostics, or errors.
func ResolveNamedCredential(name string) (string, error) {
	resolved, err := ResolveNamedCredentialOrigin(name)
	return resolved.Secret, err
}

// ResolveNamedCredentialOrigin resolves the exact named record and returns its
// persisted opaque rotation identity when the store owns one. Legacy records
// remain readable and return an empty OriginID.
func ResolveNamedCredentialOrigin(name string) (ResolvedNamedCredential, error) {
	store := NewCredentialStore(DefaultCredentialPath())
	if err := store.Load(); err != nil {
		return ResolvedNamedCredential{}, fmt.Errorf("auth: resolve named credential: %w", err)
	}
	return resolveNamedCredentialOrigin(store, name)
}

func resolveNamedCredential(store *CredentialStore, name string) (string, error) {
	resolved, err := resolveNamedCredentialOrigin(store, name)
	return resolved.Secret, err
}

func resolveNamedCredentialOrigin(
	store *CredentialStore,
	name string,
) (ResolvedNamedCredential, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ResolvedNamedCredential{}, fmt.Errorf("auth: named credential reference must not be empty")
	}

	cred := store.Get(name)
	if cred == nil || cred.Key == "" {
		return ResolvedNamedCredential{}, fmt.Errorf("auth: named credential %q is not configured", name)
	}
	if !cred.ExpiresAt.IsZero() && time.Now().After(cred.ExpiresAt) {
		return ResolvedNamedCredential{}, fmt.Errorf("auth: named credential %q has expired", name)
	}
	originID := ""
	if cred.OriginID != "" && cred.OriginRevision > 0 {
		originID = fmt.Sprintf("%s/r%d", cred.OriginID, cred.OriginRevision)
	}
	return ResolvedNamedCredential{Secret: cred.Key, OriginID: originID}, nil
}

// providerEnvKeys returns the environment variable names to check for a given provider.
func providerEnvKeys(provider string) []string {
	switch provider {
	case "anthropic":
		return []string{"ANTHROPIC_API_KEY"}
	case "openai":
		return []string{"OPENAI_API_KEY"}
	case "google":
		return []string{"GOOGLE_API_KEY", "GEMINI_API_KEY"}
	case "deepseek":
		return []string{"DEEPSEEK_API_KEY"}
	default:
		return []string{strings.ToUpper(provider) + "_API_KEY"}
	}
}

// ---------- Provider Detection ----------

// DetectProvider determines the API provider from a model name.
// Recognition rules:
//   - "claude-*" -> "anthropic"
//   - "gpt-*", "o1-*", "o3-*", "o4-*" -> "openai"
//   - "gemini-*" -> "google"
//   - "deepseek-*" -> "deepseek"
//   - default -> "anthropic"
func DetectProvider(model string) string {
	lower := strings.ToLower(model)

	switch {
	case strings.HasPrefix(lower, "claude-"):
		return "anthropic"
	case strings.HasPrefix(lower, "gpt-"),
		strings.HasPrefix(lower, "o1-"),
		strings.HasPrefix(lower, "o3-"),
		strings.HasPrefix(lower, "o4-"):
		return "openai"
	case strings.HasPrefix(lower, "gemini-"):
		return "google"
	case strings.HasPrefix(lower, "deepseek-"):
		return "deepseek"
	default:
		return "anthropic"
	}
}

// ---------- Default Paths ----------

// DefaultCredentialPath returns the default credential file path: ~/.claude/credentials.json
func DefaultCredentialPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".claude", "credentials.json")
}
