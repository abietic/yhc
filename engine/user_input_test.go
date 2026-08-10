package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/engine/provider"
)

func TestPromptInputRenderPreservesOrderAndResourceMetadata(t *testing.T) {
	title := "API schema"
	description := "The current service contract"
	mimeType := "application/json"
	size := 42
	lastModified := "2026-07-27T00:00:00Z"
	priority := 0.5
	first := "before\n\n"
	last := "after"

	rendered, err := (PromptInput{Blocks: []PromptInputBlock{
		{Text: &first},
		{ResourceLink: &PromptResourceLink{
			URI:         "file:///workspace/schema.json",
			Name:        "schema.json",
			Title:       &title,
			Description: &description,
			MIMEType:    &mimeType,
			Size:        &size,
			Annotations: &PromptResourceAnnotations{
				Audience:     []string{"user", "assistant"},
				LastModified: &lastModified,
				Priority:     &priority,
			},
		}},
		{Text: &last},
	}}).Render()
	if err != nil {
		t.Fatal(err)
	}
	want := "before\n\n\n" +
		`<resource_link>{"type":"resource_link","uri":"file:///workspace/schema.json","name":"schema.json","title":"API schema","description":"The current service contract","mimeType":"application/json","size":42,"annotations":{"audience":["user","assistant"],"lastModified":"2026-07-27T00:00:00Z","priority":0.5}}</resource_link>` +
		"\nafter"
	if rendered != want {
		t.Fatalf("rendered prompt = %q, want %q", rendered, want)
	}
}

func TestPromptInputRenderResourceOnlyIsNonEmpty(t *testing.T) {
	rendered, err := (PromptInput{Blocks: []PromptInputBlock{{
		ResourceLink: &PromptResourceLink{
			URI:  "https://example.test/context",
			Name: "context",
		},
	}}}).Render()
	if err != nil {
		t.Fatal(err)
	}
	want := `<resource_link>{"type":"resource_link","uri":"https://example.test/context","name":"context"}</resource_link>`
	if rendered != want {
		t.Fatalf("rendered prompt = %q, want %q", rendered, want)
	}
}

func TestPromptInputRenderRejectsInvalidBlocksWithoutContentLeak(t *testing.T) {
	negativeSize := -1
	largeDescription := strings.Repeat("private", maxPromptResourceDescriptorBytes)
	secret := "private"
	for _, tc := range []struct {
		name  string
		input PromptInput
		code  string
	}{
		{
			name:  "no variant",
			input: PromptInput{Blocks: []PromptInputBlock{{}}},
			code:  "invalid_block_union",
		},
		{
			name: "multiple variants",
			input: PromptInput{Blocks: []PromptInputBlock{{
				Text: &secret,
				ResourceLink: &PromptResourceLink{
					URI: "file:///secret", Name: "secret",
				},
			}}},
			code: "invalid_block_union",
		},
		{
			name: "missing uri",
			input: PromptInput{Blocks: []PromptInputBlock{{
				ResourceLink: &PromptResourceLink{Name: "secret"},
			}}},
			code: "resource_uri_required",
		},
		{
			name: "missing name",
			input: PromptInput{Blocks: []PromptInputBlock{{
				ResourceLink: &PromptResourceLink{URI: "file:///secret"},
			}}},
			code: "resource_name_required",
		},
		{
			name: "negative size",
			input: PromptInput{Blocks: []PromptInputBlock{{
				ResourceLink: &PromptResourceLink{
					URI: "file:///secret", Name: "secret", Size: &negativeSize,
				},
			}}},
			code: "resource_size_negative",
		},
		{
			name: "descriptor too large",
			input: PromptInput{Blocks: []PromptInputBlock{{
				ResourceLink: &PromptResourceLink{
					URI:         "file:///secret",
					Name:        "secret",
					Description: &largeDescription,
				},
			}}},
			code: "resource_descriptor_too_large",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.input.Render()
			var validationErr *PromptInputValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("error = %T %v", err, err)
			}
			if validationErr.BlockIndex != 0 ||
				validationErr.ReasonCode != tc.code {
				t.Fatalf("validation error = %#v", validationErr)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("error leaked prompt content: %v", err)
			}
		})
	}
}

func TestNewUserMessageBuildsMultimodalParts(t *testing.T) {
	extra := map[string]any{"request_id": "req-1"}
	message := newUserMessage("describe this", extra, []UserImage{
		{
			Name: "screen.png", Path: "/tmp/screen.png",
			MIMEType: "image/png", Base64Data: testUserImagePNGBase64,
		},
	})

	if message.Role != schema.User || message.Content != "describe this" {
		t.Fatalf("message = %#v", message)
	}
	if len(message.UserInputMultiContent) != 2 {
		t.Fatalf("parts = %#v", message.UserInputMultiContent)
	}
	if message.UserInputMultiContent[0].Type != schema.ChatMessagePartTypeText ||
		message.UserInputMultiContent[0].Text != "describe this" {
		t.Fatalf("text part = %#v", message.UserInputMultiContent[0])
	}
	image := message.UserInputMultiContent[1]
	if image.Type != schema.ChatMessagePartTypeImageURL || image.Image == nil ||
		image.Image.Base64Data == nil ||
		*image.Image.Base64Data != testUserImagePNGBase64 ||
		image.Image.MIMEType != "image/png" {
		t.Fatalf("image part = %#v", image)
	}
	if len(image.Extra) != 0 {
		t.Fatalf("image leaked provenance metadata = %#v", image.Extra)
	}

	extra["request_id"] = "mutated"
	if message.Extra["request_id"] != "req-1" {
		t.Fatal("message extra aliases caller metadata")
	}
}

func TestP300FlattenedPromptPrecedesAllImages(t *testing.T) {
	message := newUserMessage(
		"before [Image #1] after",
		nil,
		[]UserImage{{
			Name:       "screen.png",
			Path:       "/tmp/screen.png",
			MIMEType:   "image/png",
			Base64Data: testUserImagePNGBase64,
		}},
	)

	if len(message.UserInputMultiContent) != 2 {
		t.Fatalf("parts = %#v", message.UserInputMultiContent)
	}
	if got := message.UserInputMultiContent[0]; got.Type != schema.ChatMessagePartTypeText ||
		got.Text != "before [Image #1] after" {
		t.Fatalf("first part = %#v", got)
	}
	if got := message.UserInputMultiContent[1]; got.Type != schema.ChatMessagePartTypeImageURL ||
		got.Image == nil {
		t.Fatalf("second part = %#v", got)
	}
}

func TestSubmitMessageWithImagesRejectsInvalidImageBeforeModel(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		image      UserImage
	}{
		{"missing data", "missing_base64_data", UserImage{MIMEType: "image/png", Name: "name-secret", Path: "/path-secret"}},
		{"missing mime", "missing_mime_type", UserImage{Base64Data: "bytes-secret", Name: "name-secret", Path: "/path-secret"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chatModel, hookCalls := &captureInputModel{}, 0
			executor := hooks.NewExecutor()
			executor.RegisterUserPromptSubmit(func(context.Context, string) *hooks.UserPromptSubmitHookResult { hookCalls++; return nil })
			transcriptDir := t.TempDir()
			engine := NewQueryEngine(QueryEngineConfig{ChatModel: chatModel, CWD: t.TempDir(), TranscriptDir: transcriptDir, HookExecutor: executor})
			t.Cleanup(engine.Close)
			events, terminal := engine.SubmitMessageWithImages(context.Background(), "inspect", []UserImage{tc.image})
			var imageErr *UserImageValidationError
			if terminal.Reason != TerminalImageError || !errors.As(terminal.Err, &imageErr) || imageErr.ImageIndex != 0 || imageErr.ReasonCode != tc.code || strings.Contains(terminal.Err.Error(), "secret") {
				t.Fatalf("terminal = %#v", terminal)
			}
			count := 0
			for event := range events {
				count++
				if event.Type != EventTerminal || event.TerminalInfo == nil || event.TerminalInfo.Err != terminal.Err {
					t.Fatalf("event = %#v", event)
				}
			}
			if count != 1 || hookCalls != 0 || len(chatModel.inputs) != 0 || len(engine.GetMessages()) != 0 {
				t.Fatalf("events=%d hooks=%d inputs=%d history=%#v", count, hookCalls, len(chatModel.inputs), engine.GetMessages())
			}
			loaded, err := engine.GetTranscript().LoadFull()
			if err != nil {
				t.Fatal(err)
			}
			if len(loaded.Messages) != 0 {
				t.Fatalf("invalid image reached transcript: %#v", loaded.Messages)
			}
		})
	}
}

func TestSubmitMessageWithImagesReachesModelAndHistory(t *testing.T) {
	chatModel := &captureInputModel{}
	engine := NewQueryEngine(QueryEngineConfig{
		ChatModel: chatModel, CWD: t.TempDir(), TranscriptDir: t.TempDir(), MaxTurns: 2,
		Model: "gpt-4o",
		ModelResolver: ModelResolverFunc(func(string) (provider.ResolvedConfig, error) {
			return provider.ResolvedConfig{
				Config: provider.Config{
					Provider: provider.ProviderAgenticOpenAI,
					Model:    "gpt-4o",
				},
			}, nil
		}),
		PromptCapabilityResolver: DefaultPromptCapabilityResolver(),
	})
	t.Cleanup(engine.Close)
	images := []UserImage{{
		Name: "screen.png", Path: "/private/screen.png",
		MIMEType: " IMAGE/PNG ", Base64Data: testUserImagePNGBase64,
	}}
	events, _ := engine.SubmitMessageWithImages(
		context.Background(),
		"inspect",
		images,
	)
	images[0] = UserImage{
		Name:       "mutated-secret",
		Path:       "/mutated-secret",
		MIMEType:   "image/jpeg",
		Base64Data: "mutated-secret",
	}
	for range events {
	}

	if len(chatModel.inputs) == 0 {
		t.Fatal("multimodal submit never reached the model")
	}
	var user *schema.Message
	for _, message := range chatModel.inputs[0] {
		if message.Role == schema.User && message.Content == "inspect" {
			user = message
			break
		}
	}
	if user == nil || len(user.UserInputMultiContent) != 2 || user.UserInputMultiContent[1].Image == nil {
		t.Fatalf("model user input = %#v", user)
	}
	if user.UserInputMultiContent[1].Image.Base64Data == nil ||
		*user.UserInputMultiContent[1].Image.Base64Data != testUserImagePNGBase64 ||
		user.UserInputMultiContent[1].Image.MIMEType != "image/png" {
		t.Fatalf("model input did not retain admitted snapshot: %#v", user.UserInputMultiContent[1])
	}
	if len(user.UserInputMultiContent[1].Extra) != 0 {
		t.Fatalf("model input leaked image provenance: %#v", user.UserInputMultiContent[1].Extra)
	}
	messages := engine.GetMessages()
	if len(messages) < 2 || len(messages[0].UserInputMultiContent) != 2 {
		t.Fatalf("conversation history lost image parts: %#v", messages)
	}
	if len(messages[0].UserInputMultiContent[1].Extra) != 0 {
		t.Fatalf("history leaked image provenance: %#v", messages[0].UserInputMultiContent[1].Extra)
	}
}

func TestQueryPreservesMultipartInputAtModelBoundary(t *testing.T) {
	data := "cG5n"
	input := &schema.Message{
		Role:    schema.User,
		Content: "inspect",
		UserInputMultiContent: []schema.MessageInputPart{
			{Type: schema.ChatMessagePartTypeText, Text: "inspect"},
			{
				Type: schema.ChatMessagePartTypeImageURL,
				Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{
					Base64Data: &data,
					MIMEType:   "image/png",
				}},
			},
		},
	}
	chatModel := &captureInputModel{}
	maxTurns := 2
	terminal := Query(context.Background(), QueryParams{
		Messages:     []*schema.Message{input},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "system"},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    chatModel,
	}, nil)
	if terminal.Reason != TerminalCompleted || len(chatModel.inputs) != 1 {
		t.Fatalf("terminal=%#v inputs=%#v", terminal, chatModel.inputs)
	}
	var captured *schema.Message
	for _, message := range chatModel.inputs[0] {
		if message.Role == schema.User && message.Content == "inspect" {
			captured = message
			break
		}
	}
	if captured == nil ||
		len(captured.UserInputMultiContent) != 2 ||
		captured.UserInputMultiContent[1].Image == nil ||
		captured.UserInputMultiContent[1].Image.Base64Data == nil ||
		*captured.UserInputMultiContent[1].Image.Base64Data != data ||
		captured.UserInputMultiContent[1].Image.MIMEType != "image/png" {
		t.Fatalf("captured input=%#v", captured)
	}
}

func TestNewUserMessageKeepsTextOnlyShape(t *testing.T) {
	message := newUserMessage("plain", nil, nil)
	if len(message.UserInputMultiContent) != 0 || message.Content != "plain" {
		t.Fatalf("text-only message = %#v", message)
	}
}
