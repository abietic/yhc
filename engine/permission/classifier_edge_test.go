package permission

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestParseClassifierResponseEdgeCases(t *testing.T) {
	tests := []struct {
		name string
		msg  *schema.Message
		want ClassifierDecision
	}{
		{
			name: "allow tag with spaces",
			msg:  &schema.Message{Role: schema.Assistant, Content: "  <allow />  "},
			want: ClassifierAllow,
		},
		{
			name: "block tag",
			msg:  &schema.Message{Role: schema.Assistant, Content: "<block/>"},
			want: ClassifierDeny,
		},
		{
			name: "ambiguous allow and block fails closed",
			msg:  &schema.Message{Role: schema.Assistant, Content: "<allow/> but also <block/>"},
			want: ClassifierDeny,
		},
		{
			name: "malformed json-ish response fails closed",
			msg:  &schema.Message{Role: schema.Assistant, Content: `{"decision":"allow"`},
			want: ClassifierDeny,
		},
		{
			name: "empty response fails closed",
			msg:  &schema.Message{Role: schema.Assistant, Content: ""},
			want: ClassifierDeny,
		},
		{
			name: "nil response fails closed",
			msg:  nil,
			want: ClassifierDeny,
		},
		{
			name: "allow only inside thinking fails closed",
			msg:  &schema.Message{Role: schema.Assistant, Content: "<thinking><allow/></thinking>"},
			want: ClassifierDeny,
		},
		{
			name: "thinking block does not override external allow",
			msg:  &schema.Message{Role: schema.Assistant, Content: "<thinking><block/></thinking><allow/>"},
			want: ClassifierAllow,
		},
		{
			name: "unterminated thinking fails closed",
			msg:  &schema.Message{Role: schema.Assistant, Content: "<thinking>maybe <allow/>"},
			want: ClassifierDeny,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseClassifierResponse(tt.msg); got != tt.want {
				t.Fatalf("parseClassifierResponse() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractRecentContextLimitsAndTruncatesMessages(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.System, Content: "system should be skipped by limit"},
		{Role: schema.User, Content: "old user"},
		{Role: schema.Assistant, Content: strings.Repeat("assistant-long-", 60)},
		{Role: schema.User, Content: "latest user"},
	}

	got := extractRecentContext(messages, 2)
	if strings.Contains(got, "old user") || strings.Contains(got, "system should") {
		t.Fatalf("expected only last two messages in context, got:\n%s", got)
	}
	if !strings.Contains(got, "[assistant]: ") || !strings.Contains(got, "[user]: latest user") {
		t.Fatalf("expected assistant and latest user context, got:\n%s", got)
	}
	if !strings.Contains(got, "...") {
		t.Fatalf("expected long assistant message to be truncated, got:\n%s", got)
	}
}

func TestBuildClassifierPromptsIncludeRulesAndContextAdaptation(t *testing.T) {
	cfg := &ClassifierConfig{
		AllowRules:       []string{"Bash(go test*)"},
		DenyRules:        []string{"Bash(rm*)"},
		EnvironmentRules: []string{"workspace is read-only"},
		ClaudeMdContext:  strings.Repeat("project-context-", 300),
	}

	systemPrompt := buildClassifierSystemPrompt(cfg)
	for _, want := range []string{
		"User-configured ALLOW rules",
		"Bash(go test*)",
		"User-configured DENY rules",
		"Bash(rm*)",
		"Environment constraints",
		"workspace is read-only",
		"Project context",
		"...",
	} {
		if !strings.Contains(systemPrompt, want) {
			t.Fatalf("expected system prompt to contain %q, got:\n%s", want, systemPrompt)
		}
	}

	userPrompt := buildClassifierUserPrompt("Bash", map[string]any{"command": "go test ./..."}, []*schema.Message{
		{Role: schema.User, Content: "please verify tests"},
	})
	if !strings.Contains(userPrompt, "Tool being invoked: Bash") ||
		!strings.Contains(userPrompt, `"command": "go test ./..."`) ||
		!strings.Contains(userPrompt, "[user]: please verify tests") ||
		!strings.Contains(userPrompt, "Respond with <allow/> or <block/>") {
		t.Fatalf("unexpected classifier user prompt:\n%s", userPrompt)
	}
}

func TestClassifierCacheUsesStableJSONKeyAndEvictsOldest(t *testing.T) {
	cache := NewClassifierCache(2)
	first := map[string]any{"b": "2", "a": "1"}
	firstSame := map[string]any{"a": "1", "b": "2"}
	second := map[string]any{"command": "go test"}
	third := map[string]any{"command": "rm -rf /"}

	cache.Put("Bash", first, ClassifierAllow)
	if got, ok := cache.Get("Bash", firstSame); !ok || got != ClassifierAllow {
		t.Fatalf("expected stable JSON cache hit, got %q ok=%v", got, ok)
	}

	cache.Put("Bash", second, ClassifierAsk)
	cache.Put("Bash", third, ClassifierDeny)
	if _, ok := cache.Get("Bash", first); ok {
		t.Fatal("expected oldest cache entry to be evicted")
	}
	if got, ok := cache.Get("Bash", third); !ok || got != ClassifierDeny {
		t.Fatalf("expected newest cache entry, got %q ok=%v", got, ok)
	}
}
