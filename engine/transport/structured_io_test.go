package transport

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/abietic/yhc/engine"
	"github.com/cloudwego/eino/schema"
)

func decodeOutput(t *testing.T, buf *bytes.Buffer) OutputMessage {
	t.Helper()
	var out OutputMessage
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &out); err != nil {
		t.Fatalf("decode output: %v; raw=%q", err, buf.String())
		return OutputMessage{}
	}
	buf.Reset()
	return out
}

func TestStructuredIOReadWriteRoundTrip(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"control","content":"interrupt","meta":{"reason":"user"}}` + "\n")
	var output bytes.Buffer
	sio := NewStructuredIO(input, &output)

	msg, err := sio.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage failed: %v", err)
		return
	}
	if !msg.IsControl() || msg.Content != "interrupt" || msg.Meta["reason"] != "user" {
		t.Fatalf("unexpected input message: %#v", msg)
	}
	if _, err := sio.ReadMessage(); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after single input message, got %v", err)
	}

	if err := sio.WriteToolUse("Read", "toolu_1", map[string]any{"file_path": "README.md"}); err != nil {
		t.Fatalf("WriteToolUse failed: %v", err)
		return
	}
	toolUse := decodeOutput(t, &output)
	if toolUse.Type != "tool_use" || toolUse.ToolName != "Read" || toolUse.ToolID != "toolu_1" {
		t.Fatalf("unexpected tool_use output: %#v", toolUse)
	}
	if got := toolUse.ToolInput["file_path"]; got != "README.md" {
		t.Fatalf("unexpected tool input: %#v", toolUse.ToolInput)
	}

	if err := sio.WriteError(errors.New("boom")); err != nil {
		t.Fatalf("WriteError failed: %v", err)
		return
	}
	errMsg := decodeOutput(t, &output)
	if errMsg.Type != "error" || errMsg.Content != "boom" {
		t.Fatalf("unexpected error output: %#v", errMsg)
	}
}

func TestStreamAdapterMapsQueryEvents(t *testing.T) {
	var output bytes.Buffer
	adapter := NewStreamAdapter(NewStructuredIO(bytes.NewReader(nil), &output))

	if err := adapter.HandleEvent(&engine.QueryEvent{
		Type:             engine.EventAssistant,
		AssistantMessage: &schema.Message{Role: schema.Assistant, Content: "hello"},
	}); err != nil {
		t.Fatalf("assistant event failed: %v", err)
		return
	}
	assistant := decodeOutput(t, &output)
	if assistant.Type != "assistant" || assistant.Content != "hello" {
		t.Fatalf("unexpected assistant output: %#v", assistant)
	}

	if err := adapter.HandleEvent(&engine.QueryEvent{
		Type:        engine.EventStream,
		StreamEvent: &schema.Message{Role: schema.Assistant, Content: "chunk"},
	}); err != nil {
		t.Fatalf("stream event failed: %v", err)
		return
	}
	stream := decodeOutput(t, &output)
	if stream.Type != "assistant" || stream.Content != "chunk" || stream.Meta["streaming"] != true {
		t.Fatalf("unexpected stream output: %#v", stream)
	}

	if err := adapter.HandleEvent(&engine.QueryEvent{
		Type:              engine.EventToolResult,
		ToolResultMessage: &schema.Message{Role: schema.Tool, Content: "tool result"},
	}); err != nil {
		t.Fatalf("tool result event failed: %v", err)
		return
	}
	result := decodeOutput(t, &output)
	if result.Type != "result" || result.Content != "tool result" {
		t.Fatalf("unexpected result output: %#v", result)
	}

	if err := adapter.HandleEvent(&engine.QueryEvent{
		Type:         engine.EventTerminal,
		TerminalInfo: &engine.Terminal{Reason: engine.TerminalCompleted, TurnCount: 3},
	}); err != nil {
		t.Fatalf("terminal event failed: %v", err)
		return
	}
	terminal := decodeOutput(t, &output)
	if terminal.Type != "status" || terminal.Content != "terminal" {
		t.Fatalf("unexpected terminal output: %#v", terminal)
	}
	if terminal.Meta["reason"] != "completed" || terminal.Meta["turn_count"].(float64) != 3 {
		t.Fatalf("unexpected terminal metadata: %#v", terminal.Meta)
	}

	if err := adapter.HandleEvent("not a query event"); err == nil {
		t.Fatal("expected unsupported event type error")
		return
	}
}
