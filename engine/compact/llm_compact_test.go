package compact

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// --- prompt tests ---

func TestGetCompactPrompt(t *testing.T) {
	prompt := GetCompactPrompt("")
	if !strings.Contains(prompt, "CRITICAL: Respond with TEXT ONLY") {
		t.Fatal("expected no-tools preamble in compact prompt")
	}
	if !strings.Contains(prompt, "Primary Request and Intent") {
		t.Fatal("expected section headers in compact prompt")
	}
	if !strings.Contains(prompt, "REMINDER: Do NOT call any tools") {
		t.Fatal("expected no-tools trailer in compact prompt")
	}
}

func TestGetCompactPromptWithCustomInstructions(t *testing.T) {
	prompt := GetCompactPrompt("Focus on Go code changes")
	if !strings.Contains(prompt, "Additional Instructions:\nFocus on Go code changes") {
		t.Fatal("expected custom instructions in compact prompt")
	}
}

func TestGetCompactPromptIgnoresWhitespaceInstructions(t *testing.T) {
	prompt := GetCompactPrompt("   ")
	if strings.Contains(prompt, "Additional Instructions:") {
		t.Fatal("expected whitespace-only instructions to be ignored")
	}
}

func TestFormatCompactSummaryStripsAnalysis(t *testing.T) {
	input := `<analysis>
This is internal reasoning that should be stripped.
</analysis>

<summary>
1. Primary Request:
   Build a web app
</summary>`

	result := FormatCompactSummary(input)
	if strings.Contains(result, "internal reasoning") {
		t.Fatal("expected analysis to be stripped")
	}
	if !strings.Contains(result, "Summary:") {
		t.Fatal("expected Summary: header")
	}
	if !strings.Contains(result, "Build a web app") {
		t.Fatal("expected summary content to be preserved")
	}
}

func TestFormatCompactSummaryNoTags(t *testing.T) {
	input := "Plain text summary without XML tags."
	result := FormatCompactSummary(input)
	if result != input {
		t.Fatalf("expected untagged input to pass through, got %q", result)
	}
}

func TestGetCompactUserSummaryMessage(t *testing.T) {
	msg := GetCompactUserSummaryMessage("1. Did stuff", false, "", false)
	if !strings.Contains(msg, "being continued from a previous conversation") {
		t.Fatal("expected continuation header")
	}
	if !strings.Contains(msg, "1. Did stuff") {
		t.Fatal("expected summary content")
	}
}

func TestGetCompactUserSummaryMessageSuppressQuestions(t *testing.T) {
	msg := GetCompactUserSummaryMessage("summary", true, "", false)
	if !strings.Contains(msg, "without asking the user any further questions") {
		t.Fatal("expected suppress questions directive")
	}
}

func TestGetCompactUserSummaryMessageWithTranscript(t *testing.T) {
	msg := GetCompactUserSummaryMessage("summary", false, "/tmp/transcript.jsonl", false)
	if !strings.Contains(msg, "/tmp/transcript.jsonl") {
		t.Fatal("expected transcript path in message")
	}
}

// --- strip tests ---

func TestStripImagesFromMessagesReplacesImages(t *testing.T) {
	messages := []*schema.Message{
		{
			Role: schema.User,
			MultiContent: []schema.ChatMessagePart{ //nolint:staticcheck
				{Type: schema.ChatMessagePartTypeText, Text: "Look at this"},
				{Type: schema.ChatMessagePartTypeImageURL, ImageURL: &schema.ChatMessageImageURL{URL: "http://example.com/img.png"}}, //nolint:staticcheck
			},
		},
		{
			Role:    schema.Assistant,
			Content: "I see the image",
		},
	}

	result := StripImagesFromMessages(messages)
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
	// User message should have image replaced
	if len(result[0].MultiContent) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(result[0].MultiContent))
	}
	if result[0].MultiContent[1].Type != schema.ChatMessagePartTypeText {
		t.Fatal("expected image replaced with text")
	}
	if result[0].MultiContent[1].Text != "[image]" {
		t.Fatalf("expected [image] placeholder, got %q", result[0].MultiContent[1].Text)
	}
	// Assistant message should be unchanged
	if result[1] != messages[1] {
		t.Fatal("expected assistant message to be unchanged (same pointer)")
	}
}

func TestStripImagesFromMessagesNoImages(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "plain text"},
	}
	result := StripImagesFromMessages(messages)
	if result[0] != messages[0] {
		t.Fatal("expected message without images to be unchanged (same pointer)")
	}
}

func TestStripImagesFromMessagesUserInputMultiContent(t *testing.T) {
	imgURL := "data:image/png;base64,abc"
	messages := []*schema.Message{
		{
			Role: schema.User,
			UserInputMultiContent: []schema.MessageInputPart{
				{Type: schema.ChatMessagePartTypeText, Text: "text"},
				{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{URL: &imgURL}}},
			},
		},
	}

	result := StripImagesFromMessages(messages)
	if len(result[0].UserInputMultiContent) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(result[0].UserInputMultiContent))
	}
	if result[0].UserInputMultiContent[1].Text != "[image]" {
		t.Fatalf("expected [image] placeholder, got %q", result[0].UserInputMultiContent[1].Text)
	}
}

// --- grouping tests ---

func TestGroupMessagesByAPIRound(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "hi", Extra: map[string]any{"message_id": "a1"}},
		{Role: schema.User, Content: "question"},
		{Role: schema.Assistant, Content: "answer", Extra: map[string]any{"message_id": "a2"}},
	}

	groups := GroupMessagesByAPIRound(messages)
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(groups))
	}
	// First group: [user, assistant(a1), user] — boundary fires at new assistant id
	if len(groups[0]) != 3 {
		t.Fatalf("expected first group to have 3 messages, got %d", len(groups[0]))
	}
	// Second group: [assistant(a2)]
	if len(groups[1]) != 1 {
		t.Fatalf("expected second group to have 1 message, got %d", len(groups[1]))
	}
}

func TestGroupMessagesByAPIRoundSameAssistantID(t *testing.T) {
	// Multiple assistant messages with same ID stay in one group
	messages := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "part1", Extra: map[string]any{"message_id": "same"}},
		{Role: schema.Tool, Content: "result"},
		{Role: schema.Assistant, Content: "part2", Extra: map[string]any{"message_id": "same"}},
	}

	groups := GroupMessagesByAPIRound(messages)
	if len(groups) != 1 {
		t.Fatalf("expected 1 group for same assistant ID, got %d", len(groups))
	}
}

func TestGroupMessagesByAPIRoundEmpty(t *testing.T) {
	groups := GroupMessagesByAPIRound(nil)
	if len(groups) != 0 {
		t.Fatalf("expected 0 groups for nil input, got %d", len(groups))
	}
}

// --- PTL retry tests ---

func TestTruncateHeadForPTLRetryDropsGroups(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: strings.Repeat("old ", 100)},
		{Role: schema.Assistant, Content: "reply1", Extra: map[string]any{"message_id": "a1"}},
		{Role: schema.User, Content: "question"},
		{Role: schema.Assistant, Content: "reply2", Extra: map[string]any{"message_id": "a2"}},
	}

	// tokenGap=0 triggers 20% fallback (drops 1 of 2 groups)
	result := TruncateHeadForPTLRetry(messages, 0)
	if result == nil {
		t.Fatal("expected truncated result")
		return
	}
	// Should have dropped the first group, kept the second
	if len(result) < 2 {
		t.Fatalf("expected at least 2 messages in truncated result, got %d", len(result))
	}
}

func TestTruncateHeadForPTLRetryReturnsNilForSingleGroup(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "hello"},
	}
	result := TruncateHeadForPTLRetry(messages, 1000)
	if result != nil {
		t.Fatal("expected nil when only one group exists")
		return
	}
}

func TestTruncateHeadForPTLRetryStripsMarker(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: ptlRetryMarker},
		{Role: schema.User, Content: strings.Repeat("old ", 100)},
		{Role: schema.Assistant, Content: "r1", Extra: map[string]any{"message_id": "a1"}},
		{Role: schema.User, Content: "new"},
		{Role: schema.Assistant, Content: "r2", Extra: map[string]any{"message_id": "a2"}},
	}

	result := TruncateHeadForPTLRetry(messages, 0)
	if result == nil {
		t.Fatal("expected truncated result after stripping marker")
		return
	}
}

// --- LLM compact integration tests ---

// mockCompactModel is a mock that returns a fixed summary.
type mockCompactModel struct {
	model.BaseChatModel
	response         string
	usage            *schema.TokenUsage
	err              error
	calls            int
	receivedMessages []*schema.Message
}

func (m *mockCompactModel) Generate(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	m.calls++
	m.receivedMessages = msgs
	if m.err != nil {
		return nil, m.err
	}
	return &schema.Message{
		Role:         schema.Assistant,
		Content:      m.response,
		ResponseMeta: &schema.ResponseMeta{Usage: m.usage},
	}, nil
}

func TestRunLLMCompactSanitizesMediaBeforeSummaryModel(t *testing.T) {
	mediaURL := "https://example.invalid/private-media"
	mock := &mockCompactModel{response: "summary"}
	original := &schema.Message{
		Role: schema.User,
		MultiContent: []schema.ChatMessagePart{ //nolint:staticcheck
			{Type: schema.ChatMessagePartTypeAudioURL, AudioURL: &schema.ChatMessageAudioURL{URL: mediaURL}}, //nolint:staticcheck
		},
		UserInputMultiContent: []schema.MessageInputPart{
			{Type: schema.ChatMessagePartTypeText, Text: "before"},
			{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{URL: &mediaURL}}, Extra: map[string]any{"sensitive": true}},
			{Type: schema.ChatMessagePartTypeAudioURL, Audio: &schema.MessageInputAudio{MessagePartCommon: schema.MessagePartCommon{URL: &mediaURL}}},
			{Type: schema.ChatMessagePartTypeVideoURL, Video: &schema.MessageInputVideo{MessagePartCommon: schema.MessagePartCommon{URL: &mediaURL}}},
			{Type: schema.ChatMessagePartTypeFileURL, File: &schema.MessageInputFile{MessagePartCommon: schema.MessagePartCommon{URL: &mediaURL}, Name: "private.pdf"}},
			{Type: schema.ChatMessagePartTypeText, Text: "after"},
		},
	}

	if _, err := RunLLMCompact(context.Background(), []*schema.Message{original}, LLMCompactOptions{
		ChatModel: mock,
		ModelName: "test-model",
	}); err != nil {
		t.Fatalf("RunLLMCompact: %v", err)
	}
	if len(mock.receivedMessages) < 2 {
		t.Fatalf("summary model received %d messages, want conversation plus compact prompt", len(mock.receivedMessages))
	}

	var got *schema.Message
	for _, msg := range mock.receivedMessages {
		if msg != nil && (len(msg.UserInputMultiContent) > 0 || len(msg.MultiContent) > 0) { //nolint:staticcheck
			got = msg
			break
		}
	}
	if got == nil {
		t.Fatalf("summary model did not receive the rich user message: %#v", mock.receivedMessages)
	}
	wantMulti := []string{"[audio]"}
	if len(got.MultiContent) != len(wantMulti) || got.MultiContent[0].Text != wantMulti[0] { //nolint:staticcheck
		t.Fatalf("deprecated media projection = %#v, want %v", got.MultiContent, wantMulti) //nolint:staticcheck
	}
	wantUserParts := []string{"before", "[image]", "[audio]", "[video]", "[file]", "after"}
	if len(got.UserInputMultiContent) != len(wantUserParts) {
		t.Fatalf("summary user parts = %d, want %d", len(got.UserInputMultiContent), len(wantUserParts))
	}
	for i, part := range got.UserInputMultiContent {
		if part.Type != schema.ChatMessagePartTypeText || part.Text != wantUserParts[i] {
			t.Fatalf("summary user part %d = %#v, want text %q", i, part, wantUserParts[i])
		}
		if part.Image != nil || part.Audio != nil || part.Video != nil || part.File != nil || part.Extra != nil {
			t.Fatalf("summary user part %d retained media or metadata: %#v", i, part)
		}
	}

	if original.UserInputMultiContent[1].Image == nil || original.UserInputMultiContent[1].Extra == nil {
		t.Fatal("RunLLMCompact mutated the original media message")
	}
}

func (m *mockCompactModel) Stream(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, msgs, opts...)
	if err != nil {
		return nil, err
	}
	sr, sw := schema.Pipe[*schema.Message](1)
	go func() {
		sw.Send(msg, nil)
		sw.Close()
	}()
	return sr, nil
}

func TestRunLLMCompactSuccess(t *testing.T) {
	mock := &mockCompactModel{
		response: `<analysis>
Thinking about the conversation...
</analysis>

<summary>
1. Primary Request and Intent:
   User asked to build a CLI tool.
</summary>`,
		usage: &schema.TokenUsage{PromptTokens: 50, CompletionTokens: 5, TotalTokens: 55},
	}

	result, err := RunLLMCompact(context.Background(), []*schema.Message{
		{Role: schema.User, Content: "Build a CLI tool"},
		{Role: schema.Assistant, Content: "I'll help you build that."},
	}, LLMCompactOptions{
		ChatModel: mock,
		ModelName: "test-model",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if !strings.Contains(result.Summary, "build a CLI tool") {
		t.Fatalf("expected summary to contain request, got %q", result.Summary)
	}
	if strings.Contains(result.Summary, "Thinking about") {
		t.Fatal("expected analysis to be stripped from formatted summary")
	}
	if !strings.Contains(result.RawSummary, "analysis") {
		t.Fatal("expected raw summary to preserve analysis tags")
	}
	if result.Usage == nil || result.Usage.PromptTokens != 50 || result.Usage.CompletionTokens != 5 {
		t.Fatalf("usage = %#v", result.Usage)
	}
}

func TestBuildLLMAutoCompactPersistsUsageOnBoundary(t *testing.T) {
	mock := &mockCompactModel{
		response: "summary",
		usage:    &schema.TokenUsage{PromptTokens: 70, CompletionTokens: 7, TotalTokens: 77},
	}
	result, err := BuildLLMAutoCompact(context.Background(), []*schema.Message{
		{Role: schema.User, Content: "question"},
		{Role: schema.Assistant, Content: "answer"},
	}, 100, LLMCompactOptions{ChatModel: mock, ModelName: "test-model"})
	if err != nil {
		t.Fatalf("BuildLLMAutoCompact: %v", err)
	}
	if result.CompactionUsage == nil || result.CompactionUsage.TotalTokens != 77 {
		t.Fatalf("compaction usage = %#v", result.CompactionUsage)
	}
	if result.BoundaryMarker == nil || result.BoundaryMarker.ResponseMeta == nil ||
		result.BoundaryMarker.ResponseMeta.Usage == nil ||
		result.BoundaryMarker.ResponseMeta.Usage.TotalTokens != 77 {
		t.Fatalf("boundary usage = %#v", result.BoundaryMarker)
	}
	if expected, _ := result.BoundaryMarker.Extra["usage_expected"].(bool); !expected {
		t.Fatalf("boundary does not declare usage expectation: %#v", result.BoundaryMarker.Extra)
	}
}

func TestRunLLMCompactError(t *testing.T) {
	mock := &mockCompactModel{
		err: errors.New("model unavailable"),
	}

	_, err := RunLLMCompact(context.Background(), []*schema.Message{
		{Role: schema.User, Content: "hello"},
	}, LLMCompactOptions{
		ChatModel: mock,
		ModelName: "test-model",
	})

	if err == nil {
		t.Fatal("expected error when model fails")
		return
	}
}

func TestRunLLMCompactEmptyMessages(t *testing.T) {
	_, err := RunLLMCompact(context.Background(), nil, LLMCompactOptions{
		ChatModel: &mockCompactModel{},
	})
	if err == nil || !strings.Contains(err.Error(), "not enough messages") {
		t.Fatalf("expected 'not enough messages' error, got %v", err)
		return
	}
}

func TestRunLLMCompactNilModel(t *testing.T) {
	_, err := RunLLMCompact(context.Background(), []*schema.Message{
		{Role: schema.User, Content: "hello"},
	}, LLMCompactOptions{})
	if err == nil || !strings.Contains(err.Error(), "chat model is required") {
		t.Fatalf("expected 'chat model is required' error, got %v", err)
		return
	}
}

func TestAutoCompactWithLLMModel(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "2000")

	mock := &mockCompactModel{
		response: `<analysis>thinking</analysis>
<summary>
1. User asked something.
</summary>`,
	}

	messages := []*schema.Message{
		{Role: schema.User, Content: strings.Repeat("old context ", 200)},
		{Role: schema.Assistant, Content: strings.Repeat("assistant reply ", 180)},
		{Role: schema.User, Content: "latest question"},
		{Role: schema.Assistant, Content: "latest answer"},
	}

	result, failures, tracking := AutoCompact(messages, "sdk", &CompactTracking{}, 0, "", &AutoCompactParams{
		Ctx:       context.Background(),
		ChatModel: mock,
	})

	if result == nil {
		t.Fatal("expected LLM auto-compact result")
		return
	}
	if failures != 0 {
		t.Fatalf("expected 0 failures, got %d", failures)
	}
	if !tracking.Compacted {
		t.Fatal("expected tracking.Compacted = true")
	}
	if mock.calls == 0 {
		t.Fatal("expected model to be called")
	}
	// Summary should contain formatted LLM output
	if len(result.SummaryMessages) == 0 {
		t.Fatal("expected summary messages")
	}
	if !strings.Contains(result.SummaryMessages[0].Content, "User asked something") {
		t.Fatalf("expected LLM summary content, got %q", result.SummaryMessages[0].Content)
	}
}

func TestAutoCompactFallsToDeterministicOnLLMFailure(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "2000")

	mock := &mockCompactModel{
		err: errors.New("model error"),
	}

	messages := []*schema.Message{
		{Role: schema.User, Content: strings.Repeat("old context ", 200)},
		{Role: schema.Assistant, Content: strings.Repeat("assistant reply ", 180)},
		{Role: schema.User, Content: "latest question"},
		{Role: schema.Assistant, Content: "latest answer"},
	}

	result, _, tracking := AutoCompact(messages, "sdk", &CompactTracking{}, 0, "", &AutoCompactParams{
		Ctx:       context.Background(),
		ChatModel: mock,
	})

	if result == nil {
		t.Fatal("expected deterministic fallback result after LLM failure")
		return
	}
	if !tracking.Compacted {
		t.Fatal("expected tracking.Compacted = true from deterministic fallback")
	}
	// The deterministic path succeeds, so ConsecutiveFailures is reset to 0
	// (the LLM failure incremented it to 1, but the deterministic success resets it)
	if tracking.ConsecutiveFailures != 0 {
		t.Fatalf("expected 0 consecutive failures after deterministic success, got %d", tracking.ConsecutiveFailures)
	}
}
