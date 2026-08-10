package commands

import (
	"fmt"
	"sort"
	"strings"

	"github.com/abietic/yhc/tools"
)

// AgentInfo is a local representation of an active agent definition,
// used to avoid importing the engine package (which would cause a cycle).
type AgentInfo struct {
	Name            string
	WhenToUse       string
	Tools           []string
	DisallowedTools []string
	MaxTurns        int
	ReadOnly        bool
	Source          string
	FilePath        string
}

// executeAgents implements /agents — list active built-in and custom agent types.
// Mirrors reference commands/agents.tsx.
//
// Usage:
//
//	/agents            → list all agent types with descriptions
//	/agents <name>     → show details for a specific agent type
func executeAgents(ctx *CommandContext, args string) (*CommandResult, error) {
	snapshot, ok := runtimeInspectionSnapshot(ctx)
	if !ok {
		return &CommandResult{
			Output: "Agent inspection is unavailable for this runtime.",
		}, nil
	}
	defs := snapshot.AgentDefinitions

	if args != "" {
		// Show specific agent.
		name := strings.TrimSpace(args)
		for _, active := range snapshot.Tasks.Agents {
			if strings.EqualFold(active.ID, name) ||
				strings.EqualFold(active.Name, name) {
				return &CommandResult{
					Output: formatActiveAgent(active),
				}, nil
			}
		}
		def, ok := defs[name]
		if !ok {
			// Case-insensitive lookup.
			for k, v := range defs {
				if strings.EqualFold(k, name) {
					def = v
					ok = true
					break
				}
			}
		}
		if !ok {
			names := sortedAgentKeys(defs)
			return &CommandResult{
				Output: fmt.Sprintf("Unknown agent type %q.\n\nAvailable: %s", name, strings.Join(names, ", ")),
			}, nil
		}

		var sb strings.Builder
		fmt.Fprintf(&sb, "Agent: %s\n\n", def.Name)
		fmt.Fprintf(&sb, "When to use:\n  %s\n\n", def.WhenToUse)
		if len(def.Tools) > 0 {
			fmt.Fprintf(&sb, "Tools: %s\n", strings.Join(def.Tools, ", "))
		} else {
			sb.WriteString("Tools: all (minus disallowed)\n")
		}
		if len(def.DisallowedTools) > 0 {
			fmt.Fprintf(&sb, "Disallowed: %s\n", strings.Join(def.DisallowedTools, ", "))
		}
		fmt.Fprintf(&sb, "Max turns: %d\n", def.MaxTurns)
		if def.Source != "" {
			fmt.Fprintf(&sb, "Source: %s\n", def.Source)
		}
		if def.FilePath != "" {
			fmt.Fprintf(&sb, "File: %s\n", def.FilePath)
		}
		if def.ReadOnly {
			sb.WriteString("Access: read-only\n")
		}
		return &CommandResult{Output: sb.String()}, nil
	}

	// List all active agents and available definitions from one engine-owned
	// inspection snapshot.
	names := sortedAgentKeys(defs)

	var sb strings.Builder
	if snapshot.AgentDiagnostic == "" {
		sb.WriteString("Agent inspection health: healthy\n\n")
	} else {
		fmt.Fprintf(
			&sb,
			"Agent definition health: unavailable (%s)\n\n",
			snapshot.AgentDiagnostic,
		)
	}
	fmt.Fprintf(&sb, "Runtime agents (%d):\n", len(snapshot.Tasks.Agents))
	if len(snapshot.Tasks.Agents) == 0 {
		sb.WriteString("  none\n")
	} else {
		for _, active := range snapshot.Tasks.Agents {
			label := firstNonEmpty(active.Description, active.Task, active.Name, "agent")
			fmt.Fprintf(
				&sb,
				"  %-20s %-12s %s\n",
				active.ID,
				active.Status,
				truncateTaskText(label, 100),
			)
		}
	}
	sb.WriteString("\n")
	fmt.Fprintf(&sb, "Available agent types (%d):\n\n", len(names))
	for _, name := range names {
		def := defs[name]
		desc := def.WhenToUse
		// Truncate long descriptions for listing.
		if len(desc) > 100 {
			desc = desc[:97] + "..."
		}
		readOnly := ""
		if def.ReadOnly {
			readOnly = " [read-only]"
		}
		source := ""
		if def.Source != "" && def.Source != "built-in" {
			source = " [" + def.Source + "]"
		}
		fmt.Fprintf(&sb, "  %-20s %s%s%s\n", name, desc, readOnly, source)
	}
	sb.WriteString("\nDetails: /agents <runtime-id|type>")
	return &CommandResult{Output: sb.String()}, nil
}

func formatActiveAgent(agent tools.RuntimeAgentSnapshot) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Runtime agent: %s\n", agent.ID)
	if agent.Name != "" {
		fmt.Fprintf(&sb, "Name: %s\n", agent.Name)
	}
	if agent.Type != "" {
		fmt.Fprintf(&sb, "Type: %s\n", agent.Type)
	}
	fmt.Fprintf(&sb, "Status: %s\n", agent.Status)
	if agent.Description != "" {
		fmt.Fprintf(&sb, "Description: %s\n", agent.Description)
	}
	if agent.Task != "" {
		fmt.Fprintf(&sb, "Task: %s\n", agent.Task)
	}
	if agent.LastToolName != "" {
		fmt.Fprintf(&sb, "Last tool: %s\n", agent.LastToolName)
	}
	if agent.Summary != "" {
		fmt.Fprintf(&sb, "Summary: %s\n", agent.Summary)
	}
	fmt.Fprintf(&sb, "Tool uses: %d\n", agent.ToolUseCount)
	fmt.Fprintf(&sb, "Tokens: %d\n", agent.TokenCount)
	return strings.TrimRight(sb.String(), "\n")
}

func sortedAgentKeys(m map[string]AgentInfo) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ActionAgentCreate signals the TUI to open the agent creation wizard.
const ActionAgentCreate CommandAction = "agent_create"

// ActionAgentEdit signals the TUI to open the agent edit wizard.
const ActionAgentEdit CommandAction = "agent_edit"

// executeAgent implements /agent — manage custom agent definitions.
//
// Usage:
//
//	/agent create          → open wizard to create a new agent
//	/agent list            → list existing agent definitions
//	/agent edit <name>     → open wizard to edit an existing agent
func executeAgent(ctx *CommandContext, args string) (*CommandResult, error) {
	parts := strings.Fields(args)
	if len(parts) == 0 {
		return &CommandResult{
			Output: "Usage:\n  /agent create        - Create a new agent definition\n  /agent list          - List agent definitions\n  /agent edit <name>   - Edit an existing agent definition",
		}, nil
	}

	subcmd := strings.ToLower(parts[0])
	switch subcmd {
	case "create", "new":
		return &CommandResult{
			Action: ActionAgentCreate,
		}, nil

	case "list", "ls":
		// Delegate to the existing /agents list logic
		return executeAgents(ctx, "")

	case "edit":
		if len(parts) < 2 {
			return &CommandResult{
				Output: "Usage: /agent edit <name>",
			}, nil
		}
		name := strings.Join(parts[1:], " ")
		return &CommandResult{
			Action: ActionAgentEdit,
			Data:   map[string]any{"name": name},
		}, nil

	default:
		// Treat as a name lookup (like /agents <name>)
		return executeAgents(ctx, args)
	}
}
