package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestExecVerificationBudgetResumeRetainsRepairLimitAndRequirements(t *testing.T) {
	for _, mode := range []string{"repair", "coverage-review", "supplemental-check", "invalid-review"} {
		t.Run(mode, func(t *testing.T) { testExecVerificationBudgetResume(t, mode) })
	}
}

func testExecVerificationBudgetResume(t *testing.T, mode string) {
	recovery := mode == "invalid-review"
	coverage := mode == "coverage-review" || recovery
	supplemental := mode == "supplemental-check" || recovery
	prepareHeadlessJSONLProviderTest(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		n := calls.Add(1)
		if bytes.Contains(body, []byte("CONTINUATION_CONTROL")) {
			t.Error("caller control text reached resumed solver or checker")
		}
		checker := n == 2 || n == 3 || n == 5 || n == 6
		if coverage || supplemental {
			checker = n >= 2
		}
		if checker && (!bytes.Contains(body, []byte("ORIGINAL_REQUIREMENT")) || bytes.Contains(body, []byte("CONTINUATION_CONTROL"))) {
			t.Error("checker requirements changed on continuation")
		}
		text := "solver completion"
		item := ""
		if n == 2 || (!coverage && !supplemental && n == 5) || ((coverage || supplemental) && !recovery && n == 4) || (recovery && n == 5) {
			item = fmt.Sprintf(`{"type":"function_call","id":"item-%d","call_id":"check-%d","name":"Bash","arguments":"{\"command\":\"printf counterexample\"}","status":"completed"}`, n, n)
		}
		if !coverage && !supplemental && (n == 3 || n == 6) {
			text = fmt.Sprintf(`{"verdict":"FAIL","coverage_complete":true,"checks":[{"requirement":"ORIGINAL_REQUIREMENT","tool_call_id":"check-%d","expected":"correct","observed":"counterexample","status":"FAIL"}]}`, n-1)
		}
		if supplemental && !recovery && (n == 3 || n == 5) {
			text = `{"verdict":"PARTIAL","coverage_complete":false,"checks":[],"missing":["intermediate state not observed"]}`
		}
		if supplemental && ((!recovery && n == 4) || (recovery && n == 5)) && (!bytes.Contains(body, []byte("Historical verification planning")) || bytes.Contains(body, []byte("solver completion"))) {
			t.Error("resumed supplement lost planning or replayed solver")
		}
		if coverage && (n == 3 || (!recovery && n == 5) || (recovery && n == 6)) {
			text = fmt.Sprintf(`{"verdict":"PASS","coverage_complete":true,"checks":[{"requirement":"ORIGINAL_REQUIREMENT","tool_call_id":"check-%d","expected":"counterexample","observed":"counterexample","status":"PASS"}]}`, n-1)
		}
		if coverage && ((!recovery && n == 6) || (recovery && n == 7)) {
			freshID := "check-4"
			if recovery {
				freshID = "check-5"
			}
			if !bytes.Contains(body, []byte("Review whether")) || !bytes.Contains(body, []byte(freshID)) || bytes.Contains(body, []byte("check-2")) || !bytes.Contains(body, []byte(`"tool_choice":"none"`)) {
				t.Error("resumed coverage review reused old receipts or lost tool-free policy")
			}
			text = `{"verdict":"SUPPORTED","missing":[]}`
		}
		usage := `{"input_tokens":10,"output_tokens":4,"total_tokens":14}`
		if recovery && n == 4 {
			// Native Responses usage counts thinking inside output tokens even
			// when no visible report is returned. This is a local HTTP fixture.
			text = ""
			usage = `{"input_tokens":10,"output_tokens":16384,"total_tokens":16394,"output_tokens_details":{"reasoning_tokens":16384}}`
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if item != "" {
			_, _ = fmt.Fprintf(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":0,\"output_index\":0,\"item\":%s}\n\n", item)
		} else {
			encoded, _ := json.Marshal(text)
			item = fmt.Sprintf(`{"type":"message","id":"msg","role":"assistant","status":"completed","content":[{"type":"output_text","text":%s,"annotations":[]}]}`, encoded)
			_, _ = fmt.Fprintf(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":0,\"item_id\":\"msg\",\"output_index\":0,\"content_index\":0,\"delta\":%s}\n\n", encoded)
		}
		_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"resp\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"deepseek-v4-flash\",\"output\":[%s],\"usage\":%s}}\n\n", item, usage)
	}))
	defer server.Close()
	run := func(prompt, session string) headlessEnvelope {
		t.Helper()
		var out, stderr bytes.Buffer
		c := newRootCommand()
		c.SetOut(&out)
		c.SetErr(&stderr)
		c.SetIn(strings.NewReader(""))
		args := []string{"exec", prompt, "--output-format", "json", "--provider", "deepseek", "--model", "deepseek-v4-flash", "--base-url", server.URL, "--api-key", p430FakeKey, "--tools", "Bash", "--sandbox", "danger-full-access", "-y", "--max-provider-calls", "3", "--verification-turns", "2", "--verification-repairs", "1"}
		if recovery {
			args = append(args, "--max-provider-calls", "4")
		}
		if coverage {
			args = append(args, "--verification-coverage-review")
		}
		if supplemental {
			args = append(args, "--verification-coverage-checks", "1")
		}
		if session != "" {
			args = append(args, "--resume", session, "--resume-verification")
		}
		c.SetArgs(args)
		_ = c.ExecuteContext(t.Context())
		var result headlessEnvelope
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatalf("decode result: %v", err)
		}
		return result
	}
	first := run("ORIGINAL_REQUIREMENT", "")
	if first.TerminalReason != "run_budget_exceeded" || ((!coverage || recovery) && (len(first.Verification) != 1 || first.Verification[0].Attempt != 1)) || (coverage && !recovery && len(first.Verification) != 0) {
		t.Fatalf("first=%+v", first)
	}
	second := run("CONTINUATION_CONTROL", first.SessionID)
	if !coverage && (len(second.Verification) != 1 || second.Verification[0].Attempt != 2 || second.Error == nil || second.Error.Code != "independent_verification_failed") {
		t.Fatalf("repair cap reset across continuation: %+v", second)
	}
	wantAttempt := 1
	if recovery {
		wantAttempt = 2
	}
	if coverage && (second.Status != "completed" || second.Error != nil || len(second.Verification) != 1 || second.Verification[0].Attempt != wantAttempt || second.Verification[0].CoverageReviews != 1) {
		t.Fatalf("coverage pause lost original stage or consumed a repair: %+v", second)
	}
	wantCalls := int32(6)
	if supplemental {
		wantCalls = 5
		if recovery {
			wantCalls = 7
			if first.Verification[0].Verdict != "PARTIAL" || first.Verification[0].CoverageVerdict != "UNAVAILABLE" || first.Verification[0].CoverageIssue != "empty_output" || first.Usage.ReasoningTokens != 16384 {
				t.Fatalf("invalid review lost diagnostic or thinking usage: %+v", first)
			}
		}
		if first.Verification[0].CoverageChecks != 0 || second.Verification[0].CoverageChecks != 1 {
			t.Fatalf("supplemental allowance lost/reset: first=%+v second=%+v", first, second)
		}
		third := run("CONTINUATION_CONTROL", first.SessionID)
		if third.Status != "failed" || third.Usage == nil || third.Usage.ProviderCalls != 0 {
			t.Fatalf("exhausted supplement authorized more dispatch: %+v", third)
		}
	}
	wantTokens := uint64(14 * wantCalls)
	if recovery {
		wantTokens += 16384 - 4
	}
	if calls.Load() != wantCalls || first.Usage == nil || second.Usage == nil || first.Usage.TotalTokens+second.Usage.TotalTokens != wantTokens {
		t.Fatal("continuation usage or dispatch count changed")
	}
}
