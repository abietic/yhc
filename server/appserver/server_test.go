package appserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/abietic/yhc/engine"
	"github.com/cloudwego/eino/schema"
)

func TestValidateBrowserRequestRequiresYHCCSRF(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:31337/v1/sessions", nil)
	request.Header.Set("Origin", "http://127.0.0.1:31337")
	request.Header.Set("X-YHC-CSRF", "csrf")
	if err := validateBrowserRequest(request, browserSession{csrfToken: "csrf", expiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("YHC CSRF request rejected: %v", err)
	}
	request.Header.Del("X-YHC-CSRF")
	if err := validateBrowserRequest(request, browserSession{csrfToken: "csrf", expiresAt: time.Now().Add(time.Hour)}); err == nil {
		t.Fatal("missing X-YHC-CSRF was accepted")
	}
}

func TestLoopbackURLRejectsNonLoopbackListener(t *testing.T) {
	listener := &nonLoopbackListener{}
	if _, err := loopbackURL(listener); err == nil {
		t.Fatal("non-loopback listener was accepted")
	}
}

func TestLoopbackURLRejectsUnspecifiedListener(t *testing.T) {
	listener := &addressListener{addr: &net.TCPAddr{IP: net.IPv4zero, Port: 31337}}
	if _, err := loopbackURL(listener); err == nil {
		t.Fatal("unspecified listener was accepted as loopback")
	}
}

func TestServerBrowserAuthorityRejectsForgedHostAndOrigin(t *testing.T) {
	server, err := New(Config{
		Token:     "test-token",
		EnableWeb: true,
		WebAssets: fstest.MapFS{
			"assets/index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>YHC</title>")},
		},
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			return newFakeSessionEngine(input, false), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	bootstrap, err := server.BootstrapFor(listener)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, bootstrap.URL+"/v1/auth/browser-session", strings.NewReader(`{"pairing_token":"missing"}`))
	request.Host = "attacker.invalid"
	request.Header.Set("Origin", "http://attacker.invalid")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusMisdirectedRequest {
		t.Fatalf("forged host status = %d, want %d", response.Code, http.StatusMisdirectedRequest)
	}
}

func TestServerReservesSessionCapacityBeforeFactory(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	server, err := New(Config{
		Token:       "test-token",
		MaxSessions: 1,
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			if calls.Add(1) == 1 {
				close(started)
			}
			<-release
			return newFakeSessionEngine(input, false), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	firstDone := make(chan *http.Response, 1)
	firstWorkspace := registerWorkspace(t, httpServer.URL, "test-token", t.TempDir())
	secondWorkspace := registerWorkspace(t, httpServer.URL, "test-token", t.TempDir())
	go func() {
		firstDone <- doJSON(t, httpServer.URL+"/v1/sessions", "test-token", http.MethodPost, map[string]string{"workspace_handle": firstWorkspace.WorkspaceHandle})
	}()
	<-started
	second := doJSON(t, httpServer.URL+"/v1/sessions", "test-token", http.MethodPost, map[string]string{"workspace_handle": secondWorkspace.WorkspaceHandle})
	if second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second create status = %d, want %d", second.StatusCode, http.StatusTooManyRequests)
	}
	_ = second.Body.Close()
	if calls.Load() != 1 {
		t.Fatalf("factory calls = %d, want 1 while capacity is reserved", calls.Load())
	}
	close(release)
	first := <-firstDone
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("first create status = %d", first.StatusCode)
	}
	_ = first.Body.Close()
	retried := doJSON(t, httpServer.URL+"/v1/sessions", "test-token", http.MethodPost, map[string]string{"workspace_handle": secondWorkspace.WorkspaceHandle})
	if retried.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("retry after capacity rejection = %d, want %d while first session remains active", retried.StatusCode, http.StatusTooManyRequests)
	}
	_ = retried.Body.Close()
}

type nonLoopbackListener struct{}

func (*nonLoopbackListener) Accept() (net.Conn, error) { return nil, net.ErrClosed }
func (*nonLoopbackListener) Close() error              { return nil }
func (*nonLoopbackListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 31337}
}

type addressListener struct{ addr net.Addr }

func (*addressListener) Accept() (net.Conn, error) { return nil, net.ErrClosed }
func (*addressListener) Close() error              { return nil }
func (l *addressListener) Addr() net.Addr          { return l.addr }

type fakeSessionEngine struct {
	sessionID      string
	threadID       string
	block          bool
	ignoreCtx      bool
	started        chan struct{}
	release        chan struct{}
	closeOnce      sync.Once
	closed         chan struct{}
	messages       []*schema.Message
	snapshot       engine.RuntimeSnapshot
	transcriptPath string
}

func newFakeSessionEngine(input EngineOptions, block bool) *fakeSessionEngine {
	return &fakeSessionEngine{
		sessionID: input.SessionID,
		threadID:  input.ThreadID,
		block:     block,
		started:   make(chan struct{}),
		release:   make(chan struct{}),
		closed:    make(chan struct{}),
	}
}

func (f *fakeSessionEngine) SubmitMessage(
	ctx context.Context,
	prompt string,
) (<-chan engine.QueryEvent, engine.Terminal) {
	events := make(chan engine.QueryEvent, 4)
	go func() {
		defer close(events)
		f.closeOnce.Do(func() { close(f.started) })
		if f.block {
			if f.ignoreCtx {
				<-f.release
				return
			}
			<-ctx.Done()
			events <- engine.QueryEvent{
				Type: engine.EventTerminal,
				TerminalInfo: &engine.Terminal{
					Reason: engine.TerminalAbortedStreaming,
					Err:    ctx.Err(),
				},
			}
			return
		}
		events <- engine.QueryEvent{
			RuntimeEventEnvelope: engine.RuntimeEventEnvelope{
				SessionID: f.sessionID,
				ThreadID:  f.threadID,
				TurnID:    "engine-turn",
				Sequence:  1,
				Timestamp: time.Now().UTC(),
			},
			Type:    engine.EventAssistant,
			Message: schema.AssistantMessage("reply: "+prompt, nil),
		}
		events <- engine.QueryEvent{
			RuntimeEventEnvelope: engine.RuntimeEventEnvelope{
				SessionID: f.sessionID,
				ThreadID:  f.threadID,
				TurnID:    "engine-turn",
				Sequence:  2,
				Timestamp: time.Now().UTC(),
			},
			Type:         engine.EventTerminal,
			TerminalInfo: &engine.Terminal{Reason: engine.TerminalCompleted},
		}
	}()
	return events, engine.Terminal{}
}

func (f *fakeSessionEngine) SubmitRuntimeItem(
	context.Context,
	engine.RuntimeItem,
) (<-chan engine.QueryEvent, engine.Terminal) {
	events := make(chan engine.QueryEvent)
	close(events)
	return events, engine.Terminal{Reason: engine.TerminalCompleted}
}

func (f *fakeSessionEngine) ClaimNextRuntimeItem() (engine.RuntimeItem, bool, error) {
	return engine.RuntimeItem{}, false, nil
}

func (f *fakeSessionEngine) PendingProjectGraphPermissionRequest() (engine.PermissionRequestEvent, bool) {
	return engine.PermissionRequestEvent{}, false
}

func (f *fakeSessionEngine) ResolvePermissionInteraction(
	string,
	engine.PermissionInteractionResult,
) bool {
	return false
}

func (f *fakeSessionEngine) RequestStop(engine.RuntimeStopMode, string) error {
	return nil
}

func (f *fakeSessionEngine) RuntimeSnapshot() engine.RuntimeSnapshot {
	return f.snapshot
}

func (f *fakeSessionEngine) SubscribeAsyncHookEvents() <-chan engine.QueryEvent {
	return nil
}

func (f *fakeSessionEngine) SessionID() string { return f.sessionID }
func (f *fakeSessionEngine) ThreadID() string  { return f.threadID }
func (f *fakeSessionEngine) AgentID() string   { return "" }
func (f *fakeSessionEngine) TranscriptPath() string {
	return f.transcriptPath
}

func (f *fakeSessionEngine) GetMessages() []*schema.Message {
	return append([]*schema.Message(nil), f.messages...)
}

func (f *fakeSessionEngine) Close() {
	select {
	case <-f.closed:
	default:
		close(f.closed)
	}
}

func doJSON(
	t *testing.T,
	url string,
	token string,
	method string,
	body any,
) *http.Response {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(method, url, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func decodeResponse(t *testing.T, response *http.Response, target any) {
	t.Helper()
	defer response.Body.Close()
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

func readBody(t *testing.T, response *http.Response) string {
	t.Helper()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	return string(data)
}

func waitForSessionStatus(
	t *testing.T,
	baseURL, sessionID, token, want string,
) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		request, err := http.NewRequest(http.MethodGet, baseURL+"/v1/sessions/"+sessionID, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		var summary SessionSummary
		decodeResponse(t, response, &summary)
		if summary.Status == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("session status = %q, want %q", summary.Status, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func hasEventType(events []WireEvent, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func shutdownTestServer(t *testing.T, server *Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown server: %v", err)
	}
}

func getBearer(t *testing.T, endpoint, token string) *http.Response {
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
