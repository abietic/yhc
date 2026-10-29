package compact

import (
	"reflect"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestMicrocompactUsesModelPressure(t *testing.T) {
	t.Setenv("TIME_BASED_MC_ENABLED", "false")
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "")
	t.Setenv("CLAUDE_AUTOCOMPACT_PCT_OVERRIDE", "")
	history := freshRoundHistory()
	for _, tc := range []struct {
		model string
		trim  bool
	}{
		{model: "deepseek-flash"},
		{model: "gpt-4", trim: true},
	} {
		t.Run(tc.model, func(t *testing.T) {
			got := Microcompact(history, "sdk", tc.model)
			if (got.CompactionInfo != nil) != tc.trim {
				t.Fatalf("trimmed=%v want=%v", got.CompactionInfo != nil, tc.trim)
			}
			if !tc.trim && !reflect.DeepEqual(got.Messages, history) {
				t.Fatal("healthy history changed")
			}
			if tc.trim && got.Messages[1].Content == history[1].Content {
				t.Fatal("consumed tool result was not trimmed under pressure")
			}
			for i := 2; i < len(history); i++ {
				if !reflect.DeepEqual(got.Messages[i], history[i]) {
					t.Fatalf("unconsumed round message %d changed", i)
				}
			}
		})
	}
}

func TestMicrocompactPreservesIdleAndCompactQueryPolicies(t *testing.T) {
	t.Setenv("TIME_BASED_MC_ENABLED", "true")
	t.Setenv("TIME_BASED_MC_GAP_MINUTES", "1")
	t.Setenv("TIME_BASED_MC_KEEP_RECENT", "1")
	history := freshRoundHistory()
	got := Microcompact(history, "main", "deepseek-flash")
	if got.CompactionInfo == nil || got.CompactionInfo.Trigger != "time_based" || got.Messages[1].Content != timeBasedMCClearedMessage {
		t.Fatal("idle-time policy did not clear consumed history")
	}
	for i := 2; i < len(history); i++ {
		if !reflect.DeepEqual(got.Messages[i], history[i]) {
			t.Fatalf("idle-time clearing changed unconsumed round message %d", i)
		}
	}
	if got := Microcompact(history, "compact", "gpt-4"); got.CompactionInfo != nil || !reflect.DeepEqual(got.Messages, history) {
		t.Fatal("compact queries must not enter either microcompaction policy")
	}
}

func TestMicrocompactHonorsThresholdOverride(t *testing.T) {
	t.Setenv("TIME_BASED_MC_ENABLED", "false")
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "")
	t.Setenv("CLAUDE_AUTOCOMPACT_PCT_OVERRIDE", "0.1")
	got := Microcompact(freshRoundHistory(), "sdk", "deepseek-flash")
	if got.CompactionInfo == nil || got.CompactionInfo.Trigger != "pre_auto_compact" {
		t.Fatal("explicit early compaction threshold was ignored")
	}
}

func TestMicrocompactStopsAfterSufficientToolSavings(t *testing.T) {
	t.Setenv("TIME_BASED_MC_ENABLED", "false")
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "20000")
	t.Setenv("CLAUDE_AUTOCOMPACT_PCT_OVERRIDE", "")
	reasoning := strings.Repeat("reasoning step\n", 1000)
	output := strings.Repeat("fixture record\n", 1200)
	history := []*schema.Message{
		{Role: schema.Assistant, ReasoningContent: reasoning, ToolCalls: []schema.ToolCall{{ID: "old", Type: "function", Function: schema.FunctionCall{Name: "Read", Arguments: `{}`}}}},
		{Role: schema.Tool, ToolCallID: "old", Content: output},
		{Role: schema.Assistant, Content: "Consumed."},
	}
	got := Microcompact(history, "sdk", "deepseek-flash")
	if got.CompactionInfo == nil || got.Messages[1].Content == output {
		t.Fatal("tool output did not relieve pressure")
	}
	if got.Messages[0].ReasoningContent != reasoning {
		t.Fatal("reasoning was trimmed after tool savings already relieved pressure")
	}
}
