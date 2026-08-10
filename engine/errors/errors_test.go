package errors

import (
	stderrors "errors"
	"strings"
	"testing"
	"time"
)

func TestAgentErrorFormattingAndUnwrap(t *testing.T) {
	cause := stderrors.New("root cause")
	err := NewToolError("Read", "failed", cause)

	if got := err.Error(); !strings.Contains(got, "[TOOL_ERROR] tool \"Read\": failed: root cause") {
		t.Fatalf("unexpected formatted error: %q", got)
	}
	if !stderrors.Is(err, cause) {
		t.Fatal("AgentError should unwrap cause")
	}
	var ae *AgentError
	if !stderrors.As(err, &ae) || ae.Code != CodeToolError {
		t.Fatalf("errors.As failed, got %#v", ae)
	}
}

func TestConstructorsSetCodesMessagesAndFlags(t *testing.T) {
	tests := []struct {
		name       string
		err        *AgentError
		code       string
		retryable  bool
		userFacing bool
		contains   string
	}{
		{"abort", NewAbortError("cancelled"), CodeAbort, false, true, "cancelled"},
		{"overflow", NewOverflowError("too big", 123), CodeOverflow, false, true, "tokens: 123"},
		{"shell", NewShellError("false", 1, "nope"), CodeShellError, false, true, "exited with code 1"},
		{"tool", NewToolError("Write", "bad", nil), CodeToolError, true, true, "tool \"Write\": bad"},
		{"permission", NewPermissionError("Bash", "denied"), CodePermission, false, true, "tool \"Bash\": denied"},
		{"rate", NewRateLimitError(2 * time.Second), CodeRateLimit, true, true, "retry after 2s"},
		{"network", NewNetworkError("offline", nil), CodeNetwork, true, true, "offline"},
		{"model-500", NewModelError("upstream", 500), CodeModel, true, true, "status 500"},
		{"model-400", NewModelError("bad request", 400), CodeModel, false, true, "status 400"},
		{"config", NewConfigError("missing key"), CodeConfig, false, true, "missing key"},
		{"session", NewSessionError("missing session"), CodeSession, false, true, "missing session"},
		{"max turns", NewMaxTurnsError(7), CodeMaxTurns, false, true, "maximum turns (7)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err.Code != tt.code {
				t.Fatalf("code = %q, want %q", tt.err.Code, tt.code)
			}
			if tt.err.Retryable != tt.retryable {
				t.Fatalf("retryable = %v, want %v", tt.err.Retryable, tt.retryable)
			}
			if tt.err.UserFacing != tt.userFacing {
				t.Fatalf("userFacing = %v, want %v", tt.err.UserFacing, tt.userFacing)
			}
			if !strings.Contains(tt.err.Error(), tt.contains) {
				t.Fatalf("error %q does not contain %q", tt.err.Error(), tt.contains)
			}
		})
	}
}

func TestClassificationHelpers(t *testing.T) {
	plain := stderrors.New("plain")
	if IsRetryable(plain) || IsUserFacing(plain) || IsAbort(plain) || IsOverflow(plain) || IsRateLimit(plain) || GetCode(plain) != "" {
		t.Fatal("plain errors should not match AgentError helpers")
	}

	rate := NewRateLimitError(time.Second)
	if !IsRetryable(rate) || !IsUserFacing(rate) || !IsRateLimit(rate) || GetCode(rate) != CodeRateLimit {
		t.Fatalf("rate limit classification failed")
	}
	if IsAbort(rate) || IsOverflow(rate) {
		t.Fatal("rate limit should not classify as abort/overflow")
	}

	if !IsAbort(NewAbortError("stop")) {
		t.Fatal("abort classification failed")
	}
	if !IsOverflow(NewOverflowError("large", 42)) {
		t.Fatal("overflow classification failed")
	}
}
