package commands

import (
	"fmt"
	"sort"
	"strings"
)

// executeTasks implements the /tasks command.
// Lists all background tasks with their status, type, and description.
// Mirrors src/commands/tasks/ — text-based listing instead of interactive dialog.
func executeTasks(ctx *CommandContext, args string) (*CommandResult, error) {
	if strings.TrimSpace(args) != "" {
		return &CommandResult{
			Output: "Task inspection is read-only. Use TaskStop through the canonical tool/runtime owner to stop a task.",
		}, nil
	}
	snapshot, ok := runtimeInspectionSnapshot(ctx)
	if !ok {
		return &CommandResult{
			Output: "Task inspection is unavailable for this runtime.",
		}, nil
	}
	return &CommandResult{
		Output: formatTaskExplorerInspection(snapshot.TaskExplorer),
	}, nil
}

func formatTaskExplorerInspection(
	snapshot TaskExplorerInspectionSnapshot,
) string {
	var sb strings.Builder
	sb.WriteString("Task Explorer\n")
	sb.WriteString("durability=durable-session-workboard\n")
	sb.WriteString("control=read-only-command\n")
	if !snapshot.Available {
		fmt.Fprintf(
			&sb,
			"unavailable=%s",
			firstNonEmpty(
				snapshot.UnavailableReason,
				"selector_unavailable",
			),
		)
		return sb.String()
	}
	fmt.Fprintf(
		&sb,
		"board=%s revision=%d runtime_revision=%d\n",
		snapshot.BoardID,
		snapshot.BoardRevision,
		snapshot.RuntimeRevision,
	)
	if len(snapshot.WorkItems) == 0 &&
		len(snapshot.Executions) == 0 &&
		len(snapshot.Links) == 0 {
		sb.WriteString("No task explorer rows.")
		return sb.String()
	}
	if len(snapshot.WorkItems) > 0 {
		fmt.Fprintf(&sb, "\nWorkItems (%d):\n", len(snapshot.WorkItems))
		for _, item := range snapshot.WorkItems {
			label := firstNonEmpty(
				item.ActiveForm,
				item.Title,
				item.Description,
				"work item",
			)
			fmt.Fprintf(
				&sb,
				"  [%s] %s %s",
				item.WorkItemID,
				item.Status,
				truncateTaskText(label, 100),
			)
			if item.Owner != "" {
				fmt.Fprintf(&sb, " owner=%s", item.Owner)
			}
			if item.ResultSummary != "" {
				fmt.Fprintf(
					&sb,
					" result=%s",
					truncateTaskText(item.ResultSummary, 80),
				)
			}
			sb.WriteByte('\n')
		}
	}
	if len(snapshot.Executions) > 0 {
		fmt.Fprintf(
			&sb,
			"\nExecutions (%d):\n",
			len(snapshot.Executions),
		)
		for _, execution := range snapshot.Executions {
			label := firstNonEmpty(
				execution.Activity,
				execution.Task,
				execution.Description,
				execution.Name,
				"agent",
			)
			fmt.Fprintf(
				&sb,
				"  [%s/%d] %s %s",
				execution.AgentID,
				execution.Generation,
				firstNonEmpty(execution.Status, execution.Phase, "unknown"),
				truncateTaskText(label, 100),
			)
			if execution.ReplayOnly {
				sb.WriteString(" replay_only=true")
			}
			sb.WriteByte('\n')
		}
	}
	if len(snapshot.Links) > 0 {
		fmt.Fprintf(&sb, "\nLinks (%d):\n", len(snapshot.Links))
		for _, link := range snapshot.Links {
			fmt.Fprintf(
				&sb,
				"  %s -> %s/%d state=%s",
				link.WorkItemID,
				link.AgentID,
				link.Generation,
				link.State,
			)
			if link.UnavailableReason != "" {
				fmt.Fprintf(
					&sb,
					" unavailable=%s",
					link.UnavailableReason,
				)
			}
			sb.WriteByte('\n')
		}
	}
	writeTaskExplorerHidden(&sb, snapshot.Hidden)
	return strings.TrimRight(sb.String(), "\n")
}

func writeTaskExplorerHidden(
	sb *strings.Builder,
	hidden TaskExplorerInspectionHidden,
) {
	parts := make([]string, 0)
	appendMap := func(label string, values map[string]int) {
		keys := make([]string, 0, len(values))
		for key, count := range values {
			if count > 0 {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			parts = append(
				parts,
				fmt.Sprintf("%s.%s=%d", label, key, values[key]),
			)
		}
	}
	appendMap("work_items", hidden.WorkItems)
	appendMap("executions", hidden.Executions)
	appendMap("attention", hidden.Attention)
	if hidden.Links > 0 {
		parts = append(parts, fmt.Sprintf("links=%d", hidden.Links))
	}
	if hidden.WorkBoardOutsidePrimary > 0 {
		parts = append(
			parts,
			fmt.Sprintf(
				"workboard_outside_primary=%d",
				hidden.WorkBoardOutsidePrimary,
			),
		)
	}
	if hidden.RuntimeEventsDropped > 0 {
		parts = append(
			parts,
			fmt.Sprintf(
				"runtime_events_dropped=%d",
				hidden.RuntimeEventsDropped,
			),
		)
	}
	if hidden.ExecutionGenerationsEvicted > 0 {
		parts = append(
			parts,
			fmt.Sprintf(
				"execution_generations_evicted=%d",
				hidden.ExecutionGenerationsEvicted,
			),
		)
	}
	if hidden.HiddenLiveExecutions > 0 {
		parts = append(
			parts,
			fmt.Sprintf(
				"hidden_live_executions=%d",
				hidden.HiddenLiveExecutions,
			),
		)
	}
	if len(parts) > 0 {
		fmt.Fprintf(sb, "\nHidden:\n  %s\n", strings.Join(parts, " "))
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func truncateTaskText(text string, maxLen int) string {
	if maxLen <= 0 || len(text) <= maxLen {
		return text
	}
	if maxLen <= 3 {
		return text[:maxLen]
	}
	return text[:maxLen-3] + "..."
}
