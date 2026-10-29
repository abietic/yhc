package engine

import (
	"bytes"
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestSideQuestionCloseRejectsLateUsageWrites(t *testing.T) {
	usage, err := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 1})
	if err != nil {
		t.Fatal(err)
	}
	probe := &sideQuestionUncooperativeModel{entered: make(chan struct{}), release: make(chan struct{})}
	eng := newSideQuestionTestEngine(t, probe, usage)
	t.Cleanup(func() {
		select {
		case <-probe.release:
		default:
			close(probe.release)
		}
	})
	snapshot, err := eng.BeginSideQuestion("late close")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); _, _ = eng.RunSideQuestion(t.Context(), snapshot.ID) }()
	select {
	case <-probe.entered:
	case <-time.After(time.Second):
		t.Fatal("provider not entered")
	}
	path := eng.GetTranscript().Path()
	eng.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	close(probe.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("late provider did not finish")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("closed recorder was reopened or changed by late usage")
	}
	if usage.Snapshot().ProviderCalls != 1 {
		t.Fatal("wire admission disappeared after close")
	}
	if _, ok := eng.SideQuestion(snapshot.ID); ok {
		t.Fatal("closed engine exposed an answer")
	}
}

func TestSideQuestionRestoreCancelsBeforeUsageOwnerChanges(t *testing.T) {
	probe := &sideQuestionUncooperativeModel{entered: make(chan struct{}), release: make(chan struct{})}
	eng := newSideQuestionTestEngine(t, probe, nil)
	t.Cleanup(func() {
		select {
		case <-probe.release:
		default:
			close(probe.release)
		}
	})
	eng.mu.Lock()
	dir, cwd := eng.config.TranscriptDir, eng.config.CWD
	eng.mu.Unlock()
	recorder := transcript.NewRecorder("side-question-selected", dir)
	if err := recorder.RecordMessages([]*schema.Message{{Role: schema.User, Content: "selected question"}, {Role: schema.Assistant, Content: "selected answer"}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	writeProjectGraphRootTestMetadata(t, recorder, &session.SessionMetadataFull{SessionID: "side-question-selected", ThreadID: "side-question-selected", CWD: cwd, CreatedAt: now, UpdatedAt: now, MessageCount: 2})
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := eng.BeginSideQuestion("old owner")
	if err != nil {
		t.Fatal(err)
	}
	sideDone := make(chan struct{})
	go func() { defer close(sideDone); _, _ = eng.RunSideQuestion(t.Context(), snapshot.ID) }()
	select {
	case <-probe.entered:
	case <-time.After(time.Second):
		t.Fatal("provider not entered")
	}
	restoreDone := make(chan error, 1)
	go func() { _, err := eng.ResumeSession(t.Context(), "side-question-selected"); restoreDone <- err }()
	deadline := time.Now().Add(time.Second)
	for {
		eng.sideQuestions.mu.Lock()
		invalidated := len(eng.sideQuestions.records) == 0
		eng.sideQuestions.mu.Unlock()
		if invalidated {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("restore did not invalidate auxiliary request")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case err := <-restoreDone:
		t.Fatalf("restore crossed active provider boundary early: %v", err)
	default:
	}
	close(probe.release)
	select {
	case <-sideDone:
	case <-time.After(time.Second):
		t.Fatal("side question did not drain")
	}
	select {
	case err := <-restoreDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("restore did not complete")
	}
	restored, err := recorder.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Messages) != 2 || restored.Usage.TotalTokens != 0 {
		t.Fatal("old auxiliary usage or answer entered selected session")
	}
	if _, ok := eng.SideQuestion(snapshot.ID); ok {
		t.Fatal("restored engine exposed old side answer")
	}
}

// sideQuestionUncooperativeModel models a provider that does not honor a
// cancelled request until the test explicitly releases it.
type sideQuestionUncooperativeModel struct {
	entered chan struct{}
	release chan struct{}
	mu      sync.Mutex
	calls   int
}

func (m *sideQuestionUncooperativeModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "late answer"}, nil
}

func (m *sideQuestionUncooperativeModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	close(m.entered)
	<-m.release
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role: schema.Assistant, Content: "late answer",
		ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{TotalTokens: 1}},
	}}), nil
}

func (m *sideQuestionUncooperativeModel) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func newSideQuestionTestEngine(t *testing.T, chatModel model.BaseChatModel, usage *execution.RunUsage) *QueryEngine {
	t.Helper()
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID: "side-question-session", ThreadID: "side-question-thread",
		CWD: t.TempDir(), TranscriptDir: t.TempDir(), ChatModel: chatModel,
		Model: "side-question-model", CommandEntrypoint: commands.EntrypointTUI,
		RunUsage: usage,
	})
	t.Cleanup(eng.Close)
	return eng
}

func TestSideQuestionUsesDetachedSnapshotAndAccountsUsage(t *testing.T) {
	usage, err := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 1})
	if err != nil {
		t.Fatal(err)
	}
	probe := &promptSuggestionProbeModel{result: "auxiliary answer"}
	eng := newSideQuestionTestEngine(t, probe, usage)
	eng.mu.Lock()
	eng.messages = []*schema.Message{{Role: schema.User, Content: "earlier main message"}}
	eng.mu.Unlock()

	snapshot, err := eng.BeginSideQuestion("what is the next step?")
	if err != nil {
		t.Fatal(err)
	}
	if calls, _, _, _, _, _ := probe.snapshot(); calls != 0 {
		t.Fatalf("BeginSideQuestion dispatched %d provider calls", calls)
	}
	eng.mu.Lock()
	eng.messages = append(eng.messages, &schema.Message{Role: schema.User, Content: "later main message"})
	eng.mu.Unlock()

	got, err := eng.RunSideQuestion(context.Background(), snapshot.ID)
	if err != nil || got.Status != SideQuestionAnswered || got.Answer != "auxiliary answer" {
		t.Fatalf("RunSideQuestion = %#v, %v", got, err)
	}
	calls, providerMessages, _, tools, maxTokens, _ := probe.snapshot()
	if calls != 1 || tools != 0 || maxTokens != 2048 {
		t.Fatalf("provider calls=%d tools=%d maxTokens=%d", calls, tools, maxTokens)
	}
	var contents []string
	for _, message := range providerMessages {
		contents = append(contents, message.Content)
	}
	joined := strings.Join(contents, "\n")
	if !strings.Contains(joined, "earlier main message") || strings.Contains(joined, "later main message") {
		t.Fatalf("provider did not receive the Begin snapshot: %q", joined)
	}
	mainMessages := eng.GetMessages()
	if len(mainMessages) != 2 || strings.Contains(strings.Join([]string{mainMessages[0].Content, mainMessages[1].Content}, "\n"), "auxiliary answer") {
		t.Fatalf("side question leaked into main messages: %#v", mainMessages)
	}
	eng.mu.Lock()
	sessionID, transcriptDir := eng.config.SessionID, eng.config.TranscriptDir
	eng.mu.Unlock()
	restored, err := transcript.NewRecorder(sessionID, transcriptDir).LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Messages) != 0 || restored.Usage.TotalTokens != 12 {
		t.Fatalf("ephemeral side text entered durable messages or usage was lost: %+v", restored)
	}
	usageSnapshot := usage.Snapshot()
	if usageSnapshot.ProviderCalls != 1 || len(usageSnapshot.Routes) != 1 || usageSnapshot.Routes[0].Source != "side_question" {
		t.Fatalf("side-question usage not recorded: %+v", usageSnapshot)
	}
}

func TestSideQuestionCancelRetainsRunningSlotAndCannotRepeat(t *testing.T) {
	probe := &sideQuestionUncooperativeModel{entered: make(chan struct{}), release: make(chan struct{})}
	eng := newSideQuestionTestEngine(t, probe, nil)
	t.Cleanup(func() {
		select {
		case <-probe.release:
		default:
			close(probe.release)
		}
	})
	snapshot, err := eng.BeginSideQuestion("cancel me")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := eng.RunSideQuestion(context.Background(), snapshot.ID); done <- err }()
	select {
	case <-probe.entered:
	case <-time.After(time.Second):
		t.Fatal("provider was not entered")
	}
	if _, err := eng.RunSideQuestion(context.Background(), snapshot.ID); err == nil {
		t.Fatal("duplicate RunSideQuestion succeeded")
	}
	eng.CancelSideQuestion(snapshot.ID)
	if state, ok := eng.SideQuestion(snapshot.ID); !ok || state.Status != SideQuestionCancelled {
		t.Fatalf("cancel state = %#v, found=%v", state, ok)
	}
	if _, err := eng.BeginSideQuestion("must wait for provider return"); err == nil {
		t.Fatal("running cancelled provider released its slot early")
	}
	close(probe.release)
	if err := <-done; err == nil {
		t.Fatal("cancelled provider run succeeded")
	}
	if state, ok := eng.SideQuestion(snapshot.ID); !ok || state.Status != SideQuestionCancelled || state.Answer != "" {
		t.Fatalf("late provider response resurrected state: %#v, found=%v", state, ok)
	}
	if calls := probe.callCount(); calls != 1 {
		t.Fatalf("provider calls=%d, want exactly one", calls)
	}
	if _, err := eng.BeginSideQuestion("slot available after provider return"); err != nil {
		t.Fatalf("slot remained occupied: %v", err)
	}
}

func TestSideQuestionDropsStaleRoutesBeforeAndAfterProvider(t *testing.T) {
	t.Run("before dispatch", func(t *testing.T) {
		probe := &promptSuggestionProbeModel{result: "never sent"}
		eng := newSideQuestionTestEngine(t, probe, nil)
		snapshot, err := eng.BeginSideQuestion("stale")
		if err != nil {
			t.Fatal(err)
		}
		eng.mu.Lock()
		eng.promptRouteGeneration++
		eng.mu.Unlock()
		got, err := eng.RunSideQuestion(context.Background(), snapshot.ID)
		if err == nil || got.Status != SideQuestionFailed || got.Reason != "stale" {
			t.Fatalf("stale dispatch = %#v, %v", got, err)
		}
		if calls, _, _, _, _, _ := probe.snapshot(); calls != 0 {
			t.Fatalf("stale request dispatched %d calls", calls)
		}
	})
	t.Run("after response", func(t *testing.T) {
		probe := &promptSuggestionProbeModel{result: "late answer"}
		eng := newSideQuestionTestEngine(t, probe, nil)
		probe.onCall = func() {
			eng.mu.Lock()
			eng.promptRouteGeneration++
			eng.mu.Unlock()
		}
		snapshot, err := eng.BeginSideQuestion("stale after response")
		if err != nil {
			t.Fatal(err)
		}
		got, err := eng.RunSideQuestion(context.Background(), snapshot.ID)
		if err == nil || got.Status != SideQuestionFailed || got.Reason != "stale" || got.Answer != "" {
			t.Fatalf("late response = %#v, %v", got, err)
		}
	})
}

func TestSideQuestionRejectsInvalidOutputsAndBoundsText(t *testing.T) {
	tests := []struct {
		name    string
		message *schema.Message
		wantOK  bool
	}{
		{name: "empty", message: &schema.Message{Role: schema.Assistant}},
		{name: "control", message: &schema.Message{Role: schema.Assistant, Content: "bad\x00text"}},
		{name: "tool call", message: &schema.Message{Role: schema.Assistant, Content: "no", ToolCalls: []schema.ToolCall{{ID: "forbidden"}}}},
		{name: "large valid answer", message: &schema.Message{Role: schema.Assistant, Content: strings.Repeat("界", MaxSideAnswerBytes)}, wantOK: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			eng := newSideQuestionTestEngine(t, &funcModel{fn: func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
				return test.message, nil
			}}, nil)
			snapshot, err := eng.BeginSideQuestion("valid")
			if err != nil {
				t.Fatal(err)
			}
			got, err := eng.RunSideQuestion(context.Background(), snapshot.ID)
			if test.wantOK {
				if err != nil || got.Status != SideQuestionAnswered || !got.AnswerTruncated || len(got.Answer) > MaxSideAnswerBytes || !utf8.ValidString(got.Answer) {
					t.Fatalf("bounded answer = %#v, %v", got, err)
				}
				return
			}
			if err == nil || got.Status != SideQuestionFailed || got.Answer != "" {
				t.Fatalf("invalid answer = %#v, %v", got, err)
			}
		})
	}
	eng := newSideQuestionTestEngine(t, &promptSuggestionProbeModel{}, nil)
	for _, question := range []string{"", "bad\x00text", strings.Repeat("x", MaxSideQuestionBytes+1), string([]byte{0xff})} {
		if _, err := eng.BeginSideQuestion(question); err == nil {
			t.Fatalf("accepted invalid question %q", question)
		}
	}
}

func TestSideQuestionPendingCancelDoesNotDispatch(t *testing.T) {
	probe := &promptSuggestionProbeModel{result: "must not run"}
	eng := newSideQuestionTestEngine(t, probe, nil)
	snapshot, err := eng.BeginSideQuestion("cancel before dispatch")
	if err != nil {
		t.Fatal(err)
	}
	eng.CancelSideQuestion(snapshot.ID)
	if got, ok := eng.SideQuestion(snapshot.ID); !ok || got.Status != SideQuestionCancelled {
		t.Fatalf("pending cancellation = %#v, found=%v", got, ok)
	}
	if _, err := eng.RunSideQuestion(context.Background(), snapshot.ID); err == nil {
		t.Fatal("cancelled pending request ran")
	}
	if calls, _, _, _, _, _ := probe.snapshot(); calls != 0 {
		t.Fatalf("pending cancellation dispatched %d calls", calls)
	}
}

func TestSideQuestionAvailabilityAndReducerNeverResurrects(t *testing.T) {
	if ok, _ := newSideQuestionTestEngine(t, nil, nil).SideQuestionAvailability(); ok {
		t.Fatal("nil model is available")
	}
	child := newSideQuestionTestEngine(t, &promptSuggestionProbeModel{}, nil)
	child.mu.Lock()
	child.config.AgentID = "child"
	child.mu.Unlock()
	if ok, _ := child.SideQuestionAvailability(); ok {
		t.Fatal("child engine is available")
	}
	planned := newSideQuestionTestEngine(t, &promptSuggestionProbeModel{}, nil)
	planned.planMu.Lock()
	planned.planState.Phase = PlanPhaseAwaitingApproval
	planned.planMu.Unlock()
	if ok, _ := planned.SideQuestionAvailability(); ok {
		t.Fatal("awaiting plan approval is available")
	}
	closed := newSideQuestionTestEngine(t, &promptSuggestionProbeModel{}, nil)
	closed.Close()
	if ok, _ := closed.SideQuestionAvailability(); ok {
		t.Fatal("closed engine is available")
	}

	pending := SideQuestionSnapshot{ID: "q", Status: SideQuestionPending, Revision: 1}
	cancelled, ok := reduceSideQuestion(pending, sideQuestionEvent{ID: "q", Status: SideQuestionCancelled, At: time.Now()})
	if !ok || cancelled.Status != SideQuestionCancelled {
		t.Fatalf("cancel reducer = %#v, %v", cancelled, ok)
	}
	resurrected, ok := reduceSideQuestion(cancelled, sideQuestionEvent{ID: "q", Status: SideQuestionAnswered, Answer: "late", At: time.Now()})
	if ok || resurrected.Status != SideQuestionCancelled || resurrected.Answer != "" {
		t.Fatalf("late answer resurrected state: %#v, %v", resurrected, ok)
	}
}
