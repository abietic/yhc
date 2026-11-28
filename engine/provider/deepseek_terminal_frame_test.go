package provider

import (
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/engine/provider/agenticdeepseek"
	"github.com/cloudwego/eino/schema"
)

type fragmentedDeepSeekBody struct {
	*strings.Reader
	max int
}

func (r fragmentedDeepSeekBody) Read(p []byte) (int, error) {
	return r.Reader.Read(p[:min(len(p), r.max)])
}

func deepSeekTerminalFrameFixture() (string, string) {
	created := "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"object\":\"response\",\"id\":\"fixture-response\",\"status\":\"in_progress\",\"model\":\"deepseek-flash\"}}\n\n"
	start := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":1,\"output_index\":0,\"delta\":\"前🙂\"}\n\n"
	call := "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":2,\"output_index\":1,\"item\":{\"type\":\"function_call\",\"id\":\"fc-1\",\"call_id\":\"call-1\",\"name\":\"fixture\",\"arguments\":\"\"}}\n\n"
	arg1 := "event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"sequence_number\":3,\"item_id\":\"fc-1\",\"output_index\":1,\"delta\":\"{\\\"q\\\":\"}\n\n"
	arg2 := "event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"sequence_number\":4,\"item_id\":\"fc-1\",\"output_index\":1,\"delta\":\"\\\"值\\\"}\"}\n\n"
	end := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":5,\"output_index\":2,\"delta\":\"后\"}\n\n"
	terminal := "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":6,\"response\":{\"object\":\"response\",\"id\":\"fixture-response\",\"status\":\"completed\",\"model\":\"deepseek-flash\",\"output\":[{\"type\":\"function_call\",\"id\":\"fc-1\",\"call_id\":\"call-1\",\"name\":\"fixture\",\"status\":\"completed\",\"arguments\":\"{\\\"q\\\":\\\"值\\\"}\"}],\"usage\":{\"input_tokens\":9,\"input_tokens_details\":{\"cached_tokens\":4},\"output_tokens\":5,\"output_tokens_details\":{\"reasoning_tokens\":2},\"total_tokens\":14}}}"
	return created + start + call + arg1 + ": keep-alive\n\n" + arg2 + end, terminal
}

func TestDeepSeekStreamCommitsOnlyFramedTerminal(t *testing.T) {
	body, terminal := deepSeekTerminalFrameFixture()
	for _, tc := range []struct {
		name           string
		eol            string
		terminalSuffix string
		readSize       int
		dropJSONBytes  int
		wantComplete   bool
	}{
		{"all-frames-one-read", "\n", "\n\n", 1 << 16, 0, true},
		{"each-byte-separately", "\n", "\n\n", 1, 0, true},
		{"split-crlf", "\r\n", "\n\n", 1, 0, true},
		{"eof-within-terminal-json", "\n", "", 7, 3, false},
		{"eof-after-json-no-empty-line", "\n", "\n", 7, 0, false},
		{"eof-after-crlf-no-empty-line", "\r\n", "\n", 1, 0, false},
		{"eof-json-end-no-line-ending", "\n", "", 7, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := terminal[:len(terminal)-tc.dropJSONBytes]
			wire := strings.ReplaceAll(body+response+tc.terminalSuffix, "\n", tc.eol)
			var requests atomic.Int32
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				requests.Add(1)
				if req.Method != "POST" || req.URL.Path != "/responses" {
					return nil, errors.New("unexpected fixture request")
				}
				_ = req.Body.Close()
				return &http.Response{
					StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
					Body: io.NopCloser(fragmentedDeepSeekBody{Reader: strings.NewReader(wire), max: tc.readSize}),
				}, nil
			})
			inner, err := agenticdeepseek.New(t.Context(), &agenticdeepseek.Config{
				APIKey: "fixture", BaseURL: "http://fixture.invalid",
				Model: agenticdeepseek.FlashModel, HTTPClient: &http.Client{Transport: transport},
			})
			if err != nil {
				t.Fatal(err)
			}
			model := wrapAgenticModel(inner)
			stream, err := model.Stream(t.Context(), []*schema.Message{schema.UserMessage("fixture")})
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			var executed atomic.Int32
			executor := execution.NewStreamingToolExecutor(execution.StreamingToolExecutorConfig{
				Ctx: t.Context(), Execute: func(call *schema.ToolCall) *execution.ToolResult {
					executed.Add(1)
					return &execution.ToolResult{ToolCallID: call.ID, ToolName: call.Function.Name, Result: "ok"}
				},
			})
			defer executor.Discard()
			var got []string
			result, err := execution.ProcessStream(t.Context(), stream, executor, func(event execution.QueryEvent) {
				if event.Type != "assistant" {
					return
				}
				message := event.Message
				if message.Content != "" {
					got = append(got, "text:"+message.Content)
				}
				for _, tool := range message.ToolCalls {
					got = append(got, "tool:"+tool.ID+":"+tool.Function.Arguments)
				}
				if message.ResponseMeta != nil && message.ResponseMeta.Usage != nil {
					got = append(got, "usage")
				}
				if executed.Load() != 0 {
					t.Fatal("fixture tool executed before terminal commit")
				}
			})
			executor.GetRemainingResults(false)
			if requests.Load() != 1 {
				t.Fatalf("fixture requests = %d", requests.Load())
			}
			expect := []string{"text:前🙂", "tool:call-1:{\"q\":", "tool:call-1:{\"q\":\"值\"}", "text:后"}
			if tc.wantComplete {
				expect = append(expect, "usage")
				if err != nil || result == nil || !result.ToolCallsCommitted || result.Usage == nil || result.Usage.TotalTokens != 14 || executed.Load() != 1 {
					t.Fatalf("fully framed terminal: result=%+v err=%v executions=%d", result, err, executed.Load())
				}
				if len(result.ToolUseBlocks) != 1 || result.ToolUseBlocks[0].Function.Arguments != "{\"q\":\"值\"}" {
					t.Fatal("incorrect merged tool arguments")
				}
			} else {
				var protocol *agenticdeepseek.ProtocolError
				if executed.Load() != 0 || result != nil || !errors.As(err, &protocol) || protocol.ReasonCode != "stream_terminal_event_missing" {
					t.Fatalf("unframed terminal: result=%+v err=%v executions=%d", result, err, executed.Load())
				}
			}
			if !slices.Equal(got, expect) {
				t.Fatalf("event sequence = %q, want %q", got, expect)
			}
		})
	}
}
