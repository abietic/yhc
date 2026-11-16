package agenticdeepseek

import (
	"encoding/json"
	"io"
	"sort"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// SafetyLevel selects the provider's output review, independently of local
// tool permissions. High review withholds streamed content until review ends.
type SafetyLevel string

const (
	SafetyLevelLow  SafetyLevel = "low"
	SafetyLevelHigh SafetyLevel = "high"
)

// ApplicationConfig enables the optional DeepSeek Flash chatbot extensions.
// Web tools execute remotely and incur charges beyond model tokens. They are
// deliberately disabled by default and unavailable to token-bounded streams.
type ApplicationConfig struct {
	WebSearch     bool
	ReadURL       bool
	SafetyCheck   SafetyLevel
	GenerateTitle bool
}

// WithApplicationConfig replaces the application configuration for one call.
// An empty configuration disables all inherited application extensions.
func WithApplicationConfig(config ApplicationConfig) model.Option {
	return model.WrapImplSpecificOptFn(func(opts *callOptions) {
		copy := config
		opts.application = &copy
	})
}

func cloneApplication(config *ApplicationConfig) *ApplicationConfig {
	if config == nil {
		return nil
	}
	copy := *config
	return &copy
}

type (
	safetyConfig struct {
		Level SafetyLevel `json:"level"`
	}
	titleConfig struct {
		Enabled bool `json:"enabled"`
	}
)

func applyApplication(req *responseRequest, config *ApplicationConfig) error {
	if config == nil || *config == (ApplicationConfig{}) {
		return nil
	}
	if !supportsImageInput(req.Model) {
		return conversionError(-1, -1, "application_model_unsupported")
	}
	if config.SafetyCheck != "" && config.SafetyCheck != SafetyLevelLow && config.SafetyCheck != SafetyLevelHigh {
		return conversionError(-1, -1, "safety_level_invalid")
	}
	if req.Text != nil && req.Text.Format.Type != TextFormatText {
		return conversionError(-1, -1, "application_text_format_unsupported")
	}
	if req.TopLogProbs != nil {
		return conversionError(-1, -1, "application_top_logprobs_unsupported")
	}
	if len(req.Input) > 0 {
		last := req.Input[len(req.Input)-1]
		if last.Type == "reasoning" || (last.Type == "message" && last.Role == "assistant") {
			return conversionError(-1, -1, "application_continuation_unsupported")
		}
	}
	// The application route does not honor a forced local function: required
	// becomes auto on the service. Reject rather than silently relaxing intent.
	if req.ToolChoice != nil && req.ToolChoice != "auto" && req.ToolChoice != "none" {
		return conversionError(-1, -1, "application_tool_choice_unsupported")
	}
	for _, tool := range req.Tools {
		if tool.Name == "deepseek_web_search" || tool.Name == "deepseek_read_url" {
			return conversionError(-1, -1, "application_tool_name_reserved")
		}
	}
	if config.WebSearch {
		req.Tools = append(req.Tools, functionTool{Type: "deepseek_web_search"})
	}
	if config.ReadURL {
		req.Tools = append(req.Tools, functionTool{Type: "deepseek_read_url"})
	}
	if len(req.Tools) > maxToolsPerRequest {
		return conversionError(-1, -1, "tool_count_exceeded")
	}
	if config.SafetyCheck != "" {
		req.SafetyCheck = &safetyConfig{Level: config.SafetyCheck}
	}
	if config.GenerateTitle {
		req.Title = &titleConfig{Enabled: true}
	}
	return nil
}

// URLCitation retains the provider's Unicode-code-point offsets. OutputIndex
// and ContentIndex identify the output_text block to which the offsets apply.
type URLCitation struct {
	Type         string `json:"type"`
	URL          string `json:"url"`
	Title        string `json:"title"`
	StartIndex   int    `json:"start_index"`
	EndIndex     int    `json:"end_index"`
	OutputIndex  int    `json:"output_index,omitempty"`
	ContentIndex int    `json:"content_index,omitempty"`
}

// ServerToolUsage contains billed operation counts, not a monetary receipt.
// It must not be folded into model token usage or a token-only cost bound.
type ServerToolUsage struct {
	ToolSearch      int `json:"tool_search"`
	ToolFetch       int `json:"tool_fetch"`
	ToolImageSearch int `json:"tool_image_search"`
}

const (
	applicationExtraKey = "_agenticdeepseek_application"
	outputIndexExtraKey = "_agenticdeepseek_output_index"
)

func originalOutputIndex(block *schema.ContentBlock) (int, bool) {
	if value, ok := block.Extra[outputIndexExtraKey]; ok {
		raw, err := json.Marshal(value)
		var index int
		if err == nil && json.Unmarshal(raw, &index) == nil && index >= 0 {
			return index, true
		}
	}
	if block.StreamingMeta != nil {
		return block.StreamingMeta.Index, true
	}
	return 0, false
}

// RestoreOutputParts restores provider item boundaries after JSON persistence,
// where Eino intentionally omits MessageStreamingMeta. The detached slice
// leaves canonical content and caller-owned parts unchanged.
func RestoreOutputParts(parts []schema.MessageOutputPart) []schema.MessageOutputPart {
	restored := append([]schema.MessageOutputPart(nil), parts...)
	for i, part := range parts {
		if index, ok := originalOutputIndex(&schema.ContentBlock{Extra: part.Extra}); ok {
			restored[i].StreamingMeta = &schema.MessageStreamingMeta{Index: index}
		}
	}
	return restored
}

// Classic messages keep local function calls separate from text. Restore the
// provider item order on a detached slice when every block retains its index.
// Legacy blocks with incomplete indexes retain the caller's order.
func restoreReplayBlockOrder(blocks []*schema.ContentBlock) []*schema.ContentBlock {
	type indexedBlock struct {
		block *schema.ContentBlock
		index int
	}
	indexed := make([]indexedBlock, len(blocks))
	for i, block := range blocks {
		if block == nil {
			return blocks
		}
		index, ok := originalOutputIndex(block)
		if !ok || index < 0 {
			return blocks
		}
		indexed[i] = indexedBlock{block: block, index: index}
	}
	sort.SliceStable(indexed, func(i, j int) bool { return indexed[i].index < indexed[j].index })
	ordered := make([]*schema.ContentBlock, len(indexed))
	for i, part := range indexed {
		ordered[i] = part.block
	}
	return ordered
}

// ReplayExtra copies only DeepSeek's public application metadata through the
// classic Eino bridge. It conveys no private continuation or route authority.
func ReplayExtra(extra map[string]any) map[string]any {
	value, ok := extra[applicationExtraKey]
	if !ok {
		return nil
	}
	return map[string]any{applicationExtraKey: value}
}

func applicationMetadata(response *responseObject) *ResponseMetaExtension {
	ext := &ResponseMetaExtension{Title: response.Title}
	for outputIndex, item := range response.Output {
		if item.Type == "web_search_call" {
			ext.WebToolCalls = append(ext.WebToolCalls, append(json.RawMessage(nil), item.raw...))
			ext.WebToolCallIndexes = append(ext.WebToolCallIndexes, outputIndex)
		}
		for contentIndex, content := range item.Content {
			for _, citation := range content.Annotations {
				if citation.Type != "url_citation" {
					continue
				}
				citation.OutputIndex = outputIndex
				citation.ContentIndex = contentIndex
				ext.Citations = append(ext.Citations, citation)
			}
		}
	}
	if response.Usage != nil && response.Usage.ServerToolUse != nil &&
		response.Usage.ServerToolUse.ToolSearch >= 0 && response.Usage.ServerToolUse.ToolFetch >= 0 && response.Usage.ServerToolUse.ToolImageSearch >= 0 {
		usage := *response.Usage.ServerToolUse
		ext.ServerToolUsage = &usage
	}
	return ext
}

func applicationExtra(response *responseObject) map[string]any {
	ext := applicationMetadata(response)
	if ext.Title == "" && len(ext.WebToolCalls) == 0 && len(ext.Citations) == 0 && ext.ServerToolUsage == nil {
		return nil
	}
	return map[string]any{applicationExtraKey: ext}
}

func replayWebToolCalls(extra map[string]any) ([]inputItem, error) {
	value, ok := extra[applicationExtraKey]
	if !ok {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > maxResponseBytes {
		return nil, conversionError(-1, -1, "application_replay_invalid")
	}
	var metadata struct {
		WebToolCalls       []json.RawMessage `json:"web_tool_calls"`
		WebToolCallIndexes []int             `json:"web_tool_call_indexes"`
	}
	if json.Unmarshal(raw, &metadata) != nil {
		return nil, conversionError(-1, -1, "application_replay_invalid")
	}
	items := make([]inputItem, 0, len(metadata.WebToolCalls))
	if len(metadata.WebToolCallIndexes) != len(metadata.WebToolCalls) {
		return nil, conversionError(-1, -1, "application_replay_invalid")
	}
	for index, raw := range metadata.WebToolCalls {
		var item outputItem
		if json.Unmarshal(raw, &item) != nil || item.Type != "web_search_call" || item.ID == "" {
			return nil, conversionError(-1, -1, "application_replay_invalid")
		}
		position := metadata.WebToolCallIndexes[index]
		if position < 0 || (index > 0 && position <= metadata.WebToolCallIndexes[index-1]) {
			return nil, conversionError(-1, -1, "application_replay_invalid")
		}
		items = append(items, inputItem{raw: append(json.RawMessage(nil), raw...), replayIndex: position})
	}
	return items, nil
}

// Review can refuse an answer after its last text delta. Buffer high-review
// streams until the terminal verdict; failed/truncated streams release nothing.
func parseReviewedStream(reader io.Reader, maxEventBytes int, review bool, parse func(io.Reader, int, func(*schema.AgenticMessage) bool) error, send func(*schema.AgenticMessage) bool) error {
	if !review {
		return parse(reader, maxEventBytes, send)
	}
	var pending []*schema.AgenticMessage
	var pendingBytes int
	var bufferErr error
	err := parse(reader, maxEventBytes, func(message *schema.AgenticMessage) bool {
		if message.ResponseMeta == nil {
			raw, err := json.Marshal(message)
			if err != nil || len(raw) > maxResponseBytes-pendingBytes {
				bufferErr = &ProtocolError{ReasonCode: "safety_review_buffer_exceeded"}
				return true
			}
			pendingBytes += len(raw)
			pending = append(pending, message)
			return false
		}
		ext, _ := message.ResponseMeta.Extension.(*ResponseMetaExtension)
		if ext != nil && ext.IncompleteReason != "content_filter" {
			for _, chunk := range pending {
				if send(chunk) {
					return true
				}
			}
		}
		pending = nil
		return send(message)
	})
	if bufferErr != nil {
		return bufferErr
	}
	return err
}
