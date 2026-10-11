package engine

import (
	"fmt"
	"strings"

	engineconfig "github.com/abietic/yhc/engine/config"
	"github.com/abietic/yhc/engine/execution"
	enginemodel "github.com/abietic/yhc/engine/model"
	"github.com/abietic/yhc/engine/provider"
)

func normalizedVerificationEffort(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func (cfg IndependentVerificationConfig) validateCallPolicies(limits execution.RunUsageLimits) error {
	for _, policy := range []struct {
		effort  string
		cap     int
		enabled bool
	}{{cfg.CheckerEffort, cfg.CheckerMaxOutputTokens, cfg.MaxTurns > 0}, {cfg.CoverageEffort, cfg.CoverageMaxOutputTokens, cfg.MaxTurns > 0 && cfg.CoverageReview}} {
		effort, err := enginemodel.ValidateReasoningEffort(policy.effort)
		if err != nil {
			return fmt.Errorf("verification effort: %w", err)
		}
		if !policy.enabled && (effort != "" || policy.cap != 0) {
			return fmt.Errorf("verification call policy requires its stage to be enabled")
		}
		if policy.cap < 0 || (limits.MaxOutputTokensPerCall > 0 && policy.cap > limits.MaxOutputTokensPerCall) {
			return fmt.Errorf("verification output limit must be nonnegative and cannot exceed the global per-call cap")
		}
	}
	return nil
}

// Each stage derives its intent from the root, so a checker override cannot
// accidentally propagate into a default coverage review or a later repair.
// No route/client is constructed, and the root binding remains immutable.
func applyVerificationCallPolicy(p *QueryParams, root QueryParams, coverage bool) error {
	cfg := root.IndependentVerification
	effort, cap := cfg.CheckerEffort, cfg.CheckerMaxOutputTokens
	if coverage {
		effort, cap = cfg.CoverageEffort, cfg.CoverageMaxOutputTokens
	}
	effort = normalizedVerificationEffort(effort)
	if root.modelCall != nil {
		p.modelCall = root.modelCall
	}
	p.modelOutputTokenLimit = root.modelOutputTokenLimit
	if cap > 0 {
		if root.modelCall != nil && root.modelCall.binding != nil && root.modelCall.binding.MaxOutputTokens != nil && cap > *root.modelCall.binding.MaxOutputTokens {
			return fmt.Errorf("verification output limit exceeds the admitted model limit")
		}
		p.modelOutputTokenLimit = cap
	}
	if effort != "" {
		resolver, ok := root.modelResolver.(runtimeModelRoles)
		if !ok || root.modelCall == nil || root.modelCall.binding == nil {
			return fmt.Errorf("verification effort requires an admitted provider runtime binding")
		}
		main := root.modelCall
		call, err := resolver.ResolveRoleCall(provider.RoleResolutionInput{Role: engineconfig.RoleMain, MainSelector: main.Selector, MainReasoning: main.Reasoning, Requirements: provider.RoleRequirements{RequestedEffort: effort}})
		if err != nil {
			return fmt.Errorf("admit verification effort: %w", err)
		}
		binding := main.binding
		if call.Selector != main.Selector || call.ProfileID != main.Profile || call.Provider != main.Provider || call.APIModel != main.APIModel || call.PortfolioRevision != binding.PortfolioRevision || call.RouteIdentityDigest != binding.RouteIdentityDigest || call.MetadataDigest != binding.MetadataDigest || call.ReasoningEffort != effort {
			return fmt.Errorf("verification policy changed the admitted root route")
		}
		identity := *main
		identity.binding = binding.Clone()
		identity.Reasoning = effort
		identity.binding.ReasoningEffort = effort
		p.modelCall = &identity
	}
	// The caller has already narrowed tools and permissions. Clone that context,
	// then update only request intent; keep review tool denial and checker policy.
	if p.modelCall != nil && (effort != "" || (coverage && normalizedVerificationEffort(cfg.CheckerEffort) != "")) {
		p.ToolUseContext = clonePermissionReviewToolContext(p.ToolUseContext)
		if p.ToolUseContext == nil {
			p.ToolUseContext = &ToolUseContext{}
		}
		if p.ToolUseContext.Options == nil {
			p.ToolUseContext.Options = &ToolUseOptions{}
		}
		p.ToolUseContext.Options.MainLoopModel = p.modelCall.Selector
		p.ToolUseContext.Options.ModelSetting = ""
		p.ToolUseContext.Options.EffortValue = p.modelCall.Reasoning
	}
	return nil
}
