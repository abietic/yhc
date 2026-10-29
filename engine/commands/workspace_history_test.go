package commands

import (
	"context"
	"testing"
)

type historyCommandCapability struct{}

func (historyCommandCapability) WorkspaceHistoryAvailability() (bool, string) { return true, "" }

func TestWorkspaceHistoryCommandsAreExplicitReadOnlyViews(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)
	ctx := &CommandContext{Engine: historyCommandCapability{}, Environment: CommandEnvironment{Phase: CommandPhaseActiveTurn}, Extra: map[string]any{"leader_view": true}}
	for name, action := range map[string]CommandAction{"changes": "workspace_changes", "recap": "execution_recap"} {
		description, ok := r.DescribeOperation(context.Background(), EntrypointTUI, ctx, name, nil)
		if !ok || description.Command.Availability != AvailabilitySupported || description.Command.ModelInvocation != ModelInvocationNone || description.Command.SideEffect != SideEffectNone {
			t.Fatalf("read-only metadata for %s: %+v", name, description)
		}
		result, err := r.Dispatch(context.Background(), EntrypointTUI, ctx, "/"+name)
		if err != nil || result.Action != action {
			t.Fatalf("dispatch %s: %+v %v", name, result, err)
		}
		if _, ok := r.DescribeOperation(context.Background(), EntrypointPlain, ctx, name, nil); ok {
			t.Fatalf("unwired plain entrypoint advertised %s", name)
		}
		if _, err := r.Dispatch(context.Background(), EntrypointTUI, ctx, "/"+name+" extra"); err == nil {
			t.Fatal("unexpected arguments were ignored")
		}
	}
	if r.GetRemoved("summary") == nil {
		t.Fatal("factual recap revived the removed prompt workflow")
	}
}
