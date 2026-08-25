package appserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/transcript"
)

func TestTranscriptPageIsLosslessFrozenAndSkipsLifecycleCopies(t *testing.T) {
	recorder := transcript.NewRecorder("desktop-history", t.TempDir())
	defer recorder.Close()
	longContent := strings.Repeat("history-", 2000)
	if err := recorder.RecordMessages([]*schema.Message{
		{Role: schema.User, Content: "oldest"},
		{Role: schema.Assistant, Content: longContent},
	}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordLifecycleBoundary(
		transcript.LifecycleCompact,
		[]*schema.Message{
			{Role: schema.System, Content: "compaction"},
			{Role: schema.User, Content: "lifecycle-copy"},
		},
		nil,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordMessages([]*schema.Message{
		{Role: schema.User, Content: "newer-user"},
		{Role: schema.Assistant, Content: "newest-assistant"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}

	server, err := New(Config{
		Token: "test-token",
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			engine := newFakeSessionEngine(input, false)
			engine.transcriptPath = recorder.Path()
			return engine, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	defer shutdownTestServer(t, server)

	create := doJSON(
		t,
		httpServer.URL+"/v1/sessions",
		"test-token",
		http.MethodPost,
		map[string]string{"workspace_handle": registerWorkspace(t, httpServer.URL, "test-token", t.TempDir()).WorkspaceHandle},
	)
	var summary SessionSummary
	decodeResponse(t, create, &summary)
	_ = create.Body.Close()

	first := getTranscriptBearer(
		t,
		httpServer.URL+"/v1/sessions/"+summary.ID+"/transcript?limit=2",
		"test-token",
	)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first page = %d: %s", first.StatusCode, readBody(t, first))
	}
	var firstPage TranscriptPageResponse
	decodeResponse(t, first, &firstPage)
	if len(firstPage.Messages) != 2 ||
		firstPage.Messages[0].Content != "newer-user" ||
		firstPage.Messages[1].Content != "newest-assistant" ||
		!firstPage.HasMore ||
		firstPage.NextCursor == "" {
		t.Fatalf("first page = %+v", firstPage)
	}

	if err := recorder.RecordMessages([]*schema.Message{
		{Role: schema.Assistant, Content: "appended-after-first-page"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}

	second := getTranscriptBearer(
		t,
		httpServer.URL+"/v1/sessions/"+summary.ID+"/transcript?limit=2&cursor="+
			firstPage.NextCursor,
		"test-token",
	)
	if second.StatusCode != http.StatusOK {
		t.Fatalf("second page = %d: %s", second.StatusCode, readBody(t, second))
	}
	var secondPage TranscriptPageResponse
	decodeResponse(t, second, &secondPage)
	if len(secondPage.Messages) != 2 ||
		secondPage.Messages[0].Content != "oldest" ||
		secondPage.Messages[1].Content != longContent ||
		secondPage.HasMore {
		t.Fatalf("second page = %+v", secondPage)
	}
	for _, message := range append(firstPage.Messages, secondPage.Messages...) {
		if message.Content == "lifecycle-copy" ||
			message.Content == "appended-after-first-page" {
			t.Fatalf("unexpected message leaked into frozen audit history: %+v", message)
		}
	}

	invalid := getTranscriptBearer(
		t,
		httpServer.URL+"/v1/sessions/"+summary.ID+"/transcript?cursor=unknown",
		"test-token",
	)
	if invalid.StatusCode != http.StatusConflict {
		t.Fatalf("invalid cursor = %d: %s", invalid.StatusCode, readBody(t, invalid))
	}
	_ = invalid.Body.Close()
}

func TestSnapshotUsesTranscriptPhysicalIdentity(t *testing.T) {
	recorder := transcript.NewRecorder("desktop-snapshot", t.TempDir())
	defer recorder.Close()
	wantMessages := []*schema.Message{
		schema.UserMessage("repeatable prompt"),
		schema.AssistantMessage("durable answer", nil),
	}
	if err := recorder.RecordMessages(wantMessages); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}

	server, err := New(Config{
		Token: "test-token",
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			fake := newFakeSessionEngine(input, false)
			fake.messages = wantMessages
			fake.transcriptPath = recorder.Path()
			return fake, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	defer shutdownTestServer(t, server)

	create := doJSON(
		t,
		httpServer.URL+"/v1/sessions",
		"test-token",
		http.MethodPost,
		map[string]string{"workspace_handle": registerWorkspace(t, httpServer.URL, "test-token", t.TempDir()).WorkspaceHandle},
	)
	var summary SessionSummary
	decodeResponse(t, create, &summary)
	_ = create.Body.Close()
	var snapshot SessionSnapshot
	snapshotResponse := getTranscriptBearer(
		t,
		httpServer.URL+"/v1/sessions/"+summary.ID+"/snapshot",
		"test-token",
	)
	decodeResponse(t, snapshotResponse, &snapshot)
	_ = snapshotResponse.Body.Close()
	var page TranscriptPageResponse
	pageResponse := getTranscriptBearer(
		t,
		httpServer.URL+"/v1/sessions/"+summary.ID+"/transcript",
		"test-token",
	)
	decodeResponse(t, pageResponse, &page)
	_ = pageResponse.Body.Close()
	if len(snapshot.Messages) != len(page.Messages) {
		t.Fatalf("snapshot=%+v page=%+v", snapshot.Messages, page.Messages)
	}
	for index := range page.Messages {
		if snapshot.Messages[index].ID == "" ||
			snapshot.Messages[index].ID != page.Messages[index].ID ||
			snapshot.Messages[index].Source != "durable" {
			t.Fatalf("snapshot message %d = %+v", index, snapshot.Messages[index])
		}
	}
}

func TestSnapshotMessagePrefersTranscriptEntryID(t *testing.T) {
	timestamp := time.Date(2026, time.August, 7, 0, 0, 0, 0, time.UTC)
	got := snapshotMessage(engine.RuntimeMessageSnapshot{
		ID:                "runtime-message",
		TranscriptEntryID: "durable-message",
		Timestamp:         timestamp,
	})
	if got.ID != "durable-message" || got.Source != "durable" || got.Timestamp != timestamp {
		t.Fatalf("snapshot message = %+v", got)
	}
}

func TestSnapshotConversationFallbackWhenTranscriptUnavailable(t *testing.T) {
	server, err := New(Config{
		Token: "test-token",
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			fake := newFakeSessionEngine(input, false)
			fake.messages = []*schema.Message{schema.UserMessage("fallback")}
			return fake, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	defer shutdownTestServer(t, server)

	create := doJSON(
		t,
		httpServer.URL+"/v1/sessions",
		"test-token",
		http.MethodPost,
		map[string]string{"workspace_handle": registerWorkspace(t, httpServer.URL, "test-token", t.TempDir()).WorkspaceHandle},
	)
	var summary SessionSummary
	decodeResponse(t, create, &summary)
	_ = create.Body.Close()
	var snapshot SessionSnapshot
	snapshotResponse := getTranscriptBearer(
		t,
		httpServer.URL+"/v1/sessions/"+summary.ID+"/snapshot",
		"test-token",
	)
	decodeResponse(t, snapshotResponse, &snapshot)
	_ = snapshotResponse.Body.Close()
	if len(snapshot.Messages) != 1 || snapshot.Messages[0].ID != "" ||
		snapshot.Messages[0].Source != "conversation-fallback" {
		t.Fatalf("snapshot messages = %+v", snapshot.Messages)
	}
}

func getTranscriptBearer(t *testing.T, endpoint, token string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
