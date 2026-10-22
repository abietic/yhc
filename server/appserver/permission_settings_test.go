package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/config"
	"github.com/abietic/yhc/engine/permission"
)

type permissionSettingsEngine struct {
	*taskExplorerEngine
	controller *engine.QueryEngine
	entered    chan struct{}
	release    chan struct{}
}

func (e *permissionSettingsEngine) PermissionRuleSettings(scope string) (permission.RuleSettings, error) {
	return e.controller.PermissionRuleSettings(scope)
}

func (e *permissionSettingsEngine) ApplyPermissionRuleSettings(ctx context.Context, scope, revision string, changes []permission.RuleSettingsChange) (engine.PermissionRuleSettingsResult, error) {
	if e.entered != nil {
		close(e.entered)
		<-e.release
	}
	return e.controller.ApplyPermissionRuleSettings(ctx, scope, revision, changes)
}

func TestPermissionSettingsRetiredPointerAndOwnershipLease(t *testing.T) {
	e := &permissionSettingsEngine{taskExplorerEngine: &taskExplorerEngine{fakeSessionEngine: newFakeSessionEngine(EngineOptions{}, false)}}
	s, _ := newTaskExplorerServer(t, e)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	root := t.TempDir()
	e.controller = engine.NewQueryEngine(engine.QueryEngineConfig{CWD: root, PermissionProjectRoot: root, TranscriptDir: t.TempDir()})
	t.Cleanup(e.controller.Close)
	snapshot, err := e.controller.PermissionRuleSettings("local")
	if err != nil {
		t.Fatal(err)
	}
	owned := s.sessions["owned-session"]
	input := permissionSettingsRequest{Revision: snapshot.Revision, Changes: []permission.RuleSettingsChange{{Operation: "add", Action: permission.ActionDeny, Rule: "Write"}}}
	// A request retained this pointer before DELETE removed registry ownership.
	s.mu.Lock()
	delete(s.sessions, owned.id)
	s.mu.Unlock()
	_, err = s.permissionSettingsResult(context.Background(), "local", owned, true, input)
	if !errors.Is(err, errSessionClosed) {
		t.Fatalf("retired pointer admitted=%v", err)
	}
	if _, err := os.Stat(config.ProjectLocalConfigPath(root)); !os.IsNotExist(err) {
		t.Fatal("retired session persisted rules")
	}
	s.mu.Lock()
	s.sessions[owned.id] = owned
	s.mu.Unlock()
	e.entered = make(chan struct{})
	e.release = make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := s.permissionSettingsResult(context.Background(), "local", owned, true, input)
		done <- err
	}()
	<-e.entered
	// DELETE/Shutdown require the same exclusive registry lock. A mutation
	// already admitted therefore linearizes before their ownership removal.
	if s.mu.TryLock() {
		s.mu.Unlock()
		close(e.release)
		<-done
		t.Fatal("ownership removal crossed live mutation")
	}
	close(e.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !s.mu.TryLock() {
		t.Fatal("ownership read lease leaked")
	}
	s.mu.Unlock()
}

func TestPermissionSettingsHTTPUserWithoutSessionAndStrictParameters(t *testing.T) {
	s, hs := newSharedSettingsTestServer(t, false)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	endpoint := hs.URL + "/v1/permission-settings"
	r := getBearer(t, endpoint, s.Token())
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("initial status=%d", r.StatusCode)
	}
	var initial permission.RuleSettings
	decodeResponse(t, r, &initial)
	if len(initial.Revision) != 64 || initial.Scope != "user" {
		t.Fatal("invalid projection")
	}
	updated := doJSON(t, endpoint, s.Token(), http.MethodPatch, map[string]any{"revision": initial.Revision, "changes": []permission.RuleSettingsChange{{Operation: "add", Action: permission.ActionAsk, Rule: "Bash(go test *)"}}})
	defer updated.Body.Close()
	if updated.StatusCode != http.StatusOK {
		t.Fatalf("save=%d", updated.StatusCode)
	}
	var wire struct {
		permission.RuleSettings
		RuntimeApplied bool   `json:"runtime_applied"`
		SessionID      string `json:"session_id"`
	}
	decodeResponse(t, updated, &wire)
	if wire.RuntimeApplied || wire.SessionID != "" || len(wire.Rules) != 1 {
		t.Fatal("sessionless write claimed runtime application")
	}
	for _, query := range []string{"?scope=local", "?scope=project", "?scope=other", "?cwd=private", "?scope=user&scope=local", "?scope=user;path=private", "?session_id=", "?scope="} {
		r := getBearer(t, endpoint+query, s.Token())
		defer r.Body.Close()
		assertSharedSettingsStatus(t, r, http.StatusBadRequest)
	}
	r = getBearer(t, endpoint+"?session_id=unknown", s.Token())
	defer r.Body.Close()
	assertSharedSettingsStatus(t, r, http.StatusNotFound)
	r, err := http.Get(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	assertSharedSettingsStatus(t, r, http.StatusUnauthorized)
	unknown := doJSON(t, endpoint, s.Token(), http.MethodPatch, map[string]any{"revision": wire.Revision, "changes": []permission.RuleSettingsChange{{Operation: "remove", Action: permission.ActionAsk, Rule: "Bash(go test *)"}}, "path": "private"})
	defer unknown.Body.Close()
	assertSharedSettingsStatus(t, unknown, http.StatusBadRequest)
	stale := doJSON(t, endpoint, s.Token(), http.MethodPatch, map[string]any{"revision": initial.Revision, "changes": []permission.RuleSettingsChange{{Operation: "add", Action: permission.ActionDeny, Rule: "Write"}}})
	defer stale.Body.Close()
	assertSharedSettingsStatus(t, stale, http.StatusConflict)
}

func TestPermissionSettingsHTTPOwnedIdleEngineAndClosedAdmission(t *testing.T) {
	fake := &permissionSettingsEngine{taskExplorerEngine: &taskExplorerEngine{fakeSessionEngine: newFakeSessionEngine(EngineOptions{}, false)}}
	s, hs := newTaskExplorerServer(t, fake)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	root := t.TempDir()
	fake.controller = engine.NewQueryEngine(engine.QueryEngineConfig{CWD: root, PermissionProjectRoot: root, TranscriptDir: t.TempDir()})
	t.Cleanup(fake.controller.Close)
	endpoint := hs.URL + "/v1/permission-settings?scope=local&session_id=owned-session"
	r := taskExplorerRequest(t, endpoint, http.MethodGet)
	defer r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("read=%d", r.StatusCode)
	}
	var snapshot permission.RuleSettings
	decodeResponse(t, r, &snapshot)
	body := map[string]any{"revision": snapshot.Revision, "changes": []permission.RuleSettingsChange{{Operation: "add", Action: permission.ActionDeny, Rule: "Write"}}}
	owned := s.sessions["owned-session"]
	owned.mu.Lock()
	owned.activeTurnID = "active-turn"
	owned.mu.Unlock()
	r = doJSON(t, endpoint, s.Token(), http.MethodPatch, body)
	defer r.Body.Close()
	assertSharedSettingsStatus(t, r, http.StatusConflict)
	if _, err := os.Stat(config.ProjectLocalConfigPath(root)); !os.IsNotExist(err) {
		t.Fatal("active turn wrote configuration")
	}
	owned.mu.Lock()
	owned.activeTurnID = ""
	owned.mu.Unlock()
	r = doJSON(t, endpoint, s.Token(), http.MethodPatch, body)
	defer r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("save=%d", r.StatusCode)
	}
	var wire struct {
		engine.PermissionRuleSettingsResult
		SessionID string `json:"session_id"`
	}
	decodeResponse(t, r, &wire)
	if wire.SessionID != "owned-session" || !wire.RuntimeApplied || len(wire.Rules) != 1 {
		t.Fatal("owned runtime not applied")
	}
	owned.mu.Lock()
	owned.closed = true
	owned.mu.Unlock()
	for _, method := range []string{http.MethodGet, http.MethodPatch} {
		r = doJSON(t, endpoint, s.Token(), method, body)
		defer r.Body.Close()
		assertSharedSettingsStatus(t, r, http.StatusNotFound)
	}
	if fake.mutations.Load() != 0 || fake.calls.Load() != 0 {
		t.Fatal("settings entered a conversation/task mutation")
	}
}

func TestPermissionSettingsHTTPUnsupportedAdapterAndLiteralErrors(t *testing.T) {
	e := &taskExplorerEngine{fakeSessionEngine: newFakeSessionEngine(EngineOptions{}, false)}
	s, hs := newTaskExplorerServer(t, e)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	endpoint := hs.URL + "/v1/permission-settings?scope=user&session_id=owned-session"
	for _, method := range []string{http.MethodGet, http.MethodPatch} {
		r := doJSON(t, endpoint, s.Token(), method, map[string]any{"revision": strings.Repeat("0", 64), "changes": []permission.RuleSettingsChange{{Operation: "add", Action: permission.ActionAllow, Rule: "Read"}}})
		defer r.Body.Close()
		assertSharedSettingsStatus(t, r, http.StatusNotImplemented)
	}
	if _, err := os.Stat(config.UserConfigPath()); !os.IsNotExist(err) {
		t.Fatal("unsupported adapter persisted rules")
	}
	plain := hs.URL + "/v1/permission-settings"
	r := doJSON(t, plain, s.Token(), http.MethodPatch, map[string]any{"revision": strings.Repeat("0", 64), "changes": []permission.RuleSettingsChange{{Operation: "add", Action: "fixture-private-action", Rule: "fixture-private-rule"}}})
	defer r.Body.Close()
	if r.StatusCode != 422 || strings.Contains(readBody(t, r), "fixture-private") {
		t.Fatal("invalid request leaked raw values")
	}
	// Authenticated browser clients still require both same-origin and CSRF.
	pairing, _, err := s.browserAuth.newPairing()
	if err != nil {
		t.Fatal(err)
	}
	token, browser, err := s.browserAuth.exchange(pairing)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(map[string]any{"revision": strings.Repeat("0", 64), "changes": []permission.RuleSettingsChange{{Operation: "add", Action: permission.ActionAllow, Rule: "Read"}}})
	req, err := http.NewRequest(http.MethodPatch, plain, strings.NewReader(string(encoded)))
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: browserSessionCookieName, Value: token})
	req.Header.Set("Origin", hs.URL)
	r, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	assertSharedSettingsStatus(t, r, http.StatusForbidden)
	req, err = http.NewRequest(http.MethodPatch, plain, strings.NewReader(string(encoded)))
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: browserSessionCookieName, Value: token})
	req.Header.Set("Origin", "https://external.invalid")
	req.Header.Set("X-YHC-CSRF", browser.csrfToken)
	r, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	assertSharedSettingsStatus(t, r, http.StatusForbidden)
}
