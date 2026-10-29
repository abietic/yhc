package agenticdeepseek

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestFailedResponseFinalUsageIsExplicitAndDetached(t *testing.T) {
	const response = `{"id":"failed","object":"response","status":"failed","model":"deepseek-flash","usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}`
	for _, tc := range []struct {
		name, body string
		known      bool
	}{
		{"explicit_zero", response, true},
		{"missing_id", strings.Replace(response, `"id":"failed",`, "", 1), false},
		{"wrong_object", strings.Replace(response, `"object":"response"`, `"object":"other"`, 1), false},
		{"wrong_status", strings.Replace(response, `"status":"failed"`, `"status":"completed"`, 1), false},
		{"missing_model", strings.Replace(response, `"model":"deepseek-flash",`, "", 1), false},
		{"null_counter", strings.Replace(response, `"input_tokens":0`, `"input_tokens":null`, 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var object responseObject
			if err := json.Unmarshal([]byte(tc.body), &object); err != nil {
				t.Fatal(err)
			}
			err := failedResponseError(&object)
			usage, model := err.FinalProviderUsage()
			if !tc.known {
				if usage != nil || model != "" {
					t.Fatalf("invalid envelope created a receipt: usage=%+v model=%q", usage, model)
				}
				return
			}
			if usage == nil || usage.TotalTokens != 0 || model != FlashModel {
				t.Fatalf("explicit zero usage lost: usage=%+v model=%q", usage, model)
			}
			usage.PromptTokens = 100
			again, _ := err.FinalProviderUsage()
			if again.PromptTokens != 0 {
				t.Fatal("receipt getter aliases provider error state")
			}
		})
	}
}

func TestMismatchedTerminalEventDoesNotSupplyFinalUsage(t *testing.T) {
	const wire = `event: response.completed
data: {"type":"response.completed","sequence_number":0,"response":{"id":"failed","object":"response","status":"failed","model":"deepseek-flash","usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}}

`
	err := parseResponseStream(strings.NewReader(wire), 4096, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("mismatched failed response lost failure: %v", err)
	}
	if usage, _ := apiErr.FinalProviderUsage(); usage != nil {
		t.Fatal("mismatched terminal event must not release a reservation")
	}
}
