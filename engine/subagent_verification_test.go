package engine

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/schema"
)

func TestVerificationAgentCapabilityContract(t *testing.T) {
	registry := tools.NewRegistry()
	var bashCalls atomic.Int32
	var writeCalls atomic.Int32
	for _, name := range []string{"Read", "Glob", "Grep", "WebFetch", "WebSearch"} {
		registry.Register(tools.ToolImpl{Info: &schema.ToolInfo{Name: name}, IsReadOnly: true})
	}
	registry.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{Name: "Bash"},
		ExecuteCtx: func(context.Context, string) (string, error) {
			bashCalls.Add(1)
			return "verification command ran", nil
		},
	})
	registry.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{Name: "Write"},
		ExecuteCtx: func(context.Context, string) (string, error) {
			writeCalls.Add(1)
			return "unexpected write", nil
		},
	})

	mdl := &p170ToolSequenceModel{first: []schema.ToolCall{{
		ID: "verification-bash", Type: "function",
		Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"go test ./..."}`},
	}}}
	executor := NewSubAgentExecutor(mdl, registry, t.TempDir())

	if got, want := poolToolNames(executor.buildScopedTools(nil, "verification")), []string{"Read", "Glob", "Grep", "WebFetch", "WebSearch", "Bash"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("verification default tools = %q, want %q", got, want)
	}
	if got, want := poolToolNames(executor.buildScopedTools([]string{"Bash", "Read", "Write"}, "verification")), []string{"Read", "Bash"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("verification caller-scoped tools = %q, want %q", got, want)
	}

	result, err := executor.ExecuteAgent(context.Background(), tools.AgentExecOptions{
		Task:         "original task, changed files, and approach",
		SubagentType: "verification",
		AllowedTools: []string{"Bash", "Write"},
	})
	if err != nil {
		t.Fatalf("run verification child: %v", err)
	}
	if result == nil || result.Result != "done" {
		t.Fatalf("verification result = %#v", result)
	}
	if bashCalls.Load() != 1 {
		t.Fatalf("Bash calls = %d, want 1", bashCalls.Load())
	}
	if writeCalls.Load() != 0 {
		t.Fatalf("Write calls = %d, want 0", writeCalls.Load())
	}

	snapshots := mdl.toolSnapshots()
	if len(snapshots) == 0 {
		t.Fatal("child model received no bound tools")
	}
	for _, names := range snapshots {
		if !slices.Equal(names, []string{"Bash"}) {
			t.Fatalf("provider-visible tools = %q, want only Bash", names)
		}
	}
	deniedModel := &p170ToolSequenceModel{first: []schema.ToolCall{{
		ID: "verification-denied-bash", Type: "function",
		Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"go test ./..."}`},
	}}}
	deniedExecutor := NewSubAgentExecutor(deniedModel, registry, t.TempDir())

	deniedExecutor.ParentCanUseTool = func(context.Context, string, map[string]any, *ToolUseContext) (bool, string) {
		return false, "parent denied verification command"
	}
	result, err = deniedExecutor.ExecuteAgent(context.Background(), tools.AgentExecOptions{
		Task:         "original task, changed files, and approach",
		SubagentType: "verification",
		AllowedTools: []string{"Bash"},
	})
	if err != nil {
		t.Fatalf("run denied verification child: %v", err)
	}
	if result == nil {
		t.Fatalf("denied verification result = %#v", result)
	}
	denied := false
	for _, message := range result.Messages {
		if message.Role == schema.Tool && strings.Contains(message.Content, "parent denied verification command") {
			denied = true
		}
	}
	if !denied {
		t.Fatal("child did not receive the inherited permission denial")
	}
	if bashCalls.Load() != 1 {
		t.Fatalf("denied Bash call ran: calls = %d, want 1", bashCalls.Load())
	}
}
