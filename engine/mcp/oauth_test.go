package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// =============================================================================
// TokenStore Tests
// =============================================================================

func TestTokenStore_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	store := NewTokenStore(filepath.Join(dir, "tokens.json"))

	token := &OAuthToken{
		AccessToken:  "access-123",
		RefreshToken: "refresh-456",
		ExpiresAt:    time.Now().Add(1 * time.Hour).Unix(),
		TokenType:    "Bearer",
		Scopes:       []string{"read", "write"},
	}

	// Save token.
	err := store.Save("server-a", token)
	if err != nil {
		t.Fatalf("Save failed: %v", err)
		return
	}

	// Load token.
	loaded, err := store.Load("server-a")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
		return
	}
	if loaded == nil {
		t.Fatal("expected non-nil token")
		return
	}
	if loaded.AccessToken != "access-123" {
		t.Fatalf("expected access token %q, got %q", "access-123", loaded.AccessToken)
	}
	if loaded.RefreshToken != "refresh-456" {
		t.Fatalf("expected refresh token %q, got %q", "refresh-456", loaded.RefreshToken)
	}
	if loaded.TokenType != "Bearer" {
		t.Fatalf("expected token type %q, got %q", "Bearer", loaded.TokenType)
	}
	if len(loaded.Scopes) != 2 || loaded.Scopes[0] != "read" || loaded.Scopes[1] != "write" {
		t.Fatalf("unexpected scopes: %v", loaded.Scopes)
	}
}

func TestTokenStore_LoadNonExistent(t *testing.T) {
	dir := t.TempDir()
	store := NewTokenStore(filepath.Join(dir, "tokens.json"))

	token, err := store.Load("nonexistent")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
		return
	}
	if token != nil {
		t.Fatal("expected nil token for nonexistent key")
		return
	}
}

func TestTokenStore_MultipleServers(t *testing.T) {
	dir := t.TempDir()
	store := NewTokenStore(filepath.Join(dir, "tokens.json"))

	token1 := &OAuthToken{AccessToken: "token-server-1", ExpiresAt: time.Now().Add(1 * time.Hour).Unix()}
	token2 := &OAuthToken{AccessToken: "token-server-2", ExpiresAt: time.Now().Add(2 * time.Hour).Unix()}

	_ = store.Save("server-1", token1)
	_ = store.Save("server-2", token2)

	loaded1, _ := store.Load("server-1")
	loaded2, _ := store.Load("server-2")

	if loaded1.AccessToken != "token-server-1" {
		t.Fatalf("expected server-1 token, got %q", loaded1.AccessToken)
	}
	if loaded2.AccessToken != "token-server-2" {
		t.Fatalf("expected server-2 token, got %q", loaded2.AccessToken)
	}
}

func TestTokenStore_Delete(t *testing.T) {
	dir := t.TempDir()
	store := NewTokenStore(filepath.Join(dir, "tokens.json"))

	_ = store.Save("to-delete", &OAuthToken{AccessToken: "temp"})

	err := store.Delete("to-delete")
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
		return
	}

	token, _ := store.Load("to-delete")
	if token != nil {
		t.Fatal("expected nil token after delete")
		return
	}
}

func TestTokenStore_List(t *testing.T) {
	dir := t.TempDir()
	store := NewTokenStore(filepath.Join(dir, "tokens.json"))

	_ = store.Save("alpha", &OAuthToken{AccessToken: "a"})
	_ = store.Save("beta", &OAuthToken{AccessToken: "b"})
	_ = store.Save("gamma", &OAuthToken{AccessToken: "c"})

	keys, err := store.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
		return
	}
	if len(keys) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(keys))
	}

	keySet := make(map[string]bool)
	for _, k := range keys {
		keySet[k] = true
	}
	for _, expected := range []string{"alpha", "beta", "gamma"} {
		if !keySet[expected] {
			t.Fatalf("expected key %q in list", expected)
		}
	}
}

func TestTokenStore_FilePermissions(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "secure", "tokens.json")
	store := NewTokenStore(filePath)

	_ = store.Save("test", &OAuthToken{AccessToken: "secret"})

	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
		return
	}

	perm := info.Mode().Perm()
	if perm != 0o600 {
		t.Fatalf("expected file permissions 0600, got %04o", perm)
	}
}

func TestTokenStore_CreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "deep", "nested", "dir", "tokens.json")
	store := NewTokenStore(filePath)

	err := store.Save("test", &OAuthToken{AccessToken: "val"})
	if err != nil {
		t.Fatalf("Save failed: %v", err)
		return
	}

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		t.Fatal("expected file to be created")
	}
}

func TestTokenStore_Overwrite(t *testing.T) {
	dir := t.TempDir()
	store := NewTokenStore(filepath.Join(dir, "tokens.json"))

	_ = store.Save("server", &OAuthToken{AccessToken: "old-token"})
	_ = store.Save("server", &OAuthToken{AccessToken: "new-token"})

	loaded, _ := store.Load("server")
	if loaded.AccessToken != "new-token" {
		t.Fatalf("expected overwritten token, got %q", loaded.AccessToken)
	}
}

func TestTokenStore_ConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	store := NewTokenStore(filepath.Join(dir, "tokens.json"))

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			key := fmt.Sprintf("server-%d", idx)
			_ = store.Save(key, &OAuthToken{AccessToken: fmt.Sprintf("token-%d", idx)})
			_, _ = store.Load(key)
		}(i)
	}
	wg.Wait()

	// Verify no corruption.
	keys, err := store.List()
	if err != nil {
		t.Fatalf("List after concurrent access failed: %v", err)
		return
	}
	if len(keys) != 20 {
		t.Fatalf("expected 20 keys after concurrent writes, got %d", len(keys))
	}
}

// =============================================================================
// OAuthToken Tests
// =============================================================================

func TestOAuthToken_IsExpired(t *testing.T) {
	tests := []struct {
		name     string
		token    *OAuthToken
		grace    time.Duration
		expected bool
	}{
		{
			name:     "nil token",
			token:    nil,
			grace:    0,
			expected: true,
		},
		{
			name:     "empty access token",
			token:    &OAuthToken{AccessToken: "", ExpiresAt: time.Now().Add(1 * time.Hour).Unix()},
			grace:    0,
			expected: true,
		},
		{
			name:     "expired token",
			token:    &OAuthToken{AccessToken: "tok", ExpiresAt: time.Now().Add(-10 * time.Minute).Unix()},
			grace:    0,
			expected: true,
		},
		{
			name:     "valid token",
			token:    &OAuthToken{AccessToken: "tok", ExpiresAt: time.Now().Add(1 * time.Hour).Unix()},
			grace:    0,
			expected: false,
		},
		{
			name:     "expires within grace period",
			token:    &OAuthToken{AccessToken: "tok", ExpiresAt: time.Now().Add(3 * time.Minute).Unix()},
			grace:    5 * time.Minute,
			expected: true,
		},
		{
			name:     "valid beyond grace period",
			token:    &OAuthToken{AccessToken: "tok", ExpiresAt: time.Now().Add(10 * time.Minute).Unix()},
			grace:    5 * time.Minute,
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.token.IsExpired(tc.grace)
			if result != tc.expected {
				t.Fatalf("expected IsExpired=%v, got %v", tc.expected, result)
			}
		})
	}
}

// =============================================================================
// PKCE Tests
// =============================================================================

func TestGeneratePKCE(t *testing.T) {
	pkce, err := GeneratePKCE()
	if err != nil {
		t.Fatalf("GeneratePKCE failed: %v", err)
		return
	}

	if pkce.CodeVerifier == "" {
		t.Fatal("expected non-empty code verifier")
	}
	if pkce.CodeChallenge == "" {
		t.Fatal("expected non-empty code challenge")
	}
	if pkce.Method != "S256" {
		t.Fatalf("expected method S256, got %q", pkce.Method)
	}

	// Verify code verifier length (32 bytes base64url = 43 chars).
	if len(pkce.CodeVerifier) != 43 {
		t.Fatalf("expected code verifier length 43, got %d", len(pkce.CodeVerifier))
	}

	// Verify code challenge length (32 bytes SHA256 base64url = 43 chars).
	if len(pkce.CodeChallenge) != 43 {
		t.Fatalf("expected code challenge length 43, got %d", len(pkce.CodeChallenge))
	}

	// Verify code challenge is correct S256 of code verifier.
	expectedChallenge := computeS256Challenge(pkce.CodeVerifier)
	if pkce.CodeChallenge != expectedChallenge {
		t.Fatalf("code challenge mismatch:\n  got:  %s\n  want: %s", pkce.CodeChallenge, expectedChallenge)
	}
}

func TestGeneratePKCE_Uniqueness(t *testing.T) {
	// Generate multiple PKCE pairs and ensure they are unique.
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		pkce, err := GeneratePKCE()
		if err != nil {
			t.Fatalf("GeneratePKCE failed: %v", err)
			return
		}
		if seen[pkce.CodeVerifier] {
			t.Fatal("generated duplicate code verifier")
		}
		seen[pkce.CodeVerifier] = true
	}
}

// computeS256Challenge computes the expected S256 code challenge for testing.
func computeS256Challenge(verifier string) string {
	hash := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

// =============================================================================
// Token Refresh Tests
// =============================================================================

func TestDoTokenRefresh_Success(t *testing.T) {
	// Set up a mock token endpoint.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Fatalf("expected form content type")
		}

		body, _ := io.ReadAll(r.Body)
		params, _ := parseFormBody(string(body))

		if params["grant_type"] != "refresh_token" {
			t.Fatalf("expected grant_type=refresh_token, got %q", params["grant_type"])
		}
		if params["refresh_token"] != "old-refresh-token" {
			t.Fatalf("expected refresh_token=%q, got %q", "old-refresh-token", params["refresh_token"])
		}
		if params["client_id"] != "my-client" {
			t.Fatalf("expected client_id=%q, got %q", "my-client", params["client_id"])
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access-token",
			"refresh_token": "new-refresh-token",
			"expires_in":    3600,
			"token_type":    "Bearer",
			"scope":         "read write",
		})
	}))
	defer server.Close()

	cfg := &OAuthConfig{
		ClientID: "my-client",
		TokenURL: server.URL,
	}

	token, err := doTokenRefresh(context.Background(), "old-refresh-token", cfg)
	if err != nil {
		t.Fatalf("doTokenRefresh failed: %v", err)
		return
	}

	if token.AccessToken != "new-access-token" {
		t.Fatalf("expected access token %q, got %q", "new-access-token", token.AccessToken)
	}
	if token.RefreshToken != "new-refresh-token" {
		t.Fatalf("expected refresh token %q, got %q", "new-refresh-token", token.RefreshToken)
	}
	if token.TokenType != "Bearer" {
		t.Fatalf("expected token type %q, got %q", "Bearer", token.TokenType)
	}
	if len(token.Scopes) != 2 || token.Scopes[0] != "read" || token.Scopes[1] != "write" {
		t.Fatalf("unexpected scopes: %v", token.Scopes)
	}
}

func TestDoTokenRefresh_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":             "invalid_grant",
			"error_description": "refresh token expired",
		})
	}))
	defer server.Close()

	cfg := &OAuthConfig{
		ClientID: "client",
		TokenURL: server.URL,
	}

	_, err := doTokenRefresh(context.Background(), "expired-refresh", cfg)
	if err == nil {
		t.Fatal("expected error for server error response")
		return
	}
	if !strings.Contains(err.Error(), "status 400") {
		t.Fatalf("expected status 400 error, got: %v", err)
	}
}

func TestDoTokenRefresh_WithClientSecret(t *testing.T) {
	var receivedSecret string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		params, _ := parseFormBody(string(body))
		receivedSecret = params["client_secret"]

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "tok",
			"expires_in":   3600,
		})
	}))
	defer server.Close()

	cfg := &OAuthConfig{
		ClientID:     "client",
		ClientSecret: "secret-value",
		TokenURL:     server.URL,
	}

	_, err := doTokenRefresh(context.Background(), "refresh", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if receivedSecret != "secret-value" {
		t.Fatalf("expected client_secret=%q, got %q", "secret-value", receivedSecret)
	}
}

func TestDoTokenRefresh_NoTokenURL(t *testing.T) {
	cfg := &OAuthConfig{ClientID: "client", TokenURL: ""}

	_, err := doTokenRefresh(context.Background(), "refresh", cfg)
	if err == nil {
		t.Fatal("expected error for empty token URL")
		return
	}
	if !strings.Contains(err.Error(), "token URL is required") {
		t.Fatalf("expected 'token URL is required' error, got: %v", err)
	}
}

func TestDoTokenRefresh_DefaultExpiresIn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// No expires_in field.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "tok",
		})
	}))
	defer server.Close()

	cfg := &OAuthConfig{ClientID: "client", TokenURL: server.URL}

	token, err := doTokenRefresh(context.Background(), "refresh", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}

	// Should default to 1 hour.
	expectedExpiry := time.Now().Unix() + 3600
	if token.ExpiresAt < expectedExpiry-5 || token.ExpiresAt > expectedExpiry+5 {
		t.Fatalf("expected expiry around %d, got %d", expectedExpiry, token.ExpiresAt)
	}
}

// =============================================================================
// OAuthManager Tests
// =============================================================================

func TestOAuthManager_GetValidToken_Fresh(t *testing.T) {
	dir := t.TempDir()
	store := NewTokenStore(filepath.Join(dir, "tokens.json"))
	manager := NewOAuthManager(store)

	// Save a fresh token.
	freshToken := &OAuthToken{
		AccessToken: "fresh-token",
		ExpiresAt:   time.Now().Add(1 * time.Hour).Unix(),
		TokenType:   "Bearer",
	}
	_ = store.Save("server-key", freshToken)

	token, err := manager.GetValidToken(context.Background(), "server-key", nil)
	if err != nil {
		t.Fatalf("GetValidToken failed: %v", err)
		return
	}
	if token.AccessToken != "fresh-token" {
		t.Fatalf("expected fresh token, got %q", token.AccessToken)
	}
}

func TestOAuthManager_GetValidToken_Expired_WithRefresh(t *testing.T) {
	// Mock token endpoint.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "refreshed-token",
			"refresh_token": "new-refresh",
			"expires_in":    3600,
		})
	}))
	defer server.Close()

	dir := t.TempDir()
	store := NewTokenStore(filepath.Join(dir, "tokens.json"))
	manager := NewOAuthManager(store)

	// Save an expired token with a refresh token.
	expiredToken := &OAuthToken{
		AccessToken:  "expired-token",
		RefreshToken: "my-refresh",
		ExpiresAt:    time.Now().Add(-10 * time.Minute).Unix(),
		TokenType:    "Bearer",
	}
	_ = store.Save("server-key", expiredToken)

	cfg := &OAuthConfig{
		ClientID: "client",
		TokenURL: server.URL,
	}

	token, err := manager.GetValidToken(context.Background(), "server-key", cfg)
	if err != nil {
		t.Fatalf("GetValidToken failed: %v", err)
		return
	}
	if token.AccessToken != "refreshed-token" {
		t.Fatalf("expected refreshed token, got %q", token.AccessToken)
	}
}

func TestOAuthManager_GetValidToken_NoToken(t *testing.T) {
	dir := t.TempDir()
	store := NewTokenStore(filepath.Join(dir, "tokens.json"))
	manager := NewOAuthManager(store)

	token, err := manager.GetValidToken(context.Background(), "unknown", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if token != nil {
		t.Fatal("expected nil token for unknown server")
		return
	}
}

func TestOAuthManager_ConcurrentRefresh(t *testing.T) {
	// The token server should only be called once even with concurrent requests.
	var callCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callCount, 1)
		// Simulate slow refresh.
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "shared-refresh-result",
			"expires_in":   3600,
		})
	}))
	defer server.Close()

	dir := t.TempDir()
	store := NewTokenStore(filepath.Join(dir, "tokens.json"))
	manager := NewOAuthManager(store)

	// Save an expired token.
	_ = store.Save("concurrent-key", &OAuthToken{
		AccessToken:  "expired",
		RefreshToken: "refresh-tok",
		ExpiresAt:    time.Now().Add(-10 * time.Minute).Unix(),
	})

	cfg := &OAuthConfig{ClientID: "client", TokenURL: server.URL}

	// Launch concurrent refresh requests.
	var wg sync.WaitGroup
	results := make([]*OAuthToken, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			tok, _ := manager.GetValidToken(context.Background(), "concurrent-key", cfg)
			results[idx] = tok
		}(i)
	}
	wg.Wait()

	// All should have gotten the same refreshed token.
	for i, tok := range results {
		if tok == nil || tok.AccessToken != "shared-refresh-result" {
			t.Fatalf("result[%d]: expected 'shared-refresh-result', got %v", i, tok)
			return
		}
	}

	// Token server should have been called exactly once.
	if count := atomic.LoadInt32(&callCount); count != 1 {
		t.Fatalf("expected 1 refresh call, got %d", count)
	}
}

// =============================================================================
// OAuthTransport Tests
// =============================================================================

func TestOAuthTransport_InjectsBearer(t *testing.T) {
	dir := t.TempDir()
	store := NewTokenStore(filepath.Join(dir, "tokens.json"))
	manager := NewOAuthManager(store)

	// Save a valid token.
	_ = store.Save("server-key", &OAuthToken{
		AccessToken: "my-bearer-token",
		ExpiresAt:   time.Now().Add(1 * time.Hour).Unix(),
		TokenType:   "Bearer",
	})

	// Target server that checks the Authorization header.
	var receivedAuth string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	client := &http.Client{
		Transport: &OAuthTransport{
			Base:      http.DefaultTransport,
			Manager:   manager,
			ServerKey: "server-key",
			Config:    &OAuthConfig{ClientID: "client"},
		},
	}

	resp, err := client.Get(target.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
		return
	}
	_ = resp.Body.Close()

	if receivedAuth != "Bearer my-bearer-token" {
		t.Fatalf("expected 'Bearer my-bearer-token', got %q", receivedAuth)
	}
}

func TestOAuthTransport_401TriggersRefresh(t *testing.T) {
	dir := t.TempDir()
	store := NewTokenStore(filepath.Join(dir, "tokens.json"))
	manager := NewOAuthManager(store)

	// Save a token that will get 401'd.
	_ = store.Save("server-key", &OAuthToken{
		AccessToken:  "old-token",
		RefreshToken: "my-refresh",
		ExpiresAt:    time.Now().Add(1 * time.Hour).Unix(),
		TokenType:    "Bearer",
	})

	// Token refresh server.
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "fresh-token",
			"expires_in":   3600,
		})
	}))
	defer tokenServer.Close()

	// Target server: returns 401 for old token, 200 for new token.
	var requestCount int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&requestCount, 1)
		auth := r.Header.Get("Authorization")
		if count == 1 && auth == "Bearer old-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if auth == "Bearer fresh-token" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("success"))
			return
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer target.Close()

	client := &http.Client{
		Transport: &OAuthTransport{
			Base:      http.DefaultTransport,
			Manager:   manager,
			ServerKey: "server-key",
			Config:    &OAuthConfig{ClientID: "client", TokenURL: tokenServer.URL},
		},
	}

	resp, err := client.Get(target.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
		return
	}
	defer resp.Body.Close() //nolint:errcheck

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "success" {
		t.Fatalf("expected 'success' after refresh, got %q (status %d)", body, resp.StatusCode)
	}
}

// =============================================================================
// Authorization Code Flow Tests
// =============================================================================

func TestAuthorizationCodeFlow_Success(t *testing.T) {
	// Mock token endpoint.
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		params, _ := parseFormBody(string(body))

		if params["grant_type"] != "authorization_code" {
			t.Errorf("expected grant_type=authorization_code, got %q", params["grant_type"])
		}
		if params["code"] == "" {
			t.Error("expected non-empty code")
		}
		if params["code_verifier"] == "" {
			t.Error("expected non-empty code_verifier")
		}
		if params["client_id"] != "test-client" {
			t.Errorf("expected client_id=test-client, got %q", params["client_id"])
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access",
			"refresh_token": "new-refresh",
			"expires_in":    7200,
			"token_type":    "Bearer",
		})
	}))
	defer tokenServer.Close()

	dir := t.TempDir()
	store := NewTokenStore(filepath.Join(dir, "tokens.json"))

	// We simulate the browser open by directly making the callback request.
	var authURLReceived string
	opts := AuthFlowOptions{
		Config: &OAuthConfig{
			ClientID: "test-client",
			AuthURL:  "https://auth.example.com/authorize",
			TokenURL: tokenServer.URL,
			Scopes:   []string{"openid", "profile"},
		},
		ServerKey: "test-server",
		Store:     store,
		Timeout:   5 * time.Second,
		OpenBrowser: func(url string) error {
			authURLReceived = url
			// Simulate the OAuth callback by extracting state from the auth URL
			// and making a callback request.
			go func() {
				time.Sleep(100 * time.Millisecond)
				simulateCallback(t, url)
			}()
			return nil
		},
	}

	result, err := AuthorizationCodeFlow(context.Background(), opts)
	if err != nil {
		t.Fatalf("AuthorizationCodeFlow failed: %v", err)
		return
	}

	if result == nil || result.Token == nil {
		t.Fatal("expected non-nil result with token")
		return
	}
	if result.Token.AccessToken != "new-access" {
		t.Fatalf("expected access token %q, got %q", "new-access", result.Token.AccessToken)
	}
	if result.Token.RefreshToken != "new-refresh" {
		t.Fatalf("expected refresh token %q, got %q", "new-refresh", result.Token.RefreshToken)
	}

	// Verify the auth URL was correctly constructed.
	if !strings.Contains(authURLReceived, "response_type=code") {
		t.Fatal("expected response_type=code in auth URL")
	}
	if !strings.Contains(authURLReceived, "client_id=test-client") {
		t.Fatal("expected client_id in auth URL")
	}
	if !strings.Contains(authURLReceived, "code_challenge_method=S256") {
		t.Fatal("expected code_challenge_method=S256 in auth URL")
	}
	if !strings.Contains(authURLReceived, "scope=openid+profile") {
		t.Fatal("expected scopes in auth URL")
	}

	// Verify token was persisted.
	persisted, _ := store.Load("test-server")
	if persisted == nil || persisted.AccessToken != "new-access" {
		t.Fatal("expected token to be persisted in store")
		return
	}
}

func TestAuthorizationCodeFlow_Timeout(t *testing.T) {
	opts := AuthFlowOptions{
		Config: &OAuthConfig{
			ClientID: "client",
			AuthURL:  "https://auth.example.com/authorize",
			TokenURL: "https://token.example.com/token",
		},
		Timeout: 200 * time.Millisecond,
		OpenBrowser: func(url string) error {
			// Don't simulate any callback - let it time out.
			return nil
		},
	}

	_, err := AuthorizationCodeFlow(context.Background(), opts)
	if err == nil {
		t.Fatal("expected timeout error")
		return
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got: %v", err)
	}
}

func TestAuthorizationCodeFlow_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	opts := AuthFlowOptions{
		Config: &OAuthConfig{
			ClientID: "client",
			AuthURL:  "https://auth.example.com/authorize",
			TokenURL: "https://token.example.com/token",
		},
		Timeout: 5 * time.Second,
		OpenBrowser: func(url string) error {
			// Cancel context after a short delay.
			go func() {
				time.Sleep(100 * time.Millisecond)
				cancel()
			}()
			return nil
		},
	}

	_, err := AuthorizationCodeFlow(ctx, opts)
	if err == nil {
		t.Fatal("expected context cancellation error")
		return
	}
}

func TestAuthorizationCodeFlow_MissingConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  *OAuthConfig
		err  string
	}{
		{"nil config", nil, "config is required"},
		{"no auth URL", &OAuthConfig{ClientID: "c", TokenURL: "t"}, "auth_url is required"},
		{"no token URL", &OAuthConfig{ClientID: "c", AuthURL: "a"}, "token_url is required"},
		{"no client ID", &OAuthConfig{AuthURL: "a", TokenURL: "t"}, "client_id is required"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := AuthorizationCodeFlow(context.Background(), AuthFlowOptions{Config: tc.cfg})
			if err == nil {
				t.Fatal("expected error")
				return
			}
			if !strings.Contains(err.Error(), tc.err) {
				t.Fatalf("expected error containing %q, got: %v", tc.err, err)
			}
		})
	}
}

// =============================================================================
// Callback Server Tests
// =============================================================================

func TestCallbackHandler_Success(t *testing.T) {
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	handler := &callbackHandler{
		expectedState: "my-state",
		codeCh:        codeCh,
		errCh:         errCh,
	}

	req := httptest.NewRequest(http.MethodGet, "/callback?code=auth-code-123&state=my-state", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Authorization Successful") {
		t.Fatal("expected success HTML response")
	}

	select {
	case code := <-codeCh:
		if code != "auth-code-123" {
			t.Fatalf("expected code %q, got %q", "auth-code-123", code)
		}
	default:
		t.Fatal("expected code on channel")
	}
}

func TestCallbackHandlerSignalsAfterResponseWrite(t *testing.T) {
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	releaseWrite := make(chan struct{})
	writeStarted := make(chan struct{}, 1)
	writer := &blockingCallbackResponseWriter{
		header:       make(http.Header),
		releaseWrite: releaseWrite,
		writeStarted: writeStarted,
	}
	handler := &callbackHandler{
		expectedState: "state",
		codeCh:        codeCh,
		errCh:         errCh,
	}
	request := httptest.NewRequest(
		http.MethodGet,
		"/callback?code=code&state=state",
		nil,
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(writer, request)
	}()

	<-writeStarted
	select {
	case code := <-codeCh:
		t.Fatalf("callback signaled %q before response write completed", code)
	default:
	}
	close(releaseWrite)
	<-done
	select {
	case code := <-codeCh:
		if code != "code" {
			t.Fatalf("callback code = %q, want code", code)
		}
	default:
		t.Fatal("callback did not signal after response write completed")
	}
}

type blockingCallbackResponseWriter struct {
	header       http.Header
	releaseWrite <-chan struct{}
	writeStarted chan<- struct{}
}

func (w *blockingCallbackResponseWriter) Header() http.Header {
	return w.header
}

func (*blockingCallbackResponseWriter) WriteHeader(int) {}

func (w *blockingCallbackResponseWriter) Write(data []byte) (int, error) {
	select {
	case w.writeStarted <- struct{}{}:
	default:
	}
	<-w.releaseWrite
	return len(data), nil
}

func TestCallbackHandler_StateMismatch(t *testing.T) {
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	handler := &callbackHandler{
		expectedState: "expected-state",
		codeCh:        codeCh,
		errCh:         errCh,
	}

	req := httptest.NewRequest(http.MethodGet, "/callback?code=code&state=wrong-state", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	select {
	case err := <-errCh:
		if !strings.Contains(err.Error(), "state mismatch") {
			t.Fatalf("expected state mismatch error, got: %v", err)
		}
	default:
		t.Fatal("expected error on channel")
	}
}

func TestCallbackHandler_ProviderError(t *testing.T) {
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	handler := &callbackHandler{
		expectedState: "state",
		codeCh:        codeCh,
		errCh:         errCh,
	}

	req := httptest.NewRequest(http.MethodGet, "/callback?error=access_denied&error_description=User+denied+access", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	select {
	case err := <-errCh:
		if !strings.Contains(err.Error(), "User denied access") {
			t.Fatalf("expected provider error description, got: %v", err)
		}
	default:
		t.Fatal("expected error on channel")
	}
}

func TestCallbackHandler_NoCode(t *testing.T) {
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	handler := &callbackHandler{
		expectedState: "state",
		codeCh:        codeCh,
		errCh:         errCh,
	}

	req := httptest.NewRequest(http.MethodGet, "/callback?state=state", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	select {
	case err := <-errCh:
		if !strings.Contains(err.Error(), "no authorization code") {
			t.Fatalf("expected 'no authorization code' error, got: %v", err)
		}
	default:
		t.Fatal("expected error on channel")
	}
}

func TestCallbackHandler_WrongPath(t *testing.T) {
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	handler := &callbackHandler{
		expectedState: "state",
		codeCh:        codeCh,
		errCh:         errCh,
	}

	req := httptest.NewRequest(http.MethodGet, "/wrong-path?code=code&state=state", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for wrong path, got %d", w.Code)
	}
}

func TestCallbackHandler_OnlyHandledOnce(t *testing.T) {
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	handler := &callbackHandler{
		expectedState: "state",
		codeCh:        codeCh,
		errCh:         errCh,
	}

	// First call should succeed.
	req1 := httptest.NewRequest(http.MethodGet, "/callback?code=first-code&state=state", nil)
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req1)

	// Second call should be a no-op (already handled).
	req2 := httptest.NewRequest(http.MethodGet, "/callback?code=second-code&state=state", nil)
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)

	select {
	case code := <-codeCh:
		if code != "first-code" {
			t.Fatalf("expected first code, got %q", code)
		}
	default:
		t.Fatal("expected code on channel")
	}
}

// =============================================================================
// Code Exchange Tests
// =============================================================================

func TestExchangeCodeForToken_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		params, _ := parseFormBody(string(body))

		if params["grant_type"] != "authorization_code" {
			t.Errorf("expected grant_type=authorization_code, got %q", params["grant_type"])
		}
		if params["code"] != "the-auth-code" {
			t.Errorf("expected code=%q, got %q", "the-auth-code", params["code"])
		}
		if params["redirect_uri"] != "http://localhost:12345/callback" {
			t.Errorf("expected redirect_uri=%q, got %q", "http://localhost:12345/callback", params["redirect_uri"])
		}
		if params["code_verifier"] != "my-verifier" {
			t.Errorf("expected code_verifier=%q, got %q", "my-verifier", params["code_verifier"])
		}
		if params["client_id"] != "test-client" {
			t.Errorf("expected client_id=%q, got %q", "test-client", params["client_id"])
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "exchanged-access",
			"refresh_token": "exchanged-refresh",
			"expires_in":    1800,
			"token_type":    "Bearer",
		})
	}))
	defer server.Close()

	cfg := &OAuthConfig{
		ClientID: "test-client",
		TokenURL: server.URL,
	}

	token, err := exchangeCodeForToken(context.Background(), "the-auth-code", "http://localhost:12345/callback", "my-verifier", cfg)
	if err != nil {
		t.Fatalf("exchangeCodeForToken failed: %v", err)
		return
	}

	if token.AccessToken != "exchanged-access" {
		t.Fatalf("expected access token %q, got %q", "exchanged-access", token.AccessToken)
	}
	if token.RefreshToken != "exchanged-refresh" {
		t.Fatalf("expected refresh token %q, got %q", "exchanged-refresh", token.RefreshToken)
	}
}

func TestExchangeCodeForToken_ErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":             "invalid_grant",
			"error_description": "code has expired",
		})
	}))
	defer server.Close()

	cfg := &OAuthConfig{
		ClientID: "client",
		TokenURL: server.URL,
	}

	_, err := exchangeCodeForToken(context.Background(), "expired-code", "http://localhost/cb", "verifier", cfg)
	if err == nil {
		t.Fatal("expected error")
		return
	}
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("expected error with 'invalid_grant', got: %v", err)
	}
}

// =============================================================================
// Build Authorization URL Tests
// =============================================================================

func TestBuildAuthorizationURL(t *testing.T) {
	cfg := &OAuthConfig{
		ClientID: "my-client",
		AuthURL:  "https://auth.example.com/authorize",
		Scopes:   []string{"openid", "email", "profile"},
	}

	pkce := &PKCEChallenge{
		CodeChallenge: "test-challenge",
		Method:        "S256",
	}

	authURL, err := buildAuthorizationURL(cfg, "http://localhost:8080/callback", "my-state", pkce)
	if err != nil {
		t.Fatalf("buildAuthorizationURL failed: %v", err)
		return
	}

	if !strings.HasPrefix(authURL, "https://auth.example.com/authorize?") {
		t.Fatalf("unexpected URL prefix: %s", authURL)
	}
	if !strings.Contains(authURL, "response_type=code") {
		t.Fatal("missing response_type=code")
	}
	if !strings.Contains(authURL, "client_id=my-client") {
		t.Fatal("missing client_id")
	}
	if !strings.Contains(authURL, "state=my-state") {
		t.Fatal("missing state")
	}
	if !strings.Contains(authURL, "code_challenge=test-challenge") {
		t.Fatal("missing code_challenge")
	}
	if !strings.Contains(authURL, "code_challenge_method=S256") {
		t.Fatal("missing code_challenge_method")
	}
	if !strings.Contains(authURL, "redirect_uri=") {
		t.Fatal("missing redirect_uri")
	}
	if !strings.Contains(authURL, "scope=openid+email+profile") {
		t.Fatal("missing scope")
	}
}

func TestBuildAuthorizationURL_NoScopes(t *testing.T) {
	cfg := &OAuthConfig{
		ClientID: "client",
		AuthURL:  "https://auth.example.com/authorize",
	}

	pkce := &PKCEChallenge{CodeChallenge: "challenge", Method: "S256"}

	authURL, err := buildAuthorizationURL(cfg, "http://localhost/cb", "state", pkce)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if strings.Contains(authURL, "scope=") {
		t.Fatal("should not include scope when none specified")
	}
}

// =============================================================================
// Helper: splitScopes
// =============================================================================

func TestSplitScopes(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"", nil},
		{"read", []string{"read"}},
		{"read write", []string{"read", "write"}},
		{"openid email profile", []string{"openid", "email", "profile"}},
	}

	for _, tc := range tests {
		result := splitScopes(tc.input)
		if len(result) != len(tc.expected) {
			t.Fatalf("splitScopes(%q): expected %v, got %v", tc.input, tc.expected, result)
		}
		for i := range result {
			if result[i] != tc.expected[i] {
				t.Fatalf("splitScopes(%q)[%d]: expected %q, got %q", tc.input, i, tc.expected[i], result[i])
			}
		}
	}
}

// =============================================================================
// sanitizeErrorBody Tests
// =============================================================================

func TestSanitizeErrorBody_ValidJSON(t *testing.T) {
	body := []byte(`{"error":"invalid_grant","error_description":"token expired"}`)
	result := sanitizeErrorBody(body)
	if result != "invalid_grant: token expired" {
		t.Fatalf("expected 'invalid_grant: token expired', got %q", result)
	}
}

func TestSanitizeErrorBody_ErrorOnly(t *testing.T) {
	body := []byte(`{"error":"server_error"}`)
	result := sanitizeErrorBody(body)
	if result != "server_error" {
		t.Fatalf("expected 'server_error', got %q", result)
	}
}

func TestSanitizeErrorBody_InvalidJSON(t *testing.T) {
	body := []byte(`not json at all`)
	result := sanitizeErrorBody(body)
	if result != "(unparseable error response)" {
		t.Fatalf("expected unparseable message, got %q", result)
	}
}

func TestSanitizeErrorBody_NoErrorField(t *testing.T) {
	body := []byte(`{"message":"internal error","code":500}`)
	result := sanitizeErrorBody(body)
	if result != "(unparseable error response)" {
		t.Fatalf("expected unparseable message for no error field, got %q", result)
	}
}

// =============================================================================
// NewOAuthHTTPClient Tests
// =============================================================================

func TestNewOAuthHTTPClient(t *testing.T) {
	dir := t.TempDir()
	store := NewTokenStore(filepath.Join(dir, "tokens.json"))
	manager := NewOAuthManager(store)

	client := NewOAuthHTTPClient(manager, "key", &OAuthConfig{ClientID: "c"})
	if client == nil {
		t.Fatal("expected non-nil client")
		return
	}
	transport, ok := client.Transport.(*OAuthTransport)
	if !ok {
		t.Fatal("expected OAuthTransport")
	}
	if transport.ServerKey != "key" {
		t.Fatalf("expected server key %q, got %q", "key", transport.ServerKey)
	}
}

// =============================================================================
// DefaultTokenStorePath Tests
// =============================================================================

func TestDefaultTokenStorePath(t *testing.T) {
	path := DefaultTokenStorePath()
	if !strings.Contains(path, "mcp_oauth_tokens.json") {
		t.Fatalf("expected path to contain 'mcp_oauth_tokens.json', got %q", path)
	}
	if !strings.Contains(path, ".claude") {
		t.Fatalf("expected path to contain '.claude', got %q", path)
	}
}

// =============================================================================
// Test Helpers
// =============================================================================

// simulateCallback extracts the state and redirect_uri from an auth URL
// and makes a callback request with a test authorization code.
func simulateCallback(t *testing.T, authURL string) {
	t.Helper()

	parsed, err := parseURL(authURL)
	if err != nil {
		t.Errorf("failed to parse auth URL: %v", err)
		return
	}

	state := parsed.Query().Get("state")
	redirectURI := parsed.Query().Get("redirect_uri")

	if redirectURI == "" {
		t.Error("no redirect_uri in auth URL")
		return
	}

	callbackURL := redirectURI + "?code=test-auth-code&state=" + state
	resp, err := http.Get(callbackURL)
	if err != nil {
		t.Errorf("callback request failed: %v", err)
		return
	}
	_ = resp.Body.Close()
}

// parseFormBody parses a URL-encoded form body into a map.
func parseFormBody(body string) (map[string]string, error) {
	values, err := url.ParseQuery(body)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string)
	for k, v := range values {
		if len(v) > 0 {
			result[k] = v[0]
		}
	}
	return result, nil
}

// parseURL is a test helper that wraps url.Parse.
func parseURL(rawURL string) (*url.URL, error) {
	return url.Parse(rawURL)
}
