package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// OAuthConfig holds OAuth 2.0 configuration for an MCP server.
// This is specified per-server in the MCP configuration.
type OAuthConfig struct {
	// ClientID is the OAuth client identifier.
	ClientID string `json:"client_id"`
	// ClientSecret is the OAuth client secret (optional for public clients using PKCE).
	ClientSecret string `json:"client_secret,omitempty"`
	// AuthURL is the authorization endpoint URL.
	AuthURL string `json:"auth_url"`
	// TokenURL is the token endpoint URL.
	TokenURL string `json:"token_url"`
	// Scopes are the OAuth scopes to request.
	Scopes []string `json:"scopes,omitempty"`
	// RedirectURL overrides the default localhost callback URL (optional).
	RedirectURL string `json:"redirect_url,omitempty"`
}

// OAuthToken represents a stored OAuth 2.0 token set.
type OAuthToken struct {
	// AccessToken is the current access token.
	AccessToken string `json:"access_token"`
	// RefreshToken is the refresh token for obtaining new access tokens.
	RefreshToken string `json:"refresh_token,omitempty"`
	// ExpiresAt is the Unix timestamp (seconds) when the access token expires.
	ExpiresAt int64 `json:"expires_at"`
	// Scopes are the granted scopes.
	Scopes []string `json:"scopes,omitempty"`
	// TokenType is the token type (typically "Bearer").
	TokenType string `json:"token_type"`
}

// IsExpired returns true if the access token has expired or will expire
// within the given grace period.
func (t *OAuthToken) IsExpired(grace time.Duration) bool {
	if t == nil || t.AccessToken == "" {
		return true
	}
	return time.Now().Unix() >= t.ExpiresAt-int64(grace.Seconds())
}

// tokenStoreFile is the storage structure persisted to disk.
type tokenStoreFile struct {
	// Tokens maps server keys to their token data.
	Tokens map[string]*OAuthToken `json:"tokens"`
}

// TokenStore manages OAuth token persistence and retrieval.
// Tokens are stored on disk with restricted file permissions (0600)
// and keyed by server URL for multi-server support.
type TokenStore struct {
	mu       sync.RWMutex
	filePath string
}

// NewTokenStore creates a TokenStore that persists tokens to the given file path.
// The parent directory is created if it does not exist.
func NewTokenStore(filePath string) *TokenStore {
	return &TokenStore{
		filePath: filePath,
	}
}

// DefaultTokenStorePath returns the default path for the OAuth token store.
// It is located at ~/.claude/mcp_oauth_tokens.json.
func DefaultTokenStorePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".claude", "mcp_oauth_tokens.json")
}

// Save persists a token for the given server key.
func (s *TokenStore) Save(serverKey string, token *OAuthToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	store, err := s.readLocked()
	if err != nil {
		return err
	}

	store.Tokens[serverKey] = token
	return s.writeLocked(store)
}

// Load retrieves the stored token for the given server key.
// Returns nil if no token is stored for that key.
func (s *TokenStore) Load(serverKey string) (*OAuthToken, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	store, err := s.readLocked()
	if err != nil {
		return nil, err
	}

	return store.Tokens[serverKey], nil
}

// Delete removes the stored token for the given server key.
func (s *TokenStore) Delete(serverKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	store, err := s.readLocked()
	if err != nil {
		return err
	}

	delete(store.Tokens, serverKey)
	return s.writeLocked(store)
}

// List returns all server keys that have stored tokens.
func (s *TokenStore) List() ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	store, err := s.readLocked()
	if err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(store.Tokens))
	for k := range store.Tokens {
		keys = append(keys, k)
	}
	return keys, nil
}

// readLocked reads the store file. Caller must hold at least s.mu.RLock().
func (s *TokenStore) readLocked() (*tokenStoreFile, error) {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return &tokenStoreFile{Tokens: make(map[string]*OAuthToken)}, nil
		}
		return nil, fmt.Errorf("oauth: failed to read token store: %w", err)
	}

	var store tokenStoreFile
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, fmt.Errorf("oauth: failed to parse token store: %w", err)
	}
	if store.Tokens == nil {
		store.Tokens = make(map[string]*OAuthToken)
	}
	return &store, nil
}

// writeLocked writes the store file with restricted permissions.
// Caller must hold s.mu.Lock().
func (s *TokenStore) writeLocked(store *tokenStoreFile) error {
	// Ensure parent directory exists.
	dir := filepath.Dir(s.filePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("oauth: failed to create token store directory: %w", err)
	}

	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return fmt.Errorf("oauth: failed to marshal token store: %w", err)
	}

	// Write with restricted permissions (0600 - owner read/write only).
	if err := os.WriteFile(s.filePath, data, 0o600); err != nil {
		return fmt.Errorf("oauth: failed to write token store: %w", err)
	}
	return nil
}

// OAuthManager coordinates OAuth token lifecycle for MCP servers.
// It handles token refresh (with deduplication of concurrent requests)
// and token injection into HTTP requests.
type OAuthManager struct {
	store *TokenStore

	// refreshMu serializes refresh operations per server to prevent
	// concurrent refresh token usage (which may invalidate the token).
	refreshMu sync.Mutex
	// refreshing tracks in-flight refresh operations per server key.
	refreshing map[string]chan struct{}
}

// NewOAuthManager creates a new OAuthManager with the given token store.
func NewOAuthManager(store *TokenStore) *OAuthManager {
	return &OAuthManager{
		store:      store,
		refreshing: make(map[string]chan struct{}),
	}
}

// GetValidToken returns a valid (non-expired) token for the given server.
// If the token is expired and a refresh token is available, it attempts
// to refresh the token using the provided OAuth config.
// Returns nil if no token is available and authentication is needed.
func (m *OAuthManager) GetValidToken(ctx context.Context, serverKey string, cfg *OAuthConfig) (*OAuthToken, error) {
	token, err := m.store.Load(serverKey)
	if err != nil {
		return nil, err
	}
	if token == nil {
		return nil, nil
	}

	// Token is still valid (with 5-minute grace period for proactive refresh).
	if !token.IsExpired(5 * time.Minute) {
		return token, nil
	}

	// Token expired but we have a refresh token - attempt refresh.
	if token.RefreshToken != "" && cfg != nil {
		refreshed, err := m.refreshToken(ctx, serverKey, token, cfg)
		if err != nil {
			// Refresh failed - return the expired token and let the caller
			// handle the 401.
			return token, nil
		}
		return refreshed, nil
	}

	// No refresh token available - return expired token.
	return token, nil
}

// refreshToken performs token refresh with deduplication.
// Only one refresh operation per server key can be in-flight at a time.
func (m *OAuthManager) refreshToken(ctx context.Context, serverKey string, token *OAuthToken, cfg *OAuthConfig) (*OAuthToken, error) {
	m.refreshMu.Lock()

	// Check if a refresh is already in-flight for this server.
	if ch, ok := m.refreshing[serverKey]; ok {
		m.refreshMu.Unlock()
		// Wait for the in-flight refresh to complete.
		select {
		case <-ch:
			// Refresh completed, load the updated token.
			return m.store.Load(serverKey)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	// Start a new refresh operation.
	done := make(chan struct{})
	m.refreshing[serverKey] = done
	m.refreshMu.Unlock()

	defer func() {
		m.refreshMu.Lock()
		delete(m.refreshing, serverKey)
		close(done)
		m.refreshMu.Unlock()
	}()

	// Perform the actual token refresh.
	newToken, err := doTokenRefresh(ctx, token.RefreshToken, cfg)
	if err != nil {
		return nil, err
	}

	// Persist the refreshed token. Preserve the refresh token if the server
	// did not issue a new one (as per OAuth 2.0 spec).
	if newToken.RefreshToken == "" {
		newToken.RefreshToken = token.RefreshToken
	}

	if err := m.store.Save(serverKey, newToken); err != nil {
		return nil, err
	}

	return newToken, nil
}

// doTokenRefresh exchanges a refresh token for a new access token.
func doTokenRefresh(ctx context.Context, refreshToken string, cfg *OAuthConfig) (*OAuthToken, error) {
	if cfg.TokenURL == "" {
		return nil, fmt.Errorf("oauth: token URL is required for refresh")
	}

	params := "grant_type=refresh_token" +
		"&refresh_token=" + urlEncode(refreshToken) +
		"&client_id=" + urlEncode(cfg.ClientID)

	if cfg.ClientSecret != "" {
		params += "&client_secret=" + urlEncode(cfg.ClientSecret)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.TokenURL, stringReader(params))
	if err != nil {
		return nil, fmt.Errorf("oauth: failed to create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oauth: refresh request failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oauth: refresh failed with status %d", resp.StatusCode)
	}

	return parseTokenResponse(resp)
}

// parseTokenResponse parses a standard OAuth 2.0 token response.
func parseTokenResponse(resp *http.Response) (*OAuthToken, error) {
	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		TokenType    string `json:"token_type"`
		Scope        string `json:"scope"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("oauth: failed to parse token response: %w", err)
	}

	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("oauth: server returned empty access token")
	}

	expiresIn := tokenResp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600 // Default to 1 hour.
	}

	token := &OAuthToken{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ExpiresAt:    time.Now().Unix() + expiresIn,
		TokenType:    tokenResp.TokenType,
	}
	if token.TokenType == "" {
		token.TokenType = "Bearer"
	}
	if tokenResp.Scope != "" {
		token.Scopes = splitScopes(tokenResp.Scope)
	}

	return token, nil
}

// OAuthTransport wraps an http.RoundTripper to inject OAuth Bearer tokens
// into HTTP requests. It handles automatic token refresh on 401 responses.
type OAuthTransport struct {
	// Base is the underlying transport. If nil, http.DefaultTransport is used.
	Base http.RoundTripper
	// Manager is the OAuth manager for token operations.
	Manager *OAuthManager
	// ServerKey identifies which server's token to use.
	ServerKey string
	// Config is the OAuth configuration for this server.
	Config *OAuthConfig
}

// RoundTrip implements http.RoundTripper. It injects the Bearer token
// and handles 401 responses by refreshing the token and retrying once.
func (t *OAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}

	// Get a valid token.
	token, err := t.Manager.GetValidToken(req.Context(), t.ServerKey, t.Config)
	if err != nil {
		return nil, fmt.Errorf("oauth: failed to get token: %w", err)
	}

	// If we have a token, inject it.
	if token != nil && token.AccessToken != "" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	}

	// Execute the request.
	resp, err := base.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	// Handle 401 by attempting token refresh and retrying.
	if resp.StatusCode == http.StatusUnauthorized && token != nil && token.RefreshToken != "" {
		_ = resp.Body.Close()

		// Force a refresh.
		refreshed, err := t.Manager.refreshToken(req.Context(), t.ServerKey, token, t.Config)
		if err != nil {
			// Refresh failed, return the 401.
			return base.RoundTrip(req)
		}

		// Retry with the new token.
		retryReq := req.Clone(req.Context())
		retryReq.Header.Set("Authorization", "Bearer "+refreshed.AccessToken)
		return base.RoundTrip(retryReq)
	}

	return resp, nil
}

// NewOAuthHTTPClient creates an HTTP client that automatically injects
// OAuth Bearer tokens and handles token refresh.
func NewOAuthHTTPClient(manager *OAuthManager, serverKey string, cfg *OAuthConfig) *http.Client {
	return &http.Client{
		Transport: &OAuthTransport{
			Base:      http.DefaultTransport,
			Manager:   manager,
			ServerKey: serverKey,
			Config:    cfg,
		},
	}
}

// splitScopes splits a space-separated scope string into individual scopes.
func splitScopes(scope string) []string {
	if scope == "" {
		return nil
	}
	var scopes []string
	start := 0
	for i := 0; i <= len(scope); i++ {
		if i == len(scope) || scope[i] == ' ' {
			if i > start {
				scopes = append(scopes, scope[start:i])
			}
			start = i + 1
		}
	}
	return scopes
}
