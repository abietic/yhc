package tools

import (
	"testing"
)

func TestValidateSendMessageInput(t *testing.T) {
	tests := []struct {
		name    string
		input   map[string]any
		wantErr string
	}{
		{
			name:    "empty to",
			input:   map[string]any{"to": "", "message": "hello"},
			wantErr: "'to' must not be empty",
		},
		{
			name:    "to contains @",
			input:   map[string]any{"to": "@worker", "message": "hello", "summary": "hi"},
			wantErr: "bare teammate name",
		},
		{
			name:    "empty uds target",
			input:   map[string]any{"to": "uds:", "message": "hello"},
			wantErr: "address target must not be empty",
		},
		{
			name:    "empty bridge target",
			input:   map[string]any{"to": "bridge:  ", "message": "hello"},
			wantErr: "address target must not be empty",
		},
		{
			name:    "string message missing summary",
			input:   map[string]any{"to": "worker", "message": "hello"},
			wantErr: "'summary' is required",
		},
		{
			name:  "string message with summary valid",
			input: map[string]any{"to": "worker", "message": "hello", "summary": "greeting"},
		},
		{
			name:  "broadcast does not require summary",
			input: map[string]any{"to": "*", "message": "hello all"},
		},
		{
			name:  "uds does not require summary",
			input: map[string]any{"to": "uds:/tmp/sock", "message": "hello"},
		},
		{
			name:  "bridge does not require summary",
			input: map[string]any{"to": "bridge:sess-123", "message": "hello"},
		},
		{
			name:    "structured broadcast rejected",
			input:   map[string]any{"to": "*", "message": map[string]any{"type": "shutdown_request"}},
			wantErr: "cannot be broadcast",
		},
		{
			name:    "structured cross-session rejected",
			input:   map[string]any{"to": "bridge:x", "message": map[string]any{"type": "shutdown_request"}},
			wantErr: "cannot be sent cross-session",
		},
		{
			name:    "shutdown_response wrong recipient",
			input:   map[string]any{"to": "worker", "message": map[string]any{"type": "shutdown_response", "request_id": "r1", "approve": true}},
			wantErr: "shutdown_response must be sent to",
		},
		{
			name:    "shutdown rejection needs reason",
			input:   map[string]any{"to": "team-lead", "message": map[string]any{"type": "shutdown_response", "request_id": "r1", "approve": false}},
			wantErr: "'reason' is required when rejecting",
		},
		{
			name:  "shutdown approval valid",
			input: map[string]any{"to": "team-lead", "message": map[string]any{"type": "shutdown_response", "request_id": "r1", "approve": true}},
		},
		{
			name:    "nil message",
			input:   map[string]any{"to": "worker"},
			wantErr: "'message' is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSendMessageInput(tt.input)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			} else {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.wantErr)
				} else if !containsStr(err.Error(), tt.wantErr) {
					t.Errorf("error = %q, want containing %q", err.Error(), tt.wantErr)
				}
			}
		})
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && findSubstr(s, substr))
}

func findSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
