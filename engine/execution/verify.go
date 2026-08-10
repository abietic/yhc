package execution

import (
	"encoding/json"
	"fmt"
	"strings"
)

// OutputVerificationMode controls how output schema mismatches are handled.
type OutputVerificationMode string

const (
	// VerifyModeWarn logs a warning but still returns the result.
	VerifyModeWarn OutputVerificationMode = "warn"
	// VerifyModeError treats a schema mismatch as a tool error.
	VerifyModeError OutputVerificationMode = "error"
	// VerifyModeOff disables output verification entirely.
	VerifyModeOff OutputVerificationMode = "off"
)

// OutputSchema describes the expected shape of a tool's output for verification.
// Mirrors the reference's tool output_schema concept where tool results are
// validated against an expected format before being sent back to the model.
type OutputSchema struct {
	// Type is the expected output type: "json", "text", "content_block".
	// "json" means the result must be valid JSON (optionally matching RequiredFields).
	// "text" means the result must be non-empty text.
	// "content_block" means the result must follow the content-block format.
	Type string

	// RequiredFields is a list of top-level JSON keys that must be present
	// when Type is "json". Ignored for other types.
	RequiredFields []string

	// AllowEmpty when true permits empty/whitespace-only results without error.
	// Default (false) treats empty output as a verification failure.
	AllowEmpty bool

	// MaxSize is the maximum character count for the output. 0 means no limit.
	// Results exceeding this are reported as verification failures.
	MaxSize int
}

// OutputVerificationConfig configures output verification for the pipeline.
type OutputVerificationConfig struct {
	// Mode controls strictness: "warn", "error", or "off".
	Mode OutputVerificationMode

	// Schemas maps tool names to their expected output schema.
	// Tools not present in this map are not verified.
	Schemas map[string]OutputSchema

	// DefaultSchema is applied to tools not explicitly listed in Schemas.
	// If nil, unlisted tools are not verified.
	DefaultSchema *OutputSchema

	// OnViolation is called when a verification violation is detected.
	// Receives the tool name, the violation description, and whether it was
	// treated as an error (vs warning). Used for logging/monitoring.
	OnViolation func(toolName, violation string, isError bool)
}

// OutputVerificationResult holds the outcome of output verification.
type OutputVerificationResult struct {
	// Valid is true when the output matches the expected schema.
	Valid bool
	// Violations lists all detected mismatches.
	Violations []string
	// AdjustedContent holds the modified content when verification adds
	// metadata (e.g., a warning prefix). Empty string means no adjustment.
	AdjustedContent string
}

// VerifyToolOutput checks a tool's result against its expected output schema.
// Returns verification results indicating whether the output is valid and any
// violations found. The caller decides how to handle violations based on the
// configured mode.
//
// This function is safe for concurrent use — it does not mutate any shared state.
func VerifyToolOutput(toolName, result string, isError bool, config OutputVerificationConfig) OutputVerificationResult {
	if config.Mode == VerifyModeOff {
		return OutputVerificationResult{Valid: true}
	}

	schema := resolveOutputSchema(toolName, config)
	if schema == nil {
		return OutputVerificationResult{Valid: true}
	}

	violations := make([]string, 0)

	// Check empty output.
	if strings.TrimSpace(result) == "" && !schema.AllowEmpty && !isError {
		violations = append(violations, fmt.Sprintf("tool %s produced empty output", toolName))
	}

	// Check max size.
	if schema.MaxSize > 0 && len(result) > schema.MaxSize {
		violations = append(violations, fmt.Sprintf("tool %s output exceeds max size (%d > %d)", toolName, len(result), schema.MaxSize))
	}

	// Type-specific checks (only for non-error, non-empty results).
	if !isError && strings.TrimSpace(result) != "" {
		switch schema.Type {
		case "json":
			violations = append(violations, verifyJSONOutput(toolName, result, schema)...)
		case "text":
			// Text type: result must be non-empty (already checked above).
		case "content_block":
			violations = append(violations, verifyContentBlockOutput(toolName, result)...)
		}
	}

	valid := len(violations) == 0

	// Notify violation callback.
	if !valid && config.OnViolation != nil {
		treatAsError := config.Mode == VerifyModeError
		for _, v := range violations {
			config.OnViolation(toolName, v, treatAsError)
		}
	}

	vr := OutputVerificationResult{
		Valid:      valid,
		Violations: violations,
	}

	// In error mode, adjust content to include violation info.
	if !valid && config.Mode == VerifyModeError {
		vr.AdjustedContent = formatVerificationError(toolName, result, violations)
	}

	return vr
}

// resolveOutputSchema finds the applicable schema for a tool.
func resolveOutputSchema(toolName string, config OutputVerificationConfig) *OutputSchema {
	if config.Schemas != nil {
		if s, ok := config.Schemas[toolName]; ok {
			return &s
		}
	}
	return config.DefaultSchema
}

// verifyJSONOutput checks that the result is valid JSON and contains required fields.
func verifyJSONOutput(toolName, result string, schema *OutputSchema) []string {
	violations := make([]string, 0)

	if !json.Valid([]byte(result)) {
		violations = append(violations, fmt.Sprintf("tool %s output is not valid JSON", toolName))
		return violations
	}

	if len(schema.RequiredFields) > 0 {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(result), &parsed); err != nil {
			// Valid JSON but not an object — required fields don't apply to arrays/scalars.
			violations = append(violations, fmt.Sprintf("tool %s output is valid JSON but not an object (required fields cannot be checked)", toolName))
			return violations
		}
		for _, field := range schema.RequiredFields {
			if _, ok := parsed[field]; !ok {
				violations = append(violations, fmt.Sprintf("tool %s output missing required field %q", toolName, field))
			}
		}
	}

	return violations
}

// verifyContentBlockOutput checks that the result follows content-block format.
// Content blocks are expected to be JSON arrays of objects with a "type" field.
func verifyContentBlockOutput(toolName, result string) []string {
	violations := make([]string, 0)

	if !json.Valid([]byte(result)) {
		violations = append(violations, fmt.Sprintf("tool %s content_block output is not valid JSON", toolName))
		return violations
	}

	var blocks []map[string]any
	if err := json.Unmarshal([]byte(result), &blocks); err != nil {
		violations = append(violations, fmt.Sprintf("tool %s content_block output is not a JSON array of objects", toolName))
		return violations
	}

	if len(blocks) == 0 {
		violations = append(violations, fmt.Sprintf("tool %s content_block output has no blocks", toolName))
		return violations
	}

	for i, block := range blocks {
		if _, ok := block["type"]; !ok {
			violations = append(violations, fmt.Sprintf("tool %s content_block[%d] missing 'type' field", toolName, i))
		}
	}

	return violations
}

// formatVerificationError creates a tool error message that includes violation details.
func formatVerificationError(toolName, originalResult string, violations []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Output verification failed for %s:\n", toolName)
	for _, v := range violations {
		b.WriteString("- ")
		b.WriteString(v)
		b.WriteString("\n")
	}
	b.WriteString("\nOriginal output (first 500 chars):\n")
	preview := originalResult
	if len(preview) > 500 {
		preview = preview[:500] + "..."
	}
	b.WriteString(preview)
	return b.String()
}

// VerifyAndNormalize performs output verification followed by normalization.
// This is the recommended integration point for the pipeline — it applies
// verification before normalization so violations are detected on the raw output,
// then normalizes the (potentially adjusted) result.
//
// When verification fails in "error" mode, the result is replaced with a
// verification error message and marked as an error. In "warn" mode, the
// original result passes through to normalization unchanged.
func VerifyAndNormalize(toolName, result string, isError bool, verifyConfig OutputVerificationConfig, normConfig ResultNormalizationConfig) NormalizedResult {
	if verifyConfig.Mode != VerifyModeOff {
		vr := VerifyToolOutput(toolName, result, isError, verifyConfig)
		if !vr.Valid && verifyConfig.Mode == VerifyModeError {
			// Replace the result with the verification error.
			return NormalizeToolResult(toolName, vr.AdjustedContent, true, normConfig)
		}
	}

	return NormalizeToolResult(toolName, result, isError, normConfig)
}
