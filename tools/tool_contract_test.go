package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

// Tool Contract Tests
//
// These tests verify the contracts of each tool category:
// 1. Schema declares correct required/optional parameters
// 2. Missing required params returns clear error (from ValidateToolInput)
// 3. Invalid input types return clear error (not panic)
// 4. Successful execution returns expected output format
//
// Tool categories tested:
// - File Operations (Read, Write, Edit)
// - Shell (Bash)
// - Search (Grep, Glob)
// - Agent/Task (Agent, Task, SendMessage)
// - MCP/External (WebSearch, WebFetch)

// --- Helper ---

func getSchemaRequired(t *testing.T, info *schema.ToolInfo) []string {
	t.Helper()
	if info == nil || info.ParamsOneOf == nil {
		return nil
	}
	jsonSchema, err := info.ToJSONSchema()
	if err != nil || jsonSchema == nil {
		return nil
	}
	schemaBytes, err := json.Marshal(jsonSchema)
	if err != nil {
		return nil
	}
	var schemaMap map[string]any
	if err := json.Unmarshal(schemaBytes, &schemaMap); err != nil {
		return nil
	}
	reqRaw, ok := schemaMap["required"].([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(reqRaw))
	for _, r := range reqRaw {
		if s, ok := r.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

func getSchemaProperties(t *testing.T, info *schema.ToolInfo) map[string]map[string]any {
	t.Helper()
	if info == nil || info.ParamsOneOf == nil {
		return nil
	}
	jsonSchema, err := info.ToJSONSchema()
	if err != nil || jsonSchema == nil {
		return nil
	}
	schemaBytes, err := json.Marshal(jsonSchema)
	if err != nil {
		return nil
	}
	var schemaMap map[string]any
	if err := json.Unmarshal(schemaBytes, &schemaMap); err != nil {
		return nil
	}
	props, ok := schemaMap["properties"].(map[string]any)
	if !ok {
		return nil
	}
	result := make(map[string]map[string]any)
	for k, v := range props {
		if m, ok := v.(map[string]any); ok {
			result[k] = m
		}
	}
	return result
}

func assertRequiredFields(t *testing.T, info *schema.ToolInfo, expected []string) {
	t.Helper()
	required := getSchemaRequired(t, info)
	reqMap := make(map[string]bool)
	for _, r := range required {
		reqMap[r] = true
	}
	for _, exp := range expected {
		if !reqMap[exp] {
			t.Errorf("expected %q to be required in schema, required=%v", exp, required)
		}
	}
}

func assertHasProperties(t *testing.T, info *schema.ToolInfo, expected []string) {
	t.Helper()
	props := getSchemaProperties(t, info)
	for _, exp := range expected {
		if _, ok := props[exp]; !ok {
			t.Errorf("expected property %q in schema, not found", exp)
		}
	}
}

func assertValidationRejectsEmpty(t *testing.T, info *schema.ToolInfo, fields []string) {
	t.Helper()
	for _, field := range fields {
		// Missing required field entirely.
		input := map[string]any{}
		err := ValidateToolInput(info, input)
		if err == nil {
			t.Errorf("expected validation error for missing required field %q, got nil", field)
			continue
		}
		if !strings.Contains(err.Error(), field) {
			t.Errorf("validation error for missing %q should mention the field, got: %s", field, err.Error())
		}
	}
}

func assertValidationRejectsWrongType(t *testing.T, info *schema.ToolInfo, field string, wrongValue any) {
	t.Helper()
	input := map[string]any{field: wrongValue}
	// Fill other required fields with valid stubs to isolate the type error.
	required := getSchemaRequired(t, info)
	props := getSchemaProperties(t, info)
	for _, r := range required {
		if r == field {
			continue
		}
		if _, exists := input[r]; exists {
			continue
		}
		propType, _ := props[r]["type"].(string)
		switch propType {
		case "string":
			input[r] = "stub"
		case "integer", "number":
			input[r] = float64(1)
		case "boolean":
			input[r] = true
		case "array":
			input[r] = []any{}
		case "object":
			input[r] = map[string]any{}
		default:
			input[r] = "stub"
		}
	}

	err := ValidateToolInput(info, input)
	if err == nil {
		t.Errorf("expected validation error for %q with wrong type %T, got nil", field, wrongValue)
		return
	}
	if !strings.Contains(err.Error(), field) {
		t.Errorf("validation error for wrong type on %q should mention field, got: %s", field, err.Error())
	}
}

// --- Category 1: File Operations ---

func TestContractReadTool(t *testing.T) {
	tool := ReadTool()
	info := tool.Info

	t.Run("schema_name", func(t *testing.T) {
		if info.Name != "Read" {
			t.Errorf("expected name 'Read', got %q", info.Name)
		}
	})

	t.Run("required_params", func(t *testing.T) {
		assertRequiredFields(t, info, []string{"file_path"})
	})

	t.Run("has_optional_params", func(t *testing.T) {
		assertHasProperties(t, info, []string{"file_path", "offset", "limit"})
	})

	t.Run("missing_required_returns_error", func(t *testing.T) {
		assertValidationRejectsEmpty(t, info, []string{"file_path"})
	})

	t.Run("wrong_type_returns_error", func(t *testing.T) {
		// file_path should be string, not number
		assertValidationRejectsWrongType(t, info, "file_path", 12345.0)
	})

	t.Run("execution_with_missing_file_returns_error", func(t *testing.T) {
		result, err := tool.Execute(`{"file_path": "/nonexistent/path/to/file.txt"}`)
		// Either returns an error or a result indicating the error.
		if err == nil && !strings.Contains(strings.ToLower(result), "error") && !strings.Contains(strings.ToLower(result), "no such file") {
			t.Error("expected error indication for non-existent file")
		}
	})
}

func TestContractWriteTool(t *testing.T) {
	tool := WriteTool()
	info := tool.Info

	t.Run("schema_name", func(t *testing.T) {
		if info.Name != "Write" {
			t.Errorf("expected name 'Write', got %q", info.Name)
		}
	})

	t.Run("required_params", func(t *testing.T) {
		assertRequiredFields(t, info, []string{"file_path", "content"})
	})

	t.Run("missing_required_returns_error", func(t *testing.T) {
		assertValidationRejectsEmpty(t, info, []string{"file_path", "content"})
	})

	t.Run("wrong_type_returns_error", func(t *testing.T) {
		assertValidationRejectsWrongType(t, info, "file_path", []any{"not", "a", "string"})
		assertValidationRejectsWrongType(t, info, "content", 42.0)
	})

	t.Run("execution_with_valid_input_succeeds", func(t *testing.T) {
		dir := t.TempDir()
		path := dir + "/contract_test.txt"
		result, err := tool.Execute(`{"file_path": "` + path + `", "content": "contract test"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
			return
		}
		if result == "" {
			t.Error("expected non-empty result on success")
		}
	})
}

func TestContractEditTool(t *testing.T) {
	tool := EditTool()
	info := tool.Info

	t.Run("schema_name", func(t *testing.T) {
		if info.Name != "Edit" {
			t.Errorf("expected name 'Edit', got %q", info.Name)
		}
	})

	t.Run("required_params", func(t *testing.T) {
		assertRequiredFields(t, info, []string{"file_path", "old_string", "new_string"})
	})

	t.Run("has_optional_replace_all", func(t *testing.T) {
		assertHasProperties(t, info, []string{"replace_all"})
	})

	t.Run("missing_required_returns_error", func(t *testing.T) {
		assertValidationRejectsEmpty(t, info, []string{"file_path", "old_string", "new_string"})
	})

	t.Run("wrong_type_returns_error", func(t *testing.T) {
		assertValidationRejectsWrongType(t, info, "replace_all", "not_a_boolean")
	})
}

// --- Category 2: Shell ---

func TestContractBashTool(t *testing.T) {
	tool := BashTool()
	info := tool.Info

	t.Run("schema_name", func(t *testing.T) {
		if info.Name != "Bash" {
			t.Errorf("expected name 'Bash', got %q", info.Name)
		}
	})

	t.Run("required_params", func(t *testing.T) {
		assertRequiredFields(t, info, []string{"command"})
	})

	t.Run("has_optional_params", func(t *testing.T) {
		assertHasProperties(t, info, []string{"command", "timeout", "description", "run_in_background"})
	})

	t.Run("missing_required_returns_error", func(t *testing.T) {
		assertValidationRejectsEmpty(t, info, []string{"command"})
	})

	t.Run("wrong_type_returns_error", func(t *testing.T) {
		// timeout should be integer, not string
		assertValidationRejectsWrongType(t, info, "timeout", "not_a_number")
		// run_in_background should be boolean, not string
		assertValidationRejectsWrongType(t, info, "run_in_background", "yes")
	})
}

// --- Category 3: Search ---

func TestContractGrepTool(t *testing.T) {
	tool := GrepTool()
	info := tool.Info

	t.Run("schema_name", func(t *testing.T) {
		if info.Name != "Grep" {
			t.Errorf("expected name 'Grep', got %q", info.Name)
		}
	})

	t.Run("required_params", func(t *testing.T) {
		assertRequiredFields(t, info, []string{"pattern"})
	})

	t.Run("has_optional_params", func(t *testing.T) {
		assertHasProperties(t, info, []string{"pattern", "path"})
	})

	t.Run("missing_required_returns_error", func(t *testing.T) {
		assertValidationRejectsEmpty(t, info, []string{"pattern"})
	})

	t.Run("wrong_type_returns_error", func(t *testing.T) {
		assertValidationRejectsWrongType(t, info, "pattern", 42.0)
	})
}

func TestContractGlobTool(t *testing.T) {
	tool := GlobTool()
	info := tool.Info

	t.Run("schema_name", func(t *testing.T) {
		if info.Name != "Glob" {
			t.Errorf("expected name 'Glob', got %q", info.Name)
		}
	})

	t.Run("required_params", func(t *testing.T) {
		assertRequiredFields(t, info, []string{"pattern"})
	})

	t.Run("has_optional_path", func(t *testing.T) {
		assertHasProperties(t, info, []string{"pattern", "path"})
	})

	t.Run("missing_required_returns_error", func(t *testing.T) {
		assertValidationRejectsEmpty(t, info, []string{"pattern"})
	})

	t.Run("wrong_type_returns_error", func(t *testing.T) {
		assertValidationRejectsWrongType(t, info, "pattern", false)
	})

	t.Run("execution_with_valid_input_succeeds", func(t *testing.T) {
		dir := t.TempDir()
		result, err := tool.Execute(`{"pattern": "*.go", "path": "` + dir + `"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
			return
		}
		// Empty dir returns no matches, but no error.
		_ = result
	})
}

// --- Category 4: Agent/Task ---

func TestContractAgentTool(t *testing.T) {
	tool := AgentTool()
	info := tool.Info

	t.Run("schema_name", func(t *testing.T) {
		if info.Name != "Agent" {
			t.Errorf("expected name 'Agent', got %q", info.Name)
		}
	})

	t.Run("required_params", func(t *testing.T) {
		assertRequiredFields(t, info, []string{"description", "prompt"})
	})

	t.Run("has_optional_params", func(t *testing.T) {
		assertHasProperties(t, info, []string{"subagent_type", "model", "run_in_background"})
	})

	t.Run("missing_required_returns_error", func(t *testing.T) {
		assertValidationRejectsEmpty(t, info, []string{"description", "prompt"})
	})

	t.Run("wrong_type_returns_error", func(t *testing.T) {
		assertValidationRejectsWrongType(t, info, "description", 123.0)
		assertValidationRejectsWrongType(t, info, "run_in_background", "true_string")
	})

	t.Run("explicit_owner_without_executor_returns_fallback", func(t *testing.T) {
		result, err := tool.ExecuteCtx(
			WithAgentRunner(context.Background(), NewAgentRunner(1)),
			`{"description": "test task", "prompt": "do something"}`,
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
			return
		}
		if !strings.Contains(result, "not available") {
			t.Errorf("expected fallback message about executor not available, got: %s", result)
		}
	})

	t.Run("unbound_execution_fails_closed", func(t *testing.T) {
		_, err := tool.Execute(
			`{"description": "test task", "prompt": "do something"}`,
		)
		if err != ErrMissingToolOwner {
			t.Fatalf("error = %v, want %v", err, ErrMissingToolOwner)
		}
	})
}

func TestContractSendMessageTool(t *testing.T) {
	tool := SendMessageTool()
	info := tool.Info

	t.Run("schema_name", func(t *testing.T) {
		if info.Name != "SendMessage" {
			t.Errorf("expected name 'SendMessage', got %q", info.Name)
		}
	})

	t.Run("required_params", func(t *testing.T) {
		assertRequiredFields(t, info, []string{"to", "message"})
	})

	t.Run("missing_required_returns_error", func(t *testing.T) {
		assertValidationRejectsEmpty(t, info, []string{"to", "message"})
	})

	t.Run("wrong_type_returns_error", func(t *testing.T) {
		assertValidationRejectsWrongType(t, info, "to", 42.0)
		assertValidationRejectsWrongType(t, info, "message", []any{})
	})
}

func TestContractTodoWriteTool(t *testing.T) {
	tool := TodoWriteTool()
	info := tool.Info

	t.Run("schema_name", func(t *testing.T) {
		if info.Name != "TodoWrite" {
			t.Errorf("expected name 'TodoWrite', got %q", info.Name)
		}
	})

	t.Run("required_params", func(t *testing.T) {
		assertRequiredFields(t, info, []string{"todos"})
	})

	t.Run("missing_required_returns_error", func(t *testing.T) {
		assertValidationRejectsEmpty(t, info, []string{"todos"})
	})

	t.Run("wrong_type_returns_error", func(t *testing.T) {
		// todos should be array, not string
		assertValidationRejectsWrongType(t, info, "todos", "not_an_array")
	})
}

// --- Category 5: Web/External ---

func TestContractWebSearchTool(t *testing.T) {
	tool := WebSearchTool()
	info := tool.Info

	t.Run("schema_name", func(t *testing.T) {
		if info.Name != "WebSearch" {
			t.Errorf("expected name 'WebSearch', got %q", info.Name)
		}
	})

	t.Run("required_params", func(t *testing.T) {
		assertRequiredFields(t, info, []string{"query"})
	})

	t.Run("has_optional_params", func(t *testing.T) {
		assertHasProperties(t, info, []string{"query", "allowed_domains", "blocked_domains"})
	})

	t.Run("missing_required_returns_error", func(t *testing.T) {
		assertValidationRejectsEmpty(t, info, []string{"query"})
	})

	t.Run("wrong_type_returns_error", func(t *testing.T) {
		assertValidationRejectsWrongType(t, info, "query", 99.0)
	})
}

func TestContractWebFetchTool(t *testing.T) {
	tool := WebFetchTool()
	info := tool.Info

	t.Run("schema_name", func(t *testing.T) {
		if info.Name != "WebFetch" {
			t.Errorf("expected name 'WebFetch', got %q", info.Name)
		}
	})

	t.Run("required_params", func(t *testing.T) {
		assertRequiredFields(t, info, []string{"url", "prompt"})
	})

	t.Run("missing_required_returns_error", func(t *testing.T) {
		assertValidationRejectsEmpty(t, info, []string{"url", "prompt"})
	})

	t.Run("wrong_type_returns_error", func(t *testing.T) {
		assertValidationRejectsWrongType(t, info, "url", false)
	})
}

// --- Category 6: Plan Mode ---

func TestContractEnterPlanModeTool(t *testing.T) {
	tool := EnterPlanModeTool()
	info := tool.Info

	t.Run("schema_name", func(t *testing.T) {
		if info.Name != "EnterPlanMode" {
			t.Errorf("expected name 'EnterPlanMode', got %q", info.Name)
		}
	})

	t.Run("no_required_params", func(t *testing.T) {
		required := getSchemaRequired(t, info)
		if len(required) != 0 {
			t.Errorf("EnterPlanMode should have no required params, got %v", required)
		}
	})
}

// --- Schema validation integration: ValidateToolInput catches real issues ---

func TestValidateToolInputCatchesMissingRequired(t *testing.T) {
	info := &schema.ToolInfo{
		Name: "TestSchema",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"name":  {Type: schema.String, Desc: "required name", Required: true},
			"age":   {Type: schema.Integer, Desc: "required age", Required: true},
			"email": {Type: schema.String, Desc: "optional email"},
		}),
	}

	// Missing both required fields.
	err := ValidateToolInput(info, map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing required fields")
		return
	}
	if !strings.Contains(err.Error(), "name") {
		t.Error("error should mention missing 'name'")
	}
	if !strings.Contains(err.Error(), "age") {
		t.Error("error should mention missing 'age'")
	}

	// Providing required fields passes.
	err = ValidateToolInput(info, map[string]any{"name": "test", "age": float64(30)})
	if err != nil {
		t.Errorf("expected no error with valid input, got: %v", err)
	}
}

func TestValidateToolInputCatchesWrongTypes(t *testing.T) {
	info := &schema.ToolInfo{
		Name: "TypeCheck",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"count": {Type: schema.Integer, Desc: "a number", Required: true},
			"flag":  {Type: schema.Boolean, Desc: "a bool"},
			"items": {Type: schema.Array, Desc: "an array"},
		}),
	}

	// Wrong type for count: string instead of integer.
	err := ValidateToolInput(info, map[string]any{"count": "not_a_number"})
	if err == nil {
		t.Fatal("expected error for wrong type on 'count'")
		return
	}
	if !strings.Contains(err.Error(), "count") {
		t.Error("error should mention 'count'")
	}

	// Wrong type for flag: number instead of boolean.
	err = ValidateToolInput(info, map[string]any{"count": float64(1), "flag": float64(1)})
	if err == nil {
		t.Fatal("expected error for wrong type on 'flag'")
		return
	}

	// Wrong type for items: string instead of array.
	err = ValidateToolInput(info, map[string]any{"count": float64(1), "items": "not_array"})
	if err == nil {
		t.Fatal("expected error for wrong type on 'items'")
		return
	}
}

func TestValidateToolInputNoSchemaNoError(t *testing.T) {
	// Tools with no schema (nil ParamsOneOf) should pass validation.
	info := &schema.ToolInfo{Name: "NoSchema"}
	err := ValidateToolInput(info, map[string]any{"anything": "goes"})
	if err != nil {
		t.Errorf("expected nil error for tool with no schema, got: %v", err)
	}
}

func TestValidateToolInputNilInfoNoError(t *testing.T) {
	err := ValidateToolInput(nil, map[string]any{"anything": "goes"})
	if err != nil {
		t.Errorf("expected nil error for nil info, got: %v", err)
	}
}

// --- Schema validation does not panic ---

func TestValidateToolInputNeverPanics(t *testing.T) {
	tools := []ToolImpl{
		ReadTool(),
		WriteTool(),
		EditTool(),
		BashTool(),
		GrepTool(),
		GlobTool(),
		AgentTool(),
		WebSearchTool(),
		WebFetchTool(),
		SendMessageTool(),
		TodoWriteTool(),
	}

	inputs := []map[string]any{
		nil,
		{},
		{"unknown_field": "value"},
		{"file_path": 12345},
		{"command": []any{"bad"}},
		{"pattern": map[string]any{"nested": true}},
	}

	for _, tool := range tools {
		for _, input := range inputs {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("ValidateToolInput panicked for tool %q with input %v: %v",
							tool.Info.Name, input, r)
					}
				}()
				// Should not panic regardless of input.
				_ = ValidateToolInput(tool.Info, input)
			}()
		}
	}
}

// --- CoerceToolInput tests ---

func TestCoerceToolInputFixesStringBooleans(t *testing.T) {
	info := &schema.ToolInfo{
		Name: "Coerce",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"flag": {Type: schema.Boolean, Desc: "a bool"},
		}),
	}

	input := map[string]any{"flag": "true"}
	result := CoerceToolInput(info, input)
	if v, ok := result["flag"].(bool); !ok || !v {
		t.Errorf("expected 'true' string coerced to bool true, got %v (%T)", result["flag"], result["flag"])
	}

	input = map[string]any{"flag": "false"}
	result = CoerceToolInput(info, input)
	if v, ok := result["flag"].(bool); !ok || v {
		t.Errorf("expected 'false' string coerced to bool false, got %v (%T)", result["flag"], result["flag"])
	}
}

func TestCoerceToolInputFixesStringNumbers(t *testing.T) {
	info := &schema.ToolInfo{
		Name: "Coerce",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"count": {Type: schema.Integer, Desc: "an int"},
			"ratio": {Type: schema.Number, Desc: "a float"},
		}),
	}

	input := map[string]any{"count": "42", "ratio": "3.14"}
	result := CoerceToolInput(info, input)

	if v, ok := result["count"].(float64); !ok || v != 42 {
		t.Errorf("expected '42' string coerced to float64(42), got %v (%T)", result["count"], result["count"])
	}
	if v, ok := result["ratio"].(float64); !ok || v != 3.14 {
		t.Errorf("expected '3.14' string coerced to float64(3.14), got %v (%T)", result["ratio"], result["ratio"])
	}
}

// --- RegisterDefaults contract verification ---

func TestRegisterDefaultsAppliesContracts(t *testing.T) {
	reg := NewRegistry()
	RegisterDefaults(reg)

	// Read-only tools should be marked correctly.
	readOnlyExpected := []string{"Grep", "Glob", "Read", "WebFetch", "WebSearch"}
	for _, name := range readOnlyExpected {
		impl, ok := reg.Get(name)
		if !ok {
			t.Errorf("expected tool %q to be registered", name)
			continue
		}
		if !impl.IsReadOnly {
			t.Errorf("tool %q should be marked IsReadOnly", name)
		}
		if impl.NeedsPermissions {
			t.Errorf("read-only tool %q should not need permissions", name)
		}
	}

	// Permission-required tools.
	permExpected := []string{"Bash", "Edit", "Write", "Agent"}
	for _, name := range permExpected {
		impl, ok := reg.Get(name)
		if !ok {
			t.Errorf("expected tool %q to be registered", name)
			continue
		}
		if !impl.NeedsPermissions {
			t.Errorf("tool %q should be marked NeedsPermissions", name)
		}
	}

	todo, ok := reg.Get("TodoWrite")
	if !ok {
		t.Fatal("expected TodoWrite to be registered")
	}
	if todo.IsReadOnly {
		t.Fatal("TodoWrite mutates host-owned runtime state and must not be read-only")
	}
	if todo.NeedsPermissions {
		t.Fatal("TodoWrite should not require interactive permission by default")
	}
	if !todo.DefaultPermissionAllowed {
		t.Fatal("TodoWrite should carry the explicit default-allow contract")
	}
	if todo.Capabilities.ActionKind != ToolActionRuntimeState {
		t.Fatalf(
			"TodoWrite action kind = %q, want runtime state",
			todo.Capabilities.ActionKind,
		)
	}

	// Destructive tools.
	destructiveExpected := []string{"Bash", "Write"}
	for _, name := range destructiveExpected {
		impl, ok := reg.Get(name)
		if !ok {
			t.Errorf("expected tool %q to be registered", name)
			continue
		}
		if !impl.IsDestructive {
			t.Errorf("tool %q should be marked IsDestructive", name)
		}
	}
}
