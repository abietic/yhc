package appserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	enginesession "github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/abietic/yhc/internal/statepath"
)

func TestImportDurableSessionRequiresAttestationWithoutFactoryOrLease(t *testing.T) {
	fixture := newDurableImportFixture(t)
	var factoryCalls atomic.Int32
	server, err := New(Config{
		Token:              "test-token",
		SessionCatalogPath: fixture.canonicalCatalog,
		DiscoveryCWD:       fixture.project,
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			factoryCalls.Add(1)
			return newFakeSessionEngine(input, false), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	response := doJSON(
		t,
		httpServer.URL+"/v1/durable-sessions/"+fixture.sessionID+"/import",
		"test-token",
		http.MethodPost,
		ImportDurableSessionRequest{},
	)
	if response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("unattested import status = %d, body=%s", response.StatusCode, readBody(t, response))
	}
	if factoryCalls.Load() != 0 {
		t.Fatalf("factory calls = %d, want no runtime construction", factoryCalls.Load())
	}
	leasePath := filepath.Join(fixture.canonicalDir, fixture.sessionID, ".app-server.lock")
	if _, err := os.Lstat(leasePath); !os.IsNotExist(err) {
		t.Fatalf("unattested import lease = %v, want no lease", err)
	}
}

func TestImportDurableSessionRejectsUnknownFieldsAndImportsWithoutRuntime(t *testing.T) {
	fixture := newDurableImportFixture(t)
	var factoryCalls atomic.Int32
	server, err := New(Config{
		Token:              "test-token",
		SessionCatalogPath: fixture.canonicalCatalog,
		DiscoveryCWD:       fixture.project,
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			factoryCalls.Add(1)
			return newFakeSessionEngine(input, false), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	request, err := http.NewRequest(
		http.MethodPost,
		httpServer.URL+"/v1/durable-sessions/"+fixture.sessionID+"/import",
		strings.NewReader(`{"confirm_legacy_stopped":true,"cwd":"/attacker"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer test-token")
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown-field import status = %d, body=%s", response.StatusCode, readBody(t, response))
	}

	response = doJSON(
		t,
		httpServer.URL+"/v1/durable-sessions/"+fixture.sessionID+"/import",
		"test-token",
		http.MethodPost,
		ImportDurableSessionRequest{ConfirmLegacyStopped: true},
	)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("import status = %d, body=%s", response.StatusCode, readBody(t, response))
	}
	var imported ImportDurableSessionResponse
	decodeResponse(t, response, &imported)
	if imported.SessionID != fixture.sessionID || imported.Status != "imported" {
		t.Fatalf("import response = %#v", imported)
	}
	if factoryCalls.Load() != 0 {
		t.Fatalf("factory calls = %d, want no runtime construction", factoryCalls.Load())
	}
	leasePath := filepath.Join(fixture.canonicalDir, fixture.sessionID, ".app-server.lock")
	if _, err := os.Lstat(leasePath); !os.IsNotExist(err) {
		t.Fatalf("import lease = %v, want no lease", err)
	}
}

func TestImportDurableSessionRejectsCanonicalInputWithoutFactory(t *testing.T) {
	fixture := newDurableImportFixture(t)
	canonicalRecorder := transcript.NewRecorder(fixture.sessionID, fixture.canonicalDir)
	if err := canonicalRecorder.Record([]*schema.Message{{Role: schema.User, Content: "already canonical"}}, false); err != nil {
		t.Fatal(err)
	}
	if err := canonicalRecorder.Close(); err != nil {
		t.Fatal(err)
	}
	if err := enginesession.RegisterSessionRoot(
		fixture.canonicalCatalog,
		fixture.project,
		fixture.canonicalDir,
		time.Now().UTC(),
	); err != nil {
		t.Fatal(err)
	}
	var factoryCalls atomic.Int32
	server, err := New(Config{
		Token:              "test-token",
		SessionCatalogPath: fixture.canonicalCatalog,
		DiscoveryCWD:       fixture.project,
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			factoryCalls.Add(1)
			return newFakeSessionEngine(input, false), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	response := doJSON(
		t,
		httpServer.URL+"/v1/durable-sessions/"+fixture.sessionID+"/import",
		"test-token",
		http.MethodPost,
		ImportDurableSessionRequest{ConfirmLegacyStopped: true},
	)
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("canonical import status = %d, body=%s", response.StatusCode, readBody(t, response))
	}
	if factoryCalls.Load() != 0 {
		t.Fatalf("factory calls = %d, want no runtime construction", factoryCalls.Load())
	}
}

type durableImportFixture struct {
	project          string
	canonicalDir     string
	canonicalCatalog string
	sessionID        string
}

func newDurableImportFixture(t *testing.T) durableImportFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := t.TempDir()
	roots, err := statepath.UserRoots(home)
	if err != nil {
		t.Fatal(err)
	}
	sessionID := "legacy-import"
	legacyDir := filepath.Join(project, ".eino-agent", "transcripts")
	recorder := transcript.NewRecorder(sessionID, legacyDir)
	if err := recorder.Record([]*schema.Message{{Role: schema.User, Content: "preserve legacy"}}, false); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	legacyCatalog := filepath.Join(roots.Legacy, "session-roots.json")
	if err := enginesession.RegisterSessionRoot(legacyCatalog, project, legacyDir, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	return durableImportFixture{
		project:          project,
		canonicalDir:     filepath.Join(project, ".yhc", "transcripts"),
		canonicalCatalog: filepath.Join(roots.Canonical, "session-roots.json"),
		sessionID:        sessionID,
	}
}
