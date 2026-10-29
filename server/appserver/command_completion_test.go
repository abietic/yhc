package appserver

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/commands"
)

func TestCommandCompletionWithRealEngineHasNoDispatchOrSessionCreation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var created *engine.QueryEngine
	calls := 0
	server, err := New(Config{Token: "test-token", SessionCatalogPath: filepath.Join(t.TempDir(), "catalog.json"), Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
		calls++
		created = engine.NewQueryEngine(engine.QueryEngineConfig{CWD: input.CWD, TranscriptDir: input.TranscriptDir, SessionID: input.SessionID, ThreadID: input.ThreadID, CommandEntrypoint: commands.EntrypointAppServer})
		return created, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownTestServer(t, server)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	response := createExecutionSettingsTestSession(t, httpServer.URL)
	var summary SessionSummary
	decodeResponse(t, response, &summary)
	_ = response.Body.Close()
	endpoint := httpServer.URL + "/v1/sessions/" + summary.ID + "/command-completions"
	before := created.RuntimeSnapshot()
	for input, want := range map[string]string{"/mod": "/model ", "/permissions mode def": "default "} {
		response = doJSON(t, endpoint, "test-token", http.MethodPost, map[string]any{"input": input, "cursor": len([]rune(input))})
		var result commands.InputCompletion
		decodeResponse(t, response, &result)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK || len(result.Candidates) != 1 || result.Candidates[0].Replacement != want {
			t.Fatalf("completion %q = %d %+v", input, response.StatusCode, result)
		}
	}
	if calls != 1 || len(created.GetMessages()) != 0 || !reflect.DeepEqual(before, created.RuntimeSnapshot()) {
		t.Fatal("completion created a session, message, or active turn")
	}
	owned, _ := server.getSession(summary.ID)
	owned.mu.Lock()
	owned.activeTurnID = "busy-test"
	owned.mu.Unlock()
	response = doJSON(t, endpoint, "test-token", http.MethodPost, map[string]any{"input": "/mod", "cursor": 4})
	var result commands.InputCompletion
	decodeResponse(t, response, &result)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || len(result.Candidates) != 0 {
		t.Fatalf("busy completion = %+v", result)
	}
	owned.mu.Lock()
	owned.activeTurnID = ""
	owned.mu.Unlock()
	for _, body := range []string{`{}`, `{"input":"/mod","cursor":null}`, `{"input":"/mod","cursor":9}`, `{"input":"/mod","cursor":4,"path":"/private"}`, `{"input":"/mod","cursor":4,"cursor":1}`, `{"input":"/mod\n","cursor":4}`, `{"input":"` + strings.Repeat("a", 2049) + `","cursor":1}`} {
		request, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer test-token")
		response, err = http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid request = %d", response.StatusCode)
		}
	}
	response = doJSON(t, endpoint, "wrong-token", http.MethodPost, map[string]any{"input": "/mod", "cursor": 4})
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated completion = %d", response.StatusCode)
	}
	response = doJSON(t, httpServer.URL+"/v1/sessions/missing/command-completions", "test-token", http.MethodPost, map[string]any{"input": "/mod", "cursor": 4})
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound || calls != 1 {
		t.Fatal("completion attached a missing session")
	}
}

type blockingCompletionEngine struct {
	*fakeSessionEngine
	entered           chan struct{}
	releaseCompletion chan struct{}
}

func (e *blockingCompletionEngine) CompleteCommandInput(ctx context.Context, _ commands.CompletionRequest) commands.InputCompletion {
	close(e.entered)
	select {
	case <-e.releaseCompletion:
	case <-ctx.Done():
	}
	return commands.InputCompletion{Candidates: []commands.InputCandidate{}}
}

func TestCommandCompletionSnapshotFinishesBeforeEngineClose(t *testing.T) {
	var created *blockingCompletionEngine
	server, err := New(Config{Token: "test-token", SessionCatalogPath: filepath.Join(t.TempDir(), "catalog.json"), Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
		created = &blockingCompletionEngine{fakeSessionEngine: newFakeSessionEngine(input, false), entered: make(chan struct{}), releaseCompletion: make(chan struct{})}
		return created, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownTestServer(t, server)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	response := createExecutionSettingsTestSession(t, httpServer.URL)
	var summary SessionSummary
	decodeResponse(t, response, &summary)
	_ = response.Body.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, httpServer.URL+"/v1/sessions/"+summary.ID+"/command-completions", bytes.NewBufferString(`{"input":"/mod","cursor":4}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer test-token")
	finished := make(chan error, 1)
	go func() {
		result, err := http.DefaultClient.Do(request)
		if err == nil {
			_ = result.Body.Close()
		}
		finished <- err
	}()
	select {
	case <-created.entered:
	case <-ctx.Done():
		t.Fatal("completion did not start")
	}
	owned, _ := server.getSession(summary.ID)
	closed := make(chan error, 1)
	go func() { closed <- owned.close(ctx) }()
	select {
	case <-created.closed:
		close(created.releaseCompletion)
		t.Fatal("engine closed during completion snapshot")
	case <-time.After(20 * time.Millisecond):
	}
	close(created.releaseCompletion)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	select {
	case <-created.closed:
	default:
		t.Fatal("engine did not close after completion settled")
	}
}
