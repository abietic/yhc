package execution

import (
	"strings"
	"sync"
	"testing"
)

// ============================================================================
// Output Verification Tests
// ============================================================================

func TestVerifyToolOutput_OffMode(t *testing.T) {
	config := OutputVerificationConfig{
		Mode: VerifyModeOff,
		Schemas: map[string]OutputSchema{
			"Read": {Type: "json", RequiredFields: []string{"content"}},
		},
	}

	// Even invalid output should pass when mode is off.
	result := VerifyToolOutput("Read", "not json at all", false, config)
	if !result.Valid {
		t.Error("expected valid when mode is off")
	}
	if len(result.Violations) > 0 {
		t.Error("expected no violations when mode is off")
	}
}

func TestVerifyToolOutput_NoSchemaConfigured(t *testing.T) {
	config := OutputVerificationConfig{
		Mode:    VerifyModeError,
		Schemas: map[string]OutputSchema{},
	}

	// Tool not in schemas and no default — should pass.
	result := VerifyToolOutput("UnknownTool", "anything", false, config)
	if !result.Valid {
		t.Error("expected valid when no schema is configured for the tool")
	}
}

func TestVerifyToolOutput_DefaultSchemaApplied(t *testing.T) {
	config := OutputVerificationConfig{
		Mode: VerifyModeError,
		DefaultSchema: &OutputSchema{
			Type: "text",
		},
	}

	// Default schema requires non-empty text — empty should fail.
	result := VerifyToolOutput("AnyTool", "", false, config)
	if result.Valid {
		t.Error("expected invalid for empty output with text schema")
	}
	if len(result.Violations) == 0 {
		t.Error("expected violations for empty output")
	}
}

func TestVerifyToolOutput_JSONValid(t *testing.T) {
	config := OutputVerificationConfig{
		Mode: VerifyModeError,
		Schemas: map[string]OutputSchema{
			"Read": {Type: "json", RequiredFields: []string{"content", "path"}},
		},
	}

	result := VerifyToolOutput("Read", `{"content":"hello","path":"/tmp/a"}`, false, config)
	if !result.Valid {
		t.Errorf("expected valid JSON, got violations: %v", result.Violations)
	}
}

func TestVerifyToolOutput_JSONInvalid(t *testing.T) {
	config := OutputVerificationConfig{
		Mode: VerifyModeError,
		Schemas: map[string]OutputSchema{
			"Read": {Type: "json"},
		},
	}

	result := VerifyToolOutput("Read", "not json {{{", false, config)
	if result.Valid {
		t.Error("expected invalid for non-JSON output")
	}
	if !strings.Contains(result.Violations[0], "not valid JSON") {
		t.Errorf("expected JSON validation violation, got %q", result.Violations[0])
	}
}

func TestVerifyToolOutput_JSONMissingRequiredFields(t *testing.T) {
	config := OutputVerificationConfig{
		Mode: VerifyModeError,
		Schemas: map[string]OutputSchema{
			"Read": {Type: "json", RequiredFields: []string{"content", "metadata"}},
		},
	}

	result := VerifyToolOutput("Read", `{"content":"hello"}`, false, config)
	if result.Valid {
		t.Error("expected invalid when required field 'metadata' is missing")
	}
	found := false
	for _, v := range result.Violations {
		if strings.Contains(v, "metadata") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected violation mentioning 'metadata', got %v", result.Violations)
	}
}

func TestVerifyToolOutput_JSONArrayNotObject(t *testing.T) {
	config := OutputVerificationConfig{
		Mode: VerifyModeError,
		Schemas: map[string]OutputSchema{
			"Read": {Type: "json", RequiredFields: []string{"content"}},
		},
	}

	// Valid JSON but an array, not an object — required fields can't be checked.
	result := VerifyToolOutput("Read", `[1, 2, 3]`, false, config)
	if result.Valid {
		t.Error("expected invalid when JSON is array and required fields specified")
	}
	if !strings.Contains(result.Violations[0], "not an object") {
		t.Errorf("expected 'not an object' violation, got %q", result.Violations[0])
	}
}

func TestVerifyToolOutput_ContentBlockValid(t *testing.T) {
	config := OutputVerificationConfig{
		Mode: VerifyModeError,
		Schemas: map[string]OutputSchema{
			"Tool": {Type: "content_block"},
		},
	}

	result := VerifyToolOutput("Tool", `[{"type":"text","text":"hello"},{"type":"image","url":"http://x"}]`, false, config)
	if !result.Valid {
		t.Errorf("expected valid content_block, got violations: %v", result.Violations)
	}
}

func TestVerifyToolOutput_ContentBlockInvalid(t *testing.T) {
	config := OutputVerificationConfig{
		Mode: VerifyModeError,
		Schemas: map[string]OutputSchema{
			"Tool": {Type: "content_block"},
		},
	}

	// Not a JSON array.
	result := VerifyToolOutput("Tool", `{"type":"text"}`, false, config)
	if result.Valid {
		t.Error("expected invalid for non-array content_block")
	}

	// Empty array.
	result = VerifyToolOutput("Tool", `[]`, false, config)
	if result.Valid {
		t.Error("expected invalid for empty content_block array")
	}

	// Missing type field.
	result = VerifyToolOutput("Tool", `[{"text":"hello"}]`, false, config)
	if result.Valid {
		t.Error("expected invalid when block is missing 'type' field")
	}
}

func TestVerifyToolOutput_EmptyOutputWithAllowEmpty(t *testing.T) {
	config := OutputVerificationConfig{
		Mode: VerifyModeError,
		Schemas: map[string]OutputSchema{
			"Read": {Type: "text", AllowEmpty: true},
		},
	}

	result := VerifyToolOutput("Read", "", false, config)
	if !result.Valid {
		t.Errorf("expected valid when AllowEmpty is true, got violations: %v", result.Violations)
	}
}

func TestVerifyToolOutput_MaxSizeExceeded(t *testing.T) {
	config := OutputVerificationConfig{
		Mode: VerifyModeError,
		Schemas: map[string]OutputSchema{
			"Read": {Type: "text", MaxSize: 100},
		},
	}

	result := VerifyToolOutput("Read", strings.Repeat("x", 200), false, config)
	if result.Valid {
		t.Error("expected invalid when output exceeds MaxSize")
	}
	if !strings.Contains(result.Violations[0], "exceeds max size") {
		t.Errorf("expected max size violation, got %q", result.Violations[0])
	}
}

func TestVerifyToolOutput_MaxSizeNotExceeded(t *testing.T) {
	config := OutputVerificationConfig{
		Mode: VerifyModeError,
		Schemas: map[string]OutputSchema{
			"Read": {Type: "text", MaxSize: 100},
		},
	}

	result := VerifyToolOutput("Read", "short output", false, config)
	if !result.Valid {
		t.Errorf("expected valid when within MaxSize, got violations: %v", result.Violations)
	}
}

func TestVerifyToolOutput_ErrorResultSkipsTypeCheck(t *testing.T) {
	config := OutputVerificationConfig{
		Mode: VerifyModeError,
		Schemas: map[string]OutputSchema{
			"Read": {Type: "json", RequiredFields: []string{"content"}},
		},
	}

	// Error results should not be checked for JSON validity.
	result := VerifyToolOutput("Read", "file not found", true, config)
	if !result.Valid {
		t.Errorf("expected valid for error result (type checks skipped), got violations: %v", result.Violations)
	}
}

func TestVerifyToolOutput_WarnModeCallsOnViolation(t *testing.T) {
	var mu sync.Mutex
	var violations []string

	config := OutputVerificationConfig{
		Mode: VerifyModeWarn,
		Schemas: map[string]OutputSchema{
			"Read": {Type: "json"},
		},
		OnViolation: func(toolName, violation string, isError bool) {
			mu.Lock()
			defer mu.Unlock()
			violations = append(violations, violation)
			if isError {
				t.Error("expected isError=false in warn mode")
			}
		},
	}

	result := VerifyToolOutput("Read", "not json", false, config)
	if result.Valid {
		t.Error("expected invalid")
	}
	// In warn mode, AdjustedContent should be empty (no replacement).
	if result.AdjustedContent != "" {
		t.Errorf("expected no adjusted content in warn mode, got %q", result.AdjustedContent)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(violations) == 0 {
		t.Error("expected OnViolation to be called")
	}
}

func TestVerifyToolOutput_ErrorModeAdjustsContent(t *testing.T) {
	config := OutputVerificationConfig{
		Mode: VerifyModeError,
		Schemas: map[string]OutputSchema{
			"Read": {Type: "json"},
		},
	}

	result := VerifyToolOutput("Read", "not json", false, config)
	if result.Valid {
		t.Error("expected invalid")
	}
	if result.AdjustedContent == "" {
		t.Error("expected AdjustedContent to be set in error mode")
	}
	if !strings.Contains(result.AdjustedContent, "Output verification failed") {
		t.Errorf("expected verification error prefix, got %q", result.AdjustedContent)
	}
	if !strings.Contains(result.AdjustedContent, "not json") {
		t.Errorf("expected original output preview in adjusted content")
	}
}

func TestVerifyAndNormalize_ErrorMode(t *testing.T) {
	verifyConfig := OutputVerificationConfig{
		Mode: VerifyModeError,
		Schemas: map[string]OutputSchema{
			"Read": {Type: "json"},
		},
	}
	normConfig := DefaultResultNormalizationConfig()

	// Invalid JSON should result in an error normalized result.
	result := VerifyAndNormalize("Read", "not json", false, verifyConfig, normConfig)
	if !result.IsError {
		t.Error("expected error when verification fails in error mode")
	}
	if !strings.Contains(result.Content, "Output verification failed") {
		t.Errorf("expected verification error in content, got %q", result.Content)
	}
}

func TestVerifyAndNormalize_WarnMode(t *testing.T) {
	verifyConfig := OutputVerificationConfig{
		Mode: VerifyModeWarn,
		Schemas: map[string]OutputSchema{
			"Read": {Type: "json"},
		},
	}
	normConfig := DefaultResultNormalizationConfig()

	// Invalid JSON in warn mode should still pass through to normalization unchanged.
	result := VerifyAndNormalize("Read", "not json but still returned", false, verifyConfig, normConfig)
	if result.IsError {
		t.Error("expected no error in warn mode")
	}
	if result.Content != "not json but still returned" {
		t.Errorf("expected original content in warn mode, got %q", result.Content)
	}
}

func TestVerifyAndNormalize_ValidOutput(t *testing.T) {
	verifyConfig := OutputVerificationConfig{
		Mode: VerifyModeError,
		Schemas: map[string]OutputSchema{
			"Read": {Type: "json", RequiredFields: []string{"content"}},
		},
	}
	normConfig := DefaultResultNormalizationConfig()

	result := VerifyAndNormalize("Read", `{"content":"hello"}`, false, verifyConfig, normConfig)
	if result.IsError {
		t.Errorf("expected no error for valid output, got %q", result.Content)
	}
	if result.Content != `{"content":"hello"}` {
		t.Errorf("expected pass-through for valid JSON, got %q", result.Content)
	}
}

func TestVerifyAndNormalize_OffMode(t *testing.T) {
	verifyConfig := OutputVerificationConfig{
		Mode: VerifyModeOff,
	}
	normConfig := DefaultResultNormalizationConfig()

	// Even with invalid output, off mode should pass through to normalization.
	result := VerifyAndNormalize("Read", "anything", false, verifyConfig, normConfig)
	if result.IsError {
		t.Error("expected no error when verification is off")
	}
	if result.Content != "anything" {
		t.Errorf("expected pass-through, got %q", result.Content)
	}
}

func TestVerifyToolOutput_ConcurrentSafety(t *testing.T) {
	config := OutputVerificationConfig{
		Mode: VerifyModeWarn,
		Schemas: map[string]OutputSchema{
			"Read": {Type: "json"},
			"Bash": {Type: "text"},
		},
		OnViolation: func(toolName, violation string, isError bool) {
			// no-op — just verifying concurrent access doesn't panic
		},
	}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if n%2 == 0 {
				VerifyToolOutput("Read", "not json", false, config)
			} else {
				VerifyToolOutput("Bash", "output", false, config)
			}
		}(i)
	}
	wg.Wait()
}
