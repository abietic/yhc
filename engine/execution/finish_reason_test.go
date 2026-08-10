package execution

import (
	"context"
	"errors"
	"testing"
)

func TestClassifyStreamTerminal(t *testing.T) {
	t.Parallel()

	streamFailure := errors.New("stream failed")
	tests := []struct {
		name        string
		raw         string
		hasWithheld bool
		withheld    string
		cleanEOF    bool
		streamErr   error
		contextErr  error
		want        streamTerminalDisposition
		wantReason  string
		wantErr     bool
	}{
		{name: "empty clean eof", cleanEOF: true, want: streamTerminalCommit},
		{name: "stop", raw: "stop", cleanEOF: true, want: streamTerminalCommit, wantReason: "stop"},
		{name: "stop sequence", raw: "stop_sequence", cleanEOF: true, want: streamTerminalCommit, wantReason: "stop_sequence"},
		{name: "end turn", raw: "end_turn", cleanEOF: true, want: streamTerminalCommit, wantReason: "end_turn"},
		{name: "tool calls", raw: "tool_calls", cleanEOF: true, want: streamTerminalCommit, wantReason: "tool_calls"},
		{name: "tool use", raw: "tool_use", cleanEOF: true, want: streamTerminalCommit, wantReason: "tool_use"},
		{name: "length", raw: "length", cleanEOF: true, want: streamTerminalRejectTruncated, wantReason: "length"},
		{name: "max tokens", raw: "max_tokens", cleanEOF: true, want: streamTerminalRejectTruncated, wantReason: "max_tokens"},
		{name: "max output tokens", raw: "max_output_tokens", cleanEOF: true, want: streamTerminalRejectTruncated, wantReason: "max_output_tokens"},
		{name: "withheld max output wins", raw: "stop", hasWithheld: true, withheld: "max_output_tokens", cleanEOF: true, want: streamTerminalRejectTruncated, wantReason: "max_output_tokens"},
		{name: "withheld api error wins", raw: "stop", hasWithheld: true, withheld: "413", cleanEOF: true, want: streamTerminalModelError, wantReason: "413", wantErr: true},
		{name: "withheld missing type fails closed", hasWithheld: true, cleanEOF: true, want: streamTerminalModelError, wantReason: "api_error", wantErr: true},
		{name: "case and whitespace normalized", raw: " TOOL_CALLS ", cleanEOF: true, want: streamTerminalCommit, wantReason: "tool_calls"},
		{name: "unknown nonempty", raw: "content_filter", cleanEOF: true, want: streamTerminalModelError, wantReason: "content_filter", wantErr: true},
		{name: "stream error", raw: "stop", streamErr: streamFailure, want: streamTerminalModelError, wantErr: true},
		{name: "missing clean eof", raw: "stop", want: streamTerminalModelError, wantErr: true},
		{name: "context cancellation wins", raw: "stop", cleanEOF: true, streamErr: streamFailure, contextErr: context.Canceled, want: streamTerminalCancel, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := classifyStreamTerminal(tt.raw, tt.hasWithheld, tt.withheld, tt.cleanEOF, tt.streamErr, tt.contextErr)
			if got.Disposition != tt.want {
				t.Fatalf("disposition = %v, want %v", got.Disposition, tt.want)
			}
			if got.FinishReason != tt.wantReason {
				t.Fatalf("finish reason = %q, want %q", got.FinishReason, tt.wantReason)
			}
			if (got.Err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", got.Err, tt.wantErr)
			}
		})
	}
}
