package engine

import (
	"context"
	"reflect"
	"testing"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/mcp"
	"github.com/abietic/yhc/tools"
)

func TestCompleteCommandInputReadsDynamicInventoryWithoutProviderOrRuntimeEffects(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	probe := &promptSuggestionProbeModel{}
	manager := tools.NewMCPInspectionManager(&mcp.MCPConfig{Servers: map[string]*mcp.MCPServerConfig{"local-fixture": {Enabled: true}}})
	eng := NewQueryEngine(QueryEngineConfig{
		CWD: t.TempDir(), TranscriptDir: t.TempDir(), Model: "primary",
		ModelResolver: newP292InventoryResolver(200000), ChatModel: probe,
		MCPManager: manager, CommandEntrypoint: commands.EntrypointAppServer,
	})
	t.Cleanup(eng.Close)
	if got := eng.CompleteCommandInput(context.Background(), commandArgumentRequest("/mcp local")); len(got.Candidates) != 0 {
		t.Fatal("TUI-only MCP command offered through app-server")
	}
	// Exercise the allowed inventory source without expanding the built-in
	// MCP command's entrypoint capability set.
	if err := eng.GetCommandRegistry().Register(&commands.Command{
		Name: "inspect-mcp", Entrypoints: commands.EntrypointsACP,
		Completion: []commands.ArgumentCompletion{{Source: "mcp"}},
		Execute: func(context.Context, *commands.CommandContext) (*commands.CommandResult, error) {
			t.Fatal("completion executed the inspection command")
			return nil, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	before := eng.RuntimeSnapshot()
	for input, want := range map[string]string{"/model prim": "primary ", "/effort h": "high ", "/inspect-mcp local": "local-fixture "} {
		got := eng.CompleteCommandInput(context.Background(), commandArgumentRequest(input))
		if len(got.Candidates) != 1 || got.Candidates[0].Replacement != want {
			t.Fatalf("%q = %+v; want %q", input, got, want)
		}
	}
	if calls, _, _, _, _, generates := probe.snapshot(); calls != 0 || generates != 0 {
		t.Fatal("completion invoked the model")
	}
	if len(eng.GetMessages()) != 0 || !reflect.DeepEqual(before, eng.RuntimeSnapshot()) || eng.GetModelName() != "primary" || eng.ReasoningEffort() != "" {
		t.Fatal("completion mutated conversation, runtime or execution settings")
	}
	eng.planMu.Lock()
	eng.planActiveTurnID = "active-test-turn"
	eng.planMu.Unlock()
	if got := eng.CompleteCommandInput(context.Background(), commandArgumentRequest("/mod")); len(got.Candidates) != 0 {
		t.Fatal("active engine offered candidates")
	}
	eng.planMu.Lock()
	eng.planActiveTurnID = ""
	eng.planState.Phase = PlanPhaseAwaitingApproval
	eng.planMu.Unlock()
	if got := eng.CompleteCommandInput(context.Background(), commandArgumentRequest("/mod")); len(got.Candidates) != 0 {
		t.Fatal("pending plan decision offered candidates")
	}
}
