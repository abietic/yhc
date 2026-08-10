package engine

import (
	"strings"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/tools"
)

func cloneGoalCapabilityConfig(config *GoalCapabilityConfig) *GoalCapabilityConfig {
	if config == nil {
		return nil
	}
	cloned := *config
	cloned.DefaultTokenBudget = cloneUint64(config.DefaultTokenBudget)
	return &cloned
}

func (e *QueryEngine) goalCapabilityConfigured() bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	configured := e.config.GoalCapability != nil
	e.mu.Unlock()
	return configured
}

func (e *QueryEngine) goalCapabilityEnabled() bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	enabled := e.config.GoalCapability != nil &&
		e.config.GoalCapability.Enabled
	e.mu.Unlock()
	return enabled
}

func (e *QueryEngine) goalWorkflowEnabled() bool {
	if e == nil || e.administrationOnly {
		return false
	}
	e.mu.Lock()
	capability := cloneGoalCapabilityConfig(e.config.GoalCapability)
	entrypoint := e.config.CommandEntrypoint
	sessionID := e.config.SessionID
	threadID := e.config.ThreadID
	agentID := e.config.AgentID
	recorder := e.transcript
	e.mu.Unlock()
	return capability != nil &&
		capability.Enabled &&
		(entrypoint == commands.EntrypointTUI ||
			entrypoint == commands.EntrypointPlain ||
			entrypoint == commands.EntrypointHeadlessGoal ||
			(entrypoint == commands.EntrypointACP &&
				capability.ACPNegotiated)) &&
		strings.TrimSpace(sessionID) != "" &&
		strings.TrimSpace(threadID) != "" &&
		strings.TrimSpace(agentID) == "" &&
		recorder != nil &&
		strings.TrimSpace(recorder.Path()) != ""
}

func (e *QueryEngine) goalModelToolsVisible() bool {
	return e.goalWorkflowEnabled() &&
		!planPhaseRequiresContainment(e.PlanState().Phase) &&
		e.currentGoalExecutionIdentity() != nil
}

// GoalCommandAvailability is the narrow runtime capability queried by the
// command registry. It grants no Goal mutation authority.
func (e *QueryEngine) GoalCommandAvailability() (bool, string) {
	if e == nil {
		return false, "Goal requires an engine runtime"
	}
	if !e.goalWorkflowEnabled() {
		return false, "Goal is available only when enabled for a saved root TUI, Plain, or dedicated headless Goal session, or a negotiated saved-root ACP session"
	}
	if planPhaseRequiresContainment(e.PlanState().Phase) {
		return false, "Goal is unavailable while Plan mode is active"
	}
	return true, ""
}

func (e *QueryEngine) goalDefaultTokenBudget() *uint64 {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.config.GoalCapability == nil {
		return nil
	}
	return cloneUint64(e.config.GoalCapability.DefaultTokenBudget)
}

func isGoalToolName(name string) bool {
	switch strings.TrimSpace(name) {
	case tools.GetGoalToolName, tools.UpdateGoalToolName:
		return true
	default:
		return false
	}
}
