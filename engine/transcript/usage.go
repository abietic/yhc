package transcript

import "github.com/cloudwego/eino/schema"

const UsageSummaryVersion = 1

// UsageSummary is a cumulative, provider-reported usage ledger. It never
// estimates tokens: missing metadata is counted separately so callers can
// distinguish a known zero from unavailable or partial accounting.
type UsageSummary struct {
	Version                      int  `json:"version"`
	UnsupportedSnapshotVersion   int  `json:"unsupported_snapshot_version,omitempty"`
	PromptTokens                 int  `json:"prompt_tokens"`
	CompletionTokens             int  `json:"completion_tokens"`
	TotalTokens                  int  `json:"total_tokens"`
	ResponsesWithMetadata        int  `json:"responses_with_metadata"`
	ResponsesWithoutMetadata     int  `json:"responses_without_metadata"`
	LegacyBoundariesWithoutUsage int  `json:"legacy_boundaries_without_usage,omitempty"`
	LastPromptTokens             int  `json:"last_prompt_tokens"`
	LastCompletionTokens         int  `json:"last_completion_tokens"`
	LastTotalTokens              int  `json:"last_total_tokens"`
	LastResponseHadUsageMetadata bool `json:"last_response_had_usage_metadata"`
	CurrentContextPromptTokens   int  `json:"current_context_prompt_tokens"`
	CurrentContextUsageKnown     bool `json:"current_context_usage_known"`
}

// ObserveMessage adds one persisted model response when usage metadata is
// present, or records the missing coverage without inventing a value.
func (s *UsageSummary) ObserveMessage(message *schema.Message) {
	if s == nil || message == nil {
		return
	}
	if message.ResponseMeta != nil && message.ResponseMeta.Usage != nil {
		s.ObserveUsage(message.ResponseMeta.Usage)
		if message.Role == schema.Assistant && usageExpectedForMessage(message) {
			s.CurrentContextPromptTokens = message.ResponseMeta.Usage.PromptTokens
			s.CurrentContextUsageKnown = true
		} else {
			s.CurrentContextPromptTokens = 0
			s.CurrentContextUsageKnown = false
		}
		return
	}
	if usageExpectedForMessage(message) {
		s.ResponsesWithoutMetadata++
		s.LastResponseHadUsageMetadata = false
		s.CurrentContextPromptTokens = 0
		s.CurrentContextUsageKnown = false
	}
}

// ObserveUsage adds one provider-reported response usage record.
func (s *UsageSummary) ObserveUsage(usage *schema.TokenUsage) {
	if s == nil || usage == nil {
		return
	}
	if s.Version == 0 {
		s.Version = UsageSummaryVersion
	}
	total := usage.TotalTokens
	if total == 0 && (usage.PromptTokens != 0 || usage.CompletionTokens != 0) {
		total = usage.PromptTokens + usage.CompletionTokens
	}
	s.PromptTokens += usage.PromptTokens
	s.CompletionTokens += usage.CompletionTokens
	s.TotalTokens += total
	s.ResponsesWithMetadata++
	s.LastPromptTokens = usage.PromptTokens
	s.LastCompletionTokens = usage.CompletionTokens
	s.LastTotalTokens = total
	s.LastResponseHadUsageMetadata = true
}

func normalizeUsageSnapshot(summary UsageSummary) UsageSummary {
	if summary.Version == UsageSummaryVersion {
		return summary
	}
	if summary.Version == 0 {
		return UsageSummary{
			Version:                      UsageSummaryVersion,
			LegacyBoundariesWithoutUsage: 1,
		}
	}
	return UsageSummary{
		Version:                    UsageSummaryVersion,
		UnsupportedSnapshotVersion: summary.Version,
	}
}

func prepareUsageSnapshot(summary UsageSummary) UsageSummary {
	summary.Version = UsageSummaryVersion
	return summary
}

func usageExpectedForMessage(message *schema.Message) bool {
	if message == nil {
		return false
	}
	if message.Role == schema.Assistant {
		if message.Extra != nil {
			if isMeta, _ := message.Extra["is_meta"].(bool); isMeta {
				return false
			}
			if isAPIError, _ := message.Extra["api_error"].(bool); isAPIError {
				return false
			}
		}
		return true
	}
	if message.Extra != nil {
		expected, _ := message.Extra["usage_expected"].(bool)
		return expected
	}
	return false
}

func boundaryContainsUsageRelevantMessage(messages []*schema.Message) bool {
	for _, message := range messages {
		if usageExpectedForMessage(message) ||
			(message != nil && message.ResponseMeta != nil && message.ResponseMeta.Usage != nil) {
			return true
		}
	}
	return false
}
