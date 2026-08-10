package execution

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// ResultNormalizationConfig holds configuration for normalizing tool results.
type ResultNormalizationConfig struct {
	// MaxResultSize is the maximum character count before truncation applies.
	// Default: 50000 (mirrors DEFAULT_MAX_RESULT_SIZE_CHARS from the reference).
	MaxResultSize int

	// TruncationPreviewSize is the number of characters to show from the head
	// and tail when a result is truncated. Default: 5000.
	TruncationPreviewSize int
}

// DefaultResultNormalizationConfig returns the default normalization config.
// Mirrors constants/toolLimits.ts DEFAULT_MAX_RESULT_SIZE_CHARS.
func DefaultResultNormalizationConfig() ResultNormalizationConfig {
	return ResultNormalizationConfig{
		MaxResultSize:         50000,
		TruncationPreviewSize: 5000,
	}
}

// NormalizedResult holds the output of normalizing a raw tool result.
type NormalizedResult struct {
	// Content is the normalized result text.
	Content string
	// IsError indicates whether the result represents an error.
	IsError bool
	// WasTruncated indicates whether the original content was truncated.
	WasTruncated bool
	// OriginalSize is the character count of the original content before normalization.
	OriginalSize int
}

// NormalizeToolResult takes a raw tool execution result and normalizes it into
// a consistent shape suitable for insertion into the conversation. It handles:
//   - nil/empty results (empty result injection)
//   - error results (error formatting with truncation)
//   - oversized results (head+tail truncation with notice)
//   - normal results (pass-through)
//
// Mirrors the result normalization path in:
//   - toolResultStorage.ts (persistence and truncation)
//   - toolErrors.ts (error formatting)
//   - toolExecution.ts (empty result handling)
func NormalizeToolResult(toolName, result string, isError bool, cfg ...ResultNormalizationConfig) NormalizedResult {
	config := DefaultResultNormalizationConfig()
	if len(cfg) > 0 {
		config = cfg[0]
	}
	if config.MaxResultSize <= 0 {
		config.MaxResultSize = 50000
	}
	if config.TruncationPreviewSize <= 0 {
		config.TruncationPreviewSize = 5000
	}

	originalSize := utf8.RuneCountInString(result)

	// Handle error results with their own truncation path.
	if isError {
		return normalizeErrorResult(toolName, result, originalSize, config)
	}

	// Handle empty results: inject a synthetic message to prevent the model
	// from misinterpreting silence as an end-of-turn signal.
	// Mirrors TS toolResultStorage.ts:287 — "(toolName completed with no output)".
	if strings.TrimSpace(result) == "" {
		return NormalizedResult{
			Content:      fmt.Sprintf("(%s completed with no output)", toolName),
			IsError:      false,
			WasTruncated: false,
			OriginalSize: originalSize,
		}
	}

	// Handle oversized results: apply head+tail truncation.
	if originalSize > config.MaxResultSize {
		return normalizeTruncatedResult(result, originalSize, config)
	}

	// Normal result: pass through unchanged.
	return NormalizedResult{
		Content:      result,
		IsError:      false,
		WasTruncated: false,
		OriginalSize: originalSize,
	}
}

// normalizeErrorResult formats an error result with truncation for long errors.
// Mirrors TS utils/toolErrors.ts formatError().
func normalizeErrorResult(_, errMsg string, originalSize int, _ ResultNormalizationConfig) NormalizedResult {
	// Error-specific limit: 10000 chars with head/tail of 5000 each.
	// Mirrors TS MAX_ERROR_LENGTH / halfToolErrorLength.
	const maxErrorLength = 10000
	const halfErrorLength = 5000

	truncated := false
	content := errMsg

	if originalSize > maxErrorLength {
		runes := []rune(errMsg)
		head := string(runes[:halfErrorLength])
		tail := string(runes[len(runes)-halfErrorLength:])
		content = fmt.Sprintf("%s\n\n... [%d characters truncated] ...\n\n%s",
			head, originalSize-maxErrorLength, tail)
		truncated = true
	}

	return NormalizedResult{
		Content:      content,
		IsError:      true,
		WasTruncated: truncated,
		OriginalSize: originalSize,
	}
}

// normalizeTruncatedResult applies head+tail truncation to an oversized result.
// Uses the configured preview size for head and tail portions.
func normalizeTruncatedResult(result string, originalSize int, config ResultNormalizationConfig) NormalizedResult {
	runes := []rune(result)
	previewSize := config.TruncationPreviewSize

	// Ensure preview doesn't exceed half the content.
	if previewSize > len(runes)/2 {
		previewSize = len(runes) / 2
	}

	head := string(runes[:previewSize])
	tail := string(runes[len(runes)-previewSize:])
	truncatedCount := originalSize - (previewSize * 2)

	content := fmt.Sprintf("%s\n\n... [%d characters truncated] ...\n\n%s",
		head, truncatedCount, tail)

	return NormalizedResult{
		Content:      content,
		IsError:      false,
		WasTruncated: true,
		OriginalSize: originalSize,
	}
}

// NormalizeToolError is a convenience wrapper for normalizing error-only results.
// It delegates to NormalizeToolResult with isError=true.
func NormalizeToolError(toolName, errMsg string, cfg ...ResultNormalizationConfig) NormalizedResult {
	return NormalizeToolResult(toolName, errMsg, true, cfg...)
}
