package tools

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/cloudwego/eino/schema"
)

// ValidateToolInput checks the input map against the tool's parameter schema.
// Returns a structured error message to help the model self-correct.
// Returns nil if validation passes or if the tool has no schema.
// Mirrors the TS reference's JSON Schema validation in toolExecution.ts.
func ValidateToolInput(info *schema.ToolInfo, input map[string]any) error {
	if info == nil || info.ParamsOneOf == nil {
		return nil
	}

	jsonSchema, err := info.ToJSONSchema()
	if err != nil || jsonSchema == nil {
		return nil
	}

	// Marshal the schema to JSON, then unmarshal to a generic map for inspection.
	// This avoids importing the jsonschema/orderedmap packages directly.
	schemaBytes, err := json.Marshal(jsonSchema)
	if err != nil {
		return nil
	}

	var schemaMap map[string]any
	if err := json.Unmarshal(schemaBytes, &schemaMap); err != nil {
		return nil
	}

	var errs []string

	// 1. Check required fields.
	if required, ok := schemaMap["required"].([]any); ok {
		for _, r := range required {
			name, _ := r.(string)
			if name == "" {
				continue
			}
			if _, present := input[name]; !present {
				errs = append(errs, fmt.Sprintf("missing required parameter %q", name))
			}
		}
	}

	// 2. Check types and enums for provided fields.
	if props, ok := schemaMap["properties"].(map[string]any); ok {
		for key, val := range input {
			propSchema, ok := props[key].(map[string]any)
			if !ok {
				continue // unknown property — allow it
			}
			if valErr := validateValue(key, val, propSchema); valErr != "" {
				errs = append(errs, valErr)
			}
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("tool input validation errors:\n- %s\nplease fix the input and try again", strings.Join(errs, "\n- "))
}

// validateValue checks a single value against its property schema.
func validateValue(key string, val any, propSchema map[string]any) string {
	if val == nil {
		return ""
	}

	expectedType, _ := propSchema["type"].(string)
	if expectedType == "" {
		return ""
	}

	if !checkType(val, expectedType) {
		return fmt.Sprintf("parameter %q should be %s, got %T", key, expectedType, val)
	}

	// Check enum constraint.
	if enumVals, ok := propSchema["enum"].([]any); ok && len(enumVals) > 0 {
		if !isInEnum(val, enumVals) {
			allowed := make([]string, 0, len(enumVals))
			for _, e := range enumVals {
				allowed = append(allowed, fmt.Sprintf("%v", e))
			}
			return fmt.Sprintf("parameter %q value %v is not one of the allowed values: [%s]",
				key, val, strings.Join(allowed, ", "))
		}
	}

	return ""
}

// checkType validates that val matches the expected JSON Schema type.
func checkType(val any, expectedType string) bool {
	switch expectedType {
	case "string":
		_, ok := val.(string)
		return ok
	case "boolean":
		_, ok := val.(bool)
		return ok
	case "integer":
		switch v := val.(type) {
		case float64:
			return v == float64(int64(v))
		case json.Number:
			_, err := v.Int64()
			return err == nil
		case int, int64, int32:
			return true
		}
		return false
	case "number":
		switch val.(type) {
		case float64, float32, int, int64, int32, json.Number:
			return true
		}
		return false
	case "array":
		_, ok := val.([]any)
		return ok
	case "object":
		_, ok := val.(map[string]any)
		return ok
	default:
		return true // unknown type — allow
	}
}

// isInEnum checks whether val is in the enum list.
func isInEnum(val any, enumVals []any) bool {
	for _, e := range enumVals {
		if fmt.Sprintf("%v", val) == fmt.Sprintf("%v", e) {
			return true
		}
	}
	return false
}

// CoerceToolInput performs semantic type coercion on tool input values.
// Models sometimes send "true" as a string for booleans, or "5" as a string for numbers.
// This normalizes such values to their correct Go types before tool execution.
// Mirrors TS semanticNumber/semanticBoolean coercion in tool schemas.
func CoerceToolInput(info *schema.ToolInfo, input map[string]any) map[string]any {
	if info == nil || info.ParamsOneOf == nil {
		return input
	}

	jsonSchema, err := info.ToJSONSchema()
	if err != nil || jsonSchema == nil {
		return input
	}

	schemaBytes, err := json.Marshal(jsonSchema)
	if err != nil {
		return input
	}

	var schemaMap map[string]any
	if err := json.Unmarshal(schemaBytes, &schemaMap); err != nil {
		return input
	}

	props, ok := schemaMap["properties"].(map[string]any)
	if !ok {
		return input
	}

	for key, val := range input {
		propSchema, ok := props[key].(map[string]any)
		if !ok {
			continue
		}
		expectedType, _ := propSchema["type"].(string)
		if expectedType == "" {
			continue
		}

		switch expectedType {
		case "boolean":
			if s, ok := val.(string); ok {
				switch strings.ToLower(s) {
				case "true":
					input[key] = true
				case "false":
					input[key] = false
				}
			}
		case "integer":
			if s, ok := val.(string); ok {
				if n, err := strconv.ParseInt(s, 10, 64); err == nil {
					input[key] = float64(n)
				}
			}
		case "number":
			if s, ok := val.(string); ok {
				if n, err := strconv.ParseFloat(s, 64); err == nil {
					input[key] = n
				}
			}
		}
	}

	return input
}
