package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/transcript"
)

type agentTranscriptPageEngine struct {
	*taskExplorerEngine
	page  engine.AgentTranscriptPage
	found bool
	err   error
	req   engine.AgentTranscriptPageRequest
	calls atomic.Int32
}

func (e *agentTranscriptPageEngine) AgentTranscriptPage(req engine.AgentTranscriptPageRequest) (engine.AgentTranscriptPage, bool, error) {
	e.calls.Add(1)
	e.req = req
	return e.page, e.found, e.err
}

func agentTranscriptPageFixture() *agentTranscriptPageEngine {
	e := &agentTranscriptPageEngine{taskExplorerEngine: &taskExplorerEngine{fakeSessionEngine: newFakeSessionEngine(EngineOptions{SessionID: "owned-session"}, false)}, found: true}
	e.snapshot = engine.TaskExplorerSnapshot{Executions: []engine.TaskExplorerExecution{{Key: engine.RuntimeExecutionKey{AgentID: "agent", Generation: 1 << 54}, SessionID: "child-session", ThreadID: "child-thread"}}}
	e.page = engine.AgentTranscriptPage{Revision: 1<<54 + 1, AgentID: "agent", Generation: 1 << 54, SessionID: "child-session", ThreadID: "child-thread", AttachMode: engine.ThreadModeLiveAttach, Storage: "durable", Messages: []engine.AgentTranscriptMessage{{ID: "m-1", TranscriptEntryID: "m-1", Role: "assistant", Content: "ordinary /private/user-path remains conversation", Completed: true, Timestamp: time.Date(2026, 1, 2, 3, 4, 5, 6, time.FixedZone("x", 3600)), Source: "durable"}}}
	return e
}

const agentTranscriptPageQuery = "agent_id=agent&generation=18014398509481984&execution_session_id=child-session&thread_id=child-thread"

func agentTranscriptPageHTTPTestRequest(t *testing.T, s *Server, query string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/sessions/owned-session/agent-transcript?"+query, nil)
	r.SetPathValue("session_id", "owned-session")
	r.Header.Set("Authorization", "Bearer task-explorer-token")
	w := httptest.NewRecorder()
	s.middleware(http.HandlerFunc(s.handleAgentTranscriptPage)).ServeHTTP(w, r)
	return w
}

func TestAgentTranscriptPageStrictQueryAndReadOnlyProjection(t *testing.T) {
	e := agentTranscriptPageFixture()
	s, _ := newTaskExplorerServer(t, e)
	for _, query := range []string{"", agentTranscriptPageQuery + "&bad=x", agentTranscriptPageQuery + "&agent_id=agent", strings.Replace(agentTranscriptPageQuery, "generation=18014398509481984", "generation=01", 1), strings.Replace(agentTranscriptPageQuery, "generation=18014398509481984", "generation=0", 1), agentTranscriptPageQuery + "&cursor=", agentTranscriptPageQuery + "&cursor=%C2%85", agentTranscriptPageQuery + "&limit=0", agentTranscriptPageQuery + "&limit=033", agentTranscriptPageQuery + "&limit=33"} {
		if w := agentTranscriptPageHTTPTestRequest(t, s, query); w.Code != http.StatusBadRequest {
			t.Errorf("query %q = %d", query, w.Code)
		}
	}
	if e.calls.Load() != 0 || e.mutations.Load() != 0 {
		t.Fatalf("invalid request entered runtime: page=%d mutations=%d", e.calls.Load(), e.mutations.Load())
	}
	w := agentTranscriptPageHTTPTestRequest(t, s, agentTranscriptPageQuery+"&cursor=agent-transcript-x&limit=2")
	if w.Code != http.StatusOK || e.calls.Load() != 1 || e.mutations.Load() != 0 || e.req.Cursor != "agent-transcript-x" || e.req.Limit != 2 || e.req.SessionID != "child-session" || e.req.ThreadID != "child-thread" {
		t.Fatalf("status/request = %d %+v", w.Code, e.req)
	}
	var got agentTranscriptHTTPResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.SessionID != "owned-session" || got.Generation != "18014398509481984" || got.Messages[0].Content != e.page.Messages[0].Content || got.Messages[0].Timestamp != "2026-01-02T02:04:05.000000006Z" || got.Messages[0].ToolCalls == nil || got.Messages[0].PromptParts == nil {
		t.Fatalf("projection = %+v", got)
	}
}

func TestAgentTranscriptPageSelectionAndErrorFences(t *testing.T) {
	for _, test := range []struct {
		name        string
		change      func(*agentTranscriptPageEngine)
		want, calls int
	}{
		{"missing", func(e *agentTranscriptPageEngine) { e.snapshot.Executions = nil }, 404, 0},
		{"duplicate", func(e *agentTranscriptPageEngine) {
			e.snapshot.Executions = append(e.snapshot.Executions, e.snapshot.Executions[0])
		}, 409, 0},
		{"partial", func(e *agentTranscriptPageEngine) { e.snapshot.Executions[0].SessionID = "" }, 409, 0},
		{"replay-row", func(e *agentTranscriptPageEngine) { e.snapshot.Executions[0].ReplayOnly = true }, 409, 0},
		{"predispatch", func(e *agentTranscriptPageEngine) { e.snapshot.Executions[0].Predispatch = true }, 409, 0},
		{"changed", func(e *agentTranscriptPageEngine) { e.err = engine.ErrAgentTranscriptSelectionChanged }, 409, 1},
		{"revision", func(e *agentTranscriptPageEngine) { e.err = transcript.ErrTranscriptRevisionChanged }, 409, 1},
		{"cursor", func(e *agentTranscriptPageEngine) { e.err = engine.ErrAgentTranscriptCursorInvalid }, 400, 1},
		{"failure", func(e *agentTranscriptPageEngine) { e.err = errors.New("/private/hidden") }, 500, 1},
		{"not-found", func(e *agentTranscriptPageEngine) { e.found = false }, 404, 1},
		{"returned-mismatch", func(e *agentTranscriptPageEngine) { e.page.ThreadID = "replacement" }, 409, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			e := agentTranscriptPageFixture()
			test.change(e)
			s, _ := newTaskExplorerServer(t, e)
			w := agentTranscriptPageHTTPTestRequest(t, s, agentTranscriptPageQuery)
			if w.Code != test.want || int(e.calls.Load()) != test.calls || strings.Contains(w.Body.String(), "/private/") {
				t.Fatalf("status/calls/body=%d/%d/%s", w.Code, e.calls.Load(), w.Body.String())
			}
		})
	}
}

func TestAgentTranscriptPageProjectionBoundsAndMalformedRowsFailClosed(t *testing.T) {
	e := agentTranscriptPageFixture()
	e.page.AttachMode, e.page.Replay, e.page.HasMore, e.page.NextCursor = engine.ThreadModeReplayOnly, true, true, "agent-transcript-next"
	e.page.Corruptions = -1
	e.page.Messages[0].Content = strings.Repeat("界", 10000)
	e.page.Messages[0].ReasoningContent = string([]byte{0xff}) + strings.Repeat("x", 10000)
	e.page.Messages[0].ToolCalls = []engine.RuntimeToolCallSnapshot{{ID: "call", Name: "read", InputPreview: strings.Repeat("x", 5000)}}
	e.page.Messages[0].PromptParts = []engine.AgentTranscriptPromptPart{{Kind: "text", Text: strings.Repeat("x", 5000), MIMEType: "text/plain", ImageDetail: "auto", SizeBytes: -1, Width: -1, Height: -1}}
	s, _ := newTaskExplorerServer(t, e)
	w := agentTranscriptPageHTTPTestRequest(t, s, agentTranscriptPageQuery)
	var got agentTranscriptHTTPResponse
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || len(got.Messages) != 1 {
		t.Fatalf("body=%s", w.Body.String())
	}
	m := got.Messages[0]
	if !m.Truncated || len(m.Content) > agentTranscriptHTTPContentBytes || len(m.ReasoningContent) > agentTranscriptHTTPReasonBytes || len(m.ToolCalls[0].InputPreview) > agentTranscriptHTTPPreviewBytes || len(m.PromptParts[0].Text) > agentTranscriptHTTPPreviewBytes || !utf8.ValidString(m.Content) || !utf8.ValidString(m.ReasoningContent) || got.Corruptions != 0 || m.PromptParts[0].SizeBytes != 0 || m.PromptParts[0].Width != 0 || m.PromptParts[0].Height != 0 {
		t.Fatalf("unbounded projection: %+v", got)
	}
	e = agentTranscriptPageFixture()
	e.page.Messages[0].ID = ""
	s, _ = newTaskExplorerServer(t, e)
	w = agentTranscriptPageHTTPTestRequest(t, s, agentTranscriptPageQuery)
	if w.Code != 500 {
		t.Fatalf("bad identity = %d", w.Code)
	}
	e = agentTranscriptPageFixture()
	e.page.NextCursor = "unexpected"
	s, _ = newTaskExplorerServer(t, e)
	w = agentTranscriptPageHTTPTestRequest(t, s, agentTranscriptPageQuery)
	if w.Code != 409 {
		t.Fatalf("cursor consistency = %d", w.Code)
	}
	e = agentTranscriptPageFixture()
	e.page.HasMore, e.page.NextCursor = true, "agent-transcript-current"
	s, _ = newTaskExplorerServer(t, e)
	w = agentTranscriptPageHTTPTestRequest(t, s, agentTranscriptPageQuery+"&cursor=agent-transcript-current")
	if w.Code != 409 {
		t.Fatalf("cursor loop = %d", w.Code)
	}
	e = agentTranscriptPageFixture()
	e.page.Storage = "/private/transcript"
	s, _ = newTaskExplorerServer(t, e)
	w = agentTranscriptPageHTTPTestRequest(t, s, agentTranscriptPageQuery)
	if w.Code != 500 || strings.Contains(w.Body.String(), "/private/") {
		t.Fatalf("unsafe storage = %d %s", w.Code, w.Body.String())
	}
}

func TestAgentTranscriptPageAllowsRuntimeFirstRowWithoutDurableEntry(t *testing.T) {
	e := agentTranscriptPageFixture()
	e.page.Messages[0].Source = "runtime"
	e.page.Messages[0].TranscriptEntryID = ""
	s, _ := newTaskExplorerServer(t, e)
	w := agentTranscriptPageHTTPTestRequest(t, s, agentTranscriptPageQuery)
	var got agentTranscriptHTTPResponse
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Messages[0].TranscriptEntryID != "" {
		t.Fatalf("runtime first row = %d %s", w.Code, w.Body.String())
	}
	e = agentTranscriptPageFixture()
	e.page.Messages = append(e.page.Messages, e.page.Messages[0])
	s, _ = newTaskExplorerServer(t, e)
	w = agentTranscriptPageHTTPTestRequest(t, s, agentTranscriptPageQuery)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("duplicate id = %d", w.Code)
	}
	e = agentTranscriptPageFixture()
	e.page.Messages[0].TranscriptEntryID = ""
	s, _ = newTaskExplorerServer(t, e)
	w = agentTranscriptPageHTTPTestRequest(t, s, agentTranscriptPageQuery)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("durable missing entry = %d", w.Code)
	}
}

func TestAgentTranscriptPageClipsValidLargeCollectionsWithoutLosingMessage(t *testing.T) {
	e := agentTranscriptPageFixture()
	for range 16 {
		e.page.Messages[0].ToolCalls = append(e.page.Messages[0].ToolCalls, engine.RuntimeToolCallSnapshot{ID: "call", Name: "read", InputPreview: "input"})
	}
	for range 17 {
		e.page.Messages[0].PromptParts = append(e.page.Messages[0].PromptParts, engine.AgentTranscriptPromptPart{Kind: "text", Text: "text"})
	}
	s, _ := newTaskExplorerServer(t, e)
	w := agentTranscriptPageHTTPTestRequest(t, s, agentTranscriptPageQuery)
	var got agentTranscriptHTTPResponse
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || len(got.Messages) != 1 {
		t.Fatalf("valid collections rejected: %d %s", w.Code, w.Body.String())
	}
	m := got.Messages[0]
	if !m.Truncated || len(m.ToolCalls) != 8 || len(m.PromptParts) != 16 || m.Content != e.page.Messages[0].Content {
		t.Fatalf("bounded collections = %+v", m)
	}
}

func TestAgentTranscriptPageClosedAndOptionalReaderFailClosed(t *testing.T) {
	e := agentTranscriptPageFixture()
	s, _ := newTaskExplorerServer(t, e)
	unauthenticated := httptest.NewRequest(http.MethodGet, "/v1/sessions/owned-session/agent-transcript?"+agentTranscriptPageQuery, nil)
	unauthenticated.SetPathValue("session_id", "owned-session")
	unauthorized := httptest.NewRecorder()
	s.middleware(http.HandlerFunc(s.handleAgentTranscriptPage)).ServeHTTP(unauthorized, unauthenticated)
	if unauthorized.Code != http.StatusUnauthorized || e.calls.Load() != 0 {
		t.Fatalf("auth=%d calls=%d", unauthorized.Code, e.calls.Load())
	}
	s.sessions["owned-session"].closed = true
	if w := agentTranscriptPageHTTPTestRequest(t, s, agentTranscriptPageQuery); w.Code != 404 || e.calls.Load() != 0 {
		t.Fatalf("closed=%d calls=%d", w.Code, e.calls.Load())
	}
	s.sessions["owned-session"] = &session{id: "owned-session", engine: newFakeSessionEngine(EngineOptions{}, false)}
	if w := agentTranscriptPageHTTPTestRequest(t, s, agentTranscriptPageQuery); w.Code != 501 {
		t.Fatalf("optional reader = %d", w.Code)
	}
}

func TestAgentTranscriptPageReadSerializesWithClose(t *testing.T) {
	e := agentTranscriptPageFixture()
	e.entered, e.release = make(chan struct{}), make(chan struct{})
	owned, err := newSession(context.Background(), func(context.Context, EngineOptions) (SessionEngine, error) { return e, nil }, "transcript-server", CreateSessionRequest{SessionID: "owned-session", CWD: t.TempDir()}, nil, 128, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		select {
		case <-e.release:
		default:
			close(e.release)
		}
		_ = owned.close(context.Background())
	})
	s, _ := newTaskExplorerServer(t, e)
	s.sessions[owned.id] = owned
	done := make(chan int, 1)
	go func() {
		r := httptest.NewRequest(http.MethodGet, "/?"+agentTranscriptPageQuery, nil)
		r.SetPathValue("session_id", owned.id)
		w := httptest.NewRecorder()
		s.handleAgentTranscriptPage(w, r)
		done <- w.Code
	}()
	select {
	case <-e.entered:
	case <-time.After(time.Second):
		t.Fatal("snapshot did not enter")
	}
	if owned.turnAdmissionMu.TryLock() {
		owned.turnAdmissionMu.Unlock()
		t.Fatal("read did not hold admission")
	}
	closed := make(chan error, 1)
	go func() { closed <- owned.close(context.Background()) }()
	close(e.release)
	if code := <-done; code != 200 {
		t.Fatalf("read=%d", code)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
}
