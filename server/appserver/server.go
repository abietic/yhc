package appserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Config defines the bounded local app-server runtime.
type Config struct {
	Factory           EngineFactory
	Token             string
	EventBuffer       int
	MaxSessions       int
	EnableWeb         bool
	WebAssets         fs.FS
	BrowserPairingTTL time.Duration
	BrowserSessionTTL time.Duration
	Now               func() time.Time
}

// Server owns every live Desktop runtime admitted by this process.
type Server struct {
	mu          sync.RWMutex
	closeOnce   sync.Once
	creationWG  sync.WaitGroup
	closing     bool
	shutdownErr error
	id          string
	token       string
	startedAt   time.Time
	factory     EngineFactory
	eventBuffer int
	maxSessions int
	now         func() time.Time
	sessions    map[string]*session
	creating    int
	creatingIDs map[string]struct{}
	webEnabled  bool
	webAssets   fs.FS
	browserAuth *browserAuth
	authority   string
	ctx         context.Context
	cancel      context.CancelFunc
	httpServer  *http.Server
}

// New creates an authenticated loopback app-server.
func New(config Config) (*Server, error) {
	if config.Factory == nil {
		return nil, fmt.Errorf("app-server engine factory is required")
	}
	token := strings.TrimSpace(config.Token)
	if token == "" {
		var err error
		token, err = generateToken()
		if err != nil {
			return nil, err
		}
	}
	eventBuffer := config.EventBuffer
	if eventBuffer <= 0 {
		eventBuffer = defaultEventBuffer
	}
	maxSessions := config.MaxSessions
	if maxSessions <= 0 {
		maxSessions = defaultMaxSessions
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	pairingTTL := config.BrowserPairingTTL
	if pairingTTL <= 0 {
		pairingTTL = 2 * time.Minute
	}
	sessionTTL := config.BrowserSessionTTL
	if sessionTTL <= 0 {
		sessionTTL = 12 * time.Hour
	}
	if config.EnableWeb && config.WebAssets == nil {
		return nil, fmt.Errorf("app-server Web assets are required when Web is enabled")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		id:          uuid.NewString(),
		token:       token,
		startedAt:   now().UTC(),
		factory:     config.Factory,
		eventBuffer: eventBuffer,
		maxSessions: maxSessions,
		now:         now,
		sessions:    make(map[string]*session),
		creatingIDs: make(map[string]struct{}),
		webEnabled:  config.EnableWeb,
		webAssets:   config.WebAssets,
		ctx:         ctx,
		cancel:      cancel,
	}
	if config.EnableWeb {
		s.browserAuth = newBrowserAuth(now, pairingTTL, sessionTTL)
	}
	s.httpServer = &http.Server{
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	return s, nil
}

// Token returns the process-scoped bearer token for the trusted Desktop shell.
func (s *Server) Token() string {
	return s.token
}

// Handler exposes the authenticated HTTP handler for focused tests.
func (s *Server) Handler() http.Handler {
	return s.httpServer.Handler
}

// BootstrapFor returns the renderer bootstrap payload for a bound listener.
func (s *Server) BootstrapFor(listener net.Listener) (Bootstrap, error) {
	baseURL, err := loopbackURL(listener)
	if err != nil {
		return Bootstrap{}, err
	}
	if err := s.bindAuthority(baseURL); err != nil {
		return Bootstrap{}, err
	}
	bootstrap := Bootstrap{
		ProtocolVersion: ProtocolVersion,
		URL:             baseURL,
		Token:           s.token,
		PID:             os.Getpid(),
	}
	if s.webEnabled {
		pairingToken, _, err := s.browserAuth.newPairing()
		if err != nil {
			return Bootstrap{}, err
		}
		bootstrap.WebURL = baseURL + "/#pair=" + url.QueryEscape(pairingToken)
	}
	return bootstrap, nil
}

func loopbackURL(listener net.Listener) (string, error) {
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return "", fmt.Errorf("app-server requires a TCP listener")
	}
	host := addr.IP.String()
	if host == "" || addr.IP.IsUnspecified() {
		return "", fmt.Errorf("app-server listener must use an explicit loopback address")
	}
	if !addr.IP.IsLoopback() && host != "127.0.0.1" && host != "::1" {
		return "", fmt.Errorf("app-server listener must be loopback")
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("http://%s:%d", host, addr.Port), nil
}

// Serve blocks until shutdown or a listener error.
func (s *Server) Serve(listener net.Listener) error {
	baseURL, err := loopbackURL(listener)
	if err != nil {
		return err
	}
	if err := s.bindAuthority(baseURL); err != nil {
		return err
	}
	err = s.httpServer.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) bindAuthority(baseURL string) error {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.Path != "" {
		return fmt.Errorf("app-server listener authority is invalid")
	}
	authority, err := normalizeLoopbackAuthority(parsed.Host)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.authority != "" && s.authority != authority {
		return fmt.Errorf("app-server listener authority changed")
	}
	s.authority = authority
	return nil
}

func normalizeLoopbackAuthority(value string) (string, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(value))
	if err != nil || port == "" {
		return "", fmt.Errorf("app-server authority must include one loopback host and port")
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || !ip.IsLoopback() {
		return "", fmt.Errorf("app-server authority must be loopback")
	}
	parsedPort, err := strconv.ParseUint(port, 10, 16)
	if err != nil || parsedPort == 0 {
		return "", fmt.Errorf("app-server authority port is invalid")
	}
	return net.JoinHostPort(ip.String(), strconv.FormatUint(parsedPort, 10)), nil
}

func (s *Server) requestAuthorityMatches(request *http.Request) bool {
	s.mu.RLock()
	expected := s.authority
	s.mu.RUnlock()
	if expected == "" {
		// Handler-only unit tests may use a fixed bearer without binding a
		// listener. Production Serve and every renderer bootstrap bind first.
		return !s.webEnabled
	}
	actual, err := normalizeLoopbackAuthority(request.Host)
	return err == nil && actual == expected
}

// Shutdown stops HTTP admission, active turns, and engines.
func (s *Server) Shutdown(ctx context.Context) error {
	s.closeOnce.Do(func() {
		var result error
		s.cancel()
		s.mu.Lock()
		s.closing = true
		owned := make([]*session, 0, len(s.sessions))
		for id, current := range s.sessions {
			owned = append(owned, current)
			delete(s.sessions, id)
		}
		s.mu.Unlock()
		if err := s.waitForSessionCreations(ctx); result == nil && err != nil {
			result = err
		}
		if s.browserAuth != nil {
			s.browserAuth.clear()
		}
		for _, current := range owned {
			if err := current.close(ctx); result == nil && err != nil {
				result = err
			}
		}
		if err := s.httpServer.Shutdown(ctx); result == nil {
			result = err
		}
		s.mu.Lock()
		s.shutdownErr = result
		s.mu.Unlock()
	})
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.shutdownErr
}

func (s *Server) waitForSessionCreations(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		s.creationWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type requestPrincipal struct {
	bearer         bool
	browserToken   string
	browserSession *browserSession
}

type requestPrincipalKey struct{}

func (s *Server) routes() http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("GET /v1/health", s.handleHealth)
	api.HandleFunc("GET /v1/sessions", s.handleListSessions)
	api.HandleFunc("POST /v1/sessions", s.handleCreateSession)
	api.HandleFunc("GET /v1/sessions/{session_id}", s.handleGetSession)
	api.HandleFunc("DELETE /v1/sessions/{session_id}", s.handleDeleteSession)
	api.HandleFunc("GET /v1/sessions/{session_id}/snapshot", s.handleSnapshot)
	api.HandleFunc("GET /v1/sessions/{session_id}/review-diff", s.handleReviewDiff)
	api.HandleFunc("GET /v1/sessions/{session_id}/execution-settings", s.handleGetExecutionSettings)
	api.HandleFunc("PATCH /v1/sessions/{session_id}/execution-settings", s.handleUpdateExecutionSettings)
	api.HandleFunc("GET /v1/sessions/{session_id}/events", s.handleEvents)
	api.HandleFunc("POST /v1/sessions/{session_id}/turns", s.handleStartTurn)
	api.HandleFunc("POST /v1/sessions/{session_id}/cancel", s.handleCancelTurn)
	api.HandleFunc(
		"GET /v1/sessions/{session_id}/interactions/{request_id}/plan",
		s.handleInteractionPlanReview,
	)
	api.HandleFunc(
		"POST /v1/sessions/{session_id}/interactions/{request_id}/resolve",
		s.handleResolveInteraction,
	)

	root := http.NewServeMux()
	if s.webEnabled {
		root.Handle(
			"POST /v1/auth/browser-pairing",
			s.middleware(http.HandlerFunc(s.handleBrowserPairing)),
		)
		root.HandleFunc("POST /v1/auth/browser-session", s.handleBrowserPair)
		root.Handle(
			"GET /v1/auth/browser-session",
			s.middleware(http.HandlerFunc(s.handleBrowserSession)),
		)
		root.Handle(
			"DELETE /v1/auth/browser-session",
			s.middleware(http.HandlerFunc(s.handleBrowserSessionDelete)),
		)
		root.HandleFunc("GET /{$}", s.handleWebAsset)
		for asset := range webAssetPaths {
			if asset == "/" {
				continue
			}
			root.HandleFunc("GET "+asset, s.handleWebAsset)
		}
	}
	root.Handle("/v1/", s.middleware(api))
	return s.securityHeaders(root)
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		if !s.requestAuthorityMatches(r) {
			writeError(w, http.StatusMisdirectedRequest, "host_rejected", "request Host does not match the app-server listener")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization := strings.TrimSpace(r.Header.Get("Authorization"))
		if authorization != "" {
			if !bearerMatches(authorization, s.token) {
				writeError(w, http.StatusUnauthorized, "unauthorized", "valid bearer token required")
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(
				r.Context(),
				requestPrincipalKey{},
				requestPrincipal{bearer: true},
			)))
			return
		}
		if s.browserAuth == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "valid bearer token required")
			return
		}
		cookie, err := r.Cookie(browserSessionCookieName)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "valid browser session required")
			return
		}
		browser, err := s.browserAuth.validate(cookie.Value)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "valid browser session required")
			return
		}
		if err := validateBrowserRequest(r, browser); err != nil {
			writeError(w, http.StatusForbidden, "browser_request_rejected", err.Error())
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(
			r.Context(),
			requestPrincipalKey{},
			requestPrincipal{
				browserToken:   cookie.Value,
				browserSession: &browser,
			},
		)))
	})
}

func bearerMatches(header, token string) bool {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return false
	}
	provided := parts[1]
	return len(provided) == len(token) &&
		subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1
}

func validateBrowserRequest(r *http.Request, session browserSession) error {
	expectedOrigin := requestOrigin(r)
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin != "" && origin != expectedOrigin {
		return errors.New("browser Origin does not match the app-server")
	}
	if site := strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")); site != "" && site != "same-origin" {
		return errors.New("cross-site browser request rejected")
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return nil
	default:
		if origin != expectedOrigin {
			return errors.New("state-changing browser request requires the exact Origin")
		}
		if !csrfMatches(r.Header.Get("X-YHC-CSRF"), session.csrfToken) {
			return errors.New("state-changing browser request requires a valid CSRF token")
		}
		return nil
	}
}

func requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func requestPrincipalFromContext(ctx context.Context) (requestPrincipal, bool) {
	principal, ok := ctx.Value(requestPrincipalKey{}).(requestPrincipal)
	return principal, ok
}

func (s *Server) handleBrowserPairing(w http.ResponseWriter, r *http.Request) {
	principal, ok := requestPrincipalFromContext(r.Context())
	if !ok || !principal.bearer {
		writeError(
			w,
			http.StatusForbidden,
			"bearer_required",
			"trusted Desktop bearer authentication required",
		)
		return
	}
	token, expiresAt, err := s.browserAuth.newPairing()
	if err != nil {
		writeError(
			w,
			http.StatusInternalServerError,
			"pairing_create_failed",
			"could not create a browser pairing",
		)
		return
	}
	writeJSON(w, http.StatusCreated, BrowserPairingResponse{
		WebURL:    requestOrigin(r) + "/#pair=" + url.QueryEscape(token),
		ExpiresAt: expiresAt,
	})
}

func (s *Server) handleBrowserPair(w http.ResponseWriter, r *http.Request) {
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != requestOrigin(r) {
		writeError(
			w,
			http.StatusForbidden,
			"browser_request_rejected",
			"browser pairing requires the exact Origin",
		)
		return
	}
	if site := strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")); site != "" &&
		site != "same-origin" {
		writeError(
			w,
			http.StatusForbidden,
			"browser_request_rejected",
			"cross-site browser pairing rejected",
		)
		return
	}
	var input BrowserPairRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	token, session, err := s.browserAuth.exchange(input.PairingToken)
	if err != nil {
		writeError(
			w,
			http.StatusUnauthorized,
			"pairing_invalid",
			"browser pairing token is invalid or expired",
		)
		return
	}
	http.SetCookie(w, browserCookie(token, session.expiresAt, s.now().UTC()))
	writeJSON(w, http.StatusCreated, BrowserSessionResponse{
		ProtocolVersion: ProtocolVersion,
		CSRFToken:       session.csrfToken,
		ExpiresAt:       session.expiresAt,
	})
}

func (s *Server) handleBrowserSession(w http.ResponseWriter, r *http.Request) {
	principal, ok := requestPrincipalFromContext(r.Context())
	if !ok || principal.browserSession == nil {
		writeError(
			w,
			http.StatusForbidden,
			"browser_session_required",
			"browser session authentication required",
		)
		return
	}
	writeJSON(w, http.StatusOK, BrowserSessionResponse{
		ProtocolVersion: ProtocolVersion,
		CSRFToken:       principal.browserSession.csrfToken,
		ExpiresAt:       principal.browserSession.expiresAt,
	})
}

func (s *Server) handleBrowserSessionDelete(w http.ResponseWriter, r *http.Request) {
	principal, ok := requestPrincipalFromContext(r.Context())
	if !ok || principal.browserSession == nil {
		writeError(
			w,
			http.StatusForbidden,
			"browser_session_required",
			"browser session authentication required",
		)
		return
	}
	s.browserAuth.revoke(principal.browserToken)
	http.SetCookie(w, expiredBrowserCookie())
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleWebAsset(w http.ResponseWriter, r *http.Request) {
	assetPath, ok := webAssetPaths[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	content, err := fs.ReadFile(s.webAssets, assetPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	contentType := mime.TypeByExtension(path.Ext(assetPath))
	if contentType == "" {
		contentType = http.DetectContentType(content)
	}
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; script-src 'self'; style-src 'self'; "+
			"connect-src 'self'; img-src 'self' data:; object-src 'none'; "+
			"base-uri 'none'; frame-src 'none'; form-action 'none'",
	)
	w.Header().Set(
		"Permissions-Policy",
		"camera=(), microphone=(), geolocation=(), payment=(), usb=()",
	)
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

var webAssetPaths = map[string]string{
	"/":                     "assets/index.html",
	"/activity.mjs":         "assets/activity.mjs",
	"/app.mjs":              "assets/app.mjs",
	"/catalog.mjs":          "assets/catalog.mjs",
	"/layout.mjs":           "assets/layout.mjs",
	"/markdown.mjs":         "assets/markdown.mjs",
	"/provider_setup.mjs":   "assets/provider_setup.mjs",
	"/state.mjs":            "assets/state.mjs",
	"/styles.css":           "assets/styles.css",
	"/transport.mjs":        "assets/transport.mjs",
	"/vendor/marked.esm.js": "assets/vendor/marked.esm.js",
	"/view_models.mjs":      "assets/view_models.mjs",
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, HealthResponse{
		ProtocolVersion: ProtocolVersion,
		ServerID:        s.id,
		StartedAt:       s.startedAt,
	})
}

func (s *Server) handleListSessions(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	result := make([]SessionSummary, 0, len(s.sessions))
	for _, owned := range s.sessions {
		result = append(result, owned.summary())
	}
	s.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool {
		return result[i].UpdatedAt.After(result[j].UpdatedAt)
	})
	writeJSON(w, http.StatusOK, SessionListResponse{Sessions: result})
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var input CreateSessionRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	requestedID := strings.TrimSpace(input.SessionID)
	reservationStatus, reservationCode := s.reserveSessionCreation(requestedID)
	if reservationStatus != 0 {
		writeError(w, reservationStatus, reservationCode, sessionReservationMessage(reservationCode))
		return
	}
	owned, err := newSession(s.ctx, s.factory, input, s.eventBuffer, s.now().UTC())
	if err != nil {
		s.releaseSessionCreation(requestedID)
		status := http.StatusUnprocessableEntity
		code := "session_create_failed"
		if strings.Contains(err.Error(), "active app-server owner") {
			status = http.StatusConflict
			code = "session_lease_conflict"
		}
		writeError(w, status, code, err.Error())
		return
	}
	s.mu.Lock()
	s.releaseSessionCreationLocked(requestedID)
	_, exists := s.sessions[owned.id]
	closing := s.closing
	if exists || closing {
		s.mu.Unlock()
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = owned.close(closeCtx)
		cancel()
		if closing {
			writeError(
				w,
				http.StatusServiceUnavailable,
				"server_closing",
				"app-server is shutting down",
			)
			return
		}
		writeError(w, http.StatusConflict, "session_owned", "session became active concurrently")
		return
	}
	s.sessions[owned.id] = owned
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, owned.summary())
}

func (s *Server) reserveSessionCreation(requestedID string) (int, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return http.StatusServiceUnavailable, "server_closing"
	}
	if len(s.sessions)+s.creating >= s.maxSessions {
		return http.StatusTooManyRequests, "session_limit"
	}
	if requestedID != "" {
		if _, exists := s.sessions[requestedID]; exists {
			return http.StatusConflict, "session_owned"
		}
		if _, exists := s.creatingIDs[requestedID]; exists {
			return http.StatusConflict, "session_owned"
		}
		s.creatingIDs[requestedID] = struct{}{}
	}
	s.creating++
	s.creationWG.Add(1)
	return 0, ""
}

func (s *Server) releaseSessionCreation(requestedID string) {
	s.mu.Lock()
	s.releaseSessionCreationLocked(requestedID)
	s.mu.Unlock()
}

func (s *Server) releaseSessionCreationLocked(requestedID string) {
	if s.creating > 0 {
		s.creating--
		s.creationWG.Done()
	}
	if requestedID != "" {
		delete(s.creatingIDs, requestedID)
	}
}

func sessionReservationMessage(code string) string {
	switch code {
	case "server_closing":
		return "app-server is shutting down"
	case "session_limit":
		return "app-server session limit reached"
	case "session_owned":
		return "session is already active or being created"
	default:
		return "session admission failed"
	}
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	owned, ok := s.getSession(r.PathValue("session_id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session_not_found", "session not found")
		return
	}
	writeJSON(w, http.StatusOK, owned.summary())
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("session_id")
	s.mu.Lock()
	owned, ok := s.sessions[id]
	if ok {
		delete(s.sessions, id)
	}
	s.mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "session_not_found", "session not found")
		return
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := owned.close(closeCtx); err != nil {
		writeError(w, http.StatusInternalServerError, "session_close_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	owned, ok := s.getSession(r.PathValue("session_id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session_not_found", "session not found")
		return
	}
	writeJSON(w, http.StatusOK, owned.snapshot())
}

func (s *Server) handleStartTurn(w http.ResponseWriter, r *http.Request) {
	owned, ok := s.getSession(r.PathValue("session_id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session_not_found", "session not found")
		return
	}
	var input StartTurnRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := owned.startTurn(input)
	if err != nil {
		status := http.StatusUnprocessableEntity
		code := "turn_rejected"
		if errors.Is(err, errSessionBusy) {
			status = http.StatusConflict
			code = "turn_in_progress"
		}
		if errors.Is(err, errSessionClosed) {
			status = http.StatusGone
			code = "session_closed"
		}
		writeError(w, status, code, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (s *Server) handleCancelTurn(w http.ResponseWriter, r *http.Request) {
	owned, ok := s.getSession(r.PathValue("session_id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session_not_found", "session not found")
		return
	}
	var input CancelTurnRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := owned.cancelTurn(input); err != nil {
		writeError(w, http.StatusConflict, "cancel_rejected", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleResolveInteraction(w http.ResponseWriter, r *http.Request) {
	owned, ok := s.getSession(r.PathValue("session_id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session_not_found", "session not found")
		return
	}
	var input ResolveInteractionRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	switch owned.resolveInteraction(r.PathValue("request_id"), input) {
	case interactionResolveAccepted:
		writeJSON(w, http.StatusOK, ResolveInteractionResponse{Accepted: true})
	case interactionResolveInvalid:
		writeError(
			w,
			http.StatusBadRequest,
			"invalid_interaction_result",
			"interaction result does not match the pending request",
		)
	default:
		writeError(w, http.StatusNotFound, "interaction_not_found", "interaction not found")
	}
}

func (s *Server) handleInteractionPlanReview(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" || r.ContentLength != 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "Plan review accepts no renderer-supplied identity")
		return
	}
	owned, ok := s.getSession(r.PathValue("session_id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session_not_found", "session not found")
		return
	}
	review, err := owned.interactionPlanReview(r.PathValue("request_id"))
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, review)
	case errors.Is(err, errInteractionNotFound):
		writeError(w, http.StatusNotFound, "interaction_not_found", "interaction not found")
	case errors.Is(err, errPlanReviewTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "plan_review_too_large", "Plan review is too large")
	case errors.Is(err, errPlanReviewChanged):
		writeError(w, http.StatusConflict, "plan_review_changed", "Plan review changed")
	default:
		writeError(w, http.StatusConflict, "plan_review_unavailable", "Plan review is unavailable")
	}
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	owned, ok := s.getSession(r.PathValue("session_id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session_not_found", "session not found")
		return
	}
	after, err := eventCursor(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_cursor", err.Error())
		return
	}
	replay, events, unsubscribe, gap, err := owned.events.subscribe(after)
	if err != nil {
		writeJSON(w, http.StatusConflict, gap)
		return
	}
	defer unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if err := writeAndFlushSSE(w, func(writer io.Writer) error {
		for _, event := range replay {
			if err := writeSSE(writer, event); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return
	}

	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case event, open := <-events:
			if !open {
				return
			}
			if err := writeAndFlushSSE(w, func(writer io.Writer) error {
				return writeSSE(writer, event)
			}); err != nil {
				return
			}
		case <-keepalive.C:
			if err := writeAndFlushSSE(w, func(writer io.Writer) error {
				_, err := io.WriteString(writer, ": keepalive\n\n")
				return err
			}); err != nil {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) getSession(id string) (*session, bool) {
	s.mu.RLock()
	owned, ok := s.sessions[id]
	s.mu.RUnlock()
	return owned, ok
}

func generateToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate app-server token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeJSON(r *http.Request, target any) error {
	encoded, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes+1))
	if err != nil {
		return fmt.Errorf("read JSON: %w", err)
	}
	if len(encoded) > maxRequestBytes {
		return fmt.Errorf("request body exceeds %d bytes", maxRequestBytes)
	}
	if err := rejectDuplicateJSONFields(encoded); err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("request body must contain one JSON value")
	}
	return nil
}

func rejectDuplicateJSONFields(encoded []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	if err := scanUniqueJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("request body must contain one JSON value")
	}
	return nil
}

func scanUniqueJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delimiter {
	case '{':
		fields := make(map[string]struct{})
		for decoder.More() {
			fieldToken, fieldErr := decoder.Token()
			if fieldErr != nil {
				return fieldErr
			}
			field, ok := fieldToken.(string)
			if !ok {
				return fmt.Errorf("JSON object field is not a string")
			}
			if _, duplicate := fields[field]; duplicate {
				return fmt.Errorf("request body contains a duplicate JSON field")
			}
			fields[field] = struct{}{}
			if err := scanUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return fmt.Errorf("invalid JSON object")
		}
	case '[':
		for decoder.More() {
			if err := scanUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return fmt.Errorf("invalid JSON array")
		}
	default:
		return fmt.Errorf("invalid JSON delimiter")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if status == http.StatusNoContent {
		return
	}
	if err := json.NewEncoder(w).Encode(value); err != nil {
		return
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, ErrorEnvelope{Error: APIError{Code: code, Message: message}})
}

func eventCursor(r *http.Request) (uint64, error) {
	value := strings.TrimSpace(r.URL.Query().Get("after"))
	if value == "" {
		value = strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	}
	if value == "" {
		return 0, nil
	}
	cursor, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("event cursor must be an unsigned integer")
	}
	return cursor, nil
}

func writeSSE(w io.Writer, event WireEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %d\nevent: event\ndata: ", event.ID); err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	_, err = io.WriteString(w, "\n\n")
	return err
}

func writeAndFlushSSE(w http.ResponseWriter, write func(io.Writer) error) error {
	controller := http.NewResponseController(w)
	deadlineSet := false
	if err := controller.SetWriteDeadline(time.Now().Add(5 * time.Second)); err == nil {
		deadlineSet = true
	} else if !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	if err := write(w); err != nil {
		return err
	}
	if err := controller.Flush(); err != nil {
		return err
	}
	if deadlineSet {
		return controller.SetWriteDeadline(time.Time{})
	}
	return nil
}

func validateCWD(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", fmt.Errorf("cwd is required")
	}
	if !filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("cwd must be an absolute path")
	}
	absolute, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("resolve cwd: %w", err)
	}
	clean := filepath.Clean(absolute)
	info, err := os.Stat(clean)
	if err != nil {
		return "", fmt.Errorf("stat cwd: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("cwd is not a directory")
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", fmt.Errorf("resolve cwd symlinks: %w", err)
	}
	return filepath.Clean(resolved), nil
}

func baseName(path string) string {
	base := strings.TrimSpace(filepath.Base(path))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return "Workspace"
	}
	return base
}
