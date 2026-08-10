package engine

import "testing"

func newTestRuntimeInputCoordinator(
	t *testing.T,
	sessionID string,
	agentID string,
) *RuntimeInputCoordinator {
	t.Helper()
	coordinator, err := NewRuntimeInputCoordinator(RuntimeInputCoordinatorConfig{
		SessionID: sessionID,
		AgentID:   agentID,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}

func mustEnqueueRuntimePrompt(
	t *testing.T,
	coordinator *RuntimeInputCoordinator,
	id string,
	priority RuntimeInputPriority,
	agentID string,
	prompt string,
) {
	t.Helper()
	_, err := coordinator.Enqueue(RuntimeItem{
		ID:       id,
		Kind:     RuntimeItemSteering,
		Priority: priority,
		Scope: RuntimeInputScope{
			SessionID: coordinator.scope.SessionID,
			ThreadID:  coordinator.scope.ThreadID,
			AgentID:   agentID,
		},
		Origin: "sdk",
		UserPrompt: &RuntimeUserPrompt{
			Prompt: prompt,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}
