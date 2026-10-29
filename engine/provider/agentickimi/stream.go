package agentickimi

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/cloudwego/eino/schema"
)

type (
	sseRecord   struct{ event, data string }
	streamEvent struct {
		Type           string        `json:"type"`
		SequenceNumber *int64        `json:"sequence_number"`
		OutputIndex    *int          `json:"output_index"`
		ItemID         string        `json:"item_id"`
		Delta          string        `json:"delta"`
		Item           *wireItem     `json:"item"`
		Response       *wireResponse `json:"response"`
	}
)

type streamedItem struct {
	kind, id  string
	added     bool
	text      strings.Builder
	arguments strings.Builder
	done      *wireItem
}
type streamState struct {
	sequence     int64
	seenSequence bool
	responseID   string
	items        map[int]*streamedItem
	bytes        int
}

var (
	errConsumerClosed = errors.New("stream consumer closed")
	errTerminal       = errors.New("stream terminal received")
)

func parseResponseStream(reader io.Reader, limit int, tools []wireTool, send func(*schema.AgenticMessage) bool) error {
	state := &streamState{items: make(map[int]*streamedItem)}
	err := readSSE(reader, limit, func(record sseRecord) error {
		if record.data == "[DONE]" {
			return &ProtocolError{ReasonCode: "chat_done_marker"}
		}
		var event streamEvent
		if err := json.Unmarshal([]byte(record.data), &event); err != nil {
			return &ProtocolError{ReasonCode: "stream_json_invalid"}
		}
		if event.Type == "" || record.event != event.Type {
			return &ProtocolError{ReasonCode: "stream_type_mismatch"}
		}
		if event.SequenceNumber == nil || *event.SequenceNumber < 0 || !state.seenSequence && *event.SequenceNumber != 0 || state.seenSequence && *event.SequenceNumber <= state.sequence {
			return &ProtocolError{ReasonCode: "stream_sequence_invalid"}
		}
		state.sequence = *event.SequenceNumber
		state.seenSequence = true
		if event.Response != nil {
			if event.Response.ID == "" || event.Response.Object != "response" || event.Response.Model != ModelK3 {
				return &ProtocolError{ReasonCode: "stream_response_invalid"}
			}
			if state.responseID != "" && state.responseID != event.Response.ID {
				return &ProtocolError{ReasonCode: "stream_response_id_changed"}
			}
			state.responseID = event.Response.ID
			// Validate every reported usage object, including nonterminal receipts.
			if _, err := responseTokenUsage(event.Response); err != nil {
				return err
			}
		}
		switch event.Type {
		case "response.created", "response.in_progress":
			if event.Response == nil || event.Response.Status != "in_progress" {
				return &ProtocolError{ReasonCode: "stream_opening_invalid"}
			}
			return nil
		case "response.output_item.added", "response.output_item.done":
			if event.Item == nil {
				return &ProtocolError{ReasonCode: "stream_item_missing"}
			}
			item, err := state.item(event.OutputIndex, event.Item.Type, event.Item.ID)
			if err != nil {
				return err
			}
			if event.Type == "response.output_item.added" {
				if item.added || item.done != nil {
					return &ProtocolError{ReasonCode: "stream_item_duplicate"}
				}
				item.added = true
			} else {
				if item.done != nil {
					return &ProtocolError{ReasonCode: "stream_item_duplicate"}
				}
				item.done = event.Item
			}
			return nil
		case "response.output_text.delta", "response.reasoning_summary_text.delta", "response.function_call_arguments.delta":
			kind := "message"
			if event.Type == "response.reasoning_summary_text.delta" {
				kind = "reasoning"
			}
			if event.Type == "response.function_call_arguments.delta" {
				kind = "function_call"
			}
			item, err := state.item(event.OutputIndex, kind, event.ItemID)
			if err != nil {
				return err
			}
			if item.done != nil {
				return &ProtocolError{ReasonCode: "stream_delta_after_done"}
			}
			state.bytes += len(event.Delta)
			if state.bytes > maxResponseBytes {
				return &ProtocolError{ReasonCode: "stream_output_too_large"}
			}
			if kind == "function_call" {
				item.arguments.WriteString(event.Delta)
				return nil
			}
			item.text.WriteString(event.Delta)
			if event.Delta == "" {
				return nil
			}
			block := &schema.ContentBlock{StreamingMeta: &schema.StreamingMeta{Index: *event.OutputIndex}}
			if kind == "reasoning" {
				block.Type = schema.ContentBlockTypeReasoning
				block.Reasoning = &schema.Reasoning{Text: event.Delta}
			} else {
				block.Type = schema.ContentBlockTypeAssistantGenText
				block.AssistantGenText = &schema.AssistantGenText{Text: event.Delta}
			}
			if send(&schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{block}}) {
				return errConsumerClosed
			}
			return nil
		case "response.content_part.added", "response.content_part.done", "response.output_text.done", "response.reasoning_summary_part.added", "response.reasoning_summary_part.done", "response.reasoning_summary_text.done", "response.function_call_arguments.done":
			return nil
		case "response.completed", "response.incomplete", "response.failed":
			if event.Response == nil || event.Type != "response."+event.Response.Status {
				return &ProtocolError{ReasonCode: "stream_terminal_mismatch"}
			}
			if err := validateWireResponse(event.Response); err != nil {
				return err
			}
			if event.Response.Status == "failed" {
				return terminalAPIError(event.Response)
			}
			if err := validateResponseTools(event.Response, tools); err != nil {
				return err
			}
			message, err := responseMessage(event.Response)
			if err != nil {
				return err
			}
			if err := state.reconcile(event.Response, message); err != nil {
				return err
			}
			if send(message) {
				return errConsumerClosed
			}
			return errTerminal
		case "error":
			return &APIError{Code: "provider_error"}
		default:
			return &ProtocolError{ReasonCode: "stream_event_unsupported"}
		}
	})
	if errors.Is(err, errTerminal) || errors.Is(err, errConsumerClosed) {
		return nil
	}
	if err != nil {
		return err
	}
	return &ProtocolError{ReasonCode: "stream_terminal_missing"}
}

func (s *streamState) item(index *int, kind, id string) (*streamedItem, error) {
	if index == nil || *index < 0 || *index >= 1024 || (kind != "message" && kind != "reasoning" && kind != "function_call") {
		return nil, &ProtocolError{ReasonCode: "stream_item_invalid"}
	}
	item := s.items[*index]
	if item == nil {
		item = &streamedItem{kind: kind, id: id}
		s.items[*index] = item
		return item, nil
	}
	if item.kind != kind || id != "" && item.id != "" && id != item.id {
		return nil, &ProtocolError{ReasonCode: "stream_item_identity_changed"}
	}
	if item.id == "" {
		item.id = id
	}
	return item, nil
}

// The terminal snapshot is the authority for tool calls and usage. Already
// displayed text must agree with it; only its unstreamed suffix is emitted.
func (s *streamState) reconcile(response *wireResponse, message *schema.AgenticMessage) error {
	for index, item := range s.items {
		if index >= len(response.Output) {
			return &ProtocolError{ReasonCode: "stream_output_missing"}
		}
		final := response.Output[index]
		if final.Type != item.kind || item.id != "" && final.ID != item.id {
			return &ProtocolError{ReasonCode: "stream_output_changed"}
		}
		if item.done != nil && !sameCompletedItem(*item.done, final) {
			return &ProtocolError{ReasonCode: "stream_done_changed"}
		}
		if item.kind == "function_call" {
			if item.arguments.Len() > 0 && !strings.HasPrefix(final.Arguments, item.arguments.String()) {
				return &ProtocolError{ReasonCode: "stream_arguments_changed"}
			}
		}
	}
	for _, block := range message.ContentBlocks {
		item := s.items[block.StreamingMeta.Index]
		if item == nil {
			continue
		}
		switch block.Type {
		case schema.ContentBlockTypeReasoning:
			text := block.Reasoning.Text
			if !strings.HasPrefix(text, item.text.String()) {
				return &ProtocolError{ReasonCode: "stream_reasoning_changed"}
			}
			block.Reasoning.Text = text[item.text.Len():]
		case schema.ContentBlockTypeAssistantGenText:
			text := block.AssistantGenText.Text
			if !strings.HasPrefix(text, item.text.String()) {
				return &ProtocolError{ReasonCode: "stream_text_changed"}
			}
			block.AssistantGenText.Text = text[item.text.Len():]
		}
	}
	return nil
}

func sameCompletedItem(a, b wireItem) bool {
	if a.Type != b.Type || a.ID != b.ID || a.Status != b.Status || a.CallID != b.CallID || a.Name != b.Name || a.Namespace != b.Namespace || a.Arguments != b.Arguments {
		return false
	}
	if a.Type == "message" {
		av, ok := joinedParts(a.Content, "output_text")
		bv, bok := joinedParts(b.Content, "output_text")
		return ok && bok && a.Role == b.Role && av == bv
	}
	if a.Type == "reasoning" {
		av, ok := joinedParts(a.Summary, "summary_text")
		bv, bok := joinedParts(b.Summary, "summary_text")
		return ok && bok && a.EncryptedContent == nil && b.EncryptedContent == nil && av == bv
	}
	return true
}

func readSSE(reader io.Reader, maxEventBytes int, handle func(sseRecord) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), maxEventBytes+1)
	record := sseRecord{}
	size := 0
	dispatch := func() error {
		if record.data == "" {
			record = sseRecord{}
			size = 0
			return nil
		}
		err := handle(record)
		record = sseRecord{}
		size = 0
		return err
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		size += len(line) + 1
		if size > maxEventBytes {
			return &ProtocolError{ReasonCode: "stream_event_too_large"}
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if !found {
			field = line
			value = ""
		} else {
			value = strings.TrimPrefix(value, " ")
		}
		switch field {
		case "event":
			record.event = value
		case "data":
			if record.data != "" {
				record.data += "\n"
			}
			record.data += value
		case "id", "retry":
		default:
			// Per the SSE specification, unknown fields are ignored.
		}
	}
	if err := scanner.Err(); err != nil {
		if strings.Contains(err.Error(), "token too long") {
			return &ProtocolError{ReasonCode: "stream_event_too_large"}
		}
		return &transportError{err: err}
	}
	return dispatch()
}
