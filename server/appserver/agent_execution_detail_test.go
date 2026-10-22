package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/abietic/yhc/engine"
)

type agentDetailEngine struct {
	*taskExplorerEngine
	detail      engine.AgentExecutionDetail
	found       bool
	err         error
	detailCalls atomic.Int32
	request     engine.AgentExecutionDetailRequest
}

func (e *agentDetailEngine) AgentExecutionDetail(r engine.AgentExecutionDetailRequest) (engine.AgentExecutionDetail, bool, error) {
	e.detailCalls.Add(1)
	e.request = r
	return e.detail, e.found, e.err
}

func TestAgentExecutionDetailRouteExactReadOnlyProjection(t *testing.T) {
	e := &agentDetailEngine{taskExplorerEngine: &taskExplorerEngine{fakeSessionEngine: newFakeSessionEngine(EngineOptions{SessionID: "owned-session"}, false)}, found: true}
	e.snapshot = engine.TaskExplorerSnapshot{Executions: []engine.TaskExplorerExecution{{Key: engine.RuntimeExecutionKey{AgentID: "agent", Generation: 1 << 54}, SessionID: "child-session", ThreadID: "child-thread"}}}
	e.detail = engine.AgentExecutionDetail{Revision: 1<<54 + 1, Agent: engine.RuntimeAgentSnapshot{AgentID: "agent", Generation: 1 << 54, SessionID: "child-session", ThreadID: "child-thread", TranscriptPath: "secret", CWD: "/secret", Progress: engine.RuntimeAgentProgressSnapshot{Summary: "working", LastToolName: "read", ToolUses: -1, TotalTokens: -2}}, Output: "secret-output"}
	_, hs := newTaskExplorerServer(t, e)
	u := hs.URL + "/v1/sessions/owned-session/agent-execution-detail?agent_id=agent&generation=" + strconv.FormatInt(1<<54, 10) + "&execution_session_id=child-session&thread_id=child-thread"
	r := taskExplorerRequest(t, u, http.MethodGet)
	if r.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", r.StatusCode, readBody(t, r))
	}
	var raw map[string]any
	decodeResponse(t, r, &raw)
	got := string(mustJSON(t, raw))
	if raw["session_id"] != "owned-session" || raw["generation"] != strconv.FormatInt(1<<54, 10) || strings.Contains(got, "secret") || raw["output"].(map[string]any)["requested"] != false {
		t.Fatalf("projection=%s", got)
	}
	if e.calls.Load() != 1 || e.detailCalls.Load() != 1 || e.mutations.Load() != 0 || e.request.IncludeOutput {
		t.Fatalf("calls=%d/%d mutation=%d request=%+v", e.calls.Load(), e.detailCalls.Load(), e.mutations.Load(), e.request)
	}
}

func TestAgentExecutionDetailRouteRejectsInputAndSelectionChanges(t *testing.T) {
	e := &agentDetailEngine{taskExplorerEngine: &taskExplorerEngine{fakeSessionEngine: newFakeSessionEngine(EngineOptions{}, false)}, found: true}
	e.snapshot = engine.TaskExplorerSnapshot{Executions: []engine.TaskExplorerExecution{{Key: engine.RuntimeExecutionKey{AgentID: "a", Generation: 1}, SessionID: "s", ThreadID: "t", Predispatch: true}}}
	_, hs := newTaskExplorerServer(t, e)
	base := hs.URL + "/v1/sessions/owned-session/agent-execution-detail?agent_id=a&generation=1&execution_session_id=s&thread_id=t"
	for _, u := range []string{base + "&agent_id=a", strings.Replace(base, "generation=1", "generation=01", 1), strings.Replace(base, "agent_id=a", "agent_id=%20a", 1), strings.Replace(base, "generation=1", "generation=999999999999999999999999", 1)} {
		r := taskExplorerRequest(t, u, http.MethodGet)
		if r.StatusCode != 400 {
			t.Fatalf("%s: %d", u, r.StatusCode)
		}
		r.Body.Close()
	}
	r := taskExplorerRequest(t, base, http.MethodGet)
	if r.StatusCode != 409 {
		t.Fatalf("predispatch=%d", r.StatusCode)
	}
	r.Body.Close()
	e.snapshot.Executions[0].Predispatch = false
	e.err = errors.New("wrapped: " + engine.ErrAgentExecutionDetailSelectionChanged.Error())
	r = taskExplorerRequest(t, base, http.MethodGet)
	if r.StatusCode != 500 {
		t.Fatalf("generic=%d", r.StatusCode)
	}
	r.Body.Close()
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func agentDetailFixture() *agentDetailEngine {
	return &agentDetailEngine{
		taskExplorerEngine: &taskExplorerEngine{
			fakeSessionEngine: newFakeSessionEngine(EngineOptions{SessionID: "owned-session"}, false),
			snapshot:          engine.TaskExplorerSnapshot{Executions: []engine.TaskExplorerExecution{{Key: engine.RuntimeExecutionKey{AgentID: "a", Generation: 1}, SessionID: "child", ThreadID: "thread"}}},
		},
		found: true, detail: engine.AgentExecutionDetail{Agent: engine.RuntimeAgentSnapshot{AgentID: "a", Generation: 1, SessionID: "child", ThreadID: "thread"}},
	}
}

const agentDetailQuery = "agent_id=a&generation=1&execution_session_id=child&thread_id=thread"

func TestAgentExecutionDetailQueryIsCanonicalBeforeSessionLookup(t *testing.T) {
	e := agentDetailFixture()
	_, hs := newTaskExplorerServer(t, e)
	for _, query := range []string{
		"", agentDetailQuery + "&path=%2Fprivate%2Fsynthetic", agentDetailQuery + "&include_output=true&include_output=false",
		agentDetailQuery + "&include_output=1", agentDetailQuery + "&include_output=", agentDetailQuery + "&bad=%zz",
		strings.Replace(agentDetailQuery, "generation=1", "generation=%2B1", 1),
		strings.Replace(agentDetailQuery, "generation=1", "generation=0", 1),
		strings.Replace(agentDetailQuery, "generation=1", "generation=9223372036854775808", 1),
		strings.Replace(agentDetailQuery, "agent_id=a", "agent_id=%C2%85", 1),
		strings.Replace(agentDetailQuery, "agent_id=a", "agent_id=%FF", 1),
		strings.Replace(agentDetailQuery, "thread_id=thread", "thread_id="+url.QueryEscape(strings.Repeat("界", 513)), 1),
	} {
		r := taskExplorerRequest(t, hs.URL+"/v1/sessions/unknown/agent-execution-detail?"+query, http.MethodGet)
		if r.StatusCode != http.StatusBadRequest {
			t.Errorf("query %q status = %d, want 400", query, r.StatusCode)
		}
		r.Body.Close()
	}
	if e.calls.Load() != 0 || e.detailCalls.Load() != 0 || e.mutations.Load() != 0 {
		t.Fatal("invalid input entered the runtime")
	}
}

func TestAgentExecutionDetailSelectionAndErrorFences(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*agentDetailEngine)
		status int
		reads  int32
	}{
		{"missing", func(e *agentDetailEngine) { e.snapshot.Executions = nil }, 404, 0},
		{"duplicate", func(e *agentDetailEngine) {
			e.snapshot.Executions = append(e.snapshot.Executions, e.snapshot.Executions[0])
		}, 409, 0},
		{"lineage", func(e *agentDetailEngine) { e.snapshot.Executions[0].ThreadID = "replacement" }, 409, 0},
		{"replay", func(e *agentDetailEngine) { e.snapshot.Executions[0].ReplayOnly = true }, 409, 0},
		{"predispatch", func(e *agentDetailEngine) { e.snapshot.Executions[0].Predispatch = true }, 409, 0},
		{"legacy", func(e *agentDetailEngine) { e.snapshot.Executions[0].SessionID = "" }, 409, 0},
		{"historical", func(e *agentDetailEngine) { e.snapshot.Executions[0].Key.Generation = 2 }, 404, 0},
		{"changed during read", func(e *agentDetailEngine) {
			e.err = fmt.Errorf("/private/synthetic: %w", engine.ErrAgentExecutionDetailSelectionChanged)
		}, 409, 1},
		{"lost", func(e *agentDetailEngine) { e.found = false }, 404, 1},
		{"generic error", func(e *agentDetailEngine) { e.err = errors.New("/private/synthetic-error") }, 500, 1},
		{"returned lineage", func(e *agentDetailEngine) { e.detail.Agent.SessionID = "replacement" }, 409, 1},
		{"returned generation", func(e *agentDetailEngine) { e.detail.Agent.Generation = 2 }, 409, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			e := agentDetailFixture()
			test.change(e)
			_, hs := newTaskExplorerServer(t, e)
			r := taskExplorerRequest(t, hs.URL+"/v1/sessions/owned-session/agent-execution-detail?"+agentDetailQuery, http.MethodGet)
			body := readBody(t, r)
			if r.StatusCode != test.status || e.calls.Load() != 1 || e.detailCalls.Load() != test.reads || e.mutations.Load() != 0 || strings.Contains(body, "/private/") {
				t.Fatalf("status=%d snapshot/detail/mutations=%d/%d/%d body=%s", r.StatusCode, e.calls.Load(), e.detailCalls.Load(), e.mutations.Load(), body)
			}
		})
	}
}

func TestAgentExecutionDetailBoundsAndOutputErrors(t *testing.T) {
	e := agentDetailFixture()
	e.detail.Agent.Name = strings.Repeat("界", 600)
	e.detail.Agent.OutputFile = "/private/synthetic-output"
	e.detail.Agent.WorktreePath = "/private/synthetic-worktree"
	e.detail.Agent.ParentAgentID = "parent"
	e.detail.Output = strings.Repeat("界", 30000) + "tail"
	e.detail.Revision = ^uint64(0)
	_, hs := newTaskExplorerServer(t, e)
	u := hs.URL + "/v1/sessions/owned-session/agent-execution-detail?" + agentDetailQuery
	r := taskExplorerRequest(t, u+"&include_output=true", http.MethodGet)
	var got agentExecutionDetailResponse
	decodeResponse(t, r, &got)
	r.Body.Close()
	if got.Revision != "18446744073709551615" || len(got.Output.Text) > 65536 || !utf8.ValidString(got.Output.Text) || !strings.HasSuffix(got.Output.Text, "tail") || !got.Output.Truncated || utf8.RuneCountInString(got.Metadata.Name) != 512 || strings.Contains(string(mustJSON(t, got)), "/private/") {
		t.Fatal("unbounded or unsafe projection")
	}
	e.detail.LoadError = "open /private/synthetic-output: permission denied"
	for _, include := range []bool{false, true} {
		r = taskExplorerRequest(t, u+"&include_output="+strconv.FormatBool(include), http.MethodGet)
		decodeResponse(t, r, &got)
		r.Body.Close()
		wantError := ""
		if include {
			wantError = "output_read_failed"
		}
		if got.Output.Text != "" || got.Output.Truncated || got.Output.Requested != include || got.Output.ErrorCategory != wantError {
			t.Fatalf("requested %v output = %+v", include, got.Output)
		}
	}
}

func TestAgentExecutionDetailAdmissionAndBrowserOrigin(t *testing.T) {
	e := agentDetailFixture()
	s, hs := newTaskExplorerServer(t, e)
	s.sessions["owned-session"].activeTurnID = "synthetic-active-turn"
	u := hs.URL + "/v1/sessions/owned-session/agent-execution-detail?" + agentDetailQuery
	r := taskExplorerRequest(t, u, http.MethodGet)
	if r.StatusCode != 200 || e.mutations.Load() != 0 {
		t.Fatal("active-turn read was not read-only")
	}
	r.Body.Close()
	for _, method := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete} {
		r = taskExplorerRequest(t, u, method)
		if r.StatusCode != 405 {
			t.Errorf("method %s status = %d", method, r.StatusCode)
		}
		r.Body.Close()
	}
	pairing, _, err := s.browserAuth.newPairing()
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := s.browserAuth.exchange(pairing)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		origin, site string
		status       int
	}{{hs.URL, "same-origin", 200}, {"https://attacker.invalid", "same-origin", 403}, {hs.URL, "cross-site", 403}} {
		req, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(&http.Cookie{Name: browserSessionCookieName, Value: token})
		req.Header.Set("Origin", test.origin)
		req.Header.Set("Sec-Fetch-Site", test.site)
		r, err = http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != test.status {
			t.Errorf("origin %q/%q = %d", test.origin, test.site, r.StatusCode)
		}
	}
	r, err = http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 401 || e.calls.Load() != 2 || e.detailCalls.Load() != 2 || e.mutations.Load() != 0 {
		t.Fatal("auth or mutation admission changed")
	}
	s.sessions["owned-session"].closed = true
	r = taskExplorerRequest(t, u, http.MethodGet)
	r.Body.Close()
	if r.StatusCode != 404 || e.detailCalls.Load() != 2 {
		t.Fatal("closed session read entered engine")
	}
	s.sessions["owned-session"] = &session{id: "owned-session", engine: newFakeSessionEngine(EngineOptions{}, false)}
	r = taskExplorerRequest(t, u, http.MethodGet)
	r.Body.Close()
	if r.StatusCode != 501 {
		t.Fatal("optional reader absence did not fail closed")
	}
}

func TestAgentExecutionDetailReadSerializesWithRealClose(t *testing.T) {
	e := agentDetailFixture()
	e.entered, e.release = make(chan struct{}), make(chan struct{})
	owned, err := newSession(context.Background(), func(context.Context, EngineOptions) (SessionEngine, error) { return e, nil }, "detail-server", CreateSessionRequest{SessionID: "owned-session", CWD: t.TempDir()}, nil, 128, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		select {
		case <-e.release:
		default:
			close(e.release)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := owned.close(ctx); err != nil {
			t.Error(err)
		}
	})
	s, _ := newTaskExplorerServer(t, e)
	s.sessions[owned.id] = owned
	done := make(chan int, 1)
	go func() {
		r := httptest.NewRequest(http.MethodGet, "/v1/sessions/owned-session/agent-execution-detail?"+agentDetailQuery, nil)
		r.SetPathValue("session_id", owned.id)
		w := httptest.NewRecorder()
		s.handleAgentExecutionDetail(w, r)
		done <- w.Code
	}()
	select {
	case <-e.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("read never entered")
	}
	if owned.turnAdmissionMu.TryLock() {
		owned.turnAdmissionMu.Unlock()
		t.Fatal("read did not retain close admission")
	}
	closed := make(chan error, 1)
	go func() { closed <- owned.close(context.Background()) }()
	if e.mutations.Load() != 0 {
		t.Fatal("close reached engine while read held admission")
	}
	close(e.release)
	select {
	case code := <-done:
		if code != 200 {
			t.Fatalf("read status = %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("read blocked")
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close blocked")
	}
	if e.calls.Load() != 1 || e.detailCalls.Load() != 1 || e.mutations.Load() != 2 {
		t.Fatal("unexpected read or close mutation journal")
	}
}
