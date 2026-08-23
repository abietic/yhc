package engine

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/provider"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
)

type promptSuggestionProbeModel struct {
	mu            sync.Mutex
	calls         int
	messages      []*schema.Message
	result        string
	wait          bool
	model         string
	tools         int
	maxTokens     int
	generateCalls int
	onCall        func()
}

func (m *promptSuggestionProbeModel) Generate(
	ctx context.Context,
	messages []*schema.Message,
	options ...model.Option,
) (*schema.Message, error) {
	m.mu.Lock()
	m.generateCalls++
	m.mu.Unlock()
	return &schema.Message{Role: schema.Assistant, Content: m.result}, nil
}

func (m *promptSuggestionProbeModel) Stream(
	ctx context.Context,
	messages []*schema.Message,
	options ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	common := model.GetCommonOptions(nil, options...)
	selectedModel := ""
	if common.Model != nil {
		selectedModel = *common.Model
	}
	maxTokens := 0
	if common.MaxTokens != nil {
		maxTokens = *common.MaxTokens
	}
	m.mu.Lock()
	m.calls++
	m.messages = append([]*schema.Message(nil), messages...)
	m.model = selectedModel
	m.tools = len(common.Tools)
	m.maxTokens = maxTokens
	m.mu.Unlock()
	if m.onCall != nil {
		m.onCall()
	}
	if m.wait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role:    schema.Assistant,
		Content: m.result,
		ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
			PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12,
		}},
	}}), nil
}

func (m *promptSuggestionProbeModel) snapshot() (
	int,
	[]*schema.Message,
	string,
	int,
	int,
	int,
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls,
		append([]*schema.Message(nil), m.messages...),
		m.model,
		m.tools,
		m.maxTokens,
		m.generateCalls
}

func TestGeneratePromptSuggestionUsesReadOnlyCurrentConversation(t *testing.T) {
	probe := &promptSuggestionProbeModel{result: "run the focused tests"}
	transcriptDir := t.TempDir()
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:               "prompt-suggestion-usage",
		CWD:                     t.TempDir(),
		TranscriptDir:           transcriptDir,
		ChatModel:               probe,
		Model:                   "current-model",
		CommandEntrypoint:       commands.EntrypointTUI,
		EnablePromptSuggestions: true,
	})
	t.Cleanup(eng.Close)

	eng.mu.Lock()
	eng.messages = []*schema.Message{
		{Role: schema.User, Content: "修复输入框" + strings.Repeat("界", 700)},
		{Role: schema.Assistant, Content: "我先检查实现"},
		{Role: schema.User, Content: "继续"},
		{Role: schema.Assistant, Content: "实现完成"},
	}
	eng.mu.Unlock()
	mainUsageMessage := &schema.Message{
		Role:    schema.Assistant,
		Content: "main response",
		ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
			PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120,
		}},
	}
	if err := eng.recordTranscriptMessages([]*schema.Message{mainUsageMessage}); err != nil {
		t.Fatalf("record main usage response: %v", err)
	}
	eng.observeProviderUsageMessage(mainUsageMessage)

	before := eng.GetMessages()
	got := eng.GeneratePromptSuggestion(context.Background())
	if got != "run the focused tests" {
		t.Fatalf("GeneratePromptSuggestion() = %q", got)
	}
	if after := eng.GetMessages(); len(after) != len(before) {
		t.Fatalf("suggestion mutated conversation: before=%d after=%d", len(before), len(after))
	}

	calls, messages, selectedModel, toolCount, maxTokens, generateCalls := probe.snapshot()
	if calls != 1 {
		t.Fatalf("provider calls = %d, want 1", calls)
	}
	if generateCalls != 0 {
		t.Fatalf("direct Generate calls = %d, want controlled streaming side query", generateCalls)
	}
	if selectedModel != "current-model" {
		t.Fatalf("provider model = %q, want current-model", selectedModel)
	}
	if toolCount != 0 {
		t.Fatalf("provider tools = %d, want 0", toolCount)
	}
	if maxTokens != 64 {
		t.Fatalf("provider max output tokens = %d, want 64", maxTokens)
	}
	if len(messages) < 2 || messages[0].Role != schema.System ||
		!strings.Contains(messages[0].Content, "SUGGESTION MODE") {
		t.Fatalf("provider input does not start with the suggestion contract: %#v", messages)
	}
	joined := make([]string, 0, len(messages)-1)
	for _, message := range messages[1:] {
		if message == nil {
			continue
		}
		if len(message.ToolCalls) != 0 {
			t.Fatalf("suggestion request exposed tool calls: %#v", message.ToolCalls)
		}
		joined = append(joined, message.Content)
	}
	conversation := strings.Join(joined, "\n")
	if !utf8.ValidString(conversation) {
		t.Fatalf("provider conversation is not valid UTF-8: %q", conversation)
	}
	for _, want := range []string{"user: \u4fee\u590d\u8f93\u5165\u6846", "assistant: \u5b9e\u73b0\u5b8c\u6210"} {
		if !strings.Contains(conversation, want) {
			t.Fatalf("provider conversation missing %q: %q", want, conversation)
		}
	}
	usage := eng.providerUsageSummary()
	if usage.PromptTokens != 110 || usage.CompletionTokens != 22 ||
		usage.TotalTokens != 132 || usage.ResponsesWithMetadata != 2 {
		t.Fatalf("suggestion usage was not included cumulatively: %#v", usage)
	}
	if !usage.CurrentContextUsageKnown || usage.CurrentContextPromptTokens != 100 {
		t.Fatalf("suggestion usage replaced the active context fact: %#v", usage)
	}
	restored, err := transcript.NewRecorder(
		"prompt-suggestion-usage",
		transcriptDir,
	).LoadFull()
	if err != nil {
		t.Fatalf("restore prompt suggestion usage: %v", err)
	}
	if restored.Usage.TotalTokens != 132 ||
		restored.Usage.CurrentContextPromptTokens != 100 ||
		!restored.Usage.CurrentContextUsageKnown {
		t.Fatalf("restored prompt suggestion usage = %#v", restored.Usage)
	}
	if len(restored.Messages) != 1 ||
		restored.Messages[0].Content != "main response" {
		t.Fatalf("prompt suggestion content leaked into transcript: %#v", restored.Messages)
	}
}

func TestPromptSuggestionProviderCallPreservesMainRouteIdentity(t *testing.T) {
	binding := &session.PersistedModelBinding{
		Version:             session.PersistedModelBindingVersion,
		Kind:                session.ModelBindingKindProfile,
		Value:               "primary",
		Provider:            string(provider.ProviderAgenticOpenAI),
		APIModel:            "gpt-5",
		PortfolioRevision:   strings.Repeat("a", 64),
		RouteIdentityDigest: strings.Repeat("b", 64),
		MetadataDigest:      strings.Repeat("c", 64),
		ReasoningEffort:     "high",
	}
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:               "prompt-suggestion-session",
		CWD:                     t.TempDir(),
		TranscriptDir:           t.TempDir(),
		ChatModel:               &promptSuggestionProbeModel{},
		Model:                   "primary",
		CommandEntrypoint:       commands.EntrypointTUI,
		EnablePromptSuggestions: true,
	})
	t.Cleanup(eng.Close)
	eng.mu.Lock()
	eng.modelBinding = binding.Clone()
	eng.modelDispatchBlock = nil
	eng.mu.Unlock()

	call, err := eng.promptSuggestionProviderCall()
	if err != nil {
		t.Fatal(err)
	}
	options := call.options
	if options.Model != "primary" ||
		options.Provider != string(provider.ProviderAgenticOpenAI) ||
		options.ModelRole != "main" ||
		options.ModelProfile != "primary" ||
		options.EffortValue != "high" {
		t.Fatalf("prompt suggestion route identity = %#v", options)
	}
	if options.QuerySource != "prompt_suggestion_generation" ||
		options.SessionID != "prompt-suggestion-session" ||
		options.MaxOutputTokens == nil || *options.MaxOutputTokens != 64 {
		t.Fatalf("prompt suggestion call controls = %#v", options)
	}
}

func TestGeneratePromptSuggestionFailsClosedOutsideEligibleTUI(t *testing.T) {
	tests := []struct {
		name              string
		config            QueryEngineConfig
		pendingPermission bool
	}{
		{name: "disabled", config: QueryEngineConfig{CommandEntrypoint: commands.EntrypointTUI}},
		{name: "headless", config: QueryEngineConfig{CommandEntrypoint: commands.EntrypointHeadless, EnablePromptSuggestions: true}},
		{name: "child", config: QueryEngineConfig{CommandEntrypoint: commands.EntrypointTUI, EnablePromptSuggestions: true, AgentID: "child"}},
		{name: "plan", config: QueryEngineConfig{CommandEntrypoint: commands.EntrypointTUI, EnablePromptSuggestions: true, PermissionMode: permission.ModePlan}},
		{name: "pending permission", config: QueryEngineConfig{CommandEntrypoint: commands.EntrypointTUI, EnablePromptSuggestions: true}, pendingPermission: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			probe := &promptSuggestionProbeModel{result: "run the tests"}
			config := test.config
			config.CWD = t.TempDir()
			config.TranscriptDir = t.TempDir()
			config.ChatModel = probe
			eng := NewQueryEngine(config)
			t.Cleanup(eng.Close)
			eng.mu.Lock()
			eng.messages = []*schema.Message{
				{Role: schema.Assistant, Content: "one"},
				{Role: schema.Assistant, Content: "two"},
			}
			eng.mu.Unlock()
			if test.pendingPermission {
				key := permissionRequestKey{
					engineID:  eng.permissionEngineID,
					toolUseID: "suggestion-test",
				}
				eng.permissionCoordinator.mu.Lock()
				eng.permissionCoordinator.pending[key] = &permissionPendingRequest{}
				eng.permissionCoordinator.mu.Unlock()
				t.Cleanup(func() {
					eng.permissionCoordinator.mu.Lock()
					delete(eng.permissionCoordinator.pending, key)
					eng.permissionCoordinator.mu.Unlock()
				})
			}

			if got := eng.GeneratePromptSuggestion(context.Background()); got != "" {
				t.Fatalf("ineligible suggestion = %q", got)
			}
			if calls, _, _, _, _, _ := probe.snapshot(); calls != 0 {
				t.Fatalf("ineligible provider calls = %d", calls)
			}
		})
	}
}

func TestGeneratePromptSuggestionPropagatesCancellation(t *testing.T) {
	probe := &promptSuggestionProbeModel{wait: true}
	eng := NewQueryEngine(QueryEngineConfig{
		CWD:                     t.TempDir(),
		TranscriptDir:           t.TempDir(),
		ChatModel:               probe,
		CommandEntrypoint:       commands.EntrypointTUI,
		EnablePromptSuggestions: true,
	})
	t.Cleanup(eng.Close)
	eng.mu.Lock()
	eng.messages = []*schema.Message{
		{Role: schema.Assistant, Content: "one"},
		{Role: schema.Assistant, Content: "two"},
	}
	eng.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan string, 1)
	go func() { done <- eng.GeneratePromptSuggestion(ctx) }()

	deadline := time.Now().Add(time.Second)
	for {
		if calls, _, _, _, _, _ := probe.snapshot(); calls == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("suggestion provider did not start")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case got := <-done:
		if got != "" {
			t.Fatalf("cancelled suggestion = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled suggestion did not return")
	}
}

func TestGeneratePromptSuggestionDropsResultAfterModelRouteChange(t *testing.T) {
	probe := &promptSuggestionProbeModel{result: "run the tests"}
	eng := NewQueryEngine(QueryEngineConfig{
		CWD:                     t.TempDir(),
		TranscriptDir:           t.TempDir(),
		ChatModel:               probe,
		Model:                   "current-model",
		CommandEntrypoint:       commands.EntrypointTUI,
		EnablePromptSuggestions: true,
	})
	t.Cleanup(eng.Close)
	eng.mu.Lock()
	eng.messages = []*schema.Message{
		{Role: schema.Assistant, Content: "one"},
		{Role: schema.Assistant, Content: "two"},
	}
	eng.mu.Unlock()
	probe.onCall = func() {
		eng.mu.Lock()
		eng.promptRouteGeneration++
		eng.config.Model = "new-model"
		eng.mu.Unlock()
	}

	if got := eng.GeneratePromptSuggestion(context.Background()); got != "" {
		t.Fatalf("route-stale suggestion = %q", got)
	}
}
