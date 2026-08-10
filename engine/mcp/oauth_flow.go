package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// PKCEChallenge holds the code_verifier and code_challenge pair
// used in the OAuth 2.0 PKCE extension (RFC 7636).
type PKCEChallenge struct {
	// CodeVerifier is the high-entropy cryptographic random string.
	CodeVerifier string
	// CodeChallenge is the S256 hash of the code verifier.
	CodeChallenge string
	// Method is always "S256".
	Method string
}

// GeneratePKCE generates a new PKCE code_verifier and code_challenge pair.
// The code_verifier is 32 bytes of cryptographically random data, base64url-encoded.
// The code_challenge is the SHA-256 hash of the verifier, base64url-encoded.
func GeneratePKCE() (*PKCEChallenge, error) {
	// Generate 32 bytes of random data for the code verifier.
	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		return nil, fmt.Errorf("oauth: failed to generate random bytes: %w", err)
	}

	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)

	// Compute S256 code challenge: BASE64URL(SHA256(code_verifier)).
	hash := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(hash[:])

	return &PKCEChallenge{
		CodeVerifier:  verifier,
		CodeChallenge: challenge,
		Method:        "S256",
	}, nil
}

// AuthFlowResult represents the result of a successful OAuth authorization flow.
type AuthFlowResult struct {
	// Token is the obtained OAuth token.
	Token *OAuthToken
}

// AuthFlowOptions configures the authorization code flow.
type AuthFlowOptions struct {
	// Config is the OAuth configuration.
	Config *OAuthConfig
	// ServerKey identifies the server for token storage.
	ServerKey string
	// Store is the token store for persisting the obtained token.
	Store *TokenStore
	// OpenBrowser is a function that opens the authorization URL in the user's browser.
	// If nil, the URL will be provided but not automatically opened.
	OpenBrowser func(url string) error
	// Timeout is the maximum time to wait for the authorization callback.
	// Defaults to 5 minutes if zero.
	Timeout time.Duration
	// ListenAddr overrides the listen address for the callback server.
	// Defaults to "127.0.0.1:0" (random available port on localhost).
	ListenAddr string
}

// AuthorizationCodeFlow performs the full OAuth 2.0 Authorization Code flow
// with PKCE. It:
//  1. Generates a PKCE code_verifier/code_challenge
//  2. Starts a local HTTP server to receive the authorization callback
//  3. Builds the authorization URL and opens it (or returns it for manual opening)
//  4. Waits for the callback with the authorization code
//  5. Exchanges the code for tokens
//  6. Persists the tokens to the store
func AuthorizationCodeFlow(ctx context.Context, opts AuthFlowOptions) (*AuthFlowResult, error) {
	if opts.Config == nil {
		return nil, fmt.Errorf("oauth: config is required")
	}
	if opts.Config.AuthURL == "" {
		return nil, fmt.Errorf("oauth: auth_url is required")
	}
	if opts.Config.TokenURL == "" {
		return nil, fmt.Errorf("oauth: token_url is required")
	}
	if opts.Config.ClientID == "" {
		return nil, fmt.Errorf("oauth: client_id is required")
	}

	// Generate PKCE challenge.
	pkce, err := GeneratePKCE()
	if err != nil {
		return nil, err
	}

	// Generate state parameter for CSRF protection.
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		return nil, fmt.Errorf("oauth: failed to generate state: %w", err)
	}
	state := base64.RawURLEncoding.EncodeToString(stateBytes)

	// Start callback server.
	listenAddr := opts.ListenAddr
	if listenAddr == "" {
		listenAddr = "127.0.0.1:0"
	}

	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("oauth: failed to start callback server: %w", err)
	}
	defer listener.Close() //nolint:errcheck

	callbackPort := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://localhost:%d/callback", callbackPort)

	// Override redirect URI if specified in config.
	if opts.Config.RedirectURL != "" {
		redirectURI = opts.Config.RedirectURL
	}

	// Build authorization URL.
	authURL, err := buildAuthorizationURL(opts.Config, redirectURI, state, pkce)
	if err != nil {
		return nil, err
	}

	// Set up the callback handler.
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	callbackServer := &callbackHandler{
		expectedState: state,
		codeCh:        codeCh,
		errCh:         errCh,
	}

	server := &http.Server{
		Handler: callbackServer,
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("oauth: callback server error: %w", err)
		}
	}()

	// Open browser or provide URL.
	if opts.OpenBrowser != nil {
		if err := opts.OpenBrowser(authURL); err != nil {
			// Non-fatal: the URL can be opened manually.
			_ = err
		}
	}

	// Wait for callback or timeout.
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}

	var code string
	select {
	case code = <-codeCh:
		// Success - received authorization code.
	case err := <-errCh:
		shutdownCallbackServer(server, &wg)
		return nil, err
	case <-time.After(timeout):
		_ = server.Close()
		wg.Wait()
		return nil, fmt.Errorf("oauth: authorization flow timed out after %v", timeout)
	case <-ctx.Done():
		_ = server.Close()
		wg.Wait()
		return nil, ctx.Err()
	}

	// Shutdown callback server.
	shutdownCallbackServer(server, &wg)

	// Exchange authorization code for tokens.
	token, err := exchangeCodeForToken(ctx, code, redirectURI, pkce.CodeVerifier, opts.Config)
	if err != nil {
		return nil, err
	}

	// Persist token if store is provided.
	if opts.Store != nil && opts.ServerKey != "" {
		if err := opts.Store.Save(opts.ServerKey, token); err != nil {
			return nil, fmt.Errorf("oauth: failed to save token: %w", err)
		}
	}

	return &AuthFlowResult{Token: token}, nil
}

func shutdownCallbackServer(server *http.Server, wg *sync.WaitGroup) {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
	}
	cancel()
	wg.Wait()
}

// buildAuthorizationURL constructs the full authorization URL with all required parameters.
func buildAuthorizationURL(cfg *OAuthConfig, redirectURI, state string, pkce *PKCEChallenge) (string, error) {
	u, err := url.Parse(cfg.AuthURL)
	if err != nil {
		return "", fmt.Errorf("oauth: invalid auth_url: %w", err)
	}

	params := u.Query()
	params.Set("response_type", "code")
	params.Set("client_id", cfg.ClientID)
	params.Set("redirect_uri", redirectURI)
	params.Set("state", state)
	params.Set("code_challenge", pkce.CodeChallenge)
	params.Set("code_challenge_method", pkce.Method)

	if len(cfg.Scopes) > 0 {
		params.Set("scope", strings.Join(cfg.Scopes, " "))
	}

	u.RawQuery = params.Encode()
	return u.String(), nil
}

// exchangeCodeForToken exchanges an authorization code for tokens.
func exchangeCodeForToken(ctx context.Context, code, redirectURI, codeVerifier string, cfg *OAuthConfig) (*OAuthToken, error) {
	params := url.Values{}
	params.Set("grant_type", "authorization_code")
	params.Set("code", code)
	params.Set("redirect_uri", redirectURI)
	params.Set("client_id", cfg.ClientID)
	params.Set("code_verifier", codeVerifier)

	if cfg.ClientSecret != "" {
		params.Set("client_secret", cfg.ClientSecret)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.TokenURL, strings.NewReader(params.Encode()))
	if err != nil {
		return nil, fmt.Errorf("oauth: failed to create token exchange request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oauth: token exchange request failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("oauth: token exchange failed with status %d: %s", resp.StatusCode, sanitizeErrorBody(body))
	}

	return parseTokenResponse(resp)
}

// callbackHandler handles the OAuth redirect callback.
type callbackHandler struct {
	expectedState string
	codeCh        chan string
	errCh         chan error
	once          sync.Once
}

func (h *callbackHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Only handle the callback path.
	if r.URL.Path != "/callback" {
		http.NotFound(w, r)
		return
	}

	h.once.Do(func() {
		query := r.URL.Query()

		// Check for error response from authorization server.
		if errCode := query.Get("error"); errCode != "" {
			errDesc := query.Get("error_description")
			if errDesc == "" {
				errDesc = errCode
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, authFailureHTML)
			h.errCh <- fmt.Errorf("oauth: authorization denied: %s", errDesc)
			return
		}

		// Validate state parameter.
		receivedState := query.Get("state")
		if receivedState != h.expectedState {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, authFailureHTML)
			h.errCh <- fmt.Errorf("oauth: state mismatch (possible CSRF attack)")
			return
		}

		// Extract authorization code.
		code := query.Get("code")
		if code == "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, authFailureHTML)
			h.errCh <- fmt.Errorf("oauth: no authorization code in callback")
			return
		}

		// Success.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, authSuccessHTML)
		h.codeCh <- code
	})
}

// sanitizeErrorBody removes sensitive information from OAuth error response bodies.
// Only returns known safe fields from the error response.
func sanitizeErrorBody(body []byte) string {
	var errResp struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error != "" {
		if errResp.Description != "" {
			return errResp.Error + ": " + errResp.Description
		}
		return errResp.Error
	}
	// Do not return raw body - it may contain sensitive data.
	return "(unparseable error response)"
}

// urlEncode performs URL encoding for form parameters.
func urlEncode(s string) string {
	return url.QueryEscape(s)
}

// stringReader creates an io.Reader from a string.
func stringReader(s string) io.Reader {
	return strings.NewReader(s)
}

// HTML pages for the callback responses.
const authSuccessHTML = `<!DOCTYPE html>
<html><head><title>Authorization Successful</title></head>
<body style="font-family:sans-serif;text-align:center;padding:50px">
<h1>Authorization Successful</h1>
<p>You can close this window and return to the terminal.</p>
</body></html>`

const authFailureHTML = `<!DOCTYPE html>
<html><head><title>Authorization Failed</title></head>
<body style="font-family:sans-serif;text-align:center;padding:50px">
<h1>Authorization Failed</h1>
<p>Something went wrong. Please try again.</p>
</body></html>`
