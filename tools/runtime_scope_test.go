package tools

import (
	"context"
	"strings"
	"sync"
	"testing"
)

func TestContextScopedSendMessageUsesInjectedRunner(t *testing.T) {
	injected := NewAgentRunner(1)
	other := NewAgentRunner(1)
	injectedAgent := &RunningAgent{ID: "injected", Status: "running", mu: &sync.Mutex{}}
	otherAgent := &RunningAgent{ID: "other", Status: "running", mu: &sync.Mutex{}}
	injected.activeAgents[injectedAgent.ID] = injectedAgent
	other.activeAgents[otherAgent.ID] = otherAgent

	tool := SendMessageTool()
	ctx := WithTaskManager(
		WithAgentRunner(context.Background(), injected),
		NewTaskManager(),
	)
	result, err := tool.ExecuteCtx(ctx, `{
		"to":"injected","message":"continue","summary":"Continue work"
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if result == "" || len(injectedAgent.PendingMessages) != 1 {
		t.Fatalf("injected runner did not receive message: result=%q pending=%#v", result, injectedAgent.PendingMessages)
	}
	if len(otherAgent.PendingMessages) != 0 {
		t.Fatalf("default runner received scoped message: %#v", otherAgent.PendingMessages)
	}
}

func TestContextScopedTaskStopUsesInjectedRunner(t *testing.T) {
	injected := NewAgentRunner(1)
	other := NewAgentRunner(1)
	_, cancelInjected := context.WithCancel(context.Background())
	_, cancelOther := context.WithCancel(context.Background())
	injectedAgent := &RunningAgent{ID: "injected", Status: "running", cancel: cancelInjected, mu: &sync.Mutex{}}
	otherAgent := &RunningAgent{ID: "other", Status: "running", cancel: cancelOther, mu: &sync.Mutex{}}
	injected.activeAgents[injectedAgent.ID] = injectedAgent
	other.activeAgents[injectedAgent.ID] = otherAgent

	t.Cleanup(cancelOther)

	tool := TaskStopTool()
	ctx := WithTaskManager(
		WithAgentRunner(context.Background(), injected),
		NewTaskManager(),
	)
	if _, err := tool.ExecuteCtx(ctx, `{"task_id":"injected"}`); err != nil {
		t.Fatal(err)
	}
	if injectedAgent.Status != "aborted" {
		t.Fatalf("injected Agent status = %q, want aborted", injectedAgent.Status)
	}
	if otherAgent.Status != "running" {
		t.Fatalf("default runner Agent status = %q, want running", otherAgent.Status)
	}
}

func TestContextScopedTaskToolsUseInjectedManager(t *testing.T) {
	injected := NewTaskManager()
	other := NewTaskManager()

	ctx := WithTaskManager(context.Background(), injected)
	createResult, err := TaskCreateTool().ExecuteCtx(
		ctx,
		`{"subject":"Scoped task","description":"private"}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	if createResult == "" || len(injected.List()) != 1 {
		t.Fatalf("injected manager create = %q tasks=%#v", createResult, injected.List())
	}
	if len(other.List()) != 0 {
		t.Fatalf("independent manager received scoped task: %#v", other.List())
	}

	listResult, err := TaskTool().ExecuteCtx(ctx, `{"action":"list"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(listResult, "Scoped task") {
		t.Fatalf("combined Task tool ignored scoped manager: %q", listResult)
	}

	if _, err := TaskStopTool().ExecuteCtx(ctx, `{"task_id":"1"}`); err != nil {
		t.Fatal(err)
	}
	task, _ := injected.Get("1")
	if task.Status != TaskStatusKilled {
		t.Fatalf("scoped task status = %q, want killed", task.Status)
	}
}
