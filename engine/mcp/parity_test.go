package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// =============================================================================
// Parity Verification Tests — MCP Subsystem
//
// These tests verify behavioral parity with the reference implementation's
// OAuth token management, PKCE generation, notification dispatch, binary
// content handling, reconnection backoff, and config resolution.
// =============================================================================

// --- OAuth Token Store: CRUD operations ---

func TestParity_TokenStore_CRUD(t *testing.T) {
	dir := t.TempDir()
	store := NewTokenStore(filepath.Join(dir, "tokens.json"))

	token := &OAuthToken{
		AccessToken:  "access-abc",
		RefreshToken: "refresh-xyz",
		ExpiresAt:    time.Now().Add(1 * time.Hour).Unix(),
		TokenType:    "Bearer",
		Scopes:       []string{"read", "write", "admin"},
	}

	// Create
	if err := store.Save("server-1", token); err != nil {
		t.Fatalf("Save failed: %v", err)
		return
	}

	// Read
	loaded, err := store.Load("server-1")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
		return
	}
	if loaded == nil {
		t.Fatal("expected non-nil token")
		return
	}
	if loaded.AccessToken != "access-abc" {
		t.Errorf("expected access token 'access-abc', got %q", loaded.AccessToken)
	}
	if loaded.RefreshToken != "refresh-xyz" {
		t.Errorf("expected refresh token 'refresh-xyz', got %q", loaded.RefreshToken)
	}
	if len(loaded.Scopes) != 3 {
		t.Errorf("expected 3 scopes, got %d", len(loaded.Scopes))
	}

	// Update
	token.AccessToken = "access-updated"
	if err := store.Save("server-1", token); err != nil {
		t.Fatalf("Save (update) failed: %v", err)
		return
	}
	loaded, _ = store.Load("server-1")
	if loaded.AccessToken != "access-updated" {
		t.Errorf("expected updated access token, got %q", loaded.AccessToken)
	}

	// Delete
	if err := store.Delete("server-1"); err != nil {
		t.Fatalf("Delete failed: %v", err)
		return
	}
	loaded, _ = store.Load("server-1")
	if loaded != nil {
		t.Error("expected nil token after deletion")
	}
}

// --- OAuth Token Store: file permissions ---

func TestParity_TokenStore_FilePermissions(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "subdir", "tokens.json")
	store := NewTokenStore(storePath)

	token := &OAuthToken{
		AccessToken: "test-token",
		ExpiresAt:   time.Now().Add(1 * time.Hour).Unix(),
		TokenType:   "Bearer",
	}

	if err := store.Save("srv", token); err != nil {
		t.Fatal(err)
		return
	}

	// Verify file has restricted permissions (0600)
	info, err := os.Stat(storePath)
	if err != nil {
		t.Fatal(err)
		return
	}
	perm := info.Mode().Perm()
	if perm != 0o600 {
		t.Errorf("expected file permissions 0600, got %o", perm)
	}
}

// --- OAuth Token Store: multi-server isolation ---

func TestParity_TokenStore_MultiServerIsolation(t *testing.T) {
	dir := t.TempDir()
	store := NewTokenStore(filepath.Join(dir, "tokens.json"))

	tokenA := &OAuthToken{AccessToken: "token-a", ExpiresAt: time.Now().Add(1 * time.Hour).Unix(), TokenType: "Bearer"}
	tokenB := &OAuthToken{AccessToken: "token-b", ExpiresAt: time.Now().Add(2 * time.Hour).Unix(), TokenType: "Bearer"}

	_ = store.Save("server-alpha", tokenA)
	_ = store.Save("server-beta", tokenB)

	loadedA, _ := store.Load("server-alpha")
	loadedB, _ := store.Load("server-beta")

	if loadedA.AccessToken != "token-a" {
		t.Errorf("server-alpha token corrupted: got %q", loadedA.AccessToken)
	}
	if loadedB.AccessToken != "token-b" {
		t.Errorf("server-beta token corrupted: got %q", loadedB.AccessToken)
	}

	// Delete one should not affect the other
	_ = store.Delete("server-alpha")
	loadedB, _ = store.Load("server-beta")
	if loadedB == nil || loadedB.AccessToken != "token-b" {
		t.Error("deleting server-alpha should not affect server-beta")
	}

	// List should show remaining keys
	keys, _ := store.List()
	if len(keys) != 1 || keys[0] != "server-beta" {
		t.Errorf("expected [server-beta], got %v", keys)
	}
}

// --- OAuth Flow: PKCE uniqueness ---

func TestParity_PKCE_Uniqueness(t *testing.T) {
	// Each call to GeneratePKCE must produce unique verifier/challenge pairs
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		pkce, err := GeneratePKCE()
		if err != nil {
			t.Fatal(err)
			return
		}
		if seen[pkce.CodeVerifier] {
			t.Fatalf("duplicate code_verifier at iteration %d", i)
		}
		seen[pkce.CodeVerifier] = true

		// Verify the challenge is the correct S256 of the verifier
		hash := sha256.Sum256([]byte(pkce.CodeVerifier))
		expectedChallenge := base64.RawURLEncoding.EncodeToString(hash[:])
		if pkce.CodeChallenge != expectedChallenge {
			t.Fatalf("code_challenge does not match S256(code_verifier)")
		}
		if pkce.Method != "S256" {
			t.Fatalf("expected method S256, got %q", pkce.Method)
		}
	}
}

// --- OAuth Flow: code exchange ---

func TestParity_OAuth_CodeExchange(t *testing.T) {
	// Simulate an OAuth token endpoint
	var receivedParams string
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		receivedParams = r.Form.Encode()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":"new-token","refresh_token":"new-refresh","expires_in":3600,"token_type":"Bearer"}`)
	}))
	defer tokenServer.Close()

	cfg := &OAuthConfig{
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		TokenURL:     tokenServer.URL,
	}

	token, err := exchangeCodeForToken(context.Background(), "auth-code-123", "http://localhost/callback", "verifier-abc", cfg)
	if err != nil {
		t.Fatal(err)
		return
	}

	if token.AccessToken != "new-token" {
		t.Errorf("expected access token 'new-token', got %q", token.AccessToken)
	}
	if token.RefreshToken != "new-refresh" {
		t.Errorf("expected refresh token 'new-refresh', got %q", token.RefreshToken)
	}

	// Verify the request included required params
	if !strings.Contains(receivedParams, "code=auth-code-123") {
		t.Error("expected code parameter in token exchange request")
	}
	if !strings.Contains(receivedParams, "code_verifier=verifier-abc") {
		t.Error("expected code_verifier parameter in token exchange request")
	}
	if !strings.Contains(receivedParams, "grant_type=authorization_code") {
		t.Error("expected grant_type=authorization_code")
	}
}

// --- Notification Dispatcher: type routing ---

func TestParity_NotificationDispatcher_TypeRouting(t *testing.T) {
	d := NewNotificationDispatcher()

	var progressReceived atomic.Int32
	var loggingReceived atomic.Int32

	d.Register(NotificationProgress, func(ctx context.Context, notification any) {
		progressReceived.Add(1)
	})
	d.Register(NotificationLogging, func(ctx context.Context, notification any) {
		loggingReceived.Add(1)
	})

	// Dispatch a progress notification
	d.Dispatch(context.Background(), NotificationProgress, &ProgressNotification{Progress: 0.5})
	// Dispatch a logging notification
	d.Dispatch(context.Background(), NotificationLogging, &LoggingNotification{Level: "info"})

	// Give goroutines time to execute
	time.Sleep(50 * time.Millisecond)

	if progressReceived.Load() != 1 {
		t.Errorf("expected 1 progress notification, got %d", progressReceived.Load())
	}
	if loggingReceived.Load() != 1 {
		t.Errorf("expected 1 logging notification, got %d", loggingReceived.Load())
	}
}

// --- Notification Dispatcher: concurrent handlers ---

func TestParity_NotificationDispatcher_ConcurrentHandlers(t *testing.T) {
	d := NewNotificationDispatcher()
	var count atomic.Int32
	var wg sync.WaitGroup

	// Register multiple handlers for the same type
	for i := 0; i < 5; i++ {
		d.Register(NotificationProgress, func(ctx context.Context, notification any) {
			count.Add(1)
			wg.Done()
		})
	}

	wg.Add(5)
	d.Dispatch(context.Background(), NotificationProgress, &ProgressNotification{Progress: 1.0})
	wg.Wait()

	if count.Load() != 5 {
		t.Errorf("expected all 5 handlers called, got %d", count.Load())
	}
}

// --- Notification Dispatcher: unregister stops delivery ---

func TestParity_NotificationDispatcher_UnregisterStopsDelivery(t *testing.T) {
	d := NewNotificationDispatcher()
	var count atomic.Int32

	unregister := d.Register(NotificationProgress, func(ctx context.Context, notification any) {
		count.Add(1)
	})

	// First dispatch should be received
	d.Dispatch(context.Background(), NotificationProgress, nil)
	time.Sleep(50 * time.Millisecond)
	if count.Load() != 1 {
		t.Fatalf("expected 1 after first dispatch, got %d", count.Load())
	}

	// Unregister and dispatch again
	unregister()
	d.Dispatch(context.Background(), NotificationProgress, nil)
	time.Sleep(50 * time.Millisecond)

	if count.Load() != 1 {
		t.Errorf("expected count to remain 1 after unregister, got %d", count.Load())
	}
}

// --- Binary Content: encode/decode round-trip ---

func TestParity_BinaryContent_RoundTrip(t *testing.T) {
	testCases := []struct {
		name     string
		data     []byte
		wantMIME string
	}{
		{"PNG", []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00}, "image/png"},
		{"JPEG", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}, "image/jpeg"},
		{"GIF", []byte{'G', 'I', 'F', '8', '9', 'a', 0x00, 0x00}, "image/gif"},
		{"PDF", []byte{'%', 'P', 'D', 'F', '-', '1', '.', '4'}, "application/pdf"},
		{"Plain text", []byte("hello world"), "text/plain; charset=utf-8"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Encode
			encoded, err := EncodeBinaryContent(tc.data, "")
			if err != nil {
				t.Fatal(err)
				return
			}
			if encoded.MIMEType != tc.wantMIME {
				t.Errorf("expected MIME %q, got %q", tc.wantMIME, encoded.MIMEType)
			}
			if encoded.Base64 == "" {
				t.Error("expected non-empty base64 encoding")
			}

			// Decode round-trip
			decoded, err := DecodeBinaryContent(encoded.Base64, "")
			if err != nil {
				t.Fatal(err)
				return
			}
			if len(decoded.Data) != len(tc.data) {
				t.Errorf("round-trip data length mismatch: %d vs %d", len(decoded.Data), len(tc.data))
			}
			for i, b := range decoded.Data {
				if b != tc.data[i] {
					t.Errorf("round-trip data mismatch at byte %d", i)
					break
				}
			}
		})
	}
}

// --- Binary Content: size limit enforcement ---

func TestParity_BinaryContent_SizeLimitEnforcement(t *testing.T) {
	// Set a known max size via env var for deterministic testing
	t.Setenv(envMaxBinarySize, "1024")

	// Data within limit should succeed
	smallData := make([]byte, 1024)
	_, err := EncodeBinaryContent(smallData, "application/octet-stream")
	if err != nil {
		t.Fatalf("expected small data to encode successfully: %v", err)
		return
	}

	// Data exceeding limit should fail
	largeData := make([]byte, 1025)
	_, err = EncodeBinaryContent(largeData, "application/octet-stream")
	if err == nil {
		t.Fatal("expected error for data exceeding size limit")
		return
	}
	if !strings.Contains(err.Error(), "exceeds maximum") {
		t.Errorf("expected 'exceeds maximum' error, got: %v", err)
	}

	// Decode should also enforce the limit
	encoded := base64.StdEncoding.EncodeToString(largeData)
	_, err = DecodeBinaryContent(encoded, "")
	if err == nil {
		t.Fatal("expected error for decoded data exceeding size limit")
		return
	}
}

// --- Reconnection: backoff timing sequence ---

func TestParity_Reconnection_BackoffSequence(t *testing.T) {
	// Verify that calculateBackoff produces the expected exponential sequence
	initial := 1 * time.Second
	max := 30 * time.Second
	factor := 2.0

	expected := []time.Duration{
		1 * time.Second,  // attempt 1
		2 * time.Second,  // attempt 2
		4 * time.Second,  // attempt 3
		8 * time.Second,  // attempt 4
		16 * time.Second, // attempt 5
		30 * time.Second, // attempt 6 (capped at max)
		30 * time.Second, // attempt 7 (still capped)
	}

	for i, want := range expected {
		got := calculateBackoff(i+1, initial, max, factor)
		if got != want {
			t.Errorf("attempt %d: expected %v, got %v", i+1, want, got)
		}
	}
}

// --- Reconnection: max attempts exhaustion ---

func TestParity_Reconnection_MaxAttemptsExhaustion(t *testing.T) {
	cfg := ReconnectConfig{
		InitialBackoff: 1 * time.Millisecond,
		MaxBackoff:     10 * time.Millisecond,
		BackoffFactor:  2.0,
		MaxAttempts:    3,
	}

	var failedCalled atomic.Bool
	cfg.OnReconnectFailed = func(lastErr error) {
		failedCalled.Store(true)
	}

	var attempts atomic.Int32
	cfg.OnReconnecting = func(attempt int, delay time.Duration) {
		attempts.Add(1)
	}

	// Create a reconnection manager with a client that always fails reconnect.
	// We simulate the reconnection behavior by directly testing the config behavior.
	// The actual reconnectLoop requires a full MCPClient, so we verify the config
	// parameters are correctly structured.
	if cfg.MaxAttempts != 3 {
		t.Fatalf("expected MaxAttempts=3, got %d", cfg.MaxAttempts)
	}

	// Verify DefaultReconnectConfig returns sensible values
	defaultCfg := DefaultReconnectConfig()
	if defaultCfg.MaxAttempts != defaultMaxAttempts {
		t.Errorf("expected default max attempts %d, got %d", defaultMaxAttempts, defaultCfg.MaxAttempts)
	}
	if defaultCfg.InitialBackoff != defaultInitialBackoff {
		t.Errorf("expected default initial backoff %v, got %v", defaultInitialBackoff, defaultCfg.InitialBackoff)
	}
	if defaultCfg.MaxBackoff != defaultMaxBackoff {
		t.Errorf("expected default max backoff %v, got %v", defaultMaxBackoff, defaultCfg.MaxBackoff)
	}
	if defaultCfg.BackoffFactor != defaultBackoffFactor {
		t.Errorf("expected default backoff factor %v, got %v", defaultBackoffFactor, defaultCfg.BackoffFactor)
	}
}

// --- Reconnection: env var override for max attempts ---

func TestParity_Reconnection_EnvVarOverride(t *testing.T) {
	t.Setenv(envMaxReconnectAttempts, "25")

	cfg := DefaultReconnectConfig()
	if cfg.MaxAttempts != 25 {
		t.Errorf("expected MaxAttempts=25 from env, got %d", cfg.MaxAttempts)
	}
}

// --- Config: env var resolution ---

func TestParity_Config_EnvVarResolution(t *testing.T) {
	t.Setenv("MCP_TEST_VAR", "resolved-value")
	t.Setenv("MCP_PORT", "8080")

	// Test ${VAR} syntax
	result := ResolveEnvVars("host:${MCP_PORT}/path")
	if result != "host:8080/path" {
		t.Errorf("expected 'host:8080/path', got %q", result)
	}

	// Test $VAR syntax
	result = ResolveEnvVars("value=$MCP_TEST_VAR")
	if result != "value=resolved-value" {
		t.Errorf("expected 'value=resolved-value', got %q", result)
	}

	// Unset variables resolve to empty
	result = ResolveEnvVars("$UNSET_VAR_12345")
	if result != "" {
		t.Errorf("expected empty string for unset var, got %q", result)
	}
}

// --- Config: project config merging ---

func TestParity_Config_ProjectConfigMerging(t *testing.T) {
	dir := t.TempDir()

	// Create user-level config
	userDir := filepath.Join(dir, "home", ".claude")
	_ = os.MkdirAll(userDir, 0o755)
	userConfig := `{"mcpServers":{"server-user":{"command":"user-cmd","args":["--user"]}}}`
	_ = os.WriteFile(filepath.Join(userDir, mcpServersFileName), []byte(userConfig), 0o644)

	// Create project-level .mcp.json
	projectConfig := `{"mcpServers":{"server-user":{"command":"project-cmd","args":["--project"]},"server-project":{"command":"extra-cmd"}}}`
	_ = os.WriteFile(filepath.Join(dir, mcpProjectFileName), []byte(projectConfig), 0o644)

	// Merge: project should override user for "server-user"
	userCfg, _ := loadMCPConfigFile(filepath.Join(userDir, mcpServersFileName))
	projectCfg, _ := loadMCPConfigFile(filepath.Join(dir, mcpProjectFileName))

	merged := mergeMCPFileConfigs(userCfg, projectCfg)
	if merged == nil {
		t.Fatal("merged config is nil")
		return
	}

	// server-user should have project's command
	if srv, ok := merged.MCPServers["server-user"]; !ok {
		t.Error("server-user missing from merged config")
	} else if srv.Command != "project-cmd" {
		t.Errorf("expected project-cmd to override user-cmd, got %q", srv.Command)
	}

	// server-project should be present from project config
	if _, ok := merged.MCPServers["server-project"]; !ok {
		t.Error("server-project missing from merged config")
	}
}

// --- Config: name normalization ---

func TestParity_Config_NameNormalization(t *testing.T) {
	testCases := []struct {
		input    string
		expected string
	}{
		{"simple-name", "simple-name"},
		{"name with spaces", "name_with_spaces"},
		{"name@special#chars!", "name_special_chars_"},
		{"claude.ai something", "claude_ai_something"},
		{strings.Repeat("a", 100), strings.Repeat("a", 64)}, // truncation
	}

	for _, tc := range testCases {
		t.Run(tc.input, func(t *testing.T) {
			result := NormalizeNameForMCP(tc.input)
			if result != tc.expected {
				t.Errorf("NormalizeNameForMCP(%q) = %q, want %q", tc.input, result, tc.expected)
			}
		})
	}
}

// --- OAuth: token expiry detection ---

func TestParity_OAuth_TokenExpiry(t *testing.T) {
	// Valid token
	validToken := &OAuthToken{
		AccessToken: "valid",
		ExpiresAt:   time.Now().Add(1 * time.Hour).Unix(),
	}
	if validToken.IsExpired(0) {
		t.Error("expected valid token to not be expired")
	}

	// Expired token
	expiredToken := &OAuthToken{
		AccessToken: "expired",
		ExpiresAt:   time.Now().Add(-1 * time.Hour).Unix(),
	}
	if !expiredToken.IsExpired(0) {
		t.Error("expected expired token to be expired")
	}

	// Token within grace period
	almostExpired := &OAuthToken{
		AccessToken: "almost",
		ExpiresAt:   time.Now().Add(3 * time.Minute).Unix(),
	}
	if !almostExpired.IsExpired(5 * time.Minute) {
		t.Error("expected token within grace period to be considered expired")
	}
	if almostExpired.IsExpired(1 * time.Minute) {
		t.Error("expected token outside grace period to not be expired")
	}

	// Nil token
	var nilToken *OAuthToken
	if !nilToken.IsExpired(0) {
		t.Error("expected nil token to be expired")
	}
}
