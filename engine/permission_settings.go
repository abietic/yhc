package engine

import (
	"context"
	"errors"

	"github.com/abietic/yhc/engine/permission"
)

var ErrPermissionSettingsActiveTurn = errors.New("permission rules cannot change during execution or pending Plan approval")

type PermissionRuleSettingsResult struct {
	permission.RuleSettings
	RuntimeApplied bool   `json:"runtime_applied"`
	Diagnostic     string `json:"diagnostic"`
}

func (e *QueryEngine) permissionSettingsProjectRoot() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.config.PermissionProjectRoot
}

// PermissionRuleSettings inspects only the selected file, not effective policy.
// The app-server binds this engine to a live, owned session before calling it.
func (e *QueryEngine) PermissionRuleSettings(scope string) (permission.RuleSettings, error) {
	return permission.RuleSettingsSnapshot(e.permissionSettingsProjectRoot(), scope)
}

// ApplyPermissionRuleSettings serializes persistence and rule reload with turn
// admission. It never changes mode, Plan state, the registry, or a submitted
// model request. The ordinary next-turn pool assembly observes blanket denies.
func (e *QueryEngine) ApplyPermissionRuleSettings(ctx context.Context, scope, revision string, changes []permission.RuleSettingsChange) (PermissionRuleSettingsResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return PermissionRuleSettingsResult{}, err
	}
	if err := e.lockExecutionControlMutation(""); err != nil {
		return PermissionRuleSettingsResult{}, ErrPermissionSettingsActiveTurn
	}
	defer e.planMu.Unlock()
	if e.planState.Phase == PlanPhaseAwaitingApproval {
		return PermissionRuleSettingsResult{}, ErrPermissionSettingsActiveTurn
	}
	root := e.permissionSettingsProjectRoot()
	snapshot, err := permission.RuleSettingsPatchContext(ctx, root, scope, revision, changes)
	if err != nil {
		return PermissionRuleSettingsResult{}, err
	}
	result := PermissionRuleSettingsResult{RuleSettings: snapshot}
	// Persistence has committed. Even if the request is cancelled now, report
	// and attempt live application rather than imply that disk was unchanged.
	rules, err := permission.LoadPermissionRules(root)
	if err != nil {
		result.Diagnostic = "runtime_reload_failed"
		return result, nil
	}
	rules = append(rules, toolSelectionDenyRules(e.toolRegistry, e.config.ToolSelection)...)
	e.mu.Lock()
	e.permissionRules = permission.NewRulesEngine(rules)
	e.mu.Unlock()
	result.RuntimeApplied = true
	return result, nil
}
