package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/cloudwego/eino/schema"
)

// BriefTool returns a tool that sends a structured brief/status message to the user.
// Optionally includes content from file attachments.
// Mirrors the reference BriefTool — used for proactive status updates or normal replies.
func BriefTool() ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: "Brief",
			Desc: "Send a brief message to the user with optional file attachments. " +
				"Use this tool for proactive status updates, progress reports, or " +
				"when you want to communicate results alongside referenced files.",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"content":     {Type: schema.String, Desc: "The markdown message content to display to the user", Required: true},
				"attachments": {Type: schema.Array, Desc: "Optional array of file paths whose contents should be appended to the message"},
			}),
		},
		IsConcurrencySafe: func(input map[string]any) bool {
			return true
		},
		Execute:    executeBrief,
		ExecuteCtx: executeBriefAt,
	}
}

func executeBrief(input string) (string, error) {
	return executeBriefAt(context.Background(), input)
}

func executeBriefAt(ctx context.Context, input string) (string, error) {
	var params struct {
		Content     string   `json:"content"`
		Attachments []string `json:"attachments"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("invalid Brief input: %w", err)
	}
	if params.Content == "" {
		return "", fmt.Errorf("content is required")
	}

	var b strings.Builder
	b.WriteString(params.Content)

	for _, filePath := range params.Attachments {
		resolved, resolveErr := resolveExecutionPath(ctx, filePath)
		if resolveErr != nil {
			fmt.Fprintf(&b, "\n\n--- %s ---\n[Error resolving file: %v]", filePath, resolveErr)
			continue
		}
		data, err := os.ReadFile(resolved)
		if err != nil {
			fmt.Fprintf(&b, "\n\n--- %s ---\n[Error reading file: %v]", filePath, err)
			continue
		}
		fmt.Fprintf(&b, "\n\n--- %s ---\n%s", filePath, string(data))
	}

	return b.String(), nil
}
