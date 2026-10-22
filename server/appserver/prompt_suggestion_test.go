package appserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/config"
	"github.com/abietic/yhc/engine/permission"
)

type suggestionEngine struct {
	*fakeSessionEngine
	calls    atomic.Int32
	generate func(context.Context) string
}

func (e *suggestionEngine) GeneratePromptSuggestion(ctx context.Context) string {
	e.calls.Add(1)
	return e.generate(ctx)
}

func newSuggestionTestServer(t *testing.T, generate func(context.Context) string) (*Server, *httptest.Server, *session, *suggestionEngine) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	var runtime *suggestionEngine
	s, err := New(Config{Token: "test-token", SessionCatalogPath: filepath.Join(t.TempDir(), "catalog.json"), Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
		runtime = &suggestionEngine{fakeSessionEngine: newFakeSessionEngine(input, false), generate: generate}
		return runtime, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownTestServer(t, s) })
	h := httptest.NewServer(s.Handler())
	t.Cleanup(h.Close)
	response := createExecutionSettingsTestSession(t, h.URL)
	var summary SessionSummary
	decodeResponse(t, response, &summary)
	_ = response.Body.Close()
	owned, ok := s.getSession(summary.ID)
	if !ok {
		t.Fatal("missing owned session")
	}
	return s, h, owned, runtime
}

func TestPromptSuggestionRouteRequiresOwnedIdleSessionAndSharedPreference(t *testing.T) {
	_, httpServer, owned, runtime := newSuggestionTestServer(t, func(context.Context) string { return "run the tests" })
	endpoint := httpServer.URL + "/v1/sessions/" + owned.id + "/prompt-suggestion"
	response := doJSON(t, endpoint, "test-token", http.MethodPost, map[string]any{})
	var result struct {
		SessionID  string `json:"session_id"`
		Suggestion string `json:"suggestion"`
	}
	decodeResponse(t, response, &result)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || result.SessionID != owned.id || result.Suggestion != "run the tests" || runtime.calls.Load() != 1 {
		t.Fatalf("suggestion = %d %+v calls=%d", response.StatusCode, result, runtime.calls.Load())
	}
	for _, test := range []struct {
		token, id string
		body      map[string]any
		status    int
	}{
		{"wrong-token", owned.id, map[string]any{}, http.StatusUnauthorized},
		{"test-token", "missing", map[string]any{}, http.StatusNotFound},
		{"test-token", owned.id, map[string]any{"cwd": "/private"}, http.StatusBadRequest},
	} {
		response = doJSON(t, httpServer.URL+"/v1/sessions/"+test.id+"/prompt-suggestion", test.token, http.MethodPost, test.body)
		_ = response.Body.Close()
		if response.StatusCode != test.status {
			t.Fatalf("status=%d want=%d", response.StatusCode, test.status)
		}
	}
	owned.mu.Lock()
	owned.activeTurnID = "busy"
	owned.mu.Unlock()
	response = doJSON(t, endpoint, "test-token", http.MethodPost, map[string]any{})
	decodeResponse(t, response, &result)
	_ = response.Body.Close()
	if result.Suggestion != "" || runtime.calls.Load() != 1 {
		t.Fatal("busy session dispatched suggestion")
	}
	owned.mu.Lock()
	owned.activeTurnID = ""
	owned.mu.Unlock()
	snapshot, err := config.SettingsSnapshot(owned.cwd, "project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = config.SettingsPatch(owned.cwd, "project", snapshot.Revision, map[string]json.RawMessage{"prompt_suggestions": json.RawMessage("false")}); err != nil {
		t.Fatal(err)
	}
	response = doJSON(t, endpoint, "test-token", http.MethodPost, map[string]any{})
	decodeResponse(t, response, &result)
	_ = response.Body.Close()
	if result.Suggestion != "" || runtime.calls.Load() != 1 {
		t.Fatal("disabled preference dispatched suggestion")
	}
}

func TestPromptSuggestionRequestCancelsOnNewTurnAndRejectsLateResult(t *testing.T) {
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	_, _, owned, runtime := newSuggestionTestServer(t, func(ctx context.Context) string {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-release
		return "stale suggestion"
	})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	result := make(chan string, 1)
	go func() { result <- owned.promptSuggestion(context.Background()) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("not started")
	}
	if value := owned.promptSuggestion(context.Background()); value != "" || runtime.calls.Load() != 1 {
		t.Fatal("duplicate in-flight request dispatched")
	}
	if _, err := owned.startTurn(StartTurnRequest{Prompt: "new prompt"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("new turn did not cancel suggestion")
	}
	// Provider cancellation is cooperative; a late non-empty return still must
	// not escape the host's request and active-turn fence.
	close(release)
	select {
	case value := <-result:
		if value != "" {
			t.Fatal("late result escaped")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("not finished")
	}
}

func TestPromptSuggestionCloseCancelsAndJoinsBeforeEngineClose(t *testing.T) {
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	_, _, owned, runtime := newSuggestionTestServer(t, func(ctx context.Context) string {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-release
		return "stale suggestion"
	})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	result := make(chan string, 1)
	go func() { result <- owned.promptSuggestion(context.Background()) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("not started")
	}
	closed := make(chan error, 1)
	go func() { closed <- owned.close(context.Background()) }()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("close did not cancel")
	}
	select {
	case <-runtime.closed:
		t.Fatal("engine closed before suggestion joined")
	default:
	}
	close(release)
	select {
	case value := <-result:
		if value != "" {
			t.Fatal("close result escaped")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("suggestion did not finish")
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("close did not finish")
	}
}

func TestPromptSuggestionCallerCancellationAndPreferenceChangeDropResults(t *testing.T) {
	for _, change := range []string{"caller", "preference"} {
		t.Run(change, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			_, _, owned, _ := newSuggestionTestServer(t, func(context.Context) string { close(entered); <-release; return "run the tests" })
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan string, 1)
			go func() { result <- owned.promptSuggestion(ctx) }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("not started")
			}
			if change == "caller" {
				cancel()
			} else {
				snapshot, err := config.SettingsSnapshot(owned.cwd, "project")
				if err != nil {
					t.Fatal(err)
				}
				if _, err = config.SettingsPatch(owned.cwd, "project", snapshot.Revision, map[string]json.RawMessage{"prompt_suggestions": json.RawMessage("false")}); err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			select {
			case value := <-result:
				if value != "" {
					t.Fatal("invalidated result escaped")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("not finished")
			}
		})
	}
}

type suggestionQueueEngine struct {
	*queueSessionEngine
	generate func(context.Context) string
}

func (e *suggestionQueueEngine) GeneratePromptSuggestion(ctx context.Context) string {
	return e.generate(ctx)
}

type suggestionControlEngine struct {
	*executionSettingsEngine
	generate func(context.Context) string
}

func (e *suggestionControlEngine) GeneratePromptSuggestion(ctx context.Context) string {
	return e.generate(ctx)
}

func TestPromptSuggestionQueueAndExecutionControlCancelWithoutWaitingForProvider(t *testing.T) {
	for _, boundary := range []string{"queue", "execution-control"} {
		t.Run(boundary, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			generate := func(ctx context.Context) string {
				close(entered)
				<-ctx.Done()
				close(cancelled)
				<-release
				return "stale suggestion"
			}
			var queue *queueSessionEngine
			owned, err := newSession(context.Background(), func(_ context.Context, input EngineOptions) (SessionEngine, error) {
				if boundary == "queue" {
					queue = newQueueSessionEngine(input)
					return &suggestionQueueEngine{queueSessionEngine: queue, generate: generate}, nil
				}
				return &suggestionControlEngine{executionSettingsEngine: &executionSettingsEngine{fakeSessionEngine: newFakeSessionEngine(input, false), mode: permission.ModeDefault}, generate: generate}, nil
			}, "suggestion-test", CreateSessionRequest{CWD: t.TempDir()}, nil, 128, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if err := owned.close(ctx); err != nil {
					t.Error(err)
				}
			})
			result := make(chan string, 1)
			go func() { result <- owned.promptSuggestion(context.Background()) }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("not started")
			}
			admitted := make(chan error, 1)
			go func() {
				if boundary == "queue" {
					_, err := queue.EnqueueUserInputWithID("queue-fixture", engine.UserTurnInput{Prompt: "queued input", Display: "queued input"})
					if err == nil {
						owned.turnAdmissionMu.Lock()
						owned.startNextRuntimeItemLocked()
						owned.turnAdmissionMu.Unlock()
					}
					admitted <- err
				} else {
					_, err := owned.updateExecutionSettings(context.Background(), UpdateExecutionSettingsRequest{PermissionMode: stringPointer("plan")})
					admitted <- err
				}
			}()
			select {
			case err := <-admitted:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("admission blocked behind provider")
			}
			select {
			case <-cancelled:
			case <-time.After(3 * time.Second):
				t.Fatal("boundary did not cancel")
			}
			close(release)
			select {
			case value := <-result:
				if value != "" {
					t.Fatal("late result escaped")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("not finished")
			}
		})
	}
}
