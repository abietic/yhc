package agenticglm

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/cloudwego/eino/schema"
)

type sseRecord struct{ data string }

type streamedToolCall struct {
	id   string
	name string
}

type chatStreamState struct {
	responseID   string
	requestID    string
	model        string
	doneSeen     bool
	finishSeen   bool
	finishReason string
	usage        *responseUsage
	toolCalls    map[int]streamedToolCall
	terminal     *schema.AgenticMessage
}

func parseChatStream(reader io.Reader, maxEventBytes int, send func(*schema.AgenticMessage) bool) error {
	state := &chatStreamState{toolCalls: make(map[int]streamedToolCall)}
	err := readSSE(reader, maxEventBytes, func(record sseRecord) error {
		if record.data == "" {
			return nil
		}
		if record.data == "[DONE]" {
			state.doneSeen = true
			return io.EOF
		}
		var chunk chatResponse
		if err := json.Unmarshal([]byte(record.data), &chunk); err != nil {
			return &ProtocolError{ReasonCode: "stream_event_json_invalid"}
		}
		messages, err := state.convertChunk(&chunk)
		if err != nil {
			return err
		}
		for _, message := range messages {
			if send(message) {
				return io.EOF
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if !state.doneSeen {
		return &ProtocolError{ReasonCode: "stream_done_missing"}
	}
	if !state.finishSeen {
		return &ProtocolError{ReasonCode: "stream_finish_reason_missing"}
	}
	if state.terminal == nil || send(state.terminal) {
		return io.EOF
	}
	return nil
}

func (s *chatStreamState) convertChunk(chunk *chatResponse) ([]*schema.AgenticMessage, error) {
	if chunk == nil || strings.TrimSpace(chunk.ID) == "" || len(chunk.Choices) != 1 {
		return nil, &ProtocolError{ReasonCode: "stream_envelope_invalid"}
	}
	if s.responseID == "" {
		s.responseID = chunk.ID
		s.requestID = chunk.RequestID
		s.model = chunk.Model
	} else if chunk.ID != s.responseID || (chunk.Model != "" && s.model != "" && chunk.Model != s.model) {
		return nil, &ProtocolError{ReasonCode: "stream_identity_changed"}
	}
	if chunk.RequestID != "" {
		s.requestID = chunk.RequestID
	}
	if chunk.Model != "" {
		s.model = chunk.Model
	}
	if chunk.Usage != nil {
		copied := *chunk.Usage
		s.usage = &copied
	}
	choice := chunk.Choices[0]
	messages := make([]*schema.AgenticMessage, 0, 2)
	deltaMessage, err := s.convertDelta(choice.Delta)
	if err != nil {
		return nil, err
	}
	if deltaMessage != nil {
		messages = append(messages, deltaMessage)
	}
	if choice.FinishReason != nil {
		if s.finishSeen {
			return nil, &ProtocolError{ReasonCode: "stream_finish_reason_duplicate"}
		}
		s.finishSeen = true
		s.finishReason = *choice.FinishReason
		s.terminal = &schema.AgenticMessage{
			Role: schema.AgenticRoleTypeAssistant,
			ResponseMeta: responseMeta(
				s.responseID, s.requestID, s.model, s.finishReason, s.usage,
			),
		}
	}
	return messages, nil
}

func (s *chatStreamState) convertDelta(delta chatReply) (*schema.AgenticMessage, error) {
	message := &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant}
	if delta.ReasoningContent != "" {
		message.ContentBlocks = append(message.ContentBlocks, &schema.ContentBlock{
			Type:          schema.ContentBlockTypeReasoning,
			Reasoning:     &schema.Reasoning{Text: delta.ReasoningContent},
			StreamingMeta: &schema.StreamingMeta{Index: 0},
		})
	}
	if delta.Content != nil && *delta.Content != "" {
		message.ContentBlocks = append(message.ContentBlocks, &schema.ContentBlock{
			Type:             schema.ContentBlockTypeAssistantGenText,
			AssistantGenText: &schema.AssistantGenText{Text: *delta.Content},
			StreamingMeta:    &schema.StreamingMeta{Index: 0},
		})
	}
	for _, call := range delta.ToolCalls {
		if call.Index == nil || *call.Index < 0 {
			return nil, &ProtocolError{ReasonCode: "stream_tool_index_missing"}
		}
		index := *call.Index
		prior, exists := s.toolCalls[index]
		if !exists {
			if strings.TrimSpace(call.ID) == "" || call.Type != "function" || !validToolName(call.Function.Name) {
				return nil, &ProtocolError{ReasonCode: "stream_tool_start_invalid"}
			}
			prior = streamedToolCall{id: call.ID, name: call.Function.Name}
			s.toolCalls[index] = prior
		} else {
			if call.ID != "" && call.ID != prior.id {
				return nil, &ProtocolError{ReasonCode: "stream_tool_id_changed"}
			}
			if call.Function.Name != "" && call.Function.Name != prior.name {
				return nil, &ProtocolError{ReasonCode: "stream_tool_name_changed"}
			}
		}
		message.ContentBlocks = append(message.ContentBlocks, &schema.ContentBlock{
			Type: schema.ContentBlockTypeFunctionToolCall,
			FunctionToolCall: &schema.FunctionToolCall{
				CallID:    call.ID,
				Name:      call.Function.Name,
				Arguments: call.Function.Arguments,
			},
			StreamingMeta: &schema.StreamingMeta{Index: index},
		})
	}
	if len(message.ContentBlocks) == 0 {
		return nil, nil
	}
	return message, nil
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
		if found {
			value = strings.TrimPrefix(value, " ")
		}
		if field == "data" {
			if record.data != "" {
				record.data += "\n"
			}
			record.data += value
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
