package promptctx

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"
)

// ToolInfo describes a tool available to the agent, including its name,
// description, and parameter schema. This mirrors the reference implementation's
// tool schema structure used to generate tool description sections in the system prompt.
type ToolInfo struct {
	Name        string
	Description string
	Parameters  []ParamInfo
}

// ParamInfo describes a single parameter of a tool.
type ParamInfo struct {
	Name        string
	Type        string
	Description string
	Required    bool
}

// SystemPromptOptions holds all inputs needed to assemble the full system prompt.
// This mirrors the reference implementation's prompt assembly in prompts.ts,
// combining identity, environment, capabilities, tools, mode, memory, CLAUDE.md,
// and custom instructions into a single coherent prompt.
type SystemPromptOptions struct {
	// Model is the active model name/ID.
	Model string
	// Mode is the current permission mode ("normal", "plan", "auto").
	Mode string
	// Tools are the available tools with their schemas.
	Tools []ToolInfo
	// Memories are memory entries to include in the prompt.
	Memories []string
	// ClaudeMDContent is pre-loaded CLAUDE.md content for the project.
	ClaudeMDContent string
	// CustomInstructions are user-specified additional instructions.
	CustomInstructions string
	// ProjectDir is the project root directory.
	ProjectDir string
	// CWD is the current working directory.
	CWD string
	// IsSubAgent indicates whether this prompt is for a sub-agent.
	IsSubAgent bool
	// ParentContext is context passed from the parent agent (for sub-agents).
	ParentContext string
}

// BuildToolDescriptions formats all tool schemas into a text block suitable for
// inclusion in the system prompt. Each tool is rendered with its name, description,
// and parameters with types — matching how the reference runtime presents tool
// information within the system prompt.
func BuildToolDescriptions(tools []ToolInfo) string {
	if len(tools) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("# Tools\n\n")
	b.WriteString("You have access to the following tools:\n\n")

	for i, tool := range tools {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "## %s\n", tool.Name)
		if tool.Description != "" {
			fmt.Fprintf(&b, "\n%s\n", tool.Description)
		}

		if len(tool.Parameters) > 0 {
			b.WriteString("\nParameters:\n")
			for _, param := range tool.Parameters {
				requiredMarker := ""
				if param.Required {
					requiredMarker = " (required)"
				}
				fmt.Fprintf(&b, "- %s: %s%s", param.Name, param.Type, requiredMarker)
				if param.Description != "" {
					fmt.Fprintf(&b, " — %s", param.Description)
				}
				b.WriteByte('\n')
			}
		}
	}

	return b.String()
}

// BuildModeInstructions returns mode-specific behavioral instructions matching
// the reference implementation's permission mode system. This is separate from
// BuildModeBlock to allow flexible composition in AssembleFullSystemPrompt.
//
//   - "plan" mode: tells the agent to plan without implementing
//   - "normal" mode: no additional instructions (empty string)
//   - "auto" mode: instructions about autonomous permission behavior
func BuildModeInstructions(mode string) string {
	switch mode {
	case "plan":
		return `# Mode: Plan

You are currently in PLAN mode. In this mode:
- Analyze the user's request and create a detailed plan
- Do NOT make any changes to files or execute commands
- Present your plan clearly with numbered steps
- Wait for the user to approve the plan before taking action
- If the user approves, they will switch you to a mode where you can execute`

	case "auto":
		return `# Mode: Auto

You are currently in AUTO mode. In this mode:
- You can read and write files, execute commands, and make changes without asking for permission
- You should still explain what you are doing and why
- Be careful with destructive operations and prefer safe approaches
- If something seems risky, mention it before proceeding`

	case "normal", "":
		return ""

	default:
		return fmt.Sprintf("# Mode: %s", mode)
	}
}

// BuildMemoryContext formats memory entries for inclusion in the system prompt.
// Memories are contextual notes from prior interactions or user-specified context
// that should inform the agent's behavior. This mirrors how the reference
// implementation loads and presents session memory.
func BuildMemoryContext(memories []string) string {
	if len(memories) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("# Memory\n\n")
	b.WriteString("The following memory entries are available from prior context:\n\n")

	for _, mem := range memories {
		trimmed := strings.TrimSpace(mem)
		if trimmed == "" {
			continue
		}
		b.WriteString("- ")
		b.WriteString(trimmed)
		b.WriteByte('\n')
	}

	return b.String()
}

// AssembleFullSystemPrompt is the main orchestrator that assembles the complete
// system prompt from all constituent parts. This mirrors the reference
// implementation's getSystemPrompt() in prompts.ts, which combines identity,
// environment, capabilities, tools, mode, memory, CLAUDE.md, and custom
// instructions into a coherent prompt array.
//
// Assembly order:
//  1. Identity block (model info)
//  2. Environment block (OS, CWD, date, shell)
//  3. Capabilities block (tool names, usage guidelines)
//  4. Tool descriptions (detailed schemas)
//  5. Mode instructions (plan/auto/normal)
//  6. Memory context
//  7. CLAUDE.md content
//  8. Custom instructions
//  9. Sub-agent context (if applicable)
func AssembleFullSystemPrompt(opts SystemPromptOptions) string {
	sections := make([]string, 0, 10)

	// 1. Identity block
	identity := BuildIdentityBlock(opts.Model)
	if identity != "" {
		sections = append(sections, identity)
	}

	// 2. Environment block
	cwd := opts.CWD
	if cwd == "" {
		cwd = opts.ProjectDir
	}
	env := BuildEnvironmentBlock(cwd)
	if env != "" {
		sections = append(sections, env)
	}

	// 3. Capabilities block (tool names for quick reference)
	if len(opts.Tools) > 0 {
		toolNames := make([]string, len(opts.Tools))
		for i, t := range opts.Tools {
			toolNames[i] = t.Name
		}
		capabilities := BuildCapabilitiesBlock(toolNames, 0)
		if capabilities != "" {
			sections = append(sections, capabilities)
		}
	}

	// 4. Tool descriptions (detailed schemas)
	toolDescs := BuildToolDescriptions(opts.Tools)
	if toolDescs != "" {
		sections = append(sections, toolDescs)
	}

	// 5. Mode instructions
	modeInstr := BuildModeInstructions(opts.Mode)
	if modeInstr != "" {
		sections = append(sections, modeInstr)
	}

	// 6. Memory context
	memCtx := BuildMemoryContext(opts.Memories)
	if memCtx != "" {
		sections = append(sections, memCtx)
	}

	// 7. CLAUDE.md content
	if trimmed := strings.TrimSpace(opts.ClaudeMDContent); trimmed != "" {
		sections = append(sections, trimmed)
	}

	// 8. Custom instructions
	if trimmed := strings.TrimSpace(opts.CustomInstructions); trimmed != "" {
		header := "# Custom Instructions\n\nThe following additional instructions have been provided by the user:\n\n"
		sections = append(sections, header+trimmed)
	}

	// 9. Sub-agent context (if applicable)
	if opts.IsSubAgent && strings.TrimSpace(opts.ParentContext) != "" {
		subAgentBlock := "# Parent Agent Context\n\n" + strings.TrimSpace(opts.ParentContext)
		sections = append(sections, subAgentBlock)
	}

	return strings.Join(sections, "\n\n")
}

// BaseIdentityPrompt is the core identity text for the AI assistant, matching
// the reference implementation's tone and structure from context.ts.
const BaseIdentityPrompt = `You are an interactive CLI tool that helps users with software engineering tasks. Use the instructions below and the tools available to you to assist the user.

IMPORTANT: You should be concise in your responses and avoid unnecessary verbosity. Do not repeat information that the user already knows. Focus on providing direct, actionable answers.

If you are unsure about something or need more information, ask the user to clarify rather than making assumptions.`

// modeDescriptions maps permission modes to their behavioral descriptions.
var modeDescriptions = map[string]string{
	"plan": `You are currently in PLAN mode. In this mode:
- You should analyze the user's request and create a detailed plan
- You should NOT make any changes to files or execute commands
- Present your plan clearly with numbered steps
- Wait for the user to approve the plan before taking action
- If the user approves, they will switch you to a mode where you can execute`,

	"auto": `You are currently in AUTO mode. In this mode:
- You can read and write files, execute commands, and make changes without asking for permission
- You should still explain what you are doing and why
- Be careful with destructive operations and prefer safe approaches
- If something seems risky, mention it before proceeding`,

	"interactive": `You are currently in INTERACTIVE mode. In this mode:
- You should ask for permission before making changes to files or executing commands
- Present what you intend to do and wait for approval
- For read-only operations like searching or reading files, you can proceed without asking
- Group related changes together when asking for permission`,
}

// PromptConfig holds all configuration needed to assemble the full system prompt.
type PromptConfig struct {
	// BasePrompt is the core system identity prompt.
	// If empty, BaseIdentityPrompt is used.
	BasePrompt string
	// CWD is the current working directory.
	CWD string
	// Model is the active model name.
	Model string
	// PermissionMode is the current permission mode (plan, auto, interactive).
	PermissionMode string
	// ToolNames are the names of available tools.
	ToolNames []string
	// CustomInstructions from user configuration.
	CustomInstructions string
	// IsNonInteractive indicates headless/SDK mode.
	IsNonInteractive bool
	// SessionID for context.
	SessionID string
	// MaxTurns for context (relevant in non-interactive mode).
	MaxTurns int
}

// BuildSystemPrompt assembles the complete system prompt from all sources.
// This is the central prompt assembly point matching the reference's context.ts.
// It combines identity, environment, capabilities, mode, instructions, and
// custom user instructions into a single coherent system prompt.
func BuildSystemPrompt(cfg PromptConfig) string {
	sections := make([]string, 0, 6)

	// 1. Identity block
	identity := BuildIdentityBlock(cfg.Model)
	if identity != "" {
		sections = append(sections, identity)
	}

	// 2. Environment block
	env := BuildEnvironmentBlock(cfg.CWD)
	if env != "" {
		sections = append(sections, env)
	}

	// 3. Capabilities block
	capabilities := BuildCapabilitiesBlock(cfg.ToolNames, cfg.MaxTurns)
	if capabilities != "" {
		sections = append(sections, capabilities)
	}

	// 4. Mode block
	mode := BuildModeBlock(cfg.PermissionMode)
	if mode != "" {
		sections = append(sections, mode)
	}

	// 5. Instructions block (CLAUDE.md + project instructions)
	instructions := BuildInstructionsBlock(cfg.CWD, cfg.CustomInstructions)
	if instructions != "" {
		sections = append(sections, instructions)
	}

	// 6. Non-interactive context if applicable
	if cfg.IsNonInteractive {
		nonInteractive := buildNonInteractiveBlock(cfg.SessionID, cfg.MaxTurns)
		if nonInteractive != "" {
			sections = append(sections, nonInteractive)
		}
	}

	return strings.Join(sections, "\n\n")
}

// BuildIdentityBlock returns the base identity section describing who/what the
// assistant is and its general behavioral guidelines.
func BuildIdentityBlock(model string) string {
	var b strings.Builder
	b.WriteString(BaseIdentityPrompt)

	if model != "" {
		fmt.Fprintf(&b, "\n\nYou are powered by %s.", model)
	}

	return b.String()
}

// BuildEnvironmentBlock returns a section describing the runtime environment
// including OS, CWD, current date/time, and shell information.
func BuildEnvironmentBlock(cwd string) string {
	now := time.Now()

	var b strings.Builder
	b.WriteString("# Environment\n\n")
	fmt.Fprintf(&b, "- Operating System: %s/%s\n", runtime.GOOS, runtime.GOARCH)

	if cwd != "" {
		fmt.Fprintf(&b, "- Working Directory: %s\n", cwd)
	}

	fmt.Fprintf(&b, "- Current Date: %s\n", now.Format("2006-01-02 15:04:05 MST"))

	// Detect shell
	shell := detectShell()
	if shell != "" {
		fmt.Fprintf(&b, "- Shell: %s\n", shell)
	}

	return b.String()
}

// BuildCapabilitiesBlock returns a section listing available tools and
// behavioral constraints for tool usage.
func BuildCapabilitiesBlock(toolNames []string, maxTurns int) string {
	var b strings.Builder
	b.WriteString("# Capabilities\n\n")

	if len(toolNames) > 0 {
		b.WriteString("You have access to the following tools:\n")
		for _, name := range toolNames {
			fmt.Fprintf(&b, "- %s\n", name)
		}
		b.WriteByte('\n')
	} else {
		b.WriteString("You have access to a set of tools to assist with tasks.\n\n")
	}

	b.WriteString("## Tool Usage Guidelines\n\n")
	b.WriteString("- Use the appropriate tool for each task rather than trying to do everything in one step.\n")
	b.WriteString("- When reading files, prefer targeted reads over reading entire large files.\n")
	b.WriteString("- When searching code, use pattern matching tools before reading files.\n")
	b.WriteString("- Always verify changes after making them.\n")

	if maxTurns > 0 {
		fmt.Fprintf(&b, "\nYou have a maximum of %d turns to complete the task.", maxTurns)
	}

	return b.String()
}

// BuildModeBlock returns a section describing the current permission mode's
// behavioral expectations (plan, auto, interactive, etc.).
func BuildModeBlock(mode string) string {
	if mode == "" {
		return ""
	}

	desc, ok := modeDescriptions[mode]
	if !ok {
		return fmt.Sprintf("# Mode\n\nCurrent permission mode: %s", mode)
	}

	return "# Mode\n\n" + desc
}

// BuildInstructionsBlock loads CLAUDE.md content for the given working directory
// and appends any custom instructions. This integrates with the existing
// DiscoverClaudeMds/LoadClaudeMdContent infrastructure.
func BuildInstructionsBlock(cwd, customInstructions string) string {
	var parts []string

	// Load project instructions from CLAUDE.md files
	if cwd != "" {
		claudeContent, err := LoadClaudeMdContent(cwd)
		if err == nil && claudeContent != "" {
			parts = append(parts, claudeContent)
		}
	}

	// Append custom user instructions
	if trimmed := strings.TrimSpace(customInstructions); trimmed != "" {
		header := "# Custom Instructions\n\nThe following additional instructions have been provided by the user:"
		parts = append(parts, header+"\n\n"+trimmed)
	}

	if len(parts) == 0 {
		return ""
	}

	return strings.Join(parts, "\n\n")
}

// buildNonInteractiveBlock returns context specific to headless/SDK mode.
func buildNonInteractiveBlock(sessionID string, maxTurns int) string {
	var b strings.Builder
	b.WriteString("# Session Context\n\n")
	b.WriteString("You are running in non-interactive (headless) mode.\n")
	b.WriteString("- Do not ask clarifying questions; make reasonable assumptions instead.\n")
	b.WriteString("- Provide complete answers without expecting follow-up interaction.\n")
	b.WriteString("- If you cannot complete a task, explain what is blocking you.\n")

	if sessionID != "" {
		fmt.Fprintf(&b, "\nSession ID: %s\n", sessionID)
	}

	if maxTurns > 0 {
		fmt.Fprintf(&b, "Maximum turns: %d\n", maxTurns)
	}

	return b.String()
}

// detectShell returns the user's current shell, if detectable.
func detectShell() string {
	// Try SHELL env var (Unix)
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	// Try COMSPEC (Windows)
	if comspec := os.Getenv("COMSPEC"); comspec != "" {
		return comspec
	}
	return ""
}
