package commands

import (
	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/engine/skills"
	"github.com/abietic/yhc/tools"
)

// RuntimeInspectionSnapshot is the single engine-owned read model for
// orchestration and extension inspection commands.
type RuntimeInspectionSnapshot struct {
	Tasks            tools.RuntimeTaskSnapshot
	TaskExplorer     TaskExplorerInspectionSnapshot
	AgentDefinitions map[string]AgentInfo
	AgentDiagnostic  string
	Skills           skills.Snapshot
	MCP              tools.MCPInventorySnapshot
	Hooks            *hooks.ShellHookConfig
	PromptCommands   PromptCommandGenerationSnapshot
}

// TaskExplorerInspectionSnapshot is the command-safe copy of the canonical
// engine TaskExplorerSnapshot. The engine performs no independent selection
// or ordering when adapting these bounded rows.
type TaskExplorerInspectionSnapshot struct {
	Available         bool
	UnavailableReason string
	SessionID         string
	BoardID           string
	BoardRevision     uint64
	RuntimeRevision   uint64
	WorkItems         []TaskExplorerInspectionWorkItem
	Executions        []TaskExplorerInspectionExecution
	Links             []TaskExplorerInspectionLink
	Hidden            TaskExplorerInspectionHidden
}

type TaskExplorerInspectionWorkItem struct {
	WorkItemID    string
	Status        string
	Title         string
	Description   string
	ActiveForm    string
	Owner         string
	ResultSummary string
}

type TaskExplorerInspectionExecution struct {
	AgentID     string
	Generation  int64
	Status      string
	Phase       string
	Name        string
	Task        string
	Description string
	Activity    string
	ReplayOnly  bool
}

type TaskExplorerInspectionLink struct {
	WorkItemID        string
	AgentID           string
	Generation        int64
	State             string
	UnavailableReason string
}

type TaskExplorerInspectionHidden struct {
	WorkItems                   map[string]int
	Executions                  map[string]int
	Links                       int
	Attention                   map[string]int
	WorkBoardOutsidePrimary     int
	RuntimeEventsDropped        uint64
	ExecutionGenerationsEvicted uint64
	HiddenLiveExecutions        uint64
}

// RuntimeInspectionProvider is implemented by the runtime that owns the
// inspected state. Commands never fall back to package-global managers.
type RuntimeInspectionProvider interface {
	RuntimeInspectionSnapshot() RuntimeInspectionSnapshot
}

func runtimeInspectionSnapshot(
	ctx *CommandContext,
) (RuntimeInspectionSnapshot, bool) {
	if ctx == nil || ctx.Engine == nil {
		return RuntimeInspectionSnapshot{}, false
	}
	provider, ok := ctx.Engine.(RuntimeInspectionProvider)
	if !ok || provider == nil {
		return RuntimeInspectionSnapshot{}, false
	}
	return provider.RuntimeInspectionSnapshot(), true
}
