package appserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceHandleRegistersAndCreatesOneSession(t *testing.T) {
	server := newWorkspaceHandleTestServer(t, time.Now)
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	t.Cleanup(func() { shutdownTestServer(t, server) })
	registered := registerWorkspace(t, httpServer.URL, server.Token(), t.TempDir())
	created := doJSON(t, httpServer.URL+"/v1/sessions", server.Token(), http.MethodPost, map[string]string{"workspace_handle": registered.WorkspaceHandle})
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d: %s", created.StatusCode, readBody(t, created))
	}
	var summary SessionSummary
	decodeResponse(t, created, &summary)
	_ = created.Body.Close()
	if summary.WorkspaceLabel != registered.WorkspaceLabel || summary.CWD != "" || summary.Title != "" {
		t.Fatalf("unsafe session summary = %+v", summary)
	}
	owned, ok := server.getSession(summary.ID)
	if !ok {
		t.Fatal("created session was not owned")
	}
	events, _, unsubscribe, _, err := owned.events.subscribe(0)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	if len(events) == 0 || strings.Contains(string(events[0].Data), "cwd") || strings.Contains(string(events[0].Data), registered.WorkspaceHandle) {
		t.Fatalf("unsafe synthetic event = %+v", events)
	}
	reused := doJSON(t, httpServer.URL+"/v1/sessions", server.Token(), http.MethodPost, map[string]string{"workspace_handle": registered.WorkspaceHandle})
	if reused.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("reused handle = %d: %s", reused.StatusCode, readBody(t, reused))
	}
	_ = reused.Body.Close()
}

func TestWorkspaceHandleCanRetryAfterSessionCreationFailure(t *testing.T) {
	var attempts int
	server, err := New(Config{Token: "workspace-retry-token", Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
		attempts++
		if attempts == 1 {
			return nil, errors.New("synthetic first-attempt failure")
		}
		return newFakeSessionEngine(input, false), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	t.Cleanup(func() { shutdownTestServer(t, server) })
	registered := registerWorkspace(t, httpServer.URL, server.Token(), t.TempDir())
	create := func() *http.Response {
		return doJSON(t, httpServer.URL+"/v1/sessions", server.Token(), http.MethodPost, map[string]string{"workspace_handle": registered.WorkspaceHandle})
	}
	failed := create()
	if failed.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("first create = %d: %s", failed.StatusCode, readBody(t, failed))
	}
	_ = failed.Body.Close()
	retried := create()
	if retried.StatusCode != http.StatusCreated {
		t.Fatalf("retried create = %d: %s", retried.StatusCode, readBody(t, retried))
	}
	_ = retried.Body.Close()
	if attempts != 2 {
		t.Fatalf("factory attempts = %d, want 2", attempts)
	}
}

func TestWorkspaceHandleRejectsBrowserAndStrictCreateInput(t *testing.T) {
	server := newWorkspaceHandleTestServer(t, time.Now)
	request := httptest.NewRequest(http.MethodPost, "/v1/workspaces", strings.NewReader(`{"cwd":"/tmp"}`))
	request = request.WithContext(context.WithValue(request.Context(), requestPrincipalKey{}, requestPrincipal{browserSession: &browserSession{}}))
	response := httptest.NewRecorder()
	server.handleRegisterWorkspace(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("browser workspace registration = %d: %s", response.Code, response.Body.String())
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	t.Cleanup(func() { shutdownTestServer(t, server) })
	invalid := doJSON(t, httpServer.URL+"/v1/sessions", server.Token(), http.MethodPost, map[string]string{"cwd": t.TempDir()})
	if invalid.StatusCode != http.StatusBadRequest {
		t.Fatalf("path-bearing create = %d: %s", invalid.StatusCode, readBody(t, invalid))
	}
	_ = invalid.Body.Close()
	unknown := doJSON(t, httpServer.URL+"/v1/sessions", server.Token(), http.MethodPost, map[string]string{"workspace_handle": "missing-handle"})
	if unknown.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("unknown handle = %d: %s", unknown.StatusCode, readBody(t, unknown))
	}
	_ = unknown.Body.Close()
}

func TestWorkspaceHandleExpiresAndDoesNotLeakPaths(t *testing.T) {
	now := time.Now().UTC()
	server := newWorkspaceHandleTestServer(t, func() time.Time { return now })
	server.workspaceHandleTTL = time.Second
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	t.Cleanup(func() { shutdownTestServer(t, server) })
	sentinel := t.TempDir()
	registered := registerWorkspace(t, httpServer.URL, server.Token(), sentinel)
	now = now.Add(2 * time.Second)
	expired := doJSON(t, httpServer.URL+"/v1/sessions", server.Token(), http.MethodPost, map[string]string{"workspace_handle": registered.WorkspaceHandle})
	if expired.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expired handle = %d: %s", expired.StatusCode, readBody(t, expired))
	}
	body := readBody(t, expired)
	_ = expired.Body.Close()
	if strings.Contains(body, sentinel) {
		t.Fatalf("expired response leaked path: %q", body)
	}
}

func TestWorkspaceHandleSafeReviewProjection(t *testing.T) {
	server := newWorkspaceHandleTestServer(t, time.Now)
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	t.Cleanup(func() { shutdownTestServer(t, server) })
	sentinel := t.TempDir()
	registered := registerWorkspace(t, httpServer.URL, server.Token(), sentinel)
	created := doJSON(t, httpServer.URL+"/v1/sessions", server.Token(), http.MethodPost, map[string]string{"workspace_handle": registered.WorkspaceHandle})
	var summary SessionSummary
	decodeResponse(t, created, &summary)
	_ = created.Body.Close()
	review := getBearer(t, httpServer.URL+"/v1/sessions/"+summary.ID+"/review-diff", server.Token())
	if review.StatusCode != http.StatusOK {
		t.Fatalf("review = %d: %s", review.StatusCode, readBody(t, review))
	}
	payload := readBody(t, review)
	_ = review.Body.Close()
	if strings.Contains(payload, sentinel) || strings.Contains(payload, `"cwd"`) || strings.Contains(payload, `"repository_root"`) {
		t.Fatalf("review projection leaked path: %s", payload)
	}
}

func newWorkspaceHandleTestServer(t *testing.T, now func() time.Time) *Server {
	t.Helper()
	server, err := New(Config{Token: "workspace-test-token", Now: now, Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
		return newFakeSessionEngine(input, false), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func registerWorkspace(t *testing.T, baseURL, token, cwd string) RegisterWorkspaceResponse {
	t.Helper()
	response := doJSON(t, baseURL+"/v1/workspaces", token, http.MethodPost, RegisterWorkspaceRequest{CWD: cwd})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("register workspace = %d: %s", response.StatusCode, readBody(t, response))
	}
	var registered RegisterWorkspaceResponse
	decodeResponse(t, response, &registered)
	_ = response.Body.Close()
	return registered
}

func TestWorkspaceDTOsDoNotMarshalLegacyPaths(t *testing.T) {
	sentinel := "/private/workspace/sentinel"
	session, err := json.Marshal(SessionSummary{WorkspaceLabel: "sentinel", CWD: sentinel, Title: sentinel})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(session, []byte(sentinel)) || bytes.Contains(session, []byte(`"cwd"`)) || bytes.Contains(session, []byte(`"title"`)) {
		t.Fatalf("unsafe session DTO JSON: %s", session)
	}
	payload, err := json.Marshal(ReviewDiffResponse{WorkspaceLabel: "sentinel", CWD: sentinel, Sources: []ReviewDiffSource{{WorkspaceLabel: "sentinel", RepositoryRoot: sentinel}}})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte(sentinel)) || bytes.Contains(payload, []byte(`"cwd"`)) || bytes.Contains(payload, []byte(`"repository_root"`)) {
		t.Fatalf("unsafe DTO JSON: %s", payload)
	}
	durable, err := json.Marshal(DurableSessionSummary{WorkspaceLabel: "sentinel", CWD: sentinel, Title: sentinel})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(durable, []byte(sentinel)) || bytes.Contains(durable, []byte(`"cwd"`)) || bytes.Contains(durable, []byte(`"title"`)) {
		t.Fatalf("unsafe durable DTO JSON: %s", durable)
	}
}
