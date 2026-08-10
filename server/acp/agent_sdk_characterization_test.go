package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/abietic/yhc/engine"
)

func TestP231InitializeNegotiatesV1ForV1AndV2(t *testing.T) {
	for _, requested := range []acpsdk.ProtocolVersion{1, 2} {
		t.Run(fmt.Sprintf("request_%d", requested), func(t *testing.T) {
			raw := newP231RawAgentConnection(t, &mockChatModel{})
			requestID := fmt.Sprintf("initialize-%d", requested)
			raw.sendRequest(
				t,
				requestID,
				acpsdk.AgentMethodInitialize,
				acpsdk.InitializeRequest{ProtocolVersion: requested},
			)
			response := raw.waitResponse(t, requestID)
			if response["error"] != nil {
				t.Fatalf("initialize error = %#v", response["error"])
			}
			result, ok := response["result"].(map[string]any)
			if !ok {
				t.Fatalf("initialize result = %#v", response["result"])
			}
			if result["protocolVersion"] !=
				float64(acpsdk.ProtocolVersionNumber) {
				t.Fatalf(
					"protocol version = %#v, want %d",
					result["protocolVersion"],
					acpsdk.ProtocolVersionNumber,
				)
			}
			for _, v2Only := range []string{
				"info",
				"capabilities",
				"state_update",
				"replayFrom",
			} {
				if _, exposed := result[v2Only]; exposed {
					t.Fatalf(
						"v1 response exposed v2-only shape %q: %#v",
						v2Only,
						result,
					)
				}
			}
		})
	}
}

func TestP231PinnedSDKErrorMapping(t *testing.T) {
	conn, _ := setupTestACP(t, &mockChatModel{})

	t.Run("method not found", func(t *testing.T) {
		_, err := conn.CallExtension(
			t.Context(),
			"_p23.missing",
			map[string]any{},
		)
		requireP231RequestError(
			t,
			err,
			-32601,
			"Method not found",
			map[string]any{"method": "_p23.missing"},
		)
	})

	t.Run("malformed prompt", func(t *testing.T) {
		block := acpsdk.ResourceLinkBlock("resource", "")
		_, err := conn.Prompt(t.Context(), acpsdk.PromptRequest{
			SessionId: "missing",
			Prompt:    []acpsdk.ContentBlock{block},
		})
		requireP231RequestError(
			t,
			err,
			-32602,
			"Invalid params",
			map[string]any{
				"input":  "prompt.resourceLink",
				"reason": "resource_uri_required",
				"block":  float64(0),
			},
		)
	})

	t.Run("unsupported prompt", func(t *testing.T) {
		_, err := conn.Prompt(t.Context(), acpsdk.PromptRequest{
			SessionId: "missing",
			Prompt: []acpsdk.ContentBlock{
				acpsdk.AudioBlock("private", "audio/wav"),
			},
		})
		requireP231RequestError(
			t,
			err,
			codeUnsupportedInput,
			"Unsupported input",
			map[string]any{
				"input":  "prompt.audio",
				"reason": "capability_unsupported",
				"block":  float64(0),
			},
		)
	})

	t.Run("plain handler error becomes internal error", func(t *testing.T) {
		_, err := conn.Prompt(t.Context(), acpsdk.PromptRequest{
			SessionId: "missing-current-mapping",
			Prompt: []acpsdk.ContentBlock{
				acpsdk.TextBlock("valid"),
			},
		})
		requireP231RequestError(
			t,
			err,
			-32603,
			"Internal error",
			map[string]any{
				"error": "session not found: missing-current-mapping",
			},
		)
	})
}

func TestP231OutboundRequestContextCancellationMappingAndSessionReuse(t *testing.T) {
	model := newFirstCallBlockingChatModel()
	t.Cleanup(model.release)
	conn, _, agent := setupTestACPWithAgent(t, model)

	session, err := conn.NewSession(t.Context(), acpsdk.NewSessionRequest{
		Cwd:        t.TempDir(),
		McpServers: []acpsdk.McpServer{},
	})
	if err != nil {
		t.Fatal(err)
	}

	promptCtx, cancelPrompt := context.WithCancel(t.Context())
	promptDone := make(chan error, 1)
	go func() {
		_, promptErr := conn.Prompt(promptCtx, acpsdk.PromptRequest{
			SessionId: session.SessionId,
			Prompt: []acpsdk.ContentBlock{
				acpsdk.TextBlock("cancel through JSON-RPC request context"),
			},
		})
		promptDone <- promptErr
	}()

	select {
	case <-model.firstStarted:
	case <-t.Context().Done():
		t.Fatal("first prompt did not reach the model")
	}
	cancelPrompt()

	select {
	case promptErr := <-promptDone:
		requireP231RequestError(
			t,
			promptErr,
			-32800,
			"Request cancelled",
			map[string]any{"error": context.Canceled.Error()},
		)
	case <-t.Context().Done():
		t.Fatal("generic request cancellation did not return")
	}

	select {
	case <-model.firstCancelled:
	case <-t.Context().Done():
		t.Fatal("generic request cancellation did not reach the model context")
	}
	model.release()

	agent.mu.Lock()
	active := agent.sessions[session.SessionId]
	agent.mu.Unlock()
	if active == nil {
		t.Fatal("session disappeared after generic request cancellation")
	}
	waitP231PromptIdle(t, active)

	response, err := conn.Prompt(t.Context(), acpsdk.PromptRequest{
		SessionId: session.SessionId,
		Prompt: []acpsdk.ContentBlock{
			acpsdk.TextBlock("second prompt"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.StopReason != acpsdk.StopReasonEndTurn {
		t.Fatalf("second prompt stop reason = %q", response.StopReason)
	}
}

func TestP231PinnedSDKInboundGenericCancelRequest(t *testing.T) {
	inboundReader, inboundWriter := io.Pipe()
	wire := newP231WireBuffer()
	started := make(chan struct{})
	acpsdk.NewConnection(
		func(
			ctx context.Context,
			_ string,
			_ json.RawMessage,
		) (any, *acpsdk.RequestError) {
			close(started)
			<-ctx.Done()
			return nil, acpsdk.NewRequestCancelled(
				map[string]any{"error": ctx.Err().Error()},
			)
		},
		wire,
		inboundReader,
	)
	t.Cleanup(func() {
		_ = inboundWriter.Close()
		_ = inboundReader.Close()
	})
	raw := &p231RawAgentConnection{
		input: inboundWriter,
		wire:  wire,
	}
	raw.sendRequest(t, "blocked-request", "_p23.blocking", map[string]any{})
	select {
	case <-started:
	case <-t.Context().Done():
		t.Fatal("generic SDK handler did not start")
	}

	raw.sendNotification(
		t,
		"$/cancel_request",
		map[string]any{"requestId": "blocked-request"},
	)
	response := raw.waitResponse(t, "blocked-request")
	requestError, ok := response["error"].(map[string]any)
	if !ok {
		t.Fatalf("generic-cancelled request error = %#v", response["error"])
	}
	if got := requestError["code"]; got != float64(-32800) {
		t.Fatalf("generic-cancelled error code = %#v", got)
	}
	if got := requestError["message"]; got != "Request cancelled" {
		t.Fatalf("generic-cancelled error message = %#v", got)
	}
	errorData, ok := requestError["data"].(map[string]any)
	if !ok || errorData["error"] != context.Canceled.Error() {
		t.Fatalf("generic-cancelled error data = %#v", requestError["data"])
	}
}

func TestP231PromptResponseWaitsForPriorNotifications(t *testing.T) {
	baseClient := &testClient{}
	client := &p231BarrierClient{
		testClient: baseClient,
		received:   make(chan struct{}),
		release:    make(chan struct{}),
	}
	conn, agent := setupP231ACPWithClient(
		t,
		client,
		&mockChatModel{responses: []*schema.Message{{
			Role: schema.Assistant, Content: "done",
		}}},
	)
	t.Cleanup(func() {
		select {
		case <-client.release:
		default:
			close(client.release)
		}
	})

	session, err := conn.NewSession(t.Context(), acpsdk.NewSessionRequest{
		Cwd:        t.TempDir(),
		McpServers: []acpsdk.McpServer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	type promptResult struct {
		response acpsdk.PromptResponse
		err      error
	}
	done := make(chan promptResult, 1)
	go func() {
		response, promptErr := conn.Prompt(
			t.Context(),
			acpsdk.PromptRequest{
				SessionId: session.SessionId,
				Prompt: []acpsdk.ContentBlock{
					acpsdk.TextBlock("notification barrier"),
				},
			},
		)
		done <- promptResult{response: response, err: promptErr}
	}()

	select {
	case <-client.received:
	case <-t.Context().Done():
		t.Fatal("assistant notification did not reach client handler")
	}
	select {
	case result := <-done:
		t.Fatalf(
			"prompt returned before prior notification completed: %#v",
			result,
		)
	default:
	}
	close(client.release)
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.response.StopReason != acpsdk.StopReasonEndTurn {
			t.Fatalf("stop reason = %q", result.response.StopReason)
		}
	case <-t.Context().Done():
		t.Fatal("prompt did not return after notification completed")
	}

	agent.mu.Lock()
	active := agent.sessions[session.SessionId]
	agent.mu.Unlock()
	if active == nil {
		t.Fatal("session missing after prompt")
	}
	waitP231PromptIdle(t, active)
}

func TestP233CanonicalAssistantAndToolProjectionWireGolden(t *testing.T) {
	agent, wire := newP231WireCaptureAgent(t)
	sessionID := acpsdk.SessionId("p23-1-wire")
	installP232ToolLedgerSessions(agent, sessionID)
	const messageID = "550e8400-e29b-41d4-a716-446655440000"
	events := []engine.QueryEvent{
		{
			Type: engine.EventAssistant,
			AssistantMessage: &schema.Message{
				Role:    schema.Assistant,
				Content: "legacy assistant bytes must not be sent",
			},
		},
		{
			Type: engine.EventCanonicalProjection,
			CanonicalProjection: &engine.CanonicalProjectionEvent{
				Version: engine.CanonicalProjectionVersion,
				Kind:    engine.CanonicalProjectionAssistantDelta,
				Assistant: &engine.CanonicalAssistantPayload{
					MessageID: messageID,
					Delta:     []byte("a\n\n"),
				},
			},
		},
		{
			Type: engine.EventAssistant,
			AssistantMessage: &schema.Message{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{{
					ID:   "call-fragmented",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "Read",
						Arguments: `{`,
					},
				}},
			},
		},
		{
			Type: engine.EventAssistant,
			AssistantMessage: &schema.Message{
				Role:    schema.Assistant,
				Content: " ",
				ToolCalls: []schema.ToolCall{{
					ID:   "call-fragmented",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "Read",
						Arguments: `{"file_path":"x"}`,
					},
				}},
			},
		},
		{
			Type: engine.EventCanonicalProjection,
			CanonicalProjection: &engine.CanonicalProjectionEvent{
				Version: engine.CanonicalProjectionVersion,
				Kind:    engine.CanonicalProjectionToolStart,
				Tool: &engine.CanonicalToolPayload{
					ToolCallID: "call-fragmented",
					ToolName:   "Read",
				},
			},
		},
		{
			Type: engine.EventCanonicalProjection,
			CanonicalProjection: &engine.CanonicalProjectionEvent{
				Version: engine.CanonicalProjectionVersion,
				Kind:    engine.CanonicalProjectionToolInput,
				Tool: &engine.CanonicalToolPayload{
					ToolCallID:     "call-fragmented",
					EffectiveInput: json.RawMessage(`{"file_path":"x"}`),
				},
			},
		},
		{
			Type: engine.EventCanonicalProjection,
			CanonicalProjection: &engine.CanonicalProjectionEvent{
				Version: engine.CanonicalProjectionVersion,
				Kind:    engine.CanonicalProjectionToolProgress,
				Tool: &engine.CanonicalToolPayload{
					ToolCallID: "call-fragmented",
					Content:    "A",
				},
			},
		},
		{
			Type: engine.EventCanonicalProjection,
			CanonicalProjection: &engine.CanonicalProjectionEvent{
				Version: engine.CanonicalProjectionVersion,
				Kind:    engine.CanonicalProjectionToolProgress,
				Tool: &engine.CanonicalToolPayload{
					ToolCallID: "call-fragmented",
					Content:    "B",
				},
			},
		},
		{
			Type: engine.EventCanonicalProjection,
			CanonicalProjection: &engine.CanonicalProjectionEvent{
				Version: engine.CanonicalProjectionVersion,
				Kind:    engine.CanonicalProjectionToolTerminal,
				Tool: &engine.CanonicalToolPayload{
					ToolCallID: "call-fragmented",
					Outcome:    engine.CanonicalToolOutcomeFailed,
					RawOutput:  json.RawMessage(`"failed"`),
				},
			},
		},
		{
			Type: engine.EventAssistant,
			AssistantMessage: &schema.Message{
				Role:    schema.Assistant,
				Content: "legacy final bytes must not be sent",
			},
		},
		{
			Type: engine.EventCanonicalProjection,
			CanonicalProjection: &engine.CanonicalProjectionEvent{
				Version: engine.CanonicalProjectionVersion,
				Kind:    engine.CanonicalProjectionAssistantDelta,
				Assistant: &engine.CanonicalAssistantPayload{
					MessageID: messageID,
					Delta:     []byte(" b"),
				},
			},
		},
	}
	for _, event := range events {
		if err := agent.streamEvent(t.Context(), sessionID, event); err != nil {
			t.Fatal(err)
		}
	}

	actual := normalizeP231Wire(t, wire.Bytes())
	expected, err := os.ReadFile(filepath.Join(
		"testdata",
		"p23-3-assistant-tool-lifecycle.golden.json",
	))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, expected) {
		t.Fatalf(
			"P23.3 assistant/tool lifecycle wire mismatch\nactual:\n%s\nexpected:\n%s",
			actual,
			expected,
		)
	}

	var messages []map[string]any
	if err := json.Unmarshal(actual, &messages); err != nil {
		t.Fatal(err)
	}
	var assistantText strings.Builder
	for _, message := range messages {
		params, _ := message["params"].(map[string]any)
		update, _ := params["update"].(map[string]any)
		if update["sessionUpdate"] != "agent_message_chunk" {
			continue
		}
		content, _ := update["content"].(map[string]any)
		text, _ := content["text"].(string)
		assistantText.WriteString(text)
		if got := update["messageId"]; got != messageID {
			t.Fatalf("assistant messageId = %#v, want %q", got, messageID)
		}
	}
	if got, want := assistantText.String(), "a\n\n b"; got != want {
		t.Fatalf("assistant bytes = %q, want %q", got, want)
	}
}

func TestP232CanonicalToolInputIgnoresFragmentedProviderJSON(t *testing.T) {
	complete := `{"file_path":"x"}`
	for boundary := 1; boundary < len(complete); boundary++ {
		t.Run(fmt.Sprintf("boundary_%d", boundary), func(t *testing.T) {
			agent, wire := newP231WireCaptureAgent(t)
			installP232ToolLedgerSessions(agent, "fragment-session")
			for _, arguments := range []string{complete[:boundary], complete} {
				if err := agent.streamEvent(
					t.Context(),
					"fragment-session",
					engine.QueryEvent{
						Type: engine.EventAssistant,
						AssistantMessage: &schema.Message{
							Role: schema.Assistant,
							ToolCalls: []schema.ToolCall{{
								ID:   "fragment-call",
								Type: "function",
								Function: schema.FunctionCall{
									Name:      "Read",
									Arguments: arguments,
								},
							}},
						},
					},
				); err != nil {
					t.Fatal(err)
				}
			}
			for _, projection := range []*engine.CanonicalProjectionEvent{
				{
					Version: engine.CanonicalProjectionVersion,
					Kind:    engine.CanonicalProjectionToolStart,
					Tool: &engine.CanonicalToolPayload{
						ToolCallID: "fragment-call",
						ToolName:   "Read",
					},
				},
				{
					Version: engine.CanonicalProjectionVersion,
					Kind:    engine.CanonicalProjectionToolInput,
					Tool: &engine.CanonicalToolPayload{
						ToolCallID:     "fragment-call",
						EffectiveInput: json.RawMessage(complete),
					},
				},
			} {
				if err := agent.streamEvent(
					t.Context(),
					"fragment-session",
					engine.QueryEvent{
						Type:                engine.EventCanonicalProjection,
						CanonicalProjection: projection,
					},
				); err != nil {
					t.Fatal(err)
				}
			}

			var messages []map[string]any
			if err := json.Unmarshal(
				normalizeP231Wire(t, wire.Bytes()),
				&messages,
			); err != nil {
				t.Fatal(err)
			}
			if len(messages) != 2 {
				t.Fatalf("tool lifecycle updates = %d, want 2", len(messages))
			}
			first := p231WireUpdate(t, messages[0])
			if got := first["sessionUpdate"]; got != "tool_call" {
				t.Fatalf(
					"first update = %#v, want tool_call",
					got,
				)
			}
			if _, present := first["rawInput"]; present {
				t.Fatalf("start exposed provider fragment: %#v", first)
			}
			second := p231WireUpdate(t, messages[1])
			wantInput := map[string]any{"file_path": "x"}
			if got, ok := second["rawInput"].(map[string]any); !ok ||
				!equalP231JSONMap(got, wantInput) {
				t.Fatalf(
					"second raw input = %#v, want %#v",
					second["rawInput"],
					wantInput,
				)
			}
		})
	}
}

func TestP232CanonicalToolProjectionDeliveryFailureBoundary(t *testing.T) {
	t.Run("canonical assistant delivery failure is returned", func(t *testing.T) {
		agent := newP231FailingWireAgent(t)
		err := agent.streamEvent(
			t.Context(),
			"delivery-text",
			engine.QueryEvent{
				Type: engine.EventCanonicalProjection,
				CanonicalProjection: &engine.CanonicalProjectionEvent{
					Version: engine.CanonicalProjectionVersion,
					Kind:    engine.CanonicalProjectionAssistantDelta,
					Assistant: &engine.CanonicalAssistantPayload{
						MessageID: "550e8400-e29b-41d4-a716-446655440001",
						Delta:     []byte("visible"),
					},
				},
			},
		)
		if err == nil || !strings.Contains(err.Error(), "delivery failed") {
			t.Fatalf("assistant delivery error = %v", err)
		}
	})

	t.Run("tool start delivery failure is returned and settled", func(t *testing.T) {
		agent := newP231FailingWireAgent(t)
		installP232ToolLedgerSessions(agent, "delivery-tool")
		err := agent.streamEvent(
			t.Context(),
			"delivery-tool",
			engine.QueryEvent{
				Type: engine.EventCanonicalProjection,
				CanonicalProjection: &engine.CanonicalProjectionEvent{
					Version: engine.CanonicalProjectionVersion,
					Kind:    engine.CanonicalProjectionToolStart,
					Tool: &engine.CanonicalToolPayload{
						ToolCallID: "call-delivery",
						ToolName:   "Read",
					},
				},
			},
		)
		if err == nil || !strings.Contains(err.Error(), "delivery failed") {
			t.Fatalf("tool-start delivery error = %v", err)
		}
		ledger, ledgerErr := agent.sessionToolLifecycleLedger("delivery-tool")
		if ledgerErr != nil {
			t.Fatal(ledgerErr)
		}
		if snapshot := ledger.snapshot("call-delivery"); !snapshot.LocallySettled ||
			!snapshot.DeliveryFailed ||
			snapshot.TerminalDelivered {
			t.Fatalf("delivery-failure snapshot = %#v", snapshot)
		}
	})
}

func TestP233AssistantMessageIDRollbackAndSDKRoundTrip(t *testing.T) {
	const messageID = "550e8400-e29b-41d4-a716-446655440002"
	projection := &engine.CanonicalProjectionEvent{
		Version: engine.CanonicalProjectionVersion,
		Kind:    engine.CanonicalProjectionAssistantDelta,
		Assistant: &engine.CanonicalAssistantPayload{
			MessageID: messageID,
			Delta:     []byte("exact\n\nbytes"),
		},
	}

	t.Run("rollback omits only messageId", func(t *testing.T) {
		agent, wire := newP231WireCaptureAgent(t)
		agent.config.DisableACPAssistantMessageIDs = true
		if err := agent.streamEvent(t.Context(), "rollback", engine.QueryEvent{
			Type:                engine.EventCanonicalProjection,
			CanonicalProjection: projection,
		}); err != nil {
			t.Fatal(err)
		}
		var messages []map[string]any
		if err := json.Unmarshal(normalizeP231Wire(t, wire.Bytes()), &messages); err != nil {
			t.Fatal(err)
		}
		if len(messages) != 1 {
			t.Fatalf("wire messages = %d, want 1", len(messages))
		}
		update := p231WireUpdate(t, messages[0])
		if _, present := update["messageId"]; present {
			t.Fatalf("rollback exposed messageId: %#v", update)
		}
		content, _ := update["content"].(map[string]any)
		if got := content["text"]; got != "exact\n\nbytes" {
			t.Fatalf("rollback content = %#v", got)
		}
	})

	t.Run("enabled extension rejects malformed UUID without delivery", func(t *testing.T) {
		agent, wire := newP231WireCaptureAgent(t)
		malformed := *projection
		assistant := *projection.Assistant
		assistant.MessageID = "not-a-uuid"
		malformed.Assistant = &assistant
		err := agent.streamEvent(t.Context(), "malformed", engine.QueryEvent{
			Type:                engine.EventCanonicalProjection,
			CanonicalProjection: &malformed,
		})
		if err == nil || !strings.Contains(err.Error(), "not a UUID") {
			t.Fatalf("malformed message ID error = %v", err)
		}
		if len(wire.Bytes()) != 0 {
			t.Fatalf("malformed message ID wrote wire bytes: %s", wire.Bytes())
		}
	})

	t.Run("pinned SDK preserves messageId", func(t *testing.T) {
		update := acpsdk.SessionUpdate{
			AgentMessageChunk: &acpsdk.SessionUpdateAgentMessageChunk{
				Content:   acpsdk.TextBlock("exact\n\nbytes"),
				MessageId: acpsdk.Ptr(messageID),
			},
		}
		encoded, err := json.Marshal(update)
		if err != nil {
			t.Fatal(err)
		}
		var decoded acpsdk.SessionUpdate
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.AgentMessageChunk == nil ||
			decoded.AgentMessageChunk.MessageId == nil ||
			*decoded.AgentMessageChunk.MessageId != messageID {
			t.Fatalf("decoded update = %#v", decoded)
		}
		text := decoded.AgentMessageChunk.Content.Text
		if text == nil || text.Text != "exact\n\nbytes" {
			t.Fatalf("decoded content = %#v", decoded.AgentMessageChunk.Content)
		}
	})
}

func TestP232CanonicalToolProjectionSameIDIsSessionScopedOnWire(t *testing.T) {
	agent, wire := newP231WireCaptureAgent(t)
	installP232ToolLedgerSessions(agent, "session-a", "session-b")
	var wg sync.WaitGroup
	for _, sessionID := range []acpsdk.SessionId{"session-a", "session-b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, event := range []engine.QueryEvent{
				{
					Type: engine.EventCanonicalProjection,
					CanonicalProjection: &engine.CanonicalProjectionEvent{
						Version: engine.CanonicalProjectionVersion,
						Kind:    engine.CanonicalProjectionToolStart,
						Tool: &engine.CanonicalToolPayload{
							ToolCallID: "same-call",
							ToolName:   "Read",
						},
					},
				},
				{
					Type: engine.EventCanonicalProjection,
					CanonicalProjection: &engine.CanonicalProjectionEvent{
						Version: engine.CanonicalProjectionVersion,
						Kind:    engine.CanonicalProjectionToolTerminal,
						Tool: &engine.CanonicalToolPayload{
							ToolCallID: "same-call",
							Outcome:    engine.CanonicalToolOutcomeCompleted,
							RawOutput:  json.RawMessage(fmt.Sprintf("%q", sessionID)),
						},
					},
				},
			} {
				if err := agent.streamEvent(
					t.Context(),
					sessionID,
					event,
				); err != nil {
					t.Errorf("stream %s: %v", sessionID, err)
				}
			}
		}()
	}
	wg.Wait()

	var messages []map[string]any
	if err := json.Unmarshal(normalizeP231Wire(t, wire.Bytes()), &messages); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, message := range messages {
		params, _ := message["params"].(map[string]any)
		sessionID, _ := params["sessionId"].(string)
		counts[sessionID]++
	}
	if counts["session-a"] != 2 || counts["session-b"] != 2 {
		t.Fatalf("session wire counts = %#v", counts)
	}
}

func requireP231RequestError(
	t *testing.T,
	err error,
	code int,
	message string,
	data map[string]any,
) {
	t.Helper()
	var requestErr *acpsdk.RequestError
	if !errors.As(err, &requestErr) {
		t.Fatalf("error = %T %v", err, err)
	}
	if requestErr.Code != code || requestErr.Message != message {
		t.Fatalf("request error = %#v", requestErr)
	}
	actual, ok := requestErr.Data.(map[string]any)
	if !ok {
		t.Fatalf("request error data = %#v", requestErr.Data)
	}
	if !equalP231JSONMap(actual, data) {
		t.Fatalf("request error data = %#v, want %#v", actual, data)
	}
}

func equalP231JSONMap(left, right map[string]any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil &&
		rightErr == nil &&
		bytes.Equal(leftJSON, rightJSON)
}

func p231WireUpdate(t *testing.T, message map[string]any) map[string]any {
	t.Helper()
	params, ok := message["params"].(map[string]any)
	if !ok {
		t.Fatalf("wire params = %#v", message["params"])
	}
	update, ok := params["update"].(map[string]any)
	if !ok {
		t.Fatalf("wire update = %#v", params["update"])
	}
	return update
}

func installP232ToolLedgerSessions(
	agent *Agent,
	sessionIDs ...acpsdk.SessionId,
) {
	agent.mu.Lock()
	defer agent.mu.Unlock()
	for _, sessionID := range sessionIDs {
		if agent.sessions[sessionID] == nil {
			agent.sessions[sessionID] = newSession(sessionID, nil, "")
		}
	}
}

func waitP231PromptIdle(t *testing.T, session *Session) {
	t.Helper()
	for {
		session.mu.Lock()
		session.ensureSignalsLocked()
		active := session.promptActive
		notify := session.stateNotify
		session.mu.Unlock()
		if !active {
			return
		}
		select {
		case <-notify:
		case <-t.Context().Done():
			t.Fatal("session prompt did not become idle")
		}
	}
}

type p231WireBuffer struct {
	mu       sync.Mutex
	b        bytes.Buffer
	messages chan []byte
}

func newP231WireBuffer() *p231WireBuffer {
	return &p231WireBuffer{messages: make(chan []byte, 256)}
}

func (b *p231WireBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	n, err := b.b.Write(p)
	b.mu.Unlock()
	if err == nil {
		b.messages <- append([]byte(nil), p...)
	}
	return n, err
}

func (b *p231WireBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.b.Bytes()...)
}

type p231BarrierClient struct {
	*testClient
	received chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (c *p231BarrierClient) SessionUpdate(
	ctx context.Context,
	notification acpsdk.SessionNotification,
) error {
	if notification.Update.AgentMessageChunk != nil {
		c.once.Do(func() { close(c.received) })
		select {
		case <-c.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return c.testClient.SessionUpdate(ctx, notification)
}

func setupP231ACPWithClient(
	t *testing.T,
	client acpsdk.Client,
	model *mockChatModel,
) (*acpsdk.ClientSideConnection, *Agent) {
	t.Helper()
	agent, err := NewAgent(Config{
		ProviderFlag: "mock",
		ModelFlag:    "mock-model",
		APIKeyFlag:   "mock-key",
		YoloMode:     true,
		MaxTurns:     10,
		CWD:          t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	agent.mockModel = model
	clientToAgentReader, clientToAgentWriter := io.Pipe()
	agentToClientReader, agentToClientWriter := io.Pipe()
	agentConnection := acpsdk.NewAgentSideConnection(
		agent,
		agentToClientWriter,
		clientToAgentReader,
	)
	agent.SetConnection(agentConnection)
	clientConnection := acpsdk.NewClientSideConnection(
		client,
		clientToAgentWriter,
		agentToClientReader,
	)
	t.Cleanup(func() {
		agent.Close()
		_ = clientToAgentWriter.Close()
		_ = agentToClientWriter.Close()
	})
	return clientConnection, agent
}

type p231RawAgentConnection struct {
	agent *Agent
	input *io.PipeWriter
	wire  *p231WireBuffer
}

func newP231RawAgentConnection(
	t *testing.T,
	chatModel model.BaseChatModel,
) *p231RawAgentConnection {
	t.Helper()
	agent, err := NewAgent(Config{
		ProviderFlag: "mock",
		ModelFlag:    "mock-model",
		APIKeyFlag:   "mock-key",
		YoloMode:     true,
		MaxTurns:     10,
		CWD:          t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	agent.mockModel = chatModel
	inboundReader, inboundWriter := io.Pipe()
	wire := newP231WireBuffer()
	agent.SetConnection(acpsdk.NewAgentSideConnection(
		agent,
		wire,
		inboundReader,
	))
	t.Cleanup(func() {
		agent.Close()
		_ = inboundWriter.Close()
	})
	return &p231RawAgentConnection{
		agent: agent,
		input: inboundWriter,
		wire:  wire,
	}
}

func (c *p231RawAgentConnection) sendRequest(
	t *testing.T,
	id string,
	method string,
	params any,
) {
	t.Helper()
	c.send(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
}

func (c *p231RawAgentConnection) sendNotification(
	t *testing.T,
	method string,
	params any,
) {
	t.Helper()
	c.send(t, map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

func (c *p231RawAgentConnection) send(t *testing.T, message any) {
	t.Helper()
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	if _, err := c.input.Write(encoded); err != nil {
		t.Fatal(err)
	}
}

func (c *p231RawAgentConnection) waitResponse(
	t *testing.T,
	id string,
) map[string]any {
	t.Helper()
	for {
		select {
		case raw := <-c.wire.messages:
			lines := bytes.Split(bytes.TrimSpace(raw), []byte{'\n'})
			for _, line := range lines {
				if len(line) == 0 {
					continue
				}
				var message map[string]any
				if err := json.Unmarshal(line, &message); err != nil {
					t.Fatalf("raw response: %v: %s", err, line)
				}
				if message["id"] == id {
					return message
				}
			}
		case <-t.Context().Done():
			t.Fatalf("response %q did not arrive", id)
		}
	}
}

func newP231WireCaptureAgent(t *testing.T) (*Agent, *p231WireBuffer) {
	t.Helper()
	agent, err := NewAgent(Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	inboundReader, inboundWriter := io.Pipe()
	wire := newP231WireBuffer()
	agent.SetConnection(acpsdk.NewAgentSideConnection(
		agent,
		wire,
		inboundReader,
	))
	t.Cleanup(func() {
		agent.Close()
		_ = inboundWriter.Close()
	})
	return agent, wire
}

type p231FailingWriter struct{}

func (p231FailingWriter) Write([]byte) (int, error) {
	return 0, errors.New("delivery failed")
}

func newP231FailingWireAgent(t *testing.T) *Agent {
	t.Helper()
	agent, err := NewAgent(Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	inboundReader, inboundWriter := io.Pipe()
	agent.SetConnection(acpsdk.NewAgentSideConnection(
		agent,
		p231FailingWriter{},
		inboundReader,
	))
	t.Cleanup(func() {
		agent.Close()
		_ = inboundWriter.Close()
	})
	return agent
}

func normalizeP231Wire(t *testing.T, wire []byte) []byte {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(wire), []byte{'\n'})
	normalized := make([]map[string]any, 0, len(lines))
	for index, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var message map[string]any
		if err := json.Unmarshal(line, &message); err != nil {
			t.Fatalf("wire line %d: %v: %s", index, err, line)
		}
		normalized = append(normalized, message)
	}
	result, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(result, '\n')
}
