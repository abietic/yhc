package agenticdeepseek

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/cloudwego/eino/schema"
)

type streamEvent struct {
	Type           string          `json:"type"`
	SequenceNumber *int64          `json:"sequence_number"`
	OutputIndex    int             `json:"output_index"`
	ContentIndex   int             `json:"content_index"`
	ItemID         string          `json:"item_id"`
	Delta          string          `json:"delta"`
	Item           outputItem      `json:"item"`
	Response       *responseObject `json:"response"`
	Code           string          `json:"code"`
	Message        string          `json:"message"`
}

type sseRecord struct {
	event string
	data  string
}

type streamedFunctionCall struct {
	callID       string
	name         string
	outputIndex  int
	deltaEmitted bool
}

type streamState struct {
	sequenceSeen  bool
	sequence      int64
	terminalSeen  bool
	functionCalls map[string]*streamedFunctionCall
}

func parseResponseStream(
	reader io.Reader,
	maxEventBytes int,
	send func(*schema.AgenticMessage) bool,
) error {
	state := &streamState{functionCalls: make(map[string]*streamedFunctionCall)}
	err := readSSE(reader, maxEventBytes, func(record sseRecord) error {
		if record.data == "" {
			return nil
		}
		if record.data == "[DONE]" {
			return &ProtocolError{ReasonCode: "chat_completions_done_marker"}
		}
		var event streamEvent
		if err := json.Unmarshal([]byte(record.data), &event); err != nil {
			return &ProtocolError{ReasonCode: "stream_event_json_invalid"}
		}
		if event.Type == "" {
			return &ProtocolError{ReasonCode: "stream_event_type_missing"}
		}
		if record.event != "" && record.event != "message" && record.event != event.Type {
			return &ProtocolError{ReasonCode: "stream_event_type_mismatch"}
		}
		if err := state.acceptSequence(event.SequenceNumber); err != nil {
			return err
		}
		message, terminal, err := state.convertEvent(&event)
		if err != nil {
			return err
		}
		if message != nil && send(message) {
			return io.EOF
		}
		if terminal {
			state.terminalSeen = true
			return io.EOF
		}
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if !state.terminalSeen {
		return &ProtocolError{ReasonCode: "stream_terminal_event_missing"}
	}
	return nil
}

func (s *streamState) acceptSequence(sequence *int64) error {
	if sequence == nil {
		return &ProtocolError{ReasonCode: "stream_sequence_missing"}
	}
	if s.sequenceSeen && *sequence <= s.sequence {
		return &ProtocolError{ReasonCode: "stream_sequence_not_increasing"}
	}
	s.sequenceSeen = true
	s.sequence = *sequence
	return nil
}

func (s *streamState) convertEvent(event *streamEvent) (*schema.AgenticMessage, bool, error) {
	switch event.Type {
	case "response.created", "response.in_progress",
		"response.content_part.added", "response.content_part.done",
		"response.output_text.done", "response.reasoning_text.done",
		"response.reasoning_summary_text.done",
		"response.function_call_arguments.done":
		return nil, false, nil
	case "response.output_item.added":
		if event.Item.Type == "function_call" {
			if event.Item.ID == "" || event.Item.CallID == "" || event.Item.Name == "" {
				return nil, false, &ProtocolError{ReasonCode: "stream_function_call_invalid"}
			}
			if _, exists := s.functionCalls[event.Item.ID]; exists {
				return nil, false, &ProtocolError{ReasonCode: "stream_function_call_duplicate"}
			}
			s.functionCalls[event.Item.ID] = &streamedFunctionCall{
				callID:      event.Item.CallID,
				name:        event.Item.Name,
				outputIndex: event.OutputIndex,
			}
		}
		return nil, false, nil
	case "response.output_item.done":
		if event.Item.Type != "function_call" {
			return nil, false, nil
		}
		call := s.functionCalls[event.Item.ID]
		if call == nil {
			if event.Item.ID == "" || event.Item.CallID == "" || event.Item.Name == "" {
				return nil, false, &ProtocolError{ReasonCode: "stream_function_call_done_invalid"}
			}
			call = &streamedFunctionCall{
				callID:      event.Item.CallID,
				name:        event.Item.Name,
				outputIndex: event.OutputIndex,
			}
			s.functionCalls[event.Item.ID] = call
		}
		if call.deltaEmitted {
			return nil, false, nil
		}
		return functionCallChunk(call, defaultArguments(event.Item.Arguments)), false, nil
	case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
		return reasoningChunk(event.Delta, event.OutputIndex), false, nil
	case "response.output_text.delta":
		return textChunk(event.Delta, event.OutputIndex), false, nil
	case "response.function_call_arguments.delta":
		call := s.functionCalls[event.ItemID]
		if call == nil {
			return nil, false, &ProtocolError{ReasonCode: "stream_function_call_delta_without_item"}
		}
		call.deltaEmitted = true
		return functionCallChunk(call, event.Delta), false, nil
	case "response.completed", "response.incomplete":
		message, err := terminalStreamMessage(event.Response)
		return message, true, err
	case "response.failed":
		if event.Response == nil {
			return nil, false, &ProtocolError{ReasonCode: "stream_failed_response_missing"}
		}
		return nil, false, apiErrorFromResponse(event.Response)
	case "error", "response.error":
		return nil, false, &APIError{
			Code:    boundedText(event.Code, 128),
			Message: boundedText(event.Message, 1024),
		}
	default:
		// DeepSeek may add lifecycle or server-tool events. Unknown events are
		// ignored only after their envelope and ordering have been validated.
		return nil, false, nil
	}
}

func terminalStreamMessage(response *responseObject) (*schema.AgenticMessage, error) {
	if response == nil || response.Object != "response" || response.ID == "" {
		return nil, &ProtocolError{ReasonCode: "stream_terminal_response_invalid"}
	}
	if response.Status == ResponseStatusFailed {
		return nil, apiErrorFromResponse(response)
	}
	if response.Status != ResponseStatusCompleted && response.Status != ResponseStatusIncomplete {
		return nil, &ProtocolError{ReasonCode: "stream_terminal_status_invalid"}
	}
	return &schema.AgenticMessage{
		Role:         schema.AgenticRoleTypeAssistant,
		ResponseMeta: responseMeta(response),
	}, nil
}

func reasoningChunk(delta string, index int) *schema.AgenticMessage {
	return &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{{
			Type:          schema.ContentBlockTypeReasoning,
			Reasoning:     &schema.Reasoning{Text: delta},
			StreamingMeta: &schema.StreamingMeta{Index: index},
		}},
	}
}

func textChunk(delta string, index int) *schema.AgenticMessage {
	return &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{{
			Type:             schema.ContentBlockTypeAssistantGenText,
			AssistantGenText: &schema.AssistantGenText{Text: delta},
			StreamingMeta:    &schema.StreamingMeta{Index: index},
		}},
	}
}

func functionCallChunk(call *streamedFunctionCall, arguments string) *schema.AgenticMessage {
	return &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{{
			Type: schema.ContentBlockTypeFunctionToolCall,
			FunctionToolCall: &schema.FunctionToolCall{
				CallID:    call.callID,
				Name:      call.name,
				Arguments: arguments,
			},
			StreamingMeta: &schema.StreamingMeta{Index: call.outputIndex},
		}},
	}
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
