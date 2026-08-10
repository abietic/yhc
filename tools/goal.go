package tools

import (
	"fmt"

	"github.com/cloudwego/eino/schema"
)

const (
	GetGoalToolName    = "get_goal"
	UpdateGoalToolName = "update_goal"
)

// GetGoalTool declares the engine-owned Goal inspection contract. QueryEngine
// intercepts execution so a shared registry never captures parent authority.
func GetGoalTool() ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: GetGoalToolName,
			Desc: "Read the current durable Goal status, objective, token budget, provider-reported usage, and blocker progress. This tool cannot create or mutate a Goal.",
			ParamsOneOf: schema.NewParamsOneOfByParams(
				map[string]*schema.ParameterInfo{},
			),
		},
		Execute: func(string) (string, error) {
			return "", fmt.Errorf("get_goal requires QueryEngine ownership")
		},
		RequiresQueryEngine: true,
	}
}

// UpdateGoalTool declares the two model-owned evidence transitions. It cannot
// create, edit, pause, resume, clear, or budget a Goal.
func UpdateGoalTool() ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: UpdateGoalToolName,
			Desc: "Report evidence for the active Goal turn. Use status complete only when the objective is genuinely finished. Use blocked with a stable blocker_key and reason only when the same external blocker prevents meaningful progress; three distinct Goal turns are required before the Goal becomes blocked.",
			ParamsOneOf: schema.NewParamsOneOfByParams(
				map[string]*schema.ParameterInfo{
					"status": {
						Type:     schema.String,
						Desc:     "Evidence status: complete or blocked",
						Required: true,
					},
					"reason": {
						Type: schema.String,
						Desc: "Concise evidence or blocker explanation; required for blocked",
					},
					"blocker_key": {
						Type: schema.String,
						Desc: "Stable machine-comparable blocker identity; required for blocked",
					},
				},
			),
		},
		Execute: func(string) (string, error) {
			return "", fmt.Errorf("update_goal requires QueryEngine ownership")
		},
		RequiresQueryEngine: true,
	}
}
