package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
)

// DefaultRegistry is set during initialization to allow tool search to access
// the available tools. Set by RegisterDefaults when creating the registry.
var DefaultRegistry *Registry

// ToolSearchTool returns a tool that searches available tools by name or description keyword.
// This is useful when the model needs to find the right tool for a task.
func ToolSearchTool() ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: "ToolSearch",
			Desc: "Searches available tools by name or description keyword. Use when you need to find the right tool for a task.",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"query": {Type: "string", Desc: "Search query to match against tool names and descriptions", Required: true},
			}),
		},
		Execute: executeToolSearch,
		IsConcurrencySafe: func(input map[string]any) bool {
			return true
		},
	}
}

func executeToolSearch(input string) (string, error) {
	var params struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("tool_search: invalid params: %w", err)
	}
	if params.Query == "" {
		return "", fmt.Errorf("tool_search: query parameter is required")
	}

	if DefaultRegistry == nil {
		return "No tool registry available. Tools have not been initialized yet.", nil
	}

	query := strings.ToLower(params.Query)
	toolInfos := DefaultRegistry.List()

	var matches []string
	for _, info := range toolInfos {
		if info == nil {
			continue
		}
		implementation, registered := DefaultRegistry.Get(info.Name)
		if !registered ||
			implementation.IsHidden ||
			implementation.RequiresQueryEngine {
			continue
		}
		nameLower := strings.ToLower(info.Name)
		descLower := strings.ToLower(info.Desc)

		if strings.Contains(nameLower, query) || strings.Contains(descLower, query) {
			desc := info.Desc
			if len(desc) > 200 {
				desc = desc[:200] + "..."
			}
			matches = append(matches, fmt.Sprintf("Name: %s\nDescription: %s\n", info.Name, desc))
		}

		if len(matches) >= 10 {
			break
		}
	}

	if len(matches) == 0 {
		return fmt.Sprintf("No tools found matching %q. Try a broader search term, or use a single keyword like 'file', 'search', 'web', 'task', or 'edit'.", params.Query), nil
	}

	return strings.Join(matches, "\n"), nil
}
