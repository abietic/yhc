package acp

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
	acpsdk "github.com/coder/acp-go-sdk"
)

const p305aACPImageBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="

func TestP305aRichNewResumeAndLoadBoundary(t *testing.T) {
	model := &mockChatModel{responses: []*schema.Message{
		{Role: schema.Assistant, Content: "first"},
		{Role: schema.Assistant, Content: "second"},
		{Role: schema.Assistant, Content: "third"},
		{Role: schema.Assistant, Content: "fourth"},
		{Role: schema.Assistant, Content: "fifth"},
		{Role: schema.Assistant, Content: "sixth"},
		{Role: schema.Assistant, Content: "seventh"},
		{Role: schema.Assistant, Content: "eighth"},
	}}
	conn, client, agent := setupP305aACP(t, model, "gpt-4o")
	initialize, err := conn.Initialize(t.Context(), acpsdk.InitializeRequest{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !initialize.AgentCapabilities.LoadSession ||
		!initialize.AgentCapabilities.PromptCapabilities.Image ||
		!initialize.AgentCapabilities.PromptCapabilities.EmbeddedContext ||
		initialize.AgentCapabilities.PromptCapabilities.Audio {
		t.Fatalf("capabilities = %#v", initialize.AgentCapabilities)
	}

	cwd := t.TempDir()
	session, err := conn.NewSession(t.Context(), acpsdk.NewSessionRequest{
		Cwd:        cwd,
		McpServers: []acpsdk.McpServer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	lastModified := "2026-07-30T00:00:00Z"
	priority := 0.5
	resource := acpsdk.ResourceLinkBlock(
		"schema.json",
		"file:///workspace/schema.json",
	)
	resource.ResourceLink.Annotations = &acpsdk.Annotations{
		Meta:         map[string]any{"reserved": "must-not-persist"},
		Audience:     []acpsdk.Role{acpsdk.RoleAssistant},
		LastModified: &lastModified,
		Priority:     &priority,
	}
	imageURI := "file:///private/image-source-must-not-persist.png"
	image := acpsdk.ImageBlock(p305aACPImageBase64, "image/png")
	image.Image.Uri = &imageURI
	image.Image.Meta = map[string]any{"reserved": "must-not-persist"}
	image.Image.Annotations = &acpsdk.Annotations{
		Audience: []acpsdk.Role{acpsdk.RoleUser},
	}
	textMIME := "text/plain"
	blobMIME := "image/png"
	embeddedText := acpsdk.ResourceBlock(acpsdk.EmbeddedResourceResource{
		TextResourceContents: &acpsdk.TextResourceContents{
			Uri:      "file:///workspace/context.txt",
			MimeType: &textMIME,
			Text:     "embedded context",
			Meta:     map[string]any{"reserved": "must-not-persist"},
		},
	})
	embeddedText.Resource.Annotations = &acpsdk.Annotations{
		Audience: []acpsdk.Role{acpsdk.RoleAssistant},
	}
	embeddedBlob := acpsdk.ResourceBlock(acpsdk.EmbeddedResourceResource{
		BlobResourceContents: &acpsdk.BlobResourceContents{
			Uri:      "file:///workspace/pixel.png",
			MimeType: &blobMIME,
			Blob:     p305aACPImageBase64,
			Meta:     map[string]any{"reserved": "must-not-persist"},
		},
	})

	response, err := conn.Prompt(t.Context(), acpsdk.PromptRequest{
		SessionId: session.SessionId,
		Prompt: []acpsdk.ContentBlock{
			acpsdk.TextBlock("/help"),
			resource,
			image,
			embeddedText,
			embeddedBlob,
			acpsdk.TextBlock("tail"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.StopReason != acpsdk.StopReasonEndTurn ||
		model.CallCount() != 1 {
		t.Fatalf("response=%#v modelCalls=%d", response, model.CallCount())
	}
	agent.mu.Lock()
	active := agent.sessions[session.SessionId]
	agent.mu.Unlock()
	if active == nil {
		t.Fatal("active session missing")
	}
	transcriptPath := active.Engine.GetTranscript().Path()
	messages := active.Engine.GetMessages()
	if len(messages) < 2 || messages[0].Role != schema.User {
		t.Fatalf("messages = %#v", messages)
	}
	user := messages[0]
	if len(user.UserInputMultiContent) != 7 {
		t.Fatalf("ordered rich parts = %#v", user.UserInputMultiContent)
	}
	assertP305aACPTextPart(t, user.UserInputMultiContent[0], "/help")
	assertP305aACPTextContains(
		t,
		user.UserInputMultiContent[1],
		`"type":"resource_link"`,
	)
	assertP305aACPImagePart(t, user.UserInputMultiContent[2])
	assertP305aACPTextContains(
		t,
		user.UserInputMultiContent[3],
		`"kind":"text"`,
	)
	assertP305aACPTextContains(
		t,
		user.UserInputMultiContent[4],
		`"kind":"blob"`,
	)
	assertP305aACPImagePart(t, user.UserInputMultiContent[5])
	assertP305aACPTextPart(t, user.UserInputMultiContent[6], "tail")

	for _, prompt := range [][]acpsdk.ContentBlock{
		{image},
		{embeddedText},
	} {
		before := model.CallCount()
		if _, err := conn.Prompt(t.Context(), acpsdk.PromptRequest{
			SessionId: session.SessionId,
			Prompt:    prompt,
		}); err != nil {
			t.Fatal(err)
		}
		if model.CallCount() <= before {
			t.Fatalf("rich-only prompt made no model call")
		}
	}
	liveCalls := model.CallCount()

	raw, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		p305aACPImageBase64,
		imageURI,
		"must-not-persist",
		`"_meta"`,
	} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatalf("transcript leaked %q", forbidden)
		}
	}
	for _, required := range []string{
		`"version":2`,
		`"kind":"resource_link"`,
		`"kind":"embedded_text"`,
		`"kind":"embedded_blob"`,
		`"annotations"`,
		lastModified,
	} {
		if !bytes.Contains(raw, []byte(required)) {
			t.Fatalf("transcript missing %q: %s", required, raw)
		}
	}

	if _, err := conn.CloseSession(t.Context(), acpsdk.CloseSessionRequest{
		SessionId: session.SessionId,
	}); err != nil {
		t.Fatal(err)
	}
	beforeLoadTree := readP305bACPReplayTree(t, filepath.Dir(transcriptPath))
	beforeLoadUpdates := len(client.getUpdates())
	loadResponse, err := conn.LoadSession(t.Context(), acpsdk.LoadSessionRequest{
		SessionId:  session.SessionId,
		Cwd:        cwd,
		McpServers: []acpsdk.McpServer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if loadResponse.Modes == nil || len(loadResponse.ConfigOptions) == 0 {
		t.Fatalf("rich load response = %#v", loadResponse)
	}
	loadUpdates := client.getUpdates()[beforeLoadUpdates:]
	requireP305bRichLoadUpdates(t, loadUpdates, lastModified, priority)
	if model.CallCount() != liveCalls {
		t.Fatalf("rich load executed model: before=%d after=%d", liveCalls, model.CallCount())
	}
	afterLoadTree := readP305bACPReplayTree(t, filepath.Dir(transcriptPath))
	if !reflect.DeepEqual(beforeLoadTree, afterLoadTree) {
		t.Fatal("rich load rewrote transcript or MediaStore bytes")
	}
	if _, err := conn.CloseSession(t.Context(), acpsdk.CloseSessionRequest{
		SessionId: session.SessionId,
	}); err != nil {
		t.Fatal(err)
	}

	beforeResumeUpdates := len(client.getUpdates())
	if _, err := conn.ResumeSession(t.Context(), acpsdk.ResumeSessionRequest{
		SessionId:  session.SessionId,
		Cwd:        cwd,
		McpServers: []acpsdk.McpServer{},
	}); err != nil {
		t.Fatal(err)
	}
	for _, update := range client.getUpdates()[beforeResumeUpdates:] {
		if update.Update.UserMessageChunk != nil ||
			update.Update.AgentMessageChunk != nil ||
			update.Update.ToolCall != nil ||
			update.Update.ToolCallUpdate != nil {
			t.Fatalf("rich resume replayed historical update: %#v", update.Update)
		}
	}
	if _, err := conn.Prompt(t.Context(), acpsdk.PromptRequest{
		SessionId: session.SessionId,
		Prompt:    []acpsdk.ContentBlock{embeddedBlob},
	}); err != nil {
		t.Fatal(err)
	}
	if model.CallCount() <= liveCalls {
		t.Fatalf("resume rich model calls = %d", model.CallCount())
	}
}

func TestP305aRichAdmissionErrorsAreIndexedAndRedacted(t *testing.T) {
	for _, tc := range []struct {
		name      string
		modelName string
		block     acpsdk.ContentBlock
		code      int
		input     string
		reason    string
	}{
		{
			name:      "invalid base64",
			modelName: "gpt-4o",
			block:     acpsdk.ImageBlock("private-invalid-base64", "image/png"),
			code:      CodeInvalidParams,
			input:     "prompt.image",
			reason:    "invalid_base64_data",
		},
		{
			name:      "selected route unsupported",
			modelName: "o3-mini",
			block:     acpsdk.ImageBlock(p305aACPImageBase64, "image/png"),
			code:      codeUnsupportedInput,
			input:     "prompt.image",
			reason:    "capability_unsupported",
		},
		{
			name:      "embedded blob MIME required",
			modelName: "gpt-4o",
			block: acpsdk.ResourceBlock(acpsdk.EmbeddedResourceResource{
				BlobResourceContents: &acpsdk.BlobResourceContents{
					Uri:  "file:///private",
					Blob: p305aACPImageBase64,
				},
			}),
			code:   CodeInvalidParams,
			input:  "prompt.embeddedResource",
			reason: "embedded_mime_required",
		},
		{
			name:      "embedded blob safe raster required",
			modelName: "gpt-4o",
			block: acpsdk.ResourceBlock(acpsdk.EmbeddedResourceResource{
				BlobResourceContents: &acpsdk.BlobResourceContents{
					Uri:      "file:///private",
					MimeType: p305aStringPointer("text/plain"),
					Blob:     p305aACPImageBase64,
				},
			}),
			code:   CodeInvalidParams,
			input:  "prompt.embeddedResource",
			reason: "unsupported_mime_type",
		},
		{
			name:      "image MIME mismatch",
			modelName: "gpt-4o",
			block:     acpsdk.ImageBlock(p305aACPImageBase64, "image/jpeg"),
			code:      CodeInvalidParams,
			input:     "prompt.image",
			reason:    "mime_type_mismatch",
		},
		{
			name:      "resource descriptor bounded",
			modelName: "gpt-4o",
			block: acpsdk.ResourceLinkBlock(
				strings.Repeat("x", 17*1024),
				"file:///private",
			),
			code:   CodeInvalidParams,
			input:  "prompt.resourceLink",
			reason: "resource_descriptor_too_large",
		},
		{
			name:      "embedded text bounded",
			modelName: "gpt-4o",
			block: acpsdk.ResourceBlock(acpsdk.EmbeddedResourceResource{
				TextResourceContents: &acpsdk.TextResourceContents{
					Uri:  "file:///private",
					Text: strings.Repeat("x", 17*1024),
				},
			}),
			code:   CodeInvalidParams,
			input:  "prompt.embeddedResource",
			reason: "resource_descriptor_too_large",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &mockChatModel{responses: []*schema.Message{{
				Role: schema.Assistant, Content: "must not run",
			}}}
			conn, _, agent := setupP305aACP(t, model, tc.modelName)
			session, err := conn.NewSession(
				t.Context(),
				acpsdk.NewSessionRequest{
					Cwd:        t.TempDir(),
					McpServers: []acpsdk.McpServer{},
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = conn.Prompt(t.Context(), acpsdk.PromptRequest{
				SessionId: session.SessionId,
				Prompt: []acpsdk.ContentBlock{
					acpsdk.TextBlock("private prompt"),
					tc.block,
				},
			})
			var requestErr *acpsdk.RequestError
			if !errors.As(err, &requestErr) || requestErr.Code != tc.code {
				t.Fatalf("request error = %#v", requestErr)
			}
			data, ok := requestErr.Data.(map[string]any)
			if !ok ||
				data["input"] != tc.input ||
				data["reason"] != tc.reason ||
				data["block"] != float64(1) {
				t.Fatalf("error data = %#v", requestErr.Data)
			}
			if strings.Contains(requestErr.Error(), "private") ||
				model.CallCount() != 0 {
				t.Fatalf(
					"error leaked or mutated model: %v calls=%d",
					requestErr,
					model.CallCount(),
				)
			}
			agent.mu.Lock()
			active := agent.sessions[session.SessionId]
			agent.mu.Unlock()
			if active == nil || len(active.Engine.GetMessages()) != 0 {
				t.Fatalf("rejected prompt mutated Session: %#v", active)
			}
		})
	}
}

func p305aStringPointer(value string) *string {
	return &value
}

func TestP305aRichPromptUsesCurrentSelectedModel(t *testing.T) {
	model := &mockChatModel{responses: []*schema.Message{{
		Role: schema.Assistant, Content: "accepted",
	}}}
	conn, _, _ := setupP305aACP(t, model, "gpt-4o")
	session, err := conn.NewSession(t.Context(), acpsdk.NewSessionRequest{
		Cwd:        t.TempDir(),
		McpServers: []acpsdk.McpServer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	setModel := func(modelID string) {
		t.Helper()
		_, setErr := conn.SetSessionConfigOption(
			t.Context(),
			acpsdk.SetSessionConfigOptionRequest{
				ValueId: &acpsdk.SetSessionConfigOptionValueId{
					SessionId: session.SessionId,
					ConfigId:  acpsdk.SessionConfigId("model"),
					Value:     acpsdk.SessionConfigValueId(modelID),
				},
			},
		)
		if setErr != nil {
			t.Fatal(setErr)
		}
	}

	setModel("o3-mini")
	_, err = conn.Prompt(t.Context(), acpsdk.PromptRequest{
		SessionId: session.SessionId,
		Prompt: []acpsdk.ContentBlock{
			acpsdk.ImageBlock(p305aACPImageBase64, "image/png"),
		},
	})
	requireP305aUnsupported(t, err, "prompt.image", 0)
	if model.CallCount() != 0 {
		t.Fatalf("unsupported selected model calls = %d", model.CallCount())
	}

	setModel("gpt-4o")
	if _, err := conn.Prompt(t.Context(), acpsdk.PromptRequest{
		SessionId: session.SessionId,
		Prompt: []acpsdk.ContentBlock{
			acpsdk.ImageBlock(p305aACPImageBase64, "image/png"),
		},
	}); err != nil {
		t.Fatal(err)
	}
	if model.CallCount() != 1 {
		t.Fatalf("supported selected model calls = %d", model.CallCount())
	}
}

func TestP305aMalformedUnionPrecedesUnknownSession(t *testing.T) {
	agent, err := NewAgent(Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(agent.Close)
	block := acpsdk.TextBlock("private")
	block.Image = &acpsdk.ContentBlockImage{
		Type:     "image",
		Data:     p305aACPImageBase64,
		MimeType: "image/png",
	}
	_, err = agent.Prompt(t.Context(), acpsdk.PromptRequest{
		SessionId: "private-missing-session",
		Prompt:    []acpsdk.ContentBlock{block},
	})
	var requestErr *acpsdk.RequestError
	if !errors.As(err, &requestErr) ||
		requestErr.Code != CodeInvalidParams ||
		strings.Contains(requestErr.Error(), "private") {
		t.Fatalf("request error = %#v", requestErr)
	}

	for _, tc := range []struct {
		name   string
		block  acpsdk.ContentBlock
		input  string
		reason string
	}{
		{
			name:   "image data required",
			block:  acpsdk.ImageBlock("", "image/png"),
			input:  "prompt.image",
			reason: "invalid_base64_data",
		},
		{
			name:   "image MIME required",
			block:  acpsdk.ImageBlock(p305aACPImageBase64, ""),
			input:  "prompt.image",
			reason: "unsupported_mime_type",
		},
		{
			name: "embedded blob data required",
			block: acpsdk.ResourceBlock(acpsdk.EmbeddedResourceResource{
				BlobResourceContents: &acpsdk.BlobResourceContents{
					Uri:      "file:///private",
					MimeType: p305aStringPointer("image/png"),
				},
			}),
			input:  "prompt.embeddedResource",
			reason: "invalid_base64_data",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, promptErr := agent.Prompt(
				t.Context(),
				acpsdk.PromptRequest{
					SessionId: "private-missing-session",
					Prompt:    []acpsdk.ContentBlock{tc.block},
				},
			)
			var requiredErr *acpsdk.RequestError
			if !errors.As(promptErr, &requiredErr) ||
				requiredErr.Code != CodeInvalidParams {
				t.Fatalf("request error = %#v", requiredErr)
			}
			data, ok := requiredErr.Data.(map[string]any)
			if !ok ||
				data["input"] != tc.input ||
				data["reason"] != tc.reason ||
				data["block"] != 0 ||
				len(data) != 3 {
				t.Fatalf("error data = %#v", requiredErr.Data)
			}
			if strings.Contains(requiredErr.Error(), "private") {
				t.Fatalf("request error leaked content: %v", requiredErr)
			}
		})
	}
}

func setupP305aACP(
	t *testing.T,
	mockModel *mockChatModel,
	modelName string,
) (*acpsdk.ClientSideConnection, *testClient, *Agent) {
	t.Helper()
	agent, err := NewAgent(Config{
		ProviderFlag: "mock",
		ModelFlag:    modelName,
		APIKeyFlag:   "mock-key",
		YoloMode:     true,
		MaxTurns:     10,
		CWD:          t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	agent.mockModel = mockModel
	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()
	client := &testClient{}
	agentConnection := acpsdk.NewAgentSideConnection(agent, a2cW, c2aR)
	agent.SetConnection(agentConnection)
	clientConnection := acpsdk.NewClientSideConnection(client, c2aW, a2cR)
	t.Cleanup(func() {
		agent.Close()
		_ = c2aW.Close()
		_ = a2cW.Close()
	})
	return clientConnection, client, agent
}

func readP305bACPReplayTree(t *testing.T, root string) map[string][]byte {
	t.Helper()
	files := make(map[string][]byte)
	err := filepath.WalkDir(root, func(
		path string,
		entry os.DirEntry,
		err error,
	) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[relative] = data
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func requireP305bRichLoadUpdates(
	t *testing.T,
	updates []acpsdk.SessionNotification,
	lastModified string,
	priority float64,
) {
	t.Helper()
	users := make([]*acpsdk.SessionUpdateUserMessageChunk, 0)
	assistants := 0
	for _, update := range updates {
		if update.Update.UserMessageChunk != nil {
			users = append(users, update.Update.UserMessageChunk)
		}
		if update.Update.AgentMessageChunk != nil {
			assistants++
		}
	}
	if len(users) != 8 || assistants != 3 {
		t.Fatalf(
			"rich load conversation updates: users=%d assistants=%d trace=%#v",
			len(users),
			assistants,
			updates,
		)
	}
	if users[0].MessageId == nil {
		t.Fatal("rich load first message has no ID")
	}
	firstMessageID := *users[0].MessageId
	for index := 0; index < 6; index++ {
		if users[index].MessageId == nil ||
			*users[index].MessageId != firstMessageID {
			t.Fatalf("rich load part %d message ID = %#v", index, users[index].MessageId)
		}
	}
	if users[6].MessageId == nil ||
		users[7].MessageId == nil ||
		*users[6].MessageId == firstMessageID ||
		*users[7].MessageId == firstMessageID ||
		*users[6].MessageId == *users[7].MessageId {
		t.Fatalf(
			"rich load logical message IDs = %q %#v %#v",
			firstMessageID,
			users[6].MessageId,
			users[7].MessageId,
		)
	}

	if text := users[0].Content.Text; text == nil || text.Text != "/help" {
		t.Fatalf("rich load text = %#v", users[0].Content)
	}
	resource := users[1].Content.ResourceLink
	if resource == nil ||
		resource.Uri != "file:///workspace/schema.json" ||
		resource.Name != "schema.json" ||
		resource.Annotations == nil ||
		len(resource.Annotations.Audience) != 1 ||
		resource.Annotations.Audience[0] != acpsdk.RoleAssistant ||
		resource.Annotations.LastModified == nil ||
		*resource.Annotations.LastModified != lastModified ||
		resource.Annotations.Priority == nil ||
		*resource.Annotations.Priority != priority ||
		len(resource.Meta) != 0 {
		t.Fatalf("rich load resource link = %#v", resource)
	}
	image := users[2].Content.Image
	if image == nil ||
		image.Data != p305aACPImageBase64 ||
		image.MimeType != "image/png" ||
		image.Uri != nil ||
		image.Annotations == nil ||
		len(image.Annotations.Audience) != 1 ||
		image.Annotations.Audience[0] != acpsdk.RoleUser ||
		len(image.Meta) != 0 {
		t.Fatalf("rich load image = %#v", image)
	}
	embeddedText := users[3].Content.Resource
	if embeddedText == nil ||
		embeddedText.Resource.TextResourceContents == nil ||
		embeddedText.Resource.BlobResourceContents != nil ||
		embeddedText.Resource.TextResourceContents.Uri !=
			"file:///workspace/context.txt" ||
		embeddedText.Resource.TextResourceContents.Text != "embedded context" ||
		embeddedText.Annotations == nil ||
		len(embeddedText.Annotations.Audience) != 1 ||
		embeddedText.Annotations.Audience[0] != acpsdk.RoleAssistant ||
		len(embeddedText.Meta) != 0 ||
		len(embeddedText.Resource.TextResourceContents.Meta) != 0 {
		t.Fatalf("rich load embedded text = %#v", embeddedText)
	}
	embeddedBlob := users[4].Content.Resource
	if embeddedBlob == nil ||
		embeddedBlob.Resource.BlobResourceContents == nil ||
		embeddedBlob.Resource.TextResourceContents != nil ||
		embeddedBlob.Resource.BlobResourceContents.Uri !=
			"file:///workspace/pixel.png" ||
		embeddedBlob.Resource.BlobResourceContents.Blob != p305aACPImageBase64 ||
		embeddedBlob.Resource.BlobResourceContents.MimeType == nil ||
		*embeddedBlob.Resource.BlobResourceContents.MimeType != "image/png" ||
		len(embeddedBlob.Meta) != 0 ||
		len(embeddedBlob.Resource.BlobResourceContents.Meta) != 0 {
		t.Fatalf("rich load embedded blob = %#v", embeddedBlob)
	}
	if text := users[5].Content.Text; text == nil || text.Text != "tail" {
		t.Fatalf("rich load tail = %#v", users[5].Content)
	}
	if users[6].Content.Image == nil ||
		users[6].Content.Image.Data != p305aACPImageBase64 ||
		users[7].Content.Resource == nil ||
		users[7].Content.Resource.Resource.TextResourceContents == nil {
		t.Fatalf("rich-only load chunks = %#v / %#v", users[6], users[7])
	}
}

func assertP305aACPTextPart(
	t *testing.T,
	part schema.MessageInputPart,
	want string,
) {
	t.Helper()
	if part.Type != schema.ChatMessagePartTypeText || part.Text != want {
		t.Fatalf("text part = %#v, want %q", part, want)
	}
}

func assertP305aACPTextContains(
	t *testing.T,
	part schema.MessageInputPart,
	want string,
) {
	t.Helper()
	if part.Type != schema.ChatMessagePartTypeText ||
		!strings.Contains(part.Text, want) {
		t.Fatalf("text part = %#v, want substring %q", part, want)
	}
}

func assertP305aACPImagePart(t *testing.T, part schema.MessageInputPart) {
	t.Helper()
	if part.Type != schema.ChatMessagePartTypeImageURL ||
		part.Image == nil ||
		part.Image.Base64Data == nil ||
		*part.Image.Base64Data != p305aACPImageBase64 ||
		part.Image.MIMEType != "image/png" {
		t.Fatalf("image part = %#v", part)
	}
}

func requireP305aUnsupported(
	t *testing.T,
	err error,
	input string,
	block int,
) {
	t.Helper()
	var requestErr *acpsdk.RequestError
	if !errors.As(err, &requestErr) ||
		requestErr.Code != codeUnsupportedInput {
		t.Fatalf("request error = %#v", requestErr)
	}
	data, ok := requestErr.Data.(map[string]any)
	if !ok || data["input"] != input {
		t.Fatalf("error data = %#v", requestErr.Data)
	}
	if block >= 0 && data["block"] != float64(block) {
		t.Fatalf("error block = %#v", requestErr.Data)
	}
}

func TestP305aNoResourceURIEgress(t *testing.T) {
	input, err := promptInputFromACP([]acpsdk.ContentBlock{
		acpsdk.ResourceLinkBlock(
			"context",
			"https://127.0.0.1:1/must-not-be-fetched",
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	if input.Rich == nil {
		t.Fatal("ResourceLink did not retain typed rich identity")
	}
	rendered, err := input.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "https://127.0.0.1:1/must-not-be-fetched") {
		t.Fatalf("descriptor = %q", rendered)
	}
}
