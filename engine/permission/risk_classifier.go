package permission

import "strings"

// ToolRiskLevel categorizes the risk of a tool operation.
// Used by the unified evaluator to make default decisions when no explicit
// rules match. Read-only operations can be auto-allowed; destructive ones
// always require confirmation.
type ToolRiskLevel string

const (
	// RiskReadOnly means the operation only reads data and has no side effects.
	// Examples: Read, Grep, Glob, git status, ls, cat.
	RiskReadOnly ToolRiskLevel = "read_only"

	// RiskWrite means the operation modifies files or state within the project.
	// Examples: Write, Edit, git add, npm install.
	RiskWrite ToolRiskLevel = "write"

	// RiskDestructive means the operation could cause irreversible damage.
	// Examples: rm -rf, git push --force, drop database.
	RiskDestructive ToolRiskLevel = "destructive"

	// RiskUnknown means the tool or operation could not be classified.
	// Treated conservatively (as write-level risk).
	RiskUnknown ToolRiskLevel = "unknown"
)

// RiskClassification holds the classification result for a tool operation.
type RiskClassification struct {
	// Level is the risk category.
	Level ToolRiskLevel
	// Reason explains why this classification was assigned.
	Reason string
}

// ToolRiskClassifier classifies tool operations by risk level.
// This is distinct from the AI-based classifier (ClassifyToolUse) — it uses
// static analysis of tool names and inputs to determine risk without LLM calls.
//
// Classification hierarchy:
//   - Tool name determines the base risk level
//   - For Bash, the command content refines the classification
//   - Explicit read-only tools are always safe
//   - Explicit destructive patterns are always dangerous
type ToolRiskClassifier struct {
	// readOnlyTools are tools that never modify state.
	readOnlyTools map[string]bool
	// writeTools are tools that modify files/state.
	writeTools map[string]bool
}

// NewToolRiskClassifier creates a classifier with default tool risk mappings.
// The mappings mirror the tool categorization from the reference implementation's
// safe tool allowlist and permission behavior.
func NewToolRiskClassifier() *ToolRiskClassifier {
	return &ToolRiskClassifier{
		readOnlyTools: map[string]bool{
			// File reading
			"Read": true,
			// Search operations
			"Grep": true,
			"Glob": true,
			// Web operations (read-only)
			"WebFetch":  true,
			"WebSearch": true,
			// User interaction
			"AskUserQuestion": true,
			// Plan mode tools
			"EnterPlanMode": true,
			"ExitPlanMode":  true,
			// Task management (metadata only)
			"TaskCreate": true,
			"TaskGet":    true,
			"TaskUpdate": true,
			"TaskList":   true,
			"TaskStop":   true,
			"TaskOutput": true,
			// Sub-agent (sandboxed)
			"Agent": true,
			// Todo (metadata only)
			"TodoWrite": true,
		},
		writeTools: map[string]bool{
			"Write": true,
			"Edit":  true,
		},
	}
}

// Classify determines the risk level of a tool operation.
// For most tools, classification is based purely on the tool name.
// For Bash, the command content is analyzed to determine risk.
func (c *ToolRiskClassifier) Classify(toolName string, toolInput map[string]any) RiskClassification {
	if c == nil {
		return RiskClassification{Level: RiskUnknown, Reason: "no classifier configured"}
	}

	// Check explicit read-only tools
	if c.readOnlyTools[toolName] {
		return RiskClassification{
			Level:  RiskReadOnly,
			Reason: toolName + " is a read-only tool",
		}
	}

	// Check explicit write tools
	if c.writeTools[toolName] {
		return RiskClassification{
			Level:  RiskWrite,
			Reason: toolName + " modifies files",
		}
	}

	// MCP tools: treat as write by default (external side effects possible)
	if strings.HasPrefix(toolName, "mcp__") {
		return RiskClassification{
			Level:  RiskWrite,
			Reason: "MCP tool (external side effects possible)",
		}
	}

	// Bash tool: classify based on command content
	if toolName == "Bash" {
		return c.classifyBashCommand(toolInput)
	}

	// Unknown tool: treat conservatively
	return RiskClassification{
		Level:  RiskUnknown,
		Reason: "unknown tool " + toolName,
	}
}

// classifyBashCommand analyzes a bash command to determine its risk level.
// Uses the existing read-only command analysis and dangerous pattern detection.
func (c *ToolRiskClassifier) classifyBashCommand(input map[string]any) RiskClassification {
	command, _ := input["command"].(string)
	if command == "" {
		return RiskClassification{Level: RiskUnknown, Reason: "empty bash command"}
	}

	command = strings.TrimSpace(command)

	// Check if the entire command is read-only
	if IsReadOnlyCommand(command) {
		return RiskClassification{
			Level:  RiskReadOnly,
			Reason: "bash command is read-only: " + truncateForReason(command),
		}
	}

	// Check for destructive patterns
	if isDestructiveBashCommand(command) {
		return RiskClassification{
			Level:  RiskDestructive,
			Reason: "bash command is destructive: " + truncateForReason(command),
		}
	}

	// Default: write-level risk for non-read-only commands
	return RiskClassification{
		Level:  RiskWrite,
		Reason: "bash command modifies state: " + truncateForReason(command),
	}
}

// isDestructiveBashCommand checks if a command matches known destructive patterns.
// These are operations that could cause irreversible damage and should always
// require explicit user confirmation.
func isDestructiveBashCommand(command string) bool {
	// Extract first word(s) for quick classification
	firstWord := extractFirstWord(command)

	// Always-destructive commands
	destructiveCommands := map[string]bool{
		"rm":       true,
		"rmdir":    true,
		"shred":    true,
		"mkfs":     true,
		"dd":       true,
		"kill":     true,
		"killall":  true,
		"pkill":    true,
		"reboot":   true,
		"shutdown": true,
		"halt":     true,
		"poweroff": true,
	}
	if destructiveCommands[firstWord] {
		return true
	}

	// Destructive git operations
	twoWord := extractFirstTwoWords(command)
	destructiveGitOps := map[string]bool{
		"git push":        containsForceFlag(command),
		"git reset":       true,
		"git clean":       true,
		"git rebase":      true,
		"git cherry-pick": true,
	}
	if isDestructive, exists := destructiveGitOps[twoWord]; exists && isDestructive {
		return true
	}

	// Check for the dangerous patterns from the existing detection system
	dangerousResult := DetectDangerousPatterns(command)
	if dangerousResult.HasCurlPipe || dangerousResult.HasEval || dangerousResult.HasDDWrite {
		return true
	}

	return false
}

// containsForceFlag checks if a command contains --force or -f flag.
func containsForceFlag(command string) bool {
	fields := strings.Fields(command)
	for _, f := range fields {
		if f == "--force" || f == "-f" {
			return true
		}
	}
	return false
}

// truncateForReason truncates a command string for display in reason messages.
func truncateForReason(s string) string {
	const maxLen = 60
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
