package engine

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/abietic/yhc/engine/budget"
	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/plugins"
	"github.com/abietic/yhc/engine/skills"
	"github.com/abietic/yhc/tools"
)

// ProcessedInput is the result of processing raw user input through the
// input pipeline. It detects slash commands, model overrides, token budget
// continuations, and extracts the cleaned prompt for the model.
// Mirrors the reference's processUserInput in query.ts.
type ProcessedInput struct {
	// Prompt is the cleaned user prompt (after extracting overrides).
	Prompt string
	// ModelOverride is set when the user used @model: syntax.
	ModelOverride string
	// IsTokenBudgetContinuation is true when the input is a pure budget request.
	IsTokenBudgetContinuation bool
	// TokenBudgetDelta is the parsed token budget amount (0 if not a budget input).
	TokenBudgetDelta int
}

// modelOverrideRe matches @model:name at the start of input.
var modelOverrideRe = regexp.MustCompile(`^@model:(\S+)\s*`)

// processUserInput runs the input processing pipeline on raw user text.
// It handles: slash commands, @model: overrides, token budget continuations.
// Plain text without special syntax passes through unchanged.
func (e *QueryEngine) processUserInput(input string) ProcessedInput {
	input = strings.TrimSpace(input)
	result := ProcessedInput{Prompt: input}

	if input == "" {
		return result
	}

	// 1. @model: override extraction.
	if loc := modelOverrideRe.FindStringSubmatchIndex(input); loc != nil {
		result.ModelOverride = input[loc[2]:loc[3]]
		result.Prompt = strings.TrimSpace(input[loc[1]:])
		if result.Prompt == "" {
			result.Prompt = input // keep original if only override with no prompt
		}
	}

	// 2. Token budget continuation detection.
	if budget.IsTokenBudgetContinuation(result.Prompt) {
		result.IsTokenBudgetContinuation = true
		result.TokenBudgetDelta = budget.ParseTokenBudgetFromText(result.Prompt)
	}

	return result
}

// commandRegistry returns the engine's command registry, creating one if needed.
func (e *QueryEngine) ensureCommandRegistry() *commands.Registry {
	if e.commandRegistry == nil {
		e.commandRegistry = commands.NewRegistry()
		commands.RegisterDefaults(e.commandRegistry)
	}
	return e.commandRegistry
}

// GetCommandRegistry returns the engine-owned command registry shared by all
// entrypoint projections for this session.
func (e *QueryEngine) GetCommandRegistry() *commands.Registry {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ensureCommandRegistry()
}

func (e *QueryEngine) promptCommandCandidate() (
	commands.PromptCommandGenerationCandidate,
	*commands.Registry,
	error,
) {
	if e == nil {
		return commands.PromptCommandGenerationCandidate{}, nil, fmt.Errorf("query engine is nil")
	}

	e.mu.Lock()
	dirs := append([]string(nil), e.pluginDirs...)
	disableBundled := e.config.DisableBundledWorkflows
	registry := e.ensureCommandRegistry()
	e.mu.Unlock()

	loader := plugins.NewLoaderWithOptions(plugins.LoaderOptions{
		Dirs:                    dirs,
		DisableBundledWorkflows: disableBundled,
	})
	candidate, err := loader.BuildCommandGeneration()
	return candidate, registry, err
}

func promptCommandSourceCounts(sources []commands.PromptCommandSourceSnapshot) (
	enabledPlugins int,
	bundledPacks int,
) {
	for _, source := range sources {
		switch source.Kind {
		case commands.CommandSourceBundled:
			bundledPacks++
		case commands.CommandSourcePlugin:
			enabledPlugins++
		}
	}
	return enabledPlugins, bundledPacks
}

// ValidatePromptCommands builds and collision-checks the complete bundled and
// configured-plugin candidate without changing the live registry generation.
func (e *QueryEngine) ValidatePromptCommands() (
	commands.PromptCommandValidationResult,
	error,
) {
	candidate, registry, err := e.promptCommandCandidate()
	result := commands.PromptCommandValidationResult{
		Commands:    len(candidate.Commands),
		Digest:      candidate.Digest,
		Sources:     append([]commands.PromptCommandSourceSnapshot(nil), candidate.Sources...),
		Diagnostics: append([]commands.PromptCommandDiagnostic(nil), candidate.Diagnostics...),
	}
	result.EnabledPlugins, result.BundledPacks = promptCommandSourceCounts(candidate.Sources)
	if err != nil {
		if registry != nil {
			result.LiveGeneration = registry.PromptCommandGeneration()
		}
		return result, err
	}
	liveGeneration, err := registry.ValidatePromptCommandGeneration(candidate)
	result.LiveGeneration = liveGeneration
	if err != nil {
		result.Diagnostics = append(result.Diagnostics, commands.PromptCommandDiagnostic{
			Severity: "error",
			Code:     "registry_collision",
			Message:  err.Error(),
		})
		return result, err
	}
	return result, nil
}

// ReloadPromptCommands rebuilds bundled and configured-plugin prompt commands
// and atomically swaps them into the engine-owned registry. A failed reload
// leaves the previous complete snapshot live.
func (e *QueryEngine) ReloadPromptCommands() (commands.PromptCommandReloadResult, error) {
	candidate, registry, err := e.promptCommandCandidate()
	if err != nil {
		result := commands.PromptCommandReloadResult{
			Diagnostics: append(
				[]commands.PromptCommandDiagnostic(nil),
				candidate.Diagnostics...,
			),
		}
		if registry != nil {
			result.Generation = registry.PromptCommandGeneration()
		}
		return result, err
	}
	generation, err := registry.ReplacePromptCommandGeneration(candidate)
	if err != nil {
		diagnostics := append(
			[]commands.PromptCommandDiagnostic(nil),
			candidate.Diagnostics...,
		)
		diagnostics = append(diagnostics, commands.PromptCommandDiagnostic{
			Severity: "error",
			Code:     "registry_collision",
			Message:  err.Error(),
		})
		return commands.PromptCommandReloadResult{
			Generation:  registry.PromptCommandGeneration(),
			Diagnostics: diagnostics,
		}, err
	}
	result := commands.PromptCommandReloadResult{
		Commands:   generation.Commands,
		Generation: generation,
		Diagnostics: append(
			[]commands.PromptCommandDiagnostic(nil),
			generation.Diagnostics...,
		),
	}
	result.EnabledPlugins, result.BundledPacks = promptCommandSourceCounts(generation.Sources)
	return result, nil
}

// ReloadPluginCommands preserves the previous embedding API while delegating
// to the single prompt-command generation owner.
func (e *QueryEngine) ReloadPluginCommands() (commands.PluginReloadResult, error) {
	return e.ReloadPromptCommands()
}

// GetSkillRegistry returns the skills loaded for this engine.
func (e *QueryEngine) GetSkillRegistry() *skills.SkillRegistry {
	return e.skillRegistry
}

// MCPInventorySnapshot returns only the MCP manager's immutable inventory.
// It does not load Agent definitions, tasks, skills, or hook state.
func (e *QueryEngine) MCPInventorySnapshot() tools.MCPInventorySnapshot {
	if e == nil {
		return tools.MCPInventorySnapshot{}
	}
	e.mu.Lock()
	mcpManager := e.mcpManager
	e.mu.Unlock()
	if mcpManager == nil {
		return tools.MCPInventorySnapshot{}
	}
	return mcpManager.InventorySnapshot()
}

// RuntimeInspectionSnapshot returns the engine-owned read model used by
// orchestration and extension inspection commands.
func (e *QueryEngine) RuntimeInspectionSnapshot() commands.RuntimeInspectionSnapshot {
	if e == nil {
		return commands.RuntimeInspectionSnapshot{}
	}
	e.mu.Lock()
	cwd := e.config.CWD
	skillRegistry := e.skillRegistry
	hookExecutor := e.config.HookExecutor
	commandRegistry := e.ensureCommandRegistry()
	e.mu.Unlock()

	definitions, definitionErr := LoadAgentDefinitions(cwd)
	agentDefinitions := make(map[string]commands.AgentInfo, len(definitions))
	for key, definition := range definitions {
		agentDefinitions[key] = commands.AgentInfo{
			Name:            definition.Name,
			WhenToUse:       definition.WhenToUse,
			Tools:           append([]string(nil), definition.Tools...),
			DisallowedTools: append([]string(nil), definition.DisallowedTools...),
			MaxTurns:        definition.MaxTurns,
			ReadOnly:        definition.ReadOnly,
			Source:          definition.Source,
			FilePath:        definition.FilePath,
		}
	}

	snapshot := commands.RuntimeInspectionSnapshot{
		Tasks:            e.RuntimeTaskSnapshot(),
		TaskExplorer:     taskExplorerInspectionSnapshot(e.TaskExplorerSnapshot()),
		AgentDefinitions: agentDefinitions,
		PromptCommands:   commandRegistry.PromptCommandGeneration(),
	}
	if len(definitionErr) > 0 {
		snapshot.AgentDiagnostic = errors.Join(definitionErr...).Error()
	}
	if skillRegistry != nil {
		snapshot.Skills = skillRegistry.Snapshot()
	}
	snapshot.MCP = e.MCPInventorySnapshot()
	if hookExecutor != nil {
		snapshot.Hooks = hookExecutor.ShellHookSnapshot()
	}
	return snapshot
}

func taskExplorerInspectionSnapshot(
	source TaskExplorerSnapshot,
) commands.TaskExplorerInspectionSnapshot {
	out := commands.TaskExplorerInspectionSnapshot{
		Available:         source.Available,
		UnavailableReason: source.UnavailableReason,
		SessionID:         source.SessionID,
		BoardID:           source.BoardID,
		BoardRevision:     source.Revision.Board,
		RuntimeRevision:   source.Revision.Runtime,
		Hidden: commands.TaskExplorerInspectionHidden{
			WorkItems:                   cloneStringIntMap(source.Hidden.WorkItems),
			Executions:                  cloneStringIntMap(source.Hidden.Executions),
			Links:                       source.Hidden.Links,
			Attention:                   cloneStringIntMap(source.Hidden.Attention),
			WorkBoardOutsidePrimary:     source.Hidden.WorkBoardOutsidePrimary,
			RuntimeEventsDropped:        source.Hidden.RuntimeEventsDropped,
			ExecutionGenerationsEvicted: source.Hidden.ExecutionGenerationsEvicted,
			HiddenLiveExecutions:        source.Hidden.HiddenLiveExecutions,
		},
	}
	for _, item := range source.WorkItems {
		out.WorkItems = append(
			out.WorkItems,
			commands.TaskExplorerInspectionWorkItem{
				WorkItemID:    item.WorkItemID,
				Status:        item.Status,
				Title:         item.Title,
				Description:   item.Description,
				ActiveForm:    item.ActiveForm,
				Owner:         item.Owner,
				ResultSummary: item.ResultSummary,
			},
		)
	}
	for _, execution := range source.Executions {
		out.Executions = append(
			out.Executions,
			commands.TaskExplorerInspectionExecution{
				AgentID:     execution.Key.AgentID,
				Generation:  execution.Key.Generation,
				Status:      execution.Status,
				Phase:       string(execution.Phase),
				Name:        execution.Name,
				Task:        execution.Task,
				Description: execution.Description,
				Activity:    execution.Activity,
				ReplayOnly:  execution.ReplayOnly,
			},
		)
	}
	for _, link := range source.Links {
		out.Links = append(
			out.Links,
			commands.TaskExplorerInspectionLink{
				WorkItemID:        link.WorkItemID,
				AgentID:           link.AgentID,
				Generation:        link.Generation,
				State:             string(link.State),
				UnavailableReason: link.UnavailableReason,
			},
		)
	}
	return out
}

func cloneStringIntMap(source map[string]int) map[string]int {
	if len(source) == 0 {
		return nil
	}
	out := make(map[string]int, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}
