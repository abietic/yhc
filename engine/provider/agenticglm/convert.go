package agenticglm

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

var toolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func buildChatRequest(
	input []*schema.AgenticMessage,
	common *model.Options,
	specific *callOptions,
	stream bool,
) (*chatRequest, error) {
	modelID := ""
	if common.Model != nil {
		modelID = strings.ToLower(strings.TrimSpace(*common.Model))
	}
	if !supportedModel(modelID) {
		return nil, conversionError(-1, -1, "model_unsupported")
	}
	messages, err := convertMessages(input)
	if err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		return nil, conversionError(-1, -1, "messages_empty")
	}
	// The flagship is text-only. Check the effective per-call model, not the
	// constructor model, so model.WithModel cannot bypass this boundary.
	if modelID == ModelGLM53 {
		for messageIndex, message := range messages {
			if parts, ok := message.Content.([]multimodalPart); ok {
				for blockIndex, part := range parts {
					if part.Type != "text" {
						return nil, conversionError(messageIndex, blockIndex, "model_media_unsupported")
					}
				}
			}
		}
	}
	if len(common.DeferredTools) > 0 || common.ToolSearchTool != nil {
		return nil, conversionError(-1, -1, "deferred_tools_unsupported")
	}
	tools, err := convertTools(common.Tools, common.AgenticToolChoice)
	if err != nil {
		return nil, err
	}

	clearThinking := false
	reasoningEffort := ReasoningEffortMax
	requestID := ""
	userID := ""
	var doSample *bool
	toolStream := len(tools) > 0
	if specific != nil {
		if specific.clearThinking != nil {
			clearThinking = *specific.clearThinking
		}
		if specific.reasoningEffort != "" {
			reasoningEffort = specific.reasoningEffort
		}
		if specific.requestID != nil {
			requestID = strings.TrimSpace(*specific.requestID)
		}
		if specific.userID != nil {
			userID = strings.TrimSpace(*specific.userID)
		}
		if specific.doSample != nil {
			doSample = boolPtr(*specific.doSample)
		}
		if specific.toolStream != nil {
			toolStream = *specific.toolStream
		}
	}
	if !validReasoningEffort(reasoningEffort) {
		return nil, conversionError(-1, -1, "reasoning_effort_invalid")
	}
	if err := validateRequestID(requestID); err != nil {
		return nil, err
	}
	if err := validateUserID(userID); err != nil {
		return nil, err
	}
	if common.MaxTokens != nil && (*common.MaxTokens < 1 || *common.MaxTokens > 131072) {
		return nil, conversionError(-1, -1, "max_tokens_out_of_range")
	}
	if common.Temperature != nil && (*common.Temperature < 0 || *common.Temperature > 1) {
		return nil, conversionError(-1, -1, "temperature_out_of_range")
	}
	if common.TopP != nil && (*common.TopP < 0.01 || *common.TopP > 1) {
		return nil, conversionError(-1, -1, "top_p_out_of_range")
	}
	if len(common.Stop) > 4 {
		return nil, conversionError(-1, -1, "stop_count_exceeded")
	}
	req := &chatRequest{
		Model:           modelID,
		Messages:        messages,
		Stream:          stream,
		Thinking:        thinkingConfig{Type: "enabled", ClearThinking: clearThinking},
		ReasoningEffort: reasoningEffort,
		MaxTokens:       cloneInt(common.MaxTokens),
		Temperature:     cloneFloat32(common.Temperature),
		TopP:            cloneFloat32(common.TopP),
		Stop:            append([]string(nil), common.Stop...),
		DoSample:        doSample,
		Tools:           tools,
		RequestID:       requestID,
		UserID:          userID,
	}
	if stream && len(tools) > 0 {
		req.ToolStream = boolPtr(toolStream)
	}
	return req, nil
}

func convertMessages(input []*schema.AgenticMessage) ([]chatMessage, error) {
	messages := make([]chatMessage, 0, len(input))
	images := 0
	files := 0
	for messageIndex, message := range input {
		if message == nil {
			return nil, conversionError(messageIndex, -1, "message_nil")
		}
		switch message.Role {
		case schema.AgenticRoleTypeSystem:
			text, err := textOnlyMessage(messageIndex, message, schema.ContentBlockTypeUserInputText)
			if err != nil {
				return nil, err
			}
			messages = append(messages, chatMessage{Role: "system", Content: text})
		case schema.AgenticRoleTypeUser:
			converted, imageCount, fileCount, err := convertUserMessage(messageIndex, message)
			if err != nil {
				return nil, err
			}
			images += imageCount
			files += fileCount
			messages = append(messages, converted...)
		case schema.AgenticRoleTypeAssistant:
			converted, err := convertAssistantMessage(messageIndex, message)
			if err != nil {
				return nil, err
			}
			messages = append(messages, converted)
		default:
			return nil, conversionError(messageIndex, -1, "role_unsupported")
		}
	}
	if images > maxImagesPerRequest {
		return nil, conversionError(-1, -1, "image_count_exceeded")
	}
	if files > maxFilesPerRequest {
		return nil, conversionError(-1, -1, "file_count_exceeded")
	}
	return messages, nil
}

func textOnlyMessage(messageIndex int, message *schema.AgenticMessage, wanted schema.ContentBlockType) (string, error) {
	var text strings.Builder
	for blockIndex, block := range message.ContentBlocks {
		if block == nil {
			return "", conversionError(messageIndex, blockIndex, "block_nil")
		}
		if block.Type != wanted || block.UserInputText == nil {
			return "", conversionError(messageIndex, blockIndex, "system_block_unsupported")
		}
		text.WriteString(block.UserInputText.Text)
	}
	return text.String(), nil
}

func convertUserMessage(messageIndex int, message *schema.AgenticMessage) ([]chatMessage, int, int, error) {
	result := make([]chatMessage, 0, 2)
	parts := make([]multimodalPart, 0, len(message.ContentBlocks))
	images := 0
	files := 0
	flush := func() {
		if len(parts) == 0 {
			return
		}
		if len(parts) == 1 && parts[0].Type == "text" {
			result = append(result, chatMessage{Role: "user", Content: parts[0].Text})
		} else {
			copied := append([]multimodalPart(nil), parts...)
			result = append(result, chatMessage{Role: "user", Content: copied})
		}
		parts = nil
	}
	for blockIndex, block := range message.ContentBlocks {
		if block == nil {
			return nil, 0, 0, conversionError(messageIndex, blockIndex, "block_nil")
		}
		switch block.Type {
		case schema.ContentBlockTypeUserInputText:
			if block.UserInputText == nil {
				return nil, 0, 0, conversionError(messageIndex, blockIndex, "text_nil")
			}
			parts = append(parts, multimodalPart{Type: "text", Text: block.UserInputText.Text})
		case schema.ContentBlockTypeUserInputImage:
			part, err := convertImage(block.UserInputImage)
			if err != nil {
				return nil, 0, 0, conversionError(messageIndex, blockIndex, err.Error())
			}
			parts = append(parts, part)
			images++
		case schema.ContentBlockTypeUserInputVideo:
			part, err := convertVideo(block.UserInputVideo)
			if err != nil {
				return nil, 0, 0, conversionError(messageIndex, blockIndex, err.Error())
			}
			parts = append(parts, part)
		case schema.ContentBlockTypeUserInputFile:
			part, err := convertFile(block.UserInputFile, block.Extra)
			if err != nil {
				return nil, 0, 0, conversionError(messageIndex, blockIndex, err.Error())
			}
			parts = append(parts, part)
			files++
		case schema.ContentBlockTypeFunctionToolResult:
			flush()
			toolMessage, err := convertToolResult(messageIndex, blockIndex, block.FunctionToolResult)
			if err != nil {
				return nil, 0, 0, err
			}
			result = append(result, toolMessage)
		default:
			return nil, 0, 0, conversionError(messageIndex, blockIndex, "block_type_unsupported")
		}
	}
	flush()
	return result, images, files, nil
}

func convertAssistantMessage(messageIndex int, message *schema.AgenticMessage) (chatMessage, error) {
	result := chatMessage{Role: "assistant"}
	var text strings.Builder
	var reasoning strings.Builder
	for blockIndex, block := range message.ContentBlocks {
		if block == nil {
			return chatMessage{}, conversionError(messageIndex, blockIndex, "block_nil")
		}
		switch block.Type {
		case schema.ContentBlockTypeAssistantGenText:
			if block.AssistantGenText == nil {
				return chatMessage{}, conversionError(messageIndex, blockIndex, "assistant_text_nil")
			}
			text.WriteString(block.AssistantGenText.Text)
		case schema.ContentBlockTypeReasoning:
			if block.Reasoning == nil {
				return chatMessage{}, conversionError(messageIndex, blockIndex, "reasoning_nil")
			}
			reasoning.WriteString(block.Reasoning.Text)
		case schema.ContentBlockTypeFunctionToolCall:
			call := block.FunctionToolCall
			if call == nil || strings.TrimSpace(call.CallID) == "" || !validToolName(call.Name) {
				return chatMessage{}, conversionError(messageIndex, blockIndex, "tool_call_invalid")
			}
			result.ToolCalls = append(result.ToolCalls, chatToolCall{
				ID:   call.CallID,
				Type: "function",
				Function: chatFunctionCall{
					Name:      call.Name,
					Arguments: defaultArguments(call.Arguments),
				},
			})
		default:
			return chatMessage{}, conversionError(messageIndex, blockIndex, "block_type_unsupported")
		}
	}
	if text.Len() > 0 {
		result.Content = text.String()
	}
	result.ReasoningContent = reasoning.String()
	return result, nil
}

func convertToolResult(messageIndex, blockIndex int, result *schema.FunctionToolResult) (chatMessage, error) {
	if result == nil || strings.TrimSpace(result.CallID) == "" {
		return chatMessage{}, conversionError(messageIndex, blockIndex, "tool_result_invalid")
	}
	var content strings.Builder
	for _, block := range result.Content {
		if block == nil || block.Type != schema.FunctionToolResultContentBlockTypeText || block.Text == nil {
			return chatMessage{}, conversionError(messageIndex, blockIndex, "tool_result_multimodal_unsupported")
		}
		content.WriteString(block.Text.Text)
	}
	return chatMessage{Role: "tool", Content: content.String(), ToolCallID: result.CallID}, nil
}

func convertImage(image *schema.UserInputImage) (multimodalPart, error) {
	if image == nil {
		return multimodalPart{}, fmt.Errorf("image_nil")
	}
	if image.Detail != "" {
		return multimodalPart{}, fmt.Errorf("image_detail_unsupported")
	}
	urlValue := strings.TrimSpace(image.URL)
	if (urlValue == "") == (image.Base64Data == "") {
		return multimodalPart{}, fmt.Errorf("image_source_invalid")
	}
	if urlValue != "" {
		if err := validateHTTPURL(urlValue); err != nil {
			return multimodalPart{}, fmt.Errorf("image_url_invalid")
		}
		return multimodalPart{Type: "image_url", ImageURL: &mediaURL{URL: urlValue}}, nil
	}
	mimeType := strings.ToLower(strings.TrimSpace(image.MIMEType))
	if mimeType != "image/jpeg" && mimeType != "image/png" {
		return multimodalPart{}, fmt.Errorf("image_mime_unsupported")
	}
	if err := validateBase64Size(image.Base64Data, maxInlineImageBytes); err != nil {
		return multimodalPart{}, fmt.Errorf("image_base64_invalid")
	}
	return multimodalPart{
		Type:     "image_url",
		ImageURL: &mediaURL{URL: "data:" + mimeType + ";base64," + image.Base64Data},
	}, nil
}

func convertVideo(video *schema.UserInputVideo) (multimodalPart, error) {
	if video == nil || strings.TrimSpace(video.URL) == "" || video.Base64Data != "" {
		return multimodalPart{}, fmt.Errorf("video_source_invalid")
	}
	urlValue := strings.TrimSpace(video.URL)
	if err := validateHTTPURL(urlValue); err != nil {
		return multimodalPart{}, fmt.Errorf("video_url_invalid")
	}
	return multimodalPart{Type: "video_url", VideoURL: &mediaURL{URL: urlValue}}, nil
}

func convertFile(file *schema.UserInputFile, extra map[string]any) (multimodalPart, error) {
	if file == nil {
		return multimodalPart{}, fmt.Errorf("file_nil")
	}
	fileID, _ := extra[fileIDExtraKey].(string)
	fileID = strings.TrimSpace(fileID)
	urlValue := strings.TrimSpace(file.URL)
	hasData := file.Base64Data != ""
	count := 0
	for _, present := range []bool{fileID != "", urlValue != "", hasData} {
		if present {
			count++
		}
	}
	if count != 1 {
		return multimodalPart{}, fmt.Errorf("file_source_invalid")
	}
	converted := &fileContent{Filename: strings.TrimSpace(file.Name)}
	switch {
	case fileID != "":
		if len(fileID) > 256 {
			return multimodalPart{}, fmt.Errorf("file_id_invalid")
		}
		converted.FileID = fileID
	case urlValue != "":
		if err := validateHTTPURL(urlValue); err != nil {
			return multimodalPart{}, fmt.Errorf("file_url_invalid")
		}
		converted.FileURL = urlValue
	case hasData:
		mimeType := strings.TrimSpace(file.MIMEType)
		if mimeType == "" || strings.ContainsAny(mimeType, "\r\n;") {
			return multimodalPart{}, fmt.Errorf("file_mime_invalid")
		}
		if err := validateBase64Size(file.Base64Data, maxInlineFileBytes); err != nil {
			return multimodalPart{}, fmt.Errorf("file_base64_invalid")
		}
		converted.FileData = "data:" + mimeType + ";base64," + file.Base64Data
	}
	return multimodalPart{Type: "file", File: converted}, nil
}

func convertTools(tools []*schema.ToolInfo, choice *schema.AgenticToolChoice) ([]chatTool, error) {
	if len(tools) > maxToolsPerRequest {
		return nil, conversionError(-1, -1, "tool_count_exceeded")
	}
	allowed := map[string]struct{}(nil)
	if choice != nil {
		switch choice.Type {
		case "", schema.ToolChoiceAllowed:
			if choice.Allowed != nil && len(choice.Allowed.Tools) > 0 {
				allowed = make(map[string]struct{}, len(choice.Allowed.Tools))
				for _, entry := range choice.Allowed.Tools {
					if entry == nil || !validToolName(entry.FunctionName) || entry.MCPTool != nil || entry.ServerTool != nil {
						return nil, conversionError(-1, -1, "allowed_tool_invalid")
					}
					allowed[entry.FunctionName] = struct{}{}
				}
			}
		case schema.ToolChoiceForbidden:
			return nil, nil
		case schema.ToolChoiceForced:
			return nil, conversionError(-1, -1, "forced_tool_choice_unsupported")
		default:
			return nil, conversionError(-1, -1, "tool_choice_unsupported")
		}
	}
	converted := make([]chatTool, 0, len(tools))
	seen := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		if tool == nil || !validToolName(tool.Name) {
			return nil, conversionError(-1, -1, "tool_invalid")
		}
		if _, duplicate := seen[tool.Name]; duplicate {
			return nil, conversionError(-1, -1, "tool_name_duplicate")
		}
		seen[tool.Name] = struct{}{}
		if allowed != nil {
			if _, ok := allowed[tool.Name]; !ok {
				continue
			}
		}
		parameters := json.RawMessage(`{"type":"object","properties":{}}`)
		if tool.ParamsOneOf != nil {
			jsonSchema, err := tool.ParamsOneOf.ToJSONSchema()
			if err != nil {
				return nil, conversionError(-1, -1, "tool_parameters_invalid")
			}
			parameters, err = json.Marshal(jsonSchema)
			if err != nil {
				return nil, conversionError(-1, -1, "tool_parameters_invalid")
			}
		}
		converted = append(converted, chatTool{
			Type: "function",
			Function: chatFunction{
				Name:        tool.Name,
				Description: tool.Desc,
				Parameters:  parameters,
			},
		})
	}
	if allowed != nil && len(converted) != len(allowed) {
		return nil, conversionError(-1, -1, "allowed_tool_unknown")
	}
	return converted, nil
}

func responseToAgentic(response *chatResponse) (*schema.AgenticMessage, error) {
	if response == nil || strings.TrimSpace(response.ID) == "" || len(response.Choices) != 1 {
		return nil, &ProtocolError{ReasonCode: "response_envelope_invalid"}
	}
	if err := validateUsage(response.Usage); err != nil {
		return nil, err
	}
	choice := response.Choices[0]
	if choice.Message.Role != "assistant" || choice.FinishReason == nil {
		return nil, &ProtocolError{ReasonCode: "response_choice_invalid"}
	}
	message, err := replyToAgentic(choice.Message, choice.Index)
	if err != nil {
		return nil, err
	}
	message.ResponseMeta = responseMeta(response.ID, response.RequestID, response.Model, *choice.FinishReason, response.Usage)
	return message, nil
}

func replyToAgentic(reply chatReply, choiceIndex int) (*schema.AgenticMessage, error) {
	message := &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant}
	if reply.ReasoningContent != "" {
		message.ContentBlocks = append(message.ContentBlocks, &schema.ContentBlock{
			Type:      schema.ContentBlockTypeReasoning,
			Reasoning: &schema.Reasoning{Text: reply.ReasoningContent},
		})
	}
	if reply.Content != nil && *reply.Content != "" {
		message.ContentBlocks = append(message.ContentBlocks, &schema.ContentBlock{
			Type:             schema.ContentBlockTypeAssistantGenText,
			AssistantGenText: &schema.AssistantGenText{Text: *reply.Content},
		})
	}
	for callIndex, call := range reply.ToolCalls {
		if strings.TrimSpace(call.ID) == "" || call.Type != "function" || !validToolName(call.Function.Name) {
			return nil, &ProtocolError{ReasonCode: "response_tool_call_invalid"}
		}
		index := callIndex
		if call.Index != nil {
			index = *call.Index
		}
		message.ContentBlocks = append(message.ContentBlocks, &schema.ContentBlock{
			Type: schema.ContentBlockTypeFunctionToolCall,
			FunctionToolCall: &schema.FunctionToolCall{
				CallID:    call.ID,
				Name:      call.Function.Name,
				Arguments: defaultArguments(call.Function.Arguments),
			},
			StreamingMeta: &schema.StreamingMeta{Index: index},
		})
	}
	_ = choiceIndex
	return message, nil
}

func responseMeta(responseID, requestID, modelID, finishReason string, usage *responseUsage) *schema.AgenticResponseMeta {
	meta := &schema.AgenticResponseMeta{Extension: &ResponseMetaExtension{
		ResponseID:   responseID,
		RequestID:    requestID,
		FinishReason: finishReason,
		Model:        modelID,
	}}
	if usage != nil {
		meta.TokenUsage = &schema.TokenUsage{
			PromptTokens:     *usage.PromptTokens,
			CompletionTokens: *usage.CompletionTokens,
			TotalTokens:      *usage.TotalTokens,
			PromptTokenDetails: schema.PromptTokenDetails{
				CachedTokens: usage.PromptTokensDetails.CachedTokens,
			},
			CompletionTokensDetails: schema.CompletionTokensDetails{
				ReasoningTokens: usage.CompletionTokensDetails.ReasoningTokens,
			},
		}
	}
	return meta
}

func validateUsage(usage *responseUsage) error {
	if usage == nil {
		return nil // Missing usage remains unknown, never a fabricated zero.
	}
	if usage.PromptTokens == nil || usage.CompletionTokens == nil || usage.TotalTokens == nil {
		return &ProtocolError{ReasonCode: "response_usage_invalid"}
	}
	prompt, completion, total := *usage.PromptTokens, *usage.CompletionTokens, *usage.TotalTokens
	cached, reasoning := usage.PromptTokensDetails.CachedTokens, usage.CompletionTokensDetails.ReasoningTokens
	if prompt < 0 || completion < 0 || total < 0 || cached < 0 || reasoning < 0 ||
		cached > prompt || reasoning > completion || uint64(total) < uint64(prompt)+uint64(completion) {
		return &ProtocolError{ReasonCode: "response_usage_invalid"}
	}
	return nil
}

func validReasoningEffort(effort ReasoningEffort) bool {
	return effort == ReasoningEffortLow || effort == ReasoningEffortHigh || effort == ReasoningEffortMax
}

func validToolName(name string) bool {
	return len(name) >= 1 && len(name) <= 64 && toolNamePattern.MatchString(name)
}

func validateRequestID(value string) error {
	if value != "" && (len(value) < 6 || len(value) > 64) {
		return conversionError(-1, -1, "request_id_invalid")
	}
	return nil
}

func validateUserID(value string) error {
	if value != "" && (len(value) < 6 || len(value) > 128) {
		return conversionError(-1, -1, "user_id_invalid")
	}
	return nil
}

func validateHTTPURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return fmt.Errorf("url_invalid")
	}
	return nil
}

func validateBase64Size(value string, maxBytes int) error {
	decodedLen := base64.StdEncoding.DecodedLen(len(value))
	if decodedLen > maxBytes {
		return fmt.Errorf("base64_too_large")
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(decoded) > maxBytes {
		return fmt.Errorf("base64_invalid")
	}
	return nil
}

func defaultArguments(value string) string {
	if value == "" {
		return "{}"
	}
	return value
}

func boundedText(value string, limit int) string {
	value = strings.ToValidUTF8(value, "�")
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.ValidString(value[:cut]) {
		cut--
	}
	return value[:cut]
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneFloat32(value *float32) *float32 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
