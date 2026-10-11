package appserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/internal/webui"
)

type taskExplorerEngine struct {
	*fakeSessionEngine
	snapshot  engine.TaskExplorerSnapshot
	calls     atomic.Int32
	mutations atomic.Int32
	entered   chan struct{}
	release   chan struct{}
}

func (e *taskExplorerEngine) TaskExplorerSnapshot() engine.TaskExplorerSnapshot {
	e.calls.Add(1)
	if e.entered != nil {
		close(e.entered)
		<-e.release
	}
	return e.snapshot
}

func (e *taskExplorerEngine) SubmitMessage(context.Context, string) (<-chan engine.QueryEvent, engine.Terminal) {
	e.mutations.Add(1)
	return nil, engine.Terminal{}
}

func (e *taskExplorerEngine) SubmitRuntimeItem(context.Context, engine.RuntimeItem) (<-chan engine.QueryEvent, engine.Terminal) {
	e.mutations.Add(1)
	return nil, engine.Terminal{}
}

func (e *taskExplorerEngine) ClaimNextRuntimeItem() (engine.RuntimeItem, bool, error) {
	e.mutations.Add(1)
	return engine.RuntimeItem{}, false, nil
}

func (e *taskExplorerEngine) ResolvePermissionInteraction(string, engine.PermissionInteractionResult) bool {
	e.mutations.Add(1)
	return false
}

func (e *taskExplorerEngine) RequestStop(engine.RuntimeStopMode, string) error {
	e.mutations.Add(1)
	return nil
}

func (e *taskExplorerEngine) Close() {
	e.mutations.Add(1)
	e.fakeSessionEngine.Close()
}

func newTaskExplorerServer(t *testing.T, runtime SessionEngine) (*Server, *httptest.Server) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	var creations atomic.Int32
	t.Cleanup(func() {
		if creations.Load() != 0 {
			t.Errorf("task read constructed %d engines", creations.Load())
		}
	})
	server, err := New(Config{
		Token:              "task-explorer-token",
		EnableWeb:          true,
		WebAssets:          webui.Assets,
		DiscoveryCWD:       t.TempDir(),
		SessionCatalogPath: filepath.Join(t.TempDir(), "session-roots.json"),
		Factory: func(context.Context, EngineOptions) (SessionEngine, error) {
			creations.Add(1)
			return runtime, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server.sessions["owned-session"] = &session{id: "owned-session", engine: runtime}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	if _, err := server.BootstrapFor(httpServer.Listener); err != nil {
		t.Fatal(err)
	}
	return server, httpServer
}

func taskExplorerRequest(t *testing.T, url, method string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer task-explorer-token")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestTaskExplorerRouteProjectsOwnedSnapshotWithoutControlFields(t *testing.T) {
	runtime := &taskExplorerEngine{fakeSessionEngine: newFakeSessionEngine(EngineOptions{SessionID: "owned-session", ThreadID: "owned-thread"}, false)}
	runtime.snapshot = engine.TaskExplorerSnapshot{
		Available:   true,
		SessionID:   "different-workboard-lineage",
		BoardID:     "board-1",
		Revision:    engine.TaskExplorerRevision{Board: 1<<54 + 7, Runtime: 1<<54 + 8},
		WorkItems:   []engine.TaskExplorerWorkItem{{BoardID: "board-1", WorkItemID: "item-1", Revision: 1<<54 + 9, Order: 3, Status: "running", Title: "title", Blocks: nil, BlockedBy: nil, Attention: nil}},
		Executions:  []engine.TaskExplorerExecution{{Key: engine.RuntimeExecutionKey{AgentID: "agent-1", Generation: 1<<54 + 10}, SessionID: "private-session", ThreadID: "private-thread", ParentSessionID: "private-parent", TranscriptPath: "/private/transcript", Name: "worker", Task: "task", Status: "running", Phase: engine.TaskExplorerExecutionRunning, ObservationOrdinal: 1<<54 + 11, OrdinalPresent: true, AllowedActions: []engine.TaskExplorerAction{engine.TaskExplorerActionCancel}}},
		Links:       []engine.TaskExplorerLink{{WorkExecutionLink: engine.WorkExecutionLink{BoardID: "board-1", WorkItemID: "item-1", AgentID: "agent-1", Generation: 1<<54 + 12}, State: engine.TaskExplorerLinkValid}},
		Attention:   []engine.TaskExplorerAttention{{Category: "attention", WorkItemID: "item-1", AgentID: "agent-1", Generation: 1<<54 + 13, Sequence: 1<<54 + 14, Reason: "reason"}},
		Diagnostics: []engine.TaskExplorerDiagnostic{{Sequence: 1<<54 + 15, Kind: "notice", ItemID: "item-1", Message: "message"}},
		Hidden:      engine.TaskExplorerHiddenCounts{WorkItems: nil, Executions: nil, Attention: nil, RuntimeEventsDropped: 1<<54 + 16, ExecutionGenerationsEvicted: 1<<54 + 17, HiddenLiveExecutions: 1<<54 + 18},
	}
	_, httpServer := newTaskExplorerServer(t, runtime)

	response := taskExplorerRequest(t, httpServer.URL+"/v1/sessions/owned-session/tasks", http.MethodGet)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.StatusCode, readBody(t, response))
	}
	var raw map[string]any
	decodeResponse(t, response, &raw)
	if raw["session_id"] != "owned-session" || raw["revision"].(map[string]any)["board"] != strconv.FormatUint(1<<54+7, 10) {
		t.Fatalf("identity/revision projection = %#v", raw)
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"TranscriptPath", "transcript_path", "AllowedActions", "allowed_actions", "private-parent", "/private/transcript"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("unsafe projection contains %q: %s", forbidden, encoded)
		}
	}
	if runtime.calls.Load() != 1 {
		t.Fatalf("snapshot calls = %d, want 1", runtime.calls.Load())
	}
	if runtime.mutations.Load() != 0 {
		t.Fatalf("read caused %d engine mutations", runtime.mutations.Load())
	}
	workItems := raw["work_items"].([]any)
	execution := raw["executions"].([]any)[0].(map[string]any)
	if execution["execution_session_id"] != "private-session" || execution["thread_id"] != "private-thread" {
		t.Fatalf("exact lineage projection = %#v", execution)
	}
	if _, ok := workItems[0].(map[string]any)["blocks"].([]any); !ok {
		t.Fatalf("nil blocks did not serialize as []: %#v", workItems[0])
	}
}

func TestTaskExplorerRouteBrowserOriginAndReadAdmission(t *testing.T) {
	runtime := &taskExplorerEngine{fakeSessionEngine: newFakeSessionEngine(EngineOptions{SessionID: "owned-session"}, false)}
	server, httpServer := newTaskExplorerServer(t, runtime)
	pairing, _, err := server.browserAuth.newPairing()
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := server.browserAuth.exchange(pairing)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		origin, site string
		want         int
	}{
		{httpServer.URL, "same-origin", http.StatusOK},
		{"https://attacker.invalid", "same-origin", http.StatusForbidden},
		{httpServer.URL, "cross-site", http.StatusForbidden},
	} {
		request, err := http.NewRequest(http.MethodGet, httpServer.URL+"/v1/sessions/owned-session/tasks", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.AddCookie(&http.Cookie{Name: browserSessionCookieName, Value: token})
		request.Header.Set("Origin", test.origin)
		request.Header.Set("Sec-Fetch-Site", test.site)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != test.want {
			t.Fatalf("origin %q site %q status = %d, want %d", test.origin, test.site, response.StatusCode, test.want)
		}
	}
	if runtime.calls.Load() != 1 || runtime.mutations.Load() != 0 {
		t.Fatalf("reads/mutations = %d/%d", runtime.calls.Load(), runtime.mutations.Load())
	}
}

func TestTaskExplorerRouteReadSerializesWithRealSessionClose(t *testing.T) {
	runtime := &taskExplorerEngine{fakeSessionEngine: newFakeSessionEngine(EngineOptions{SessionID: "owned-session"}, false), entered: make(chan struct{}), release: make(chan struct{})}
	owned, err := newSession(context.Background(), func(context.Context, EngineOptions) (SessionEngine, error) { return runtime, nil }, "task-test-server",
		CreateSessionRequest{SessionID: "owned-session", CWD: t.TempDir()}, nil, 128, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		select {
		case <-runtime.release:
		default:
			close(runtime.release)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := owned.close(ctx); err != nil {
			t.Errorf("fixture close: %v", err)
		}
	})
	server, httpServer := newTaskExplorerServer(t, runtime)
	server.sessions[owned.id] = owned
	requestDone := make(chan int, 1)
	go func() {
		request := httptest.NewRequest(http.MethodGet, "/v1/sessions/owned-session/tasks", nil)
		request.SetPathValue("session_id", owned.id)
		response := httptest.NewRecorder()
		server.handleTaskExplorer(response, request)
		requestDone <- response.Code
	}()
	select {
	case <-runtime.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("snapshot never entered")
	}
	if owned.turnAdmissionMu.TryLock() {
		owned.turnAdmissionMu.Unlock()
		t.Fatal("snapshot did not retain close admission")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- owned.close(context.Background()) }()
	if runtime.mutations.Load() != 0 {
		t.Fatal("close reached engine while snapshot owns admission")
	}
	close(runtime.release)
	select {
	case status := <-requestDone:
		if status != http.StatusOK {
			t.Fatalf("read status = %d", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("read blocked")
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close blocked")
	}
	response := taskExplorerRequest(t, httpServer.URL+"/v1/sessions/owned-session/tasks", http.MethodGet)
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound || runtime.calls.Load() != 1 || runtime.mutations.Load() != 2 {
		t.Fatalf("closed read: status=%d reads=%d mutations=%d", response.StatusCode, runtime.calls.Load(), runtime.mutations.Load())
	}
}

func TestTaskExplorerHTTPProjectionBoundsAllRowsAndHiddenMap(t *testing.T) {
	snapshot := engine.TaskExplorerSnapshot{
		Available: true,
		WorkItems: make([]engine.TaskExplorerWorkItem, 130), Executions: make([]engine.TaskExplorerExecution, 129),
		Links: make([]engine.TaskExplorerLink, 132), Attention: make([]engine.TaskExplorerAttention, 131), Diagnostics: make([]engine.TaskExplorerDiagnostic, 140),
		Hidden: engine.TaskExplorerHiddenCounts{WorkItems: map[string]int{}, Attention: map[string]int{"permission": 3}},
	}
	for i := 0; i < 40; i++ {
		snapshot.Hidden.WorkItems[strconv.Itoa(i)+strings.Repeat("界", 200)] = 1
	}
	snapshot.WorkItems[0].Blocks = make([]string, 129)
	projection := taskExplorerHTTPProjection("owned", snapshot)
	if len(projection.WorkItems) != 128 || len(projection.Executions) != 128 || len(projection.Links) != 128 || len(projection.Attention) != 128 || len(projection.Diagnostics) != 128 || len(projection.WorkItems[0].Blocks) != 128 {
		t.Fatal("unbounded projection")
	}
	if len(projection.Hidden.WorkItems) != 32 || projection.Hidden.Executions["other"] != 1 || projection.Hidden.Links != 4 || projection.Hidden.Attention["other"] != 3 || projection.Hidden.Attention["permission"] != 3 {
		t.Fatalf("hidden = %#v", projection.Hidden)
	}
	total := 0
	for key, value := range projection.Hidden.WorkItems {
		if utf8.RuneCountInString(key) > 128 {
			t.Fatal("unbounded map key")
		}
		total += value
	}
	if total != 42 {
		t.Fatalf("hidden total = %d, want 42", total)
	}
}

func TestTaskExplorerRouteRejectsUnavailableAndNonOwnedRequests(t *testing.T) {
	runtime := newFakeSessionEngine(EngineOptions{SessionID: "owned-session", ThreadID: "owned-thread"}, false)
	server, httpServer := newTaskExplorerServer(t, runtime)
	t.Run("authentication", func(t *testing.T) {
		response, err := http.Get(httpServer.URL + "/v1/sessions/owned-session/tasks")
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusUnauthorized)
		}
	})
	for _, test := range []struct {
		name   string
		url    string
		method string
		want   int
	}{
		{name: "reader unavailable", url: "/v1/sessions/owned-session/tasks", method: http.MethodGet, want: http.StatusNotImplemented},
		{name: "unknown", url: "/v1/sessions/unknown/tasks", method: http.MethodGet, want: http.StatusNotFound},
		{name: "query", url: "/v1/sessions/owned-session/tasks?detail=1", method: http.MethodGet, want: http.StatusBadRequest},
		{name: "post", url: "/v1/sessions/owned-session/tasks", method: http.MethodPost, want: http.StatusMethodNotAllowed},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := taskExplorerRequest(t, httpServer.URL+test.url, test.method)
			defer response.Body.Close()
			if response.StatusCode != test.want {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.want)
			}
		})
	}
	server.sessions["owned-session"].mu.Lock()
	server.sessions["owned-session"].closed = true
	server.sessions["owned-session"].mu.Unlock()
	response := taskExplorerRequest(t, httpServer.URL+"/v1/sessions/owned-session/tasks", http.MethodGet)
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("closed status = %d, want %d", response.StatusCode, http.StatusNotFound)
	}
}

func TestTaskExplorerRouteAllowsBusyProjectionAndBoundsValues(t *testing.T) {
	long := strings.Repeat("界", 600)
	runtime := &taskExplorerEngine{fakeSessionEngine: newFakeSessionEngine(EngineOptions{SessionID: "owned-session", ThreadID: "owned-thread"}, false)}
	runtime.snapshot = engine.TaskExplorerSnapshot{Available: false, UnavailableReason: long, Executions: make([]engine.TaskExplorerExecution, 130), Hidden: engine.TaskExplorerHiddenCounts{WorkItems: map[string]int{"z": -1, "a": 4}, Executions: map[string]int{}, Attention: map[string]int{}}}
	for i := range runtime.snapshot.Executions {
		runtime.snapshot.Executions[i] = engine.TaskExplorerExecution{Key: engine.RuntimeExecutionKey{AgentID: long + strconv.Itoa(i), Generation: int64(i)}, Name: long, Attention: []string{long}, ToolUseCount: -1, TokenCount: -2}
	}
	server, httpServer := newTaskExplorerServer(t, runtime)
	server.sessions["owned-session"].activeTurnID = "running-turn"

	response := taskExplorerRequest(t, httpServer.URL+"/v1/sessions/owned-session/tasks", http.MethodGet)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("busy status = %d, body = %s", response.StatusCode, readBody(t, response))
	}
	var raw struct {
		Available         bool   `json:"available"`
		UnavailableReason string `json:"unavailable_reason"`
		Executions        []struct {
			AgentID      string   `json:"agent_id"`
			Name         string   `json:"name"`
			Attention    []string `json:"attention"`
			ToolUseCount int      `json:"tool_use_count"`
			TokenCount   int      `json:"token_count"`
		} `json:"executions"`
		Hidden struct {
			Executions map[string]int `json:"executions"`
		} `json:"hidden"`
	}
	decodeResponse(t, response, &raw)
	if raw.Available || len(raw.Executions) != 128 || raw.Hidden.Executions["other"] != 2 {
		t.Fatalf("bounded unavailable projection = %#v", raw)
	}
	if utf8.RuneCountInString(raw.UnavailableReason) != 512 || utf8.RuneCountInString(raw.Executions[0].AgentID) != 512 || utf8.RuneCountInString(raw.Executions[0].Name) != 512 || utf8.RuneCountInString(raw.Executions[0].Attention[0]) != 512 || raw.Executions[0].ToolUseCount != 0 || raw.Executions[0].TokenCount != 0 {
		t.Fatalf("bounded execution = %#v", raw.Executions[0])
	}
	if runtime.calls.Load() != 1 {
		t.Fatalf("snapshot calls = %d, want 1", runtime.calls.Load())
	}
}
