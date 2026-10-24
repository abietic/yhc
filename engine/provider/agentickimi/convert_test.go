package agentickimi

import (
	"math"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestConvertInputPreservesOrderAndToolOutput(t *testing.T) {
	input := []*schema.AgenticMessage{
		{Role: schema.AgenticRoleTypeSystem, ContentBlocks: []*schema.ContentBlock{{Type: schema.ContentBlockTypeUserInputText, UserInputText: &schema.UserInputText{Text: "sys"}}}},
		{Role: schema.AgenticRoleTypeUser, ContentBlocks: []*schema.ContentBlock{{Type: schema.ContentBlockTypeUserInputText, UserInputText: &schema.UserInputText{Text: "ask"}}, {Type: schema.ContentBlockTypeFunctionToolResult, FunctionToolResult: &schema.FunctionToolResult{CallID: "call_1", Content: []*schema.FunctionToolResultContentBlock{{Type: schema.FunctionToolResultContentBlockTypeText, Text: &schema.UserInputText{Text: ""}}}}}}},
		{Role: schema.AgenticRoleTypeAssistant, ResponseMeta: &schema.AgenticResponseMeta{Extension: &ResponseMetaExtension{}}, ContentBlocks: []*schema.ContentBlock{{Type: schema.ContentBlockTypeReasoning, Reasoning: &schema.Reasoning{Text: "think"}}, {Type: schema.ContentBlockTypeAssistantGenText, AssistantGenText: &schema.AssistantGenText{Text: "answer"}}, {Type: schema.ContentBlockTypeFunctionToolCall, FunctionToolCall: &schema.FunctionToolCall{CallID: "call_2", Name: "do_it", Arguments: "{}"}}}},
	}
	got, err := convertInput(input)
	if err != nil || len(got) != 6 {
		t.Fatalf("convertInput() items=%#v err=%v", got, err)
	}
	if got[0].Role != "developer" || got[1].Content[0].Type != "input_text" || got[2].Type != "function_call_output" || got[2].Output == nil || *got[2].Output != "" || got[3].Content[0].Type != "reasoning_text" || got[5].Arguments != "{}" {
		t.Fatalf("unexpected lowering: %#v", got)
	}
}

func TestConvertInputPreservesPlainReasoningAndRejectsSignature(t *testing.T) {
	base := &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{{Type: schema.ContentBlockTypeReasoning, Reasoning: &schema.Reasoning{Text: "private"}}}}
	got, err := convertInput([]*schema.AgenticMessage{base})
	if err != nil || len(got) != 1 || got[0].Content[0].Text != "private" {
		t.Fatalf("plain reasoning: %#v %v", got, err)
	}
	base.ResponseMeta = &schema.AgenticResponseMeta{Extension: &ResponseMetaExtension{}}
	base.ContentBlocks[0].Reasoning.Signature = "opaque"
	if _, err := convertInput([]*schema.AgenticMessage{base}); err == nil {
		t.Fatal("expected signature rejection")
	}
}

func TestConvertToolsBoundsAndChoice(t *testing.T) {
	options := &model.Options{Tools: []*schema.ToolInfo{{Name: "good_name-1"}}, AgenticToolChoice: &schema.AgenticToolChoice{Type: schema.ToolChoiceAllowed}}
	got, err := convertTools(options)
	if err != nil || len(got) != 1 || got[0].Strict || got[0].Parameters == nil {
		t.Fatalf("convertTools()=%#v err=%v", got, err)
	}
	options.AgenticToolChoice.Type = schema.ToolChoiceForbidden
	got, err = convertTools(options)
	if err != nil || got != nil {
		t.Fatalf("forbidden tools=%#v err=%v", got, err)
	}
	options.AgenticToolChoice.Type = schema.ToolChoiceForced
	if _, err = convertTools(options); err == nil {
		t.Fatal("expected forced rejection")
	}
	options.AgenticToolChoice = nil
	options.Tools = []*schema.ToolInfo{{Name: "1invalid"}}
	if _, err = convertTools(options); err == nil {
		t.Fatal("expected invalid name rejection")
	}
}

func TestResponseUsageMissingIsUnknownAndInvalidRejected(t *testing.T) {
	got, err := responseTokenUsage(&wireResponse{})
	if err != nil || got != nil {
		t.Fatalf("missing usage=%#v err=%v", got, err)
	}
	one, two, three, four, five, six := 1, 2, 3, 0, 0, 1
	response := &wireResponse{Usage: &wireUsage{InputTokens: &one, OutputTokens: &two, TotalTokens: &three}}
	response.Usage.InputDetails.CachedTokens, response.Usage.InputDetails.CacheWriteTokens, response.Usage.OutputDetails.ReasoningTokens = &four, &five, &six
	if _, err := responseTokenUsage(response); err != nil {
		t.Fatalf("valid usage: %v", err)
	}
	response.Usage.TotalTokens = &two
	if _, err := responseTokenUsage(response); err == nil {
		t.Fatal("expected invalid total")
	}
	max := math.MaxInt
	response.Usage.InputTokens, response.Usage.OutputTokens, response.Usage.TotalTokens = &max, &one, &max
	if _, err := responseTokenUsage(response); err == nil {
		t.Fatal("expected overflow rejection")
	}
}

func TestResponseMessageValidatesWireContract(t *testing.T) {
	response := &wireResponse{ID: "resp", Object: "response", Model: ModelK3, Status: "completed", Output: []wireItem{{Type: "reasoning", Status: "completed", Summary: []wirePart{{Type: "summary_text", Text: "a"}, {Type: "summary_text", Text: "b"}}}, {Type: "message", Role: "assistant", Status: "completed", Content: []wirePart{{Type: "output_text", Text: "ok"}}}, {Type: "function_call", Status: "completed", CallID: "call", Name: "run", Arguments: "{}"}}}
	message, err := responseMessage(response)
	if err != nil || len(message.ContentBlocks) != 3 {
		t.Fatalf("responseMessage()=%#v err=%v", message, err)
	}
	if message.ContentBlocks[0].Reasoning.Text != "ab" || message.ContentBlocks[2].StreamingMeta.Index != 2 {
		t.Fatalf("unexpected blocks: %#v", message.ContentBlocks)
	}
	response.Output[2].Namespace = "other"
	if _, err := responseMessage(response); err == nil {
		t.Fatal("expected namespace rejection")
	}
	response.Output[2].Namespace = ""
	response.Output[0].EncryptedContent = ptr("cipher")
	if _, err := responseMessage(response); err == nil {
		t.Fatal("expected encrypted content rejection")
	}
}

func TestValidateWireResponseRestrictsStatusAndCache(t *testing.T) {
	response := &wireResponse{ID: "resp", Object: "response", Model: ModelK3, Status: "incomplete", IncompleteDetails: &struct {
		Reason string "json:\"reason\""
	}{Reason: "max_output_tokens"}}
	if err := validateWireResponse(response); err != nil {
		t.Fatalf("valid incomplete: %v", err)
	}
	response.IncompleteDetails.Reason = "content_filter"
	if err := validateWireResponse(response); err == nil {
		t.Fatal("expected incomplete reason rejection")
	}
	response.IncompleteDetails.Reason = "max_output_tokens"
	response.PromptCacheOptions = &cacheOptions{Mode: "implicit", TTL: "1h"}
	if err := validateWireResponse(response); err == nil {
		t.Fatal("expected cache ttl rejection")
	}
}

func ptr(value string) *string { return &value }
