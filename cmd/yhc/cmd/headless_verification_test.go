package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestExecIndependentVerificationRunsRealCheckAndSharesUsage(t *testing.T) {
	for _, tc := range []struct {
		name, review string
	}{
		{name: "default"},
		{name: "supported", review: `{"verdict":"SUPPORTED","missing":[]}`},
		{name: "insufficient", review: `{"verdict":"INSUFFICIENT","missing":["Required intermediate state was not observed."]}`},
	} {
		t.Run(tc.name, func(t *testing.T) { testExecVerificationCoverage(t, tc.name, tc.review) })
	}
}

func testExecVerificationCoverage(t *testing.T, name, review string) {
	prepareHeadlessJSONLProviderTest(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		call := calls.Add(1)
		text := "solver success is provisional"
		item := ""
		if call == 2 {
			if !bytes.Contains(body, []byte("You independently verify")) || bytes.Contains(body, []byte("solver success is provisional")) {
				t.Error("verification request contaminated by solver history")
			}
			// This command really executes via the production Bash tool. It proves the
			// test fixture output, not arbitrary task coverage or model competence.
			item = `{"type":"function_call","id":"verify-item","call_id":"verify-shell","name":"Bash","arguments":"{\"command\":\"printf independent-evidence\"}","status":"completed"}`
		}
		if call == 3 {
			if !bytes.Contains(body, []byte("independent-evidence")) {
				t.Error("real Bash output missing from verifier")
			}
			text = `{"verdict":"PASS","coverage_complete":true,"checks":[{"requirement":"print independent evidence","tool_call_id":"verify-shell","command":"printf independent-evidence","expected":"independent-evidence","observed":"independent-evidence","status":"PASS"}]}`
		}
		if call == 4 {
			if review == "" || !bytes.Contains(body, []byte("Review whether")) || !bytes.Contains(body, []byte("independent-evidence")) || !bytes.Contains(body, []byte(`"tool_choice":"none"`)) || bytes.Contains(body, []byte("solver success is provisional")) {
				t.Error("coverage review did not receive bounded real evidence without tools or inherited solver history")
			}
			text = review
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if item != "" {
			_, _ = fmt.Fprintf(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":0,\"output_index\":0,\"item\":%s}\n\n", item)
		} else {
			encoded, _ := json.Marshal(text)
			item = fmt.Sprintf(`{"type":"message","id":"verify-final","role":"assistant","status":"completed","content":[{"type":"output_text","text":%s,"annotations":[]}]}`, encoded)
			_, _ = fmt.Fprintf(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":0,\"item_id\":\"verify-final\",\"output_index\":0,\"content_index\":0,\"delta\":%s}\n\n", encoded)
		}
		_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"verify-response\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"deepseek-v4-flash\",\"output\":[%s],\"usage\":{\"input_tokens\":10,\"output_tokens\":4,\"total_tokens\":14}}}\n\n", item)
	}))
	defer server.Close()
	var out, stderr bytes.Buffer
	cmd := newRootCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SetIn(bytes.NewReader(nil))
	expectedCalls, expectedVerdict := 3, "PASS"
	args := []string{"exec", "print independent evidence", "--output-format", "json", "--provider", "deepseek", "--model", "deepseek-v4-flash", "--base-url", server.URL, "--api-key", p430FakeKey, "--tools", "Bash", "--sandbox", "danger-full-access", "-y", "--max-provider-calls", "4", "--verification-turns", "2"}
	if review != "" {
		args = append(args, "--verification-coverage-review")
		expectedCalls = 4
	}
	if name == "insufficient" {
		expectedVerdict = "PARTIAL"
	}
	cmd.SetArgs(args)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	if err := cmd.ExecuteContext(ctx); (err != nil) != (name == "insufficient") {
		t.Fatalf("err=%v stderr=%s stdout=%s", err, stderr.String(), out.String())
	}
	var result headlessEnvelope
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Verification) != 1 || result.Verification[0].Verdict != expectedVerdict || result.Verification[0].Checks != 1 {
		t.Fatalf("missing verification summary: %+v", result.Verification)
	}
	expectedStatus := "completed"
	if name == "insufficient" {
		expectedStatus = "failed"
	}
	if calls.Load() != int32(expectedCalls) || result.Status != expectedStatus || result.Usage == nil || result.Usage.ProviderCalls != uint64(expectedCalls) || result.Usage.TotalTokens != uint64(14*expectedCalls) {
		t.Fatalf("calls=%d result=%+v", calls.Load(), result)
	}
	found := false
	for _, route := range result.Usage.Routes {
		if route.Source == "independent_verification" && route.ProviderCalls == 2 {
			found = true
		}
	}
	if !found {
		t.Fatal("verification usage missing from shared report")
	}
	if review != "" {
		found = false
		for _, route := range result.Usage.Routes {
			found = found || (route.Source == "independent_verification_coverage" && route.ProviderCalls == 1)
		}
		if !found || result.Verification[0].CoverageReviews != 1 {
			t.Fatal("coverage review not separately counted")
		}
	}
}

func TestExecIndependentVerificationRejectsUnboundedOptionsBeforeProvider(t *testing.T) {
	for _, flags := range [][]string{{"--resume-verification"}, {"--resume-verification", "--verification-turns", "2", "--max-provider-calls", "4"}, {"--verification-turns", "2"}, {"--verification-repairs", "1"}, {"--verification-coverage-review"}, {"--verification-turns", "33", "--max-provider-calls", "4"}} {
		var out, stderr bytes.Buffer
		cmd := newRootCommand()
		cmd.SetOut(&out)
		cmd.SetErr(&stderr)
		cmd.SetIn(strings.NewReader(""))
		cmd.SetArgs(append([]string{"exec", "check", "--output-format", "json"}, flags...))
		if err := cmd.ExecuteContext(t.Context()); err == nil {
			t.Fatal("invalid verification options succeeded")
		}
		var result headlessEnvelope
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.ExitCode != ExitUsage || result.Error == nil || result.Error.Code != "usage_error" {
			t.Fatalf("result=%+v", result)
		}
	}
}
