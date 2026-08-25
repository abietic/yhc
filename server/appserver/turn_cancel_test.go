package appserver

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abietic/yhc/engine"
)

type observedStop struct {
	mode   engine.RuntimeStopMode
	reason string
}

type cancelObservingEngine struct {
	*fakeSessionEngine
	stopOnce          sync.Once
	terminalOnce      sync.Once
	stopped           chan observedStop
	terminalPublished chan struct{}
	events            chan engine.QueryEvent
}

type rejectingStopEngine struct {
	*fakeSessionEngine
}

func (f *rejectingStopEngine) RequestStop(engine.RuntimeStopMode, string) error {
	return context.DeadlineExceeded
}

func newCancelObservingEngine(input EngineOptions) *cancelObservingEngine {
	return &cancelObservingEngine{
		fakeSessionEngine: newFakeSessionEngine(input, true),
		stopped:           make(chan observedStop, 1),
		terminalPublished: make(chan struct{}),
	}
}

func (f *cancelObservingEngine) SubmitMessage(
	ctx context.Context,
	_ string,
) (<-chan engine.QueryEvent, engine.Terminal) {
	f.events = make(chan engine.QueryEvent)
	f.closeOnce.Do(func() { close(f.started) })
	go func() {
		<-ctx.Done()
		f.publishCancellationTerminal(ctx.Err())
	}()
	return f.events, engine.Terminal{}
}

func (f *cancelObservingEngine) publishCancellationTerminal(err error) {
	f.terminalOnce.Do(func() {
		f.events <- engine.QueryEvent{
			Type: engine.EventTerminal,
			TerminalInfo: &engine.Terminal{
				Reason: engine.TerminalAbortedStreaming,
				Err:    err,
			},
		}
		close(f.events)
	})
}

func (f *cancelObservingEngine) RequestStop(mode engine.RuntimeStopMode, reason string) error {
	f.stopOnce.Do(func() {
		f.stopped <- observedStop{mode: mode, reason: reason}
	})
	f.publishCancellationTerminal(context.Canceled)
	select {
	case <-f.terminalPublished:
		return nil
	case <-time.After(2 * time.Second):
		return context.DeadlineExceeded
	}
}

func TestTerminalErrorTextSuppressesOnlyAbortedContextCancellation(t *testing.T) {
	tests := []struct {
		name                 string
		reason               engine.TerminalReason
		err                  error
		ownedImmediateCancel bool
		want                 string
	}{
		{name: "no error", reason: engine.TerminalCompleted},
		{name: "owned user cancellation", reason: engine.TerminalAbortedStreaming, err: context.Canceled, ownedImmediateCancel: true},
		{name: "unowned cancellation remains an error", reason: engine.TerminalAbortedStreaming, err: context.Canceled, want: context.Canceled.Error()},
		{name: "deadline remains an error", reason: engine.TerminalAbortedStreaming, err: context.DeadlineExceeded, ownedImmediateCancel: true, want: context.DeadlineExceeded.Error()},
		{name: "other terminal remains an error", reason: engine.TerminalModelError, err: context.Canceled, ownedImmediateCancel: true, want: context.Canceled.Error()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := terminalErrorText(test.reason, test.err, test.ownedImmediateCancel); got != test.want {
				t.Fatalf("terminalErrorText(%q, %v, %t) = %q, want %q", test.reason, test.err, test.ownedImmediateCancel, got, test.want)
			}
		})
	}
}

func TestUnownedAbortedContextCancellationRemainsError(t *testing.T) {
	const turnID = "11111111-1111-4111-8111-111111111111"
	session := &session{
		id:           "session-1",
		threadID:     "thread-1",
		status:       "running",
		activeTurnID: turnID,
		events:       newEventLog(8),
		activity:     newActivityLog(),
	}
	terminal := engine.QueryEvent{
		Type: engine.EventTerminal,
		TerminalInfo: &engine.Terminal{
			Reason: engine.TerminalAbortedStreaming,
			Err:    context.Canceled,
		},
	}
	session.publishEngine(terminal, turnID)
	session.finishTurn(turnID, terminal.TerminalInfo.Reason, terminal.TerminalInfo.Err)

	summary := session.summary()
	if summary.Status != "error" || summary.LastError != context.Canceled.Error() {
		t.Fatalf("unowned cancellation summary = %+v", summary)
	}
	replay, _, unsubscribe, _, err := session.events.subscribe(0)
	if err != nil {
		t.Fatal(err)
	}
	unsubscribe()
	for _, eventType := range []string{"terminal", "turn.finished"} {
		var found bool
		for _, event := range replay {
			if event.Type != eventType {
				continue
			}
			found = true
			var data struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(event.Data, &data); err != nil {
				t.Fatalf("decode %s: %v", eventType, err)
			}
			if data.Error != context.Canceled.Error() {
				t.Fatalf("%s error = %q, want %q", eventType, data.Error, context.Canceled.Error())
			}
		}
		if !found {
			t.Fatalf("missing %s in %#v", eventType, replay)
		}
	}
}

func TestRejectedImmediateCancelReleasesTerminalOwnership(t *testing.T) {
	const turnID = "11111111-1111-4111-8111-111111111111"
	cancelled := false
	session := &session{
		engine: &rejectingStopEngine{
			fakeSessionEngine: newFakeSessionEngine(EngineOptions{}, true),
		},
		activeTurnID: turnID,
		activeCancel: func() {
			cancelled = true
		},
	}
	err := session.cancelTurn(CancelTurnRequest{
		TurnID: turnID,
		Mode:   string(engine.RuntimeStopImmediate),
	})
	if err != context.DeadlineExceeded {
		t.Fatalf("cancelTurn error = %v, want %v", err, context.DeadlineExceeded)
	}
	if session.ownsImmediateCancel(turnID) {
		t.Fatal("rejected stop retained terminal ownership")
	}
	if cancelled {
		t.Fatal("rejected stop cancelled the local turn context")
	}
}

func TestStartTurnStreamsAndImmediateCancelReturnsIdle(t *testing.T) {
	var created *cancelObservingEngine
	server, err := New(Config{
		Token: strings.Join([]string{"test", "token"}, "-"),
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			created = newCancelObservingEngine(input)
			return created, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	t.Cleanup(func() { shutdownTestServer(t, server) })

	registered := registerWorkspace(t, httpServer.URL, server.Token(), t.TempDir())
	createResponse := doJSON(
		t,
		httpServer.URL+"/v1/sessions",
		server.Token(),
		http.MethodPost,
		map[string]string{"workspace_handle": registered.WorkspaceHandle},
	)
	if createResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create session = %d: %s", createResponse.StatusCode, readBody(t, createResponse))
	}
	var summary SessionSummary
	decodeResponse(t, createResponse, &summary)
	owned, ok := server.getSession(summary.ID)
	if !ok {
		t.Fatal("created session was not owned")
	}

	eventsContext, cancelEvents := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelEvents()
	eventsRequest, err := http.NewRequestWithContext(
		eventsContext,
		http.MethodGet,
		httpServer.URL+"/v1/sessions/"+summary.ID+"/events?after="+
			strconv.FormatUint(owned.events.latestCursor(), 10),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	eventsRequest.Header.Set("Authorization", "Bearer "+server.Token())
	eventsResponse, err := http.DefaultClient.Do(eventsRequest)
	if err != nil {
		t.Fatalf("open event stream: %v", err)
	}
	defer eventsResponse.Body.Close()
	if eventsResponse.StatusCode != http.StatusOK {
		t.Fatalf("event stream = %d: %s", eventsResponse.StatusCode, readBody(t, eventsResponse))
	}
	scanner := bufio.NewScanner(eventsResponse.Body)

	const turnID = "11111111-1111-4111-8111-111111111111"
	turnResponse := doJSON(
		t,
		httpServer.URL+"/v1/sessions/"+summary.ID+"/turns",
		server.Token(),
		http.MethodPost,
		StartTurnRequest{Prompt: "cancel me", ClientTurnID: turnID},
	)
	if turnResponse.StatusCode != http.StatusAccepted {
		t.Fatalf("start turn = %d: %s", turnResponse.StatusCode, readBody(t, turnResponse))
	}
	var started StartTurnResponse
	decodeResponse(t, turnResponse, &started)
	if !started.Accepted || started.SessionID != summary.ID || started.TurnID != turnID {
		t.Fatalf("start turn response = %+v", started)
	}
	select {
	case <-created.started:
	case <-eventsContext.Done():
		t.Fatal("fake engine did not start")
	}

	observed := []WireEvent{
		nextNonActivitySSE(t, scanner),
		nextNonActivitySSE(t, scanner),
	}
	if observed[0].Type != "user_message" || observed[1].Type != "turn.accepted" {
		t.Fatalf("turn admission events = %#v", observed)
	}

	const stopReason = "test requested cancellation"
	cancelBody, err := json.Marshal(CancelTurnRequest{
		TurnID: turnID,
		Mode:   string(engine.RuntimeStopImmediate),
		Reason: stopReason,
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelRequest, err := http.NewRequestWithContext(
		eventsContext,
		http.MethodPost,
		httpServer.URL+"/v1/sessions/"+summary.ID+"/cancel",
		strings.NewReader(string(cancelBody)),
	)
	if err != nil {
		t.Fatal(err)
	}
	cancelRequest.Header.Set("Authorization", "Bearer "+server.Token())
	cancelRequest.Header.Set("Content-Type", "application/json")
	type cancelResult struct {
		statusCode int
		body       string
		err        error
	}
	cancelResults := make(chan cancelResult, 1)
	go func() {
		response, requestErr := http.DefaultClient.Do(cancelRequest)
		if requestErr != nil {
			cancelResults <- cancelResult{err: requestErr}
			return
		}
		defer response.Body.Close()
		body, readErr := io.ReadAll(response.Body)
		cancelResults <- cancelResult{
			statusCode: response.StatusCode,
			body:       string(body),
			err:        readErr,
		}
	}()

	acknowledgedTerminal := false
	for len(observed) < 5 {
		event := nextNonActivitySSE(t, scanner)
		observed = append(observed, event)
		if event.Type == "terminal" && !acknowledgedTerminal {
			close(created.terminalPublished)
			acknowledgedTerminal = true
		}
	}
	if !acknowledgedTerminal {
		t.Fatal("terminal was not published before the stop request returned")
	}
	var cancelResponse cancelResult
	select {
	case result := <-cancelResults:
		if result.err != nil {
			t.Fatalf("cancel turn: %v", result.err)
		}
		cancelResponse = result
	case <-eventsContext.Done():
		t.Fatal("cancel response remained blocked after terminal publication")
	}
	if cancelResponse.statusCode != http.StatusNoContent {
		t.Fatalf("cancel turn = %d: %s", cancelResponse.statusCode, cancelResponse.body)
	}
	select {
	case stop := <-created.stopped:
		if stop.mode != engine.RuntimeStopImmediate || stop.reason != stopReason {
			t.Fatalf("engine stop = %+v", stop)
		}
	case <-eventsContext.Done():
		t.Fatal("engine did not observe immediate stop")
	}
	byType := make(map[string]WireEvent, len(observed))
	positions := make(map[string]int, len(observed))
	for index, event := range observed {
		if event.ProtocolVersion != ProtocolVersion || event.SessionID != summary.ID || event.TurnID != turnID {
			t.Fatalf("event identity = %#v", event)
		}
		if _, duplicate := byType[event.Type]; duplicate {
			t.Fatalf("duplicate event type %q in %#v", event.Type, observed)
		}
		byType[event.Type] = event
		positions[event.Type] = index
	}
	for _, eventType := range []string{
		"user_message", "turn.accepted", "turn.cancel.requested", "terminal", "turn.finished",
	} {
		if _, ok := byType[eventType]; !ok {
			t.Fatalf("missing %q in %#v", eventType, observed)
		}
	}
	if positions["user_message"] >= positions["turn.accepted"] ||
		positions["turn.accepted"] >= positions["turn.cancel.requested"] ||
		positions["terminal"] >= positions["turn.finished"] {
		t.Fatalf("invalid turn event order = %#v", observed)
	}

	var cancelData struct {
		Mode   engine.RuntimeStopMode `json:"mode"`
		Reason string                 `json:"reason"`
	}
	if err := json.Unmarshal(byType["turn.cancel.requested"].Data, &cancelData); err != nil {
		t.Fatalf("decode cancellation event: %v", err)
	}
	if cancelData.Mode != engine.RuntimeStopImmediate || cancelData.Reason != stopReason {
		t.Fatalf("cancellation event = %+v", cancelData)
	}
	for _, eventType := range []string{"terminal", "turn.finished"} {
		var terminalData struct {
			Reason engine.TerminalReason `json:"reason"`
			Error  string                `json:"error"`
		}
		if err := json.Unmarshal(byType[eventType].Data, &terminalData); err != nil {
			t.Fatalf("decode %s: %v", eventType, err)
		}
		if terminalData.Reason != engine.TerminalAbortedStreaming || terminalData.Error != "" {
			t.Fatalf("%s = %+v", eventType, terminalData)
		}
	}

	snapshotResponse := getBearer(
		t,
		httpServer.URL+"/v1/sessions/"+summary.ID+"/snapshot",
		server.Token(),
	)
	if snapshotResponse.StatusCode != http.StatusOK {
		t.Fatalf("snapshot = %d: %s", snapshotResponse.StatusCode, readBody(t, snapshotResponse))
	}
	var snapshot SessionSnapshot
	decodeResponse(t, snapshotResponse, &snapshot)
	if snapshot.Session.ActiveTurnID != "" || snapshot.Session.Status != "idle" || snapshot.Session.LastError != "" {
		t.Fatalf("cancelled snapshot = %+v", snapshot.Session)
	}
}

func nextNonActivitySSE(t *testing.T, scanner *bufio.Scanner) WireEvent {
	t.Helper()
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event WireEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatalf("decode SSE event: %v", err)
		}
		if event.Type != "activity" {
			return event
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan SSE event: %v", err)
	}
	t.Fatal("event stream closed before the cancellation terminal")
	return WireEvent{}
}
