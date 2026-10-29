package agentickimi

import (
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestRejectsAmbiguousInputAndToolRestrictions(t *testing.T) {
	for _, input := range [][]*schema.AgenticMessage{
		{{Role: schema.AgenticRoleTypeUser, ContentBlocks: []*schema.ContentBlock{{Type: schema.ContentBlockTypeUserInputText, UserInputText: &schema.UserInputText{Text: "text"}, UserInputImage: &schema.UserInputImage{URL: "unsupported"}}}}},
		{{Role: schema.AgenticRoleTypeUser, ContentBlocks: []*schema.ContentBlock{{Type: schema.ContentBlockTypeFunctionToolResult, FunctionToolResult: &schema.FunctionToolResult{CallID: "call", Content: []*schema.FunctionToolResultContentBlock{{Type: schema.FunctionToolResultContentBlockTypeText, Text: &schema.UserInputText{Text: "text"}, Image: &schema.UserInputImage{URL: "unsupported"}}}}}}}},
		{{Role: "unknown"}},
	} {
		if _, err := convertInput(input); err == nil {
			t.Error("ambiguous or unsupported input was silently reduced")
		}
	}
	_, err := convertTools(&model.Options{AgenticToolChoice: &schema.AgenticToolChoice{Type: schema.ToolChoiceAllowed, Allowed: &schema.AgenticAllowedToolChoice{Tools: []*schema.AllowedTool{{FunctionName: "one"}}}}})
	if err == nil {
		t.Error("allowed tool restriction was silently ignored")
	}
}
