package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/compact"
	engineconfig "github.com/abietic/yhc/engine/config"
	"github.com/abietic/yhc/engine/provider"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/tools"
)

func childModelCallKey(agentID string, generation int64) string {
	return strings.TrimSpace(agentID) + ":" + fmt.Sprintf("%d", generation)
}

func (e *SubAgentExecutor) modelRoleAdmissionEnabled() bool {
	if e == nil || e.parentModelCallSnapshot == nil {
		return false
	}
	if _, ok := e.ModelResolver.(runtimeModelRoles); !ok {
		return false
	}
	return e.parentModelCallSnapshot() != nil
}

func (e *SubAgentExecutor) resolveNewChildModelCall(
	ctx context.Context,
	opts tools.AgentExecOptions,
	messages []*schema.Message,
) (roleModelCall, error) {
	role := modelRoleForAgentType(opts.SubagentType)
	var main *modelCallIdentity
	if e.parentModelCallSnapshot != nil {
		main = e.parentModelCallSnapshot()
	}
	var compatibilityModel model.BaseChatModel
	var compatibilitySelector string
	if (role == string(engineconfig.RoleExplore) ||
		role == string(engineconfig.RolePlan)) &&
		e.SubagentModel != nil {
		compatibilityModel = e.SubagentModel
		compatibilitySelector = e.SubagentModelSelector
	}
	return resolveRoleModelCall(
		ctx,
		e.ModelResolver,
		e.ChatModel,
		compatibilityModel,
		compatibilitySelector,
		main,
		role,
		provider.RoleRequirements{
			NeedReasoningHistory: messagesContainReasoning(messages),
			PromptTokens:         compact.EstimateTokenCount(messages),
		},
	)
}

func (e *SubAgentExecutor) resolveLegacyChildModelCall(
	opts tools.AgentExecOptions,
	messages []*schema.Message,
) (roleModelCall, error) {
	if e == nil || e.parentModelCallSnapshot == nil {
		return roleModelCall{}, fmt.Errorf(
			"legacy child has no admitted parent model binding",
		)
	}
	main := e.parentModelCallSnapshot()
	if main == nil {
		return roleModelCall{}, fmt.Errorf(
			"legacy child has no admitted parent model binding",
		)
	}
	role := modelRoleForAgentType(opts.SubagentType)
	identity := *main
	identity.Role = role
	identity.binding = main.binding.Clone()
	if role != string(engineconfig.RoleExplore) &&
		role != string(engineconfig.RolePlan) ||
		e.SubagentModel == nil {
		return roleModelCall{Model: e.ChatModel, Identity: &identity}, nil
	}
	selector := strings.TrimSpace(e.SubagentModelSelector)
	if selector == "" {
		return roleModelCall{}, fmt.Errorf(
			"trusted %s model injection requires a truthful model selector",
			role,
		)
	}
	roleResolver, ok := e.ModelResolver.(runtimeModelRoles)
	if !ok {
		return roleModelCall{}, fmt.Errorf(
			"trusted %s model injection has no role resolver",
			role,
		)
	}
	snapshot, err := roleResolver.ResolveRoleCall(
		provider.RoleResolutionInput{
			Role:         engineconfig.RoleMain,
			MainSelector: selector,
			Requirements: provider.RoleRequirements{
				NeedReasoningHistory: messagesContainReasoning(messages),
				PromptTokens:         compact.EstimateTokenCount(messages),
			},
		},
	)
	if err != nil {
		return roleModelCall{}, err
	}
	snapshot.Role = engineconfig.ModelRole(role)
	snapshot.Source = provider.RoleCallSourceCompatibility
	compatibilityIdentity, err := modelCallIdentityFromRoleSnapshot(snapshot)
	if err != nil {
		return roleModelCall{}, err
	}
	return roleModelCall{
		Model:    e.SubagentModel,
		Identity: compatibilityIdentity,
	}, nil
}

func messagesContainReasoning(messages []*schema.Message) bool {
	for _, message := range messages {
		if message != nil && strings.TrimSpace(message.ReasoningContent) != "" {
			return true
		}
	}
	return false
}

func (e *SubAgentExecutor) reAdmitPersistedChildModelCall(
	ctx context.Context,
	metadata *session.SessionMetadataFull,
) (roleModelCall, error) {
	if metadata == nil ||
		strings.TrimSpace(metadata.ModelRole) == "" ||
		metadata.ModelBinding == nil {
		return roleModelCall{}, fmt.Errorf(
			"subagent: persisted model role admission is incomplete",
		)
	}
	role := normalizeModelRole(metadata.ModelRole)
	if role == string(engineconfig.RoleMain) ||
		role != strings.ToLower(strings.TrimSpace(metadata.ModelRole)) {
		return roleModelCall{}, fmt.Errorf(
			"subagent: persisted model role %q is invalid",
			metadata.ModelRole,
		)
	}
	agentRole := strings.TrimSpace(metadata.AgentRole)
	if agentRole == "" {
		return roleModelCall{}, fmt.Errorf(
			"subagent: persisted model role %q has no original Agent role",
			metadata.ModelRole,
		)
	}
	if expected := modelRoleForAgentType(agentRole); expected != role {
		return roleModelCall{}, fmt.Errorf(
			"subagent: persisted model role %q does not match Agent role %q",
			metadata.ModelRole,
			metadata.AgentRole,
		)
	}
	checker := &QueryEngine{config: QueryEngineConfig{
		ModelResolver: e.ModelResolver,
	}}
	admission := checker.admitResumedModelBinding(
		ctx,
		metadata.ModelBinding,
		metadata.Model,
		0,
	)
	if admission.block != nil {
		return roleModelCall{}, fmt.Errorf(
			"subagent: persisted model binding rejected: %s",
			admission.block.Code,
		)
	}
	if admission.binding == nil {
		return roleModelCall{}, fmt.Errorf(
			"subagent: persisted model binding could not be re-admitted",
		)
	}
	identity := modelCallIdentityFromBinding(role, admission.binding)
	if identity == nil {
		return roleModelCall{}, fmt.Errorf(
			"subagent: persisted model binding is invalid",
		)
	}
	return roleModelCall{Model: e.ChatModel, Identity: identity}, nil
}

func (e *SubAgentExecutor) storeAdmittedChildModelCall(
	opts tools.AgentExecOptions,
	call roleModelCall,
) {
	if e == nil || call.Identity == nil {
		return
	}
	identity := *call.Identity
	identity.binding = call.Identity.binding.Clone()
	call.Identity = &identity
	e.admittedModelCallsMu.Lock()
	if e.admittedModelCalls == nil {
		e.admittedModelCalls = make(map[string]roleModelCall)
	}
	key := childModelCallKey(opts.AgentID, opts.Generation)
	e.admittedModelCalls[key] = call
	e.admittedModelCallsMu.Unlock()
}

func (e *SubAgentExecutor) admittedChildModelCall(
	opts tools.AgentExecOptions,
) (roleModelCall, bool) {
	if e == nil {
		return roleModelCall{}, false
	}
	key := childModelCallKey(opts.AgentID, opts.Generation)
	e.admittedModelCallsMu.Lock()
	call, ok := e.admittedModelCalls[key]
	e.admittedModelCallsMu.Unlock()
	if !ok || call.Identity == nil {
		return roleModelCall{}, false
	}
	identity := *call.Identity
	identity.binding = call.Identity.binding.Clone()
	call.Identity = &identity
	return call, true
}

func (e *SubAgentExecutor) releaseAdmittedChildModelCall(
	opts tools.AgentExecOptions,
) {
	if e == nil {
		return
	}
	e.admittedModelCallsMu.Lock()
	delete(
		e.admittedModelCalls,
		childModelCallKey(opts.AgentID, opts.Generation),
	)
	e.admittedModelCallsMu.Unlock()
}
