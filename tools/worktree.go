package tools

import (
	"fmt"

	"github.com/cloudwego/eino/schema"
)

// EnterWorktreeTool retains source compatibility for the removed top-level
// worktree tool. The returned implementation always fails before side effects.
//
// Deprecated: top-level session worktree switching is unavailable. Use Agent
// with isolation="worktree".
func EnterWorktreeTool() ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: "EnterWorktree",
			Desc: unavailableWorktreeToolReason,
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"branch": {Type: schema.String, Desc: "The branch name to create/checkout in the worktree", Required: true},
				"base":   {Type: schema.String, Desc: "The base ref to branch from (defaults to HEAD)"},
			}),
		},
		Execute: unavailableWorktreeExecution("EnterWorktree"),
	}
}

// ExitWorktreeTool retains source compatibility for the removed top-level
// worktree tool. The returned implementation always fails before side effects.
//
// Deprecated: top-level session worktree switching is unavailable. Use Agent
// with isolation="worktree".
func ExitWorktreeTool() ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: "ExitWorktree",
			Desc: unavailableWorktreeToolReason,
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"remove": {Type: schema.Boolean, Desc: "Whether to remove the worktree after exiting (default: false)"},
			}),
		},
		Execute: unavailableWorktreeExecution("ExitWorktree"),
	}
}

func unavailableWorktreeExecution(name string) func(string) (string, error) {
	return func(string) (string, error) {
		reason, _ := UnavailableBuiltinToolReason(name)
		return "", fmt.Errorf("%s unavailable: %s", name, reason)
	}
}
