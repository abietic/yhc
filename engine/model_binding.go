package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/compact"
	modelcaps "github.com/abietic/yhc/engine/model"
	"github.com/abietic/yhc/engine/provider"
	"github.com/abietic/yhc/engine/session"
)

var errModelReasoningUnsupported = errors.New(
	"model reasoning effort is unsupported",
)

const (
	ModelDispatchBlockInvalidBinding      = "model_binding_invalid"
	ModelDispatchBlockUnsupportedVersion  = "model_binding_unsupported_version"
	ModelDispatchBlockRebindRequired      = "model_binding_rebind_required"
	ModelDispatchBlockRouteChanged        = "model_binding_route_changed"
	ModelDispatchBlockRouteRevision       = "model_binding_route_revision_changed"
	ModelDispatchBlockMetadataChanged     = "model_binding_metadata_incompatible"
	ModelDispatchBlockCompactRequired     = "model_binding_compact_required"
	ModelDispatchBlockCheckpointUncertain = "model_binding_checkpoint_uncertain"
)

// ModelDispatchBlock is process-local fail-closed admission state. It carries
// only a stable code, a validated safe selector, and remediation text.
type ModelDispatchBlock struct {
	Code        string
	Selector    string
	Remediation string
	ContextOnly bool
}

type runtimeModelInventory interface {
	ModelResolver
	InventorySnapshot() provider.RuntimeInventorySnapshot
	ResolveInventorySelector(string) (provider.RuntimeInventoryEntry, error)
}

type runtimePortfolioMode interface {
	UsesNamedPortfolio() bool
}

type resumedModelAdmission struct {
	selector  string
	binding   *session.PersistedModelBinding
	block     *ModelDispatchBlock
	reasoning string
	warnings  []string
}

// ModelInventory returns the detached provider-owned inventory with an active
// labelled legacy selection overlaid when it is outside the configured list.
func (e *QueryEngine) ModelInventory() provider.RuntimeInventorySnapshot {
	if e == nil {
		return provider.RuntimeInventorySnapshot{}
	}
	inventory, ok := e.config.ModelResolver.(runtimeModelInventory)
	if !ok {
		return provider.RuntimeInventorySnapshot{}
	}
	snapshot := inventory.InventorySnapshot()
	e.mu.Lock()
	binding := e.modelBinding.Clone()
	e.mu.Unlock()
	projection := session.SafeModelBindingProjection(binding)
	if projection.State != session.ModelBindingStateValid ||
		projection.Kind != session.ModelBindingKindLegacy {
		return snapshot
	}
	selector := "legacy:" + projection.Value
	for _, entry := range snapshot.Entries {
		if strings.EqualFold(entry.Selector, selector) {
			return snapshot
		}
	}
	entry, err := inventory.ResolveInventorySelector(selector)
	if err != nil {
		return snapshot
	}
	snapshot.Entries = append(snapshot.Entries, entry)
	sort.SliceStable(snapshot.Entries, func(i, j int) bool {
		return strings.ToLower(snapshot.Entries[i].Selector) <
			strings.ToLower(snapshot.Entries[j].Selector)
	})
	return snapshot
}

// ModelDispatchBlockSnapshot returns detached safe process-local admission
// state for diagnostics and focused tests.
func (e *QueryEngine) ModelDispatchBlockSnapshot() *ModelDispatchBlock {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return cloneModelDispatchBlock(e.modelDispatchBlock)
}

func cloneModelDispatchBlock(block *ModelDispatchBlock) *ModelDispatchBlock {
	if block == nil {
		return nil
	}
	clone := *block
	return &clone
}

func (e *QueryEngine) initializeModelBinding() {
	inventory, ok := e.config.ModelResolver.(runtimeModelInventory)
	if !ok {
		return
	}
	snapshot := inventory.InventorySnapshot()
	selector := strings.TrimSpace(e.config.Model)
	if snapshot.Default != "" {
		for _, entry := range snapshot.Entries {
			if strings.EqualFold(entry.Selector, snapshot.Default) &&
				(selector == "" ||
					strings.EqualFold(selector, entry.APIModel) ||
					strings.EqualFold(selector, entry.ProfileID)) {
				selector = snapshot.Default
				break
			}
		}
	}
	state, binding, err := e.resolveModelBindingCandidate(
		context.Background(),
		selector,
		"",
	)
	if err != nil || binding == nil {
		code := ModelDispatchBlockRebindRequired
		safeSelector := ""
		remediation := "select an available model explicitly"
		if entry, resolveErr := inventory.ResolveInventorySelector(
			selector,
		); resolveErr == nil {
			safeSelector = entry.Selector
			if metadataErr := validateMainRouteMetadataForInventory(
				inventory,
				entry,
			); metadataErr != nil {
				code = ModelDispatchBlockMetadataChanged
				remediation = "select a model with compatible required capabilities"
			}
		}
		e.modelDispatchBlock = newModelDispatchBlock(
			code,
			safeSelector,
			remediation,
			false,
		)
		return
	}
	e.config.Model = state.Requested
	e.modelBinding = binding
	e.reasoningEffort = state.ReasoningEffort
}

func (e *QueryEngine) initializeAdmittedModelBinding(
	persisted *session.PersistedModelBinding,
) {
	if e == nil {
		return
	}
	admission := e.admitResumedModelBinding(
		context.Background(),
		persisted,
		e.config.Model,
		0,
	)
	if strings.TrimSpace(admission.selector) != "" {
		e.config.Model = admission.selector
	}
	e.modelBinding = admission.binding.Clone()
	e.modelDispatchBlock = cloneModelDispatchBlock(admission.block)
	e.reasoningEffort = admission.reasoning
}

func (e *QueryEngine) resolveModelBindingCandidate(
	ctx context.Context,
	modelSpec string,
	reasoning string,
) (ModelControlState, *session.PersistedModelBinding, error) {
	inventory, ok := e.config.ModelResolver.(runtimeModelInventory)
	if !ok {
		state, err := e.resolveModelControl(ctx, modelSpec)
		if err == nil {
			reasoning = strings.ToLower(strings.TrimSpace(reasoning))
			if state.SupportsReasoningEffort {
				state.ReasoningEffort = reasoning
			} else {
				state.ReasoningEffort = ""
			}
		}
		return state, nil, err
	}
	entry, err := inventory.ResolveInventorySelector(modelSpec)
	if err != nil {
		return ModelControlState{}, nil, err
	}
	if err := validateMainRouteMetadataForInventory(inventory, entry); err != nil {
		return ModelControlState{}, nil, err
	}
	resolved, err := inventory.ResolveModel(entry.Selector)
	if err != nil {
		return ModelControlState{}, nil, err
	}
	if string(resolved.Provider) != entry.Provider ||
		resolved.Model != entry.APIModel {
		return ModelControlState{}, nil,
			fmt.Errorf("provider inventory returned inconsistent resolved identity")
	}
	state := ModelControlState{
		Requested: entry.Selector,
		Provider:  resolved.Provider,
		Model:     resolved.Model,
	}
	_, legacyRoute := splitEngineLegacySelector(entry.Selector)
	state.SupportsReasoningEffort = inventoryHasAdapterReasoningEffort(
		entry,
		legacyRoute,
	)
	reasoning = strings.ToLower(strings.TrimSpace(reasoning))
	if reasoning == "" {
		reasoning = strings.ToLower(strings.TrimSpace(entry.ReasoningDefault))
	}
	if reasoning != "" {
		metadataSupported := inventoryReasoningEffortSupported(entry, reasoning)
		if legacyRoute &&
			resolved.Provider == provider.ProviderAgenticClaude &&
			entry.Metadata.Thinking.Source != "" &&
			entry.Metadata.Thinking.Source != "unknown" &&
			entry.Metadata.Thinking.Value {
			metadataSupported = true
		}
		if !metadataSupported ||
			!modelcaps.SupportsAdapterReasoningEffort(
				entry.Provider,
				reasoning,
			) {
			return ModelControlState{}, nil, fmt.Errorf(
				"%w: model selector %q does not support reasoning effort %q",
				errModelReasoningUnsupported,
				entry.Selector,
				reasoning,
			)
		}
	}
	state.ReasoningEffort = reasoning

	snapshot := inventory.InventorySnapshot()
	kind := session.ModelBindingKindProfile
	value := strings.ToLower(strings.TrimSpace(entry.ProfileID))
	if legacy, labelled := splitEngineLegacySelector(entry.Selector); labelled {
		kind = session.ModelBindingKindLegacy
		value = legacy
	}
	binding := &session.PersistedModelBinding{
		Version:             session.PersistedModelBindingVersion,
		Kind:                kind,
		Value:               value,
		Provider:            entry.Provider,
		APIModel:            entry.APIModel,
		PortfolioRevision:   snapshot.Revision,
		RouteIdentityDigest: entry.RouteIdentityDigest,
		MetadataDigest:      entry.MetadataDigest,
		ContextWindowTokens: knownPositiveMetadataInt(
			entry.Metadata.ContextWindowTokens,
		),
		MaxOutputTokens: knownPositiveMetadataInt(
			entry.Metadata.MaxOutputTokens,
		),
		ReasoningEffort: reasoning,
	}
	if err := binding.ValidateV1(); err != nil {
		return ModelControlState{}, nil,
			fmt.Errorf("materialize model binding: %w", err)
	}
	return state, binding, nil
}

func inventoryReasoningEffortSupported(
	entry provider.RuntimeInventoryEntry,
	effort string,
) bool {
	field := entry.Metadata.SupportedReasoningEfforts
	if field.Source == "" || field.Source == "unknown" {
		return false
	}
	for _, supported := range field.Value {
		if strings.EqualFold(strings.TrimSpace(supported), effort) {
			return true
		}
	}
	return false
}

func inventoryHasAdapterReasoningEffort(
	entry provider.RuntimeInventoryEntry,
	legacyRoute bool,
) bool {
	if legacyRoute &&
		entry.Provider == string(provider.ProviderAgenticClaude) &&
		entry.Metadata.Thinking.Source != "" &&
		entry.Metadata.Thinking.Source != "unknown" &&
		entry.Metadata.Thinking.Value {
		return true
	}
	field := entry.Metadata.SupportedReasoningEfforts
	if field.Source == "" || field.Source == "unknown" {
		return false
	}
	for _, effort := range field.Value {
		if modelcaps.SupportsAdapterReasoningEffort(
			entry.Provider,
			effort,
		) {
			return true
		}
	}
	return false
}

func splitEngineLegacySelector(selector string) (string, bool) {
	trimmed := strings.TrimSpace(selector)
	if !strings.HasPrefix(strings.ToLower(trimmed), "legacy:") {
		return "", false
	}
	return strings.TrimSpace(trimmed[len("legacy:"):]), true
}

func knownPositiveMetadataInt(
	field modelcaps.MetadataField[int],
) *int {
	if field.Source == "" || field.Source == "unknown" || field.Value <= 0 {
		return nil
	}
	value := field.Value
	return &value
}

func validateMainRouteMetadata(
	entry provider.RuntimeInventoryEntry,
) error {
	required := []struct {
		name  string
		value modelcaps.MetadataField[bool]
	}{
		{name: "text", value: entry.Metadata.Text},
		{name: "streaming", value: entry.Metadata.Streaming},
		{name: "tools", value: entry.Metadata.Tools},
		{name: "system_prompt", value: entry.Metadata.SystemPrompt},
	}
	for _, fact := range required {
		if fact.value.Source == "" || fact.value.Source == "unknown" {
			return fmt.Errorf(
				"model selector %q has unknown required %s capability",
				entry.Selector,
				fact.name,
			)
		}
		if !fact.value.Value {
			return fmt.Errorf(
				"model selector %q does not support required %s capability",
				entry.Selector,
				fact.name,
			)
		}
	}
	return nil
}

func validateMainRouteMetadataForInventory(
	inventory runtimeModelInventory,
	entry provider.RuntimeInventoryEntry,
) error {
	if _, legacyRoute := splitEngineLegacySelector(entry.Selector); legacyRoute {
		mode, authoritative := inventory.(runtimePortfolioMode)
		if authoritative && !mode.UsesNamedPortfolio() {
			return nil
		}
	}
	return validateMainRouteMetadata(entry)
}

func (e *QueryEngine) admitResumedModelBinding(
	ctx context.Context,
	persisted *session.PersistedModelBinding,
	legacyModel string,
	tokenEstimate int,
) resumedModelAdmission {
	if persisted == nil {
		selector := strings.TrimSpace(legacyModel)
		if selector == "" {
			return resumedModelAdmission{}
		}
		state, binding, err := e.resolveModelBindingCandidate(
			ctx,
			"legacy:"+selector,
			"",
		)
		if err != nil {
			return resumedModelAdmission{selector: selector}
		}
		return resumedModelAdmission{
			selector: state.Requested,
			binding:  binding,
		}
	}

	projection := session.SafeModelBindingProjection(persisted)
	switch projection.State {
	case session.ModelBindingStateInvalid:
		return resumedModelAdmission{
			selector: strings.TrimSpace(legacyModel),
			binding:  persisted.Clone(),
			block: newModelDispatchBlock(
				ModelDispatchBlockInvalidBinding,
				"",
				"select a model explicitly to replace the invalid binding",
				false,
			),
		}
	case session.ModelBindingStateUnsupportedVersion:
		return resumedModelAdmission{
			selector: strings.TrimSpace(legacyModel),
			binding:  persisted.Clone(),
			block: newModelDispatchBlock(
				ModelDispatchBlockUnsupportedVersion,
				"",
				"select a model explicitly with a compatible runtime",
				false,
			),
		}
	}

	selector := projection.Value
	if projection.Kind == session.ModelBindingKindLegacy {
		selector = "legacy:" + projection.Value
	}
	resumeReasoning := persisted.ReasoningEffort
	clearPersistedReasoning := false
	if resumeReasoning != "" {
		if inventory, ok := e.config.ModelResolver.(runtimeModelInventory); ok {
			if entry, resolveErr := inventory.ResolveInventorySelector(
				selector,
			); resolveErr == nil {
				metadataSupported := inventoryReasoningEffortSupported(
					entry,
					resumeReasoning,
				)
				_, legacyRoute := splitEngineLegacySelector(entry.Selector)
				if legacyRoute &&
					entry.Provider == string(provider.ProviderAgenticClaude) &&
					entry.Metadata.Thinking.Source != "" &&
					entry.Metadata.Thinking.Source != "unknown" &&
					entry.Metadata.Thinking.Value {
					metadataSupported = true
				}
				if !metadataSupported ||
					!modelcaps.SupportsAdapterReasoningEffort(
						entry.Provider,
						resumeReasoning,
					) {
					resumeReasoning = ""
					clearPersistedReasoning = true
				}
			}
		}
	}
	state, candidate, err := e.resolveModelBindingCandidate(
		ctx,
		selector,
		resumeReasoning,
	)
	if err != nil {
		code := ModelDispatchBlockRebindRequired
		remediation := "select an available model explicitly"
		if inventory, ok := e.config.ModelResolver.(runtimeModelInventory); ok {
			if entry, resolveErr := inventory.ResolveInventorySelector(
				selector,
			); resolveErr == nil {
				if metadataErr := validateMainRouteMetadataForInventory(
					inventory,
					entry,
				); metadataErr != nil {
					code = ModelDispatchBlockMetadataChanged
					remediation = "select a model with compatible required capabilities"
				}
			}
		}
		return resumedModelAdmission{
			selector: selector,
			binding:  persisted.Clone(),
			block: newModelDispatchBlock(
				code,
				selector,
				remediation,
				false,
			),
		}
	}
	admission := resumedModelAdmission{
		selector:  state.Requested,
		binding:   candidate,
		reasoning: state.ReasoningEffort,
	}
	if clearPersistedReasoning {
		state.ReasoningEffort = ""
		candidate.ReasoningEffort = ""
		admission.binding = candidate
		admission.reasoning = ""
	}
	if persisted.Provider != candidate.Provider ||
		persisted.APIModel != candidate.APIModel {
		admission.binding = persisted.Clone()
		admission.block = newModelDispatchBlock(
			ModelDispatchBlockRouteChanged,
			selector,
			"review the changed provider model and rebind explicitly",
			false,
		)
		return admission
	}
	if persisted.RouteIdentityDigest != candidate.RouteIdentityDigest {
		admission.binding = persisted.Clone()
		admission.block = newModelDispatchBlock(
			ModelDispatchBlockRouteRevision,
			selector,
			"review the changed route and rebind explicitly",
			false,
		)
		return admission
	}
	if persisted.MetadataDigest != candidate.MetadataDigest {
		admission.warnings = append(
			admission.warnings,
			"model_binding_metadata_changed: compatible model metadata was re-admitted",
		)
	}
	if persisted.PortfolioRevision != candidate.PortfolioRevision {
		admission.warnings = append(
			admission.warnings,
			"model_binding_portfolio_changed: compatible portfolio revision was accepted",
		)
	}
	if limitDecreased(
		persisted.MaxOutputTokens,
		candidate.MaxOutputTokens,
	) {
		admission.warnings = append(
			admission.warnings,
			"model_binding_output_limit_decreased: the new limit applies to future turns",
		)
	}
	if limitDecreased(
		persisted.ContextWindowTokens,
		candidate.ContextWindowTokens,
	) {
		admission.warnings = append(
			admission.warnings,
			"model_binding_context_limit_decreased: current history was rechecked",
		)
	}
	if persisted.ReasoningEffort != "" &&
		state.ReasoningEffort == "" {
		admission.warnings = append(
			admission.warnings,
			"model_binding_reasoning_cleared: persisted reasoning is not supported by the current route",
		)
	}
	warning := compact.CalculateTokenWarningStateForContextWindow(
		tokenEstimate,
		candidate.APIModel,
		candidate.ContextWindowTokens,
	)
	if warning.IsAtBlockingLimit {
		admission.block = newModelDispatchBlock(
			ModelDispatchBlockCompactRequired,
			selector,
			"compact the Session successfully or select a compatible model",
			true,
		)
	}
	return admission
}

func newModelDispatchBlock(
	code string,
	selector string,
	remediation string,
	contextOnly bool,
) *ModelDispatchBlock {
	return &ModelDispatchBlock{
		Code:        code,
		Selector:    selector,
		Remediation: remediation,
		ContextOnly: contextOnly,
	}
}

func limitDecreased(previous, current *int) bool {
	return previous != nil && current != nil && *current < *previous
}

func (e *QueryEngine) checkModelDispatch(_ string) error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	block := cloneModelDispatchBlock(e.modelDispatchBlock)
	e.mu.Unlock()
	if block == nil {
		return nil
	}
	return fmt.Errorf(
		"%s: model dispatch blocked; %s",
		block.Code,
		block.Remediation,
	)
}

func (e *QueryEngine) checkModelCompaction(_ string) error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	block := cloneModelDispatchBlock(e.modelDispatchBlock)
	e.mu.Unlock()
	if block == nil || block.ContextOnly {
		return nil
	}
	return fmt.Errorf(
		"%s: model compaction blocked; %s",
		block.Code,
		block.Remediation,
	)
}

func (e *QueryEngine) clearContextModelDispatchBlock(
	messages []*schema.Message,
) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.modelDispatchBlock == nil ||
		!e.modelDispatchBlock.ContextOnly ||
		e.modelBinding == nil {
		return
	}
	warning := compact.CalculateTokenWarningStateForContextWindow(
		compact.EstimateTokenCount(messages),
		e.modelBinding.APIModel,
		e.modelBinding.ContextWindowTokens,
	)
	if !warning.IsAtBlockingLimit {
		e.modelDispatchBlock = nil
	}
}
