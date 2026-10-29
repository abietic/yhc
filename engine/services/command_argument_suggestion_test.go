package services

import (
	"strings"
	"testing"
)

func TestCommandArgumentSuggestionContextIsBounded(t *testing.T) {
	context := BuildCommandArgumentSuggestionContext(
		"compact", strings.Repeat("界", 1200), strings.Repeat("参", 300), strings.Repeat("前", 300), 2,
	)
	if strings.Count(context, "界") != maxCommandArgumentHelpRunes ||
		strings.Count(context, "参") != maxCommandArgumentHintRunes ||
		strings.Count(context, "前") != maxCommandArgumentPrefixRunes {
		t.Fatalf("bounded context = %q", context)
	}
}

func TestParseCommandArgumentSuggestionFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		output string
		prefix string
		want   string
	}{
		{name: "valid", output: `{"argument":"because context is stale"}`, prefix: "because", want: "because context is stale"},
		{name: "case sensitive prefix", output: `{"argument":"Because context is stale"}`, prefix: "because"},
		{name: "unchanged prefix", output: `{"argument":"because"}`, prefix: "because"},
		{name: "malformed", output: `{"argument":`},
		{name: "multiple values", output: `{"argument":"because x"} {"argument":"because y"}`, prefix: "because"},
		{name: "unknown field", output: `{"argument":"because x","note":"no"}`, prefix: "because"},
		{name: "line break", output: `{"argument":"because\nnext"}`, prefix: "because"},
		{name: "too long", output: `{"argument":"` + strings.Repeat("a", 257) + `"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ParseCommandArgumentSuggestion(test.output, test.prefix); got != test.want {
				t.Fatalf("ParseCommandArgumentSuggestion() = %q, want %q", got, test.want)
			}
		})
	}
}
