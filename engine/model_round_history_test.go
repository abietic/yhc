package engine

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/execution"
)

func TestFailedModelRoundCannotCheckpointUncommittedOutput(t *testing.T) {
	for _, kind := range []string{"tool", "text", "success", "withheld"} {
		t.Run(kind, func(t *testing.T) {
			committed := &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
				ID: "committed", Type: "function", Function: schema.FunctionCall{Name: "Write", Arguments: `{}`},
			}}}
			completed := &schema.Message{Role: schema.Tool, ToolCallID: "committed", ToolName: "Write", Content: "completed write"}
			base := []*schema.Message{schema.UserMessage("fixture"), committed, completed}
			history := newConversationHistory(base)
			pending := &schema.Message{Role: schema.Assistant, Content: "uncommitted text"}
			if kind != "text" {
				pending.Content = ""
				pending.ToolCalls = []schema.ToolCall{{ID: "pending", Type: "function", Function: schema.FunctionCall{Name: "Write", Arguments: `{}`}}}
			}
			failure := errors.New("fixture stream failure")
			var presentedAssistant, presentedRejectedResults, checkpoints int
			result := runCanonicalModelRound(t.Context(), canonicalModelRoundInput{
				params: QueryParams{ChatModel: &canonicalScriptModel{}, QuerySource: QuerySourceSDK},
				deps: &QueryDeps{UUID: func() string { return "fixture-round" }, CallModel: func(
					context.Context, model.BaseChatModel, []*schema.Message, *schema.Message, []*schema.ToolInfo, execution.CallModelOptions,
				) (*execution.CallModelResult, error) {
					reader, writer := schema.Pipe[*schema.Message](2)
					writer.Send(pending, nil)
					if kind == "withheld" {
						writer.Send(&schema.Message{Role: schema.Assistant, Extra: map[string]any{"api_error": true, "error_type": "max_output_tokens"}}, nil)
					} else if kind != "success" {
						writer.Send(nil, failure)
					}
					writer.Close()
					t.Cleanup(reader.Close)
					return &execution.CallModelResult{StreamReader: reader}, nil
				}},
				messagesForQuery:       base,
				fullSystemPrompt:       schema.SystemMessage("fixture"),
				disableGenericFallback: true,
				yield: func(event QueryEvent) {
					if event.Type == EventAssistant {
						presentedAssistant++
					}
					if event.Type == EventToolResult {
						presentedRejectedResults++
					}
					history.Observe(event)
					if shouldCheckpointTranscript(event) {
						checkpoints++
						if kind != "withheld" {
							assertNoPendingRoundOutput(t, history.Messages())
						}
					}
				},
			})
			if presentedAssistant != 1 {
				t.Fatal("live assistant projection was removed")
			}
			if kind == "withheld" {
				// Valid truncation is an existing bounded continuation protocol,
				// not a hard stream/accounting failure. Preserve its recovery pair.
				final := history.Messages()
				if result.terminal != nil || result.toolCallsCommitted || result.withheldReason != "max_output_tokens" ||
					len(final) != len(base)+2 || final[len(base)+1].ToolCallID != "pending" ||
					final[len(base)+1].Extra["is_error"] != true || presentedRejectedResults != 1 {
					t.Fatal("valid truncation lost its existing continuation history")
				}
				return
			}
			if kind == "success" {
				if result.terminal != nil || !result.toolCallsCommitted || len(history.Messages()) != len(base)+1 || presentedRejectedResults != 0 {
					t.Fatal("successful model proposal was discarded")
				}
				return
			}
			if result.terminal == nil || result.terminal.Reason != TerminalModelError || !errors.Is(result.terminal.Err, failure) || checkpoints != 1 {
				t.Fatal("failure identity or diagnostic checkpoint was lost")
			}
			if kind == "tool" && presentedRejectedResults != 1 {
				t.Fatal("presentation lost the explicit rejected tool result")
			}
			final := history.Messages()
			assertNoPendingRoundOutput(t, final)
			if len(final) != len(base)+1 || final[1] != committed || final[2] != completed || final[len(base)].Extra["attachment_kind"] != "provider_stream_error" {
				t.Fatal("failed round removed completed work or its diagnostic")
			}
		})
	}
}

func assertNoPendingRoundOutput(t *testing.T, messages []*schema.Message) {
	t.Helper()
	for _, message := range messages {
		if message.Content == "uncommitted text" || message.ToolCallID == "pending" {
			t.Fatal("uncommitted model output entered a transcript checkpoint")
		}
		for _, call := range message.ToolCalls {
			if call.ID == "pending" {
				t.Fatal("uncommitted tool proposal entered a transcript checkpoint")
			}
		}
	}
}

func TestMediaRecoveryFailureKeepsDiagnosticSeparateFromPartialOutput(t *testing.T) {
	for _, attempt := range []mediaRecoveryAttempt{mediaRecoveryAttemptSelected, mediaRecoveryAttemptFallback} {
		t.Run(string(attempt), func(t *testing.T) {
			base := []*schema.Message{schema.UserMessage("fixture")}
			history := newConversationHistory(base)
			var diagnostics, checkpoints, identity int
			displayed := make(map[string]string)
			projector := newAssistantProjectionEmitter(nil, func() string {
				identity++
				return fmt.Sprintf("fixture-media-%d", identity)
			}, func(event QueryEvent) {
				if event.Type == EventCanonicalProjection && event.CanonicalProjection.Assistant != nil {
					message := event.CanonicalProjection.Assistant
					displayed[message.MessageID] += string(message.Delta)
				}
				if event.Type == EventAssistant && event.AssistantMessage != nil && event.AssistantMessage.Extra["api_error"] == true {
					diagnostics++
				}
				history.Observe(event)
				if shouldCheckpointTranscript(event) {
					checkpoints++
					assertNoPendingRoundOutput(t, history.Messages())
				}
			})
			failure := errors.New("fixture media stream failure")
			round := runCanonicalModelRound(t.Context(), canonicalModelRoundInput{
				params: QueryParams{ChatModel: &canonicalScriptModel{}, QuerySource: QuerySourceSDK},
				deps: &QueryDeps{UUID: func() string { return "fixture-media" }, CallModel: func(
					context.Context, model.BaseChatModel, []*schema.Message, *schema.Message, []*schema.ToolInfo, execution.CallModelOptions,
				) (*execution.CallModelResult, error) {
					reader, writer := schema.Pipe[*schema.Message](2)
					writer.Send(&schema.Message{Role: schema.Assistant, Content: "uncommitted text"}, nil)
					writer.Send(nil, failure)
					writer.Close()
					t.Cleanup(reader.Close)
					return &execution.CallModelResult{StreamReader: reader}, nil
				}},
				messagesForQuery:       base,
				fullSystemPrompt:       schema.SystemMessage("fixture"),
				disableGenericFallback: true,
				mediaRecoveryAttempt:   attempt,
				yield:                  projector.Emit,
			})
			if round.withheldReason != "media_recovery_failure" {
				t.Fatal("media round did not take its typed recovery-failure path")
			}
			outcome := handleActiveMediaRecoveryFailure(canonicalAfterModelInput{
				ctx: t.Context(), state: &QueryState{}, yield: projector.Emit,
			}, attempt)
			if projector.Err() != nil || outcome.terminal == nil || outcome.terminal.Reason != TerminalImageError ||
				diagnostics != 1 || checkpoints != 1 || len(displayed) != 2 || displayed["fixture-media-1"] != "uncommitted text" {
				t.Fatalf("media failure replaced partial display or lost its diagnostic: projection_error=%t diagnostics=%d checkpoints=%d",
					projector.Err() != nil, diagnostics, checkpoints)
			}
			final := history.Messages()
			assertNoPendingRoundOutput(t, final)
			if len(final) != len(base)+1 || final[len(base)].Extra["error_type"] != "media_size" || displayed["fixture-media-2"] != final[len(base)].Content {
				t.Fatal("media recovery retained failed partial text or lost its typed diagnostic")
			}
		})
	}
}
