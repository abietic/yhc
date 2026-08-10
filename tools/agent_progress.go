package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	// MaxAgentRecentActivities mirrors the reference local-agent progress window.
	MaxAgentRecentActivities = 5

	maxAgentActivityInputBytes       = 8 * 1024
	maxAgentActivityInputKeys        = 64
	maxAgentActivityContainerEntries = 64
	maxAgentActivityInputDepth       = 8
	maxAgentActivityDescriptionRunes = 320
	maxAgentProgressSummaryRunes     = 512
	maxAgentToolNameRunes            = 128
)

// NormalizeAgentProgress applies the bounded runtime contract shared by the
// runner, runtime projections, and TUI consumers.
func NormalizeAgentProgress(progress AgentProgress) AgentProgress {
	out := AgentProgress{
		ToolUseCount:    max(progress.ToolUseCount, 0),
		TokenCount:      max(progress.TokenCount, 0),
		Summary:         truncateAgentProgressString(strings.TrimSpace(progress.Summary), maxAgentProgressSummaryRunes),
		ActivitySummary: truncateAgentProgressString(strings.TrimSpace(progress.ActivitySummary), maxAgentProgressSummaryRunes),
	}

	recent := progress.RecentActivities
	if len(recent) > MaxAgentRecentActivities {
		recent = recent[len(recent)-MaxAgentRecentActivities:]
	}
	if len(recent) > 0 {
		out.RecentActivities = make([]ToolActivity, len(recent))
		for i, activity := range recent {
			out.RecentActivities[i] = normalizeToolActivity(activity)
		}
		last := cloneToolActivity(out.RecentActivities[len(out.RecentActivities)-1])
		out.LastActivity = &last
	} else if progress.LastActivity != nil {
		last := normalizeToolActivity(*progress.LastActivity)
		out.LastActivity = &last
	}

	return out
}

func normalizeToolActivity(activity ToolActivity) ToolActivity {
	return ToolActivity{
		ToolName:            truncateAgentProgressString(strings.TrimSpace(activity.ToolName), maxAgentToolNameRunes),
		Input:               normalizeAgentActivityInput(activity.Input),
		ActivityDescription: truncateAgentProgressString(strings.TrimSpace(activity.ActivityDescription), maxAgentActivityDescriptionRunes),
		IsSearch:            activity.IsSearch,
		IsRead:              activity.IsRead,
	}
}

func normalizeAgentActivityInput(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}

	keys := boundedAgentProgressKeys(input)

	out := make(map[string]any, len(input))
	for _, key := range keys {
		boundedKey := truncateAgentProgressString(key, maxAgentToolNameRunes)
		value := cloneAgentProgressValue(input[key])
		out[boundedKey] = value
		encoded, err := json.Marshal(out)
		if err == nil && len(encoded) <= maxAgentActivityInputBytes {
			continue
		}

		delete(out, boundedKey)
		valueJSON, valueErr := json.Marshal(value)
		var valueText string
		if text, ok := value.(string); ok {
			valueText = text
		} else if valueErr == nil {
			valueText = string(valueJSON)
		} else {
			valueText = fmt.Sprint(value)
		}

		low, high := 0, len(valueText)
		best := ""
		for low <= high {
			mid := low + (high-low)/2
			candidate := truncateAgentProgressBytes(valueText, mid)
			out[boundedKey] = candidate
			candidateJSON, marshalErr := json.Marshal(out)
			if marshalErr == nil && len(candidateJSON) <= maxAgentActivityInputBytes {
				best = candidate
				low = mid + 1
			} else {
				high = mid - 1
			}
			delete(out, boundedKey)
		}
		if best == "" {
			break
		}
		out[boundedKey] = best
		encoded, err = json.Marshal(out)
		if err != nil {
			delete(out, boundedKey)
			break
		}
		if len(encoded) >= maxAgentActivityInputBytes {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func truncateAgentProgressBytes(value string, maxBytes int) string {
	if maxBytes <= 0 || value == "" {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	for maxBytes > 0 && (value[maxBytes]&0xc0) == 0x80 {
		maxBytes--
	}
	return value[:maxBytes]
}

func cloneAgentProgressValue(value any) any {
	budget := maxAgentActivityInputBytes
	return cloneAgentProgressValueBounded(value, &budget, 0)
}

func cloneAgentProgressValueBounded(value any, budget *int, depth int) any {
	if budget == nil || *budget <= 0 {
		return nil
	}
	if depth >= maxAgentActivityInputDepth {
		const marker = "[truncated]"
		*budget -= min(len(marker), *budget)
		return marker
	}
	switch typed := value.(type) {
	case map[string]any:
		keys := boundedAgentProgressKeys(typed)
		out := make(map[string]any, len(keys))
		for _, key := range keys {
			if *budget <= 0 {
				break
			}
			boundedKey := truncateAgentProgressBytes(key, min(*budget, maxAgentToolNameRunes*4))
			*budget -= min(len(boundedKey), *budget)
			out[boundedKey] = cloneAgentProgressValueBounded(typed[key], budget, depth+1)
		}
		return out
	case []any:
		limit := min(len(typed), maxAgentActivityContainerEntries)
		out := make([]any, 0, limit)
		for i := 0; i < limit && *budget > 0; i++ {
			out = append(out, cloneAgentProgressValueBounded(typed[i], budget, depth+1))
		}
		return out
	case []string:
		limit := min(len(typed), maxAgentActivityContainerEntries)
		out := make([]string, 0, limit)
		for i := 0; i < limit && *budget > 0; i++ {
			item := truncateAgentProgressBytes(typed[i], *budget)
			*budget -= len(item)
			out = append(out, item)
		}
		return out
	case []map[string]any:
		limit := min(len(typed), maxAgentActivityContainerEntries)
		out := make([]map[string]any, 0, limit)
		for i := 0; i < limit && *budget > 0; i++ {
			cloned, _ := cloneAgentProgressValueBounded(typed[i], budget, depth+1).(map[string]any)
			out = append(out, cloned)
		}
		return out
	case string:
		out := truncateAgentProgressBytes(typed, *budget)
		*budget -= len(out)
		return out
	case nil, bool, float32, float64, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, json.Number:
		*budget -= min(32, *budget)
		return typed
	default:
		out := truncateAgentProgressBytes(fmt.Sprint(typed), *budget)
		*budget -= len(out)
		return out
	}
}

func boundedAgentProgressKeys(input map[string]any) []string {
	keys := make([]string, 0, min(len(input), maxAgentActivityInputKeys))
	for key := range input {
		if len(keys) < maxAgentActivityInputKeys {
			keys = append(keys, key)
			continue
		}
		maxIndex := 0
		for i := 1; i < len(keys); i++ {
			if keys[i] > keys[maxIndex] {
				maxIndex = i
			}
		}
		if key < keys[maxIndex] {
			keys[maxIndex] = key
		}
	}
	sort.Strings(keys)
	return keys
}

func truncateAgentProgressString(value string, maxRunes int) string {
	if maxRunes <= 0 || value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}
