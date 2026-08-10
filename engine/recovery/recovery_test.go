package recovery

import (
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestTryPTLRecoveryCascade(t *testing.T) {
	messages := []*schema.Message{{Role: schema.User, Content: "large prompt"}}

	t.Run("collapse drain retry", func(t *testing.T) {
		result := TryPTLRecovery(nil, messages, "sdk", false, "",
			func(got []*schema.Message, source string) *DrainResultT {
				if source != "sdk" {
					t.Fatalf("unexpected query source %q", source)
				}
				if len(got) != len(messages) {
					t.Fatalf("unexpected messages passed to drain: %#v", got)
				}
				return &DrainResultT{Committed: 1, Messages: got}
			},
			nil,
		)

		if result == nil || !result.Retry || !result.Continue || result.Terminal {
			t.Fatalf("expected retry continuation, got %#v", result)
			return
		}
		if result.Reason != string(ReasonCollapseDrainRetry) {
			t.Fatalf("expected collapse retry reason, got %q", result.Reason)
		}
	})

	t.Run("reactive compact retry after collapse was already attempted", func(t *testing.T) {
		drainCalled := false
		result := TryPTLRecovery(nil, messages, "sdk", false, ReasonCollapseDrainRetry,
			func(got []*schema.Message, source string) *DrainResultT {
				drainCalled = true
				return &DrainResultT{Committed: 1, Messages: got}
			},
			func(got []*schema.Message, source string) []*schema.Message {
				if source != "sdk" {
					t.Fatalf("unexpected query source %q", source)
				}
				return append([]*schema.Message(nil), got...)
			},
		)

		if drainCalled {
			t.Fatal("did not expect collapse drain after collapse retry transition")
		}
		if result == nil || !result.Retry || !result.Continue || result.Terminal {
			t.Fatalf("expected reactive compact retry, got %#v", result)
			return
		}
		if result.Reason != string(ReasonReactiveCompactRetry) {
			t.Fatalf("expected reactive compact retry reason, got %q", result.Reason)
		}
	})

	t.Run("terminal when both recovery strategies are exhausted", func(t *testing.T) {
		result := TryPTLRecovery(nil, messages, "sdk", true, ReasonCollapseDrainRetry,
			nil,
			func(got []*schema.Message, source string) []*schema.Message {
				t.Fatal("reactive compact should not be called after it was already attempted")
				return got
			},
		)

		if result == nil || !result.Terminal || result.Retry || result.Continue {
			t.Fatalf("expected terminal PTL result, got %#v", result)
			return
		}
		if result.Reason != "prompt_too_long" {
			t.Fatalf("expected prompt_too_long reason, got %q", result.Reason)
		}
	})
}

func TestTryPTLRecoveryCollapseCallbackContract(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "first"},
		{Role: schema.User, Content: "second"},
	}
	transformed := []*schema.Message{
		{Role: schema.User, Content: "collapsed"},
	}

	var collapseCalls int
	var reactiveCalls int

	result := TryPTLRecovery(nil, messages, "sdk", false, "",
		func(got []*schema.Message, source string) *DrainResultT {
			collapseCalls++
			if source != "sdk" {
				t.Fatalf("unexpected query source %q", source)
			}
			if len(got) != len(messages) {
				t.Fatalf("unexpected messages passed to collapse: got %d, want %d", len(got), len(messages))
			}
			for i := range got {
				if got[i].Content != messages[i].Content {
					t.Fatalf("message %d content mismatch: got %q, want %q", i, got[i].Content, messages[i].Content)
				}
			}
			return &DrainResultT{Committed: 1, Messages: transformed}
		},
		func(got []*schema.Message, source string) []*schema.Message {
			reactiveCalls++
			return nil
		},
	)

	if collapseCalls != 1 {
		t.Fatalf("expected collapse callback called once, got %d", collapseCalls)
	}
	if reactiveCalls != 0 {
		t.Fatalf("expected reactive callback not called, got %d", reactiveCalls)
	}
	if result == nil || !result.Retry || !result.Continue || result.Terminal {
		t.Fatalf("expected retry continuation, got %#v", result)
	}
	if result.Reason != string(ReasonCollapseDrainRetry) {
		t.Fatalf("expected reason %q, got %q", ReasonCollapseDrainRetry, result.Reason)
	}
	if len(result.Messages) != len(transformed) {
		t.Fatalf("expected result.Messages length %d, got %d", len(transformed), len(result.Messages))
	}
	for i := range result.Messages {
		if result.Messages[i].Content != transformed[i].Content {
			t.Fatalf("result.Messages[%d] content mismatch: got %q, want %q", i, result.Messages[i].Content, transformed[i].Content)
		}
	}
}

func TestTryPTLRecoverySkipsCollapseWhenAlreadyAttempted(t *testing.T) {
	messages := []*schema.Message{{Role: schema.User, Content: "large prompt"}}
	compacted := []*schema.Message{{Role: schema.User, Content: "summarized"}}

	var collapseCalls int
	var reactiveCalls int

	result := TryPTLRecovery(nil, messages, "sdk", false, ReasonCollapseDrainRetry,
		func(got []*schema.Message, source string) *DrainResultT {
			collapseCalls++
			return &DrainResultT{Committed: 1, Messages: got}
		},
		func(got []*schema.Message, source string) []*schema.Message {
			reactiveCalls++
			if source != "sdk" {
				t.Fatalf("unexpected query source %q", source)
			}
			if len(got) != len(messages) {
				t.Fatalf("unexpected messages passed to reactive: got %d, want %d", len(got), len(messages))
			}
			return compacted
		},
	)

	if collapseCalls != 0 {
		t.Fatalf("expected collapse callback skipped, got %d calls", collapseCalls)
	}
	if reactiveCalls != 1 {
		t.Fatalf("expected reactive callback called once, got %d", reactiveCalls)
	}
	if result == nil || !result.Retry || !result.Continue || result.Terminal {
		t.Fatalf("expected reactive compact retry, got %#v", result)
	}
	if result.Reason != string(ReasonReactiveCompactRetry) {
		t.Fatalf("expected reason %q, got %q", ReasonReactiveCompactRetry, result.Reason)
	}
	if len(result.Messages) != len(compacted) || result.Messages[0].Content != compacted[0].Content {
		t.Fatalf("expected result.Messages to match compacted slice, got %#v", result.Messages)
	}
}

func TestTryPTLRecoveryEmptyTransformsTerminal(t *testing.T) {
	messages := []*schema.Message{{Role: schema.User, Content: "large prompt"}}

	t.Run("collapse reports progress without messages", func(t *testing.T) {
		result := TryPTLRecovery(nil, messages, "sdk", true, "",
			func([]*schema.Message, string) *DrainResultT {
				return &DrainResultT{Committed: 1}
			},
			nil,
		)
		if result == nil || !result.Terminal || result.Retry || result.Continue {
			t.Fatalf("expected terminal result for empty collapse messages, got %#v", result)
		}
	})

	t.Run("reactive returns nil after collapse skipped", func(t *testing.T) {
		result := TryPTLRecovery(nil, messages, "sdk", false, ReasonCollapseDrainRetry,
			nil,
			func(got []*schema.Message, source string) []*schema.Message {
				return nil
			},
		)
		if result == nil || !result.Terminal || result.Retry || result.Continue {
			t.Fatalf("expected terminal result for nil reactive messages, got %#v", result)
		}
		if result.Reason != "prompt_too_long" {
			t.Fatalf("expected reason prompt_too_long, got %q", result.Reason)
		}
	})

	t.Run("reactive returns empty after collapse skipped", func(t *testing.T) {
		result := TryPTLRecovery(nil, messages, "sdk", false, ReasonCollapseDrainRetry,
			nil,
			func(got []*schema.Message, source string) []*schema.Message {
				return []*schema.Message{}
			},
		)
		if result == nil || !result.Terminal || result.Retry || result.Continue {
			t.Fatalf("expected terminal result for empty reactive messages, got %#v", result)
		}
		if result.Reason != "prompt_too_long" {
			t.Fatalf("expected reason prompt_too_long, got %q", result.Reason)
		}
	})
}

func TestTryMaxTokensRecoveryStages(t *testing.T) {
	escalated := TryMaxTokensRecovery(0, nil, true)
	if escalated == nil || !escalated.Retry || !escalated.Continue || escalated.Terminal {
		t.Fatalf("expected max-token escalation retry, got %#v", escalated)
		return
	}
	if escalated.MaxOutputTokensOverride == nil || *escalated.MaxOutputTokensOverride != escalatedMaxTokens {
		t.Fatalf("expected 64k override, got %#v", escalated.MaxOutputTokensOverride)
		return
	}
	if escalated.RecoveryMessage != nil {
		t.Fatalf("did not expect recovery message during escalation, got %#v", escalated.RecoveryMessage)
		return
	}

	override := escalatedMaxTokens
	recovery := TryMaxTokensRecovery(1, &override, true)
	if recovery == nil || !recovery.Retry || !recovery.Continue || recovery.Terminal {
		t.Fatalf("expected recovery-message retry, got %#v", recovery)
		return
	}
	if recovery.RecoveryMessage == nil || recovery.RecoveryMessage.Role != schema.User {
		t.Fatalf("expected user recovery message, got %#v", recovery.RecoveryMessage)
		return
	}
	if recovery.RecoveryMessage.Extra == nil || recovery.RecoveryMessage.Extra["is_meta"] != true {
		t.Fatalf("expected recovery message to be marked meta, got %#v", recovery.RecoveryMessage.Extra)
		return
	}
	if !strings.Contains(recovery.RecoveryMessage.Content, "Output token limit hit") {
		t.Fatalf("unexpected recovery prompt %q", recovery.RecoveryMessage.Content)
	}

	terminal := TryMaxTokensRecovery(maxOutputTokensRecoveryLimit, &override, true)
	if terminal == nil || !terminal.Terminal || terminal.Reason != "model_error" {
		t.Fatalf("expected terminal model_error, got %#v", terminal)
		return
	}
}

func TestDetermineAndApplyRecovery(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.System, Content: "system"},
		{Role: schema.User, Content: "old user"},
		{Role: schema.Assistant, Content: "old assistant"},
		{Role: schema.User, Content: "new user"},
		{Role: schema.Assistant, Content: "new assistant"},
	}

	compact := DetermineRecovery(&RecoveryContext{
		Error:        errors.New("status 413: request too large"),
		StatusCode:   413,
		Messages:     messages,
		TokenCount:   10_000,
		AttemptCount: 0,
		MaxAttempts:  3,
	})
	if compact == nil || compact.Action != ActionCompact {
		t.Fatalf("expected compact decision for large 413, got %#v", compact)
		return
	}
	applied, err := ApplyRecovery(compact, messages)
	if err != nil {
		t.Fatalf("unexpected compact application error: %v", err)
		return
	}
	if len(applied) >= len(messages) {
		t.Fatalf("expected compact/truncate to reduce messages, got %d >= %d", len(applied), len(messages))
	}
	if applied[0].Role != schema.System {
		t.Fatalf("expected system message to be preserved, got %#v", applied[0])
	}

	skip := DetermineRecovery(&RecoveryContext{
		Error:        errors.New("invalid_request_error: messages.2.content tool_result invalid"),
		StatusCode:   400,
		AttemptCount: 0,
		MaxAttempts:  3,
	})
	if skip == nil || skip.Action != ActionSkip {
		t.Fatalf("expected skip decision for message-format 400, got %#v", skip)
		return
	}
	afterSkip, err := ApplyRecovery(skip, messages)
	if err != nil {
		t.Fatalf("unexpected skip application error: %v", err)
		return
	}
	if len(afterSkip) != len(messages)-1 || afterSkip[len(afterSkip)-1].Content != "new user" {
		t.Fatalf("expected last non-system message to be skipped, got %#v", afterSkip)
	}

	abort := DetermineRecovery(&RecoveryContext{
		Error:        errors.New("status 503: unavailable"),
		StatusCode:   503,
		AttemptCount: 3,
		MaxAttempts:  3,
	})
	if abort == nil || abort.Action != ActionAbort {
		t.Fatalf("expected max-attempt abort, got %#v", abort)
		return
	}
	if _, err := ApplyRecovery(abort, messages); err == nil {
		t.Fatal("expected abort recovery application to fail")
		return
	}
}

func TestEmergencyTruncatePreservesToolCallPairs(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.System, Content: "system"},
		{Role: schema.User, Content: "old user"},
		{Role: schema.Assistant, Content: "old assistant"},
		{Role: schema.Tool, ToolCallID: "orphaned-old", Content: "old tool result"},
		{Role: schema.User, Content: "new user"},
		{
			Role:    schema.Assistant,
			Content: "calling tool",
			ToolCalls: []schema.ToolCall{{
				ID:       "call-1",
				Type:     "function",
				Function: schema.FunctionCall{Name: "Read", Arguments: `{"file_path":"README.md"}`},
			}},
		},
		{Role: schema.Tool, ToolCallID: "call-1", ToolName: "Read", Content: "file content"},
	}

	truncated := EmergencyTruncate(messages, 0)
	if len(truncated) == 0 || truncated[0].Role != schema.System {
		t.Fatalf("expected leading system message to be preserved, got %#v", truncated)
	}
	for _, msg := range truncated {
		if msg.Role == schema.Tool && msg.ToolCallID == "orphaned-old" {
			t.Fatalf("expected orphaned old tool result to be removed, got %#v", truncated)
		}
	}

	assistantIdx := -1
	toolIdx := -1
	for i, msg := range truncated {
		if msg.Role == schema.Assistant && len(msg.ToolCalls) > 0 && msg.ToolCalls[0].ID == "call-1" {
			assistantIdx = i
		}
		if msg.Role == schema.Tool && msg.ToolCallID == "call-1" {
			toolIdx = i
		}
	}
	if assistantIdx < 0 || toolIdx < 0 {
		t.Fatalf("expected kept assistant tool call and result pair, got %#v", truncated)
	}
	if assistantIdx > toolIdx {
		t.Fatalf("expected assistant tool call before tool result, got assistant=%d tool=%d", assistantIdx, toolIdx)
	}
}
