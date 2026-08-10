package tools

import (
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/schema"
)

// SyntheticOutputTool returns a tool for producing structured SDK output
// without model generation. Used by SDK control requests that need to
// inject tool results directly into the conversation.
//
// Reference: src/tools/SyntheticOutputTool/SyntheticOutputTool.ts (163 lines)
func SyntheticOutputTool() ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: "SyntheticOutput",
			Desc: "Produces structured output for SDK control requests. Not typically invoked by the model directly.",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"content": {
					Type:     "string",
					Desc:     "The content to output",
					Required: true,
				},
				"format": {
					Type: "string",
					Desc: "Output format: text, json, or markdown",
				},
			}),
		},
		IsReadOnly: true,
		IsHidden:   true,
		Execute: func(input string) (string, error) {
			var params struct {
				Content string `json:"content"`
				Format  string `json:"format"`
			}
			if err := json.Unmarshal([]byte(input), &params); err != nil {
				return "", fmt.Errorf("synthetic_output: %w", err)
			}
			if params.Content == "" {
				return "", fmt.Errorf("synthetic_output: content is required")
			}
			return params.Content, nil
		},
	}
}
