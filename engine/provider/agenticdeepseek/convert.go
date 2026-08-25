package agenticdeepseek

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

var (
	toolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	userIDPattern   = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
)

type responseRequest struct {
	Model           string           `json:"model"`
	Input           []inputItem      `json:"input"`
	Stream          bool             `json:"stream,omitempty"`
	MaxOutputTokens *int             `json:"max_output_tokens,omitempty"`
	Temperature     *float32         `json:"temperature,omitempty"`
	TopP            *float32         `json:"top_p,omitempty"`
	TopLogProbs     *int             `json:"top_logprobs,omitempty"`
	Reasoning       *reasoningConfig `json:"reasoning,omitempty"`
	Text            *textConfig      `json:"text,omitempty"`
	Tools           []functionTool   `json:"tools,omitempty"`
	ToolChoice      any              `json:"tool_choice,omitempty"`
	User            string           `json:"user,omitempty"`
}

type reasoningConfig struct {
	Effort ReasoningEffort `json:"effort"`
}

type textConfig struct {
	Format textFormat `json:"format"`
}

type textFormat struct {
	Type   TextFormatType  `json:"type"`
	Name   string          `json:"name,omitempty"`
	Schema json.RawMessage `json:"schema,omitempty"`
}

type inputItem struct {
	Type      string        `json:"type,omitempty"`
	Role      string        `json:"role,omitempty"`
	Content   []contentPart `json:"content,omitempty"`
	CallID    string        `json:"call_id,omitempty"`
	Name      string        `json:"name,omitempty"`
	Arguments string        `json:"arguments,omitempty"`
	Output    []contentPart `json:"output,omitempty"`
}

type contentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	FileID   string `json:"file_id,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

type functionTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type responseObject struct {
	ID                string             `json:"id"`
	Object            string             `json:"object"`
	CreatedAt         int64              `json:"created_at"`
	Status            ResponseStatus     `json:"status"`
	Error             *responseError     `json:"error"`
	IncompleteDetails *incompleteDetails `json:"incomplete_details"`
	Model             string             `json:"model"`
	Output            []outputItem       `json:"output"`
	Usage             *responseUsage     `json:"usage"`
}

type responseError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type incompleteDetails struct {
	Reason string `json:"reason"`
}

type outputItem struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Status    string          `json:"status"`
	Role      string          `json:"role"`
	Content   []outputContent `json:"content"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"`
	Action    json.RawMessage `json:"action"`
}

type outputContent struct {
	Type     string            `json:"type"`
	Text     string            `json:"text"`
	LogProbs []responseLogProb `json:"logprobs"`
}

type responseLogProb struct {
	Token       string               `json:"token"`
	LogProb     float64              `json:"logprob"`
	Bytes       []int64              `json:"bytes"`
	TopLogProbs []responseTopLogProb `json:"top_logprobs"`
}

type responseTopLogProb struct {
	Token   string  `json:"token"`
	LogProb float64 `json:"logprob"`
	Bytes   []int64 `json:"bytes"`
}

type responseUsage struct {
	InputTokens        int `json:"input_tokens"`
	InputTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokens        int `json:"output_tokens"`
	OutputTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
	TotalTokens int `json:"total_tokens"`
}

func buildResponseRequest(
	input []*schema.AgenticMessage,
	common *model.Options,
	specific *callOptions,
	stream bool,
) (*responseRequest, error) {
	modelID := ""
	if common.Model != nil {
		modelID = strings.TrimSpace(*common.Model)
	}
	if modelID == "" {
		return nil, conversionError(-1, -1, "model_missing")
	}
	items, err := messagesToInputItems(input, modelID)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, conversionError(-1, -1, "input_empty")
	}
	if len(common.Stop) > 0 {
		return nil, conversionError(-1, -1, "stop_unsupported")
	}
	if len(common.DeferredTools) > 0 || common.ToolSearchTool != nil {
		return nil, conversionError(-1, -1, "deferred_tools_unsupported")
	}

	tools, toolChoice, err := convertTools(common.Tools, common.AgenticToolChoice)
	if err != nil {
		return nil, err
	}
	req := &responseRequest{
		Model:           modelID,
		Input:           items,
		Stream:          stream,
		MaxOutputTokens: common.MaxTokens,
		Temperature:     common.Temperature,
		TopP:            common.TopP,
		Tools:           tools,
		ToolChoice:      toolChoice,
	}
	if specific != nil {
		if specific.reasoningEffort != "" {
			if !validReasoningEffort(specific.reasoningEffort) {
				return nil, conversionError(-1, -1, "reasoning_effort_invalid")
			}
			req.Reasoning = &reasoningConfig{Effort: specific.reasoningEffort}
		}
		if specific.textFormat != nil {
			converted, convertErr := convertTextFormat(specific.textFormat)
			if convertErr != nil {
				return nil, convertErr
			}
			req.Text = &textConfig{Format: converted}
		}
		if specific.userID != nil {
			if err := validateUserID(*specific.userID); err != nil {
				return nil, err
			}
			req.User = *specific.userID
		}
		if specific.topLogProbs != nil {
			if *specific.topLogProbs < 0 || *specific.topLogProbs > 20 {
				return nil, conversionError(-1, -1, "top_logprobs_out_of_range")
			}
			req.TopLogProbs = specific.topLogProbs
		}
	}
	if req.MaxOutputTokens != nil && *req.MaxOutputTokens <= 0 {
		return nil, conversionError(-1, -1, "max_output_tokens_invalid")
	}
	if req.Temperature != nil && (*req.Temperature < 0 || *req.Temperature > 2) {
		return nil, conversionError(-1, -1, "temperature_out_of_range")
	}
	if req.TopP != nil && (*req.TopP < 0 || *req.TopP > 1) {
		return nil, conversionError(-1, -1, "top_p_out_of_range")
	}
	return req, nil
}

func messagesToInputItems(messages []*schema.AgenticMessage, modelID string) ([]inputItem, error) {
	items := make([]inputItem, 0, len(messages))
	for messageIndex, message := range messages {
		if message == nil {
			return nil, conversionError(messageIndex, -1, "message_nil")
		}
		switch message.Role {
		case schema.AgenticRoleTypeSystem:
			converted, err := convertSystemOrUserMessage(messageIndex, message, "system", modelID)
			if err != nil {
				return nil, err
			}
			items = append(items, converted...)
		case schema.AgenticRoleTypeUser:
			converted, err := convertSystemOrUserMessage(messageIndex, message, "user", modelID)
			if err != nil {
				return nil, err
			}
			items = append(items, converted...)
		case schema.AgenticRoleTypeAssistant:
			converted, err := convertAssistantMessage(messageIndex, message)
			if err != nil {
				return nil, err
			}
			items = append(items, converted...)
		default:
			return nil, conversionError(messageIndex, -1, "role_unsupported")
		}
	}
	if imageCount(items) > maxImagesPerRequest {
		return nil, conversionError(-1, -1, "image_count_exceeded")
	}
	return items, nil
}

func convertSystemOrUserMessage(
	messageIndex int,
	message *schema.AgenticMessage,
	role string,
	modelID string,
) ([]inputItem, error) {
	items := make([]inputItem, 0, 1)
	content := make([]contentPart, 0, len(message.ContentBlocks))
	flush := func() {
		if len(content) == 0 {
			return
		}
		items = append(items, inputItem{Type: "message", Role: role, Content: content})
		content = nil
	}
	for blockIndex, block := range message.ContentBlocks {
		if block == nil {
			return nil, conversionError(messageIndex, blockIndex, "block_nil")
		}
		switch block.Type {
		case schema.ContentBlockTypeUserInputText:
			if block.UserInputText == nil {
				return nil, conversionError(messageIndex, blockIndex, "text_nil")
			}
			content = append(content, contentPart{Type: "input_text", Text: block.UserInputText.Text})
		case schema.ContentBlockTypeUserInputImage:
			if role != "user" {
				return nil, conversionError(messageIndex, blockIndex, "image_role_unsupported")
			}
			if modelID != VisionModel {
				return nil, conversionError(messageIndex, blockIndex, "image_model_unsupported")
			}
			part, err := convertImage(block.UserInputImage, block.Extra)
			if err != nil {
				return nil, conversionError(messageIndex, blockIndex, err.Error())
			}
			content = append(content, part)
		case schema.ContentBlockTypeFunctionToolResult:
			if role != "user" {
				return nil, conversionError(messageIndex, blockIndex, "tool_result_role_unsupported")
			}
			flush()
			item, err := convertFunctionToolResult(messageIndex, blockIndex, block.FunctionToolResult, modelID)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		default:
			return nil, conversionError(messageIndex, blockIndex, "block_type_unsupported")
		}
	}
	flush()
	return items, nil
}

func convertAssistantMessage(messageIndex int, message *schema.AgenticMessage) ([]inputItem, error) {
	items := make([]inputItem, 0, len(message.ContentBlocks))
	text := make([]contentPart, 0, 1)
	flushText := func() {
		if len(text) == 0 {
			return
		}
		items = append(items, inputItem{Type: "message", Role: "assistant", Content: text})
		text = nil
	}
	for blockIndex, block := range message.ContentBlocks {
		if block == nil {
			return nil, conversionError(messageIndex, blockIndex, "block_nil")
		}
		switch block.Type {
		case schema.ContentBlockTypeAssistantGenText:
			if block.AssistantGenText == nil {
				return nil, conversionError(messageIndex, blockIndex, "assistant_text_nil")
			}
			text = append(text, contentPart{Type: "output_text", Text: block.AssistantGenText.Text})
		case schema.ContentBlockTypeReasoning:
			if block.Reasoning == nil {
				return nil, conversionError(messageIndex, blockIndex, "reasoning_nil")
			}
			flushText()
			items = append(items, inputItem{
				Type:    "reasoning",
				Content: []contentPart{{Type: "reasoning_text", Text: block.Reasoning.Text}},
			})
		case schema.ContentBlockTypeFunctionToolCall:
			if block.FunctionToolCall == nil || strings.TrimSpace(block.FunctionToolCall.CallID) == "" || strings.TrimSpace(block.FunctionToolCall.Name) == "" {
				return nil, conversionError(messageIndex, blockIndex, "tool_call_invalid")
			}
			flushText()
			items = append(items, inputItem{
				Type:      "function_call",
				CallID:    block.FunctionToolCall.CallID,
				Name:      block.FunctionToolCall.Name,
				Arguments: defaultArguments(block.FunctionToolCall.Arguments),
			})
		default:
			return nil, conversionError(messageIndex, blockIndex, "block_type_unsupported")
		}
	}
	flushText()
	return items, nil
}

func convertFunctionToolResult(
	messageIndex int,
	blockIndex int,
	result *schema.FunctionToolResult,
	modelID string,
) (inputItem, error) {
	if result == nil || strings.TrimSpace(result.CallID) == "" {
		return inputItem{}, conversionError(messageIndex, blockIndex, "tool_result_invalid")
	}
	output := make([]contentPart, 0, len(result.Content))
	for resultIndex, block := range result.Content {
		if block == nil {
			return inputItem{}, conversionError(messageIndex, blockIndex, "tool_result_block_nil")
		}
		switch block.Type {
		case schema.FunctionToolResultContentBlockTypeText:
			if block.Text == nil {
				return inputItem{}, conversionError(messageIndex, blockIndex, "tool_result_text_nil")
			}
			output = append(output, contentPart{Type: "input_text", Text: block.Text.Text})
		case schema.FunctionToolResultContentBlockTypeImage:
			if modelID != VisionModel {
				return inputItem{}, conversionError(messageIndex, blockIndex, "image_model_unsupported")
			}
			part, err := convertImage(block.Image, block.Extra)
			if err != nil {
				return inputItem{}, conversionError(messageIndex, blockIndex, fmt.Sprintf("tool_result_%d_%s", resultIndex, err.Error()))
			}
			output = append(output, part)
		default:
			return inputItem{}, conversionError(messageIndex, blockIndex, "tool_result_type_unsupported")
		}
	}
	if len(output) == 0 {
		output = append(output, contentPart{Type: "input_text", Text: ""})
	}
	return inputItem{Type: "function_call_output", CallID: result.CallID, Output: output}, nil
}

func convertImage(image *schema.UserInputImage, extra map[string]any) (contentPart, error) {
	if image == nil {
		return contentPart{}, fmt.Errorf("image_nil")
	}
	fileID, _ := extra[imageFileIDExtraKey].(string)
	fileID = strings.TrimSpace(fileID)
	hasFileID := fileID != ""
	hasURL := strings.TrimSpace(image.URL) != ""
	hasBase64 := image.Base64Data != ""
	count := 0
	for _, present := range []bool{hasFileID, hasURL, hasBase64} {
		if present {
			count++
		}
	}
	if count == 0 {
		return contentPart{}, fmt.Errorf("image_source_missing")
	}
	if count != 1 {
		return contentPart{}, fmt.Errorf("image_source_ambiguous")
	}
	detail := string(image.Detail)
	if !validImageDetail(detail) {
		return contentPart{}, fmt.Errorf("image_detail_invalid")
	}
	part := contentPart{Type: "input_image", Detail: detail}
	if hasFileID {
		if !strings.HasPrefix(fileID, "file-api-") {
			return contentPart{}, fmt.Errorf("image_file_id_invalid")
		}
		part.FileID = fileID
		part.Detail = ""
		return part, nil
	}
	if hasURL {
		raw := strings.TrimSpace(image.URL)
		if strings.HasPrefix(raw, "data:") {
			if reason := validateImageDataURL(raw); reason != "" {
				return contentPart{}, fmt.Errorf("%s", reason)
			}
		} else {
			if len(raw) > 8192 {
				return contentPart{}, fmt.Errorf("image_url_too_long")
			}
			parsed, err := url.Parse(raw)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
				return contentPart{}, fmt.Errorf("image_url_invalid")
			}
		}
		part.ImageURL = raw
		return part, nil
	}
	mime := strings.ToLower(strings.TrimSpace(image.MIMEType))
	if !supportedImageMIME(mime) {
		return contentPart{}, fmt.Errorf("image_mime_unsupported")
	}
	if reason := validateImageBase64(image.Base64Data); reason != "" {
		return contentPart{}, fmt.Errorf("%s", reason)
	}
	part.ImageURL = "data:" + mime + ";base64," + image.Base64Data
	return part, nil
}

func convertTools(tools []*schema.ToolInfo, choice *schema.AgenticToolChoice) ([]functionTool, any, error) {
	selected := tools
	choiceValue := any(nil)
	if choice != nil {
		var allowed []*schema.AllowedTool
		switch choice.Type {
		case schema.ToolChoiceForbidden:
			choiceValue = "none"
		case schema.ToolChoiceAllowed:
			choiceValue = "auto"
			if choice.Allowed != nil {
				allowed = choice.Allowed.Tools
			}
		case schema.ToolChoiceForced:
			choiceValue = "required"
			if choice.Forced != nil {
				allowed = choice.Forced.Tools
			}
		default:
			return nil, nil, conversionError(-1, -1, "tool_choice_invalid")
		}
		if len(allowed) > 0 {
			names := make(map[string]struct{}, len(allowed))
			for _, tool := range allowed {
				if tool == nil || tool.FunctionName == "" || tool.MCPTool != nil || tool.ServerTool != nil {
					return nil, nil, conversionError(-1, -1, "tool_choice_type_unsupported")
				}
				names[tool.FunctionName] = struct{}{}
			}
			selected = make([]*schema.ToolInfo, 0, len(names))
			for _, tool := range tools {
				if tool != nil {
					if _, ok := names[tool.Name]; ok {
						selected = append(selected, tool)
						delete(names, tool.Name)
					}
				}
			}
			if len(names) != 0 {
				return nil, nil, conversionError(-1, -1, "tool_choice_unknown_name")
			}
			if choice.Type == schema.ToolChoiceForced && len(selected) == 1 {
				choiceValue = map[string]any{"type": "function", "name": selected[0].Name}
			}
		}
	}
	if len(selected) == 0 {
		if choice != nil && choice.Type == schema.ToolChoiceForced {
			return nil, nil, conversionError(-1, -1, "forced_tools_empty")
		}
		return nil, choiceValue, nil
	}
	if len(selected) > maxToolsPerRequest {
		return nil, nil, conversionError(-1, -1, "tool_count_exceeded")
	}

	converted := make([]functionTool, 0, len(selected))
	seen := make(map[string]struct{}, len(selected))
	for _, tool := range selected {
		if tool == nil || tool.Name == "" || len(tool.Name) > 128 || !toolNamePattern.MatchString(tool.Name) {
			return nil, nil, conversionError(-1, -1, "tool_name_invalid")
		}
		if _, ok := seen[tool.Name]; ok {
			return nil, nil, conversionError(-1, -1, "tool_name_duplicate")
		}
		seen[tool.Name] = struct{}{}
		params, err := tool.ToJSONSchema()
		if err != nil {
			return nil, nil, conversionError(-1, -1, "tool_schema_invalid")
		}
		var raw json.RawMessage
		if params != nil {
			raw, err = json.Marshal(params)
			if err != nil {
				return nil, nil, conversionError(-1, -1, "tool_schema_invalid")
			}
		}
		converted = append(converted, functionTool{
			Type:        "function",
			Name:        tool.Name,
			Description: tool.Desc,
			Parameters:  raw,
		})
	}
	return converted, choiceValue, nil
}

func convertTextFormat(format *TextFormat) (textFormat, error) {
	if format == nil {
		return textFormat{}, conversionError(-1, -1, "text_format_nil")
	}
	out := textFormat{Type: format.Type, Name: strings.TrimSpace(format.Name)}
	switch format.Type {
	case TextFormatText, TextFormatJSONObject:
		if out.Name != "" || len(format.Schema) != 0 {
			return textFormat{}, conversionError(-1, -1, "text_format_fields_invalid")
		}
	case TextFormatJSONSchema:
		if out.Name == "" || !json.Valid(format.Schema) {
			return textFormat{}, conversionError(-1, -1, "text_format_schema_invalid")
		}
		out.Schema = append(json.RawMessage(nil), format.Schema...)
	default:
		return textFormat{}, conversionError(-1, -1, "text_format_type_invalid")
	}
	return out, nil
}

func responseToAgentic(response *responseObject) (*schema.AgenticMessage, error) {
	if response == nil || response.Object != "response" || response.ID == "" {
		return nil, &ProtocolError{ReasonCode: "response_object_invalid"}
	}
	if response.Status == ResponseStatusFailed {
		return nil, apiErrorFromResponse(response)
	}
	if response.Status != ResponseStatusCompleted && response.Status != ResponseStatusIncomplete {
		return nil, &ProtocolError{ReasonCode: "response_status_not_terminal"}
	}
	message := &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant}
	for _, item := range response.Output {
		switch item.Type {
		case "reasoning":
			for _, content := range item.Content {
				if content.Type != "reasoning_text" {
					continue
				}
				message.ContentBlocks = append(message.ContentBlocks, &schema.ContentBlock{
					Type:      schema.ContentBlockTypeReasoning,
					Reasoning: &schema.Reasoning{Text: content.Text},
				})
			}
		case "message":
			for _, content := range item.Content {
				if content.Type != "output_text" {
					continue
				}
				message.ContentBlocks = append(message.ContentBlocks, &schema.ContentBlock{
					Type:             schema.ContentBlockTypeAssistantGenText,
					AssistantGenText: &schema.AssistantGenText{Text: content.Text},
				})
			}
		case "function_call":
			if item.CallID == "" || item.Name == "" {
				return nil, &ProtocolError{ReasonCode: "function_call_invalid"}
			}
			message.ContentBlocks = append(message.ContentBlocks, &schema.ContentBlock{
				Type: schema.ContentBlockTypeFunctionToolCall,
				FunctionToolCall: &schema.FunctionToolCall{
					CallID:    item.CallID,
					Name:      item.Name,
					Arguments: defaultArguments(item.Arguments),
				},
			})
		case "web_search_call":
			// The server already executed this tool. It is deliberately not
			// projected as a client-side function call.
		default:
			continue
		}
	}
	message.ResponseMeta = responseMeta(response)
	return message, nil
}

func responseMeta(response *responseObject) *schema.AgenticResponseMeta {
	logProbs := responseLogProbs(response)
	meta := &schema.AgenticResponseMeta{
		Extension: &ResponseMetaExtension{
			ResponseID:       response.ID,
			Status:           response.Status,
			FinishReason:     finishReason(response),
			IncompleteReason: incompleteReason(response),
			ErrorCode:        responseErrorCode(response),
			Model:            response.Model,
			LogProbs:         logProbs,
		},
	}
	if response.Usage != nil {
		meta.TokenUsage = &schema.TokenUsage{
			PromptTokens:     response.Usage.InputTokens,
			CompletionTokens: response.Usage.OutputTokens,
			TotalTokens:      response.Usage.TotalTokens,
		}
		meta.TokenUsage.PromptTokenDetails.CachedTokens = response.Usage.InputTokensDetails.CachedTokens
		meta.TokenUsage.CompletionTokensDetails.ReasoningTokens = response.Usage.OutputTokensDetails.ReasoningTokens
	}
	return meta
}

func responseLogProbs(response *responseObject) *schema.LogProbs {
	if response == nil {
		return nil
	}
	var converted []schema.LogProb
	for _, item := range response.Output {
		if item.Type != "message" {
			continue
		}
		for _, content := range item.Content {
			if content.Type != "output_text" {
				continue
			}
			for _, logProb := range content.LogProbs {
				top := make([]schema.TopLogProb, 0, len(logProb.TopLogProbs))
				for _, candidate := range logProb.TopLogProbs {
					top = append(top, schema.TopLogProb{
						Token:   candidate.Token,
						LogProb: candidate.LogProb,
						Bytes:   append([]int64(nil), candidate.Bytes...),
					})
				}
				converted = append(converted, schema.LogProb{
					Token:       logProb.Token,
					LogProb:     logProb.LogProb,
					Bytes:       append([]int64(nil), logProb.Bytes...),
					TopLogProbs: top,
				})
			}
		}
	}
	if len(converted) == 0 {
		return nil
	}
	return &schema.LogProbs{Content: converted}
}

func finishReason(response *responseObject) string {
	for _, item := range response.Output {
		if item.Type == "function_call" {
			return "tool_calls"
		}
	}
	switch response.Status {
	case ResponseStatusCompleted:
		return "stop"
	case ResponseStatusIncomplete:
		switch incompleteReason(response) {
		case "max_output_tokens":
			return "length"
		case "content_filter":
			return "content_filter"
		default:
			return "incomplete"
		}
	case ResponseStatusFailed:
		return "failed"
	default:
		return ""
	}
}

func incompleteReason(response *responseObject) string {
	if response != nil && response.IncompleteDetails != nil {
		return response.IncompleteDetails.Reason
	}
	return ""
}

func responseErrorCode(response *responseObject) string {
	if response != nil && response.Error != nil {
		return response.Error.Code
	}
	return ""
}

func apiErrorFromResponse(response *responseObject) *APIError {
	err := &APIError{}
	if response != nil && response.Error != nil {
		err.Code = boundedText(response.Error.Code, 128)
		err.Message = boundedText(response.Error.Message, 1024)
	}
	return err
}

func validateUserID(value string) error {
	if value == "" {
		return nil
	}
	if len(value) > 512 || !utf8.ValidString(value) || !userIDPattern.MatchString(value) {
		return conversionError(-1, -1, "user_id_invalid")
	}
	return nil
}

func validReasoningEffort(value ReasoningEffort) bool {
	switch value {
	case ReasoningEffortNone,
		ReasoningEffortMinimal,
		ReasoningEffortLow,
		ReasoningEffortMedium,
		ReasoningEffortHigh,
		ReasoningEffortXHigh,
		ReasoningEffortMax:
		return true
	default:
		return false
	}
}

func validImageDetail(value string) bool {
	switch value {
	case "", "low", "high", "original", "auto":
		return true
	default:
		return false
	}
}

func supportedImageMIME(value string) bool {
	switch value {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

func validateImageDataURL(value string) string {
	prefix, data, ok := strings.Cut(value, ",")
	if !ok || !strings.HasPrefix(prefix, "data:image/") || !strings.HasSuffix(prefix, ";base64") || data == "" {
		return "image_data_url_invalid"
	}
	mime := strings.TrimSuffix(strings.TrimPrefix(prefix, "data:"), ";base64")
	if !supportedImageMIME(strings.ToLower(mime)) {
		return "image_data_url_invalid"
	}
	if reason := validateImageBase64(data); reason != "" {
		if reason == "image_base64_too_large" {
			return "image_data_url_too_large"
		}
		return "image_data_url_invalid"
	}
	return ""
}

func validateImageBase64(value string) string {
	decoder := base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(value))
	read, err := io.CopyN(io.Discard, decoder, int64(maxSingleImageBytes)+1)
	if err == nil || read > maxSingleImageBytes {
		return "image_base64_too_large"
	}
	if !errors.Is(err, io.EOF) {
		return "image_base64_invalid"
	}
	return ""
}

func imageCount(items []inputItem) int {
	count := 0
	for _, item := range items {
		for _, part := range item.Content {
			if part.Type == "input_image" {
				count++
			}
		}
		for _, part := range item.Output {
			if part.Type == "input_image" {
				count++
			}
		}
	}
	return count
}

func defaultArguments(value string) string {
	if value == "" {
		return "{}"
	}
	return value
}

func conversionError(messageIndex, blockIndex int, reason string) *ConversionError {
	return &ConversionError{MessageIndex: messageIndex, BlockIndex: blockIndex, ReasonCode: reason}
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
