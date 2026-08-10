package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/model"

	engineconfig "github.com/abietic/yhc/engine/config"
	"github.com/abietic/yhc/engine/provider"
	"github.com/abietic/yhc/engine/session"
)

type runtimeModelRoles interface {
	ResolveRoleCall(
		provider.RoleResolutionInput,
	) (provider.RoleCallSnapshot, error)
	UsesNamedPortfolio() bool
}

type runtimeModelFailover interface {
	ResolveFailoverChain(
		provider.RoleResolutionInput,
	) (provider.FailoverChainSnapshot, error)
}

type runtimeModelPreparer interface {
	PrepareModel(context.Context, string) (provider.ResolvedConfig, error)
}

type modelCallIdentity struct {
	Role      string
	Source    provider.RoleCallSource
	Selector  string
	Profile   string
	Provider  string
	APIModel  string
	Reasoning string
	binding   *session.PersistedModelBinding
}

type roleModelCall struct {
	Model    model.BaseChatModel
	Identity *modelCallIdentity
}

func normalizeModelRole(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(engineconfig.RoleExplore):
		return string(engineconfig.RoleExplore)
	case string(engineconfig.RolePlan):
		return string(engineconfig.RolePlan)
	case string(engineconfig.RoleGeneral):
		return string(engineconfig.RoleGeneral)
	case string(engineconfig.RoleSummary):
		return string(engineconfig.RoleSummary)
	default:
		return string(engineconfig.RoleMain)
	}
}

func modelRoleForAgentType(agentType string) string {
	switch strings.ToLower(strings.TrimSpace(agentType)) {
	case "explore":
		return string(engineconfig.RoleExplore)
	case "plan":
		return string(engineconfig.RolePlan)
	default:
		return string(engineconfig.RoleGeneral)
	}
}

func modelCallIdentityFromBinding(
	role string,
	binding *session.PersistedModelBinding,
) *modelCallIdentity {
	if binding == nil || binding.ValidateV1() != nil {
		return nil
	}
	selector := binding.Value
	profile := ""
	if binding.Kind == session.ModelBindingKindProfile {
		profile = binding.Value
	} else {
		selector = "legacy:" + binding.Value
	}
	return &modelCallIdentity{
		Role:      normalizeModelRole(role),
		Source:    provider.RoleCallSourceInheritedMain,
		Selector:  selector,
		Profile:   profile,
		Provider:  binding.Provider,
		APIModel:  binding.APIModel,
		Reasoning: binding.ReasoningEffort,
		binding:   binding.Clone(),
	}
}

func modelCallIdentityFromRoleSnapshot(
	snapshot provider.RoleCallSnapshot,
) (*modelCallIdentity, error) {
	kind := session.ModelBindingKindProfile
	value := strings.ToLower(strings.TrimSpace(snapshot.ProfileID))
	if value == "" {
		legacy, labelled := splitEngineLegacySelector(snapshot.Selector)
		if !labelled {
			return nil, fmt.Errorf(
				"model role %q returned no durable profile identity",
				snapshot.Role,
			)
		}
		kind = session.ModelBindingKindLegacy
		value = legacy
	}
	binding := &session.PersistedModelBinding{
		Version:             session.PersistedModelBindingVersion,
		Kind:                kind,
		Value:               value,
		Provider:            snapshot.Provider,
		APIModel:            snapshot.APIModel,
		PortfolioRevision:   snapshot.PortfolioRevision,
		RouteIdentityDigest: snapshot.RouteIdentityDigest,
		MetadataDigest:      snapshot.MetadataDigest,
		ContextWindowTokens: cloneModelRoleInt(snapshot.ContextWindowTokens),
		MaxOutputTokens:     cloneModelRoleInt(snapshot.MaxOutputTokens),
		ReasoningEffort:     snapshot.ReasoningEffort,
	}
	if err := binding.ValidateV1(); err != nil {
		return nil, fmt.Errorf(
			"materialize model role %q binding: %w",
			snapshot.Role,
			err,
		)
	}
	return &modelCallIdentity{
		Role:      string(snapshot.Role),
		Source:    snapshot.Source,
		Selector:  snapshot.Selector,
		Profile:   snapshot.ProfileID,
		Provider:  snapshot.Provider,
		APIModel:  snapshot.APIModel,
		Reasoning: snapshot.ReasoningEffort,
		binding:   binding,
	}, nil
}

func cloneModelRoleInt(value *int) *int {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func modelCallIdentityMatches(identity *modelCallIdentity, modelSpec string) bool {
	if identity == nil {
		return false
	}
	modelSpec = strings.TrimSpace(modelSpec)
	return strings.EqualFold(modelSpec, identity.Selector) ||
		strings.EqualFold(modelSpec, identity.Profile) ||
		strings.EqualFold(modelSpec, identity.APIModel)
}

func (e *QueryEngine) modelCallIdentitySnapshot() *modelCallIdentity {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return modelCallIdentityFromBinding(e.config.ModelRole, e.modelBinding)
}

func (e *QueryEngine) toolUseSummaryModelCall(
	ctx context.Context,
) roleModelCall {
	if e == nil ||
		!e.config.EmitToolUseSummaries ||
		strings.TrimSpace(e.config.AgentID) != "" {
		return roleModelCall{}
	}
	resolved, err := resolveRoleModelCall(
		ctx,
		e.config.ModelResolver,
		e.config.ChatModel,
		e.config.SummaryModel,
		e.config.SummaryModelSelector,
		e.modelCallIdentitySnapshot(),
		string(engineconfig.RoleSummary),
		provider.RoleRequirements{},
	)
	if err != nil {
		// Tool-use summaries are best-effort and non-authoritative. Admission
		// failure skips the side call without affecting the main turn.
		return roleModelCall{}
	}
	return resolved
}

func validateResumedModelRole(metadata session.SessionMetadata) error {
	rawRole := strings.ToLower(strings.TrimSpace(metadata.ModelRole))
	if rawRole == "" {
		return nil
	}
	role := normalizeModelRole(rawRole)
	if role != rawRole {
		return fmt.Errorf("persisted model role %q is invalid", metadata.ModelRole)
	}
	if metadata.ModelBinding == nil {
		return fmt.Errorf(
			"persisted model role %q has no model binding",
			rawRole,
		)
	}
	if strings.TrimSpace(metadata.AgentID) == "" {
		if role != string(engineconfig.RoleMain) {
			return fmt.Errorf(
				"root Session cannot resume model role %q",
				rawRole,
			)
		}
		return nil
	}
	if strings.TrimSpace(metadata.AgentRole) == "" {
		return fmt.Errorf(
			"child Session model role %q has no original Agent role",
			rawRole,
		)
	}
	expected := modelRoleForAgentType(metadata.AgentRole)
	if role != expected {
		return fmt.Errorf(
			"child Session model role %q does not match Agent role %q",
			rawRole,
			metadata.AgentRole,
		)
	}
	return nil
}

func resolveRoleModelCall(
	ctx context.Context,
	resolver ModelResolver,
	sharedModel model.BaseChatModel,
	compatibilityModel model.BaseChatModel,
	compatibilitySelector string,
	main *modelCallIdentity,
	role string,
	requirements provider.RoleRequirements,
) (roleModelCall, error) {
	role = normalizeModelRole(role)
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return roleModelCall{}, err
	}
	if main == nil {
		if compatibilityModel != nil {
			return roleModelCall{
				Model: compatibilityModel,
				Identity: &modelCallIdentity{
					Role:     role,
					Source:   provider.RoleCallSourceCompatibility,
					Selector: strings.TrimSpace(compatibilitySelector),
				},
			}, nil
		}
		return roleModelCall{}, fmt.Errorf(
			"model role %q has no admitted main binding",
			role,
		)
	}

	roleResolver, hasRoleResolver := resolver.(runtimeModelRoles)
	if !hasRoleResolver {
		identity := *main
		identity.Role = role
		identity.binding = main.binding.Clone()
		if compatibilityModel != nil {
			identity.Source = provider.RoleCallSourceCompatibility
			identity.Selector = firstNonEmptyString(
				strings.TrimSpace(compatibilitySelector),
				identity.Selector,
			)
			return roleModelCall{
				Model:    compatibilityModel,
				Identity: &identity,
			}, nil
		}
		return roleModelCall{Model: sharedModel, Identity: &identity}, nil
	}
	if !roleResolver.UsesNamedPortfolio() {
		identity := *main
		identity.Role = role
		identity.binding = main.binding.Clone()
		if compatibilityModel == nil {
			return roleModelCall{Model: sharedModel, Identity: &identity}, nil
		}
		compatibilitySelector = strings.TrimSpace(compatibilitySelector)
		if compatibilitySelector == "" {
			identity.Source = provider.RoleCallSourceCompatibility
			identity.Selector = ""
			identity.Profile = ""
			identity.Provider = ""
			identity.APIModel = ""
			identity.Reasoning = ""
			identity.binding = nil
			return roleModelCall{
				Model:    compatibilityModel,
				Identity: &identity,
			}, nil
		}
		snapshot, err := roleResolver.ResolveRoleCall(
			provider.RoleResolutionInput{
				Role:         engineconfig.RoleMain,
				MainSelector: compatibilitySelector,
				Requirements: requirements,
			},
		)
		if err != nil {
			return roleModelCall{}, fmt.Errorf(
				"admit trusted %s legacy model injection: %w",
				role,
				err,
			)
		}
		snapshot.Role = engineconfig.ModelRole(role)
		snapshot.Source = provider.RoleCallSourceCompatibility
		compatibilityIdentity, err := modelCallIdentityFromRoleSnapshot(
			snapshot,
		)
		if err != nil {
			return roleModelCall{}, err
		}
		return roleModelCall{
			Model:    compatibilityModel,
			Identity: compatibilityIdentity,
		}, nil
	}

	snapshot, err := roleResolver.ResolveRoleCall(
		provider.RoleResolutionInput{
			Role:          engineconfig.ModelRole(role),
			MainSelector:  main.Selector,
			MainReasoning: main.Reasoning,
			Requirements:  requirements,
		},
	)
	if err != nil {
		return roleModelCall{}, err
	}
	selectedModel := sharedModel
	if snapshot.Source == provider.RoleCallSourceInheritedMain &&
		compatibilityModel != nil {
		compatibilitySelector = strings.TrimSpace(compatibilitySelector)
		if compatibilitySelector == "" {
			// Summary injection historically had no separately admitted model
			// identity and remains best-effort/non-durable.
			if role == string(engineconfig.RoleSummary) {
				return roleModelCall{
					Model: compatibilityModel,
					Identity: &modelCallIdentity{
						Role:   role,
						Source: provider.RoleCallSourceCompatibility,
					},
				}, nil
			}
			return roleModelCall{}, fmt.Errorf(
				"trusted %s model injection requires a truthful model selector",
				role,
			)
		}
		snapshot, err = roleResolver.ResolveRoleCall(
			provider.RoleResolutionInput{
				Role:         engineconfig.ModelRole(role),
				MainSelector: compatibilitySelector,
				Requirements: requirements,
			},
		)
		if err != nil {
			return roleModelCall{}, fmt.Errorf(
				"admit trusted %s model injection: %w",
				role,
				err,
			)
		}
		snapshot.Source = provider.RoleCallSourceCompatibility
		selectedModel = compatibilityModel
	}
	identity, err := modelCallIdentityFromRoleSnapshot(snapshot)
	if err != nil {
		return roleModelCall{}, err
	}
	return roleModelCall{Model: selectedModel, Identity: identity}, nil
}
