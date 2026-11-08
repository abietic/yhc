package compact

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestAuthoritativeContextWindowAndNarrowing(t *testing.T) {
	for _, tc := range []struct {
		name, model, override string
		limit, want           int
	}{
		{name: "profile-large", model: "custom-profile", limit: 1048576, want: 1048576},
		{name: "profile-small", model: "deepseek-flash", limit: 128000, want: 128000},
		{name: "unknown", model: "custom-profile", want: 200000},
		{name: "invalid-limit", model: "custom-profile", limit: -1, want: 200000},
		{name: "narrowed", model: "custom-profile", limit: 1048576, override: "128000", want: 128000},
		{name: "override-cannot-enlarge", model: "custom-profile", limit: 128000, override: "1048576", want: 128000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", tc.override)
			t.Setenv("CLAUDE_AUTOCOMPACT_PCT_OVERRIDE", "")
			if got := GetEffectiveContextWindowSizeForContextWindow(tc.model, &tc.limit); got != tc.want {
				t.Fatalf("context window=%d want=%d", got, tc.want)
			}
			if got := GetAutoCompactThresholdForContextWindow(tc.model, &tc.limit); got != tc.want-13000 {
				t.Fatalf("auto threshold=%d want=%d", got, tc.want-13000)
			}
		})
	}
}

func TestAutoCompactUsesAuthoritativeContextWindow(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "")
	t.Setenv("CLAUDE_AUTOCOMPACT_PCT_OVERRIDE", "")
	for _, tc := range []struct {
		name, model  string
		limit, bytes int
		compacted    bool
	}{
		{name: "large-profile-preserves-history", model: "custom-profile", limit: 1048576, bytes: 760000},
		{name: "small-profile-compacts", model: "deepseek-flash", limit: 128000, bytes: 480000, compacted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := []*schema.Message{
				{Role: schema.User, Content: "Original requirement."},
				{Role: schema.Assistant, Content: strings.Repeat("x", tc.bytes)},
				{Role: schema.Assistant, Content: "Earlier output consumed."},
				{Role: schema.User, Content: "Continue."},
			}
			result, _, _ := AutoCompact(messages, "sdk", nil, 0, tc.model, &AutoCompactParams{ContextWindow: &tc.limit})
			if (result != nil) != tc.compacted {
				t.Fatalf("compacted=%t want=%t", result != nil, tc.compacted)
			}
			if len(messages[1].Content) != tc.bytes {
				t.Fatal("auto compaction mutated caller history")
			}
		})
	}
}
