package engine

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/execution"
)

func validAssistantDelta() *CanonicalProjectionEvent {
	return &CanonicalProjectionEvent{
		Version: CanonicalProjectionVersion,
		Kind:    CanonicalProjectionAssistantDelta,
		Assistant: &CanonicalAssistantPayload{
			MessageID: "msg-1",
			Delta:     []byte("a b"),
		},
	}
}

func validToolStart() *CanonicalProjectionEvent {
	return &CanonicalProjectionEvent{
		Version: CanonicalProjectionVersion,
		Kind:    CanonicalProjectionToolStart,
		Tool: &CanonicalToolPayload{
			ToolCallID:     "call-1",
			ToolName:       "Read",
			EffectiveInput: json.RawMessage(`{"path":"engine/events.go"}`),
		},
	}
}

func validToolProgress() *CanonicalProjectionEvent {
	return &CanonicalProjectionEvent{
		Version: CanonicalProjectionVersion,
		Kind:    CanonicalProjectionToolProgress,
		Tool: &CanonicalToolPayload{
			ToolCallID: "call-1",
			Content:    "read 453 lines",
		},
	}
}

func validToolInput() *CanonicalProjectionEvent {
	return &CanonicalProjectionEvent{
		Version: CanonicalProjectionVersion,
		Kind:    CanonicalProjectionToolInput,
		Tool: &CanonicalToolPayload{
			ToolCallID:     "call-1",
			EffectiveInput: json.RawMessage(`{"path":"engine/events.go"}`),
		},
	}
}

func validToolTerminal() *CanonicalProjectionEvent {
	return &CanonicalProjectionEvent{
		Version: CanonicalProjectionVersion,
		Kind:    CanonicalProjectionToolTerminal,
		Tool: &CanonicalToolPayload{
			ToolCallID: "call-1",
			Outcome:    CanonicalToolOutcomeCompleted,
			RawOutput:  json.RawMessage(`{"lines":453}`),
		},
	}
}

func TestCanonicalProjectionLifecycle(t *testing.T) {
	t.Run("valid envelopes", func(t *testing.T) {
		cases := map[string]*CanonicalProjectionEvent{
			"assistant delta":           validAssistantDelta(),
			"tool start":                validToolStart(),
			"tool start without input":  withTool(validToolStart(), func(p *CanonicalToolPayload) { p.EffectiveInput = nil }),
			"tool input":                validToolInput(),
			"tool progress":             validToolProgress(),
			"tool progress empty state": withTool(validToolProgress(), func(p *CanonicalToolPayload) { p.Content = "" }),
			"tool terminal completed":   validToolTerminal(),
			"tool terminal failed":      withTool(validToolTerminal(), func(p *CanonicalToolPayload) { p.Outcome = CanonicalToolOutcomeFailed }),
			"tool terminal raw scalars": withTool(validToolTerminal(), func(p *CanonicalToolPayload) { p.RawOutput = json.RawMessage(`"plain output"`) }),
			"tool terminal no output":   withTool(validToolTerminal(), func(p *CanonicalToolPayload) { p.RawOutput = nil }),
		}
		for name, event := range cases {
			if err := event.Validate(); err != nil {
				t.Errorf("%s: expected valid envelope, got %v", name, err)
			}
		}
	})

	t.Run("exact delta bytes preserved without normalization", func(t *testing.T) {
		delta := []byte("word1\n\nword2  \t word3")
		event := validAssistantDelta()
		event.Assistant.Delta = delta
		if err := event.Validate(); err != nil {
			t.Fatalf("expected valid envelope, got %v", err)
		}
		if !bytes.Equal(event.Assistant.Delta, delta) {
			t.Fatalf("delta bytes changed: got %q want %q", event.Assistant.Delta, delta)
		}
	})

	t.Run("invalid envelopes", func(t *testing.T) {
		cases := map[string]*CanonicalProjectionEvent{
			"nil event": nil,
			"version zero": func() *CanonicalProjectionEvent {
				e := validAssistantDelta()
				e.Version = 0
				return e
			}(),
			"future version": func() *CanonicalProjectionEvent {
				e := validAssistantDelta()
				e.Version = CanonicalProjectionVersion + 1
				return e
			}(),
			"unknown kind": func() *CanonicalProjectionEvent {
				e := validAssistantDelta()
				e.Kind = CanonicalProjectionKind("assistant_final")
				return e
			}(),
			"assistant missing payload": func() *CanonicalProjectionEvent {
				e := validAssistantDelta()
				e.Assistant = nil
				return e
			}(),
			"assistant with tool payload": func() *CanonicalProjectionEvent {
				e := validAssistantDelta()
				e.Tool = validToolProgress().Tool
				return e
			}(),
			"tool kind with assistant payload": func() *CanonicalProjectionEvent {
				e := validToolStart()
				e.Assistant = validAssistantDelta().Assistant
				return e
			}(),
			"tool kind missing payload": func() *CanonicalProjectionEvent {
				e := validToolStart()
				e.Tool = nil
				return e
			}(),
			"assistant missing message ID": func() *CanonicalProjectionEvent {
				e := validAssistantDelta()
				e.Assistant.MessageID = ""
				return e
			}(),
			"assistant empty delta": func() *CanonicalProjectionEvent {
				e := validAssistantDelta()
				e.Assistant.Delta = nil
				return e
			}(),
			"tool start missing call ID": func() *CanonicalProjectionEvent {
				return withTool(validToolStart(), func(p *CanonicalToolPayload) { p.ToolCallID = "" })
			}(),
			"tool start missing name": func() *CanonicalProjectionEvent {
				return withTool(validToolStart(), func(p *CanonicalToolPayload) { p.ToolName = "" })
			}(),
			"tool start invalid JSON input": func() *CanonicalProjectionEvent {
				return withTool(validToolStart(), func(p *CanonicalToolPayload) { p.EffectiveInput = json.RawMessage(`{"path":`) })
			}(),
			"tool start scalar input": func() *CanonicalProjectionEvent {
				return withTool(validToolStart(), func(p *CanonicalToolPayload) { p.EffectiveInput = json.RawMessage(`"path"`) })
			}(),
			"tool start with progress content": func() *CanonicalProjectionEvent {
				return withTool(validToolStart(), func(p *CanonicalToolPayload) { p.Content = "nope" })
			}(),
			"tool start with outcome": func() *CanonicalProjectionEvent {
				return withTool(validToolStart(), func(p *CanonicalToolPayload) { p.Outcome = CanonicalToolOutcomeCompleted })
			}(),
			"tool start with raw output": func() *CanonicalProjectionEvent {
				return withTool(validToolStart(), func(p *CanonicalToolPayload) { p.RawOutput = json.RawMessage(`{}`) })
			}(),
			"tool input missing effective input": func() *CanonicalProjectionEvent {
				return withTool(validToolInput(), func(p *CanonicalToolPayload) { p.EffectiveInput = nil })
			}(),
			"tool input null effective input": func() *CanonicalProjectionEvent {
				return withTool(validToolInput(), func(p *CanonicalToolPayload) { p.EffectiveInput = json.RawMessage(`null`) })
			}(),
			"tool input with tool name": func() *CanonicalProjectionEvent {
				return withTool(validToolInput(), func(p *CanonicalToolPayload) { p.ToolName = "Read" })
			}(),
			"tool input with progress content": func() *CanonicalProjectionEvent {
				return withTool(validToolInput(), func(p *CanonicalToolPayload) { p.Content = "nope" })
			}(),
			"tool progress missing call ID": func() *CanonicalProjectionEvent {
				return withTool(validToolProgress(), func(p *CanonicalToolPayload) { p.ToolCallID = "" })
			}(),
			"tool progress with tool name": func() *CanonicalProjectionEvent {
				return withTool(validToolProgress(), func(p *CanonicalToolPayload) { p.ToolName = "Read" })
			}(),
			"tool progress with effective input": func() *CanonicalProjectionEvent {
				return withTool(validToolProgress(), func(p *CanonicalToolPayload) { p.EffectiveInput = json.RawMessage(`{}`) })
			}(),
			"tool terminal missing call ID": func() *CanonicalProjectionEvent {
				return withTool(validToolTerminal(), func(p *CanonicalToolPayload) { p.ToolCallID = "" })
			}(),
			"tool terminal missing outcome": func() *CanonicalProjectionEvent {
				return withTool(validToolTerminal(), func(p *CanonicalToolPayload) { p.Outcome = "" })
			}(),
			"tool terminal unknown outcome": func() *CanonicalProjectionEvent {
				return withTool(validToolTerminal(), func(p *CanonicalToolPayload) { p.Outcome = CanonicalToolOutcome("cancelled") })
			}(),
			"tool terminal invalid JSON output": func() *CanonicalProjectionEvent {
				return withTool(validToolTerminal(), func(p *CanonicalToolPayload) { p.RawOutput = json.RawMessage(`{`) })
			}(),
			"tool terminal with tool name": func() *CanonicalProjectionEvent {
				return withTool(validToolTerminal(), func(p *CanonicalToolPayload) { p.ToolName = "Read" })
			}(),
			"tool terminal with content": func() *CanonicalProjectionEvent {
				return withTool(validToolTerminal(), func(p *CanonicalToolPayload) { p.Content = "nope" })
			}(),
		}
		for name, event := range cases {
			if err := event.Validate(); err == nil {
				t.Errorf("%s: expected validation error", name)
			}
		}
	})

	t.Run("clone deep-copies raw JSON and slices", func(t *testing.T) {
		original := validToolStart()
		clone := original.Clone()
		if clone == original || clone.Tool == original.Tool {
			t.Fatal("clone must not alias the original envelope or payload")
		}
		clone.Tool.EffectiveInput[1] = 'X'
		if bytes.Equal(clone.Tool.EffectiveInput, original.Tool.EffectiveInput) {
			t.Fatal("clone effective input shares memory with the original")
		}
		if string(original.Tool.EffectiveInput) != `{"path":"engine/events.go"}` {
			t.Fatalf("original effective input mutated: %s", original.Tool.EffectiveInput)
		}

		terminal := validToolTerminal()
		terminalClone := terminal.Clone()
		terminalClone.Tool.RawOutput[1] = 'X'
		if string(terminal.Tool.RawOutput) != `{"lines":453}` {
			t.Fatalf("original raw output mutated: %s", terminal.Tool.RawOutput)
		}

		assistant := validAssistantDelta()
		assistantClone := assistant.Clone()
		if assistantClone.Assistant == assistant.Assistant {
			t.Fatal("clone must not alias the assistant payload")
		}
		assistantClone.Assistant.Delta[0] = 'X'
		if string(assistant.Assistant.Delta) != "a b" {
			t.Fatalf("original delta mutated: %q", assistant.Assistant.Delta)
		}
	})

	t.Run("clone of nil is nil", func(t *testing.T) {
		var event *CanonicalProjectionEvent
		if event.Clone() != nil {
			t.Fatal("expected nil clone for nil envelope")
		}
	})

	t.Run("clone preserves validity and field values", func(t *testing.T) {
		for _, original := range []*CanonicalProjectionEvent{
			validAssistantDelta(),
			validToolStart(),
			validToolInput(),
			validToolProgress(),
			validToolTerminal(),
		} {
			clone := original.Clone()
			if err := clone.Validate(); err != nil {
				t.Fatalf("clone of valid %s envelope is invalid: %v", original.Kind, err)
			}
		}
	})

	t.Run("query event carries optional additive projection", func(t *testing.T) {
		var event QueryEvent
		if event.CanonicalProjection != nil {
			t.Fatal("CanonicalProjection must default to nil on existing events")
		}
		event.CanonicalProjection = validToolStart()
		if err := event.CanonicalProjection.Validate(); err != nil {
			t.Fatalf("attached projection must validate: %v", err)
		}
	})
}

// withTool mutates the tool payload of a valid envelope for a negative case.
func withTool(e *CanonicalProjectionEvent, mutate func(*CanonicalToolPayload)) *CanonicalProjectionEvent {
	mutate(e.Tool)
	return e
}

const (
	testAWSKey       = "AKIAIOSFODNN7EXAMPLE"
	testGCPKey       = "AI" + "zaSyD4iE4jK9s8f7g6h5j4k3l2m1n0b9v8c7x"
	testGitHubPAT    = "gh" + "p_16C7e42F292c6912E7710c838347Ae178B4a"
	testOpenAIKey    = "sk-proj-abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGH"
	testAnthropicKey = "sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789"
	testSlackToken   = "xo" + "xb-123456789012-123456789012-abcdefghijklmnopqrstuvwx"
	testPrivateKey   = "-----BEGIN RSA PRIVATE KEY-----\nMIIBOgIBAAJBAK34\n-----END RSA PRIVATE KEY-----"
)

func TestCanonicalProjectionToolLifecycleBuilder(t *testing.T) {
	t.Run("tool start from committed tool call", func(t *testing.T) {
		event, err := buildCanonicalToolStartProjection(&schema.ToolCall{
			ID: "call-1",
			Function: schema.FunctionCall{
				Name:      "Read",
				Arguments: `{"path":"engine/events.go"}`,
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertCanonicalBuilderEvent(t, event)
		projection := event.CanonicalProjection
		if projection.Kind != CanonicalProjectionToolStart {
			t.Fatalf("kind = %q, want tool_start", projection.Kind)
		}
		if projection.Tool.ToolCallID != "call-1" || projection.Tool.ToolName != "Read" {
			t.Fatalf("payload = %#v", projection.Tool)
		}
		if len(projection.Tool.EffectiveInput) != 0 {
			t.Fatalf("tool_start must not carry input, got %s", projection.Tool.EffectiveInput)
		}
	})

	t.Run("tool start rejects nil and invalid calls", func(t *testing.T) {
		cases := map[string]*schema.ToolCall{
			"nil call": nil,
			"missing call ID": {
				ID:       "",
				Function: schema.FunctionCall{Name: "Read"},
			},
			"missing tool name": {
				ID: "call-1",
			},
		}
		for name, call := range cases {
			if _, err := buildCanonicalToolStartProjection(call); err == nil {
				t.Errorf("%s: expected error", name)
			}
		}
	})

	t.Run("tool input preserves shape and redacts nested credential keys", func(t *testing.T) {
		input := map[string]any{
			"path":      "engine/events.go",
			"count":     float64(2),
			"flag":      true,
			"tokenizer": "ordinary-value",
			"nested": map[string]any{
				"Password": "hunter2",
				"items": []any{
					"plain",
					map[string]any{"apiKey": "abc", "label": "kept"},
					map[string]any{"X-API-Key": "abc"},
					map[string]any{"aws_secret_access_key": "abc"},
					map[string]any{"clientSecret": "abc"},
					map[string]any{"accessToken": "abc"},
					map[string]any{
						"authorizationHeader": "Basic dXNlcjpwYXNz",
					},
				},
			},
		}
		event, err := buildCanonicalToolInputProjection("call-1", input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertCanonicalBuilderEvent(t, event)
		projection := event.CanonicalProjection
		if projection.Kind != CanonicalProjectionToolInput {
			t.Fatalf("kind = %q, want tool_input", projection.Kind)
		}
		var decoded map[string]any
		if err := json.Unmarshal(projection.Tool.EffectiveInput, &decoded); err != nil {
			t.Fatalf("effective input is not a valid JSON object: %v", err)
		}
		if decoded["path"] != "engine/events.go" ||
			decoded["count"] != float64(2) ||
			decoded["flag"] != true ||
			decoded["tokenizer"] != "ordinary-value" {
			t.Fatalf("ordinary values changed: %s", projection.Tool.EffectiveInput)
		}
		nested := decoded["nested"].(map[string]any)
		if nested["Password"] != canonicalRedactedPlaceholder {
			t.Fatalf("nested credential key not redacted: %s", projection.Tool.EffectiveInput)
		}
		items := nested["items"].([]any)
		if items[0] != "plain" {
			t.Fatalf("array shape changed: %s", projection.Tool.EffectiveInput)
		}
		for index, key := range []string{
			"apiKey",
			"X-API-Key",
			"aws_secret_access_key",
			"clientSecret",
			"accessToken",
			"authorizationHeader",
		} {
			entry := items[index+1].(map[string]any)
			if entry[key] != canonicalRedactedPlaceholder {
				t.Errorf("credential key %q not redacted: %s", key, projection.Tool.EffectiveInput)
			}
		}
		if items[1].(map[string]any)["label"] != "kept" {
			t.Fatalf("non-credential sibling changed: %s", projection.Tool.EffectiveInput)
		}
	})

	t.Run("tool input redacts Config-style value named by sibling", func(t *testing.T) {
		cases := map[string]map[string]any{
			"setting names credential": {
				"setting": "provider.apiKey",
				"value":   "raw-config-value",
			},
			"camel setting names credential": {
				"setting": "provider.clientSecret",
				"value":   "raw-config-value",
			},
			"legacy key names credential": {
				"key":   "access_key",
				"value": "raw-secret",
			},
			"name names credential": {
				"name":  "API_TOKEN",
				"value": "raw-secret",
			},
			"non-credential setting keeps value": {
				"setting": "model",
				"value":   "glm-5",
			},
		}
		for name, input := range cases {
			event, err := buildCanonicalToolInputProjection("call-1", input)
			if err != nil {
				t.Fatalf("%s: unexpected error: %v", name, err)
			}
			var decoded map[string]any
			if err := json.Unmarshal(event.CanonicalProjection.Tool.EffectiveInput, &decoded); err != nil {
				t.Fatalf("%s: invalid effective input: %v", name, err)
			}
			if name == "non-credential setting keeps value" {
				if decoded["value"] != "glm-5" {
					t.Errorf("%s: value must be preserved: %v", name, decoded)
				}
				continue
			}
			if decoded["value"] != canonicalRedactedPlaceholder {
				t.Errorf("%s: sibling value not redacted: %v", name, decoded)
			}
		}
	})

	t.Run("high-confidence tokens redacted in ordinary strings", func(t *testing.T) {
		secrets := []string{
			testAWSKey, testGCPKey, testGitHubPAT, testOpenAIKey,
			testAnthropicKey, testSlackToken, testPrivateKey,
		}
		input := map[string]any{
			"command": "deploy with " + testAWSKey + " and " + testGitHubPAT,
			"note":    testOpenAIKey + " " + testAnthropicKey + " " + testSlackToken,
			"gcp":     testGCPKey,
			"pem":     "prefix " + testPrivateKey + " suffix",
		}
		event, err := buildCanonicalToolInputProjection("call-1", input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		encoded := string(event.CanonicalProjection.Tool.EffectiveInput)
		for _, secret := range secrets {
			if strings.Contains(encoded, secret) {
				t.Errorf("effective input still contains credential bytes: %s", encoded)
			}
		}
		if !strings.Contains(encoded, canonicalRedactedPlaceholder) {
			t.Errorf("expected redaction placeholder in %s", encoded)
		}
		var decoded map[string]any
		if err := json.Unmarshal(event.CanonicalProjection.Tool.EffectiveInput, &decoded); err != nil {
			t.Fatalf("effective input is not a valid JSON object: %v", err)
		}
		if decoded["pem"] != "prefix "+canonicalRedactedPlaceholder+" suffix" {
			t.Errorf("PEM block not redacted in place: %q", decoded["pem"])
		}
	})

	t.Run("tool input does not mutate caller input", func(t *testing.T) {
		input := map[string]any{
			"nested": map[string]any{"password": "hunter2", "path": "a"},
			"items":  []any{map[string]any{"token": "abc"}},
			"note":   "uses " + testAWSKey,
		}
		before, err := json.Marshal(input)
		if err != nil {
			t.Fatalf("marshal before: %v", err)
		}
		if _, err := buildCanonicalToolInputProjection("call-1", input); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		after, err := json.Marshal(input)
		if err != nil {
			t.Fatalf("marshal after: %v", err)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("caller input mutated:\nbefore %s\nafter  %s", before, after)
		}
	})

	t.Run("tool input rejects nil and invalid inputs", func(t *testing.T) {
		if _, err := buildCanonicalToolInputProjection("", map[string]any{}); err == nil {
			t.Error("empty call ID: expected error")
		}
		if _, err := buildCanonicalToolInputProjection("call-1", nil); err == nil {
			t.Error("nil input: expected error")
		}
		invalid := map[string]any{"bad": func() {}}
		if _, err := buildCanonicalToolInputProjection("call-1", invalid); err == nil {
			t.Error("unmarshalable input: expected error")
		}
		event, err := buildCanonicalToolInputProjection("call-1", map[string]any{})
		if err != nil {
			t.Fatalf("empty object: unexpected error: %v", err)
		}
		if string(event.CanonicalProjection.Tool.EffectiveInput) != "{}" {
			t.Fatalf("empty input = %s, want {}", event.CanonicalProjection.Tool.EffectiveInput)
		}
	})

	t.Run("tool progress is a redacted complete snapshot", func(t *testing.T) {
		event, err := buildCanonicalToolProgressProjection(
			"call-1",
			"read 453 lines with "+testGitHubPAT,
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertCanonicalBuilderEvent(t, event)
		projection := event.CanonicalProjection
		if projection.Kind != CanonicalProjectionToolProgress {
			t.Fatalf("kind = %q, want tool_progress", projection.Kind)
		}
		want := "read 453 lines with " + canonicalRedactedPlaceholder
		if projection.Tool.Content != want {
			t.Fatalf("content = %q, want %q", projection.Tool.Content, want)
		}

		plain, err := buildCanonicalToolProgressProjection("call-1", "read 453 lines")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if plain.CanonicalProjection.Tool.Content != "read 453 lines" {
			t.Fatalf("ordinary snapshot changed: %q", plain.CanonicalProjection.Tool.Content)
		}
		diagnostic, err := buildCanonicalToolProgressProjection(
			"call-1",
			"authorization=Bearer-real password: hunter2 Bearer direct-token "+
				"clientSecret=camel-secret Authorization: Bearer spaced-token "+
				"authorizationHeader=Basic dXNlcjpwYXNz",
		)
		if err != nil {
			t.Fatalf("diagnostic snapshot: unexpected error: %v", err)
		}
		for _, secret := range []string{
			"Bearer-real",
			"hunter2",
			"direct-token",
			"camel-secret",
			"spaced-token",
			"dXNlcjpwYXNz",
		} {
			if strings.Contains(
				diagnostic.CanonicalProjection.Tool.Content,
				secret,
			) {
				t.Fatalf(
					"diagnostic snapshot retained %q: %q",
					secret,
					diagnostic.CanonicalProjection.Tool.Content,
				)
			}
		}
		if _, err := buildCanonicalToolProgressProjection("", "x"); err == nil {
			t.Error("empty call ID: expected error")
		}
		empty, err := buildCanonicalToolProgressProjection("call-1", "")
		if err != nil {
			t.Fatalf("empty snapshot: unexpected error: %v", err)
		}
		if err := empty.CanonicalProjection.Validate(); err != nil {
			t.Fatalf("empty snapshot must validate: %v", err)
		}
		ordinary, err := buildCanonicalToolProgressProjection(
			"call-1",
			"tokenizer=sentencepiece",
		)
		if err != nil {
			t.Fatalf("ordinary assignment: unexpected error: %v", err)
		}
		if ordinary.CanonicalProjection.Tool.Content !=
			"tokenizer=sentencepiece" {
			t.Fatalf(
				"ordinary assignment changed: %q",
				ordinary.CanonicalProjection.Tool.Content,
			)
		}
	})

	t.Run("tool terminal maps outcome and redacts output", func(t *testing.T) {
		cases := map[string]struct {
			result  *execution.ToolResult
			want    CanonicalToolOutcome
			secrets []string
		}{
			"success": {
				result: &execution.ToolResult{ToolCallID: "call-1", Result: "wrote 12 lines"},
				want:   CanonicalToolOutcomeCompleted,
			},
			"error": {
				result: &execution.ToolResult{
					ToolCallID: "call-2",
					Result:     "auth failed for " + testAnthropicKey,
					IsError:    true,
				},
				want:    CanonicalToolOutcomeFailed,
				secrets: []string{testAnthropicKey},
			},
			"authorization header": {
				result: &execution.ToolResult{
					ToolCallID: "call-3",
					Result: "request failed with " +
						"authorizationHeader=Basic dXNlcjpwYXNz",
					IsError: true,
				},
				want:    CanonicalToolOutcomeFailed,
				secrets: []string{"dXNlcjpwYXNz"},
			},
			"empty result": {
				result: &execution.ToolResult{ToolCallID: "call-4"},
				want:   CanonicalToolOutcomeCompleted,
			},
		}
		for name, tc := range cases {
			event, err := buildCanonicalToolTerminalProjection(tc.result)
			if err != nil {
				t.Fatalf("%s: unexpected error: %v", name, err)
			}
			assertCanonicalBuilderEvent(t, event)
			projection := event.CanonicalProjection
			if projection.Kind != CanonicalProjectionToolTerminal {
				t.Fatalf("%s: kind = %q, want tool_terminal", name, projection.Kind)
			}
			if projection.Tool.Outcome != tc.want {
				t.Errorf("%s: outcome = %q, want %q", name, projection.Tool.Outcome, tc.want)
			}
			if projection.Tool.ToolCallID != tc.result.ToolCallID {
				t.Errorf("%s: call ID = %q", name, projection.Tool.ToolCallID)
			}
			var output string
			if err := json.Unmarshal(projection.Tool.RawOutput, &output); err != nil {
				t.Fatalf("%s: raw output is not a JSON string: %v", name, err)
			}
			for _, secret := range tc.secrets {
				if strings.Contains(output, secret) {
					t.Errorf(
						"%s: terminal output still contains credential bytes",
						name,
					)
				}
			}
			if output != redactCanonicalProjectionText(tc.result.Result) {
				t.Errorf("%s: output = %q, want redacted %q", name, output, tc.result.Result)
			}
		}
	})

	t.Run("tool terminal rejects nil and invalid results", func(t *testing.T) {
		if _, err := buildCanonicalToolTerminalProjection(nil); err == nil {
			t.Error("nil result: expected error")
		}
		if _, err := buildCanonicalToolTerminalProjection(&execution.ToolResult{}); err == nil {
			t.Error("missing call ID: expected error")
		}
	})
}

// assertCanonicalBuilderEvent checks the shared builder contract: the event
// type is the canonical projection type and the attached payload validates.
func assertCanonicalBuilderEvent(t *testing.T, event QueryEvent) {
	t.Helper()
	if event.Type != EventCanonicalProjection {
		t.Fatalf("event type = %q, want %q", event.Type, EventCanonicalProjection)
	}
	if event.CanonicalProjection == nil {
		t.Fatal("canonical projection is nil")
	}
	if err := event.CanonicalProjection.Validate(); err != nil {
		t.Fatalf("built projection must validate: %v", err)
	}
}
