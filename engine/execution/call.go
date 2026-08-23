package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/cloudwego/eino-ext/components/model/agenticark"
	"github.com/cloudwego/eino-ext/components/model/agenticclaude"
	"github.com/cloudwego/eino-ext/components/model/agenticgemini"
	"github.com/cloudwego/eino-ext/components/model/agenticopenai"
	aclopenai "github.com/cloudwego/eino-ext/libs/acl/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	openairesponses "github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
	arkresponses "github.com/volcengine/volcengine-go-sdk/service/arkruntime/model/responses"
	"google.golang.org/genai"

	"github.com/abietic/yhc/engine/internal/providerorigin"
	enginemodel "github.com/abietic/yhc/engine/model"
)

const (
	claudeAnthropicBetaHeader   = "anthropic-beta"
	claudeTaskBudgetsBetaHeader = "task-budgets-2026-03-13"
	providerAgenticDeepSeek     = "agenticdeepseek"
	providerAgenticClaude       = "agenticclaude"
	providerAgenticGemini       = "agenticgemini"
	providerAgenticOpenAI       = "agenticopenai"
	providerAgenticArk          = "agenticark"
	providerAgenticQwen         = "agenticqwen"
)

// CallModelResult wraps the result of a model call.
type CallModelResult struct {
	StreamReader      *schema.StreamReader[*schema.Message]
	Model             string
	ProviderUsageCall ProviderUsageCall
	ProviderOrigin    *providerorigin.Origin
}

// TaskBudget carries API task_budget values through the model-call seam.
type TaskBudget struct {
	Total     int
	Remaining *int
}

// QueryTracking carries nested-query lineage through the model-call seam.
type QueryTracking struct {
	ChainID string
	Depth   int
}

// ThinkingConfig controls model thinking behavior forwarded through the call seam.
type ThinkingConfig struct {
	Type         string // "adaptive", "enabled", "disabled"
	BudgetTokens *int   // optional token budget for "enabled" mode
}

// CallModelOptions holds parameters for the model call.
type CallModelOptions struct {
	SystemPrompt           *schema.Message
	UserContext            map[string]string
	ThinkingConfig         *ThinkingConfig
	Tools                  []*schema.ToolInfo
	Signal                 context.Context
	Model                  string
	Provider               string
	ModelRole              string
	ModelProfile           string
	FastMode               bool
	ToolChoice             string
	ForcedToolName         string
	IsNonInteractive       bool
	FallbackModel          string
	QuerySource            string
	QueryTracking          *QueryTracking
	AgentID                string
	SessionID              string
	SkipCacheWrite         bool
	MaxOutputTokens        *int
	TaskBudget             *TaskBudget
	EffortValue            string
	ProviderUsage          ProviderUsageAdmitter
	UsageLogicalRoundID    string
	UsageLogicalRequestID  string
	UsageModelAttemptID    string
	UsageModelAttemptIndex int
	UsageModelRetryIndex   int
	ProviderCallID         string
	ProviderCallBudget     *ModelAttemptBudget
}

// CallModel wraps eino BaseChatModel.Stream() for the query loop.
// Mirrors query.ts:659-708.
func CallModel(
	ctx context.Context,
	chatModel model.BaseChatModel,
	messages []*schema.Message,
	systemPrompt *schema.Message,
	tools []*schema.ToolInfo,
	opts CallModelOptions,
) (*CallModelResult, error) {
	effectiveSystemPrompt := systemPrompt
	if effectiveSystemPrompt == nil {
		effectiveSystemPrompt = opts.SystemPrompt
	}

	fullMessages := make([]*schema.Message, 0, len(messages)+1)
	if effectiveSystemPrompt != nil {
		fullMessages = append(fullMessages, effectiveSystemPrompt)
	}
	fullMessages = append(fullMessages, messages...)

	// Bind tools if the model supports it
	if tcm, ok := chatModel.(model.ToolCallingChatModel); ok && len(tools) > 0 {
		var err error
		chatModel, err = tcm.WithTools(tools)
		if err != nil {
			return nil, fmt.Errorf("bind tools: %w", err)
		}
	}

	effortOption, hasEffortOption, err := buildProviderEffortOption(
		opts.Provider,
		opts.EffortValue,
	)
	if err != nil {
		return nil, err
	}

	var providerUsageCall ProviderUsageCall
	if opts.ProviderUsage != nil {
		if strings.TrimSpace(opts.UsageLogicalRoundID) == "" {
			return nil, fmt.Errorf("goal provider usage requires a logical round identity")
		}
		var err error
		providerUsageCall, err = opts.ProviderUsage.AdmitProviderUsage(
			ctx,
			ProviderUsageDescriptor{
				LogicalRoundID:    opts.UsageLogicalRoundID,
				LogicalRequestID:  opts.UsageLogicalRequestID,
				ModelAttemptID:    opts.UsageModelAttemptID,
				ModelAttemptIndex: opts.UsageModelAttemptIndex,
				ModelRetryIndex:   opts.UsageModelRetryIndex,
				Model:             opts.Model,
				QuerySource:       opts.QuerySource,
				ModelRole:         opts.ModelRole,
				ModelProfile:      opts.ModelProfile,
				ReasoningEffort: strings.ToLower(
					strings.TrimSpace(opts.EffortValue),
				),
			},
		)
		if err != nil {
			return nil, err
		}
		if providerUsageCall == nil ||
			strings.TrimSpace(providerUsageCall.ProviderCallID()) == "" {
			return nil, fmt.Errorf(
				"goal provider usage admission returned no call identity",
			)
		}
		opts.ProviderCallID = providerUsageCall.ProviderCallID()
		if err := ctx.Err(); err != nil {
			return nil, ReleaseProviderUsageBeforeDispatch(providerUsageCall, err)
		}
	}

	streamOpts := make([]model.Option, 0, 7)
	if name := strings.TrimSpace(opts.Model); name != "" {
		streamOpts = append(streamOpts, model.WithModel(name))
	}
	if opts.MaxOutputTokens != nil {
		streamOpts = append(streamOpts, model.WithMaxTokens(*opts.MaxOutputTokens))
	}
	if toolChoice, allowedToolNames, ok := resolveToolChoice(opts.ToolChoice, opts.ForcedToolName); ok {
		streamOpts = append(streamOpts, model.WithToolChoice(toolChoice, allowedToolNames...))
	}
	if isClaudeCallProvider(opts.Provider) {
		if headers := buildClaudeCustomHeaders(opts); len(headers) > 0 {
			streamOpts = append(streamOpts, agenticclaude.WithCustomHeaders(headers))
		}
		if extraFields := buildClaudeExtraFields(opts); len(extraFields) > 0 {
			streamOpts = append(streamOpts, agenticclaude.WithExtraFields(extraFields))
		}
	}
	if hasEffortOption {
		streamOpts = append(streamOpts, effortOption)
	}

	if err := opts.ProviderCallBudget.ReserveProviderCall(ctx); err != nil {
		return nil, ReleaseProviderUsageBeforeDispatch(providerUsageCall, err)
	}

	// Stream from the model. The private dispatch state is created here so only
	// the actual routing owner can publish the origin selected for this call.
	dispatchCtx, dispatchState := providerorigin.WithDispatchState(ctx)
	sr, err := chatModel.Stream(dispatchCtx, fullMessages, streamOpts...)
	if err != nil {
		streamErr := fmt.Errorf("model stream: %w", err)
		return nil, MarkProviderUsageAmbiguous(providerUsageCall, streamErr)
	}

	result := &CallModelResult{
		StreamReader:      sr,
		Model:             opts.Model,
		ProviderUsageCall: providerUsageCall,
	}
	if origin, ok := dispatchState.Snapshot(); ok {
		copied := origin
		result.ProviderOrigin = &copied
	}
	return result, nil
}

func isClaudeCallProvider(raw string) bool {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	return normalized == "" ||
		normalized == providerAgenticClaude
}

func buildProviderEffortOption(
	rawProvider string,
	rawEffort string,
) (model.Option, bool, error) {
	effort := strings.ToLower(strings.TrimSpace(rawEffort))
	if effort == "" {
		return model.Option{}, false, nil
	}
	normalizedProvider := strings.ToLower(strings.TrimSpace(rawProvider))
	// Empty provider retains the pre-P29.3 trusted embedding contract, whose
	// only typed effort path was Claude output_config.effort.
	if normalizedProvider == "" {
		normalizedProvider = providerAgenticClaude
	}
	resolved, err := enginemodel.ResolveAdapterReasoningEffort(
		normalizedProvider,
		effort,
	)
	if err != nil {
		return model.Option{}, false, fmt.Errorf(
			"provider %q does not support reasoning effort %q: %w",
			normalizedProvider,
			effort,
			err,
		)
	}
	switch resolved.Dialect {
	case enginemodel.ReasoningDialectClaudeOutputConfig:
		return model.Option{}, false, nil
	case enginemodel.ReasoningDialectOpenAIResponses:
		return agenticopenai.WithResponsesReasoning(&openairesponses.ReasoningParam{
			Effort: shared.ReasoningEffort(resolved.WireEffort),
		}), true, nil
	case enginemodel.ReasoningDialectArkResponses:
		enumValue, ok := arkresponses.ReasoningEffort_Enum_value[resolved.WireEffort]
		if !ok {
			return model.Option{}, false, fmt.Errorf(
				"provider %q has no typed reasoning effort %q",
				normalizedProvider,
				resolved.WireEffort,
			)
		}
		return agenticark.WithReasoning(&arkresponses.ResponsesReasoning{
			Effort: arkresponses.ReasoningEffort_Enum(enumValue),
		}), true, nil
	case enginemodel.ReasoningDialectGeminiThinking:
		level := genai.ThinkingLevelLow
		if resolved.WireEffort == "high" {
			level = genai.ThinkingLevelHigh
		}
		return agenticgemini.WithThinkingConfig(&genai.ThinkingConfig{
			ThinkingLevel: level,
		}), true, nil
	case enginemodel.ReasoningDialectDeepSeek:
		extraFields := map[string]any{
			"thinking": map[string]any{
				"type": string(resolved.ThinkingMode),
			},
		}
		if resolved.WireEffort != "" {
			extraFields["reasoning_effort"] = resolved.WireEffort
		}
		return aclopenai.WithExtraFields(extraFields), true, nil
	default:
		return model.Option{}, false, fmt.Errorf(
			"provider %q has unknown reasoning dialect %q",
			normalizedProvider,
			resolved.Dialect,
		)
	}
}

func buildClaudeCustomHeaders(opts CallModelOptions) map[string]string {
	headers := make(map[string]string)

	// Attribution header — mirrors reference client.ts defaultHeaders.
	headers["x-app"] = "cli"

	// Session tracking — mirrors X-Claude-Code-Session-Id in the reference.
	if opts.SessionID != "" {
		headers["X-Claude-Code-Session-Id"] = opts.SessionID
	}

	// Per-request correlation ID — mirrors x-client-request-id in the reference.
	headers["x-client-request-id"] = firstNonEmptyProviderCallID(
		opts.ProviderCallID,
		uuid.New().String(),
	)

	// Beta feature header for task budgets.
	if opts.TaskBudget != nil {
		headers[claudeAnthropicBetaHeader] = claudeTaskBudgetsBetaHeader
	}

	return headers
}

func firstNonEmptyProviderCallID(primary, fallback string) string {
	if value := strings.TrimSpace(primary); value != "" {
		return value
	}
	return fallback
}

func buildClaudeExtraFields(opts CallModelOptions) map[string]any {
	var merged map[string]any

	if metadata := buildClaudeMetadata(opts.SessionID); len(metadata) > 0 {
		merged = mergeExtraFields(merged, map[string]any{"metadata": metadata})
	}
	if opts.TaskBudget != nil {
		merged = mergeExtraFields(merged, buildClaudeTaskBudgetExtraFields(opts.TaskBudget))
	}
	if thinking := buildClaudeThinkingExtraFields(opts.ThinkingConfig); len(thinking) > 0 {
		merged = mergeExtraFields(merged, thinking)
	}
	if effort := buildClaudeEffortExtraFields(opts.EffortValue); len(effort) > 0 {
		merged = mergeExtraFields(merged, effort)
	}
	if opts.FastMode {
		merged = mergeExtraFields(merged, map[string]any{"speed": "fast"})
	}

	return merged
}

func buildClaudeTaskBudgetExtraFields(taskBudget *TaskBudget) map[string]any {
	claudeTaskBudget := map[string]any{
		"type":  "tokens",
		"total": taskBudget.Total,
	}
	if taskBudget.Remaining != nil {
		claudeTaskBudget["remaining"] = *taskBudget.Remaining
	}

	return map[string]any{
		"output_config": map[string]any{
			"task_budget": claudeTaskBudget,
		},
	}
}

// buildClaudeThinkingExtraFields maps ThinkingConfig into the Claude API thinking field.
// Mirrors claude.ts:1596-1629.
//
//	"adaptive" → {"type": "adaptive"}
//	"enabled"  → {"type": "enabled", "budget_tokens": N}
//	"disabled" → omitted (no thinking field)
func buildClaudeThinkingExtraFields(config *ThinkingConfig) map[string]any {
	if config == nil {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(config.Type)) {
	case "adaptive":
		return map[string]any{
			"thinking": map[string]any{
				"type": "adaptive",
			},
		}
	case "enabled":
		thinking := map[string]any{
			"type": "enabled",
		}
		if config.BudgetTokens != nil && *config.BudgetTokens > 0 {
			thinking["budget_tokens"] = *config.BudgetTokens
		}
		return map[string]any{
			"thinking": thinking,
		}
	default:
		// "disabled" or unrecognized → no thinking field
		return nil
	}
}

// buildClaudeEffortExtraFields maps the effort value into the Claude API output_config.effort field.
// Mirrors claude.ts paramsFromContext() output_config.effort.
func buildClaudeEffortExtraFields(effort string) map[string]any {
	trimmed := strings.TrimSpace(effort)
	if trimmed == "" {
		return nil
	}
	return map[string]any{
		"output_config": map[string]any{
			"effort": trimmed,
		},
	}
}

func buildClaudeMetadata(sessionID string) map[string]any {
	extra := parseClaudeExtraMetadataEnv(os.Getenv("CLAUDE_CODE_EXTRA_METADATA"))
	userIDPayload := make(map[string]any, len(extra)+1)
	for key, value := range extra {
		userIDPayload[key] = value
	}
	if trimmedSessionID := strings.TrimSpace(sessionID); trimmedSessionID != "" {
		userIDPayload["session_id"] = trimmedSessionID
	}
	if len(userIDPayload) == 0 {
		return nil
	}

	encoded, err := json.Marshal(userIDPayload)
	if err != nil {
		return nil
	}

	return map[string]any{
		"user_id": string(encoded),
	}
}

func parseClaudeExtraMetadataEnv(raw string) map[string]any {
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil
	}

	object, ok := parsed.(map[string]any)
	if !ok {
		return nil
	}
	return object
}

func mergeExtraFields(dst, src map[string]any) map[string]any {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = make(map[string]any, len(src))
	}

	for key, value := range src {
		if existingMap, ok := dst[key].(map[string]any); ok {
			if incomingMap, ok := value.(map[string]any); ok {
				dst[key] = mergeExtraFields(existingMap, incomingMap)
				continue
			}
		}

		if incomingMap, ok := value.(map[string]any); ok {
			dst[key] = mergeExtraFields(nil, incomingMap)
			continue
		}
		dst[key] = value
	}

	return dst
}

func resolveToolChoice(raw, forcedToolName string) (schema.ToolChoice, []string, bool) {
	if name := strings.TrimSpace(forcedToolName); name != "" {
		return schema.ToolChoiceForced, []string{name}, true
	}

	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "default":
		return "", nil, false
	case string(schema.ToolChoiceAllowed), "auto":
		return schema.ToolChoiceAllowed, nil, true
	case string(schema.ToolChoiceForbidden), "none":
		return schema.ToolChoiceForbidden, nil, true
	case string(schema.ToolChoiceForced), "required":
		return schema.ToolChoiceForced, nil, true
	default:
		return "", nil, false
	}
}
