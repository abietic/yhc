package hooks

// HasWorkspaceMutationHooks conservatively treats host callbacks as potential
// writers. Programmatic registration is construction-time, like HasAgentHooks;
// the dynamically replaceable shell configuration uses its existing lock.
func (e *Executor) HasWorkspaceMutationHooks() bool {
	if e == nil {
		return false
	}
	if len(e.postSamplingHooks)+len(e.preToolHooks)+len(e.postToolHooks)+len(e.postToolFailureHooks)+len(e.stopHooks)+len(e.stopFailureHooks)+len(e.permissionDeniedHooks)+len(e.userPromptSubmitHooks)+len(e.sessionStartHooks)+len(e.sessionEndHooks)+len(e.preCompactHooks)+len(e.postCompactHooks)+len(e.notificationHooks)+len(e.commandHooks)+len(e.turnStartHooks)+len(e.turnEndHooks)+len(e.promptHooks)+len(e.agentStartHooks)+len(e.agentCompleteHooks)+len(e.agentFailHooks) > 0 {
		return true
	}
	config := e.ShellHookSnapshot()
	return config != nil && len(config.PreToolHooks)+len(config.PostToolHooks)+len(config.UserPromptHooks) > 0
}
