package storage

import (
	"fmt"
	"strings"
)

const (
	// defaultMaxInlineTokens is the default token threshold for inline results.
	defaultMaxInlineTokens = 12000

	// charsPerToken is a rough approximation for estimating token count.
	charsPerToken = 4

	// defaultTruncateMessage is the message appended when a result is truncated.
	defaultTruncateMessage = "[Result truncated]"
)

// ToolResultHandler manages the processing of tool results, offloading
// large results to disk via ResultStorage and returning previews for
// inclusion in conversation messages.
type ToolResultHandler struct {
	storage         *ResultStorage
	maxInlineTokens int
	truncateMessage string
}

// NewToolResultHandler creates a ToolResultHandler backed by a ResultStorage
// rooted in the given sessionDir. maxInlineTokens controls the threshold
// (in estimated tokens) below which results are kept inline.
func NewToolResultHandler(sessionDir string, maxInlineTokens int) *ToolResultHandler {
	return &ToolResultHandler{
		storage:         NewResultStorage(sessionDir),
		maxInlineTokens: maxInlineTokens,
		truncateMessage: defaultTruncateMessage,
	}
}

// estimateTokens returns a rough token count for a string.
func estimateTokens(s string) int {
	return (len(s) + charsPerToken - 1) / charsPerToken
}

// maxInlineChars returns the character budget implied by maxInlineTokens.
func (h *ToolResultHandler) maxInlineChars() int {
	return h.maxInlineTokens * charsPerToken
}

// ProcessResult checks whether the result exceeds the inline token budget.
// If it fits, it is returned unchanged. Otherwise the full result is stored
// to disk and a preview with a reference note is returned.
func (h *ToolResultHandler) ProcessResult(toolName, result string) (string, error) {
	if estimateTokens(result) <= h.maxInlineTokens {
		return result, nil
	}

	stored, err := h.storage.Store(toolName, result)
	if err != nil {
		// If storage fails, fall back to truncation so the caller still
		// gets a usable (though lossy) result.
		return h.TruncateResult(result, h.maxInlineTokens), nil
	}

	// stored can be nil if ResultStorage decides the result is under its own
	// inline threshold (character-based). In that case, return as-is.
	if stored == nil {
		return result, nil
	}

	remaining := len(result) - len(stored.Preview)
	if remaining < 0 {
		remaining = 0
	}

	var b strings.Builder
	b.WriteString("[Result stored - showing preview]\n")
	b.WriteString(stored.Preview)
	fmt.Fprintf(&b, "\n[...%d more characters stored on disk]", remaining)
	return b.String(), nil
}

// TruncateResult performs smart truncation that respects line boundaries.
// It truncates to approximately maxTokens tokens worth of characters and
// appends a truncation indicator with the omitted character count.
func (h *ToolResultHandler) TruncateResult(result string, maxTokens int) string {
	maxChars := maxTokens * charsPerToken
	if len(result) <= maxChars {
		return result
	}

	truncated := result[:maxChars]

	// Try to break at a line boundary in the latter half to avoid
	// cutting mid-line.
	halfPoint := maxChars / 2
	if idx := strings.LastIndex(truncated[halfPoint:], "\n"); idx >= 0 {
		cutPoint := halfPoint + idx + 1
		truncated = result[:cutPoint]
	}

	omitted := len(result) - len(truncated)
	return truncated + fmt.Sprintf("\n%s (%d characters omitted)", h.truncateMessage, omitted)
}

// FormatToolResult formats a tool result for inclusion in conversation
// messages. Large results get a tool-name header for context; errors are
// prefixed with an error marker.
func (h *ToolResultHandler) FormatToolResult(toolName, result string, isError bool) string {
	var b strings.Builder

	if isError {
		b.WriteString("[ERROR] ")
	}

	// Add a tool name header when the result is large enough that the reader
	// benefits from knowing which tool produced it.
	isLarge := estimateTokens(result) > h.maxInlineTokens/4
	if isLarge && toolName != "" {
		fmt.Fprintf(&b, "[%s]\n", toolName)
	}

	b.WriteString(result)
	return b.String()
}

// DefaultToolResultHandler is the package-level handler used by the
// convenience function ProcessToolOutput. It is nil until InitToolResultHandler
// is called.
var DefaultToolResultHandler *ToolResultHandler

// InitToolResultHandler creates the DefaultToolResultHandler with reasonable
// defaults (maxInlineTokens: 12000) rooted in the given session directory.
func InitToolResultHandler(sessionDir string) {
	DefaultToolResultHandler = NewToolResultHandler(sessionDir, defaultMaxInlineTokens)
}

// ProcessToolOutput is a convenience function that processes a tool result
// using DefaultToolResultHandler. If the handler has not been initialized,
// it falls back to simple truncation based on the default token budget.
func ProcessToolOutput(toolName, result string) string {
	if DefaultToolResultHandler == nil {
		// Fallback: simple character-based truncation without disk storage.
		maxChars := defaultMaxInlineTokens * charsPerToken
		if len(result) <= maxChars {
			return result
		}
		truncated := result[:maxChars]
		halfPoint := maxChars / 2
		if idx := strings.LastIndex(truncated[halfPoint:], "\n"); idx >= 0 {
			cutPoint := halfPoint + idx + 1
			truncated = result[:cutPoint]
		}
		omitted := len(result) - len(truncated)
		return truncated + fmt.Sprintf("\n%s (%d characters omitted)", defaultTruncateMessage, omitted)
	}

	processed, err := DefaultToolResultHandler.ProcessResult(toolName, result)
	if err != nil {
		// Should not happen (ProcessResult handles storage errors internally),
		// but guard against it.
		return result
	}
	return processed
}
