package tools

import (
	"context"
	"reflect"
	"testing"
)

func TestTodoWriteUsesTrustedSessionAndAgentScope(t *testing.T) {
	tool := TodoWriteTool()
	if tool.ExecuteCtx == nil || tool.Execute != nil {
		t.Fatal("TodoWrite must use context-aware execution only")
	}

	rootSession := "todo-root-session"
	otherSession := "todo-other-session"
	agentID := "todo-child-agent"
	authority := NewEphemeralTodoAuthority()

	rootItems := []TodoItem{{
		Content:    "Verify root scope",
		Status:     "in_progress",
		ActiveForm: "Verifying root scope",
	}}
	rootCtx := WithNonSessionLogicalWorkScope(
		context.Background(),
		rootSession,
	)
	rootCtx = WithLogicalWorkAuthority(rootCtx, authority)
	if _, err := tool.ExecuteCtx(
		rootCtx,
		`{"session_id":"forged-session","todos":[{"content":"Verify root scope","status":"in_progress","activeForm":"Verifying root scope"}]}`,
	); err != nil {
		t.Fatal(err)
	}
	got, err := authority.Todos(TodoScope{SessionID: rootSession})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, rootItems) {
		t.Fatalf("root todos = %#v, want %#v", got, rootItems)
	}
	got, err = authority.Todos(TodoScope{SessionID: "forged-session"})
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("model-supplied session_id selected state: %#v", got)
	}

	childItems := []TodoItem{{
		Content:    "Verify child scope",
		Status:     "pending",
		ActiveForm: "Verifying child scope",
	}}
	childCtx := WithAgentID(
		rootCtx,
		agentID,
	)
	if _, err := tool.ExecuteCtx(
		childCtx,
		`{"todos":[{"content":"Verify child scope","status":"pending","activeForm":"Verifying child scope"}]}`,
	); err != nil {
		t.Fatal(err)
	}
	got, err = authority.Todos(TodoScope{
		SessionID: rootSession,
		AgentID:   agentID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, childItems) {
		t.Fatalf("child todos = %#v, want %#v", got, childItems)
	}
	got, err = authority.Todos(TodoScope{SessionID: rootSession})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, rootItems) {
		t.Fatalf("child write changed root todos: %#v", got)
	}
	got, err = authority.Todos(TodoScope{
		SessionID: otherSession,
		AgentID:   agentID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("child todos crossed Session boundary: %#v", got)
	}

	if _, err := tool.ExecuteCtx(
		childCtx,
		`{"todos":[{"content":"Verify child scope","status":"completed","activeForm":"Verifying child scope"}]}`,
	); err != nil {
		t.Fatal(err)
	}
	got, err = authority.Todos(TodoScope{
		SessionID: rootSession,
		AgentID:   agentID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("completed child list was not cleared: %#v", got)
	}
}
