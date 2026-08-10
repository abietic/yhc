package budget

import (
	"math"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
)

// TokenEstimator provides provider-specific token count estimation.
// Mirrors the rough heuristic approach in tokenEstimation.ts:roughTokenCountEstimation
// with provider-specific tuning for chars-per-token ratios and overhead costs.
type TokenEstimator struct {
	provider           string
	charsPerToken      float64
	overheadPerMessage int
	toolCallOverhead   int
}

// NewTokenEstimator creates a TokenEstimator with provider-specific defaults.
//
// Provider defaults:
//   - "claude": 3.5 chars/token, 8 message overhead, 12 tool-call overhead
//   - "openai": 4.0 chars/token, 4 message overhead, 8 tool-call overhead
//   - default: 4.0 chars/token, 6 message overhead, 10 tool-call overhead
func NewTokenEstimator(provider string) *TokenEstimator {
	switch strings.ToLower(provider) {
	case "claude":
		return &TokenEstimator{
			provider:           "claude",
			charsPerToken:      3.5,
			overheadPerMessage: 8,
			toolCallOverhead:   12,
		}
	case "openai":
		return &TokenEstimator{
			provider:           "openai",
			charsPerToken:      4.0,
			overheadPerMessage: 4,
			toolCallOverhead:   8,
		}
	default:
		return &TokenEstimator{
			provider:           provider,
			charsPerToken:      4.0,
			overheadPerMessage: 6,
			toolCallOverhead:   10,
		}
	}
}

// EstimateMessage estimates the token count for a single message.
// Accounts for content, reasoning, tool calls, multimodal parts,
// and per-message overhead.
func (e *TokenEstimator) EstimateMessage(msg *schema.Message) int {
	if msg == nil {
		return 0
	}

	total := e.overheadPerMessage

	// Main text content
	total += e.textTokens(msg.Content)

	// Reasoning content
	total += e.textTokens(msg.ReasoningContent)

	// Metadata fields
	total += e.textTokens(msg.Name)
	total += e.textTokens(msg.ToolCallID)
	total += e.textTokens(msg.ToolName)

	// Multimodal content (deprecated field)
	for _, part := range msg.MultiContent {
		total += e.estimateMultiContentPart(part)
	}

	// User input multimodal content
	for _, part := range msg.UserInputMultiContent {
		total += e.estimateInputPart(part)
	}

	// Assistant-generated multimodal content
	for _, part := range msg.AssistantGenMultiContent {
		total += e.estimateOutputPart(part)
	}

	// Tool calls
	for _, tc := range msg.ToolCalls {
		total += e.toolCallOverhead
		total += e.textTokens(tc.ID)
		total += e.textTokens(tc.Type)
		total += e.textTokens(tc.Function.Name)
		total += e.textTokens(tc.Function.Arguments)
	}

	return total
}

// EstimateMessages estimates the total token count for a batch of messages.
func (e *TokenEstimator) EstimateMessages(msgs []*schema.Message) int {
	total := 0
	for _, msg := range msgs {
		total += e.EstimateMessage(msg)
	}
	return total
}

// EstimateText estimates the token count for raw text.
func (e *TokenEstimator) EstimateText(text string) int {
	return e.textTokens(text)
}

// textTokens computes the token estimate for a string using the provider ratio.
func (e *TokenEstimator) textTokens(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	return int(math.Ceil(float64(len(text)) / e.charsPerToken))
}

// estimateMultiContentPart estimates tokens for a deprecated ChatMessagePart.
func (e *TokenEstimator) estimateMultiContentPart(part schema.ChatMessagePart) int { //nolint:staticcheck
	switch part.Type {
	case schema.ChatMessagePartTypeText:
		return e.textTokens(part.Text)
	case schema.ChatMessagePartTypeImageURL:
		// Conservative image estimate matching reference: 2000 tokens
		return 2000
	default:
		return 32
	}
}

// estimateInputPart estimates tokens for a MessageInputPart.
func (e *TokenEstimator) estimateInputPart(part schema.MessageInputPart) int {
	switch part.Type {
	case schema.ChatMessagePartTypeText:
		return e.textTokens(part.Text)
	case schema.ChatMessagePartTypeImageURL:
		return 2000
	case schema.ChatMessagePartTypeAudioURL:
		return 2000
	case schema.ChatMessagePartTypeVideoURL:
		return 2000
	case schema.ChatMessagePartTypeFileURL:
		return 2000
	default:
		return 32
	}
}

// estimateOutputPart estimates tokens for a MessageOutputPart.
func (e *TokenEstimator) estimateOutputPart(part schema.MessageOutputPart) int {
	switch part.Type {
	case schema.ChatMessagePartTypeText:
		return e.textTokens(part.Text)
	case schema.ChatMessagePartTypeReasoning:
		if part.Reasoning != nil {
			return e.textTokens(part.Reasoning.Text)
		}
		return 0
	case schema.ChatMessagePartTypeImageURL:
		return 2000
	case schema.ChatMessagePartTypeAudioURL:
		return 2000
	case schema.ChatMessagePartTypeVideoURL:
		return 2000
	default:
		return 32
	}
}

// --- Diminishing Returns Detection ---

// TurnDelta records the token usage delta for a single turn.
type TurnDelta struct {
	TurnNumber  int
	TokensUsed  int
	TokensDelta int
	Timestamp   time.Time
}

// DiminishingReturnsTracker monitors token consumption growth across turns
// and detects when continued execution is approaching diminishing returns
// relative to the total budget.
type DiminishingReturnsTracker struct {
	history    []TurnDelta
	threshold  float64
	windowSize int
}

// NewDiminishingReturnsTracker creates a tracker with default settings:
//   - threshold: 0.1 (10% of total budget)
//   - windowSize: 5 (look at last 5 turns)
func NewDiminishingReturnsTracker() *DiminishingReturnsTracker {
	return &DiminishingReturnsTracker{
		threshold:  0.1,
		windowSize: 5,
	}
}

// RecordTurn records a turn's cumulative token usage. The delta from the
// previous turn is computed automatically.
func (d *DiminishingReturnsTracker) RecordTurn(turnNumber, totalTokens int) {
	delta := totalTokens
	if len(d.history) > 0 {
		delta = totalTokens - d.history[len(d.history)-1].TokensUsed
	}
	if delta < 0 {
		delta = 0
	}

	d.history = append(d.history, TurnDelta{
		TurnNumber:  turnNumber,
		TokensUsed:  totalTokens,
		TokensDelta: delta,
		Timestamp:   time.Now(),
	})
}

// IsApproachingLimit returns true if the conversation is approaching the
// token budget limit based on growth rate analysis.
//
// Returns true when:
//   - Current usage is above 80% of totalBudget, OR
//   - The average delta per turn projected over remaining capacity would exceed budget
func (d *DiminishingReturnsTracker) IsApproachingLimit(totalBudget int) bool {
	if totalBudget <= 0 || len(d.history) == 0 {
		return false
	}

	current := d.history[len(d.history)-1].TokensUsed

	// Direct threshold: above 80% of budget
	if float64(current) >= 0.8*float64(totalBudget) {
		return true
	}

	// Growth-rate projection
	avgDelta := d.averageDelta()
	if avgDelta <= 0 {
		return false
	}

	remaining := totalBudget - current
	projectedTurns := float64(remaining) / avgDelta
	// If fewer than 2 turns remain at current growth rate, we're approaching the limit
	return projectedTurns < 2.0
}

// ProjectedTurnsRemaining estimates how many more turns fit within the
// total budget based on the average token growth rate.
func (d *DiminishingReturnsTracker) ProjectedTurnsRemaining(totalBudget int) int {
	if totalBudget <= 0 || len(d.history) == 0 {
		return 0
	}

	current := d.history[len(d.history)-1].TokensUsed
	remaining := totalBudget - current
	if remaining <= 0 {
		return 0
	}

	avgDelta := d.averageDelta()
	if avgDelta <= 0 {
		// No growth — effectively infinite turns, cap at a large number
		return remaining
	}

	turns := int(math.Floor(float64(remaining) / avgDelta))
	if turns < 0 {
		return 0
	}
	return turns
}

// averageDelta computes the average token delta over the configured window.
func (d *DiminishingReturnsTracker) averageDelta() float64 {
	if len(d.history) == 0 {
		return 0
	}

	window := d.windowSize
	if window <= 0 {
		window = 5
	}

	start := len(d.history) - window
	if start < 0 {
		start = 0
	}

	slice := d.history[start:]
	if len(slice) == 0 {
		return 0
	}

	sum := 0
	for _, td := range slice {
		sum += td.TokensDelta
	}
	return float64(sum) / float64(len(slice))
}
