package appserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	enginesession "github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
)

func TestDurableSessionsDiscoverRegisteredRootsWithBoundedPaging(t *testing.T) {
	catalogDir := t.TempDir()
	catalogPath := filepath.Join(catalogDir, "session-roots.json")
	firstRoot := t.TempDir()
	secondRoot := t.TempDir()
	firstTranscriptDir := filepath.Join(firstRoot, ".eino-agent", "transcripts")
	secondTranscriptDir := filepath.Join(secondRoot, ".eino-agent", "transcripts")
	base := time.Date(2026, 7, 27, 1, 0, 0, 0, time.UTC)

	writeDurableSession(
		t,
		firstTranscriptDir,
		"11111111-1111-4111-8111-111111111111",
		firstRoot,
		"older desktop session",
		"main",
	)
	writeDurableSession(
		t,
		secondTranscriptDir,
		"22222222-2222-4222-8222-222222222222",
		secondRoot,
		"newer desktop session",
		"feat/desktop",
	)
	if err := enginesession.RegisterSessionRoot(
		catalogPath,
		firstRoot,
		firstTranscriptDir,
		base,
	); err != nil {
		t.Fatal(err)
	}
	if err := enginesession.RegisterSessionRoot(
		catalogPath,
		secondRoot,
		secondTranscriptDir,
		base.Add(time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	newerPath := filepath.Join(
		secondTranscriptDir,
		"22222222-2222-4222-8222-222222222222.jsonl",
	)
	if err := touchFile(newerPath, base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	olderPath := filepath.Join(
		firstTranscriptDir,
		"11111111-1111-4111-8111-111111111111.jsonl",
	)
	if err := touchFile(olderPath, base); err != nil {
		t.Fatal(err)
	}

	server, err := New(Config{
		Token: "test-token",
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			return newFakeSessionEngine(input, false), nil
		},
		SessionCatalogPath: catalogPath,
		DiscoveryCWD:       catalogDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	defer shutdownTestServer(t, server)

	first := getBearer(
		t,
		httpServer.URL+"/v1/durable-sessions?limit=1",
		"test-token",
	)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first page = %d: %s", first.StatusCode, readBody(t, first))
	}
	var firstPage DurableSessionListResponse
	decodeResponse(t, first, &firstPage)
	if len(firstPage.Sessions) != 1 ||
		firstPage.Sessions[0].ID != "22222222-2222-4222-8222-222222222222" ||
		firstPage.Sessions[0].CWD != "" ||
		firstPage.Sessions[0].Title != "" ||
		firstPage.Sessions[0].GitBranch != "feat/desktop" ||
		!firstPage.Sessions[0].Resumable ||
		!firstPage.HasMore ||
		firstPage.NextCursor == "" {
		t.Fatalf("first page = %+v", firstPage)
	}

	second := getBearer(
		t,
		httpServer.URL+"/v1/durable-sessions?limit=1&cursor="+firstPage.NextCursor,
		"test-token",
	)
	if second.StatusCode != http.StatusOK {
		t.Fatalf("second page = %d: %s", second.StatusCode, readBody(t, second))
	}
	var secondPage DurableSessionListResponse
	decodeResponse(t, second, &secondPage)
	if len(secondPage.Sessions) != 1 ||
		secondPage.Sessions[0].ID != "11111111-1111-4111-8111-111111111111" ||
		secondPage.HasMore {
		t.Fatalf("second page = %+v", secondPage)
	}

	search := getBearer(
		t,
		httpServer.URL+"/v1/durable-sessions?search=newer",
		"test-token",
	)
	var searchPage DurableSessionListResponse
	decodeResponse(t, search, &searchPage)
	_ = search.Body.Close()
	if len(searchPage.Sessions) != 1 ||
		searchPage.Sessions[0].ID != firstPage.Sessions[0].ID {
		t.Fatalf("search page = %+v", searchPage)
	}
}

func TestDurableSessionsRejectInvalidBoundsAndMissingCatalogIsEmpty(t *testing.T) {
	discoveryCWD := t.TempDir()
	server, err := New(Config{
		Token: "test-token",
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			return newFakeSessionEngine(input, false), nil
		},
		SessionCatalogPath: filepath.Join(discoveryCWD, "missing-session-roots.json"),
		DiscoveryCWD:       discoveryCWD,
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	defer shutdownTestServer(t, server)

	empty := getBearer(t, httpServer.URL+"/v1/durable-sessions", "test-token")
	var page DurableSessionListResponse
	decodeResponse(t, empty, &page)
	_ = empty.Body.Close()
	if len(page.Sessions) != 0 || page.HasMore {
		t.Fatalf("empty page = %+v", page)
	}

	invalid := getBearer(
		t,
		httpServer.URL+"/v1/durable-sessions?limit=101",
		"test-token",
	)
	if invalid.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid limit = %d: %s", invalid.StatusCode, readBody(t, invalid))
	}
	_ = invalid.Body.Close()
}

func TestDurableTranscriptReadsTrustedDescriptorWithoutLiveSession(t *testing.T) {
	catalogDir := t.TempDir()
	catalogPath := filepath.Join(catalogDir, "session-roots.json")
	root := t.TempDir()
	transcriptDir := filepath.Join(root, ".eino-agent", "transcripts")
	sessionID := "33333333-3333-4333-8333-333333333333"
	writeDurableSession(t, transcriptDir, sessionID, "/untrusted/renderer-cwd", "detached history", "main")
	if err := enginesession.RegisterSessionRoot(catalogPath, root, transcriptDir, time.Now()); err != nil {
		t.Fatal(err)
	}

	factoryCalls := 0
	resumeValidations := 0
	server, err := New(Config{
		Token: "test-token",
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			factoryCalls++
			return newFakeSessionEngine(input, false), nil
		},
		ValidateResume: func(context.Context, EngineOptions) error {
			resumeValidations++
			return nil
		},
		SessionCatalogPath: catalogPath,
		DiscoveryCWD:       catalogDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	defer shutdownTestServer(t, server)

	response := getBearer(
		t,
		httpServer.URL+"/v1/durable-sessions/"+sessionID+"/transcript",
		"test-token",
	)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("durable transcript = %d: %s", response.StatusCode, readBody(t, response))
	}
	var page TranscriptPageResponse
	decodeResponse(t, response, &page)
	if len(page.Messages) != 2 || page.Messages[0].Content != "detached history" {
		t.Fatalf("durable transcript page = %+v", page)
	}
	if factoryCalls != 0 || resumeValidations != 0 || len(server.sessions) != 0 {
		t.Fatalf("detached transcript created live runtime: factory=%d validate=%d sessions=%d", factoryCalls, resumeValidations, len(server.sessions))
	}
}

func TestDurableHistoryNeverAcquiresLeaseOrCreatesLiveRuntime(t *testing.T) {
	catalogDir := t.TempDir()
	catalogPath := filepath.Join(catalogDir, "session-roots.json")
	root := t.TempDir()
	transcriptDir := filepath.Join(root, ".yhc", "transcripts")
	sessionID := "34343434-3434-4434-8434-343434343434"
	writeDurableSession(t, transcriptDir, sessionID, root, "history only", "main")
	if err := enginesession.RegisterSessionRoot(catalogPath, root, transcriptDir, time.Now()); err != nil {
		t.Fatal(err)
	}
	var factoryCalls int
	server, err := New(Config{
		Token: "test-token",
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			factoryCalls++
			return newFakeSessionEngine(input, false), nil
		},
		SessionCatalogPath: catalogPath,
		DiscoveryCWD:       catalogDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	defer shutdownTestServer(t, server)

	list := getBearer(t, httpServer.URL+"/v1/durable-sessions", "test-token")
	if list.StatusCode != http.StatusOK {
		t.Fatalf("history list = %d: %s", list.StatusCode, readBody(t, list))
	}
	_ = list.Body.Close()
	page := getBearer(t, httpServer.URL+"/v1/durable-sessions/"+sessionID+"/transcript", "test-token")
	if page.StatusCode != http.StatusOK {
		t.Fatalf("history transcript = %d: %s", page.StatusCode, readBody(t, page))
	}
	_ = page.Body.Close()

	lease, err := acquireSessionLease(transcriptDir, sessionID, "independent-history-reader")
	if err != nil {
		t.Fatalf("history read acquired a live lease: %v", err)
	}
	if err := lease.close(); err != nil {
		t.Fatal(err)
	}
	if factoryCalls != 0 || len(server.sessions) != 0 {
		t.Fatalf("history read created a live runtime: factory=%d sessions=%d", factoryCalls, len(server.sessions))
	}
}

func TestDurableTranscriptRejectsAmbiguousTrustedCandidates(t *testing.T) {
	catalogDir := t.TempDir()
	catalogPath := filepath.Join(catalogDir, "session-roots.json")
	firstRoot := t.TempDir()
	secondRoot := t.TempDir()
	firstDir := filepath.Join(firstRoot, ".eino-agent", "transcripts")
	secondDir := filepath.Join(secondRoot, ".eino-agent", "transcripts")
	sessionID := "44444444-4444-4444-8444-444444444444"
	writeDurableSession(t, firstDir, sessionID, firstRoot, "first copy", "main")
	writeDurableSession(t, secondDir, sessionID, secondRoot, "second copy", "main")
	for _, root := range []struct {
		cwd string
		dir string
	}{{firstRoot, firstDir}, {secondRoot, secondDir}} {
		if err := enginesession.RegisterSessionRoot(catalogPath, root.cwd, root.dir, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	server, err := New(Config{
		Token: "test-token",
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			return newFakeSessionEngine(input, false), nil
		},
		SessionCatalogPath: catalogPath,
		DiscoveryCWD:       catalogDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	defer shutdownTestServer(t, server)

	response := getBearer(
		t,
		httpServer.URL+"/v1/durable-sessions/"+sessionID+"/transcript",
		"test-token",
	)
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("ambiguous durable transcript = %d: %s", response.StatusCode, readBody(t, response))
	}
	var envelope ErrorEnvelope
	decodeResponse(t, response, &envelope)
	if envelope.Error.Code != "durable_session_ambiguous" {
		t.Fatalf("ambiguous error = %+v", envelope.Error)
	}
}

func TestDurableTranscriptPagersAreScopedToTrustedStablePath(t *testing.T) {
	firstDir := t.TempDir()
	secondDir := t.TempDir()
	sessionID := "55555555-5555-4555-8555-555555555555"
	writeDurableSession(t, firstDir, sessionID, firstDir, "first path", "main")
	writeDurableSession(t, secondDir, sessionID, secondDir, "second path", "main")
	server, err := New(Config{
		Token: "test-token",
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			return newFakeSessionEngine(input, false), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownTestServer(t, server)

	first, err := server.durableTranscriptPager(enginesession.SessionInfo{
		SessionID:      sessionID,
		TranscriptPath: filepath.Join(firstDir, sessionID+".jsonl"),
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := server.durableTranscriptPager(enginesession.SessionInfo{
		SessionID:      sessionID,
		TranscriptPath: filepath.Join(secondDir, sessionID+".jsonl"),
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := first.page("", 1)
	if err != nil || page.NextCursor == "" {
		t.Fatalf("first transcript page = %+v, %v", page, err)
	}
	if _, err := second.page(page.NextCursor, 1); !errors.Is(err, errTranscriptCursorInvalid) {
		t.Fatalf("cursor leaked across trusted paths: %v", err)
	}
	if len(server.durableTranscripts) != 2 {
		t.Fatalf("durable transcript registry = %d", len(server.durableTranscripts))
	}
	shutdownTestServer(t, server)
	if len(server.durableTranscripts) != 0 || len(server.durableTranscriptOrder) != 0 {
		t.Fatal("shutdown retained durable transcript pagers")
	}
}

func TestDurableTranscriptPagerCannotRegisterAfterShutdownStarts(t *testing.T) {
	server, err := New(Config{
		Token: "test-token",
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			return newFakeSessionEngine(input, false), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	server.mu.Lock()
	result := make(chan error, 1)
	transcriptPath := filepath.Join(t.TempDir(), "66666666-6666-4666-8666-666666666666.jsonl")
	go func() {
		_, registerErr := server.durableTranscriptPager(enginesession.SessionInfo{
			SessionID:      "66666666-6666-4666-8666-666666666666",
			TranscriptPath: transcriptPath,
		})
		result <- registerErr
	}()
	server.closing = true
	server.durableTranscripts = make(map[string]*transcriptPager)
	server.durableTranscriptOrder = nil
	server.mu.Unlock()

	if registerErr := <-result; !errors.Is(registerErr, errDurableServerClosing) {
		t.Fatalf("pager registration error = %v", registerErr)
	}
	if len(server.durableTranscripts) != 0 || len(server.durableTranscriptOrder) != 0 {
		t.Fatal("shutdown race repopulated durable transcript pagers")
	}
	shutdownTestServer(t, server)
}

func writeDurableSession(
	t *testing.T,
	dir, sessionID, cwd, prompt, branch string,
) {
	t.Helper()
	recorder := transcript.NewRecorder(sessionID, dir)
	defer recorder.Close()
	if err := recorder.RecordMessages([]*schema.Message{
		{Role: schema.User, Content: prompt},
		{Role: schema.Assistant, Content: "saved response"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordMetadata("cwd", cwd); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordMetadata("git_branch", branch); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
}

func touchFile(path string, timestamp time.Time) error {
	return os.Chtimes(path, timestamp, timestamp)
}
