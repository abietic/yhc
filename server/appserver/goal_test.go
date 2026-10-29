package appserver

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/commands"
)

type goalTestEngine struct {
	*engine.QueryEngine
	prompts chan string
}

type desktopGoalModel struct {
	calls   atomic.Int32
	inputs  chan string
	release chan struct{}
}

func (m *desktopGoalModel) Generate(ctx context.Context, messages []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	call := m.calls.Add(1)
	last := ""
	for _, message := range messages {
		if message.Role == schema.User {
			last = message.Content
		}
	}
	m.inputs <- last
	if call == 1 {
		select {
		case <-m.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if call > 3 {
		return nil, errors.New("unexpected fixture call")
	}
	return &schema.Message{Role: schema.Assistant, Content: "bounded fixture step", ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 6, CompletionTokens: 4, TotalTokens: 10}}}, nil
}

func (m *desktopGoalModel) Stream(ctx context.Context, messages []*schema.Message, options ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, messages, options...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func TestDesktopGoalProductionPumpPrioritizesUserAndStopsAtBudget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := &desktopGoalModel{inputs: make(chan string, 8), release: make(chan struct{})}
	var e *engine.QueryEngine
	s, err := New(Config{Token: "test-token", SessionCatalogPath: filepath.Join(t.TempDir(), "catalog.json"), Factory: func(_ context.Context, options EngineOptions) (SessionEngine, error) {
		e = engine.NewQueryEngine(engine.QueryEngineConfig{CWD: options.CWD, TranscriptDir: options.TranscriptDir, SessionID: options.SessionID, ThreadID: options.ThreadID, CommandEntrypoint: commands.EntrypointAppServer, GoalCapability: &engine.GoalCapabilityConfig{Enabled: true}, ChatModel: m})
		return e, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(hs.Close)
	t.Cleanup(func() { shutdownTestServer(t, s) })
	r := createExecutionSettingsTestSession(t, hs.URL)
	defer r.Body.Close()
	var summary SessionSummary
	decodeResponse(t, r, &summary)
	endpoint := hs.URL + "/v1/sessions/" + summary.ID
	owned, _ := s.getSession(summary.ID)
	_, wake, unsubscribe, _, err := owned.events.subscribe(0)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	r = doJSON(t, endpoint+"/goal/control", "test-token", http.MethodPost, map[string]any{"operation": "create", "expected_goal_id": "", "expected_revision": "0", "objective": "perform three bounded steps", "token_budget": "30"})
	if r.StatusCode != 200 {
		t.Fatalf("create=%d %s", r.StatusCode, readBody(t, r))
	}
	r.Body.Close()
	select {
	case first := <-m.inputs:
		if first != engine.GoalInitialPrompt {
			t.Fatalf("first=%s", first)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("initial model call missing")
	}
	r = doJSON(t, endpoint+"/queued-prompts", "test-token", http.MethodPost, QueuePromptRequest{Prompt: "explicit user priority", ClientQueueID: "11111111-1111-4111-8111-111111111111"})
	if r.StatusCode != 202 {
		t.Fatalf("queue=%d %s", r.StatusCode, readBody(t, r))
	}
	r.Body.Close()
	close(m.release)
	select {
	case second := <-m.inputs:
		if second != "explicit user priority" {
			t.Fatalf("Goal overtook user: %s", second)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("queued user not dispatched")
	}
	select {
	case <-m.inputs:
	case <-time.After(5 * time.Second):
		t.Fatal("dedicated Goal continuation never ran")
	}
	deadline := time.After(5 * time.Second)
	for {
		goal, _ := e.GoalSnapshot()
		if goal != nil && goal.Status == "budget_limited" && owned.summary().ActiveTurnID == "" {
			if goal.TokensUsed != 30 || m.calls.Load() != 3 {
				t.Fatalf("usage=%+v calls=%d", goal, m.calls.Load())
			}
			break
		}
		select {
		case <-wake:
		case <-deadline:
			t.Fatalf("budget did not stop: %+v", goal)
		}
	}
	if item, claimed, err := e.ClaimNextRuntimeItem(); err != nil || claimed {
		t.Fatalf("generic queue widened: %+v %v %v", item, claimed, err)
	}
}

func (e *goalTestEngine) SubmitMessage(ctx context.Context, prompt string) (<-chan engine.QueryEvent, engine.Terminal) {
	events := make(chan engine.QueryEvent, 1)
	e.prompts <- prompt
	go func() {
		defer close(events)
		<-ctx.Done()
		events <- engine.QueryEvent{Type: engine.EventTerminal, TerminalInfo: &engine.Terminal{Reason: engine.TerminalAbortedStreaming, Err: ctx.Err()}}
	}()
	return events, engine.Terminal{}
}

func newGoalServer(t *testing.T) (*Server, *httptest.Server, *goalTestEngine, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	var e *goalTestEngine
	budget := uint64(9007199254740993)
	s, err := New(Config{Token: "test-token", SessionCatalogPath: filepath.Join(t.TempDir(), "catalog.json"), Factory: func(_ context.Context, options EngineOptions) (SessionEngine, error) {
		e = &goalTestEngine{QueryEngine: engine.NewQueryEngine(engine.QueryEngineConfig{CWD: options.CWD, TranscriptDir: options.TranscriptDir, SessionID: options.SessionID, ThreadID: options.ThreadID, CommandEntrypoint: commands.EntrypointAppServer, GoalCapability: &engine.GoalCapabilityConfig{Enabled: true, DefaultTokenBudget: &budget}}), prompts: make(chan string, 4)}
		return e, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(hs.Close)
	t.Cleanup(func() { shutdownTestServer(t, s) })
	r := createExecutionSettingsTestSession(t, hs.URL)
	defer r.Body.Close()
	var summary SessionSummary
	decodeResponse(t, r, &summary)
	return s, hs, e, summary.ID
}

func TestDesktopGoalRoutePersistsThenStartsAndStopPauses(t *testing.T) {
	s, hs, e, id := newGoalServer(t)
	endpoint := hs.URL + "/v1/sessions/" + id + "/goal"
	r := doJSON(t, endpoint, "test-token", http.MethodGet, nil)
	var state goalResponse
	decodeResponse(t, r, &state)
	if r.StatusCode != 200 || !state.Available || state.Goal != nil || state.DefaultTokenBudget == nil || *state.DefaultTokenBudget != "9007199254740993" || len(e.GetMessages()) != 0 {
		t.Fatalf("initial read = %+v", state)
	}
	r = doJSON(t, endpoint+"/control", "test-token", http.MethodPost, map[string]any{"operation": "create", "expected_goal_id": "", "expected_revision": "0", "objective": "desktop fixture"})
	var created goalControlResponse
	decodeResponse(t, r, &created)
	if r.StatusCode != 200 || !created.Saved || !created.Started || created.Turn == nil || created.State.Goal == nil || created.State.Goal.TokenBudget == nil || *created.State.Goal.TokenBudget != "9007199254740993" {
		t.Fatalf("create = %d %+v", r.StatusCode, created)
	}
	select {
	case prompt := <-e.prompts:
		if prompt != engine.GoalInitialPrompt {
			t.Fatal("adapter invented initial prompt")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("create did not start")
	}
	request := map[string]any{"operation": "edit", "expected_goal_id": created.State.Goal.GoalID, "expected_revision": created.State.Goal.Revision, "objective": "new objective"}
	r = doJSON(t, endpoint+"/control", "test-token", http.MethodPost, request)
	if r.StatusCode != 409 {
		t.Fatalf("active control = %d", r.StatusCode)
	}
	r.Body.Close()
	r = doJSON(t, hs.URL+"/v1/sessions/"+id+"/cancel", "test-token", http.MethodPost, CancelTurnRequest{TurnID: created.Turn.TurnID})
	if r.StatusCode != 204 {
		t.Fatalf("stop = %d", r.StatusCode)
	}
	r.Body.Close()
	owned, _ := s.getSession(id)
	_, wake, unsubscribe, _, err := owned.events.subscribe(0)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	deadline := time.After(3 * time.Second)
	for owned.summary().ActiveTurnID != "" {
		select {
		case <-wake:
		case <-deadline:
			t.Fatal("turn did not stop")
		}
	}
	goal, _ := e.GoalSnapshot()
	if goal.Status != "paused" {
		t.Fatalf("stop did not persist pause: %+v", goal)
	}
	r = doJSON(t, endpoint+"/control", "test-token", http.MethodPost, request)
	if r.StatusCode != 409 {
		t.Fatalf("stale edit = %d", r.StatusCode)
	}
	r.Body.Close()
	request["expected_revision"] = strconv.FormatUint(goal.Revision, 10)
	r = doJSON(t, endpoint+"/control", "test-token", http.MethodPost, request)
	var edited goalControlResponse
	decodeResponse(t, r, &edited)
	if r.StatusCode != 200 || edited.Started || !edited.Saved || edited.State.Goal.Objective != "new objective" {
		t.Fatalf("idle edit = %+v", edited)
	}
}

func TestDesktopGoalStrictHTTPAndReadHasNoAttach(t *testing.T) {
	_, hs, e, id := newGoalServer(t)
	endpoint := hs.URL + "/v1/sessions/" + id + "/goal"
	for _, body := range []string{
		`{}`, `null`, `{"operation":"create","expected_goal_id":"","expected_revision":0,"objective":"x"}`,
		`{"operation":"create","expected_goal_id":"","expected_revision":"00","objective":"x"}`,
		`{"operation":"create","expected_goal_id":"","expected_revision":"0","objective":"x","path":"private"}`,
		`{"operation":"create","expected_goal_id":"","expected_revision":"0","objective":"x","objective":"y"}`,
		`{"operation":"pause","expected_goal_id":"id","expected_revision":"1","objective":"ignored"}`,
		`{"operation":"create","expected_goal_id":"","expected_revision":"0","objective":"x","token_budget":"18446744073709551616"}`,
		`{"operation":"create","expected_goal_id":"","expected_revision":"0","objective":"\ud800"}`,
		`{"operation":"create","expected_goal_id":"","expected_revision":"0","objective":"` + strings.Repeat("界", 4001) + `"}`,
	} {
		request, _ := http.NewRequest(http.MethodPost, endpoint+"/control", bytes.NewBufferString(body))
		request.Header.Set("Authorization", "Bearer test-token")
		r, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		if r.StatusCode != 400 {
			t.Fatalf("malformed control = %d", r.StatusCode)
		}
		r.Body.Close()
	}
	for _, path := range []string{endpoint + "?path=private", hs.URL + "/v1/sessions/missing/goal"} {
		r := doJSON(t, path, "test-token", http.MethodGet, nil)
		if r.StatusCode != 400 && r.StatusCode != 404 {
			t.Fatalf("read = %d", r.StatusCode)
		}
		r.Body.Close()
	}
	if goal, exists := e.GoalSnapshot(); exists || goal != nil || len(e.prompts) != 0 {
		t.Fatal("invalid request mutated or dispatched")
	}
}

func TestDesktopGoalOwnershipLeaseRewakesFastAttachQueue(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var e *queueSessionEngine
	s, err := New(Config{Token: "test-token", SessionCatalogPath: filepath.Join(t.TempDir(), "catalog.json"), Factory: func(_ context.Context, options EngineOptions) (SessionEngine, error) {
		e = newQueueSessionEngine(options)
		return e, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	owned, err := newSession(t.Context(), s.factory, s.id, CreateSessionRequest{CWD: t.TempDir()}, nil, defaultEventBuffer, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	owned.server = s
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := owned.close(ctx); err != nil {
			t.Error(err)
		}
	})
	_, wake, unsubscribe, _, err := owned.events.subscribe(0)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	if _, err := e.EnqueueUserInputWithID("11111111-1111-4111-8111-111111111111", engine.UserTurnInput{Prompt: "recovered user work", Display: "recovered user work"}); err != nil {
		t.Fatal(err)
	}
	owned.startRuntimeInputPump()
	if _, err := owned.startTurn(StartTurnRequest{Prompt: "explicit attach first turn"}); err != nil {
		t.Fatal(err)
	}
	close(e.directRelease)
	deadline := time.After(3 * time.Second)
	for owned.summary().ActiveTurnID != "" {
		select {
		case <-wake:
		case <-deadline:
			t.Fatal("fast initial turn did not finish")
		}
	}
	owned.tryStartNextRuntimeItem() // deterministically consume a wake before map publication
	select {
	case <-e.runtimeStarted:
		t.Fatal("unowned activation dispatched queue")
	default:
	}
	s.mu.Lock()
	s.publishAttachedSessionLocked(owned)
	s.mu.Unlock()
	select {
	case item := <-e.runtimeStarted:
		if item.UserPrompt == nil || item.UserPrompt.Prompt != "recovered user work" {
			t.Fatalf("wrong item %+v", item)
		}
	case <-deadline:
		t.Fatal("published fast attach lost recovered queue wake")
	}
}
