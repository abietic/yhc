package compact

import (
	"context"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/execution"
	modelcap "github.com/abietic/yhc/engine/model"
)

const (
	defaultEffectiveContextWindow = 200000
	autoCompactBufferTokens       = 13000
	warningThresholdBufferTokens  = 20000
	errorThresholdBufferTokens    = 20000
	manualCompactBufferTokens     = 3000

	maxConsecutiveAutoCompactFailures = 3
	autoCompactPreservedTailMessages  = 2
)

// TokenWarningState mirrors the threshold helpers from the reference runtime.
// The Go port intentionally keeps the model sizing simple for now: until
// provider/model capability tables land, we assume a 200k effective window.
type TokenWarningState struct {
	PercentLeft                 int
	IsAboveWarningThreshold     bool
	IsAboveErrorThreshold       bool
	IsAboveAutoCompactThreshold bool
	IsAtBlockingLimit           bool
}

// CompactTracking is minimal tracking for this package (avoids circular imports with engine).
type CompactTracking struct {
	Compacted           bool
	TurnID              string
	TurnCounter         int
	ConsecutiveFailures int
}

// AutoCompactResult holds the result of proactive auto-compaction.
type AutoCompactResult struct {
	PreCompactTokenCount      int
	PostCompactTokenCount     int
	TruePostCompactTokenCount int
	CompactionUsage           *schema.TokenUsage
	BoundaryMarker            *schema.Message
	SummaryMessages           []*schema.Message
	MessagesToKeep            []*schema.Message
	Attachments               []*schema.Message
	HookResults               []*schema.Message
	Summary                   string // Formatted summary text for display (analysis stripped).
}

// GetEffectiveContextWindowSize returns the usable input budget for the main
// loop. Uses the centralized model capability table; falls back to the local
// map if the model is not found in the centralized table, and ultimately to
// the default 200k if the model is unknown.
func GetEffectiveContextWindowSize(model string) int {
	window := defaultEffectiveContextWindow
	if model != "" {
		// Prefer centralized model capability table
		centralWindow := modelcap.ContextWindow(model)
		if centralWindow > 0 {
			window = centralWindow
		} else if modelWindow, ok := modelContextWindows[strings.ToLower(model)]; ok {
			// Fallback to local map for backwards compatibility
			window = modelWindow
		}
	}

	if override := strings.TrimSpace(os.Getenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW")); override != "" {
		if parsed, err := strconv.Atoi(override); err == nil && parsed > 0 && parsed < window {
			window = parsed
		}
	}

	return window
}

// modelContextWindows is the legacy local fallback map. The centralized
// model.ContextWindow() function is preferred; this map is retained for
// backwards compatibility with any models not yet in the centralized table.
var modelContextWindows = map[string]int{
	// Claude 3.5 / 4 family
	"claude-sonnet-4-20250514":   200000,
	"claude-3-5-sonnet-20241022": 200000,
	"claude-3-5-sonnet-20240620": 200000,
	"claude-3-5-haiku-20241022":  200000,
	"claude-3-opus-20240229":     200000,
	"claude-3-sonnet-20240229":   200000,
	"claude-3-haiku-20240307":    200000,
	"claude-sonnet-4":            200000,
	"claude-3-5-sonnet":          200000,
	"claude-3-5-haiku":           200000,
	"claude-3-opus":              200000,
	"claude-3-sonnet":            200000,
	"claude-3-haiku":             200000,
	// GPT-4 family
	"gpt-4":               8192,
	"gpt-4-turbo":         128000,
	"gpt-4-turbo-preview": 128000,
	"gpt-4o":              128000,
	"gpt-4o-mini":         128000,
	// Gemini
	"gemini-1.5-pro":   1000000,
	"gemini-1.5-flash": 1000000,
	"gemini-2.0-flash": 1000000,
}

// GetAutoCompactThreshold returns the proactive compaction threshold.
func GetAutoCompactThreshold(model string) int {
	effectiveWindow := GetEffectiveContextWindowSize(model)
	return autoCompactThresholdForWindow(effectiveWindow)
}

func autoCompactThresholdForWindow(effectiveWindow int) int {
	threshold := effectiveWindow - autoCompactBufferTokens
	if threshold < 0 {
		threshold = 0
	}

	if override := strings.TrimSpace(os.Getenv("CLAUDE_AUTOCOMPACT_PCT_OVERRIDE")); override != "" {
		if parsed, err := strconv.ParseFloat(override, 64); err == nil && parsed > 0 && parsed <= 100 {
			percentageThreshold := int(math.Floor(float64(effectiveWindow) * (parsed / 100)))
			if percentageThreshold < threshold {
				return percentageThreshold
			}
		}
	}

	return threshold
}

// CalculateTokenWarningState computes the same high-water marks the query loop
// uses for warnings, autocompaction, and the hard blocking limit.
func CalculateTokenWarningState(tokenUsage int, model string) TokenWarningState {
	return calculateTokenWarningState(
		tokenUsage,
		GetEffectiveContextWindowSize(model),
	)
}

// CalculateTokenWarningStateForContextWindow applies the existing warning,
// auto-compact, and blocking buffers to an authoritative positive context
// limit. Nil or non-positive limits preserve the model-capability fallback.
func CalculateTokenWarningStateForContextWindow(
	tokenUsage int,
	model string,
	contextWindow *int,
) TokenWarningState {
	effectiveWindow := GetEffectiveContextWindowSize(model)
	if contextWindow != nil && *contextWindow > 0 {
		effectiveWindow = *contextWindow
	}
	return calculateTokenWarningState(tokenUsage, effectiveWindow)
}

func calculateTokenWarningState(
	tokenUsage int,
	effectiveWindow int,
) TokenWarningState {
	if tokenUsage < 0 {
		tokenUsage = 0
	}

	autoCompactThreshold := autoCompactThresholdForWindow(effectiveWindow)
	percentLeft := 0
	if effectiveWindow > 0 {
		percentLeft = int(math.Max(0, math.Round((float64(effectiveWindow-tokenUsage)/float64(effectiveWindow))*100)))
	}

	warningThreshold := effectiveWindow - warningThresholdBufferTokens
	if warningThreshold < 0 {
		warningThreshold = 0
	}
	errorThreshold := effectiveWindow - errorThresholdBufferTokens
	if errorThreshold < 0 {
		errorThreshold = 0
	}
	blockingLimit := effectiveWindow - manualCompactBufferTokens
	if blockingLimit < 0 {
		blockingLimit = effectiveWindow
	}
	if override := strings.TrimSpace(os.Getenv("CLAUDE_CODE_BLOCKING_LIMIT_OVERRIDE")); override != "" {
		if parsed, err := strconv.Atoi(override); err == nil && parsed > 0 {
			blockingLimit = parsed
		}
	}

	return TokenWarningState{
		PercentLeft:                 percentLeft,
		IsAboveWarningThreshold:     tokenUsage >= warningThreshold,
		IsAboveErrorThreshold:       tokenUsage >= errorThreshold,
		IsAboveAutoCompactThreshold: tokenUsage >= autoCompactThreshold,
		IsAtBlockingLimit:           tokenUsage >= blockingLimit,
	}
}

// EstimateTokenCount is the current Go-port stand-in for tokenCountWithEstimation.
// It intentionally uses a simple text-and-tool-call heuristic until provider
// usage accounting and message normalization are fully migrated.
func EstimateTokenCount(messages []*schema.Message) int {
	total := 0
	for _, msg := range messages {
		total += estimateMessageTokens(msg)
	}
	return total
}

func estimateMessageTokens(msg *schema.Message) int {
	if msg == nil {
		return 0
	}

	total := 8
	total += roughTextTokens(msg.Content)
	total += roughTextTokens(msg.ReasoningContent)
	total += roughTextTokens(msg.Name)
	total += roughTextTokens(msg.ToolCallID)
	total += roughTextTokens(msg.ToolName)

	if len(msg.MultiContent) > 0 {
		total += len(msg.MultiContent) * 32
	}
	if len(msg.UserInputMultiContent) > 0 {
		total += len(msg.UserInputMultiContent) * 32
	}
	if len(msg.AssistantGenMultiContent) > 0 {
		total += len(msg.AssistantGenMultiContent) * 32
	}

	for _, tc := range msg.ToolCalls {
		total += 12
		total += roughTextTokens(tc.ID)
		total += roughTextTokens(tc.Type)
		total += roughTextTokens(tc.Function.Name)
		total += roughTextTokens(tc.Function.Arguments)
	}

	return total
}

func roughTextTokens(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	return int(math.Ceil(float64(len(text)) / 4.0))
}

// AutoCompactParams holds optional parameters for AutoCompact that enable
// LLM-driven summarization when a ChatModel is available.
type AutoCompactParams struct {
	// Ctx is the context for LLM calls. Required for LLM compaction.
	Ctx context.Context
	// ChatModel enables LLM-driven summarization when non-nil.
	ChatModel model.BaseChatModel
	// CustomInstructions are user-supplied summarization directives.
	CustomInstructions string
	// SuppressFollowUpQuestions instructs the model to continue without asking.
	SuppressFollowUpQuestions bool
	// TranscriptPath is included in the summary for reference.
	TranscriptPath string
	// PreferPartial enables partial (bidirectional) compaction when possible.
	// When true and enough messages exist, AutoCompact will compact only the
	// older half of the conversation, preserving recent messages verbatim.
	PreferPartial bool
	// ProviderUsage attributes compaction requests to an active root Goal.
	ProviderUsage execution.ProviderUsageAdmitter
}

// AutoCompact runs proactive compaction when the conversation approaches
// the model's context window limit. When params.ChatModel is non-nil, uses
// LLM-driven summarization; otherwise falls back to deterministic compaction.
// Mirrors query.ts:453-543.
func AutoCompact(
	messages []*schema.Message,
	querySource string,
	tracking *CompactTracking,
	snipTokensFreed int,
	modelName string,
	params *AutoCompactParams,
) (*AutoCompactResult, int, *CompactTracking) {
	if tracking == nil {
		tracking = &CompactTracking{}
	}

	if querySource == "compact" || len(messages) == 0 {
		tracking.Compacted = false
		return nil, tracking.ConsecutiveFailures, tracking
	}
	if tracking.ConsecutiveFailures >= maxConsecutiveAutoCompactFailures {
		tracking.Compacted = false
		return nil, tracking.ConsecutiveFailures, tracking
	}

	tokenCount := EstimateTokenCount(messages) - snipTokensFreed
	if tokenCount < 0 {
		tokenCount = 0
	}
	warningState := CalculateTokenWarningState(tokenCount, modelName)
	if warningState.IsAtBlockingLimit {
		tracking.Compacted = false
		return nil, tracking.ConsecutiveFailures, tracking
	}
	if !warningState.IsAboveAutoCompactThreshold {
		tracking.Compacted = false
		return nil, tracking.ConsecutiveFailures, tracking
	}

	// Try LLM-driven compaction when a model is available
	if params != nil && params.ChatModel != nil {
		ctx := params.Ctx
		if ctx == nil {
			ctx = context.Background()
		}
		llmOpts := LLMCompactOptions{
			ChatModel:                 params.ChatModel,
			ModelName:                 modelName,
			CustomInstructions:        params.CustomInstructions,
			SuppressFollowUpQuestions: params.SuppressFollowUpQuestions,
			TranscriptPath:            params.TranscriptPath,
			IsAutoCompact:             true,
			ProviderUsage:             params.ProviderUsage,
		}

		// Try partial compact first when preferred and feasible.
		// Partial compact preserves recent messages verbatim, compacting only the older half.
		if params.PreferPartial {
			pivot := FindPartialCompactPivot(messages)
			if pivot > 0 {
				llmOpts.Direction = CompactDirectionUpTo
				result, err := PartialCompact(ctx, messages, pivot, llmOpts)
				if err == nil {
					tracking.Compacted = true
					tracking.TurnCounter = 0
					tracking.ConsecutiveFailures = 0
					return result, 0, tracking
				}
				// Partial compact failed — fall through to full compact
			}
		}

		// Full LLM compact
		llmOpts.Direction = CompactDirectionFull
		result, err := BuildLLMAutoCompact(ctx, messages, tokenCount, llmOpts)
		if err == nil {
			tracking.Compacted = true
			tracking.TurnCounter = 0
			tracking.ConsecutiveFailures = 0
			return result, 0, tracking
		}
		// LLM compaction failed — increment failure counter and fall back to deterministic
		tracking.ConsecutiveFailures++
		if tracking.ConsecutiveFailures >= maxConsecutiveAutoCompactFailures {
			tracking.Compacted = false
			return nil, tracking.ConsecutiveFailures, tracking
		}
	}

	// Fallback: deterministic compaction
	result := buildDeterministicAutoCompact(messages, tokenCount)
	tracking.Compacted = true
	tracking.TurnCounter = 0
	tracking.ConsecutiveFailures = 0
	return result, 0, tracking
}

func buildDeterministicAutoCompact(messages []*schema.Message, preCompactTokens int) *AutoCompactResult {
	preserved := buildAutoCompactPreservedTail(messages)
	boundary := &schema.Message{
		Role:    schema.System,
		Content: "",
		Extra: map[string]any{
			"subtype": "compact_boundary",
			"trigger": "auto_compact",
		},
	}
	summary := &schema.Message{
		Role:    schema.System,
		Content: buildAutoCompactSummary(messages),
		Extra: map[string]any{
			"subtype": "compact_summary",
			"trigger": "auto_compact",
		},
	}

	postMessages := make([]*schema.Message, 0, 2+len(preserved))
	postMessages = append(postMessages, boundary, summary)
	postMessages = append(postMessages, preserved...)
	postCompactTokens := EstimateTokenCount(postMessages)

	return &AutoCompactResult{
		PreCompactTokenCount:      preCompactTokens,
		PostCompactTokenCount:     postCompactTokens,
		TruePostCompactTokenCount: postCompactTokens,
		BoundaryMarker:            boundary,
		SummaryMessages:           []*schema.Message{summary},
		MessagesToKeep:            preserved,
		Attachments:               nil,
		HookResults:               nil,
	}
}

func buildAutoCompactPreservedTail(messages []*schema.Message) []*schema.Message {
	preserved := make([]*schema.Message, 0, autoCompactPreservedTailMessages)
	for i := len(messages) - 1; i >= 0 && len(preserved) < autoCompactPreservedTailMessages; i-- {
		msg := cloneMessage(messages[i])
		if msg == nil {
			continue
		}
		if msg.Extra != nil {
			if subtype, _ := msg.Extra["subtype"].(string); subtype == "compact_boundary" || subtype == "compact_summary" || subtype == "collapse_staged" {
				continue
			}
		}
		preserved = append([]*schema.Message{msg}, preserved...)
	}
	return preserved
}

func buildAutoCompactSummary(messages []*schema.Message) string {
	lines := []string{"Conversation was proactively compacted to stay within the context window.", "Preserved recent context:"}
	recent := make([]string, 0, 4)
	for i := len(messages) - 1; i >= 0 && len(recent) < 4; i-- {
		msg := messages[i]
		if msg == nil {
			continue
		}
		if msg.Extra != nil {
			if subtype, _ := msg.Extra["subtype"].(string); subtype == "compact_boundary" || subtype == "compact_summary" || subtype == "collapse_staged" {
				continue
			}
		}
		preview := autoCompactPreview(msg)
		if preview == "" {
			continue
		}
		recent = append([]string{fmt.Sprintf("- %s: %s", msg.Role, preview)}, recent...)
	}
	if len(recent) == 0 {
		recent = append(recent, "- recent context unavailable")
	}
	lines = append(lines, recent...)
	lines = append(lines, "Continue from the preserved tail without repeating compacted history.")
	return strings.Join(lines, "\n")
}

func autoCompactPreview(msg *schema.Message) string {
	if msg == nil {
		return ""
	}
	preview := strings.TrimSpace(msg.Content)
	if preview == "" && len(msg.UserInputMultiContent) > 0 {
		parts := make([]string, 0, len(msg.UserInputMultiContent))
		for _, part := range msg.UserInputMultiContent {
			if strings.TrimSpace(part.Text) != "" {
				parts = append(parts, strings.TrimSpace(part.Text))
			}
		}
		preview = strings.Join(parts, " ")
	}
	if preview == "" && len(msg.ToolCalls) > 0 {
		preview = fmt.Sprintf("%d tool call(s)", len(msg.ToolCalls))
	}
	preview = strings.Join(strings.Fields(preview), " ")
	if len(preview) > 160 {
		preview = preview[:157] + "..."
	}
	return preview
}

// BuildPostCompactMessages builds the message array after compaction.
func BuildPostCompactMessages(result *AutoCompactResult) []*schema.Message {
	if result == nil {
		return nil
	}
	msgs := make([]*schema.Message, 0, 1+len(result.SummaryMessages)+len(result.MessagesToKeep)+len(result.Attachments)+len(result.HookResults))
	if result.BoundaryMarker != nil {
		msgs = append(msgs, result.BoundaryMarker)
	}
	msgs = append(msgs, result.SummaryMessages...)
	msgs = append(msgs, result.MessagesToKeep...)
	msgs = append(msgs, result.Attachments...)
	msgs = append(msgs, result.HookResults...)
	return msgs
}
