package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestNormalizeAgentProgressBoundsRecentActivitiesAndPayload(t *testing.T) {
	activities := make([]ToolActivity, 8)
	for i := range activities {
		activities[i] = ToolActivity{
			ToolName:            "Tool" + string(rune('0'+i)),
			Input:               map[string]any{"value": i},
			ActivityDescription: strings.Repeat("activity ", 100),
		}
	}
	activities[len(activities)-1].Input = map[string]any{
		"payload": strings.Repeat("界", maxAgentActivityInputBytes),
	}

	progress := NormalizeAgentProgress(AgentProgress{
		ToolUseCount:     -1,
		TokenCount:       -1,
		RecentActivities: activities,
		Summary:          strings.Repeat("s", maxAgentProgressSummaryRunes+20),
	})

	if progress.ToolUseCount != 0 || progress.TokenCount != 0 {
		t.Fatalf("negative counters were not normalized: %#v", progress)
	}
	if len(progress.RecentActivities) != MaxAgentRecentActivities {
		t.Fatalf("recent activities = %d, want %d", len(progress.RecentActivities), MaxAgentRecentActivities)
	}
	if progress.RecentActivities[0].ToolName != "Tool3" || progress.LastActivity == nil || progress.LastActivity.ToolName != "Tool7" {
		t.Fatalf("unexpected bounded activity window: %#v", progress.RecentActivities)
	}
	if len([]rune(progress.Summary)) != maxAgentProgressSummaryRunes {
		t.Fatalf("summary runes = %d, want %d", len([]rune(progress.Summary)), maxAgentProgressSummaryRunes)
	}
	encoded, err := json.Marshal(progress.RecentActivities[len(progress.RecentActivities)-1].Input)
	if err != nil {
		t.Fatalf("marshal normalized input: %v", err)
	}
	if len(encoded) > maxAgentActivityInputBytes {
		t.Fatalf("normalized input bytes = %d, want <= %d", len(encoded), maxAgentActivityInputBytes)
	}
}

func TestNormalizeAgentProgressDeepCopiesNestedInput(t *testing.T) {
	nested := map[string]any{
		"items": []any{map[string]any{"value": "original"}},
	}
	progress := NormalizeAgentProgress(AgentProgress{
		RecentActivities: []ToolActivity{{ToolName: "Read", Input: nested}},
	})

	nestedItems := nested["items"].([]any)
	nestedItems[0].(map[string]any)["value"] = "mutated"
	normalizedItems := progress.RecentActivities[0].Input["items"].([]any)
	if got := normalizedItems[0].(map[string]any)["value"]; got != "original" {
		t.Fatalf("normalized nested input aliases caller data: %v", got)
	}

	cloned := cloneAgentProgress(progress)
	normalizedItems[0].(map[string]any)["value"] = "changed-again"
	clonedItems := cloned.RecentActivities[0].Input["items"].([]any)
	if got := clonedItems[0].(map[string]any)["value"]; got != "original" {
		t.Fatalf("cloned nested input aliases progress state: %v", got)
	}
}

func TestNormalizeAgentProgressBoundsInputKeyCardinality(t *testing.T) {
	input := make(map[string]any, maxAgentActivityInputKeys*4)
	for i := 0; i < maxAgentActivityInputKeys*4; i++ {
		input[fmt.Sprintf("key-%03d", i)] = i
	}
	progress := NormalizeAgentProgress(AgentProgress{
		RecentActivities: []ToolActivity{{ToolName: "Inspect", Input: input}},
	})
	if got := len(progress.RecentActivities[0].Input); got > maxAgentActivityInputKeys {
		t.Fatalf("normalized input keys = %d, want <= %d", got, maxAgentActivityInputKeys)
	}
}
