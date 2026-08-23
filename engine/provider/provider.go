package provider

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/cloudwego/eino-ext/components/model/agenticark"
	"github.com/cloudwego/eino-ext/components/model/agenticclaude"
	"github.com/cloudwego/eino-ext/components/model/agenticgemini"
	"github.com/cloudwego/eino-ext/components/model/agenticopenai"
	"github.com/cloudwego/eino-ext/components/model/agenticqwen"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	openaischema "github.com/cloudwego/eino/schema/openai"
	"google.golang.org/genai"

	enginemessages "github.com/abietic/yhc/engine/messages"
	"github.com/abietic/yhc/engine/provider/agenticdeepseek"
)

// Provider enumerates supported model providers.
type Provider string

const (
	ProviderAgenticDeepSeek Provider = "agenticdeepseek"
	ProviderAgenticClaude   Provider = "agenticclaude"
	ProviderAgenticGemini   Provider = "agenticgemini"
	ProviderAgenticOpenAI   Provider = "agenticopenai"
	ProviderAgenticArk      Provider = "agenticark"
	ProviderAgenticQwen     Provider = "agenticqwen"
)

// Config holds model provider configuration.
type Config struct {
	Provider Provider
	Model    string
	APIKey   string
	BaseURL  string
	// ModelAliases maps user-facing names to provider model identifiers.
	ModelAliases map[string]string

	// Claude-specific
	MaxTokens int
}

// NewChatModel creates a provider-aware BaseChatModel from the given explicit
// config. Environment variables and stored credentials fill missing values.
func NewChatModel(ctx context.Context, cfg Config) (model.BaseChatModel, error) {
	runtime, err := NewRuntime(ctx, RuntimeOptions{Resolution: ResolveInput{Explicit: cfg}})
	if err != nil {
		return nil, err
	}
	return runtime.ChatModel, nil
}

func newAgenticModel(ctx context.Context, cfg Config) (model.AgenticModel, error) {
	switch cfg.Provider {
	case ProviderAgenticDeepSeek:
		return newAgenticDeepSeek(ctx, cfg)
	case ProviderAgenticClaude:
		return newAgenticClaude(ctx, cfg)
	case ProviderAgenticGemini:
		return newAgenticGemini(ctx, cfg)
	case ProviderAgenticOpenAI:
		return newAgenticOpenAI(ctx, cfg)
	case ProviderAgenticArk:
		return newAgenticArk(ctx, cfg)
	case ProviderAgenticQwen:
		return newAgenticQwen(ctx, cfg)
	default:
		return nil, fmt.Errorf("unknown provider: %q (supported: agenticdeepseek, agenticclaude, agenticgemini, agenticopenai, agenticark, agenticqwen)", cfg.Provider)
	}
}

func newAgenticDeepSeek(ctx context.Context, cfg Config) (model.AgenticModel, error) {
	if cfg.APIKey == "" {
		cfg.APIKey = os.Getenv("DEEPSEEK_API_KEY")
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("API key required for Agentic DeepSeek. Set PROV_API_KEY or DEEPSEEK_API_KEY env var")
	}
	if cfg.Model == "" {
		cfg.Model = "deepseek-v4-flash"
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = os.Getenv("DEEPSEEK_BASE_URL")
	}
	return agenticdeepseek.New(ctx, &agenticdeepseek.Config{
		BaseURL: cfg.BaseURL,
		APIKey:  cfg.APIKey,
		Model:   cfg.Model,
	})
}

func newAgenticClaude(ctx context.Context, cfg Config) (model.AgenticModel, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("API key required for Agentic Claude. Set PROV_API_KEY env var")
	}
	if cfg.Model == "" {
		cfg.Model = "claude-sonnet-4-6"
	}
	maxTokens := cfg.MaxTokens
	if maxTokens == 0 {
		maxTokens = 8192
	}
	return agenticclaude.New(ctx, &agenticclaude.Config{
		HTTPClient: claudeHTTPClientForBaseURL(cfg.BaseURL),
		BaseURL:    cfg.BaseURL,
		APIKey:     cfg.APIKey,
		Model:      cfg.Model,
		MaxTokens:  maxTokens,
	})
}

func newAgenticGemini(ctx context.Context, cfg Config) (model.AgenticModel, error) {
	if cfg.APIKey == "" {
		cfg.APIKey = os.Getenv("GOOGLE_API_KEY")
		if cfg.APIKey == "" {
			cfg.APIKey = os.Getenv("GEMINI_API_KEY")
		}
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("API key required for Agentic Gemini. Set PROV_API_KEY, GOOGLE_API_KEY, or GEMINI_API_KEY env var")
		}
	}
	if cfg.Model == "" {
		cfg.Model = "gemini-2.5-flash"
	}
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:      cfg.APIKey,
		HTTPOptions: genai.HTTPOptions{BaseURL: cfg.BaseURL},
	})
	if err != nil {
		return nil, fmt.Errorf("agenticgemini: create client: %w", err)
	}
	return agenticgemini.New(ctx, &agenticgemini.Config{
		Client: client,
		Model:  cfg.Model,
	})
}

func newAgenticOpenAI(ctx context.Context, cfg Config) (model.AgenticModel, error) {
	if cfg.APIKey == "" {
		cfg.APIKey = os.Getenv("OPENAI_API_KEY")
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("API key required for Agentic OpenAI. Set PROV_API_KEY or OPENAI_API_KEY env var")
	}
	if cfg.Model == "" {
		cfg.Model = "gpt-4o"
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = os.Getenv("OPENAI_BASE_URL")
	}
	// The canonical engine attempt coordinator owns retries and provider-call
	// budgets. Disable the leaf SDK's default retries so one admitted call maps
	// to exactly one transport request before retry or failover is decided.
	maxRetries := 0
	return agenticopenai.NewResponsesModel(ctx, &agenticopenai.ResponsesConfig{
		BaseURL:    cfg.BaseURL,
		APIKey:     cfg.APIKey,
		MaxRetries: &maxRetries,
		Model:      cfg.Model,
	})
}

func newAgenticArk(ctx context.Context, cfg Config) (model.AgenticModel, error) {
	if cfg.APIKey == "" {
		cfg.APIKey = os.Getenv("ARK_API_KEY")
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("API key required for Agentic Ark. Set PROV_API_KEY or ARK_API_KEY env var")
	}
	if cfg.Model == "" {
		cfg.Model = "doubao-1.5-pro-32k"
	}
	return agenticark.New(ctx, &agenticark.Config{
		BaseURL: cfg.BaseURL,
		APIKey:  cfg.APIKey,
		Model:   cfg.Model,
	})
}

func newAgenticQwen(ctx context.Context, cfg Config) (model.AgenticModel, error) {
	if cfg.APIKey == "" {
		cfg.APIKey = os.Getenv("DASHSCOPE_API_KEY")
	}
	if cfg.APIKey == "" {
		cfg.APIKey = os.Getenv("QWEN_API_KEY")
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("API key required for Agentic Qwen. Set PROV_API_KEY, DASHSCOPE_API_KEY, or QWEN_API_KEY env var")
	}
	if cfg.Model == "" {
		cfg.Model = "qwen-max"
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = os.Getenv("QWEN_BASE_URL")
	}
	return agenticqwen.New(ctx, &agenticqwen.Config{
		BaseURL: cfg.BaseURL,
		APIKey:  cfg.APIKey,
		Model:   cfg.Model,
	})
}

// agenticChatModel wraps model.AgenticModel as model.BaseChatModel
// by converting between *schema.Message and *schema.AgenticMessage.
type agenticChatModel struct {
	inner model.AgenticModel
	tools []*schema.ToolInfo
}

func wrapAgenticModel(inner model.AgenticModel) *agenticChatModel {
	return &agenticChatModel{inner: inner}
}

func (a *agenticChatModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	// Agentic models pass tools at request time via model.WithTools option,
	// not via model-level binding. Store here for use in Generate/Stream.
	clone := *a
	clone.tools = append([]*schema.ToolInfo(nil), tools...)
	return &clone, nil
}

func (a *agenticChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return a.generateTrusted(ctx, input, nil, opts...)
}

func (a *agenticChatModel) generateTrusted(
	ctx context.Context,
	input []*schema.Message,
	proof *routeProof,
	opts ...model.Option,
) (*schema.Message, error) {
	agenticInput, err := a.prepareAgenticInput(input, proof)
	if err != nil {
		return nil, err
	}
	opts = normalizeAgenticOptions(opts)
	if len(a.tools) > 0 {
		opts = append(opts, model.WithTools(a.tools))
	}
	out, err := a.inner.Generate(ctx, agenticInput, opts...)
	if err != nil {
		return nil, err
	}
	return agenticToMessage(out), nil
}

func (a *agenticChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return a.streamTrusted(ctx, input, nil, opts...)
}

func (a *agenticChatModel) streamTrusted(
	ctx context.Context,
	input []*schema.Message,
	proof *routeProof,
	opts ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	agenticInput, err := a.prepareAgenticInput(input, proof)
	if err != nil {
		return nil, err
	}
	opts = normalizeAgenticOptions(opts)
	if len(a.tools) > 0 {
		opts = append(opts, model.WithTools(a.tools))
	}
	sr, err := a.inner.Stream(ctx, agenticInput, opts...)
	if err != nil {
		return nil, err
	}
	acc := newToolCallAccumulator()
	return schema.StreamReaderWithConvert(sr, func(am *schema.AgenticMessage) (*schema.Message, error) {
		if am == nil {
			return nil, nil
		}
		converted := acc.convertChunk(am)
		if converted.Content == "" && converted.ReasoningContent == "" && len(converted.AssistantGenMultiContent) == 0 && len(converted.ToolCalls) == 0 && converted.ResponseMeta == nil {
			return nil, nil
		}
		return converted, nil
	}), nil
}

func (a *agenticChatModel) prepareAgenticInput(
	input []*schema.Message,
	proof *routeProof,
) ([]*schema.AgenticMessage, error) {
	if proof == nil || proof.client != a || proof.publication == 0 {
		return messagesToAgentic(input)
	}
	return messagesToAgenticWithAllowed(input, proof.allowed)
}

func normalizeAgenticOptions(opts []model.Option) []model.Option {
	common := model.GetCommonOptions(nil, opts...)
	if common.AgenticToolChoice != nil || common.ToolChoice == nil {
		return opts
	}

	agenticToolChoice := &schema.AgenticToolChoice{Type: *common.ToolChoice}
	if len(common.AllowedToolNames) > 0 {
		tools := make([]*schema.AllowedTool, 0, len(common.AllowedToolNames))
		for _, name := range common.AllowedToolNames {
			if name == "" {
				continue
			}
			tools = append(tools, &schema.AllowedTool{FunctionName: name})
		}
		if len(tools) > 0 {
			switch *common.ToolChoice {
			case schema.ToolChoiceAllowed:
				agenticToolChoice.Allowed = &schema.AgenticAllowedToolChoice{Tools: tools}
			case schema.ToolChoiceForced:
				agenticToolChoice.Forced = &schema.AgenticForcedToolChoice{Tools: tools}
			}
		}
	}

	normalized := make([]model.Option, 0, len(opts)+1)
	normalized = append(normalized, model.WithAgenticToolChoice(agenticToolChoice))
	for _, opt := range opts {
		probe := model.GetCommonOptions(nil, opt)
		if probe.ToolChoice != nil || len(probe.AllowedToolNames) > 0 {
			continue
		}
		normalized = append(normalized, opt)
	}
	return normalized
}

// AgenticInputConversionError identifies an invalid classic user-input part
// without exposing its potentially sensitive payload.
type AgenticInputConversionError struct {
	MessageIndex int
	PartIndex    int
	Role         schema.RoleType
	PartType     schema.ChatMessagePartType
	ReasonCode   string
}

func (e *AgenticInputConversionError) Error() string {
	return fmt.Sprintf("agentic input conversion: message=%d part=%d reason=%s", e.MessageIndex, e.PartIndex, e.ReasonCode)
}

func messagesToAgentic(msgs []*schema.Message) ([]*schema.AgenticMessage, error) {
	return messagesToAgenticWithAllowed(msgs, nil)
}

func messagesToAgenticWithAllowed(
	msgs []*schema.Message,
	allowed map[int]struct{},
) ([]*schema.AgenticMessage, error) {
	out := make([]*schema.AgenticMessage, 0, len(msgs))
	for messageIndex, m := range msgs {
		if m == nil {
			return nil, &AgenticInputConversionError{MessageIndex: messageIndex, PartIndex: -1, ReasonCode: "nil_message"}
		}
		switch m.Role {
		case schema.System:
			out = append(out, schema.SystemAgenticMessage(m.Content))
		case schema.Assistant:
			// Skip empty assistant messages (no text, no tool calls, no structured output).
			if m.Content == "" && m.ReasoningContent == "" && len(m.AssistantGenMultiContent) == 0 && len(m.ToolCalls) == 0 {
				continue
			}

			msg := &schema.AgenticMessage{
				Role: schema.AgenticRoleTypeAssistant,
			}
			// Message-level Extra belongs to the response adapter that produced
			// the canonical message. Restored or caller-supplied metadata must
			// never grant private continuation reuse. Only exact dispatch-route
			// verification may synthesize the adapter's typed trust marker.
			if m.ResponseMeta != nil {
				msg.ResponseMeta = &schema.AgenticResponseMeta{TokenUsage: m.ResponseMeta.Usage}
			}
			if _, ok := allowed[messageIndex]; ok {
				if msg.ResponseMeta == nil {
					msg.ResponseMeta = &schema.AgenticResponseMeta{}
				}
				msg.ResponseMeta.OpenAIExtension = &openaischema.ResponseMetaExtension{}
			}

			outputParts := m.AssistantGenMultiContent
			if merged, err := enginemessages.ConcatAssistantOutputParts(outputParts); err == nil {
				outputParts = merged
			}
			multiContentHasReasoning := outputPartsContainType(outputParts, schema.ChatMessagePartTypeReasoning)
			multiContentHasText := outputPartsContainType(outputParts, schema.ChatMessagePartTypeText)

			if !multiContentHasReasoning && m.ReasoningContent != "" {
				msg.ContentBlocks = append(msg.ContentBlocks, newReasoningContentBlock(&schema.Reasoning{Text: m.ReasoningContent}, nil, nil))
			}

			for _, part := range outputParts {
				block, ok := outputPartToContentBlock(part)
				if !ok {
					continue
				}
				msg.ContentBlocks = append(msg.ContentBlocks, block)
			}

			if !multiContentHasText && m.Content != "" {
				msg.ContentBlocks = append(msg.ContentBlocks, newAssistantTextContentBlock(m.Content, nil, nil))
			}

			for _, tc := range m.ToolCalls {
				if tc.ID == "" || tc.Function.Name == "" {
					continue
				}
				block := &schema.ContentBlock{
					Type: schema.ContentBlockTypeFunctionToolCall,
					FunctionToolCall: &schema.FunctionToolCall{
						CallID:    tc.ID,
						Name:      tc.Function.Name,
						Arguments: defaultArgs(tc.Function.Arguments),
					},
					Extra: tc.Extra,
				}
				if tc.Index != nil {
					block.StreamingMeta = &schema.StreamingMeta{Index: *tc.Index}
				}
				msg.ContentBlocks = append(msg.ContentBlocks, block)
			}

			out = append(out, msg)
		case schema.Tool:
			resultContent := m.Content
			if resultContent == "" {
				resultContent = "ok"
			}
			out = append(out, &schema.AgenticMessage{
				Role: schema.AgenticRoleTypeUser,
				ContentBlocks: []*schema.ContentBlock{
					{
						Type: schema.ContentBlockTypeFunctionToolResult,
						FunctionToolResult: &schema.FunctionToolResult{
							CallID: m.ToolCallID,
							Name:   m.ToolName,
							Content: []*schema.FunctionToolResultContentBlock{
								{
									Type: schema.FunctionToolResultContentBlockTypeText,
									Text: &schema.UserInputText{Text: resultContent},
								},
							},
						},
					},
				},
			})
		case schema.User:
			if len(m.UserInputMultiContent) == 0 {
				out = append(out, schema.UserAgenticMessage(m.Content))
				continue
			}
			blocks := make([]*schema.ContentBlock, 0, len(m.UserInputMultiContent))
			for partIndex, part := range m.UserInputMultiContent {
				block, err := userInputPartToAgentic(messageIndex, partIndex, m.Role, part)
				if err != nil {
					return nil, err
				}
				blocks = append(blocks, block)
			}
			out = append(out, &schema.AgenticMessage{Role: schema.AgenticRoleTypeUser, ContentBlocks: blocks})
		default:
			out = append(out, schema.UserAgenticMessage(m.Content))
		}
	}
	return out, nil
}

func userInputPartToAgentic(messageIndex, partIndex int, role schema.RoleType, part schema.MessageInputPart) (*schema.ContentBlock, error) {
	errFor := func(code string) (*schema.ContentBlock, error) {
		return nil, &AgenticInputConversionError{MessageIndex: messageIndex, PartIndex: partIndex, Role: role, PartType: part.Type, ReasonCode: code}
	}
	if part.Type == schema.ChatMessagePartTypeReasoning || part.Type == schema.ChatMessagePartTypeToolSearchResult {
		return errFor("unsupported_part_type")
	}
	validMedia := func(common schema.MessagePartCommon) (bool, string) {
		url, data := common.URL != nil, common.Base64Data != nil
		if url && data {
			return false, "media_source_ambiguous"
		}
		if !url && !data {
			return false, "media_source_missing"
		}
		if url && *common.URL == "" || data && *common.Base64Data == "" {
			return false, "media_source_missing"
		}
		if data && strings.TrimSpace(common.MIMEType) == "" {
			return false, "media_mime_type_missing"
		}
		return true, ""
	}
	noMediaPayload := func() bool {
		return part.Image == nil && part.Audio == nil && part.Video == nil && part.File == nil && part.ToolSearchResult == nil
	}
	switch part.Type {
	case schema.ChatMessagePartTypeText:
		if !noMediaPayload() {
			return errFor("mismatched_part_payload")
		}
		return &schema.ContentBlock{Type: schema.ContentBlockTypeUserInputText, UserInputText: &schema.UserInputText{Text: part.Text}}, nil
	case schema.ChatMessagePartTypeImageURL:
		if part.Image == nil {
			return errFor("nil_part_payload")
		}
		if part.Text != "" || part.Audio != nil || part.Video != nil || part.File != nil || part.ToolSearchResult != nil {
			return errFor("mismatched_part_payload")
		}
		if ok, code := validMedia(part.Image.MessagePartCommon); !ok {
			return errFor(code)
		}
		b := &schema.UserInputImage{MIMEType: part.Image.MIMEType, Detail: part.Image.Detail}
		if part.Image.URL != nil {
			b.URL = *part.Image.URL
		} else {
			b.Base64Data = *part.Image.Base64Data
		}
		return &schema.ContentBlock{Type: schema.ContentBlockTypeUserInputImage, UserInputImage: b}, nil
	case schema.ChatMessagePartTypeAudioURL:
		if part.Audio == nil {
			return errFor("nil_part_payload")
		}
		if part.Text != "" || part.Image != nil || part.Video != nil || part.File != nil || part.ToolSearchResult != nil {
			return errFor("mismatched_part_payload")
		}
		if ok, code := validMedia(part.Audio.MessagePartCommon); !ok {
			return errFor(code)
		}
		b := &schema.UserInputAudio{MIMEType: part.Audio.MIMEType}
		if part.Audio.URL != nil {
			b.URL = *part.Audio.URL
		} else {
			b.Base64Data = *part.Audio.Base64Data
		}
		return &schema.ContentBlock{Type: schema.ContentBlockTypeUserInputAudio, UserInputAudio: b}, nil
	case schema.ChatMessagePartTypeVideoURL:
		if part.Video == nil {
			return errFor("nil_part_payload")
		}
		if part.Text != "" || part.Image != nil || part.Audio != nil || part.File != nil || part.ToolSearchResult != nil {
			return errFor("mismatched_part_payload")
		}
		if ok, code := validMedia(part.Video.MessagePartCommon); !ok {
			return errFor(code)
		}
		b := &schema.UserInputVideo{MIMEType: part.Video.MIMEType}
		if part.Video.URL != nil {
			b.URL = *part.Video.URL
		} else {
			b.Base64Data = *part.Video.Base64Data
		}
		return &schema.ContentBlock{Type: schema.ContentBlockTypeUserInputVideo, UserInputVideo: b}, nil
	case schema.ChatMessagePartTypeFileURL:
		if part.File == nil {
			return errFor("nil_part_payload")
		}
		if part.Text != "" || part.Image != nil || part.Audio != nil || part.Video != nil || part.ToolSearchResult != nil {
			return errFor("mismatched_part_payload")
		}
		if ok, code := validMedia(part.File.MessagePartCommon); !ok {
			return errFor(code)
		}
		b := &schema.UserInputFile{MIMEType: part.File.MIMEType, Name: part.File.Name}
		if part.File.URL != nil {
			b.URL = *part.File.URL
		} else {
			b.Base64Data = *part.File.Base64Data
		}
		return &schema.ContentBlock{Type: schema.ContentBlockTypeUserInputFile, UserInputFile: b}, nil
	default:
		return errFor("unknown_part_type")
	}
}

func agenticToMessage(am *schema.AgenticMessage) *schema.Message {
	msg := &schema.Message{
		Role:  schema.Assistant,
		Extra: am.Extra,
	}
	if am.Role == schema.AgenticRoleTypeUser {
		msg.Role = schema.User
	}
	msg.ResponseMeta = legacyResponseMeta(am)

	for _, b := range am.ContentBlocks {
		switch b.Type {
		case schema.ContentBlockTypeAssistantGenText:
			if b.AssistantGenText == nil {
				continue
			}
			msg.Content += b.AssistantGenText.Text
			msg.AssistantGenMultiContent = append(msg.AssistantGenMultiContent, schema.MessageOutputPart{
				Type:          schema.ChatMessagePartTypeText,
				Text:          b.AssistantGenText.Text,
				Extra:         b.Extra,
				StreamingMeta: blockStreamingMetaToOutputPart(b.StreamingMeta),
			})
		case schema.ContentBlockTypeReasoning:
			if b.Reasoning == nil {
				continue
			}
			msg.ReasoningContent += b.Reasoning.Text
			msg.AssistantGenMultiContent = append(msg.AssistantGenMultiContent, schema.MessageOutputPart{
				Type: schema.ChatMessagePartTypeReasoning,
				Reasoning: &schema.MessageOutputReasoning{
					Text:      b.Reasoning.Text,
					Signature: b.Reasoning.Signature,
				},
				Extra:         b.Extra,
				StreamingMeta: blockStreamingMetaToOutputPart(b.StreamingMeta),
			})
		case schema.ContentBlockTypeFunctionToolCall:
			if b.FunctionToolCall == nil || b.FunctionToolCall.Name == "" {
				continue
			}
			id := b.FunctionToolCall.CallID
			if id == "" {
				id = b.FunctionToolCall.Name
			}
			tc := schema.ToolCall{
				ID:   id,
				Type: "function",
				Function: schema.FunctionCall{
					Name:      b.FunctionToolCall.Name,
					Arguments: defaultArgs(b.FunctionToolCall.Arguments),
				},
				Extra: b.Extra,
			}
			if b.StreamingMeta != nil {
				idx := b.StreamingMeta.Index
				tc.Index = &idx
			}
			msg.ToolCalls = append(msg.ToolCalls, tc)
		case schema.ContentBlockTypeServerToolCall:
			if b.ServerToolCall == nil || b.ServerToolCall.Name == "" {
				continue
			}
			tc := schema.ToolCall{
				ID:   b.ServerToolCall.CallID,
				Type: "function",
				Function: schema.FunctionCall{
					Name:      b.ServerToolCall.Name,
					Arguments: defaultArgs("{}"),
				},
				Extra: b.Extra,
			}
			if b.StreamingMeta != nil {
				idx := b.StreamingMeta.Index
				tc.Index = &idx
			}
			msg.ToolCalls = append(msg.ToolCalls, tc)
		}
	}

	return msg
}

func outputPartsContainType(parts []schema.MessageOutputPart, typ schema.ChatMessagePartType) bool {
	for _, part := range parts {
		if part.Type == typ {
			return true
		}
	}
	return false
}

func outputPartToContentBlock(part schema.MessageOutputPart) (*schema.ContentBlock, bool) {
	switch part.Type {
	case schema.ChatMessagePartTypeText:
		return newAssistantTextContentBlock(part.Text, part.Extra, part.StreamingMeta), true
	case schema.ChatMessagePartTypeReasoning:
		if part.Reasoning == nil {
			return nil, false
		}
		return newReasoningContentBlock(&schema.Reasoning{
			Text:      part.Reasoning.Text,
			Signature: part.Reasoning.Signature,
		}, part.Extra, part.StreamingMeta), true
	default:
		return nil, false
	}
}

func newAssistantTextContentBlock(text string, extra map[string]any, meta *schema.MessageStreamingMeta) *schema.ContentBlock {
	block := &schema.ContentBlock{
		Type:             schema.ContentBlockTypeAssistantGenText,
		AssistantGenText: &schema.AssistantGenText{Text: text},
		Extra:            extra,
	}
	if meta != nil {
		block.StreamingMeta = &schema.StreamingMeta{Index: meta.Index}
	}
	return block
}

func newReasoningContentBlock(reasoning *schema.Reasoning, extra map[string]any, meta *schema.MessageStreamingMeta) *schema.ContentBlock {
	block := &schema.ContentBlock{
		Type:      schema.ContentBlockTypeReasoning,
		Reasoning: reasoning,
		Extra:     extra,
	}
	if meta != nil {
		block.StreamingMeta = &schema.StreamingMeta{Index: meta.Index}
	}
	return block
}

func blockStreamingMetaToOutputPart(meta *schema.StreamingMeta) *schema.MessageStreamingMeta {
	if meta == nil {
		return nil
	}
	return &schema.MessageStreamingMeta{Index: meta.Index}
}

// toolCallAccumulator accumulates streaming tool call argument deltas across
// chunks. Eino-ext agentic SDKs stream tool call arguments as incremental
// deltas: the first chunk carries Name/CallID with empty Arguments, subsequent
// chunks carry only Arguments deltas (no Name, no CallID). This accumulator
// reassembles the full state per streaming index.
type toolCallAccumulator struct {
	calls map[int]*accumulatedToolCall
}

type accumulatedToolCall struct {
	CallID    string
	Name      string
	Arguments string
}

func newToolCallAccumulator() *toolCallAccumulator {
	return &toolCallAccumulator{calls: make(map[int]*accumulatedToolCall)}
}

// convertChunk converts an AgenticMessage streaming chunk to a Message.
// For text and reasoning blocks it uses the same logic as agenticToMessage.
// For FunctionToolCall blocks it accumulates per-index state and emits the
// full accumulated tool call. For ServerToolCall blocks it passes through.
func (a *toolCallAccumulator) convertChunk(am *schema.AgenticMessage) *schema.Message {
	msg := &schema.Message{
		Role:  schema.Assistant,
		Extra: am.Extra,
	}
	if am.Role == schema.AgenticRoleTypeUser {
		msg.Role = schema.User
	}
	msg.ResponseMeta = legacyResponseMeta(am)

	for _, b := range am.ContentBlocks {
		switch b.Type {
		case schema.ContentBlockTypeAssistantGenText:
			if b.AssistantGenText == nil {
				continue
			}
			text := b.AssistantGenText.Text
			msg.Content += text
			msg.AssistantGenMultiContent = append(msg.AssistantGenMultiContent, schema.MessageOutputPart{
				Type:          schema.ChatMessagePartTypeText,
				Text:          text,
				Extra:         b.Extra,
				StreamingMeta: blockStreamingMetaToOutputPart(b.StreamingMeta),
			})
		case schema.ContentBlockTypeReasoning:
			if b.Reasoning == nil {
				continue
			}
			msg.ReasoningContent += b.Reasoning.Text
			msg.AssistantGenMultiContent = append(msg.AssistantGenMultiContent, schema.MessageOutputPart{
				Type: schema.ChatMessagePartTypeReasoning,
				Reasoning: &schema.MessageOutputReasoning{
					Text:      b.Reasoning.Text,
					Signature: b.Reasoning.Signature,
				},
				Extra:         b.Extra,
				StreamingMeta: blockStreamingMetaToOutputPart(b.StreamingMeta),
			})
		case schema.ContentBlockTypeFunctionToolCall:
			if b.FunctionToolCall == nil {
				continue
			}
			// Determine the streaming index for accumulation.
			idx := 0
			if b.StreamingMeta != nil {
				idx = b.StreamingMeta.Index
			}
			acc, ok := a.calls[idx]
			if !ok {
				acc = &accumulatedToolCall{}
				a.calls[idx] = acc
			}
			// Store Name and CallID when non-empty (arrives in first chunk).
			if b.FunctionToolCall.Name != "" {
				acc.Name = b.FunctionToolCall.Name
			}
			if b.FunctionToolCall.CallID != "" {
				acc.CallID = b.FunctionToolCall.CallID
			}
			// Append arguments delta.
			acc.Arguments += b.FunctionToolCall.Arguments

			// Only emit a tool call when Name is known.
			if acc.Name == "" {
				continue
			}
			id := acc.CallID
			if id == "" {
				id = acc.Name
			}
			tc := schema.ToolCall{
				ID:   id,
				Type: "function",
				Function: schema.FunctionCall{
					Name:      acc.Name,
					Arguments: defaultArgs(acc.Arguments),
				},
				Extra: b.Extra,
			}
			if b.StreamingMeta != nil {
				sidx := b.StreamingMeta.Index
				tc.Index = &sidx
			}
			msg.ToolCalls = append(msg.ToolCalls, tc)
		case schema.ContentBlockTypeServerToolCall:
			if b.ServerToolCall == nil || b.ServerToolCall.Name == "" {
				continue
			}
			tc := schema.ToolCall{
				ID:   b.ServerToolCall.CallID,
				Type: "function",
				Function: schema.FunctionCall{
					Name:      b.ServerToolCall.Name,
					Arguments: defaultArgs("{}"),
				},
				Extra: b.Extra,
			}
			if b.StreamingMeta != nil {
				sidx := b.StreamingMeta.Index
				tc.Index = &sidx
			}
			msg.ToolCalls = append(msg.ToolCalls, tc)
		}
	}

	return msg
}

func legacyResponseMeta(am *schema.AgenticMessage) *schema.ResponseMeta {
	if am == nil {
		return nil
	}
	var usage *schema.TokenUsage
	if am.ResponseMeta != nil {
		usage = am.ResponseMeta.TokenUsage
	}
	finishReason := agenticFinishReason(am)
	if usage == nil && finishReason == "" {
		return nil
	}
	return &schema.ResponseMeta{
		Usage:        usage,
		FinishReason: finishReason,
	}
}

func agenticFinishReason(am *schema.AgenticMessage) string {
	if am == nil {
		return ""
	}
	if meta := am.ResponseMeta; meta != nil {
		if meta.ClaudeExtension != nil && meta.ClaudeExtension.StopReason != "" {
			return meta.ClaudeExtension.StopReason
		}
		if meta.GeminiExtension != nil && meta.GeminiExtension.FinishReason != "" {
			return meta.GeminiExtension.FinishReason
		}
		if meta.OpenAIExtension != nil {
			incompleteReason := ""
			if meta.OpenAIExtension.IncompleteDetails != nil {
				incompleteReason = meta.OpenAIExtension.IncompleteDetails.Reason
			}
			if reason := finishReasonFromStatus(string(meta.OpenAIExtension.Status), incompleteReason, false); reason != "" {
				return reason
			}
		}
		if ext, ok := meta.Extension.(*agenticark.ResponseMetaExtension); ok && ext != nil {
			incompleteReason := ""
			if ext.IncompleteDetails != nil {
				incompleteReason = ext.IncompleteDetails.Reason
			}
			if reason := finishReasonFromStatus(string(ext.Status), incompleteReason, ext.StreamingError != nil); reason != "" {
				return reason
			}
		}
		if ext, ok := meta.Extension.(*agenticdeepseek.ResponseMetaExtension); ok && ext != nil {
			return ext.FinishReason
		}
	}
	for _, value := range am.Extra {
		switch ext := value.(type) {
		case *agenticdeepseek.ResponseMetaExtension:
			if ext != nil && ext.FinishReason != "" {
				return ext.FinishReason
			}
		case *agenticqwen.ResponseMetaExtension:
			if ext != nil && ext.FinishReason != "" {
				return ext.FinishReason
			}
		}
	}
	return ""
}

func finishReasonFromStatus(status, incompleteReason string, streamingError bool) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed":
		return "stop"
	case "incomplete":
		if reason := strings.TrimSpace(incompleteReason); reason != "" {
			return reason
		}
		return "incomplete"
	case "failed", "cancelled":
		return strings.ToLower(strings.TrimSpace(status))
	default:
		if streamingError {
			return "stream_error"
		}
		return ""
	}
}

// defaultArgs ensures the arguments field is never empty (API rejects
// missing "arguments" — FunctionToolCall uses omitempty on the JSON tag).
func defaultArgs(s string) string {
	if s == "" {
		return "{}"
	}
	return s
}
