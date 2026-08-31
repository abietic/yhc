package appserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abietic/yhc/engine"
	enginesession "github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
)

type recoveredProjectGraphEngine struct {
	*fakeSessionEngine
	pending            engine.PermissionRequestEvent
	resolved           chan struct{}
	runtimeSubmitted   chan struct{}
	resolvedOnce       sync.Once
	runtimeOnce        sync.Once
	pendingActive      atomic.Bool
	submitMessageCalls atomic.Int32
}

type recoveredRuntimeQueueEngine struct {
	*queueSessionEngine

	claimed          atomic.Bool
	pendingChecks    atomic.Int32
	restoreWaitTimed atomic.Bool
	runtimeSubmitted chan struct{}
	runtimeOnce      sync.Once
}

func newRecoveredRuntimeQueueEngine(input EngineOptions) *recoveredRuntimeQueueEngine {
	base := newQueueSessionEngine(input)
	base.ready <- struct{}{}
	return &recoveredRuntimeQueueEngine{
		queueSessionEngine: base,
		runtimeSubmitted:   make(chan struct{}),
	}
}

func (e *recoveredRuntimeQueueEngine) PendingProjectGraphPermissionRequest() (
	engine.PermissionRequestEvent,
	bool,
) {
	if e.pendingChecks.Add(1) != 1 {
		return engine.PermissionRequestEvent{}, false
	}
	// A constructor-started pump has subscribed before attach can reserve its
	// explicit first turn. Waiting for its runtime submission makes that bad
	// ordering deterministic instead of relying on goroutine scheduling.
	select {
	case <-e.subscribed:
		select {
		case <-e.runtimeSubmitted:
		case <-time.After(2 * time.Second):
			e.restoreWaitTimed.Store(true)
		}
	default:
	}
	return engine.PermissionRequestEvent{}, false
}

func (e *recoveredRuntimeQueueEngine) ClaimNextRuntimeItem() (
	engine.RuntimeItem,
	bool,
	error,
) {
	if !e.claimed.CompareAndSwap(false, true) {
		return engine.RuntimeItem{}, false, nil
	}
	return engine.RuntimeItem{
		ID:         "recovered-generic",
		Kind:       engine.RuntimeItemAgentNotification,
		Priority:   engine.RuntimePriorityNext,
		State:      engine.RuntimeItemProcessing,
		EnqueuedAt: time.Now().UTC(),
		AgentNotification: &engine.RuntimeAgentNotification{
			AgentID: "recovered-agent",
			Status:  "completed",
			Message: "recovered runtime event",
		},
	}, true, nil
}

func (e *recoveredRuntimeQueueEngine) SubmitRuntimeItem(
	ctx context.Context,
	item engine.RuntimeItem,
) (<-chan engine.QueryEvent, engine.Terminal) {
	e.runtimeOnce.Do(func() { close(e.runtimeSubmitted) })
	return e.queueSessionEngine.SubmitRuntimeItem(ctx, item)
}

func (e *recoveredProjectGraphEngine) SubmitMessage(
	context.Context,
	string,
) (<-chan engine.QueryEvent, engine.Terminal) {
	e.submitMessageCalls.Add(1)
	events := make(chan engine.QueryEvent)
	close(events)
	return events, engine.Terminal{Reason: engine.TerminalCompleted}
}

func (e *recoveredProjectGraphEngine) PendingProjectGraphPermissionRequest() (
	engine.PermissionRequestEvent,
	bool,
) {
	return e.pending, e.pendingActive.Load()
}

func (e *recoveredProjectGraphEngine) ResolvePermissionInteraction(
	string,
	engine.PermissionInteractionResult,
) bool {
	e.pendingActive.Store(false)
	e.resolvedOnce.Do(func() { close(e.resolved) })
	return true
}

func (e *recoveredProjectGraphEngine) ClaimNextRuntimeItem() (
	engine.RuntimeItem,
	bool,
	error,
) {
	return engine.RuntimeItem{Kind: engine.RuntimeItemPermissionDecision}, true, nil
}

func (e *recoveredProjectGraphEngine) SubmitRuntimeItem(
	context.Context,
	engine.RuntimeItem,
) (<-chan engine.QueryEvent, engine.Terminal) {
	e.runtimeOnce.Do(func() { close(e.runtimeSubmitted) })
	events := make(chan engine.QueryEvent)
	close(events)
	return events, engine.Terminal{Reason: engine.TerminalCompleted}
}

func TestAttachTurnCoalescesAndReplaysReceipt(t *testing.T) {
	catalogDir := t.TempDir()
	root := t.TempDir()
	catalog := filepath.Join(catalogDir, "roots.json")
	id := "11111111-1111-4111-8111-111111111111"
	writeDurableSession(
		t,
		filepath.Join(root, ".yhc", "transcripts"),
		id,
		root,
		"saved",
		"main",
	)
	if err := enginesession.RegisterSessionRoot(
		catalog,
		root,
		filepath.Join(root, ".yhc", "transcripts"),
		time.Now(),
	); err != nil {
		t.Fatal(err)
	}
	var factories atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	server, err := New(Config{
		Token:              "test-token",
		SessionCatalogPath: catalog,
		DiscoveryCWD:       catalogDir,
		ValidateResume:     func(context.Context, EngineOptions) error { return nil },
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			factories.Add(1)
			close(entered)
			<-release
			return newFakeSessionEngine(input, false), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	defer shutdownTestServer(t, server)
	input := AttachTurnRequest{
		Prompt:       "hello",
		ClientTurnID: "22222222-2222-4222-8222-222222222222",
	}
	endpoint := httpServer.URL + "/v1/durable-sessions/" + id + "/attach-turn"
	firstDone := make(chan bufferedJSONResponse, 1)
	go func() {
		firstDone <- doJSONBuffered(
			endpoint,
			"test-token",
			http.MethodPost,
			input,
		)
	}()
	<-entered
	secondDone := make(chan bufferedJSONResponse, 1)
	go func() {
		secondDone <- doJSONBuffered(
			endpoint,
			"test-token",
			http.MethodPost,
			input,
		)
	}()
	close(release)
	first, second := <-firstDone, <-secondDone
	for _, response := range []bufferedJSONResponse{first, second} {
		if response.err != nil {
			t.Fatal(response.err)
		}
		if response.statusCode != http.StatusOK {
			t.Fatalf("attach = %d: %s", response.statusCode, response.body)
		}
		var got AttachTurnResponse
		if err := json.Unmarshal(response.body, &got); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if got.Status != "turn_accepted" || got.TurnID != input.ClientTurnID {
			t.Fatalf("response = %+v", got)
		}
	}
	if factories.Load() != 1 {
		t.Fatalf("factory calls = %d, want 1", factories.Load())
	}
	replay := doJSON(
		t,
		httpServer.URL+"/v1/durable-sessions/"+id+"/attach-turn",
		"test-token",
		http.MethodPost,
		input,
	)
	if replay.StatusCode != http.StatusOK {
		t.Fatalf("replay = %d: %s", replay.StatusCode, readBody(t, replay))
	}
	_ = replay.Body.Close()
}

func TestAttachTurnAdmitsExplicitPromptBeforeRecoveredRuntimeQueue(t *testing.T) {
	catalogDir, root := t.TempDir(), t.TempDir()
	catalog := filepath.Join(catalogDir, "roots.json")
	const sessionID = "21212121-2121-4121-8121-212121212121"
	transcriptDir := filepath.Join(root, ".yhc", "transcripts")
	writeDurableSession(t, transcriptDir, sessionID, root, "saved", "main")
	if err := enginesession.RegisterSessionRoot(catalog, root, transcriptDir, time.Now()); err != nil {
		t.Fatal(err)
	}
	var created *recoveredRuntimeQueueEngine
	server, err := New(Config{
		Token:              "test-token",
		SessionCatalogPath: catalog,
		DiscoveryCWD:       catalogDir,
		ValidateResume:     func(context.Context, EngineOptions) error { return nil },
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			created = newRecoveredRuntimeQueueEngine(input)
			return created, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	t.Cleanup(func() { shutdownTestServer(t, server) })

	const clientTurnID = "22222222-2222-4222-8222-222222222223"
	response := doJSON(
		t,
		httpServer.URL+"/v1/durable-sessions/"+sessionID+"/attach-turn",
		"test-token",
		http.MethodPost,
		AttachTurnRequest{Prompt: "explicit attach prompt", ClientTurnID: clientTurnID},
	)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("attach = %d: %s", response.StatusCode, readBody(t, response))
	}
	var attached AttachTurnResponse
	decodeResponse(t, response, &attached)
	if attached.Status != "turn_accepted" || attached.TurnID != clientTurnID ||
		attached.Session.ActiveTurnID != clientTurnID {
		t.Fatalf("attach response = %#v", attached)
	}
	if created.restoreWaitTimed.Load() {
		t.Fatal("attach waited for a constructor-started runtime pump")
	}
	select {
	case <-created.directStarted:
	case <-time.After(time.Second):
		t.Fatal("explicit attach prompt did not start")
	}
	if created.claimed.Load() {
		t.Fatal("recovered runtime item was claimed before explicit attach terminal")
	}
	select {
	case item := <-created.runtimeStarted:
		t.Fatalf("recovered runtime item started before explicit attach terminal: %#v", item)
	default:
	}

	owned, ok := server.getSession(sessionID)
	if !ok {
		t.Fatal("attached session disappeared")
	}
	_, updates, unsubscribe, _, err := owned.events.subscribe(owned.events.latestCursor())
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	close(created.directRelease)
	select {
	case item := <-created.runtimeStarted:
		if item.ID != "recovered-generic" || item.Kind != engine.RuntimeItemAgentNotification {
			t.Fatalf("recovered runtime item = %#v", item)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("recovered runtime item did not start after explicit attach terminal")
	}
	close(created.runtimeRelease)

	directFinished := false
	runtimeTurnID := ""
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event, open := <-updates:
			if !open {
				t.Fatal("event stream closed before recovered runtime item settled")
			}
			if event.Type == "turn.finished" && event.TurnID == clientTurnID {
				directFinished = true
			}
			if event.Type == "turn.accepted" && event.TurnID != clientTurnID {
				if !directFinished {
					t.Fatal("recovered runtime turn was accepted before explicit attach terminal")
				}
				runtimeTurnID = event.TurnID
			}
			if runtimeTurnID != "" && event.Type == "turn.finished" && event.TurnID == runtimeTurnID {
				return
			}
		case <-timer.C:
			t.Fatalf("recovered runtime turn did not settle: %#v", owned.summary())
		}
	}
}

func TestAttachTurnCountsConcurrentSessionCreationAgainstCapacity(t *testing.T) {
	catalogDir, root := t.TempDir(), t.TempDir()
	catalog := filepath.Join(catalogDir, "roots.json")
	id := "23232323-2323-4232-8232-232323232323"
	transcriptDir := filepath.Join(root, ".yhc", "transcripts")
	writeDurableSession(t, transcriptDir, id, root, "saved", "main")
	if err := enginesession.RegisterSessionRoot(catalog, root, transcriptDir, time.Now()); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	server, err := New(Config{
		Token:              "test-token",
		MaxSessions:        1,
		SessionCatalogPath: catalog,
		DiscoveryCWD:       catalogDir,
		ValidateResume:     func(context.Context, EngineOptions) error { return nil },
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
	defer httpServer.Close()
	defer shutdownTestServer(t, server)
	workspace := registerWorkspace(t, httpServer.URL, "test-token", t.TempDir())
	created := make(chan bufferedJSONResponse, 1)
	go func() {
		created <- doJSONBuffered(
			httpServer.URL+"/v1/sessions",
			"test-token",
			http.MethodPost,
			map[string]string{"workspace_handle": workspace.WorkspaceHandle},
		)
	}()
	<-started

	attached := doJSON(
		t,
		httpServer.URL+"/v1/durable-sessions/"+id+"/attach-turn",
		"test-token",
		http.MethodPost,
		AttachTurnRequest{Prompt: "resume", ClientTurnID: "24242424-2424-4242-8242-242424242424"},
	)
	defer attached.Body.Close()
	assertAttachError(t, attached, http.StatusTooManyRequests, "session_limit")
	if got := calls.Load(); got != 1 {
		t.Fatalf("factory calls while create reserves capacity = %d, want 1", got)
	}

	close(release)
	select {
	case response := <-created:
		if response.err != nil {
			t.Fatal(response.err)
		}
		if response.statusCode != http.StatusCreated {
			t.Fatalf("create after release = %d: %s", response.statusCode, response.body)
		}
	case <-time.After(time.Second):
		t.Fatal("create did not complete after factory release")
	}
}

func TestAttachTurnConflicts(t *testing.T) {
	server, base, id := attachTestServer(t)
	defer shutdownTestServer(t, server)
	first := AttachTurnRequest{
		Prompt:       "one",
		ClientTurnID: "33333333-3333-4333-8333-333333333333",
	}
	response := doJSON(
		t,
		base+"/v1/durable-sessions/"+id+"/attach-turn",
		"test-token",
		http.MethodPost,
		first,
	)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("attach = %d: %s", response.StatusCode, readBody(t, response))
	}
	_ = response.Body.Close()
	for _, input := range []AttachTurnRequest{
		{Prompt: "two", ClientTurnID: first.ClientTurnID},
		{Prompt: "one", ClientTurnID: "44444444-4444-4444-8444-444444444444"},
	} {
		response = doJSON(
			t,
			base+"/v1/durable-sessions/"+id+"/attach-turn",
			"test-token",
			http.MethodPost,
			input,
		)
		if response.StatusCode != http.StatusConflict {
			t.Fatalf("conflict = %d: %s", response.StatusCode, readBody(t, response))
		}
		var envelope ErrorEnvelope
		decodeResponse(t, response, &envelope)
		if input.ClientTurnID == first.ClientTurnID &&
			envelope.Error.Code != "client_turn_conflict" {
			t.Fatalf("same id = %+v", envelope)
		}
		if input.ClientTurnID != first.ClientTurnID &&
			envelope.Error.Code != "session_already_attached" {
			t.Fatalf("unknown id = %+v", envelope)
		}
	}
}

func TestAttachTurnRejectsInvalidAndUnknownFields(t *testing.T) {
	server, base, id := attachTestServer(t)
	defer shutdownTestServer(t, server)
	response := doJSON(
		t,
		base+"/v1/durable-sessions/"+id+"/attach-turn",
		"test-token",
		http.MethodPost,
		map[string]any{"prompt": "x", "client_turn_id": "bad"},
	)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid = %d", response.StatusCode)
	}
	_ = response.Body.Close()
	body, _ := json.Marshal(map[string]any{
		"prompt":         "x",
		"client_turn_id": "55555555-5555-4555-8555-555555555555",
		"cwd":            t.TempDir(),
	})
	req, _ := http.NewRequest(
		http.MethodPost,
		base+"/v1/durable-sessions/"+id+"/attach-turn",
		bytes.NewReader(body),
	)
	req.Header.Set("Authorization", "Bearer test-token")
	response, _ = http.DefaultClient.Do(req)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field = %d", response.StatusCode)
	}
	_ = response.Body.Close()
}

func TestImportAndAttachDTOsDoNotExposeFilesystemLocations(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "protocol.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	foundAttach := false
	for _, declaration := range file.Decls {
		generic, ok := declaration.(*ast.GenDecl)
		if !ok || generic.Tok != token.TYPE {
			continue
		}
		for _, spec := range generic.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok || (typeSpec.Name.Name != "AttachTurnRequest" && !strings.Contains(typeSpec.Name.Name, "Import")) {
				continue
			}
			if typeSpec.Name.Name == "AttachTurnRequest" {
				foundAttach = true
			}
			structType, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				t.Fatalf("%s is not a request struct", typeSpec.Name.Name)
			}
			for _, field := range structType.Fields.List {
				for _, name := range field.Names {
					lower := strings.ToLower(name.Name)
					if strings.Contains(lower, "cwd") || strings.Contains(lower, "path") ||
						strings.Contains(lower, "transcript") || strings.Contains(lower, "root") ||
						strings.Contains(lower, "directory") {
						t.Fatalf("%s exposes renderer filesystem field %q", typeSpec.Name.Name, name.Name)
					}
				}
			}
		}
	}
	if !foundAttach {
		t.Fatal("AttachTurnRequest is missing")
	}
}

func TestAttachTurnShutdownReleasesFlight(t *testing.T) {
	catalogDir, root := t.TempDir(), t.TempDir()
	catalog := filepath.Join(catalogDir, "roots.json")
	id := "66666666-6666-4666-8666-666666666666"
	dir := filepath.Join(root, ".yhc", "transcripts")
	writeDurableSession(t, dir, id, root, "saved", "main")
	if err := enginesession.RegisterSessionRoot(catalog, root, dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	server, err := New(Config{
		Token:              "test-token",
		SessionCatalogPath: catalog,
		DiscoveryCWD:       catalogDir,
		ValidateResume:     func(context.Context, EngineOptions) error { return nil },
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			close(entered)
			<-release
			return newFakeSessionEngine(input, false), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(AttachTurnRequest{
		Prompt:       "hello",
		ClientTurnID: "77777777-7777-4777-8777-777777777777",
	})
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/durable-sessions/"+id+"/attach-turn",
		bytes.NewReader(body),
	)
	request.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		server.Handler().ServeHTTP(response, request)
		close(done)
	}()
	<-entered
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- server.Shutdown(shutdownCtx) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not wake attach waiter")
	}
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("shutdown response = %d: %s", response.Code, response.Body.String())
	}
	select {
	case err := <-shutdownDone:
		t.Fatalf("shutdown returned before activation cleanup: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-shutdownDone; err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	lease, err := acquireSessionLease(dir, id, "replacement-server")
	if err != nil {
		t.Fatalf("acquire lease after shutdown: %v", err)
	}
	if err := lease.close(); err != nil {
		t.Fatalf("close replacement lease: %v", err)
	}
}

func TestAttachTurnShutdownTimeoutRemainsObservable(t *testing.T) {
	catalogDir, root := t.TempDir(), t.TempDir()
	catalog := filepath.Join(catalogDir, "roots.json")
	id := "67676767-6767-4767-8767-676767676767"
	dir := filepath.Join(root, ".yhc", "transcripts")
	writeDurableSession(t, dir, id, root, "saved", "main")
	if err := enginesession.RegisterSessionRoot(catalog, root, dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	server, err := New(Config{
		Token:              "test-token",
		SessionCatalogPath: catalog,
		DiscoveryCWD:       catalogDir,
		ValidateResume:     func(context.Context, EngineOptions) error { return nil },
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			close(entered)
			<-release
			return newFakeSessionEngine(input, false), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(AttachTurnRequest{
		Prompt:       "hello",
		ClientTurnID: "68686868-6868-4868-8868-686868686868",
	})
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/durable-sessions/"+id+"/attach-turn",
		bytes.NewReader(body),
	)
	request.Header.Set("Authorization", "Bearer test-token")
	handlerDone := make(chan struct{})
	go func() {
		server.Handler().ServeHTTP(httptest.NewRecorder(), request)
		close(handlerDone)
	}()
	<-entered
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err = server.Shutdown(shutdownCtx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown timeout = %v", err)
	}
	if retryErr := server.Shutdown(context.Background()); !errors.Is(retryErr, context.DeadlineExceeded) {
		t.Fatalf("repeated shutdown hid first error: %v", retryErr)
	}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("shutdown timeout did not wake attach waiter")
	}
	close(release)
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
	defer cleanupCancel()
	if err := server.waitForAttachActivations(cleanupCtx); err != nil {
		t.Fatalf("activation cleanup after timeout: %v", err)
	}
	lease, err := acquireSessionLease(dir, id, "replacement-after-timeout")
	if err != nil {
		t.Fatalf("acquire lease after delayed cleanup: %v", err)
	}
	if err := lease.close(); err != nil {
		t.Fatalf("close replacement lease: %v", err)
	}
}

func TestAttachTurnContinuesAfterRequestCancellation(t *testing.T) {
	catalogDir, root := t.TempDir(), t.TempDir()
	catalog := filepath.Join(catalogDir, "roots.json")
	id := "88888888-8888-4888-8888-888888888888"
	dir := filepath.Join(root, ".yhc", "transcripts")
	writeDurableSession(t, dir, id, root, "saved", "main")
	if err := enginesession.RegisterSessionRoot(catalog, root, dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var factories atomic.Int32
	server, err := New(Config{
		Token:              "test-token",
		SessionCatalogPath: catalog,
		DiscoveryCWD:       catalogDir,
		ValidateResume:     func(context.Context, EngineOptions) error { return nil },
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			factories.Add(1)
			close(entered)
			<-release
			return newFakeSessionEngine(input, false), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownTestServer(t, server)
	input := AttachTurnRequest{
		Prompt:       "continue after disconnect",
		ClientTurnID: "99999999-9999-4999-8999-999999999999",
	}
	body, _ := json.Marshal(input)
	requestContext, cancelRequest := context.WithCancel(context.Background())
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/durable-sessions/"+id+"/attach-turn",
		bytes.NewReader(body),
	).WithContext(requestContext)
	request.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	handlerDone := make(chan struct{})
	go func() {
		server.Handler().ServeHTTP(response, request)
		close(handlerDone)
	}()
	<-entered
	cancelRequest()
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("cancelled request handler did not return")
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for {
		if _, live := server.getSession(id); live {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server-owned activation did not complete")
		}
		time.Sleep(time.Millisecond)
	}

	retryBody, _ := json.Marshal(input)
	retry := httptest.NewRequest(
		http.MethodPost,
		"/v1/durable-sessions/"+id+"/attach-turn",
		bytes.NewReader(retryBody),
	)
	retry.Header.Set("Authorization", "Bearer test-token")
	retryResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(retryResponse, retry)
	if retryResponse.Code != http.StatusOK {
		t.Fatalf("receipt replay = %d: %s", retryResponse.Code, retryResponse.Body.String())
	}
	if factories.Load() != 1 {
		t.Fatalf("factory calls = %d, want 1", factories.Load())
	}
}

func TestAttachTurnFailureReleasesFlightForRetry(t *testing.T) {
	catalogDir, root := t.TempDir(), t.TempDir()
	catalog := filepath.Join(catalogDir, "roots.json")
	id := "12121212-1212-4212-8212-121212121212"
	dir := filepath.Join(root, ".yhc", "transcripts")
	writeDurableSession(t, dir, id, root, "saved", "main")
	if err := enginesession.RegisterSessionRoot(catalog, root, dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	var validations atomic.Int32
	var factories atomic.Int32
	server, err := New(Config{
		Token:              "test-token",
		SessionCatalogPath: catalog,
		DiscoveryCWD:       catalogDir,
		ValidateResume: func(context.Context, EngineOptions) error {
			if validations.Add(1) == 1 {
				return errors.New("validation failed")
			}
			return nil
		},
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			factories.Add(1)
			return newFakeSessionEngine(input, false), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	defer shutdownTestServer(t, server)
	input := AttachTurnRequest{
		Prompt:       "retry me",
		ClientTurnID: "13131313-1313-4313-8313-131313131313",
	}
	endpoint := httpServer.URL + "/v1/durable-sessions/" + id + "/attach-turn"
	failed := doJSON(t, endpoint, "test-token", http.MethodPost, input)
	if failed.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("first attach = %d: %s", failed.StatusCode, readBody(t, failed))
	}
	_ = failed.Body.Close()
	retried := doJSON(t, endpoint, "test-token", http.MethodPost, input)
	if retried.StatusCode != http.StatusOK {
		t.Fatalf("retry attach = %d: %s", retried.StatusCode, readBody(t, retried))
	}
	_ = retried.Body.Close()
	if validations.Load() != 2 || factories.Load() != 1 {
		t.Fatalf("validations = %d, factories = %d", validations.Load(), factories.Load())
	}
}

func TestAttachTurnUsesAdmittedTranscriptDirectoryForRuntimeLease(t *testing.T) {
	catalogDir, root := t.TempDir(), t.TempDir()
	catalog := filepath.Join(catalogDir, "roots.json")
	id := "24242424-2424-4242-8242-242424242424"
	transcriptDir := filepath.Join(root, ".yhc", "transcripts")
	writeDurableSession(t, transcriptDir, id, root, "saved", "main")
	if err := enginesession.RegisterSessionRoot(catalog, root, transcriptDir, time.Now()); err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{
		Token:              "test-token",
		SessionCatalogPath: catalog,
		DiscoveryCWD:       catalogDir,
		ValidateResume:     func(context.Context, EngineOptions) error { return nil },
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			canonicalTranscriptDir, err := filepath.EvalSymlinks(transcriptDir)
			if err != nil {
				t.Fatal(err)
			}
			if !input.Resume || input.TranscriptDir != canonicalTranscriptDir {
				t.Fatalf("attach runtime options = %+v, want admitted transcript dir %q", input, transcriptDir)
			}
			return newFakeSessionEngine(input, false), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	defer shutdownTestServer(t, server)
	response := doJSON(
		t,
		httpServer.URL+"/v1/durable-sessions/"+id+"/attach-turn",
		"test-token",
		http.MethodPost,
		AttachTurnRequest{Prompt: "resume", ClientTurnID: "25252525-2525-4252-8252-252525252525"},
	)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("attach = %d: %s", response.StatusCode, readBody(t, response))
	}
	_ = response.Body.Close()
}

func TestAttachTurnRejectsMissingAmbiguousAndNonResumable(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		server, base, _ := attachTestServer(t)
		defer shutdownTestServer(t, server)
		response := doJSON(
			t,
			base+"/v1/durable-sessions/missing-session/attach-turn",
			"test-token",
			http.MethodPost,
			AttachTurnRequest{
				Prompt:       "hello",
				ClientTurnID: "14141414-1414-4414-8414-141414141414",
			},
		)
		defer response.Body.Close()
		assertAttachError(t, response, http.StatusNotFound, "durable_session_not_found")
	})

	t.Run("ambiguous", func(t *testing.T) {
		catalogDir := t.TempDir()
		catalog := filepath.Join(catalogDir, "roots.json")
		id := "15151515-1515-4515-8515-151515151515"
		for _, root := range []string{t.TempDir(), t.TempDir()} {
			dir := filepath.Join(root, ".yhc", "transcripts")
			writeDurableSession(t, dir, id, root, "saved", "main")
			if err := enginesession.RegisterSessionRoot(catalog, root, dir, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
		var factories atomic.Int32
		server, err := New(Config{
			Token:              "test-token",
			SessionCatalogPath: catalog,
			DiscoveryCWD:       catalogDir,
			ValidateResume:     func(context.Context, EngineOptions) error { return nil },
			Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
				factories.Add(1)
				return newFakeSessionEngine(input, false), nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		httpServer := httptest.NewServer(server.Handler())
		defer httpServer.Close()
		defer shutdownTestServer(t, server)
		response := doJSON(
			t,
			httpServer.URL+"/v1/durable-sessions/"+id+"/attach-turn",
			"test-token",
			http.MethodPost,
			AttachTurnRequest{
				Prompt:       "hello",
				ClientTurnID: "16161616-1616-4616-8616-161616161616",
			},
		)
		defer response.Body.Close()
		assertAttachError(t, response, http.StatusConflict, "durable_session_ambiguous")
		if factories.Load() != 0 {
			t.Fatalf("ambiguous record created %d engines", factories.Load())
		}
	})

	t.Run("non-resumable", func(t *testing.T) {
		catalogDir, root := t.TempDir(), t.TempDir()
		catalog := filepath.Join(catalogDir, "roots.json")
		id := "17171717-1717-4717-8717-171717171717"
		dir := filepath.Join(root, ".yhc", "transcripts")
		writeDurableSession(t, dir, id, root, "saved", "main")
		recorder := transcript.NewRecorder(id, dir)
		if err := recorder.RecordMetadata("parent_session_id", "parent-session"); err != nil {
			t.Fatal(err)
		}
		if err := recorder.Flush(); err != nil {
			t.Fatal(err)
		}
		if err := recorder.Close(); err != nil {
			t.Fatal(err)
		}
		if err := enginesession.RegisterSessionRoot(catalog, root, dir, time.Now()); err != nil {
			t.Fatal(err)
		}
		var factories atomic.Int32
		server, err := New(Config{
			Token:              "test-token",
			SessionCatalogPath: catalog,
			DiscoveryCWD:       catalogDir,
			ValidateResume:     func(context.Context, EngineOptions) error { return nil },
			Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
				factories.Add(1)
				return newFakeSessionEngine(input, false), nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		httpServer := httptest.NewServer(server.Handler())
		defer httpServer.Close()
		defer shutdownTestServer(t, server)
		response := doJSON(
			t,
			httpServer.URL+"/v1/durable-sessions/"+id+"/attach-turn",
			"test-token",
			http.MethodPost,
			AttachTurnRequest{
				Prompt:       "hello",
				ClientTurnID: "18181818-1818-4818-8818-181818181818",
			},
		)
		defer response.Body.Close()
		assertAttachError(
			t,
			response,
			http.StatusUnprocessableEntity,
			"durable_session_not_resumable",
		)
		if factories.Load() != 0 {
			t.Fatalf("non-resumable record created %d engines", factories.Load())
		}
	})
}

func TestAttachTurnRestoresProjectGraphInteractionWithoutSubmittingPrompt(t *testing.T) {
	catalogDir, root := t.TempDir(), t.TempDir()
	catalog := filepath.Join(catalogDir, "roots.json")
	id := "19191919-1919-4919-8919-191919191919"
	dir := filepath.Join(root, ".yhc", "transcripts")
	writeDurableSession(t, dir, id, root, "Saved title", "main")
	if err := enginesession.RegisterSessionRoot(catalog, root, dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	request := permissionBrokerTestRequest("recovered-project-graph")
	var created *recoveredProjectGraphEngine
	server, err := New(Config{
		Token:              "test-token",
		SessionCatalogPath: catalog,
		DiscoveryCWD:       catalogDir,
		ValidateResume:     func(context.Context, EngineOptions) error { return nil },
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			created = &recoveredProjectGraphEngine{
				fakeSessionEngine: newFakeSessionEngine(input, false),
				pending: engine.PermissionRequestEvent{
					Kind:              request.Kind,
					Source:            "project_graph",
					ToolName:          request.ToolName,
					CanonicalToolName: request.CanonicalToolName,
					ToolUseID:         request.ToolUseID,
					Input:             request.Input,
					Message:           request.Message,
					Presentation:      request.Presentation,
				},
				resolved:         make(chan struct{}),
				runtimeSubmitted: make(chan struct{}),
			}
			created.pendingActive.Store(true)
			return created, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	defer shutdownTestServer(t, server)
	clientTurnID := "20202020-2020-4020-8020-202020202020"
	response := doJSON(
		t,
		httpServer.URL+"/v1/durable-sessions/"+id+"/attach-turn",
		"test-token",
		http.MethodPost,
		AttachTurnRequest{Prompt: "do not submit yet", ClientTurnID: clientTurnID},
	)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("attach = %d: %s", response.StatusCode, readBody(t, response))
	}
	var attached AttachTurnResponse
	decodeResponse(t, response, &attached)
	if attached.Status != "interaction_required" || attached.Interaction == nil ||
		attached.Interaction.RequestID != request.ToolUseID ||
		attached.Session.ActiveTurnID != attached.Interaction.TurnID ||
		attached.Session.ActiveTurnID == clientTurnID || attached.Session.Status != "waiting" {
		t.Fatalf("interaction attach = %#v", attached)
	}
	if created.submitMessageCalls.Load() != 0 {
		t.Fatal("recovered interaction submitted the new prompt")
	}
	resolve := doJSON(
		t,
		httpServer.URL+"/v1/sessions/"+id+"/interactions/"+request.ToolUseID+"/resolve",
		"test-token",
		http.MethodPost,
		permissionResolution(engine.PermissionAllowOnce),
	)
	if resolve.StatusCode != http.StatusOK {
		t.Fatalf("resolve = %d: %s", resolve.StatusCode, readBody(t, resolve))
	}
	_ = resolve.Body.Close()
	select {
	case <-created.resolved:
	case <-time.After(time.Second):
		t.Fatal("permission decision did not reach recovered engine")
	}
	select {
	case <-created.runtimeSubmitted:
	case <-time.After(time.Second):
		t.Fatal("recovered runtime item was not submitted")
	}
	if created.submitMessageCalls.Load() != 0 {
		t.Fatal("resolving recovered interaction submitted the retained prompt")
	}
	deadline := time.Now().Add(time.Second)
	for {
		owned, live := server.getSession(id)
		if live && owned.summary().ActiveTurnID == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("recovered interaction turn did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	explicit := doJSON(
		t,
		httpServer.URL+"/v1/sessions/"+id+"/turns",
		"test-token",
		http.MethodPost,
		StartTurnRequest{
			Prompt:       "do not submit yet",
			ClientTurnID: "23232323-2323-4323-8323-232323232323",
		},
	)
	if explicit.StatusCode != http.StatusAccepted {
		t.Fatalf("explicit turn = %d: %s", explicit.StatusCode, readBody(t, explicit))
	}
	_ = explicit.Body.Close()
	deadline = time.Now().Add(time.Second)
	for created.submitMessageCalls.Load() != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("explicit draft submits = %d, want 1", created.submitMessageCalls.Load())
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAttachTurnReservationCountsTowardSessionLimit(t *testing.T) {
	catalogDir, root := t.TempDir(), t.TempDir()
	catalog := filepath.Join(catalogDir, "roots.json")
	id := "21212121-2121-4121-8121-212121212121"
	dir := filepath.Join(root, ".yhc", "transcripts")
	writeDurableSession(t, dir, id, root, "saved", "main")
	if err := enginesession.RegisterSessionRoot(catalog, root, dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var factories atomic.Int32
	server, err := New(Config{
		Token:              "test-token",
		MaxSessions:        1,
		SessionCatalogPath: catalog,
		DiscoveryCWD:       catalogDir,
		ValidateResume:     func(context.Context, EngineOptions) error { return nil },
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			factories.Add(1)
			close(entered)
			<-release
			return newFakeSessionEngine(input, false), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	defer shutdownTestServer(t, server)
	attachDone := make(chan bufferedJSONResponse, 1)
	go func() {
		attachDone <- doJSONBuffered(
			httpServer.URL+"/v1/durable-sessions/"+id+"/attach-turn",
			"test-token",
			http.MethodPost,
			AttachTurnRequest{
				Prompt:       "hello",
				ClientTurnID: "22222222-3333-4333-8333-222222222222",
			},
		)
	}()
	<-entered
	create := doJSON(
		t,
		httpServer.URL+"/v1/sessions",
		"test-token",
		http.MethodPost,
		map[string]string{"workspace_handle": registerWorkspace(t, httpServer.URL, "test-token", t.TempDir()).WorkspaceHandle},
	)
	if create.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("create during attach = %d: %s", create.StatusCode, readBody(t, create))
	}
	_ = create.Body.Close()
	close(release)
	attached := <-attachDone
	if attached.err != nil {
		t.Fatal(attached.err)
	}
	if attached.statusCode != http.StatusOK {
		t.Fatalf("attach = %d: %s", attached.statusCode, attached.body)
	}
	if factories.Load() != 1 {
		t.Fatalf("factory calls = %d, want 1", factories.Load())
	}
}

type bufferedJSONResponse struct {
	statusCode int
	body       []byte
	err        error
}

func doJSONBuffered(url, token, method string, body any) bufferedJSONResponse {
	encoded, err := json.Marshal(body)
	if err != nil {
		return bufferedJSONResponse{err: err}
	}
	request, err := http.NewRequest(method, url, bytes.NewReader(encoded))
	if err != nil {
		return bufferedJSONResponse{err: err}
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return bufferedJSONResponse{err: err}
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	return bufferedJSONResponse{
		statusCode: response.StatusCode,
		body:       responseBody,
		err:        err,
	}
}

func assertAttachError(t *testing.T, response *http.Response, status int, code string) {
	t.Helper()
	if response.StatusCode != status {
		t.Fatalf("attach error = %d: %s", response.StatusCode, readBody(t, response))
	}
	var envelope ErrorEnvelope
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Error.Code != code {
		t.Fatalf("attach error = %+v, want %q", envelope, code)
	}
}

func attachTestServer(t *testing.T) (*Server, string, string) {
	t.Helper()
	catalogDir, root := t.TempDir(), t.TempDir()
	catalog := filepath.Join(catalogDir, "roots.json")
	id := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	dir := filepath.Join(root, ".yhc", "transcripts")
	writeDurableSession(t, dir, id, root, "saved", "main")
	if err := enginesession.RegisterSessionRoot(catalog, root, dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{
		Token:              "test-token",
		SessionCatalogPath: catalog,
		DiscoveryCWD:       catalogDir,
		ValidateResume:     func(context.Context, EngineOptions) error { return nil },
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			return newFakeSessionEngine(input, false), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	return server, httpServer.URL, id
}
