package main

import (
	"encoding/json"
	"testing"
)

func TestHeadlessEnvelopeCurrentUsageExtension(t *testing.T) {
	for _, tc := range []struct {
		name  string
		usage string
		valid bool
	}{
		{"historical-without-usage", "", true},
		{"current", `{"provider_calls":3,"known_calls":3,"prompt_tokens":3,"completion_tokens":3,"total_tokens":6,"complete":true}`, true},
		{"incomplete", `{"complete":false}`, false},
		{"unknown", `{"complete":true,"unknown_calls":1}`, false},
		{"in-flight", `{"complete":true,"in_flight":1}`, false},
		{"token-total", `{"complete":true,"prompt_tokens":3,"completion_tokens":3,"total_tokens":5}`, false},
		{"cache-write", `{"complete":true,"known_calls":1,"prompt_tokens":8,"completion_tokens":2,"total_tokens":10,"cached_prompt_tokens":2,"cache_write_prompt_tokens":3,"cache_write_reported_calls":1}`, true},
		{"cache-write-zero", `{"complete":true,"known_calls":1,"cache_write_prompt_tokens":0,"cache_write_reported_calls":1}`, true},
		{"cache-write-overlap", `{"complete":true,"known_calls":1,"prompt_tokens":8,"total_tokens":8,"cached_prompt_tokens":2,"cache_write_prompt_tokens":7,"cache_write_reported_calls":1}`, false},
		{"cache-write-no-receipt", `{"complete":true,"known_calls":1,"prompt_tokens":8,"total_tokens":8,"cache_write_prompt_tokens":1}`, false},
		{"cache-write-too-many-receipts", `{"complete":true,"known_calls":1,"cache_write_reported_calls":2}`, false},
		{"cached-subset", `{"complete":true,"prompt_tokens":3,"total_tokens":3,"cached_prompt_tokens":4}`, false},
		{"reasoning-subset", `{"complete":true,"completion_tokens":3,"total_tokens":3,"reasoning_tokens":4}`, false},
		{"wrong-type", `{"complete":true,"provider_calls":"3"}`, false},
		{"negative", `{"complete":true,"prompt_tokens":-1}`, false},
		{"unknown-field", `{"complete":true,"invented_counter":1}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envelope := map[string]any{"schema_version": 1, "status": "completed", "output": "fixed", "exit_code": 0}
			if tc.usage != "" {
				envelope["usage"] = json.RawMessage(tc.usage)
			}
			data, err := json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateHeadlessEnvelope(data, "fixed"); (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}
