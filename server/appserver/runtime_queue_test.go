package appserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/abietic/yhc/engine"
	enginesession "github.com/abietic/yhc/engine/session"
)

type queueSessionEngine struct {
	*fakeSessionEngine

	mu             sync.Mutex
	queued         []engine.QueuedUserInput
	admitted       map[string]engine.UserTurnInput
	revision       uint64
	claimErr       error
	ready          chan struct{}
	directStarted  chan struct{}
	directRelease  chan struct{}
	runtimeStarted chan engine.RuntimeItem
	runtimeRelease chan struct{}
	directOnce     sync.Once
	subscribeOnce  sync.Once
	subscribed     chan struct{}
}

func newQueueSessionEngine(input EngineOptions) *queueSessionEngine {
	return &queueSessionEngine{
		fakeSessionEngine: newFakeSessionEngine(input, false),
		admitted:          make(map[string]engine.UserTurnInput),
		ready:             make(chan struct{}, 1),
		directStarted:     make(chan struct{}),
		directRelease:     make(chan struct{}),
		runtimeStarted:    make(chan engine.RuntimeItem, 1),
		runtimeRelease:    make(chan struct{}),
		subscribed:        make(chan struct{}),
	}
}

func (f *queueSessionEngine) SubmitMessage(
	ctx context.Context,
	_ string,
) (<-chan engine.QueryEvent, engine.Terminal) {
	events := make(chan engine.QueryEvent, 1)
	f.directOnce.Do(func() { close(f.directStarted) })
	go func() {
		defer close(events)
		select {
		case <-f.directRelease:
			events <- engine.QueryEvent{
				Type: engine.EventTerminal,
				TerminalInfo: &engine.Terminal{
					Reason: engine.TerminalCompleted,
				},
			}
		case <-ctx.Done():
			events <- engine.QueryEvent{
				Type: engine.EventTerminal,
				TerminalInfo: &engine.Terminal{
					Reason: engine.TerminalAbortedStreaming,
					Err:    ctx.Err(),
				},
			}
		}
	}()
	return events, engine.Terminal{}
}

func (f *queueSessionEngine) EnqueueUserInputWithID(
	id string,
	input engine.UserTurnInput,
) (engine.QueuedUserInput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if admitted, ok := f.admitted[id]; ok {
		if admitted.Display != input.Display || admitted.Prompt != input.Prompt {
			return engine.QueuedUserInput{}, &engine.RuntimeInputConflictError{ID: id}
		}
		for _, item := range f.queued {
			if item.ID == id {
				return item, nil
			}
		}
		return engine.QueuedUserInput{
			ID: id, Display: admitted.Display, Prompt: admitted.Prompt,
			State: engine.RuntimeItemProcessing,
		}, nil
	}
	for _, item := range f.queued {
		if item.ID != id {
			continue
		}
		if item.Display != input.Display || item.Prompt != input.Prompt {
			return engine.QueuedUserInput{}, &engine.RuntimeInputConflictError{ID: id}
		}
		return item, nil
	}
	f.admitted[id] = input
	item := engine.QueuedUserInput{
		ID:         id,
		Display:    input.Display,
		Prompt:     input.Prompt,
		EnqueuedAt: time.Now().UTC(),
		State:      engine.RuntimeItemPending,
	}
	f.queued = append(f.queued, item)
	f.revision++
	select {
	case f.ready <- struct{}{}:
	default:
	}
	return item, nil
}

func (f *queueSessionEngine) HasQueuedUserInputAdmission(
	id string,
	input engine.UserTurnInput,
) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	admitted, ok := f.admitted[id]
	if !ok {
		return false, nil
	}
	if admitted.Display != input.Display || admitted.Prompt != input.Prompt {
		return false, &engine.RuntimeInputConflictError{ID: id}
	}
	return true, nil
}

func (f *queueSessionEngine) QueuedPromptState() (engine.QueuedPromptState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]engine.QueuedPromptSnapshot, 0, len(f.queued))
	for _, item := range f.queued {
		result = append(result, engine.QueuedPromptSnapshot{
			ID: item.ID, Display: item.Display, EnqueuedAt: item.EnqueuedAt,
			State: item.State,
		})
	}
	return engine.QueuedPromptState{Revision: f.revision, Items: result}, nil
}

func (f *queueSessionEngine) CancelQueuedPrompt(id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for index, item := range f.queued {
		if item.ID != id {
			continue
		}
		f.queued = append(f.queued[:index], f.queued[index+1:]...)
		f.revision++
		return true, nil
	}
	return false, nil
}

func (f *queueSessionEngine) SubscribeRuntimeItems() <-chan struct{} {
	f.subscribeOnce.Do(func() { close(f.subscribed) })
	return f.ready
}

func (f *queueSessionEngine) ClaimNextRuntimeItem() (engine.RuntimeItem, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claimErr != nil {
		return engine.RuntimeItem{}, false, f.claimErr
	}
	if len(f.queued) == 0 {
		return engine.RuntimeItem{}, false, nil
	}
	queued := f.queued[0]
	f.queued = append([]engine.QueuedUserInput(nil), f.queued[1:]...)
	f.revision++
	return engine.RuntimeItem{
		ID:         queued.ID,
		Kind:       engine.RuntimeItemUserPrompt,
		Priority:   engine.RuntimePriorityNext,
		State:      engine.RuntimeItemProcessing,
		EnqueuedAt: queued.EnqueuedAt,
		UserPrompt: &engine.RuntimeUserPrompt{
			Display: queued.Display,
			Prompt:  queued.Prompt,
		},
	}, true, nil
}

func (f *queueSessionEngine) SubmitRuntimeItem(
	ctx context.Context,
	item engine.RuntimeItem,
) (<-chan engine.QueryEvent, engine.Terminal) {
	events := make(chan engine.QueryEvent, 1)
	f.runtimeStarted <- item
	go func() {
		defer close(events)
		select {
		case <-f.runtimeRelease:
			events <- engine.QueryEvent{
				Type: engine.EventTerminal,
				TerminalInfo: &engine.Terminal{
					Reason: engine.TerminalCompleted,
				},
			}
		case <-ctx.Done():
			events <- engine.QueryEvent{
				Type: engine.EventTerminal,
				TerminalInfo: &engine.Terminal{
					Reason: engine.TerminalAbortedStreaming,
					Err:    ctx.Err(),
				},
			}
		}
	}()
	return events, engine.Terminal{}
}

func TestResumedSessionDefersRuntimePumpUntilExplicitAttachTurn(t *testing.T) {
	const (
		sessionID = "44444444-4444-4444-8444-444444444444"
		turnID    = "55555555-5555-4555-8555-555555555555"
		queueID   = "66666666-6666-4666-8666-666666666666"
	)
	cwd := t.TempDir()
	transcriptDir := t.TempDir()
	var created *queueSessionEngine
	owned, err := newSession(
		context.Background(),
		func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			created = newQueueSessionEngine(input)
			created.queued = []engine.QueuedUserInput{{
				ID: queueID, Display: "recovered queue", Prompt: "recovered queue",
				EnqueuedAt: time.Now().UTC(), State: engine.RuntimeItemPending,
			}}
			created.revision = 1
			created.ready <- struct{}{}
			return created, nil
		},
		"test-server",
		CreateSessionRequest{SessionID: sessionID, CWD: cwd, Resume: true},
		&enginesession.SessionInfo{
			SessionID: sessionID, CWD: cwd, TranscriptDir: transcriptDir,
		},
		128,
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := owned.close(closeCtx); err != nil {
			t.Errorf("close resumed session: %v", err)
		}
	})

	select {
	case <-created.subscribed:
		t.Fatal("resumed session subscribed runtime input before attach admission")
	default:
	}
	select {
	case item := <-created.runtimeStarted:
		t.Fatalf("recovered queue started before attach admission: %#v", item)
	default:
	}

	started, err := owned.startTurn(StartTurnRequest{
		Prompt: "explicit attach prompt", ClientTurnID: turnID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if started.TurnID != turnID || owned.summary().ActiveTurnID != turnID {
		t.Fatalf("explicit attach admission = %#v summary=%#v", started, owned.summary())
	}
	owned.startRuntimeInputPump()
	select {
	case <-created.subscribed:
	case <-time.After(time.Second):
		t.Fatal("runtime input pump did not start after attach admission")
	}
	select {
	case <-created.directStarted:
	case <-time.After(time.Second):
		t.Fatal("explicit attach prompt did not start")
	}
	select {
	case item := <-created.runtimeStarted:
		t.Fatalf("recovered queue started before explicit attach terminal: %#v", item)
	default:
	}

	close(created.directRelease)
	select {
	case item := <-created.runtimeStarted:
		if item.ID != queueID || item.Kind != engine.RuntimeItemUserPrompt {
			t.Fatalf("recovered runtime item = %#v", item)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("recovered queue did not start after explicit attach terminal")
	}
	close(created.runtimeRelease)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if current := owned.summary(); current.ActiveTurnID == "" && current.Status == "idle" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("recovered queue did not settle: %#v", owned.summary())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRuntimeQueueIsDurableOwnedAndServerAutoDrainsAfterTerminal(t *testing.T) {
	var created *queueSessionEngine
	server, err := New(Config{
		Token: "test-token",
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			created = newQueueSessionEngine(input)
			return created, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	t.Cleanup(func() { shutdownTestServer(t, server) })

	workspace := registerWorkspace(t, httpServer.URL, server.Token(), t.TempDir())
	createResponse := doJSON(t, httpServer.URL+"/v1/sessions", server.Token(), http.MethodPost, map[string]string{
		"workspace_handle": workspace.WorkspaceHandle,
	})
	if createResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create session = %d: %s", createResponse.StatusCode, readBody(t, createResponse))
	}
	var summary SessionSummary
	decodeResponse(t, createResponse, &summary)

	firstTurnID := "11111111-1111-4111-8111-111111111111"
	turnResponse := doJSON(t, httpServer.URL+"/v1/sessions/"+summary.ID+"/turns", server.Token(), http.MethodPost, StartTurnRequest{
		Prompt: "first turn", ClientTurnID: firstTurnID,
	})
	if turnResponse.StatusCode != http.StatusAccepted {
		t.Fatalf("start turn = %d: %s", turnResponse.StatusCode, readBody(t, turnResponse))
	}
	_ = turnResponse.Body.Close()
	select {
	case <-created.directStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first turn did not start")
	}

	queueID := "22222222-2222-4222-8222-222222222222"
	queueURL := httpServer.URL + "/v1/sessions/" + summary.ID + "/queued-prompts"
	queuedResponse := doJSON(t, queueURL, server.Token(), http.MethodPost, QueuePromptRequest{
		Prompt: "run after the first turn", ClientQueueID: queueID,
	})
	if queuedResponse.StatusCode != http.StatusAccepted {
		t.Fatalf("enqueue = %d: %s", queuedResponse.StatusCode, readBody(t, queuedResponse))
	}
	var queued QueuePromptResponse
	decodeResponse(t, queuedResponse, &queued)
	if queued.AcceptedID != queueID || queued.Revision == 0 ||
		len(queued.Items) != 1 || queued.Items[0].ID != queueID {
		t.Fatalf("enqueue response = %#v", queued)
	}

	repeatedResponse := doJSON(t, queueURL, server.Token(), http.MethodPost, QueuePromptRequest{
		Prompt: "run after the first turn", ClientQueueID: queueID,
	})
	if repeatedResponse.StatusCode != http.StatusAccepted {
		t.Fatalf("idempotent enqueue = %d: %s", repeatedResponse.StatusCode, readBody(t, repeatedResponse))
	}
	var repeated QueuePromptResponse
	decodeResponse(t, repeatedResponse, &repeated)
	if repeated.AcceptedID != queueID || repeated.Revision != queued.Revision || len(repeated.Items) != 1 {
		t.Fatalf("idempotent response = %#v", repeated)
	}

	conflictResponse := doJSON(t, queueURL, server.Token(), http.MethodPost, QueuePromptRequest{
		Prompt: "different prompt", ClientQueueID: queueID,
	})
	if conflictResponse.StatusCode != http.StatusConflict {
		t.Fatalf("conflicting enqueue = %d: %s", conflictResponse.StatusCode, readBody(t, conflictResponse))
	}
	_ = conflictResponse.Body.Close()

	cancelID := "33333333-3333-4333-8333-333333333333"
	secondResponse := doJSON(t, queueURL, server.Token(), http.MethodPost, QueuePromptRequest{
		Prompt: "cancel this queued prompt", ClientQueueID: cancelID,
	})
	if secondResponse.StatusCode != http.StatusAccepted {
		t.Fatalf("enqueue second = %d: %s", secondResponse.StatusCode, readBody(t, secondResponse))
	}
	_ = secondResponse.Body.Close()
	cancelResponse := doJSON(t, queueURL+"/"+cancelID, server.Token(), http.MethodDelete, nil)
	if cancelResponse.StatusCode != http.StatusOK {
		t.Fatalf("cancel pending = %d: %s", cancelResponse.StatusCode, readBody(t, cancelResponse))
	}
	var afterCancel QueuedPromptsResponse
	decodeResponse(t, cancelResponse, &afterCancel)
	if afterCancel.Revision <= queued.Revision || len(afterCancel.Items) != 1 || afterCancel.Items[0].ID != queueID {
		t.Fatalf("queue after cancel = %#v", afterCancel)
	}

	listResponse := getBearer(t, queueURL, server.Token())
	if listResponse.StatusCode != http.StatusOK {
		t.Fatalf("list queue = %d: %s", listResponse.StatusCode, readBody(t, listResponse))
	}
	var listed QueuedPromptsResponse
	decodeResponse(t, listResponse, &listed)
	if listed.Revision != afterCancel.Revision || len(listed.Items) != 1 || listed.Items[0].ID != queueID {
		t.Fatalf("listed queue = %#v", listed)
	}

	close(created.directRelease)
	var runtimeItem engine.RuntimeItem
	select {
	case runtimeItem = <-created.runtimeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("queued prompt was not started after the first terminal")
	}
	if runtimeItem.ID != queueID || runtimeItem.Kind != engine.RuntimeItemUserPrompt {
		t.Fatalf("claimed runtime item = %#v", runtimeItem)
	}

	processingCancel := doJSON(t, queueURL+"/"+queueID, server.Token(), http.MethodDelete, nil)
	if processingCancel.StatusCode != http.StatusConflict {
		t.Fatalf("cancel processing = %d: %s", processingCancel.StatusCode, readBody(t, processingCancel))
	}
	_ = processingCancel.Body.Close()

	snapshotResponse := getBearer(t, httpServer.URL+"/v1/sessions/"+summary.ID+"/snapshot", server.Token())
	if snapshotResponse.StatusCode != http.StatusOK {
		t.Fatalf("snapshot = %d: %s", snapshotResponse.StatusCode, readBody(t, snapshotResponse))
	}
	var snapshot SessionSnapshot
	decodeResponse(t, snapshotResponse, &snapshot)
	if snapshot.QueuedPromptsRevision <= listed.Revision || len(snapshot.QueuedPrompts) != 0 || snapshot.Session.ActiveTurnID == "" ||
		snapshot.Session.ActiveTurnID == firstTurnID {
		t.Fatalf("runtime queue snapshot = %#v", snapshot)
	}

	runtimeTurnID := snapshot.Session.ActiveTurnID
	owned, ok := server.getSession(summary.ID)
	if !ok {
		t.Fatal("session disappeared")
	}
	_, updates, stopWaiting, _, err := owned.events.subscribe(owned.events.latestCursor())
	if err != nil {
		t.Fatal(err)
	}
	close(created.runtimeRelease)
	settled := false
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for !settled {
		select {
		case event, open := <-updates:
			if !open {
				t.Fatal("event stream closed before queued turn settled")
			}
			settled = event.Type == "turn.finished" && event.TurnID == runtimeTurnID
		case <-timer.C:
			t.Fatalf("queued turn did not settle: %#v", owned.summary())
		}
	}
	stopWaiting()
	if current := owned.summary(); current.ActiveTurnID != "" || current.Status != "idle" {
		t.Fatalf("settled queued turn = %#v", current)
	}

	historicalRetry := doJSON(t, queueURL, server.Token(), http.MethodPost, QueuePromptRequest{
		Prompt: "run after the first turn", ClientQueueID: queueID,
	})
	if historicalRetry.StatusCode != http.StatusAccepted {
		t.Fatalf("historical retry = %d: %s", historicalRetry.StatusCode, readBody(t, historicalRetry))
	}
	var historical QueuePromptResponse
	decodeResponse(t, historicalRetry, &historical)
	if historical.AcceptedID != queueID || historical.Pending ||
		historical.Revision != snapshot.QueuedPromptsRevision || len(historical.Items) != 0 {
		t.Fatalf("historical retry response = %#v", historical)
	}
	conflictingHistorical := doJSON(t, queueURL, server.Token(), http.MethodPost, QueuePromptRequest{
		Prompt: "different historical payload", ClientQueueID: queueID,
	})
	if conflictingHistorical.StatusCode != http.StatusConflict {
		t.Fatalf("historical conflict = %d: %s", conflictingHistorical.StatusCode, readBody(t, conflictingHistorical))
	}
	_ = conflictingHistorical.Body.Close()
	unknownIdle := doJSON(t, queueURL, server.Token(), http.MethodPost, QueuePromptRequest{
		Prompt: "new idle work", ClientQueueID: "99999999-9999-4999-8999-999999999999",
	})
	if unknownIdle.StatusCode != http.StatusConflict {
		t.Fatalf("unknown idle queue = %d: %s", unknownIdle.StatusCode, readBody(t, unknownIdle))
	}
	var unknownEnvelope ErrorEnvelope
	decodeResponse(t, unknownIdle, &unknownEnvelope)
	if unknownEnvelope.Error.Code != "queue_requires_active_turn" {
		t.Fatalf("unknown idle queue error = %#v", unknownEnvelope)
	}
	select {
	case duplicate := <-created.runtimeStarted:
		t.Fatalf("historical retry created duplicate work: %#v", duplicate)
	default:
	}

	replay, _, unsubscribe, _, err := owned.events.subscribe(0)
	if err != nil {
		t.Fatal(err)
	}
	unsubscribe()
	positions := map[string]int{
		"first_finished":  -1,
		"queue_updated":   -1,
		"queued_user":     -1,
		"queued_accepted": -1,
	}
	for index, event := range replay {
		if event.Type == "turn.finished" && event.TurnID == firstTurnID {
			positions["first_finished"] = index
		}
		if event.Type == "user_message" && event.TurnID == runtimeTurnID {
			positions["queued_user"] = index
		}
		if event.Type == "turn.accepted" && event.TurnID == runtimeTurnID {
			positions["queued_accepted"] = index
		}
	}
	for index, event := range replay {
		if event.Type == "queue.updated" &&
			index > positions["first_finished"] &&
			index < positions["queued_user"] {
			positions["queue_updated"] = index
		}
	}
	if positions["first_finished"] < 0 || positions["queue_updated"] < 0 ||
		positions["queued_user"] < 0 || positions["queued_accepted"] < 0 ||
		positions["first_finished"] >= positions["queued_user"] ||
		positions["queue_updated"] >= positions["queued_user"] ||
		positions["queued_user"] >= positions["queued_accepted"] {
		t.Fatalf("automatic queue event order = %#v replay=%#v", positions, replay)
	}
}

func TestRuntimeQueueRejectsInvalidIdentityAndUnavailableController(t *testing.T) {
	server, err := New(Config{
		Token: "test-token",
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			return newFakeSessionEngine(input, false), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	t.Cleanup(func() { shutdownTestServer(t, server) })
	workspace := registerWorkspace(t, httpServer.URL, server.Token(), t.TempDir())
	createResponse := doJSON(t, httpServer.URL+"/v1/sessions", server.Token(), http.MethodPost, map[string]string{
		"workspace_handle": workspace.WorkspaceHandle,
	})
	if createResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create session = %d: %s", createResponse.StatusCode, readBody(t, createResponse))
	}
	var summary SessionSummary
	decodeResponse(t, createResponse, &summary)
	queueURL := httpServer.URL + "/v1/sessions/" + summary.ID + "/queued-prompts"

	invalid := doJSON(t, queueURL, server.Token(), http.MethodPost, QueuePromptRequest{
		Prompt: "queued", ClientQueueID: "not-a-uuid",
	})
	if invalid.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid queue identity = %d: %s", invalid.StatusCode, readBody(t, invalid))
	}
	_ = invalid.Body.Close()
	nonCanonical := doJSON(t, queueURL, server.Token(), http.MethodPost, QueuePromptRequest{
		Prompt: "queued", ClientQueueID: "urn:uuid:44444444-4444-4444-8444-444444444444",
	})
	if nonCanonical.StatusCode != http.StatusBadRequest {
		t.Fatalf("non-canonical queue identity = %d: %s", nonCanonical.StatusCode, readBody(t, nonCanonical))
	}
	_ = nonCanonical.Body.Close()

	unavailable := doJSON(t, queueURL, server.Token(), http.MethodPost, QueuePromptRequest{
		Prompt: "queued", ClientQueueID: "44444444-4444-4444-8444-444444444444",
	})
	if unavailable.StatusCode != http.StatusNotImplemented {
		t.Fatalf("unavailable queue = %d: %s", unavailable.StatusCode, readBody(t, unavailable))
	}
	var envelope ErrorEnvelope
	decodeResponse(t, unavailable, &envelope)
	if envelope.Error.Code != "runtime_queue_unavailable" {
		t.Fatalf("unavailable error = %#v", envelope)
	}
}

func TestRuntimeQueueRequiresActiveTurnAndRejectsClosedMutation(t *testing.T) {
	var created *queueSessionEngine
	server, err := New(Config{
		Token: "test-token",
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			created = newQueueSessionEngine(input)
			return created, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	t.Cleanup(func() { shutdownTestServer(t, server) })
	workspace := registerWorkspace(t, httpServer.URL, server.Token(), t.TempDir())
	createResponse := doJSON(t, httpServer.URL+"/v1/sessions", server.Token(), http.MethodPost, map[string]string{
		"workspace_handle": workspace.WorkspaceHandle,
	})
	if createResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create session = %d: %s", createResponse.StatusCode, readBody(t, createResponse))
	}
	var summary SessionSummary
	decodeResponse(t, createResponse, &summary)
	queueURL := httpServer.URL + "/v1/sessions/" + summary.ID + "/queued-prompts"
	queueID := "55555555-5555-4555-8555-555555555555"

	idle := doJSON(t, queueURL, server.Token(), http.MethodPost, QueuePromptRequest{
		Prompt: "must not start through the queue route", ClientQueueID: queueID,
	})
	if idle.StatusCode != http.StatusConflict {
		t.Fatalf("idle queue admission = %d: %s", idle.StatusCode, readBody(t, idle))
	}
	var idleEnvelope ErrorEnvelope
	decodeResponse(t, idle, &idleEnvelope)
	if idleEnvelope.Error.Code != "queue_requires_active_turn" {
		t.Fatalf("idle queue error = %#v", idleEnvelope)
	}
	state, err := created.QueuedPromptState()
	if err != nil || state.Revision != 0 || len(state.Items) != 0 {
		t.Fatalf("idle rejection mutated queue: state=%#v err=%v", state, err)
	}

	owned, ok := server.getSession(summary.ID)
	if !ok {
		t.Fatal("session disappeared")
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := owned.close(closeCtx); err != nil {
		t.Fatal(err)
	}
	if _, err := owned.enqueuePrompt(QueuePromptRequest{
		Prompt: "closed enqueue", ClientQueueID: queueID,
	}); !errors.Is(err, errSessionClosed) {
		t.Fatalf("closed enqueue error = %v", err)
	}
	if _, err := owned.cancelQueuedPrompt(queueID); !errors.Is(err, errSessionClosed) {
		t.Fatalf("closed cancel error = %v", err)
	}
	state, err = created.QueuedPromptState()
	if err != nil || state.Revision != 0 || len(state.Items) != 0 {
		t.Fatalf("closed mutation changed queue: state=%#v err=%v", state, err)
	}
}

func TestRuntimeQueueClaimFailureProjectsBlockedAndExactRecovery(t *testing.T) {
	var created *queueSessionEngine
	server, err := New(Config{
		Token: "test-token",
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			created = newQueueSessionEngine(input)
			return created, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	t.Cleanup(func() { shutdownTestServer(t, server) })
	workspace := registerWorkspace(t, httpServer.URL, server.Token(), t.TempDir())
	createResponse := doJSON(t, httpServer.URL+"/v1/sessions", server.Token(), http.MethodPost, map[string]string{
		"workspace_handle": workspace.WorkspaceHandle,
	})
	if createResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create session = %d: %s", createResponse.StatusCode, readBody(t, createResponse))
	}
	var summary SessionSummary
	decodeResponse(t, createResponse, &summary)
	turnResponse := doJSON(t, httpServer.URL+"/v1/sessions/"+summary.ID+"/turns", server.Token(), http.MethodPost, StartTurnRequest{
		Prompt: "first turn", ClientTurnID: "77777777-7777-4777-8777-777777777777",
	})
	if turnResponse.StatusCode != http.StatusAccepted {
		t.Fatalf("start turn = %d: %s", turnResponse.StatusCode, readBody(t, turnResponse))
	}
	_ = turnResponse.Body.Close()
	select {
	case <-created.directStarted:
	case <-time.After(time.Second):
		t.Fatal("first turn did not start")
	}
	queueURL := httpServer.URL + "/v1/sessions/" + summary.ID + "/queued-prompts"
	queueID := "88888888-8888-4888-8888-888888888888"
	queued := doJSON(t, queueURL, server.Token(), http.MethodPost, QueuePromptRequest{
		Prompt: "blocked queue", ClientQueueID: queueID,
	})
	if queued.StatusCode != http.StatusAccepted {
		t.Fatalf("enqueue = %d: %s", queued.StatusCode, readBody(t, queued))
	}
	_ = queued.Body.Close()

	owned, ok := server.getSession(summary.ID)
	if !ok {
		t.Fatal("session disappeared")
	}
	_, updates, unsubscribe, _, err := owned.events.subscribe(owned.events.latestCursor())
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	created.mu.Lock()
	created.claimErr = errors.New("fixture claim failure")
	created.mu.Unlock()
	close(created.directRelease)
	waitForQueueEvent := func(want string) {
		t.Helper()
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		for {
			select {
			case event, open := <-updates:
				if !open {
					t.Fatalf("event stream closed before %s", want)
				}
				if event.Type == want {
					return
				}
			case <-timer.C:
				t.Fatalf("missing %s event: %#v", want, owned.summary())
			}
		}
	}
	waitForQueueEvent("queue.rewake_blocked")
	blocked := owned.summary()
	if blocked.Status != "error" || blocked.LastError != runtimeQueueBlockedMessage || blocked.ActiveTurnID != "" {
		t.Fatalf("blocked queue summary = %#v", blocked)
	}

	created.mu.Lock()
	created.claimErr = nil
	created.mu.Unlock()
	cancelled := doJSON(t, queueURL+"/"+queueID, server.Token(), http.MethodDelete, nil)
	if cancelled.StatusCode != http.StatusOK {
		t.Fatalf("cancel blocked queue = %d: %s", cancelled.StatusCode, readBody(t, cancelled))
	}
	_ = cancelled.Body.Close()
	waitForQueueEvent("queue.rewake_ready")
	recovered := owned.summary()
	if recovered.Status != "idle" || recovered.LastError != "" || recovered.ActiveTurnID != "" {
		t.Fatalf("recovered queue summary = %#v", recovered)
	}
}
