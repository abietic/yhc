package engine

import (
	"errors"
	"testing"

	"github.com/abietic/yhc/engine/transcript"
	"github.com/cloudwego/eino/schema"
)

func agentTranscriptIdentityFixture(t *testing.T) (*QueryEngine, *RuntimeStateStore, *transcript.Recorder, AgentTranscriptPageRequest) {
	t.Helper()
	recorder := transcript.NewRecorder("child-session", t.TempDir())
	if err := recorder.RecordMessages([]*schema.Message{{Role: schema.User, Content: "older"}, {Role: schema.Assistant, Content: "newer"}}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
	store := NewRuntimeStateStore()
	launch := agentTranscriptTestEvent("agent-1", "child-session", "child-thread", 1, EventAgentLifecycle)
	launch.AgentLifecycle = &AgentLifecycleEvent{Status: "running", Generation: 1, TranscriptPath: recorder.Path(), StartedAt: launch.Timestamp}
	if err := store.Apply(launch); err != nil {
		t.Fatal(err)
	}
	qe := NewQueryEngine(QueryEngineConfig{CWD: t.TempDir(), SessionID: "leader", ThreadID: "leader", RuntimeState: store})
	t.Cleanup(qe.Close)
	qe.agentRunner = nil
	return qe, store, recorder, AgentTranscriptPageRequest{AgentID: "agent-1", Generation: 1, SessionID: "child-session", ThreadID: "child-thread", Limit: 1}
}

func TestAgentTranscriptPageBindsOptionalExactLineageBeforeIO(t *testing.T) {
	for _, selection := range []struct{ session, thread string }{{"other", "child-thread"}, {"child-session", "other"}, {"", "child-thread"}, {"child-session", ""}} {
		t.Run(selection.session+"/"+selection.thread, func(t *testing.T) {
			qe, _, _, request := agentTranscriptIdentityFixture(t)
			request.SessionID, request.ThreadID = selection.session, selection.thread
			calls := 0
			page, _, err := qe.agentTranscriptPage(request, func(req transcript.MessagePageRequest) (*transcript.MessagePageResult, error) {
				calls++
				return transcript.LoadMessagePage(req)
			})
			if !errors.Is(err, ErrAgentTranscriptSelectionChanged) || calls != 0 || len(page.Messages) != 0 {
				t.Fatalf("lineage rejection: calls=%d page=%#v err=%v", calls, page, err)
			}
		})
	}
}

func TestAgentTranscriptPageRechecksIdentityAfterIOWithoutAdmittingCursor(t *testing.T) {
	qe, store, recorder, request := agentTranscriptIdentityFixture(t)
	page, _, err := qe.agentTranscriptPage(request, func(req transcript.MessagePageRequest) (*transcript.MessagePageResult, error) {
		loaded, err := transcript.LoadMessagePage(req)
		if err != nil {
			return loaded, err
		}
		launch := agentTranscriptTestEvent("agent-1", "replacement-session", "replacement-thread", 1, EventAgentLifecycle)
		launch.AgentLifecycle = &AgentLifecycleEvent{Status: "running", Generation: 2, TranscriptPath: recorder.Path(), StartedAt: launch.Timestamp}
		if err := store.Apply(launch); err != nil {
			t.Fatal(err)
		}
		return loaded, nil
	})
	if !errors.Is(err, ErrAgentTranscriptSelectionChanged) || len(page.Messages) != 0 || page.NextCursor != "" {
		t.Fatalf("post-read lineage rejection: page=%#v err=%v", page, err)
	}
	if len(qe.agentTranscriptSelectorState().states) != 0 {
		t.Fatal("rejected page admitted a continuation capability")
	}
}

func TestAgentTranscriptPageRechecksFileAfterIO(t *testing.T) {
	qe, _, recorder, request := agentTranscriptIdentityFixture(t)
	page, _, err := qe.agentTranscriptPage(request, func(req transcript.MessagePageRequest) (*transcript.MessagePageResult, error) {
		loaded, err := transcript.LoadMessagePage(req)
		if err != nil {
			return loaded, err
		}
		if err := recorder.Replace([]*schema.Message{{Role: schema.User, Content: "replacement"}}); err != nil {
			t.Fatal(err)
		}
		return loaded, nil
	})
	if !errors.Is(err, transcript.ErrTranscriptRevisionChanged) || len(page.Messages) != 0 || page.NextCursor != "" {
		t.Fatalf("post-read file rejection: page=%#v err=%v", page, err)
	}
}

func TestAgentTranscriptPageAcceptsOrdinaryProgressAndUsesCurrentMode(t *testing.T) {
	qe, store, _, request := agentTranscriptIdentityFixture(t)
	page, found, err := qe.agentTranscriptPage(request, func(req transcript.MessagePageRequest) (*transcript.MessagePageResult, error) {
		loaded, err := transcript.LoadMessagePage(req)
		complete := agentTranscriptTestEvent("agent-1", "child-session", "child-thread", 2, EventAgentLifecycle)
		complete.AgentLifecycle = &AgentLifecycleEvent{Status: "completed", Generation: 1}
		if err := store.Apply(complete); err != nil {
			t.Fatal(err)
		}
		return loaded, err
	})
	if err != nil || !found || page.AttachMode != ThreadModeReplayOnly || !page.Replay || len(page.Messages) != 1 || !page.Messages[0].Replay {
		t.Fatalf("ordinary terminal progress: found=%v page=%#v err=%v", found, page, err)
	}
}
