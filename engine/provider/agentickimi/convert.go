package agentickimi

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

var functionNamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9-_]{0,127}$`)

func convertInput(input []*schema.AgenticMessage) ([]wireItem, error) {
	items := make([]wireItem, 0, len(input))
	for mi, message := range input {
		if message == nil {
			return nil, conversionError(mi, -1, "message_nil")
		}
		switch message.Role {
		case schema.AgenticRoleTypeSystem, schema.AgenticRoleTypeUser, schema.AgenticRoleTypeAssistant:
		default:
			return nil, conversionError(mi, -1, "role_unsupported")
		}
		if len(message.ContentBlocks) == 0 {
			return nil, conversionError(mi, -1, "message_empty")
		}
		for bi, block := range message.ContentBlocks {
			if block == nil {
				return nil, conversionError(mi, bi, "block_nil")
			}
			if !singleBlockPayload(block) {
				return nil, conversionError(mi, bi, "block_payload_ambiguous")
			}
			switch message.Role {
			case schema.AgenticRoleTypeSystem, schema.AgenticRoleTypeUser:
				if block.Type == schema.ContentBlockTypeUserInputText && block.UserInputText != nil {
					role := "user"
					if message.Role == schema.AgenticRoleTypeSystem {
						role = "developer"
					}
					items = append(items, wireItem{Type: "message", Role: role, Status: "completed", Content: []wirePart{{Type: "input_text", Text: block.UserInputText.Text}}})
					continue
				}
				if message.Role == schema.AgenticRoleTypeUser && block.Type == schema.ContentBlockTypeFunctionToolResult {
					output, err := toolResultOutput(block.FunctionToolResult)
					if err != nil {
						return nil, conversionError(mi, bi, "tool_result_invalid")
					}
					items = append(items, wireItem{Type: "function_call_output", CallID: block.FunctionToolResult.CallID, Output: &output, Status: "completed"})
					continue
				}
				return nil, conversionError(mi, bi, "block_unsupported")
			case schema.AgenticRoleTypeAssistant:
				switch block.Type {
				case schema.ContentBlockTypeAssistantGenText:
					if block.AssistantGenText == nil {
						return nil, conversionError(mi, bi, "assistant_text_nil")
					}
					items = append(items, wireItem{Type: "message", Role: "assistant", Status: "completed", Content: []wirePart{{Type: "output_text", Text: block.AssistantGenText.Text}}})
				case schema.ContentBlockTypeReasoning:
					if block.Reasoning == nil {
						return nil, conversionError(mi, bi, "reasoning_nil")
					}
					if block.Reasoning.Signature != "" {
						return nil, conversionError(mi, bi, "reasoning_signature_unsupported")
					}
					items = append(items, wireItem{Type: "reasoning", Status: "completed", Content: []wirePart{{Type: "reasoning_text", Text: block.Reasoning.Text}}})
				case schema.ContentBlockTypeFunctionToolCall:
					call := block.FunctionToolCall
					if call == nil || strings.TrimSpace(call.CallID) == "" || !functionNamePattern.MatchString(call.Name) || !jsonObject(defaultArguments(call.Arguments)) {
						return nil, conversionError(mi, bi, "tool_call_invalid")
					}
					items = append(items, wireItem{Type: "function_call", CallID: call.CallID, Name: call.Name, Arguments: defaultArguments(call.Arguments), Status: "completed"})
				default:
					return nil, conversionError(mi, bi, "block_unsupported")
				}
			default:
				return nil, conversionError(mi, bi, "role_unsupported")
			}
		}
	}
	return items, nil
}

func toolResultOutput(result *schema.FunctionToolResult) (string, error) {
	if result == nil || strings.TrimSpace(result.CallID) == "" {
		return "", &ProtocolError{ReasonCode: "tool_result_invalid"}
	}
	var value strings.Builder
	for _, part := range result.Content {
		if part == nil || part.Type != schema.FunctionToolResultContentBlockTypeText || part.Text == nil || part.Image != nil || part.Audio != nil || part.Video != nil || part.File != nil {
			return "", &ProtocolError{ReasonCode: "tool_result_invalid"}
		}
		value.WriteString(part.Text.Text)
	}
	return value.String(), nil
}

func convertTools(common *model.Options) ([]wireTool, error) {
	if common == nil {
		return nil, conversionError(-1, -1, "options_nil")
	}
	if len(common.DeferredTools) != 0 || common.ToolSearchTool != nil || len(common.AllowedToolNames) != 0 {
		return nil, conversionError(-1, -1, "tool_mode_unsupported")
	}
	if len(common.Tools) > maxTools {
		return nil, conversionError(-1, -1, "tool_count_exceeded")
	}
	if common.AgenticToolChoice != nil {
		if common.AgenticToolChoice.Allowed != nil || common.AgenticToolChoice.Forced != nil {
			return nil, conversionError(-1, -1, "tool_choice_unsupported")
		}
		switch common.AgenticToolChoice.Type {
		case "", schema.ToolChoiceAllowed:
		case schema.ToolChoiceForbidden:
			return nil, nil
		default:
			return nil, conversionError(-1, -1, "tool_choice_unsupported")
		}
	}
	tools := make([]wireTool, 0, len(common.Tools))
	seen := make(map[string]struct{}, len(common.Tools))
	for _, tool := range common.Tools {
		if tool == nil || !functionNamePattern.MatchString(tool.Name) {
			return nil, conversionError(-1, -1, "tool_invalid")
		}
		if _, exists := seen[tool.Name]; exists {
			return nil, conversionError(-1, -1, "tool_name_duplicate")
		}
		seen[tool.Name] = struct{}{}
		parameters := any(map[string]any{"type": "object", "properties": map[string]any{}})
		if tool.ParamsOneOf != nil {
			schemaValue, err := tool.ParamsOneOf.ToJSONSchema()
			if err != nil || schemaValue == nil {
				return nil, conversionError(-1, -1, "tool_parameters_invalid")
			}
			parameters = schemaValue
		}
		tools = append(tools, wireTool{Type: "function", Name: tool.Name, Description: tool.Desc, Parameters: parameters, Strict: false})
	}
	return tools, nil
}

func validateWireResponse(response *wireResponse) error {
	if response == nil || strings.TrimSpace(response.ID) == "" || response.Object != "response" || response.Model != ModelK3 {
		return &ProtocolError{ReasonCode: "response_envelope_invalid"}
	}
	switch response.Status {
	case "completed":
		if response.Error != nil || response.IncompleteDetails != nil {
			return &ProtocolError{ReasonCode: "response_status_invalid"}
		}
	case "incomplete":
		if response.Error != nil {
			return &ProtocolError{ReasonCode: "response_status_invalid"}
		}
		if response.IncompleteDetails == nil || response.IncompleteDetails.Reason != "max_output_tokens" {
			return &ProtocolError{ReasonCode: "response_incomplete_invalid"}
		}
	case "failed":
	default:
		return &ProtocolError{ReasonCode: "response_status_invalid"}
	}
	if response.PromptCacheOptions != nil && (response.PromptCacheOptions.Mode != "implicit" || response.PromptCacheOptions.TTL != "5m") {
		return &ProtocolError{ReasonCode: "response_cache_invalid"}
	}
	return nil
}

func responseTokenUsage(response *wireResponse) (*schema.TokenUsage, error) {
	if response == nil {
		return nil, &ProtocolError{ReasonCode: "response_envelope_invalid"}
	}
	if response.Usage == nil {
		return nil, nil
	}
	usage := response.Usage
	if usage.InputTokens == nil || usage.OutputTokens == nil || usage.TotalTokens == nil || usage.InputDetails.CachedTokens == nil || usage.InputDetails.CacheWriteTokens == nil || usage.OutputDetails.ReasoningTokens == nil {
		return nil, &ProtocolError{ReasonCode: "response_usage_invalid"}
	}
	input, output, total := *usage.InputTokens, *usage.OutputTokens, *usage.TotalTokens
	cached, written, reasoning := *usage.InputDetails.CachedTokens, *usage.InputDetails.CacheWriteTokens, *usage.OutputDetails.ReasoningTokens
	if input < 0 || output < 0 || total < 0 || cached < 0 || written < 0 || reasoning < 0 || input > int(^uint(0)>>1)-output || input+output != total || cached > input || written > input || cached > input-written || reasoning > output {
		return nil, &ProtocolError{ReasonCode: "response_usage_invalid"}
	}
	return &schema.TokenUsage{PromptTokens: input, CompletionTokens: output, TotalTokens: total, PromptTokenDetails: schema.PromptTokenDetails{CachedTokens: cached, CacheWriteTokens: written}, CompletionTokensDetails: schema.CompletionTokensDetails{ReasoningTokens: reasoning}}, nil
}

func responseMessage(response *wireResponse) (*schema.AgenticMessage, error) {
	if response == nil || (response.Status != "completed" && response.Status != "incomplete") {
		return nil, &ProtocolError{ReasonCode: "response_status_invalid"}
	}
	usage, err := responseTokenUsage(response)
	if err != nil {
		return nil, err
	}
	message := &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant}
	seenCallIDs := make(map[string]struct{})
	for index, item := range response.Output {
		if response.Status == "incomplete" && item.Type == "function_call" {
			// Output-limit terminals report spent tokens, but do not authorize
			// even a syntactically complete function call for execution.
			if item.Namespace != "" || item.EncryptedContent != nil || (item.Status != "completed" && item.Status != "in_progress") {
				return nil, &ProtocolError{ReasonCode: "response_function_invalid"}
			}
			continue
		}
		partialText := response.Status == "incomplete" && item.Status == "in_progress" && (item.Type == "reasoning" || item.Type == "message")
		if (item.Status != "completed" && !partialText) || item.EncryptedContent != nil {
			return nil, &ProtocolError{ReasonCode: "response_output_invalid"}
		}
		switch item.Type {
		case "message":
			if item.Role != "assistant" {
				return nil, &ProtocolError{ReasonCode: "response_output_invalid"}
			}
			text, ok := joinedParts(item.Content, "output_text")
			if !ok {
				return nil, &ProtocolError{ReasonCode: "response_output_invalid"}
			}
			message.ContentBlocks = append(message.ContentBlocks, &schema.ContentBlock{Type: schema.ContentBlockTypeAssistantGenText, AssistantGenText: &schema.AssistantGenText{Text: text}, StreamingMeta: &schema.StreamingMeta{Index: index}})
		case "reasoning":
			text, ok := joinedParts(item.Summary, "summary_text")
			if !ok {
				return nil, &ProtocolError{ReasonCode: "response_output_invalid"}
			}
			message.ContentBlocks = append(message.ContentBlocks, &schema.ContentBlock{Type: schema.ContentBlockTypeReasoning, Reasoning: &schema.Reasoning{Text: text}, StreamingMeta: &schema.StreamingMeta{Index: index}})
		case "function_call":
			if item.Namespace != "" || strings.TrimSpace(item.CallID) == "" || !functionNamePattern.MatchString(item.Name) || !jsonObject(item.Arguments) {
				return nil, &ProtocolError{ReasonCode: "response_function_invalid"}
			}
			if _, exists := seenCallIDs[item.CallID]; exists {
				return nil, &ProtocolError{ReasonCode: "response_function_invalid"}
			}
			seenCallIDs[item.CallID] = struct{}{}
			message.ContentBlocks = append(message.ContentBlocks, &schema.ContentBlock{Type: schema.ContentBlockTypeFunctionToolCall, FunctionToolCall: &schema.FunctionToolCall{CallID: item.CallID, Name: item.Name, Arguments: item.Arguments}, StreamingMeta: &schema.StreamingMeta{Index: index}})
		default:
			return nil, &ProtocolError{ReasonCode: "response_output_invalid"}
		}
	}
	finishReason := "stop"
	if response.Status == "incomplete" {
		finishReason = "length"
	} else if len(seenCallIDs) > 0 {
		finishReason = "tool_calls"
	}
	cacheTTL := ""
	if response.PromptCacheOptions != nil {
		cacheTTL = response.PromptCacheOptions.TTL
	}
	ext := &ResponseMetaExtension{ID: response.ID, Model: response.Model, FinishReason: finishReason, CacheTTL: cacheTTL}
	if usage != nil {
		n := usage.PromptTokenDetails.CacheWriteTokens
		ext.cacheWriteTokens = &n
	}
	message.ResponseMeta = &schema.AgenticResponseMeta{TokenUsage: usage, Extension: ext}
	return message, nil
}

func joinedParts(parts []wirePart, kind string) (string, bool) {
	var text strings.Builder
	for _, part := range parts {
		if part.Type != kind {
			return "", false
		}
		text.WriteString(part.Text)
	}
	return text.String(), true
}

func jsonObject(value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal([]byte(value), &object) == nil && object != nil
}

func defaultArguments(value string) string {
	if value == "" {
		return "{}"
	}
	return value
}

func singleBlockPayload(block *schema.ContentBlock) bool {
	count := 0
	for _, present := range []bool{
		block.Reasoning != nil, block.UserInputText != nil, block.UserInputImage != nil,
		block.UserInputAudio != nil, block.UserInputVideo != nil, block.UserInputFile != nil,
		block.AssistantGenText != nil, block.AssistantGenImage != nil, block.AssistantGenAudio != nil, block.AssistantGenVideo != nil,
		block.FunctionToolCall != nil, block.FunctionToolResult != nil, block.ToolSearchFunctionToolResult != nil,
		block.ServerToolCall != nil, block.ServerToolResult != nil, block.MCPToolCall != nil, block.MCPToolResult != nil,
		block.MCPListToolsResult != nil, block.MCPToolApprovalRequest != nil, block.MCPToolApprovalResponse != nil,
	} {
		if present {
			count++
		}
	}
	return count == 1
}
