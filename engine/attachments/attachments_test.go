package attachments

import (
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestProcessorCurrentlyReturnsNoGeneratedAttachments(t *testing.T) {
	processor := NewProcessor()
	if processor == nil {
		t.Fatal("NewProcessor returned nil")
		return
	}

	messages := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "hi"},
	}
	toolResults := []*schema.Message{
		{Role: schema.Tool, Content: "tool result"},
	}
	if got := processor.GetAttachments(messages, toolResults); got != nil {
		t.Fatalf("v1 pass-through processor should not generate attachments, got %#v", got)
		return
	}
	if len(messages) != 2 || messages[0].Content != "hello" || len(toolResults) != 1 {
		t.Fatalf("GetAttachments should not mutate inputs")
	}
}

func TestProcessorHandlesNilInputs(t *testing.T) {
	processor := NewProcessor()
	if got := processor.GetAttachments(nil, nil); got != nil {
		t.Fatalf("nil inputs should produce no generated attachments, got %#v", got)
		return
	}
}
