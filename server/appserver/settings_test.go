package appserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	engineconfig "github.com/abietic/yhc/engine/config"
	"github.com/abietic/yhc/internal/webui"
)

func TestSharedSettingsHTTPAuthenticationAndOwnedWorkspace(t *testing.T) {
	server, httpServer := newSharedSettingsTestServer(t, false)
	endpoint := httpServer.URL + "/v1/settings"
	for _, method := range []string{http.MethodGet, http.MethodPatch} {
		request, err := http.NewRequest(method, endpoint, strings.NewReader(`{"revision":"missing","changes":{"max_turns":8}}`))
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		assertSharedSettingsStatus(t, response, http.StatusUnauthorized)
	}
	initialResponse := getBearer(t, endpoint, server.Token())
	defer initialResponse.Body.Close()
	initial := decodeSharedSettingsSnapshot(t, initialResponse)
	if initial.Scope != "user" || len(initial.Revision) != 64 || string(initial.Fields["max_turns"].StoredValue) != "null" {
		t.Fatalf("sessionless user settings = %+v", initial)
	}
	for _, invalid := range []struct {
		query  string
		status int
	}{
		{"?scope=project", http.StatusBadRequest},
		{"?scope=project&cwd=" + url.QueryEscape(t.TempDir()), http.StatusBadRequest},
		{"?scope=project&session_id=not-owned", http.StatusNotFound},
		{"?scope=user&session_id=not-owned", http.StatusNotFound},
		{"?scope=other", http.StatusBadRequest},
	} {
		readResponse := getBearer(t, endpoint+invalid.query, server.Token())
		defer readResponse.Body.Close()
		assertSharedSettingsStatus(t, readResponse, invalid.status)
		writeResponse := doJSON(t, endpoint+invalid.query, server.Token(), http.MethodPatch, map[string]any{
			"revision": initial.Revision, "changes": map[string]any{"max_turns": 8},
		})
		defer writeResponse.Body.Close()
		assertSharedSettingsStatus(t, writeResponse, invalid.status)
	}
	unknownFieldResponse := doJSON(t, endpoint, server.Token(), http.MethodPatch, map[string]any{
		"revision": initial.Revision, "changes": map[string]any{"max_turns": 8}, "cwd": t.TempDir(),
	})
	defer unknownFieldResponse.Body.Close()
	assertSharedSettingsStatus(t, unknownFieldResponse, http.StatusBadRequest)
	if _, err := os.Stat(engineconfig.UserConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("unauthorized/invalid requests wrote user settings: %v", err)
	}
}

func TestSharedSettingsHTTPPropagatesRequestCancellation(t *testing.T) {
	server, _ := newSharedSettingsTestServer(t, false)
	snapshot, err := engineconfig.SettingsSnapshot("", "user")
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"revision": snapshot.Revision,
		"changes":  map[string]any{"max_turns": 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	request := httptest.NewRequestWithContext(ctx, http.MethodPatch, "/v1/settings", strings.NewReader(string(body)))
	response := httptest.NewRecorder()
	server.handleUpdateSettings(response, request)
	if response.Code == http.StatusOK {
		t.Fatal("cancelled settings request reported a saved document")
	}
	if _, err := os.Stat(engineconfig.UserConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("cancelled HTTP request saved settings: %v", err)
	}
}

func TestSharedSettingsHTTPPersistsSharedValuesRedactsSecretsAndRejectsStaleRevision(t *testing.T) {
	server, httpServer := newSharedSettingsTestServer(t, false)
	project := t.TempDir()
	const secret = "settings-http-private-key-fixture"
	if err := os.MkdirAll(engineconfig.UserConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(engineconfig.UserConfigPath(), []byte(`{"api_key":"`+secret+`","api_base_url":"https://user:`+secret+`@example.test/v1?token=`+secret+`#private","max_turns":4}`), 0o600); err != nil {
		t.Fatal(err)
	}
	endpoint := httpServer.URL + "/v1/settings"
	response := getBearer(t, endpoint, server.Token())
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("get user settings = %d: %s", response.StatusCode, readBody(t, response))
	}
	body := readBody(t, response)
	if strings.Contains(body, secret) || strings.Contains(body, "api_key") || strings.Contains(body, "token=") {
		t.Fatalf("snapshot exposed secrets: %s", body)
	}
	var initial engineconfig.WorkbenchSettingsSnapshot
	if err := json.Unmarshal([]byte(body), &initial); err != nil {
		t.Fatal(err)
	}
	updatedResponse := doJSON(t, endpoint, server.Token(), http.MethodPatch, map[string]any{
		"revision": initial.Revision,
		"changes":  map[string]any{"max_turns": 10, "reduced_motion": true, "theme": "daybreak"},
	})
	defer updatedResponse.Body.Close()
	if updatedResponse.StatusCode != http.StatusOK {
		t.Fatalf("patch user settings = %d: %s", updatedResponse.StatusCode, readBody(t, updatedResponse))
	}
	updatedBody := readBody(t, updatedResponse)
	if strings.Contains(updatedBody, secret) || strings.Contains(updatedBody, "api_key") {
		t.Fatalf("patch response exposed secrets: %s", updatedBody)
	}
	var updated engineconfig.WorkbenchSettingsSnapshot
	if err := json.Unmarshal([]byte(updatedBody), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Revision == initial.Revision || string(updated.Fields["max_turns"].EffectiveValue) != "10" {
		t.Fatalf("user patch snapshot = %+v", updated)
	}
	savedBeforeConflict, err := os.ReadFile(engineconfig.UserConfigPath())
	if err != nil || !strings.Contains(string(savedBeforeConflict), secret) {
		t.Fatalf("private field was not preserved: %v", err)
	}
	stale := doJSON(t, endpoint, server.Token(), http.MethodPatch, map[string]any{
		"revision": initial.Revision, "changes": map[string]any{"max_turns": 99},
	})
	defer stale.Body.Close()
	assertSharedSettingsStatus(t, stale, http.StatusConflict)
	savedAfterConflict, err := os.ReadFile(engineconfig.UserConfigPath())
	if err != nil || string(savedAfterConflict) != string(savedBeforeConflict) {
		t.Fatalf("stale patch overwrote settings: %v", err)
	}
	registered := registerWorkspace(t, httpServer.URL, server.Token(), project)
	created := doJSON(t, httpServer.URL+"/v1/sessions", server.Token(), http.MethodPost, map[string]string{"workspace_handle": registered.WorkspaceHandle})
	defer created.Body.Close()
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create owned project session = %d: %s", created.StatusCode, readBody(t, created))
	}
	var summary SessionSummary
	decodeResponse(t, created, &summary)
	projectEndpoint := endpoint + "?scope=project&session_id=" + url.QueryEscape(summary.ID)
	projectInitialResponse := getBearer(t, projectEndpoint, server.Token())
	defer projectInitialResponse.Body.Close()
	projectInitial := decodeSharedSettingsSnapshot(t, projectInitialResponse)
	projectUpdatedResponse := doJSON(t, projectEndpoint, server.Token(), http.MethodPatch, map[string]any{
		"revision": projectInitial.Revision,
		"changes":  map[string]any{"max_turns": 0, "reduced_motion": false, "theme": "polar-night"},
	})
	defer projectUpdatedResponse.Body.Close()
	projectUpdated := decodeSharedSettingsSnapshot(t, projectUpdatedResponse)
	if projectUpdated.Scope != "project" || projectUpdated.Fields["max_turns"].Source != "project" ||
		string(projectUpdated.Fields["max_turns"].StoredValue) != "0" || string(projectUpdated.Fields["reduced_motion"].StoredValue) != "false" {
		t.Fatalf("project explicit zero/false = %+v", projectUpdated)
	}
	effective, err := engineconfig.LoadEffectiveConfig(project)
	if err != nil || effective.MaxTurns != 0 || effective.ReducedMotion || effective.Theme != "polar-night" {
		t.Fatalf("runtime did not consume HTTP settings: config=%+v err=%v", effective, err)
	}
	resetResponse := doJSON(t, projectEndpoint, server.Token(), http.MethodPatch, map[string]any{
		"revision": projectUpdated.Revision,
		"changes":  map[string]any{"max_turns": nil, "reduced_motion": nil, "theme": nil},
	})
	defer resetResponse.Body.Close()
	reset := decodeSharedSettingsSnapshot(t, resetResponse)
	if reset.Fields["max_turns"].Source != "user" || string(reset.Fields["max_turns"].StoredValue) != "null" {
		t.Fatalf("project reset = %+v", reset)
	}
	effective, err = engineconfig.LoadEffectiveConfig(project)
	if err != nil || effective.MaxTurns != 10 || !effective.ReducedMotion || effective.Theme != "daybreak" {
		t.Fatalf("runtime did not inherit user settings after reset: config=%+v err=%v", effective, err)
	}
}

func TestSharedSettingsHTTPBrowserMutationRequiresOriginAndCSRF(t *testing.T) {
	server, httpServer := newSharedSettingsTestServer(t, true)
	pairing, _, err := server.browserAuth.newPairing()
	if err != nil {
		t.Fatal(err)
	}
	token, browser, err := server.browserAuth.exchange(pairing)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := httpServer.URL + "/v1/settings"
	request := func(method, origin, csrf, body string) *http.Response {
		t.Helper()
		request, err := http.NewRequest(method, endpoint, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.AddCookie(&http.Cookie{Name: browserSessionCookieName, Value: token})
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", origin)
		request.Header.Set("X-YHC-CSRF", csrf)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	initialResponse := request(http.MethodGet, "", "", "")
	defer initialResponse.Body.Close()
	initial := decodeSharedSettingsSnapshot(t, initialResponse)
	body := `{"revision":"` + initial.Revision + `","changes":{"max_turns":6}}`
	for _, invalid := range []struct{ origin, csrf string }{
		{httpServer.URL, ""},
		{httpServer.URL, "incorrect-csrf"},
		{"", browser.csrfToken},
		{"https://attacker.invalid", browser.csrfToken},
	} {
		invalidResponse := request(http.MethodPatch, invalid.origin, invalid.csrf, body)
		defer invalidResponse.Body.Close()
		assertSharedSettingsStatus(t, invalidResponse, http.StatusForbidden)
	}
	if _, err := os.Stat(engineconfig.UserConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("CSRF-rejected request wrote settings: %v", err)
	}
	updatedResponse := request(http.MethodPatch, httpServer.URL, browser.csrfToken, body)
	defer updatedResponse.Body.Close()
	updated := decodeSharedSettingsSnapshot(t, updatedResponse)
	if string(updated.Fields["max_turns"].StoredValue) != "6" {
		t.Fatalf("authorized browser patch = %+v", updated)
	}
	user, err := engineconfig.LoadUserConfig()
	if err != nil || user.MaxTurns != 6 {
		t.Fatalf("browser patch did not reach shared user settings: config=%+v err=%v", user, err)
	}
}

func newSharedSettingsTestServer(t *testing.T, web bool) (*Server, *httptest.Server) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	server, err := New(Config{
		Token: "settings-test-token", EnableWeb: web,
		DiscoveryCWD:       t.TempDir(),
		SessionCatalogPath: filepath.Join(t.TempDir(), "session-roots.json"),
		WebAssets:          webui.Assets,
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			return newFakeSessionEngine(input, false), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownTestServer(t, server) })
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	if _, err := server.BootstrapFor(httpServer.Listener); err != nil {
		t.Fatal(err)
	}
	return server, httpServer
}

func decodeSharedSettingsSnapshot(t *testing.T, response *http.Response) engineconfig.WorkbenchSettingsSnapshot {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("settings status = %d: %s", response.StatusCode, readBody(t, response))
	}
	var snapshot engineconfig.WorkbenchSettingsSnapshot
	decodeResponse(t, response, &snapshot)
	return snapshot
}

func assertSharedSettingsStatus(t *testing.T, response *http.Response, want int) {
	t.Helper()
	defer response.Body.Close()
	body := readBody(t, response)
	if response.StatusCode != want {
		t.Fatalf("settings status = %d, want %d: %s", response.StatusCode, want, body)
	}
}
