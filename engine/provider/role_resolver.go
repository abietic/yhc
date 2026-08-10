package provider

import (
	"fmt"
	"strings"

	engineconfig "github.com/abietic/yhc/engine/config"
	enginemodel "github.com/abietic/yhc/engine/model"
)

type RoleCallSource string

const (
	RoleCallSourceConfigured    RoleCallSource = "configured"
	RoleCallSourceInheritedMain RoleCallSource = "inherited_main"
	RoleCallSourceCompatibility RoleCallSource = "compatibility"
)

type RoleRequirements struct {
	NeedImage            bool
	NeedPDF              bool
	NeedReasoningHistory bool
	PromptTokens         int
	RequestedEffort      string
}

type RoleResolutionInput struct {
	Role          engineconfig.ModelRole
	MainSelector  string
	MainReasoning string
	Requirements  RoleRequirements
}

// RoleCallSnapshot is detached, non-secret, and side-effect-free. It is an
// admission result, not permission to initialize or dispatch a route.
type RoleCallSnapshot struct {
	Role                engineconfig.ModelRole
	Source              RoleCallSource
	Selector            string
	ProfileID           string
	Provider            string
	APIModel            string
	PortfolioRevision   string
	RouteIdentityDigest string
	MetadataDigest      string
	ContextWindowTokens *int
	MaxOutputTokens     *int
	ReasoningEffort     string
	Requirements        RoleRequirements
	Metadata            enginemodel.EffectiveModelMetadata
}

// FailoverCandidateSnapshot is one detached, non-secret candidate admission
// result. Admission failures are safe bounded diagnostics; resolving a
// candidate never constructs a provider route or admits provider usage.
type FailoverCandidateSnapshot struct {
	ProfileID     string
	Call          RoleCallSnapshot
	AdmissionCode string
}

// FailoverChainSnapshot freezes one role's current primary, ordered
// alternates, and shared request budgets. It is an immutable request input,
// not permission to construct or dispatch any route.
type FailoverChainSnapshot struct {
	Role              engineconfig.ModelRole
	PortfolioRevision string
	Primary           RoleCallSnapshot
	Alternates        []FailoverCandidateSnapshot
	On                []string
	MaxSwitches       int
	MaxProviderCalls  int
	MaxElapsedMS      int64
}

// ResolveRoleCall selects and admits one immutable role route without
// constructing a provider client or mutating runtime state.
func (r *Runtime) ResolveRoleCall(
	input RoleResolutionInput,
) (RoleCallSnapshot, error) {
	if r == nil {
		return RoleCallSnapshot{}, fmt.Errorf("provider runtime is not initialized")
	}
	role, err := normalizeRole(input.Role)
	if err != nil {
		return RoleCallSnapshot{}, err
	}
	selector := strings.TrimSpace(input.MainSelector)
	if selector == "" {
		selector = strings.TrimSpace(r.inventory.Default)
	}
	source := RoleCallSourceInheritedMain
	if role != engineconfig.RoleMain &&
		r.portfolio != nil &&
		r.portfolio.ExplicitRoles != nil {
		if profileID, explicit := r.portfolio.ExplicitRoles[role]; explicit {
			selector = string(profileID)
			source = RoleCallSourceConfigured
		}
	}
	if selector == "" {
		return RoleCallSnapshot{}, fmt.Errorf(
			"model role %q has no current main selector",
			role,
		)
	}
	return r.resolveExactRoleCall(input, role, selector, source)
}

func (r *Runtime) resolveExactRoleCall(
	input RoleResolutionInput,
	role engineconfig.ModelRole,
	selector string,
	source RoleCallSource,
) (RoleCallSnapshot, error) {
	entry, err := r.ResolveInventorySelector(selector)
	if err != nil {
		return RoleCallSnapshot{}, fmt.Errorf(
			"resolve model role %q: %w",
			role,
			err,
		)
	}
	if err := admitRoleMetadata(role, entry.Metadata, input.Requirements); err != nil {
		return RoleCallSnapshot{}, err
	}

	effort := strings.ToLower(strings.TrimSpace(
		input.Requirements.RequestedEffort,
	))
	if effort == "" && source == RoleCallSourceInheritedMain {
		effort = strings.ToLower(strings.TrimSpace(input.MainReasoning))
	}
	if effort == "" {
		effort = strings.ToLower(strings.TrimSpace(entry.ReasoningDefault))
	}
	if effort != "" {
		if entry.Metadata.SupportedReasoningEfforts.Source == "" ||
			entry.Metadata.SupportedReasoningEfforts.Source == "unknown" ||
			!containsNormalized(
				entry.Metadata.SupportedReasoningEfforts.Value,
				effort,
			) {
			return RoleCallSnapshot{}, fmt.Errorf(
				"model role %q reasoning effort %q is not authoritatively supported",
				role,
				effort,
			)
		}
		if !enginemodel.SupportsAdapterReasoningEffort(entry.Provider, effort) {
			return RoleCallSnapshot{}, fmt.Errorf(
				"model role %q provider %q cannot lower reasoning effort %q",
				role,
				entry.Provider,
				effort,
			)
		}
	}

	snapshot := RoleCallSnapshot{
		Role:                role,
		Source:              source,
		Selector:            entry.Selector,
		ProfileID:           entry.ProfileID,
		Provider:            entry.Provider,
		APIModel:            entry.APIModel,
		PortfolioRevision:   r.inventory.Revision,
		RouteIdentityDigest: entry.RouteIdentityDigest,
		MetadataDigest:      entry.MetadataDigest,
		ReasoningEffort:     effort,
		Requirements: RoleRequirements{
			NeedImage:            input.Requirements.NeedImage,
			NeedPDF:              input.Requirements.NeedPDF,
			NeedReasoningHistory: input.Requirements.NeedReasoningHistory,
			PromptTokens:         input.Requirements.PromptTokens,
			RequestedEffort: strings.ToLower(strings.TrimSpace(
				input.Requirements.RequestedEffort,
			)),
		},
		Metadata: cloneEffectiveMetadata(entry.Metadata),
	}
	if knownPositive(entry.Metadata.ContextWindowTokens) {
		value := entry.Metadata.ContextWindowTokens.Value
		snapshot.ContextWindowTokens = &value
	}
	if knownPositive(entry.Metadata.MaxOutputTokens) {
		value := entry.Metadata.MaxOutputTokens.Value
		snapshot.MaxOutputTokens = &value
	}
	return snapshot, nil
}

// ResolveFailoverChain freezes one role's configured failover policy and
// admits every ordered candidate without constructing provider clients.
// Candidate admission failures are returned as bounded codes so the engine
// can skip them without losing the remaining configured order.
func (r *Runtime) ResolveFailoverChain(
	input RoleResolutionInput,
) (FailoverChainSnapshot, error) {
	if r == nil {
		return FailoverChainSnapshot{}, fmt.Errorf(
			"provider runtime is not initialized",
		)
	}
	primary, err := r.ResolveRoleCall(input)
	if err != nil {
		return FailoverChainSnapshot{}, err
	}
	result := FailoverChainSnapshot{
		Role:              primary.Role,
		PortfolioRevision: primary.PortfolioRevision,
		Primary:           primary,
	}
	if r.portfolio == nil {
		return result, nil
	}
	policy, ok := r.portfolio.Failover[primary.Role]
	if !ok {
		return result, nil
	}
	result.On = append([]string(nil), policy.On...)
	result.MaxSwitches = policy.MaxSwitches
	result.MaxProviderCalls = policy.MaxProviderCalls
	result.MaxElapsedMS = policy.MaxElapsedMS
	result.Alternates = make(
		[]FailoverCandidateSnapshot,
		0,
		len(policy.Alternates),
	)
	for _, profileID := range policy.Alternates {
		candidate := FailoverCandidateSnapshot{ProfileID: string(profileID)}
		candidateInput := input
		candidateInput.MainSelector = string(profileID)
		candidateInput.MainReasoning = ""
		candidateInput.Requirements.RequestedEffort = primary.ReasoningEffort
		call, candidateErr := r.resolveExactRoleCall(
			candidateInput,
			primary.Role,
			string(profileID),
			RoleCallSourceConfigured,
		)
		if candidateErr != nil {
			candidate.AdmissionCode = failoverAdmissionCode(candidateErr)
		} else {
			candidate.Call = call
		}
		result.Alternates = append(result.Alternates, candidate)
	}
	return result, nil
}

func failoverAdmissionCode(err error) string {
	if err == nil {
		return ""
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "requires authoritative images"):
		return "capability_image"
	case strings.Contains(message, "requires authoritative pdfs"):
		return "capability_pdf"
	case strings.Contains(message, "requires authoritative thinking"):
		return "capability_reasoning_history"
	case strings.Contains(message, "reasoning effort"):
		return "capability_reasoning_effort"
	case strings.Contains(message, "context window"),
		strings.Contains(message, "exceed context"):
		return "context_window"
	default:
		return "incompatible"
	}
}

func normalizeRole(
	role engineconfig.ModelRole,
) (engineconfig.ModelRole, error) {
	switch engineconfig.ModelRole(strings.ToLower(strings.TrimSpace(string(role)))) {
	case engineconfig.RoleMain:
		return engineconfig.RoleMain, nil
	case engineconfig.RoleExplore:
		return engineconfig.RoleExplore, nil
	case engineconfig.RolePlan:
		return engineconfig.RolePlan, nil
	case engineconfig.RoleGeneral:
		return engineconfig.RoleGeneral, nil
	case engineconfig.RoleSummary:
		return engineconfig.RoleSummary, nil
	default:
		return "", fmt.Errorf("unknown model role %q", role)
	}
}

func admitRoleMetadata(
	role engineconfig.ModelRole,
	metadata enginemodel.EffectiveModelMetadata,
	requirements RoleRequirements,
) error {
	required := []struct {
		name  string
		field enginemodel.MetadataField[bool]
	}{
		{name: "text", field: metadata.Text},
		{name: "streaming", field: metadata.Streaming},
		{name: "system_prompt", field: metadata.SystemPrompt},
	}
	if role != engineconfig.RoleSummary {
		required = append(required, struct {
			name  string
			field enginemodel.MetadataField[bool]
		}{name: "tools", field: metadata.Tools})
	}
	if requirements.NeedImage {
		required = append(required, struct {
			name  string
			field enginemodel.MetadataField[bool]
		}{name: "images", field: metadata.Images})
	}
	if requirements.NeedPDF {
		required = append(required, struct {
			name  string
			field enginemodel.MetadataField[bool]
		}{name: "pdfs", field: metadata.PDFs})
	}
	if requirements.NeedReasoningHistory {
		required = append(required, struct {
			name  string
			field enginemodel.MetadataField[bool]
		}{name: "thinking", field: metadata.Thinking})
	}
	for _, capability := range required {
		if capability.field.Source == "" ||
			capability.field.Source == "unknown" ||
			!capability.field.Value {
			return fmt.Errorf(
				"model role %q requires authoritative %s capability",
				role,
				capability.name,
			)
		}
	}
	if requirements.PromptTokens < 0 {
		return fmt.Errorf("model role %q prompt tokens must not be negative", role)
	}
	if requirements.PromptTokens > 0 {
		if !knownPositive(metadata.ContextWindowTokens) {
			return fmt.Errorf(
				"model role %q requires an authoritative context window",
				role,
			)
		}
		if requirements.PromptTokens > metadata.ContextWindowTokens.Value {
			return fmt.Errorf(
				"model role %q prompt tokens %d exceed context window %d",
				role,
				requirements.PromptTokens,
				metadata.ContextWindowTokens.Value,
			)
		}
	}
	return nil
}

func knownPositive(field enginemodel.MetadataField[int]) bool {
	return field.Source != "" &&
		field.Source != "unknown" &&
		field.Value > 0
}

func containsNormalized(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}
