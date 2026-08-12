package appserver

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine"
)

func TestActivityProjectionExcludesConversationAndSensitivePayloads(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	secret := "SECRET prompt command result plan path reasoning"
	conversation := []engine.QueryEvent{
		{
			RuntimeEventEnvelope: engine.RuntimeEventEnvelope{TurnID: "turn-1", Timestamp: now},
			Type:                 engine.EventAssistant,
			AssistantMessage:     &schema.Message{Content: secret, ReasoningContent: secret},
		},
		{
			RuntimeEventEnvelope: engine.RuntimeEventEnvelope{TurnID: "turn-1", Timestamp: now},
			Type:                 engine.EventStream,
			StreamEvent:          &schema.Message{Content: secret, ReasoningContent: secret},
		},
		{
			RuntimeEventEnvelope: engine.RuntimeEventEnvelope{TurnID: "turn-1", Timestamp: now},
			Type:                 engine.EventCanonicalProjection,
		},
	}
	for _, event := range conversation {
		if entry, ok := projectEngineActivity(event, "turn-1"); ok {
			t.Fatalf("conversation event projected into Activity: %#v", entry)
		}
	}

	events := []struct {
		name          string
		event         engine.QueryEvent
		wantKind      string
		wantState     string
		wantCategory  string
		wantStableKey string
	}{
		{
			name: "tool progress",
			event: engine.QueryEvent{
				RuntimeEventEnvelope: engine.RuntimeEventEnvelope{TurnID: "turn-1", Timestamp: now},
				Type:                 engine.EventToolProgress,
				ToolProgress: &engine.ToolProgressEvent{
					ToolName: "Bash", ToolUseID: "tool-1", Content: secret,
				},
			},
			wantKind: "tool", wantState: "running", wantCategory: "command",
			wantStableKey: "tool-1",
		},
		{
			name: "tool result",
			event: engine.QueryEvent{
				RuntimeEventEnvelope: engine.RuntimeEventEnvelope{TurnID: "turn-1", Timestamp: now.Add(time.Second)},
				Type:                 engine.EventToolResult,
				ToolResultMessage: &schema.Message{
					Content: secret, ToolCallID: "tool-1", ToolName: "Bash",
				},
			},
			wantKind: "tool", wantState: "completed", wantCategory: "command",
			wantStableKey: "tool-1",
		},
		{
			name: "task progress",
			event: engine.QueryEvent{
				RuntimeEventEnvelope: engine.RuntimeEventEnvelope{TurnID: "turn-1", Timestamp: now},
				Type:                 engine.EventTaskProgress,
				TaskProgress: &engine.TaskProgressEvent{
					TaskID: "task-1", Description: secret, Summary: secret, LastToolName: "Bash",
				},
			},
			wantKind: "task", wantState: "running", wantCategory: "task",
			wantStableKey: "task-1",
		},
		{
			name: "agent failure",
			event: engine.QueryEvent{
				RuntimeEventEnvelope: engine.RuntimeEventEnvelope{
					TurnID: "turn-1", AgentID: "agent-1", Timestamp: now,
				},
				Type: engine.EventAgentLifecycle,
				AgentLifecycle: &engine.AgentLifecycleEvent{
					Phase: "launch_failed", Status: "failed", Task: secret,
					Error: secret, CWD: "/" + secret,
				},
			},
			wantKind: "agent", wantState: "failed", wantCategory: "agent",
			wantStableKey: "agent-1",
		},
		{
			name: "question waiting",
			event: engine.QueryEvent{
				RuntimeEventEnvelope: engine.RuntimeEventEnvelope{TurnID: "turn-1", Timestamp: now},
				Type:                 engine.EventPermissionRequest,
				PermissionRequest: &engine.PermissionRequestEvent{
					ToolUseID: "question-1", Kind: engine.PermissionInteractionKindQuestion,
					Input: map[string]any{"secret": secret}, Message: secret,
				},
			},
			wantKind: "interaction", wantState: "waiting", wantCategory: "question",
			wantStableKey: "question-1",
		},
	}

	stableIDs := make(map[string]string)
	for _, test := range events {
		t.Run(test.name, func(t *testing.T) {
			entry, ok := projectEngineActivity(test.event, "turn-1")
			if !ok {
				t.Fatal("event did not project")
			}
			if entry.Kind != test.wantKind || entry.State != test.wantState ||
				entry.Category != test.wantCategory || entry.TurnID != "turn-1" {
				t.Fatalf("entry = %#v", entry)
			}
			encoded, err := json.Marshal(entry)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{secret, "Bash", "tool-1", "task-1", "agent-1", "question-1"} {
				if strings.Contains(string(encoded), forbidden) {
					t.Fatalf("Activity leaked %q: %s", forbidden, encoded)
				}
			}
			if prior := stableIDs[test.wantStableKey]; prior != "" && prior != entry.ID {
				t.Fatalf("stable ID changed: %q -> %q", prior, entry.ID)
			}
			stableIDs[test.wantStableKey] = entry.ID
		})
	}
}

func TestActivityLogCoalescesStableIdentityAndBoundsTail(t *testing.T) {
	log := newActivityLog()
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	started, ok := projectTurnActivity("turn-1", engine.TerminalReason(""), now)
	if !ok || !log.upsert(started) {
		t.Fatal("turn start was not admitted")
	}
	completed, ok := projectTurnActivity("turn-1", engine.TerminalCompleted, now.Add(time.Second))
	if !ok || !log.upsert(completed) {
		t.Fatal("turn completion was not admitted")
	}
	if got := log.snapshot(); len(got) != 1 || got[0].State != "completed" || got[0].ID != started.ID {
		t.Fatalf("coalesced turn = %#v", got)
	}

	for index := 0; index < maxActivityEntries+5; index++ {
		entry, projected := projectEngineActivity(engine.QueryEvent{
			RuntimeEventEnvelope: engine.RuntimeEventEnvelope{
				TurnID: "turn-1", Timestamp: now.Add(time.Duration(index+2) * time.Second),
			},
			Type: engine.EventToolProgress,
			ToolProgress: &engine.ToolProgressEvent{
				ToolName: "Read", ToolUseID: fmt.Sprintf("tool-%03d", index), IsFinal: true,
			},
		}, "turn-1")
		if !projected || !log.upsert(entry) {
			t.Fatalf("tool %d was not admitted", index)
		}
	}
	tail := log.snapshot()
	if len(tail) != maxActivityEntries {
		t.Fatalf("tail length = %d, want %d", len(tail), maxActivityEntries)
	}
	lastID := tail[len(tail)-1].ID
	tail[len(tail)-1].State = "failed"
	if got := log.snapshot(); got[len(got)-1].ID != lastID || got[len(got)-1].State != "completed" {
		t.Fatal("snapshot mutation leaked into Activity log")
	}
}

func TestActivityLogDoesNotRegressTerminalLifecycle(t *testing.T) {
	log := newActivityLog()
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	completed, ok := projectEngineActivity(engine.QueryEvent{
		RuntimeEventEnvelope: engine.RuntimeEventEnvelope{TurnID: "turn-1", Timestamp: now},
		Type:                 engine.EventToolProgress,
		ToolProgress: &engine.ToolProgressEvent{
			ToolName: "Bash", ToolUseID: "tool-terminal", IsFinal: true,
		},
	}, "turn-1")
	if !ok || !log.upsert(completed) {
		t.Fatal("completed tool was not admitted")
	}
	lateRunning := completed
	lateRunning.State = "running"
	lateRunning.Timestamp = now.Add(time.Second)
	if log.upsert(lateRunning) {
		t.Fatal("late running tool regressed completed Activity")
	}

	waiting, ok := projectEngineActivity(engine.QueryEvent{
		RuntimeEventEnvelope: engine.RuntimeEventEnvelope{TurnID: "turn-1", Timestamp: now},
		Type:                 engine.EventPermissionRequest,
		PermissionRequest: &engine.PermissionRequestEvent{
			ToolUseID: "question-terminal", Kind: engine.PermissionInteractionKindQuestion,
		},
	}, "turn-1")
	if !ok || !log.upsert(waiting) {
		t.Fatal("waiting interaction was not admitted")
	}
	resolved := waiting
	resolved.State = "resolved"
	resolved.Timestamp = now.Add(time.Second)
	if !log.upsert(resolved) {
		t.Fatal("resolved interaction was not admitted")
	}
	lateWaiting := waiting
	lateWaiting.Timestamp = now.Add(2 * time.Second)
	if log.upsert(lateWaiting) {
		t.Fatal("late waiting interaction regressed resolved Activity")
	}

	tail := log.snapshot()
	if len(tail) != 2 || tail[0].State != "completed" || tail[1].State != "resolved" {
		t.Fatalf("terminal Activity tail = %#v", tail)
	}
}

func TestInteractionActivityCoalescesAcrossRuntimeResumeTurns(t *testing.T) {
	now := time.Date(2026, time.August, 11, 4, 10, 0, 0, time.UTC)
	waiting, ok := projectEngineActivity(engine.QueryEvent{
		RuntimeEventEnvelope: engine.RuntimeEventEnvelope{
			TurnID:    "turn-origin",
			Timestamp: now,
		},
		Type: engine.EventPermissionRequest,
		PermissionRequest: &engine.PermissionRequestEvent{
			ToolUseID: "question-across-resume",
			Kind:      engine.PermissionInteractionKindQuestion,
		},
	}, "")
	if !ok {
		t.Fatal("waiting interaction was not projected")
	}
	resolved, ok := projectEngineActivity(engine.QueryEvent{
		RuntimeEventEnvelope: engine.RuntimeEventEnvelope{
			TurnID:    "turn-resume",
			Timestamp: now.Add(time.Second),
		},
		Type: engine.EventPermissionResolved,
		PermissionResolved: &engine.PermissionResolvedEvent{
			ToolUseID: "question-across-resume",
			Kind:      engine.PermissionInteractionKindQuestion,
		},
	}, "")
	if !ok {
		t.Fatal("resolved interaction was not projected")
	}
	if waiting.ID != resolved.ID {
		t.Fatalf("interaction IDs differ across resume: waiting=%q resolved=%q", waiting.ID, resolved.ID)
	}

	log := newActivityLog()
	if !log.upsert(waiting) || !log.upsert(resolved) {
		t.Fatal("interaction lifecycle was not admitted")
	}
	tail := log.snapshot()
	if len(tail) != 1 || tail[0].State != "resolved" ||
		tail[0].Category != "question" || tail[0].TurnID != "turn-origin" {
		t.Fatalf("interaction Activity tail = %#v", tail)
	}

	lateWaiting := waiting
	lateWaiting.Timestamp = now.Add(2 * time.Second)
	if log.upsert(lateWaiting) {
		t.Fatal("late waiting interaction regressed resolved Activity")
	}
}

func TestSessionPublishesInteractionActivityWithOriginTurn(t *testing.T) {
	now := time.Date(2026, time.August, 11, 4, 20, 0, 0, time.UTC)
	waiting, ok := activityForResource(
		"interaction", "waiting", "question", "turn-origin", "question-resume", now,
	)
	if !ok {
		t.Fatal("waiting interaction was not projected")
	}
	resolved, ok := activityForResource(
		"interaction", "resolved", "question", "turn-resume", "question-resume", now.Add(time.Second),
	)
	if !ok {
		t.Fatal("resolved interaction was not projected")
	}

	s := &session{
		id:       "session-1",
		threadID: "thread-1",
		events:   newEventLog(8),
		activity: newActivityLog(),
	}
	s.publishActivity(waiting)
	s.publishActivity(resolved)

	replay, _, unsubscribe, _, err := s.events.subscribe(0)
	if err != nil {
		t.Fatal(err)
	}
	unsubscribe()
	if len(replay) != 2 {
		t.Fatalf("published Activity events = %d, want 2", len(replay))
	}
	var published ActivityEntry
	if err := json.Unmarshal(replay[1].Data, &published); err != nil {
		t.Fatal(err)
	}
	if replay[1].TurnID != "turn-origin" || published.TurnID != "turn-origin" ||
		published.ID != waiting.ID || published.State != "resolved" {
		t.Fatalf("resolved Activity event = %#v; envelope = %#v", published, replay[1])
	}
}

func TestInteractionActivityPreservesOriginAfterTailEviction(t *testing.T) {
	now := time.Date(2026, time.August, 11, 4, 30, 0, 0, time.UTC)
	waiting, ok := activityForResource(
		"interaction", "waiting", "question", "turn-origin", "question-evicted", now,
	)
	if !ok {
		t.Fatal("waiting interaction was not projected")
	}
	resolved, ok := activityForResource(
		"interaction", "resolved", "question", "turn-resume", "question-evicted", now.Add(time.Minute),
	)
	if !ok {
		t.Fatal("resolved interaction was not projected")
	}

	log := newActivityLog()
	if !log.upsert(waiting) {
		t.Fatal("waiting interaction was not admitted")
	}
	for index := 0; index < maxActivityEntries; index++ {
		entry, projected := activityForResource(
			"tool", "completed", "tool", "turn-origin",
			fmt.Sprintf("tool-after-question-%03d", index), now.Add(time.Duration(index+1)*time.Second),
		)
		if !projected || !log.upsert(entry) {
			t.Fatalf("tool %d was not admitted", index)
		}
	}
	for _, entry := range log.snapshot() {
		if entry.ID == waiting.ID {
			t.Fatal("waiting interaction was not evicted from the bounded tail")
		}
	}

	normalized, updated := log.upsertEntry(resolved)
	if !updated || normalized.TurnID != "turn-origin" {
		t.Fatalf("resolved interaction = %#v, updated = %t", normalized, updated)
	}
	tail := log.snapshot()
	if len(tail) != maxActivityEntries || tail[len(tail)-1] != normalized ||
		tail[len(tail)-1].State != "resolved" {
		t.Fatalf("Activity tail after resolved interaction = %#v", tail)
	}
	if len(log.pendingInteractionOrigins) != 0 {
		t.Fatalf("resolved interaction origin was retained: %#v", log.pendingInteractionOrigins)
	}
}

func TestSessionPublishesSemanticActivityAlongsideTransportEvents(t *testing.T) {
	s := &session{
		id:       "session-1",
		threadID: "thread-1",
		events:   newEventLog(16),
		activity: newActivityLog(),
	}
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	s.publishSynthetic("turn.accepted", "turn-1", map[string]any{"turn_id": "turn-1"})
	s.publishEngine(engine.QueryEvent{
		RuntimeEventEnvelope: engine.RuntimeEventEnvelope{TurnID: "turn-1", Timestamp: now},
		Type:                 engine.EventAssistant,
		AssistantMessage:     &schema.Message{Content: "SECRET assistant prose"},
	}, "turn-1")
	s.publishEngine(engine.QueryEvent{
		RuntimeEventEnvelope: engine.RuntimeEventEnvelope{TurnID: "turn-1", Timestamp: now},
		Type:                 engine.EventToolProgress,
		ToolProgress: &engine.ToolProgressEvent{
			ToolName: "Bash", ToolUseID: "tool-1", Content: "SECRET command",
		},
	}, "turn-1")
	s.publishSynthetic("turn.finished", "turn-1", map[string]any{
		"reason": engine.TerminalCompleted,
		"error":  "SECRET error",
	})

	replay, _, unsubscribe, _, err := s.events.subscribe(0)
	if err != nil {
		t.Fatal(err)
	}
	unsubscribe()
	activityEvents := make([]WireEvent, 0, 3)
	for _, event := range replay {
		if event.Type == "activity" {
			activityEvents = append(activityEvents, event)
		}
	}
	if len(activityEvents) != 3 {
		t.Fatalf("activity event count = %d; replay = %#v", len(activityEvents), replay)
	}
	encoded, err := json.Marshal(activityEvents)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "SECRET") || strings.Contains(string(encoded), "terminal") {
		t.Fatalf("semantic Activity leaked transport content: %s", encoded)
	}
	tail := s.activity.snapshot()
	if len(tail) != 2 || tail[0].Kind != "tool" || tail[1].Kind != "turn" || tail[1].State != "completed" {
		t.Fatalf("Activity tail = %#v", tail)
	}
}
