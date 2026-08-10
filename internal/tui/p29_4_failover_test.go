package tui

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine"
)

func TestP294AppRetractsExactAttemptBeforeProjectingNextAttempt(
	t *testing.T,
) {
	app := newTestApp(80, 24)
	first := &engine.ModelAttemptEvent{AttemptID: "attempt-1"}
	app.handleEngineEvent(engine.QueryEvent{
		Type:         engine.EventAssistant,
		ModelAttempt: first,
		Message: &schema.Message{
			Role:             schema.Assistant,
			ReasoningContent: "first reasoning",
			Content:          "first partial",
			ToolCalls: []schema.ToolCall{{
				ID: "failed-tool",
				Function: schema.FunctionCall{
					Name:      "Bash",
					Arguments: `{"command":"false"}`,
				},
			}},
		},
	})
	if _, exists := app.chat.toolsByID["failed-tool"]; !exists {
		t.Fatal("failed attempt tool was not projected before tombstone")
	}
	if _, exists := app.activeTools["failed-tool"]; !exists {
		t.Fatal("failed attempt tool was not tracked before tombstone")
	}
	app.handleEngineEvent(engine.QueryEvent{
		Type:          engine.EventTombstone,
		TombstoneUUID: "attempt-1",
		ModelAttempt:  first,
	})
	for _, item := range app.chat.items {
		switch typed := item.(type) {
		case *AssistantMessage:
			if typed.attemptID == "attempt-1" {
				t.Fatal("failed assistant attempt remained visible")
			}
		case *ThinkingMessage:
			if typed.attemptID == "attempt-1" {
				t.Fatal("failed reasoning attempt remained visible")
			}
		case *ToolMessage:
			if typed.attemptID == "attempt-1" {
				t.Fatal("failed tool attempt remained visible")
			}
		}
	}
	if _, exists := app.chat.toolsByID["failed-tool"]; exists {
		t.Fatal("failed tool remained indexed after tombstone")
	}
	if _, exists := app.activeTools["failed-tool"]; exists {
		t.Fatal("failed tool remained active after tombstone")
	}
	if app.toolProgress.Count() != 0 {
		t.Fatal("failed tool remained in progress display after tombstone")
	}

	second := &engine.ModelAttemptEvent{AttemptID: "attempt-2"}
	app.handleEngineEvent(engine.QueryEvent{
		Type:         engine.EventAssistant,
		ModelAttempt: second,
		Message: &schema.Message{
			Role:    schema.Assistant,
			Content: "second committed",
		},
	})
	if len(app.chat.items) != 1 {
		t.Fatalf("visible items = %d, want 1", len(app.chat.items))
	}
	assistant, ok := app.chat.items[0].(*AssistantMessage)
	if !ok ||
		assistant.attemptID != "attempt-2" ||
		assistant.content != "second committed" {
		t.Fatalf("next attempt projection = %#v", app.chat.items[0])
	}
}

func TestP462TUIRetractsBeforeOneBoundedFallbackWarning(t *testing.T) {
	app := newTestApp(80, 24)
	first := &engine.ModelAttemptEvent{AttemptID: "attempt-1"}
	app.handleEngineEvent(engine.QueryEvent{
		Type:         engine.EventAssistant,
		ModelAttempt: first,
		Message: &schema.Message{
			Role: schema.Assistant, Content: "first partial",
		},
	})
	app.handleEngineEvent(engine.QueryEvent{
		Type: engine.EventModelAttempt,
		ModelAttempt: &engine.ModelAttemptEvent{
			AttemptID:         "attempt-1",
			AttemptIndex:      0,
			Phase:             engine.ModelAttemptDiscarded,
			FailureClass:      "overloaded",
			OutputDisposition: engine.ModelAttemptOutputDiscarded,
		},
	})
	if active := app.notifications.Active(); len(active) != 0 {
		t.Fatalf("discarded event created presentation = %#v", active)
	}
	app.handleEngineEvent(engine.QueryEvent{
		Type:          engine.EventTombstone,
		TombstoneUUID: "attempt-1",
		ModelAttempt:  first,
	})
	if len(app.chat.items) != 0 {
		t.Fatalf("tombstoned attempt remained = %#v", app.chat.items)
	}
	if active := app.notifications.Active(); len(active) != 0 {
		t.Fatalf("tombstone created fallback presentation = %#v", active)
	}

	app.handleEngineEvent(engine.QueryEvent{
		Type: engine.EventModelAttempt,
		ModelAttempt: &engine.ModelAttemptEvent{
			AttemptID:    "attempt-2",
			AttemptIndex: 1,
			Profile:      "fallback.profile",
			Phase:        engine.ModelAttemptStarted,
			SwitchCount:  1,
		},
	})
	active := app.notifications.Active()
	if len(active) != 1 ||
		active[0].Severity != NotifyWarning ||
		active[0].Message != "Model fallback: profile fallback.profile after overload (switch 1)" {
		t.Fatalf("fallback notification = %#v", active)
	}
	if len(app.chat.items) != 0 {
		t.Fatalf("fallback notice entered chat = %#v", app.chat.items)
	}
	for _, width := range []int{40, 80, 120, 180} {
		if line := app.notifications.RenderSingleLine(app.styles, width); app.renderEnvironment.profile.width(line) > width {
			t.Fatalf("width=%d fallback line=%q", width, line)
		}
	}
}

func TestP462TUIRejectsUnsafeOrNonFallbackAttemptNotices(t *testing.T) {
	const secret = "secret-control"
	for _, attempt := range []*engine.ModelAttemptEvent{
		nil,
		{AttemptID: "primary", Profile: "primary", Phase: engine.ModelAttemptStarted},
		{AttemptID: "discarded", AttemptIndex: 1, Profile: "safe", Phase: engine.ModelAttemptDiscarded, SwitchCount: 1},
		{AttemptID: "missing-switch", AttemptIndex: 1, Profile: "safe", Phase: engine.ModelAttemptStarted},
		{AttemptID: "unsafe", AttemptIndex: 1, Profile: "safe\n" + secret, Phase: engine.ModelAttemptStarted, SwitchCount: 1},
	} {
		app := newTestApp(80, 24)
		app.handleEngineEvent(engine.QueryEvent{
			Type: engine.EventModelAttempt, ModelAttempt: attempt,
		})
		active := app.notifications.Active()
		if len(active) != 0 {
			t.Fatalf("attempt %#v created notice %#v", attempt, active)
		}
		if strings.Contains(app.activeToast(), secret) {
			t.Fatalf("unsafe profile escaped: %q", app.activeToast())
		}
	}
}

func TestP294TombstoneKeepsSameToolIDOwnedByAnotherAttempt(
	t *testing.T,
) {
	app := newTestApp(80, 24)
	first := &engine.ModelAttemptEvent{AttemptID: "attempt-1"}
	second := &engine.ModelAttemptEvent{AttemptID: "attempt-2"}
	toolMessage := func(arguments string) *schema.Message {
		return &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID: "shared-tool",
				Function: schema.FunctionCall{
					Name:      "Bash",
					Arguments: arguments,
				},
			}},
		}
	}
	app.handleEngineEvent(engine.QueryEvent{
		Type:         engine.EventAssistant,
		ModelAttempt: first,
		Message:      toolMessage(`{"command":"first"}`),
	})
	app.handleEngineEvent(engine.QueryEvent{
		Type:         engine.EventAssistant,
		ModelAttempt: second,
		Message:      toolMessage(`{"command":"second"}`),
	})

	app.handleEngineEvent(engine.QueryEvent{
		Type:          engine.EventTombstone,
		TombstoneUUID: first.AttemptID,
		ModelAttempt:  first,
	})

	tool, exists := app.chat.toolsByID["shared-tool"]
	if !exists || tool.attemptID != second.AttemptID {
		t.Fatalf("new attempt tool index = %#v", tool)
	}
	active, exists := app.activeTools["shared-tool"]
	if !exists || active.attemptID != second.AttemptID {
		t.Fatalf("new attempt active tool = %#v", active)
	}
	progress, exists := app.toolProgress.entries["shared-tool"]
	if !exists || progress.AttemptID != second.AttemptID {
		t.Fatalf("new attempt progress = %#v", progress)
	}
	for _, item := range app.chat.items {
		if toolItem, ok := item.(*ToolMessage); ok &&
			toolItem.attemptID == first.AttemptID {
			t.Fatal("old attempt tool remained visible")
		}
	}
}
