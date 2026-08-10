package transcript

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/internal/providerorigin"
)

func testAssistantOrigin(publication uint64) providerorigin.Origin {
	return providerorigin.Origin{
		Version:             providerorigin.OriginVersion,
		Provider:            "agenticopenai",
		AccountID:           "work-openai",
		APIFamily:           providerorigin.OpenAIResponsesV1,
		APIModel:            "gpt-5.4",
		RouteIdentityDigest: strings.Repeat("a", 64),
		CredentialOriginID:  "local-record/r7",
		RoutePublication:    publication,
	}
}

func testOriginAssistant() *schema.Message {
	return &schema.Message{
		Role:             schema.Assistant,
		Content:          "answer",
		ReasoningContent: "private reasoning",
		Extra: map[string]any{
			"message_id": "assistant-1",
			"nested":     map[string]any{"number": json.Number("17")},
		},
		AssistantGenMultiContent: []schema.MessageOutputPart{
			{
				Type: schema.ChatMessagePartTypeReasoning,
				Reasoning: &schema.MessageOutputReasoning{
					Text: "signed reasoning",
				},
			},
			{Type: schema.ChatMessagePartTypeText, Text: "answer"},
		},
	}
}

func TestAssistantOriginSidecarRoundTripAndPublicExclusion(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	message := testOriginAssistant()
	recorder := NewRecorder("source", dir)
	if err := recorder.StageAssistantOrigin(message, testAssistantOrigin(9)); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordMessages([]*schema.Message{message}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(recorder.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"assistant_origins"`) ||
		!strings.Contains(string(raw), assistantOriginBindingCodec) {
		t.Fatalf("private sidecar missing from physical record: %s", raw)
	}

	reloaded := NewRecorder("source", dir)
	loaded, err := reloaded.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Messages) != 1 {
		t.Fatalf("loaded messages = %d, want 1", len(loaded.Messages))
	}
	resolution := reloaded.AssistantOriginResolver().ResolveAssistantOrigin(
		loaded.Messages[0],
	)
	if resolution.State != providerorigin.BindingVerified {
		t.Fatalf("binding state = %v, want verified", resolution.State)
	}
	if resolution.Origin.RoutePublication != 0 ||
		resolution.Origin.CredentialOriginID != "local-record/r7" {
		t.Fatalf("durable origin = %#v", resolution.Origin)
	}
	publicJSON, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(publicJSON), "local-record/r7") ||
		strings.Contains(string(publicJSON), "assistant-origin-binding") {
		t.Fatalf("public load result leaked private origin: %s", publicJSON)
	}
}

func TestAssistantOriginRejectsChangedPayloadAndMalformedSidecar(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	message := testOriginAssistant()
	recorder := NewRecorder("changed", dir)
	if err := recorder.StageAssistantOrigin(message, testAssistantOrigin(3)); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordMessages([]*schema.Message{message}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	reloaded := NewRecorder("changed", dir)
	loaded, err := reloaded.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	changed := *loaded.Messages[0]
	changed.Content = "tampered"
	resolution := reloaded.AssistantOriginResolver().ResolveAssistantOrigin(&changed)
	if resolution.State != providerorigin.BindingRecoveryMismatch {
		t.Fatalf("changed binding state = %v, want recovery mismatch", resolution.State)
	}

	malformed := NewRecorder("malformed", dir)
	line := `{"timestamp":"2026-08-02T00:00:00Z","entry_id":{"version":1,"id":"entry-1"},"kind":"assistant","message":{"role":"assistant","content":"answer","extra":{"message_id":"assistant-1"}},"assistant_origins":{"unexpected":true}}` + "\n"
	if err := os.WriteFile(malformed.Path(), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	malformedLoad, err := malformed.LoadFullContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(malformedLoad.Messages) != 1 {
		t.Fatalf("malformed sidecar hid enclosing message: %#v", malformedLoad)
	}
	resolution = malformed.AssistantOriginResolver().ResolveAssistantOrigin(
		malformedLoad.Messages[0],
	)
	if resolution.State != providerorigin.BindingLegacyUnverified {
		t.Fatalf("malformed binding state = %v, want legacy", resolution.State)
	}
}

func TestAssistantOriginBranchRebindsPhysicalRecord(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	message := testOriginAssistant()
	source := NewRecorder("parent", dir)
	if err := source.StageAssistantOrigin(message, testAssistantOrigin(5)); err != nil {
		t.Fatal(err)
	}
	if err := source.RecordMessages([]*schema.Message{message}); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	loaded, err := source.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	parentIdentity, ok := source.LatestMessageEntryIdentity(loaded.Messages[0])
	if !ok {
		t.Fatal("parent physical identity missing")
	}
	child, err := source.BranchProjectionWithState(
		"child",
		[]BranchMessage{{Message: loaded.Messages[0]}},
		BranchState{},
	)
	if err != nil {
		t.Fatal(err)
	}
	childLoad, err := child.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	resolution := child.AssistantOriginResolver().ResolveAssistantOrigin(
		childLoad.Messages[0],
	)
	if resolution.State != providerorigin.BindingVerified {
		t.Fatalf("child binding state = %v, want verified", resolution.State)
	}
	childIdentity, ok := child.LatestMessageEntryIdentity(childLoad.Messages[0])
	if !ok || childIdentity.Record.ID == parentIdentity.Record.ID {
		t.Fatalf("child identity = %#v, parent = %#v", childIdentity, parentIdentity)
	}
}

func TestAssistantOriginSurvivesLifecycleReloadAndRewrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	message := testOriginAssistant()
	recorder := NewRecorder("lifecycle", dir)
	if err := recorder.StageAssistantOrigin(message, testAssistantOrigin(8)); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordMessages([]*schema.Message{message}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordLifecycleBoundary(
		LifecycleCheckpoint,
		[]*schema.Message{message},
		nil,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}

	reloaded := NewRecorder("lifecycle", dir)
	active, err := reloaded.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if len(active.Messages) != 1 {
		t.Fatalf("active messages = %#v", active.Messages)
	}
	if resolution := reloaded.AssistantOriginResolver().ResolveAssistantOrigin(
		active.Messages[0],
	); resolution.State != providerorigin.BindingVerified {
		t.Fatalf("reloaded lifecycle origin = %#v", resolution)
	}
	if err := reloaded.Replace(active.Messages); err != nil {
		t.Fatal(err)
	}
	if err := reloaded.Close(); err != nil {
		t.Fatal(err)
	}

	final := NewRecorder("lifecycle", dir)
	finalLoad, err := final.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if resolution := final.AssistantOriginResolver().ResolveAssistantOrigin(
		finalLoad.Messages[0],
	); resolution.State != providerorigin.BindingVerified {
		t.Fatalf("rewritten lifecycle origin = %#v", resolution)
	}
}

func TestAssistantOriginCanonicalMessageGolden(t *testing.T) {
	t.Parallel()
	index := 2
	message := &schema.Message{
		Role:    schema.Assistant,
		Content: "current text",
		MultiContent: []schema.ChatMessagePart{{ //nolint:staticcheck // Golden covers the readable deprecated field.
			Type: schema.ChatMessagePartTypeText,
			Text: "legacy text",
		}},
		UserInputMultiContent: []schema.MessageInputPart{{
			Type:  schema.ChatMessagePartTypeText,
			Text:  "current input",
			Extra: map[string]any{"input": "metadata"},
		}},
		AssistantGenMultiContent: []schema.MessageOutputPart{{
			Type: schema.ChatMessagePartTypeReasoning,
			Reasoning: &schema.MessageOutputReasoning{
				Text:      "summary",
				Signature: "encrypted",
			},
			Extra:         map[string]any{"output": "metadata"},
			StreamingMeta: &schema.MessageStreamingMeta{Index: 9},
		}},
		Name: "assistant-name",
		ToolCalls: []schema.ToolCall{{
			Index: &index,
			ID:    "call-1",
			Type:  "function",
			Function: schema.FunctionCall{
				Name:      "Read",
				Arguments: `{"file_path":"README.md"}`,
			},
			Extra: map[string]any{"tool": "metadata"},
		}},
		ToolCallID: "tool-call-id",
		ToolName:   "tool-name",
		ResponseMeta: &schema.ResponseMeta{
			FinishReason: "stop",
			Usage: &schema.TokenUsage{
				PromptTokens:     1,
				CompletionTokens: 2,
				TotalTokens:      3,
			},
		},
		ReasoningContent: "legacy reasoning",
		Extra: map[string]any{
			"message_id": "golden-message",
			"a":          json.Number("1.25"),
			"z":          true,
		},
	}
	digest, err := assistantOriginPayloadDigest(message)
	if err != nil {
		t.Fatal(err)
	}
	const want = "d58bad884e774a0228ecb45ea8761af56c4ebcf4ccb097de616f433017a44ca7"
	if digest != want {
		t.Fatalf("canonical digest = %q, want %q", digest, want)
	}

	invalid := *message
	invalid.Extra = map[string]any{
		"message_id": "invalid-message",
		"nan":        math.NaN(),
	}
	if _, err := assistantOriginPayloadDigest(&invalid); err == nil {
		t.Fatal("non-canonical NaN payload was accepted")
	}
}

func TestAssistantOriginBindingRejectsPhysicalAndLogicalSwaps(t *testing.T) {
	t.Parallel()
	message := testOriginAssistant()
	digest, err := assistantOriginPayloadDigest(message)
	if err != nil {
		t.Fatal(err)
	}
	identity := EntryIdentity{Version: 1, ID: "entry-a"}
	record := assistantOriginRecord{
		BindingCodec:  assistantOriginBindingCodec,
		EntryVersion:  identity.Version,
		EntryID:       identity.ID,
		MessageIndex:  0,
		LogicalID:     "assistant-1",
		PayloadDigest: digest,
		Origin:        testAssistantOrigin(0),
	}
	if resolution := validateAssistantOriginRecord(
		identity,
		0,
		message,
		record,
	); resolution.State != providerorigin.BindingVerified {
		t.Fatalf("exact binding = %#v", resolution)
	}
	tests := []struct {
		name     string
		identity EntryIdentity
		index    int
		message  *schema.Message
		record   assistantOriginRecord
		want     providerorigin.BindingState
	}{
		{name: "physical entry", identity: EntryIdentity{Version: 1, ID: "entry-b"}, index: 0, message: message, record: record, want: providerorigin.BindingRecoveryMismatch},
		{name: "message index", identity: identity, index: 1, message: message, record: record, want: providerorigin.BindingRecoveryMismatch},
		{name: "unknown codec", identity: identity, index: 0, message: message, record: func() assistantOriginRecord {
			changed := record
			changed.BindingCodec = "assistant-origin-binding/v2"
			return changed
		}(), want: providerorigin.BindingLegacyUnverified},
		{name: "logical id", identity: identity, index: 0, message: func() *schema.Message {
			changed := *message
			changed.Extra = map[string]any{"message_id": "assistant-2"}
			return &changed
		}(), record: record, want: providerorigin.BindingRecoveryMismatch},
		{name: "payload", identity: identity, index: 0, message: func() *schema.Message { changed := *message; changed.Content = "different"; return &changed }(), record: record, want: providerorigin.BindingRecoveryMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolution := validateAssistantOriginRecord(
				test.identity,
				test.index,
				test.message,
				test.record,
			)
			if resolution.State != test.want {
				t.Fatalf("state = %v, want %v", resolution.State, test.want)
			}
		})
	}
}
