package acp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/google/uuid"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
)

func TestP234bACPReplayProjectionPreservesOrderBytesAndToolFacts(t *testing.T) {
	dir := t.TempDir()
	const sessionID = "p234b-wire"
	p234bWriteReplayTranscript(
		t,
		dir,
		sessionID,
		p234bReplayRecord("entry-user", &schema.Message{
			Role:    schema.User,
			Content: " user\n\n",
		}),
		p234bReplayRecord("entry-assistant", &schema.Message{
			Role:    schema.Assistant,
			Content: "a\n\n b",
			Extra: map[string]any{
				"message_id": "11111111-1111-4111-8111-111111111111",
			},
			ToolCalls: []schema.ToolCall{{
				ID:   "call-read",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Read",
					Arguments: `{"path":"/tmp/input"}`,
				},
			}},
		}),
		p234bReplayRecord("entry-tool-success", &schema.Message{
			Role:       schema.Tool,
			ToolCallID: "call-read",
			Content:    `{"ok":true}`,
		}),
		p234bReplayRecord("entry-fallback", &schema.Message{
			Role:    schema.Assistant,
			Content: "fallback",
		}),
		p234bReplayRecord("entry-tool-call-failed", &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:   "call-bash",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Bash",
					Arguments: `{"command":"false"}`,
				},
			}},
		}),
		p234bReplayRecord("entry-tool-failed", &schema.Message{
			Role:       schema.Tool,
			ToolCallID: "call-bash",
			Content:    "exit 1",
			Extra:      map[string]any{"is_error": true},
		}),
	)
	snapshot, err := session.LoadSessionReplaySnapshot(
		t.Context(),
		session.ResumeOptions{SessionID: sessionID, SessionDir: dir},
	)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := buildACPReplayProjection(snapshot, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.updates) != 7 {
		t.Fatalf("updates = %d, want 7", len(projection.updates))
	}
	user := projection.updates[0].UserMessageChunk
	if user == nil || user.Content.Text == nil ||
		user.Content.Text.Text != " user\n\n" ||
		user.MessageId == nil {
		t.Fatalf("user update = %#v", projection.updates[0])
	}
	if got, want := *user.MessageId, "66013c7f-c7de-5198-823a-1cff455c3dc7"; got != want {
		t.Fatalf("user UUID = %q, want %q", got, want)
	}
	assistant := projection.updates[1].AgentMessageChunk
	if assistant == nil || assistant.Content.Text == nil ||
		assistant.Content.Text.Text != "a\n\n b" ||
		assistant.MessageId == nil ||
		*assistant.MessageId != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("assistant update = %#v", projection.updates[1])
	}
	start := projection.updates[2].ToolCall
	if start == nil ||
		start.ToolCallId != "call-read" ||
		start.Status != acpsdk.ToolCallStatusInProgress ||
		start.RawInput == nil ||
		len(start.Locations) != 1 ||
		start.Locations[0].Path != "/tmp/input" {
		t.Fatalf("tool start = %#v", projection.updates[2])
	}
	terminal := projection.updates[3].ToolCallUpdate
	if terminal == nil ||
		terminal.ToolCallId != "call-read" ||
		terminal.Status == nil ||
		*terminal.Status != acpsdk.ToolCallStatusCompleted {
		t.Fatalf("tool terminal = %#v", projection.updates[3])
	}
	if terminal.RawOutput != `{"ok":true}` ||
		len(terminal.Content) != 1 ||
		terminal.Content[0].Content == nil ||
		terminal.Content[0].Content.Content.Text == nil ||
		terminal.Content[0].Content.Content.Text.Text != `{"ok":true}` {
		t.Fatalf("tool output = %#v", terminal)
	}
	fallback := projection.updates[4].AgentMessageChunk
	if fallback == nil || fallback.MessageId == nil {
		t.Fatalf("fallback assistant = %#v", projection.updates[4])
	}
	if got, want := *fallback.MessageId, "739554bf-b3ad-5b33-8b60-b26687caf85d"; got != want {
		t.Fatalf("fallback UUID = %q, want %q", got, want)
	}
	failed := projection.updates[6].ToolCallUpdate
	if failed == nil || failed.Status == nil ||
		*failed.Status != acpsdk.ToolCallStatusFailed ||
		failed.RawOutput != "exit 1" {
		t.Fatalf("failed tool terminal = %#v", projection.updates[6])
	}
}

func TestACPToolRawOutputRemainsStringAcrossLiveAndReplay(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "object", value: `{"ok":true}`},
		{name: "array", value: `[1]`},
		{name: "null", value: `null`},
		{name: "number", value: `1`},
		{name: "boolean", value: `true`},
		{name: "quoted-string", value: `"quoted"`},
		{name: "empty", value: ``},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encodedLiveOutput, err := json.Marshal(test.value)
			if err != nil {
				t.Fatal(err)
			}
			liveAgent, liveWire := newP231WireCaptureAgent(t)
			liveSessionID := acpsdk.SessionId("raw-output-live-" + test.name)
			installP232ToolLedgerSessions(liveAgent, liveSessionID)
			for _, projection := range []*engine.CanonicalProjectionEvent{
				canonicalACPToolEvent(
					engine.CanonicalProjectionToolStart,
					&engine.CanonicalToolPayload{
						ToolCallID: "call-live",
						ToolName:   "Read",
					},
				),
				canonicalACPToolEvent(
					engine.CanonicalProjectionToolTerminal,
					&engine.CanonicalToolPayload{
						ToolCallID: "call-live",
						Outcome:    engine.CanonicalToolOutcomeCompleted,
						RawOutput:  json.RawMessage(encodedLiveOutput),
					},
				),
			} {
				if err := liveAgent.streamEvent(
					t.Context(),
					liveSessionID,
					engine.QueryEvent{
						Type:                engine.EventCanonicalProjection,
						CanonicalProjection: projection,
					},
				); err != nil {
					t.Fatal(err)
				}
			}
			var liveMessages []map[string]any
			if err := json.Unmarshal(
				normalizeP231Wire(t, liveWire.Bytes()),
				&liveMessages,
			); err != nil {
				t.Fatal(err)
			}
			if len(liveMessages) != 2 {
				t.Fatalf("live wire messages = %d, want 2", len(liveMessages))
			}
			liveUpdate := p231WireUpdate(t, liveMessages[1])
			liveRawOutput, ok := liveUpdate["rawOutput"].(string)
			if !ok || liveRawOutput != test.value {
				t.Fatalf(
					"live rawOutput = %#v (%T), want exact string %q",
					liveUpdate["rawOutput"],
					liveUpdate["rawOutput"],
					test.value,
				)
			}

			replayDir := t.TempDir()
			replaySessionID := "raw-output-replay-" + test.name
			p234bWriteReplayTranscript(
				t,
				replayDir,
				replaySessionID,
				p234bReplayRecord("entry-assistant", &schema.Message{
					Role: schema.Assistant,
					ToolCalls: []schema.ToolCall{{
						ID:   "call-replay",
						Type: "function",
						Function: schema.FunctionCall{
							Name:      "Read",
							Arguments: `{"path":"/tmp/input"}`,
						},
					}},
				}),
				p234bReplayRecord("entry-tool", &schema.Message{
					Role:       schema.Tool,
					ToolCallID: "call-replay",
					Content:    test.value,
				}),
			)
			snapshot, err := session.LoadSessionReplaySnapshot(
				t.Context(),
				session.ResumeOptions{
					SessionID:  replaySessionID,
					SessionDir: replayDir,
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			projection, err := buildACPReplayProjection(snapshot, true)
			if err != nil {
				t.Fatal(err)
			}
			replayAgent := p234bNewReplayAgent(t, replayDir)
			replayWire := newP231WireBuffer()
			p234bAttachReplayWriter(t, replayAgent, replayWire)
			if err := replayAgent.deliverACPReplay(
				t.Context(),
				acpsdk.SessionId(replaySessionID),
				projection,
			); err != nil {
				t.Fatal(err)
			}
			var replayMessages []map[string]any
			if err := json.Unmarshal(
				normalizeP231Wire(t, replayWire.Bytes()),
				&replayMessages,
			); err != nil {
				t.Fatal(err)
			}
			if len(replayMessages) != 2 {
				t.Fatalf(
					"replay wire messages = %d, want 2",
					len(replayMessages),
				)
			}
			replayUpdate := p231WireUpdate(t, replayMessages[1])
			replayRawOutput, ok := replayUpdate["rawOutput"].(string)
			if !ok || replayRawOutput != test.value {
				t.Fatalf(
					"replay rawOutput = %#v (%T), want exact string %q",
					replayUpdate["rawOutput"],
					replayUpdate["rawOutput"],
					test.value,
				)
			}
		})
	}
}

func TestP234bACPReplaySDKWireUUIDGoldensAndRollback(t *testing.T) {
	dir := t.TempDir()
	const sessionID = "p234b-wire"
	p234bWriteReplayTranscript(
		t,
		dir,
		sessionID,
		p234bReplayRecord("entry-user", &schema.Message{
			Role:    schema.User,
			Content: " user\n\n",
		}),
		p234bReplayRecord("entry-fallback", &schema.Message{
			Role:    schema.Assistant,
			Content: "fallback",
		}),
	)
	snapshot, err := session.LoadSessionReplaySnapshot(
		t.Context(),
		session.ResumeOptions{SessionID: sessionID, SessionDir: dir},
	)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name                       string
		includeAssistantMessageIDs bool
	}{
		{name: "enabled", includeAssistantMessageIDs: true},
		{name: "rollback", includeAssistantMessageIDs: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			projection, err := buildACPReplayProjection(
				snapshot,
				test.includeAssistantMessageIDs,
			)
			if err != nil {
				t.Fatal(err)
			}
			agent := p234bNewReplayAgent(t, dir)
			wire := newP231WireBuffer()
			p234bAttachReplayWriter(t, agent, wire)
			if err := agent.deliverACPReplay(
				t.Context(),
				sessionID,
				projection,
			); err != nil {
				t.Fatal(err)
			}
			var messages []map[string]any
			if err := json.Unmarshal(
				normalizeP231Wire(t, wire.Bytes()),
				&messages,
			); err != nil {
				t.Fatal(err)
			}
			if len(messages) != 2 {
				t.Fatalf("wire messages = %d, want 2", len(messages))
			}
			user := p231WireUpdate(t, messages[0])
			if got := user["messageId"]; got !=
				"66013c7f-c7de-5198-823a-1cff455c3dc7" {
				t.Fatalf("wire user messageId = %#v", got)
			}
			userContent, _ := user["content"].(map[string]any)
			if got := userContent["text"]; got != " user\n\n" {
				t.Fatalf("wire user content = %#v", got)
			}
			assistant := p231WireUpdate(t, messages[1])
			assistantContent, _ := assistant["content"].(map[string]any)
			if got := assistantContent["text"]; got != "fallback" {
				t.Fatalf("wire assistant content = %#v", got)
			}
			messageID, present := assistant["messageId"]
			if !test.includeAssistantMessageIDs {
				if present {
					t.Fatalf("rollback exposed messageId = %#v", messageID)
				}
				return
			}
			if !present || messageID !=
				"739554bf-b3ad-5b33-8b60-b26687caf85d" {
				t.Fatalf("wire assistant messageId = %#v", messageID)
			}
		})
	}
}

func TestP234bACPReplayLegacyIDsUseOrdinalWithoutPrivateIdentity(t *testing.T) {
	dir := t.TempDir()
	const sessionID = "p234b-legacy"
	p234bWriteReplayTranscript(
		t,
		dir,
		sessionID,
		p234bLegacyReplayRecord(&schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Read",
					Arguments: `{}`,
				},
			}},
		}),
		p234bLegacyReplayRecord(&schema.Message{
			Role:    schema.Tool,
			Content: "ok",
		}),
	)
	snapshot, err := session.LoadSessionReplaySnapshot(
		t.Context(),
		session.ResumeOptions{SessionID: sessionID, SessionDir: dir},
	)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := buildACPReplayProjection(snapshot, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.updates) != 2 {
		t.Fatalf("legacy updates = %#v", projection.updates)
	}
	start := projection.updates[0].ToolCall
	if start == nil {
		t.Fatalf("legacy start = %#v", projection.updates[0])
	}
	const wantToolID = "81586967-ddce-5388-93eb-2efb8487202a/tool/0"
	if got := string(start.ToolCallId); got != wantToolID {
		t.Fatalf("legacy tool ID = %q, want %q", got, wantToolID)
	}
	if strings.Contains(string(start.ToolCallId), "legacy/") ||
		strings.Contains(string(start.ToolCallId), string(snapshot.Revision)) {
		t.Fatalf("legacy tool ID leaked private identity: %q", start.ToolCallId)
	}
	terminal := projection.updates[1].ToolCallUpdate
	if terminal == nil || terminal.ToolCallId != start.ToolCallId {
		t.Fatalf("legacy terminal = %#v", projection.updates[1])
	}
}

func TestP234bACPReplayRejectsInvalidIdentityAndRichContentBeforeDelivery(t *testing.T) {
	tests := []struct {
		name     string
		message  *schema.Message
		wantCode int
	}{
		{
			name: "present non UUID logical ID",
			message: &schema.Message{
				Role:    schema.Assistant,
				Content: "text",
				Extra:   map[string]any{"message_id": "not-a-uuid"},
			},
		},
		{
			name: "rich user content",
			message: &schema.Message{
				Role:    schema.User,
				Content: "text fallback",
				UserInputMultiContent: []schema.MessageInputPart{{
					Type: schema.ChatMessagePartTypeText,
					Text: "rich",
				}},
			},
			wantCode: codeUnsupportedInput,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			p234bWriteReplayTranscript(
				t,
				dir,
				"invalid",
				p234bReplayRecord("entry", test.message),
			)
			snapshot, err := session.LoadSessionReplaySnapshot(
				t.Context(),
				session.ResumeOptions{
					SessionID:  "invalid",
					SessionDir: dir,
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			projection, err := buildACPReplayProjection(snapshot, true)
			if err == nil || projection != nil {
				t.Fatalf("projection = %#v, err = %v", projection, err)
			}
			if test.wantCode != 0 {
				var requestErr *acpsdk.RequestError
				if !errors.As(err, &requestErr) ||
					requestErr.Code != test.wantCode {
					t.Fatalf("request error = %v", err)
				}
			}
		})
	}

	dir := t.TempDir()
	p234bWriteReplayTranscript(
		t,
		dir,
		"invalid-tool-input",
		p234bReplayRecord("call", &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID: "call-invalid-input",
				Function: schema.FunctionCall{
					Name:      "Read",
					Arguments: `{} {}`,
				},
			}},
		}),
		p234bReplayRecord("result", &schema.Message{
			Role:       schema.Tool,
			ToolCallID: "call-invalid-input",
			Content:    "not delivered",
		}),
	)
	snapshot, err := session.LoadSessionReplaySnapshot(
		t.Context(),
		session.ResumeOptions{
			SessionID:  "invalid-tool-input",
			SessionDir: dir,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if projection, err := buildACPReplayProjection(snapshot, true); err == nil ||
		projection != nil {
		t.Fatalf("trailing input projection = %#v, err = %v", projection, err)
	}
}

func TestP305bACPReplayPromptContentPreservesLogicalKinds(t *testing.T) {
	lastModified := "2026-07-30T00:00:00Z"
	priority := 0.75
	title := "Schema"
	description := "Workspace schema"
	jsonMIME := "application/json"
	textMIME := "text/plain"
	size := 42
	annotations := &session.SessionReplayPromptAnnotations{
		Audience:     []string{"assistant"},
		LastModified: &lastModified,
		Priority:     &priority,
	}
	tests := []struct {
		name string
		part session.SessionReplayPromptPart
		want func(*testing.T, acpsdk.ContentBlock)
	}{
		{
			name: "text",
			part: session.SessionReplayPromptPart{
				Kind: session.SessionReplayPromptPartText,
				Text: &session.SessionReplayPromptText{Text: "before"},
			},
			want: func(t *testing.T, block acpsdk.ContentBlock) {
				t.Helper()
				if block.Text == nil || block.Text.Text != "before" {
					t.Fatalf("text block = %#v", block)
				}
			},
		},
		{
			name: "image",
			part: session.SessionReplayPromptPart{
				Kind: session.SessionReplayPromptPartImage,
				Image: &session.SessionReplayPromptImage{
					Data:        p305aACPImageBase64,
					MIMEType:    "image/png",
					Annotations: annotations,
				},
			},
			want: func(t *testing.T, block acpsdk.ContentBlock) {
				t.Helper()
				if block.Image == nil ||
					block.Image.Data != p305aACPImageBase64 ||
					block.Image.MimeType != "image/png" ||
					block.Image.Uri != nil ||
					block.Image.Meta != nil ||
					block.Image.Annotations == nil ||
					len(block.Image.Annotations.Audience) != 1 ||
					block.Image.Annotations.Audience[0] != acpsdk.RoleAssistant {
					t.Fatalf("image block = %#v", block)
				}
			},
		},
		{
			name: "resource link",
			part: session.SessionReplayPromptPart{
				Kind: session.SessionReplayPromptPartResourceLink,
				ResourceLink: &session.SessionReplayPromptResourceLink{
					URI:         "file:///workspace/schema.json",
					Name:        "schema.json",
					Title:       &title,
					Description: &description,
					MIMEType:    &jsonMIME,
					Size:        &size,
					Annotations: annotations,
				},
			},
			want: func(t *testing.T, block acpsdk.ContentBlock) {
				t.Helper()
				if block.ResourceLink == nil ||
					block.ResourceLink.Uri != "file:///workspace/schema.json" ||
					block.ResourceLink.Name != "schema.json" ||
					block.ResourceLink.Title == nil ||
					*block.ResourceLink.Title != title ||
					block.ResourceLink.Meta != nil {
					t.Fatalf("resource-link block = %#v", block)
				}
			},
		},
		{
			name: "embedded text",
			part: session.SessionReplayPromptPart{
				Kind: session.SessionReplayPromptPartEmbeddedText,
				EmbeddedText: &session.SessionReplayPromptEmbeddedText{
					URI:         "file:///workspace/context.txt",
					MIMEType:    &textMIME,
					Text:        "embedded context",
					Annotations: annotations,
				},
			},
			want: func(t *testing.T, block acpsdk.ContentBlock) {
				t.Helper()
				if block.Resource == nil ||
					block.Resource.Resource.TextResourceContents == nil ||
					block.Resource.Resource.BlobResourceContents != nil ||
					block.Resource.Resource.TextResourceContents.Text !=
						"embedded context" ||
					block.Resource.Meta != nil {
					t.Fatalf("embedded-text block = %#v", block)
				}
			},
		},
		{
			name: "embedded blob",
			part: session.SessionReplayPromptPart{
				Kind: session.SessionReplayPromptPartEmbeddedBlob,
				EmbeddedBlob: &session.SessionReplayPromptEmbeddedBlob{
					URI:         "file:///workspace/pixel.png",
					MIMEType:    "image/png",
					Data:        p305aACPImageBase64,
					Annotations: annotations,
				},
			},
			want: func(t *testing.T, block acpsdk.ContentBlock) {
				t.Helper()
				if block.Resource == nil ||
					block.Image != nil ||
					block.Resource.Resource.BlobResourceContents == nil ||
					block.Resource.Resource.TextResourceContents != nil ||
					block.Resource.Resource.BlobResourceContents.Blob !=
						p305aACPImageBase64 ||
					block.Resource.Resource.BlobResourceContents.Meta != nil ||
					block.Resource.Meta != nil {
					t.Fatalf("embedded-blob block = %#v", block)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			block, err := acpReplayPromptContent(test.part)
			if err != nil {
				t.Fatal(err)
			}
			test.want(t, block)
			encoded, err := json.Marshal(block)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(encoded, []byte(`"_meta"`)) ||
				bytes.Contains(encoded, []byte(`"ref"`)) ||
				bytes.Contains(encoded, []byte(`"digest"`)) {
				t.Fatalf("wire block leaks private metadata: %s", encoded)
			}
		})
	}

	overlapping := session.SessionReplayPromptPart{
		Kind: session.SessionReplayPromptPartText,
		Text: &session.SessionReplayPromptText{Text: "text"},
		Image: &session.SessionReplayPromptImage{
			Data:     p305aACPImageBase64,
			MIMEType: "image/png",
		},
	}
	if block, err := acpReplayPromptContent(overlapping); err == nil {
		t.Fatalf("overlapping union = %#v", block)
	}
}

func TestP305bVersion1TextImageLoadsThroughACP(t *testing.T) {
	cwd := t.TempDir()
	model := &mockChatModel{responses: []*schema.Message{{
		Role:    schema.Assistant,
		Content: "version one response",
	}}}
	conn, client, agent := setupP305aACP(t, model, "gpt-4o")
	if _, err := conn.Initialize(t.Context(), acpsdk.InitializeRequest{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
	}); err != nil {
		t.Fatal(err)
	}
	created, err := conn.NewSession(t.Context(), acpsdk.NewSessionRequest{
		Cwd:        cwd,
		McpServers: []acpsdk.McpServer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Prompt(t.Context(), acpsdk.PromptRequest{
		SessionId: created.SessionId,
		Prompt: []acpsdk.ContentBlock{
			acpsdk.TextBlock("version one before"),
			acpsdk.ImageBlock(p305aACPImageBase64, "image/png"),
			acpsdk.TextBlock("version one after"),
		},
	}); err != nil {
		t.Fatal(err)
	}
	agent.mu.Lock()
	active := agent.sessions[created.SessionId]
	agent.mu.Unlock()
	if active == nil || active.Engine == nil {
		t.Fatal("version-one producer session is unavailable")
	}
	transcriptPath := active.Engine.GetTranscript().Path()
	if _, err := conn.CloseSession(t.Context(), acpsdk.CloseSessionRequest{
		SessionId: created.SessionId,
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"user_prompt":{"version":1`)) {
		t.Fatalf("producer did not persist a version-one prompt record: %s", raw)
	}
	root := filepath.Dir(transcriptPath)
	before := readP305bACPReplayTree(t, root)
	beforeUpdates := len(client.getUpdates())
	beforeCalls := model.CallCount()

	if _, err := conn.LoadSession(t.Context(), acpsdk.LoadSessionRequest{
		SessionId:  created.SessionId,
		Cwd:        cwd,
		McpServers: []acpsdk.McpServer{},
	}); err != nil {
		t.Fatal(err)
	}
	var users []*acpsdk.SessionUpdateUserMessageChunk
	for _, notification := range client.getUpdates()[beforeUpdates:] {
		if notification.Update.UserMessageChunk != nil {
			users = append(users, notification.Update.UserMessageChunk)
		}
	}
	if len(users) != 3 ||
		users[0].Content.Text == nil ||
		users[0].Content.Text.Text != "version one before" ||
		users[1].Content.Image == nil ||
		users[1].Content.Image.Data != p305aACPImageBase64 ||
		users[1].Content.Image.MimeType != "image/png" ||
		users[2].Content.Text == nil ||
		users[2].Content.Text.Text != "version one after" {
		t.Fatalf("version-one load user chunks = %#v", users)
	}
	if users[0].MessageId == nil ||
		users[1].MessageId == nil ||
		users[2].MessageId == nil ||
		*users[0].MessageId != *users[1].MessageId ||
		*users[0].MessageId != *users[2].MessageId {
		t.Fatalf("version-one load message IDs = %#v", users)
	}
	if model.CallCount() != beforeCalls {
		t.Fatalf(
			"version-one load executed model: before=%d after=%d",
			beforeCalls,
			model.CallCount(),
		)
	}
	if after := readP305bACPReplayTree(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("version-one load rewrote durable state")
	}
}

func TestP305bRichLoadInvalidDurableContentFailsBeforeFirstUpdate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{
			name: "missing media",
			mutate: func(t *testing.T, transcriptPath string) {
				t.Helper()
				if err := os.Remove(p305bFirstACPReplayBlob(t, transcriptPath)); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "corrupt media",
			mutate: func(t *testing.T, transcriptPath string) {
				t.Helper()
				if err := os.WriteFile(
					p305bFirstACPReplayBlob(t, transcriptPath),
					[]byte("corrupt"),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "unknown prompt version",
			mutate: func(t *testing.T, transcriptPath string) {
				t.Helper()
				p305bReplaceACPReplayTranscript(
					t,
					transcriptPath,
					`"user_prompt":{"version":2`,
					`"user_prompt":{"version":99`,
				)
			},
		},
		{
			name: "unknown prompt kind",
			mutate: func(t *testing.T, transcriptPath string) {
				t.Helper()
				p305bReplaceACPReplayTranscript(
					t,
					transcriptPath,
					`"kind":"image","image":`,
					`"kind":"unknown","image":`,
				)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cwd := t.TempDir()
			sessionID, transcriptPath := p305bCreateACPReplaySession(t, cwd)
			test.mutate(t, transcriptPath)
			root := filepath.Dir(transcriptPath)
			before := readP305bACPReplayTree(t, root)

			agent := p234bNewReplayAgent(t, cwd)
			wire := newP231WireBuffer()
			p234bAttachReplayWriter(t, agent, wire)
			response, loadErr := agent.LoadSession(
				t.Context(),
				acpsdk.LoadSessionRequest{
					SessionId:  sessionID,
					Cwd:        cwd,
					McpServers: []acpsdk.McpServer{},
				},
			)
			if loadErr == nil {
				t.Fatalf("invalid rich load response = %#v", response)
			}
			if got := len(wire.Bytes()); got != 0 {
				t.Fatalf("invalid rich load sent %d wire bytes: %s", got, wire.Bytes())
			}
			agent.mu.Lock()
			_, active := agent.sessions[sessionID]
			agent.mu.Unlock()
			if active {
				t.Fatal("invalid rich load registered an active session")
			}
			if after := readP305bACPReplayTree(t, root); !reflect.DeepEqual(before, after) {
				t.Fatal("invalid rich load rewrote durable state")
			}
		})
	}
}

func TestP305bUnboundRichProviderContentFailsBeforeFirstUpdate(t *testing.T) {
	tests := []struct {
		name    string
		message *schema.Message
	}{
		{
			name: "user",
			message: &schema.Message{
				Role: schema.User,
				UserInputMultiContent: []schema.MessageInputPart{{
					Type: schema.ChatMessagePartTypeText,
					Text: "unbound rich user",
				}},
			},
		},
		{
			name: "assistant",
			message: &schema.Message{
				Role: schema.Assistant,
				AssistantGenMultiContent: []schema.MessageOutputPart{{
					Type: schema.ChatMessagePartTypeText,
					Text: "unbound rich assistant",
				}},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cwd := t.TempDir()
			dir := acpSessionTranscriptDir(cwd)
			sessionID := "p305b-unbound-" + test.name
			p234bWriteReplayTranscript(
				t,
				dir,
				sessionID,
				p234bReplayRecord("unbound", test.message),
			)
			p234bAppendProjectGraphMetadata(t, dir, sessionID, cwd)
			agent := p234bNewReplayAgent(t, cwd)
			wire := newP231WireBuffer()
			p234bAttachReplayWriter(t, agent, wire)

			response, loadErr := agent.LoadSession(
				t.Context(),
				acpsdk.LoadSessionRequest{
					SessionId:  acpsdk.SessionId(sessionID),
					Cwd:        cwd,
					McpServers: []acpsdk.McpServer{},
				},
			)
			if loadErr == nil {
				t.Fatalf("unbound rich load response = %#v", response)
			}
			if got := len(wire.Bytes()); got != 0 {
				t.Fatalf("unbound rich load sent %d wire bytes: %s", got, wire.Bytes())
			}
			agent.mu.Lock()
			_, active := agent.sessions[acpsdk.SessionId(sessionID)]
			agent.mu.Unlock()
			if active {
				t.Fatal("unbound rich load registered an active session")
			}
		})
	}
}

func TestP305bRichLoadDeliveryFailureAbortsStaging(t *testing.T) {
	cwd := t.TempDir()
	sessionID, transcriptPath := p305bCreateACPReplaySession(t, cwd)
	root := filepath.Dir(transcriptPath)
	before := readP305bACPReplayTree(t, root)
	agent := p234bNewReplayAgent(t, cwd)
	writer := &p305bFailSecondUserReplayWriter{wire: newP231WireBuffer()}
	p234bAttachReplayWriter(t, agent, writer)

	if response, err := agent.LoadSession(
		t.Context(),
		acpsdk.LoadSessionRequest{
			SessionId:  sessionID,
			Cwd:        cwd,
			McpServers: []acpsdk.McpServer{},
		},
	); err == nil {
		t.Fatalf("partial rich load response = %#v", response)
	}
	if writer.userUpdates != 2 ||
		!bytes.Contains(
			writer.wire.Bytes(),
			[]byte(`"sessionUpdate":"user_message_chunk"`),
		) {
		t.Fatalf(
			"partial rich delivery trace: updates=%d wire=%s",
			writer.userUpdates,
			writer.wire.Bytes(),
		)
	}
	agent.mu.Lock()
	_, active := agent.sessions[sessionID]
	agent.mu.Unlock()
	if active {
		t.Fatal("partial rich delivery registered an active session")
	}
	if after := readP305bACPReplayTree(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("partial rich delivery rewrote durable state")
	}
}

func TestP305bRichLoadPreservesMessageAndToolOrdering(t *testing.T) {
	cwd := t.TempDir()
	sessionID, transcriptPath := p305bCreateACPReplaySession(t, cwd)
	recorder := transcript.NewRecorder(string(sessionID), filepath.Dir(transcriptPath))
	if err := recorder.RecordMessages([]*schema.Message{
		{
			Role: schema.Assistant,
			Extra: map[string]any{
				"message_id": "55555555-5555-4555-8555-555555555555",
			},
			ToolCalls: []schema.ToolCall{{
				ID:   "p305b-tool",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Read",
					Arguments: `{"path":"/tmp/p305b"}`,
				},
			}},
		},
		{
			Role:       schema.Tool,
			ToolCallID: "p305b-tool",
			Content:    "tool output",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(transcriptPath)
	before := readP305bACPReplayTree(t, root)
	agent := p234bNewReplayAgent(t, cwd)
	wire := newP231WireBuffer()
	p234bAttachReplayWriter(t, agent, wire)

	if _, err := agent.LoadSession(t.Context(), acpsdk.LoadSessionRequest{
		SessionId:  sessionID,
		Cwd:        cwd,
		McpServers: []acpsdk.McpServer{},
	}); err != nil {
		t.Fatal(err)
	}
	p234bRequireWireOrder(
		t,
		wire.Bytes(),
		`"sessionUpdate":"user_message_chunk"`,
		`"sessionUpdate":"agent_message_chunk"`,
		`"sessionUpdate":"tool_call"`,
		`"sessionUpdate":"tool_call_update"`,
		`"sessionUpdate":"config_option_update"`,
		`"sessionUpdate":"current_mode_update"`,
		`"sessionUpdate":"available_commands_update"`,
	)
	if after := readP305bACPReplayTree(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("successful rich/tool load rewrote durable state")
	}
}

func TestP234bLoadDeliversBeforeCommitRegistrationAndStartsHooksAfter(t *testing.T) {
	cwd := t.TempDir()
	dir := acpSessionTranscriptDir(cwd)
	const sessionID = "p234b-load-order"
	path := p234bWriteReplayTranscript(
		t,
		dir,
		sessionID,
		p234bReplayRecord("entry-user", &schema.Message{
			Role:    schema.User,
			Content: "before",
		}),
		p234bReplayRecord("entry-assistant", &schema.Message{
			Role:    schema.Assistant,
			Content: "after",
			Extra: map[string]any{
				"message_id": "22222222-2222-4222-8222-222222222222",
			},
		}),
	)
	p234bAppendProjectGraphMetadata(t, dir, sessionID, cwd)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	agent := p234bNewReplayAgent(t, cwd)
	writer := &p234bRegistrationGuardWriter{
		agent: agent,
		wire:  newP231WireBuffer(),
	}
	p234bAttachReplayWriter(t, agent, writer)

	response, err := agent.LoadSession(t.Context(), acpsdk.LoadSessionRequest{
		SessionId:  sessionID,
		Cwd:        cwd,
		McpServers: []acpsdk.McpServer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if writer.registeredDuringWrite {
		t.Fatal("load registered the session before required delivery finished")
	}
	agent.mu.Lock()
	loaded := agent.sessions[sessionID]
	agent.mu.Unlock()
	if loaded == nil {
		t.Fatal("load did not register the committed session")
	}
	loaded.mu.Lock()
	hooksStarted := loaded.hookCancel != nil
	loaded.mu.Unlock()
	if !hooksStarted {
		t.Fatal("load did not start ACP hook delivery after registration")
	}
	if err := loaded.Engine.AbortRestoreStaging(); !errors.Is(
		err,
		engine.ErrRestoreStagingTransition,
	) {
		t.Fatalf("loaded engine was not committed: %v", err)
	}
	if response.Modes == nil || len(response.ConfigOptions) == 0 {
		t.Fatalf("load response state = %#v", response)
	}
	wire := writer.wire.Bytes()
	p234bRequireWireOrder(
		t,
		wire,
		`"sessionUpdate":"user_message_chunk"`,
		`"sessionUpdate":"agent_message_chunk"`,
		`"sessionUpdate":"config_option_update"`,
		`"sessionUpdate":"current_mode_update"`,
		`"sessionUpdate":"available_commands_update"`,
	)
	var wireMessages []map[string]any
	if err := json.Unmarshal(
		normalizeP231Wire(t, wire),
		&wireMessages,
	); err != nil {
		t.Fatal(err)
	}
	if len(wireMessages) != 5 {
		t.Fatalf(
			"load wire trace has %d messages, want exactly 5: %s",
			len(wireMessages),
			wire,
		)
	}
	wantUpdates := []string{
		"user_message_chunk",
		"agent_message_chunk",
		"config_option_update",
		"current_mode_update",
		"available_commands_update",
	}
	for index, want := range wantUpdates {
		update := p231WireUpdate(t, wireMessages[index])
		if got := update["sessionUpdate"]; got != want {
			t.Fatalf("load wire update %d = %#v, want %q", index, got, want)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("successful staged load rewrote the transcript before return")
	}
}

func TestP234bLoadSetupDeliveryFailureAbortsWithoutRegistration(t *testing.T) {
	for _, failureKind := range []string{
		"config_option_update",
		"current_mode_update",
		"available_commands_update",
	} {
		t.Run(failureKind, func(t *testing.T) {
			cwd := t.TempDir()
			dir := acpSessionTranscriptDir(cwd)
			const sessionID = "p234b-setup-failure"
			path := p234bWriteReplayTranscript(
				t,
				dir,
				sessionID,
				p234bReplayRecord("entry-user", &schema.Message{
					Role:    schema.User,
					Content: "delivered before setup",
				}),
			)
			p234bAppendProjectGraphMetadata(t, dir, sessionID, cwd)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			agent := p234bNewReplayAgent(t, cwd)
			p234bAttachReplayWriter(t, agent, p234bFailUpdateWriter{
				sessionUpdate: failureKind,
			})

			if _, err := agent.LoadSession(
				t.Context(),
				acpsdk.LoadSessionRequest{
					SessionId:  sessionID,
					Cwd:        cwd,
					McpServers: []acpsdk.McpServer{},
				},
			); err == nil {
				t.Fatalf("load succeeded after %s delivery failed", failureKind)
			}
			agent.mu.Lock()
			_, active := agent.sessions[sessionID]
			agent.mu.Unlock()
			if active {
				t.Fatal("failed setup delivery registered an active session")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("failed setup delivery rewrote the transcript")
			}
		})
	}
}

func TestP241ACPCommitFailureClosesCommittingStagingEngine(t *testing.T) {
	for _, operation := range []string{"resume", "load"} {
		t.Run(operation, func(t *testing.T) {
			cwd := t.TempDir()
			dir := acpSessionTranscriptDir(cwd)
			sessionID := "p241-commit-failure-" + operation
			path := p234bWriteReplayTranscript(
				t,
				dir,
				sessionID,
				p234bReplayRecord("entry-user", &schema.Message{
					Role:    schema.User,
					Content: "restore Goal safely",
				}),
				p234bReplayRecord("entry-assistant", &schema.Message{
					Role:    schema.Assistant,
					Content: "checkpoint before activation",
				}),
			)
			now := time.Date(2026, 7, 28, 19, 0, 0, 0, time.UTC)
			recorder := transcript.NewRecorder(sessionID, dir)
			budget := uint64(100)
			writeACPProjectGraphRootMetadata(
				t,
				recorder,
				&session.SessionMetadataFull{
					SessionID: sessionID,
					ThreadID:  sessionID,
					CWD:       cwd,
					GoalState: &session.PersistedGoalState{
						Version:           session.PersistedGoalStateVersion,
						GoalID:            "goal-acp-commit",
						Objective:         "finish restore safely",
						ObjectiveRevision: 1,
						Status:            "active",
						Revision:          1,
						TokenBudget:       &budget,
						CreatedAt:         now,
						UpdatedAt:         now,
					},
					CreatedAt: now,
					UpdatedAt: now,
				},
			)
			if err := recorder.Flush(); err != nil {
				t.Fatal(err)
			}
			if err := recorder.Close(); err != nil {
				t.Fatal(err)
			}
			originalTranscript, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			agent := p234bNewReplayAgent(t, cwd)
			var captured *engine.QueryEngine
			agent.createRestoreStagingEngineFn = func(
				id string,
				projectDir string,
			) (*engine.QueryEngine, error) {
				eng, createErr := agent.createEngineForSessionWithConstructor(
					id,
					projectDir,
					engine.NewRestoreStagingQueryEngine,
				)
				if createErr == nil {
					captured = eng
				}
				return eng, createErr
			}
			writer := &p241ACPCommitFailureWriter{
				path:   path,
				engine: func() *engine.QueryEngine { return captured },
			}
			p234bAttachReplayWriter(t, agent, writer)

			switch operation {
			case "resume":
				_, err = agent.ResumeSession(
					t.Context(),
					acpsdk.ResumeSessionRequest{
						SessionId:  acpsdk.SessionId(sessionID),
						Cwd:        cwd,
						McpServers: []acpsdk.McpServer{},
					},
				)
			case "load":
				_, err = agent.LoadSession(
					t.Context(),
					acpsdk.LoadSessionRequest{
						SessionId:  acpsdk.SessionId(sessionID),
						Cwd:        cwd,
						McpServers: []acpsdk.McpServer{},
					},
				)
			default:
				t.Fatalf("unknown operation %q", operation)
			}
			if err == nil || !strings.Contains(err.Error(), "commit") {
				t.Fatalf("%s commit error = %v", operation, err)
			}
			if writer.err != nil {
				t.Fatalf("inject checkpoint failure: %v", writer.err)
			}
			if captured == nil {
				t.Fatal("restore-staging engine was not captured")
			}
			agent.mu.Lock()
			_, active := agent.sessions[acpsdk.SessionId(sessionID)]
			agent.mu.Unlock()
			if active {
				t.Fatal("failed commit registered an active ACP session")
			}
			if err := captured.AbortRestoreStaging(); err != nil {
				t.Fatalf("ACP failure did not close staging engine: %v", err)
			}

			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, originalTranscript, 0o600); err != nil {
				t.Fatal(err)
			}
			retry := p234bNewReplayAgent(t, cwd)
			p234bAttachReplayWriter(t, retry, io.Discard)
			if _, err := retry.LoadSession(
				t.Context(),
				acpsdk.LoadSessionRequest{
					SessionId:  acpsdk.SessionId(sessionID),
					Cwd:        cwd,
					McpServers: []acpsdk.McpServer{},
				},
			); err != nil {
				t.Fatalf("retry load: %v", err)
			}
			loaded, err := transcript.NewRecorder(sessionID, dir).LoadFull()
			if err != nil {
				t.Fatal(err)
			}
			metadata := session.ReadSessionMetadataFull(loaded)
			if metadata == nil ||
				metadata.GoalState == nil ||
				metadata.GoalState.Status != "paused" {
				t.Fatalf("retried Goal metadata = %#v", metadata)
			}
		})
	}
}

func TestP234bConcurrentCloseLinearizesAfterLoad(t *testing.T) {
	cwd := t.TempDir()
	dir := acpSessionTranscriptDir(cwd)
	const sessionID = "p234b-load-close"
	p234bWriteReplayTranscript(
		t,
		dir,
		sessionID,
		p234bReplayRecord("entry-user", &schema.Message{
			Role:    schema.User,
			Content: "block replay",
		}),
	)
	p234bAppendProjectGraphMetadata(t, dir, sessionID, cwd)
	agent := p234bNewReplayAgent(t, cwd)
	writer := &p234bBlockingUpdateWriter{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	p234bAttachReplayWriter(t, agent, writer)

	loadDone := make(chan error, 1)
	go func() {
		_, err := agent.LoadSession(
			t.Context(),
			acpsdk.LoadSessionRequest{
				SessionId:  sessionID,
				Cwd:        cwd,
				McpServers: []acpsdk.McpServer{},
			},
		)
		loadDone <- err
	}()
	select {
	case <-writer.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("load did not reach replay delivery")
	}

	closeDone := make(chan error, 1)
	go func() {
		_, err := agent.CloseSession(
			t.Context(),
			acpsdk.CloseSessionRequest{SessionId: sessionID},
		)
		closeDone <- err
	}()
	select {
	case err := <-closeDone:
		t.Fatalf("close escaped the in-flight load lifecycle: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(writer.release)
	select {
	case err := <-loadDone:
		if err != nil {
			t.Fatalf("load failed: %v", err)
		}
	// Commit activates the restored runtime and revalidates durable child
	// transcripts. The race detector makes that reconstruction substantially
	// slower; keep this deadline about linearization, not machine speed.
	case <-time.After(60 * time.Second):
		t.Fatal("load did not finish")
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("close failed: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("close did not finish after load")
	}
	agent.mu.Lock()
	_, active := agent.sessions[sessionID]
	agent.mu.Unlock()
	if active {
		t.Fatal("close did not remove the just-loaded session")
	}
}

func TestP234bLoadDeliveryFailureAbortsWithoutRegistrationOrPersistence(t *testing.T) {
	cwd := t.TempDir()
	dir := acpSessionTranscriptDir(cwd)
	const sessionID = "p234b-load-failure"
	path := p234bWriteReplayTranscript(
		t,
		dir,
		sessionID,
		p234bReplayRecord("entry-user", &schema.Message{
			Role:    schema.User,
			Content: "delivered",
		}),
		p234bReplayRecord("entry-assistant", &schema.Message{
			Role:    schema.Assistant,
			Content: "fails",
			Extra: map[string]any{
				"message_id": "33333333-3333-4333-8333-333333333333",
			},
		}),
	)
	p234bAppendProjectGraphMetadata(t, dir, sessionID, cwd)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	agent := p234bNewReplayAgent(t, cwd)
	p234bAttachReplayWriter(t, agent, p234bFailAssistantWriter{})

	if _, err := agent.LoadSession(
		t.Context(),
		acpsdk.LoadSessionRequest{
			SessionId:  sessionID,
			Cwd:        cwd,
			McpServers: []acpsdk.McpServer{},
		},
	); err == nil {
		t.Fatal("load succeeded after replay delivery failed")
	}
	agent.mu.Lock()
	_, active := agent.sessions[sessionID]
	agent.mu.Unlock()
	if active {
		t.Fatal("failed load registered an active session")
	}
	if agent.mockModel.(*mockChatModel).CallCount() != 0 {
		t.Fatal("failed load executed the model")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed load rewrote the transcript")
	}
}

func TestP234bLoadProjectionFailureSendsNothing(t *testing.T) {
	cwd := t.TempDir()
	dir := acpSessionTranscriptDir(cwd)
	const sessionID = "p234b-load-invalid"
	p234bWriteReplayTranscript(
		t,
		dir,
		sessionID,
		p234bReplayRecord("entry-assistant", &schema.Message{
			Role:    schema.Assistant,
			Content: "invalid",
			Extra:   map[string]any{"message_id": "not-a-uuid"},
		}),
	)
	p234bAppendProjectGraphMetadata(t, dir, sessionID, cwd)
	agent := p234bNewReplayAgent(t, cwd)
	wire := newP231WireBuffer()
	p234bAttachReplayWriter(t, agent, wire)

	if _, err := agent.LoadSession(
		t.Context(),
		acpsdk.LoadSessionRequest{
			SessionId:  sessionID,
			Cwd:        cwd,
			McpServers: []acpsdk.McpServer{},
		},
	); err == nil {
		t.Fatal("load accepted a persisted non-UUID logical ID")
	}
	if got := len(wire.Bytes()); got != 0 {
		t.Fatalf("projection failure sent %d wire bytes", got)
	}
	agent.mu.Lock()
	_, active := agent.sessions[sessionID]
	agent.mu.Unlock()
	if active {
		t.Fatal("projection failure registered a session")
	}
}

func TestP234bResumeDoesNotReplayDurableConversation(t *testing.T) {
	cwd := t.TempDir()
	dir := acpSessionTranscriptDir(cwd)
	const sessionID = "p234b-resume-no-replay"
	p234bWriteReplayTranscript(
		t,
		dir,
		sessionID,
		p234bReplayRecord("entry-user", &schema.Message{
			Role:    schema.User,
			Content: "must not replay",
		}),
		p380ReplayRecordWithOrigin(
			t,
			"entry-assistant",
			p361RichAssistantMessage(),
		),
	)
	p234bAppendProjectGraphMetadata(t, dir, sessionID, cwd)
	agent := p234bNewReplayAgent(t, cwd)
	wire := newP231WireBuffer()
	p234bAttachReplayWriter(t, agent, wire)

	if _, err := agent.ResumeSession(
		t.Context(),
		acpsdk.ResumeSessionRequest{
			SessionId:  sessionID,
			Cwd:        cwd,
			McpServers: []acpsdk.McpServer{},
		},
	); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		`"sessionUpdate":"user_message_chunk"`,
		`"sessionUpdate":"agent_message_chunk"`,
		`"sessionUpdate":"tool_call"`,
		`"sessionUpdate":"tool_call_update"`,
	} {
		if bytes.Contains(wire.Bytes(), []byte(forbidden)) {
			t.Fatalf("resume emitted durable replay %q: %s", forbidden, wire.Bytes())
		}
	}
	if !bytes.Contains(
		wire.Bytes(),
		[]byte(`"sessionUpdate":"available_commands_update"`),
	) {
		t.Fatalf("resume lost its initial command snapshot: %s", wire.Bytes())
	}
	p361RequireNoPrivateMarkers(t, wire.Bytes())
}

func TestP234bLoadMissingSessionReturnsTypedNotFound(t *testing.T) {
	agent := p234bNewReplayAgent(t, t.TempDir())
	_, err := agent.LoadSession(
		t.Context(),
		acpsdk.LoadSessionRequest{SessionId: "missing"},
	)
	var requestErr *acpsdk.RequestError
	if !errors.As(err, &requestErr) ||
		requestErr.Code != CodeSessionNotFound {
		t.Fatalf("missing load error = %v", err)
	}
}

type p234bReplayRecordValue struct {
	Timestamp        time.Time       `json:"timestamp"`
	Kind             string          `json:"kind"`
	EntryID          map[string]any  `json:"entry_id,omitempty"`
	Message          *schema.Message `json:"message"`
	AssistantOrigins any             `json:"assistant_origins,omitempty"`
}

func p380ReplayRecordWithOrigin(
	t *testing.T,
	entryID string,
	message *schema.Message,
) p234bReplayRecordValue {
	t.Helper()
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	logicalID, _ := message.Extra["message_id"].(string)
	record := p234bReplayRecord(entryID, message)
	record.AssistantOrigins = []map[string]any{{
		"binding_codec":      "assistant-origin-binding/v1",
		"entry_version":      1,
		"entry_id":           entryID,
		"message_index":      0,
		"logical_message_id": logicalID,
		"payload_digest":     fmt.Sprintf("%x", digest),
		"origin": map[string]any{
			"version":               1,
			"provider":              "agenticopenai",
			"account_id":            "private-origin-account",
			"api_family":            "openai-responses/v1",
			"api_model":             "gpt-private-origin",
			"route_identity_digest": strings.Repeat("a", 64),
			"credential_origin_id":  "private-credential-origin",
		},
	}}
	return record
}

func p234bReplayRecord(
	entryID string,
	message *schema.Message,
) p234bReplayRecordValue {
	return p234bReplayRecordValue{
		Timestamp: time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC),
		Kind:      string(message.Role),
		EntryID:   map[string]any{"version": 1, "id": entryID},
		Message:   message,
	}
}

func p234bLegacyReplayRecord(
	message *schema.Message,
) p234bReplayRecordValue {
	record := p234bReplayRecord("", message)
	record.EntryID = nil
	return record
}

func p234bWriteReplayTranscript(
	t *testing.T,
	dir string,
	sessionID string,
	records ...p234bReplayRecordValue,
) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var content bytes.Buffer
	for _, record := range records {
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		content.Write(encoded)
		content.WriteByte('\n')
	}
	path := filepath.Join(dir, sessionID+".jsonl")
	if err := os.WriteFile(path, content.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func p305bCreateACPReplaySession(
	t *testing.T,
	cwd string,
) (acpsdk.SessionId, string) {
	t.Helper()
	model := &mockChatModel{responses: []*schema.Message{{
		Role:    schema.Assistant,
		Content: "rich replay response",
	}}}
	conn, _, agent := setupP305aACP(t, model, "gpt-4o")
	if _, err := conn.Initialize(t.Context(), acpsdk.InitializeRequest{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
	}); err != nil {
		t.Fatal(err)
	}
	created, err := conn.NewSession(t.Context(), acpsdk.NewSessionRequest{
		Cwd:        cwd,
		McpServers: []acpsdk.McpServer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	blobMIME := "image/png"
	if _, err := conn.Prompt(t.Context(), acpsdk.PromptRequest{
		SessionId: created.SessionId,
		Prompt: []acpsdk.ContentBlock{
			acpsdk.TextBlock("rich replay before"),
			acpsdk.ImageBlock(p305aACPImageBase64, "image/png"),
			acpsdk.ResourceBlock(acpsdk.EmbeddedResourceResource{
				BlobResourceContents: &acpsdk.BlobResourceContents{
					Uri:      "file:///workspace/replay.png",
					MimeType: &blobMIME,
					Blob:     p305aACPImageBase64,
				},
			}),
		},
	}); err != nil {
		t.Fatal(err)
	}
	agent.mu.Lock()
	active := agent.sessions[created.SessionId]
	agent.mu.Unlock()
	if active == nil || active.Engine == nil {
		t.Fatal("rich replay producer session is unavailable")
	}
	transcriptPath := active.Engine.GetTranscript().Path()
	if _, err := conn.CloseSession(t.Context(), acpsdk.CloseSessionRequest{
		SessionId: created.SessionId,
	}); err != nil {
		t.Fatal(err)
	}
	return created.SessionId, transcriptPath
}

func p305bFirstACPReplayBlob(t *testing.T, transcriptPath string) string {
	t.Helper()
	root := filepath.Join(transcriptPath+".media", "blobs", "sha256")
	var found string
	err := filepath.WalkDir(root, func(
		path string,
		entry os.DirEntry,
		err error,
	) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && found == "" {
			found = path
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == "" {
		t.Fatal("rich replay producer created no media blob")
	}
	return found
}

func p305bReplaceACPReplayTranscript(
	t *testing.T,
	path string,
	old string,
	replacement string,
) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := bytes.Replace(data, []byte(old), []byte(replacement), 1)
	if bytes.Equal(data, updated) {
		t.Fatalf("transcript mutation did not find %q", old)
	}
	if err := os.WriteFile(path, updated, 0o600); err != nil {
		t.Fatal(err)
	}
}

func p234bNewReplayAgent(t *testing.T, cwd string) *Agent {
	t.Helper()
	agent, err := NewAgent(Config{
		CWD:          cwd,
		ProviderFlag: "mock",
		ModelFlag:    "mock-model",
		APIKeyFlag:   "mock-key",
		YoloMode:     true,
		MaxTurns:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	agent.mockModel = &mockChatModel{}
	t.Cleanup(agent.Close)
	return agent
}

func p234bAppendProjectGraphMetadata(
	t *testing.T,
	dir string,
	sessionID string,
	cwd string,
) {
	t.Helper()
	recorder := transcript.NewRecorder(sessionID, dir)
	writeACPProjectGraphRootMetadata(
		t,
		recorder,
		&session.SessionMetadataFull{
			SessionID: sessionID,
			ThreadID:  sessionID,
			CWD:       cwd,
			CreatedAt: time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC),
		},
	)
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
}

func p234bAttachReplayWriter(
	t *testing.T,
	agent *Agent,
	writer io.Writer,
) {
	t.Helper()
	inboundReader, inboundWriter := io.Pipe()
	agent.SetConnection(acpsdk.NewAgentSideConnection(
		agent,
		writer,
		inboundReader,
	))
	t.Cleanup(func() {
		_ = inboundWriter.Close()
	})
}

type p234bRegistrationGuardWriter struct {
	mu                    sync.Mutex
	agent                 *Agent
	wire                  *p231WireBuffer
	registeredDuringWrite bool
}

func (w *p234bRegistrationGuardWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.agent.mu.Lock()
	registered := len(w.agent.sessions) != 0
	w.agent.mu.Unlock()
	if registered {
		w.registeredDuringWrite = true
	}
	return w.wire.Write(p)
}

type p234bFailAssistantWriter struct{}

func (p234bFailAssistantWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte(`"sessionUpdate":"agent_message_chunk"`)) {
		return 0, errors.New("assistant replay delivery failed")
	}
	return len(p), nil
}

type p305bFailSecondUserReplayWriter struct {
	mu          sync.Mutex
	wire        *p231WireBuffer
	userUpdates int
}

func (w *p305bFailSecondUserReplayWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if bytes.Contains(p, []byte(`"sessionUpdate":"user_message_chunk"`)) {
		w.userUpdates++
		if w.userUpdates == 2 {
			return 0, errors.New("second rich replay update failed")
		}
	}
	return w.wire.Write(p)
}

type p234bFailUpdateWriter struct {
	sessionUpdate string
}

func (w p234bFailUpdateWriter) Write(p []byte) (int, error) {
	needle := []byte(`"sessionUpdate":"` + w.sessionUpdate + `"`)
	if bytes.Contains(p, needle) {
		return 0, errors.New("setup update delivery failed")
	}
	return len(p), nil
}

type p241ACPCommitFailureWriter struct {
	mu     sync.Mutex
	once   sync.Once
	path   string
	engine func() *engine.QueryEngine
	err    error
}

func (w *p241ACPCommitFailureWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !bytes.Contains(
		p,
		[]byte(`"sessionUpdate":"available_commands_update"`),
	) {
		return len(p), nil
	}
	w.once.Do(func() {
		eng := w.engine()
		if eng == nil {
			w.err = errors.New("restore-staging engine is unavailable")
			return
		}
		if err := eng.GetTranscript().Close(); err != nil {
			w.err = err
			return
		}
		if err := os.Remove(w.path); err != nil {
			w.err = err
			return
		}
		w.err = os.Mkdir(w.path, 0o700)
	})
	return len(p), nil
}

type p234bBlockingUpdateWriter struct {
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (w *p234bBlockingUpdateWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte(`"sessionUpdate":"user_message_chunk"`)) {
		w.once.Do(func() {
			close(w.entered)
			<-w.release
		})
	}
	return len(p), nil
}

func p234bRequireWireOrder(
	t *testing.T,
	wire []byte,
	kinds ...string,
) {
	t.Helper()
	previous := -1
	for _, kind := range kinds {
		index := bytes.Index(wire, []byte(kind))
		if index < 0 || index <= previous {
			t.Fatalf(
				"wire order missing or invalid for %q after %d: %s",
				kind,
				previous,
				wire,
			)
		}
		previous = index
	}
}

func TestP234bReplayMessageUUIDsAreUUIDv5(t *testing.T) {
	if acpReplayMessageNamespace.Version() != 5 {
		t.Fatalf("namespace version = %d", acpReplayMessageNamespace.Version())
	}
	if _, err := uuid.Parse(acpReplayMessageNamespace.String()); err != nil {
		t.Fatal(err)
	}
}

func p361RichAssistantMessage() *schema.Message {
	return &schema.Message{
		Role:    schema.Assistant,
		Content: "public-one public-two",
		Extra: map[string]any{
			"message_id":       "66666666-6666-4666-8666-666666666666",
			"provider_trace":   "private-provider-marker",
			"openai-generated": true,
		},
		ReasoningContent: "private-chain-of-thought",
		AssistantGenMultiContent: []schema.MessageOutputPart{
			{
				Type:  schema.ChatMessagePartTypeText,
				Text:  "public-one ",
				Extra: map[string]any{"stream_marker": "private-part-extra"},
			},
			{
				Type: schema.ChatMessagePartTypeReasoning,
				Reasoning: &schema.MessageOutputReasoning{
					Text:      "private-chain-of-thought",
					Signature: "encrypted-signature-blob",
				},
			},
			{
				Type: schema.ChatMessagePartTypeText,
				Text: "public-two",
			},
		},
	}
}

type p361ContinuationModel struct {
	mu               sync.Mutex
	callCount        int
	reasoningContent string
	reasoningText    string
	signature        string
}

func (m *p361ContinuationModel) Generate(
	ctx context.Context,
	input []*schema.Message,
	opts ...model.Option,
) (*schema.Message, error) {
	m.record(input)
	return p361ContinuationResponse(), nil
}

func (m *p361ContinuationModel) Stream(
	ctx context.Context,
	input []*schema.Message,
	opts ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	m.record(input)
	return schema.StreamReaderFromArray([]*schema.Message{
		p361ContinuationResponse(),
	}), nil
}

func (m *p361ContinuationModel) record(input []*schema.Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	for _, message := range input {
		if message == nil || message.Role != schema.Assistant {
			continue
		}
		for _, part := range message.AssistantGenMultiContent {
			if part.Type != schema.ChatMessagePartTypeReasoning || part.Reasoning == nil {
				continue
			}
			m.reasoningContent = message.ReasoningContent
			m.reasoningText = part.Reasoning.Text
			m.signature = part.Reasoning.Signature
		}
	}
}

func (m *p361ContinuationModel) snapshot() (int, string, string, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount, m.reasoningContent, m.reasoningText, m.signature
}

func p361ContinuationResponse() *schema.Message {
	return &schema.Message{
		Role:    schema.Assistant,
		Content: "continued",
		AssistantGenMultiContent: []schema.MessageOutputPart{{
			Type: schema.ChatMessagePartTypeText,
			Text: "continued",
		}},
	}
}

func p361RequireNoPrivateMarkers(t *testing.T, encoded []byte) {
	t.Helper()
	for _, private := range []string{
		"private-chain-of-thought",
		"encrypted-signature-blob",
		"private-provider-marker",
		"private-part-extra",
		"stream_marker",
		"provider_trace",
		"openai-generated",
		"assistant-origin-binding/v1",
		"private-origin-account",
		"private-credential-origin",
		"gpt-private-origin",
		"agent_thought_chunk",
	} {
		if bytes.Contains(encoded, []byte(private)) {
			t.Fatalf("replay leaked private marker %q: %s", private, encoded)
		}
	}
}

func TestP380ACPNewSessionEmitsNoPrivateProviderState(t *testing.T) {
	cwd := t.TempDir()
	agent := p234bNewReplayAgent(t, cwd)
	agent.mockModel = &mockChatModel{responses: []*schema.Message{
		p361RichAssistantMessage(),
	}}
	wire := newP231WireBuffer()
	p234bAttachReplayWriter(t, agent, wire)
	created, err := agent.NewSession(t.Context(), acpsdk.NewSessionRequest{
		Cwd:        cwd,
		McpServers: []acpsdk.McpServer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := agent.Prompt(t.Context(), acpsdk.PromptRequest{
		SessionId: created.SessionId,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("private replay boundary")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.StopReason != acpsdk.StopReasonEndTurn {
		t.Fatalf("new-session stop reason = %q", response.StopReason)
	}
	if !bytes.Contains(wire.Bytes(), []byte("public-one public-two")) {
		t.Fatalf("new-session wire lost public assistant text: %s", wire.Bytes())
	}
	p361RequireNoPrivateMarkers(t, wire.Bytes())
}

func TestP361ACPReplayProjectionEmitsOnlyOrderedPublicText(t *testing.T) {
	dir := t.TempDir()
	const sessionID = "p361-acp-rich"
	p234bWriteReplayTranscript(
		t,
		dir,
		sessionID,
		p234bReplayRecord("entry-user", &schema.Message{
			Role:    schema.User,
			Content: "question",
		}),
		p380ReplayRecordWithOrigin(t, "entry-rich", p361RichAssistantMessage()),
		p234bReplayRecord("entry-plain", &schema.Message{
			Role:    schema.Assistant,
			Content: "plain tail",
		}),
	)
	snapshot, err := session.LoadSessionReplaySnapshot(
		t.Context(),
		session.ResumeOptions{SessionID: sessionID, SessionDir: dir},
	)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := buildACPReplayProjection(snapshot, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.updates) != 4 {
		t.Fatalf("updates = %d, want 4: %#v", len(projection.updates), projection.updates)
	}
	first := projection.updates[1].AgentMessageChunk
	second := projection.updates[2].AgentMessageChunk
	if first == nil || second == nil ||
		first.Content.Text == nil ||
		first.Content.Text.Text != "public-one " ||
		second.Content.Text == nil ||
		second.Content.Text.Text != "public-two" {
		t.Fatalf("rich assistant chunks = %#v / %#v", first, second)
	}
	if first.MessageId == nil ||
		second.MessageId == nil ||
		*first.MessageId != "66666666-6666-4666-8666-666666666666" ||
		*second.MessageId != *first.MessageId {
		t.Fatalf("rich assistant message IDs = %#v / %#v", first.MessageId, second.MessageId)
	}
	plain := projection.updates[3].AgentMessageChunk
	if plain == nil || plain.Content.Text == nil ||
		plain.Content.Text.Text != "plain tail" ||
		plain.MessageId == nil ||
		*plain.MessageId == *first.MessageId {
		t.Fatalf("plain fallback chunk = %#v", plain)
	}
	for index, update := range projection.updates {
		if update.AgentThoughtChunk != nil {
			t.Fatalf("update %d emitted a thought chunk: %#v", index, update)
		}
	}
	encoded, err := json.Marshal(projection.updates)
	if err != nil {
		t.Fatal(err)
	}
	p361RequireNoPrivateMarkers(t, encoded)
}

func TestP361ACPReplayProjectionRollbackHidesRichMessageIDs(t *testing.T) {
	dir := t.TempDir()
	const sessionID = "p361-acp-rollback"
	p234bWriteReplayTranscript(
		t,
		dir,
		sessionID,
		p380ReplayRecordWithOrigin(t, "entry-rich", p361RichAssistantMessage()),
	)
	snapshot, err := session.LoadSessionReplaySnapshot(
		t.Context(),
		session.ResumeOptions{SessionID: sessionID, SessionDir: dir},
	)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := buildACPReplayProjection(snapshot, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.updates) != 2 {
		t.Fatalf("rollback updates = %#v", projection.updates)
	}
	for index, update := range projection.updates {
		chunk := update.AgentMessageChunk
		if chunk == nil || chunk.MessageId != nil {
			t.Fatalf("rollback update %d exposed a message ID: %#v", index, update)
		}
	}
}

func TestP361ACPReplayProjectionPreservesEmptyTextParts(t *testing.T) {
	dir := t.TempDir()
	const sessionID = "p361-acp-empty-text"
	p234bWriteReplayTranscript(
		t,
		dir,
		sessionID,
		p234bReplayRecord("entry-rich", &schema.Message{
			Role:    schema.Assistant,
			Content: "public",
			AssistantGenMultiContent: []schema.MessageOutputPart{
				{Type: schema.ChatMessagePartTypeText},
				{
					Type: schema.ChatMessagePartTypeReasoning,
					Reasoning: &schema.MessageOutputReasoning{
						Text:      "private-chain-of-thought",
						Signature: "encrypted-signature-blob",
					},
				},
				{Type: schema.ChatMessagePartTypeText, Text: "public"},
				{Type: schema.ChatMessagePartTypeText},
			},
		}),
	)
	snapshot, err := session.LoadSessionReplaySnapshot(
		t.Context(),
		session.ResumeOptions{SessionID: sessionID, SessionDir: dir},
	)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := buildACPReplayProjection(snapshot, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.updates) != 3 {
		t.Fatalf("empty text updates = %#v", projection.updates)
	}
	want := []string{"", "public", ""}
	for index, update := range projection.updates {
		chunk := update.AgentMessageChunk
		if chunk == nil || chunk.Content.Text == nil ||
			chunk.Content.Text.Text != want[index] {
			t.Fatalf("empty text update %d = %#v", index, update)
		}
	}
}

func TestP361ACPReplayReasoningOnlyAssistantEmitsNoTextChunk(t *testing.T) {
	dir := t.TempDir()
	const sessionID = "p361-acp-reasoning-only"
	p234bWriteReplayTranscript(
		t,
		dir,
		sessionID,
		p234bReplayRecord("entry-reasoning", &schema.Message{
			Role:             schema.Assistant,
			ReasoningContent: "private-chain-of-thought",
			AssistantGenMultiContent: []schema.MessageOutputPart{{
				Type: schema.ChatMessagePartTypeReasoning,
				Reasoning: &schema.MessageOutputReasoning{
					Text:      "private-chain-of-thought",
					Signature: "encrypted-signature-blob",
				},
			}},
			ToolCalls: []schema.ToolCall{{
				ID:   "p361-tool",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Read",
					Arguments: `{"path":"/tmp/p361"}`,
				},
			}},
		}),
		p234bReplayRecord("entry-result", &schema.Message{
			Role:       schema.Tool,
			ToolCallID: "p361-tool",
			Content:    "ok",
		}),
		p234bReplayRecord("entry-user", &schema.Message{
			Role:    schema.User,
			Content: "after",
		}),
	)
	snapshot, err := session.LoadSessionReplaySnapshot(
		t.Context(),
		session.ResumeOptions{SessionID: sessionID, SessionDir: dir},
	)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := buildACPReplayProjection(snapshot, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.updates) != 3 {
		t.Fatalf("reasoning-only updates = %#v", projection.updates)
	}
	if projection.updates[0].ToolCall == nil ||
		projection.updates[1].ToolCallUpdate == nil ||
		projection.updates[2].UserMessageChunk == nil {
		t.Fatalf("reasoning-only order = %#v", projection.updates)
	}
	for index, update := range projection.updates {
		if update.AgentMessageChunk != nil || update.AgentThoughtChunk != nil {
			t.Fatalf("reasoning-only update %d emitted assistant text: %#v", index, update)
		}
	}
	encoded, err := json.Marshal(projection.updates)
	if err != nil {
		t.Fatal(err)
	}
	p361RequireNoPrivateMarkers(t, encoded)
}

func TestP361ACPReplayReasoningOnlyAssistantWithoutToolsEmitsNoTextChunk(t *testing.T) {
	dir := t.TempDir()
	const sessionID = "p361-acp-reasoning-only-no-tools"
	p234bWriteReplayTranscript(
		t,
		dir,
		sessionID,
		p234bReplayRecord("entry-reasoning", &schema.Message{
			Role:             schema.Assistant,
			ReasoningContent: "private-chain-of-thought",
			AssistantGenMultiContent: []schema.MessageOutputPart{{
				Type: schema.ChatMessagePartTypeReasoning,
				Reasoning: &schema.MessageOutputReasoning{
					Text:      "private-chain-of-thought",
					Signature: "encrypted-signature-blob",
				},
			}},
		}),
		p234bReplayRecord("entry-user", &schema.Message{
			Role:    schema.User,
			Content: "after",
		}),
	)
	snapshot, err := session.LoadSessionReplaySnapshot(
		t.Context(),
		session.ResumeOptions{SessionID: sessionID, SessionDir: dir},
	)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := buildACPReplayProjection(snapshot, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.updates) != 1 ||
		projection.updates[0].UserMessageChunk == nil {
		t.Fatalf("reasoning-only no-tool updates = %#v", projection.updates)
	}
	encoded, err := json.Marshal(projection.updates)
	if err != nil {
		t.Fatal(err)
	}
	p361RequireNoPrivateMarkers(t, encoded)
}

func TestP361ACPProviderRichLoadDeliversPublicTextBeforeResponse(t *testing.T) {
	cwd := t.TempDir()
	dir := acpSessionTranscriptDir(cwd)
	const sessionID = "p361-load-rich"
	path := p234bWriteReplayTranscript(
		t,
		dir,
		sessionID,
		p234bReplayRecord("entry-user", &schema.Message{
			Role:    schema.User,
			Content: "question",
		}),
		p234bReplayRecord("entry-rich", p361RichAssistantMessage()),
	)
	p234bAppendProjectGraphMetadata(t, dir, sessionID, cwd)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	agent := p234bNewReplayAgent(t, cwd)
	continuationModel := &p361ContinuationModel{}
	agent.mockModel = continuationModel
	wire := newP231WireBuffer()
	p234bAttachReplayWriter(t, agent, wire)

	if _, err := agent.LoadSession(t.Context(), acpsdk.LoadSessionRequest{
		SessionId:  sessionID,
		Cwd:        cwd,
		McpServers: []acpsdk.McpServer{},
	}); err != nil {
		t.Fatal(err)
	}
	p234bRequireWireOrder(
		t,
		wire.Bytes(),
		`"sessionUpdate":"user_message_chunk"`,
		`"sessionUpdate":"agent_message_chunk"`,
		`"sessionUpdate":"config_option_update"`,
		`"sessionUpdate":"current_mode_update"`,
		`"sessionUpdate":"available_commands_update"`,
	)
	var messages []map[string]any
	if err := json.Unmarshal(
		normalizeP231Wire(t, wire.Bytes()),
		&messages,
	); err != nil {
		t.Fatal(err)
	}
	chunks := make([]map[string]any, 0, 2)
	for _, message := range messages {
		update := p231WireUpdate(t, message)
		if update["sessionUpdate"] == "agent_message_chunk" {
			chunks = append(chunks, update)
		}
	}
	if len(chunks) != 2 {
		t.Fatalf("load agent chunks = %d, want 2: %s", len(chunks), wire.Bytes())
	}
	firstContent, _ := chunks[0]["content"].(map[string]any)
	secondContent, _ := chunks[1]["content"].(map[string]any)
	if firstContent["text"] != "public-one " ||
		secondContent["text"] != "public-two" {
		t.Fatalf("load chunk texts = %#v / %#v", firstContent, secondContent)
	}
	if chunks[0]["messageId"] != "66666666-6666-4666-8666-666666666666" ||
		chunks[1]["messageId"] != chunks[0]["messageId"] {
		t.Fatalf(
			"load chunk message IDs = %#v / %#v",
			chunks[0]["messageId"],
			chunks[1]["messageId"],
		)
	}
	p361RequireNoPrivateMarkers(t, wire.Bytes())
	if calls, _, _, _ := continuationModel.snapshot(); calls != 0 {
		t.Fatal("provider-rich load executed the model")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("provider-rich load rewrote the transcript")
	}
	response, err := agent.Prompt(t.Context(), acpsdk.PromptRequest{
		SessionId: sessionID,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("continue")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.StopReason != acpsdk.StopReasonEndTurn {
		t.Fatalf("continuation stop reason = %q", response.StopReason)
	}
	calls, reasoningContent, reasoningText, signature := continuationModel.snapshot()
	if calls == 0 ||
		reasoningContent != "private-chain-of-thought" ||
		reasoningText != "private-chain-of-thought" ||
		signature != "encrypted-signature-blob" {
		t.Fatalf(
			"restored continuation = calls:%d content:%q text:%q signature:%q",
			calls,
			reasoningContent,
			reasoningText,
			signature,
		)
	}
}

func TestP361ACPProviderRichLoadRejectsMalformedBeforeFirstUpdate(t *testing.T) {
	rich := func(content string, parts ...schema.MessageOutputPart) *schema.Message {
		return &schema.Message{
			Role:                     schema.Assistant,
			Content:                  content,
			ReasoningContent:         "private-chain-of-thought",
			AssistantGenMultiContent: parts,
		}
	}
	tests := []struct {
		name    string
		message *schema.Message
	}{
		{
			name: "public text mismatch",
			message: rich("different", schema.MessageOutputPart{
				Type: schema.ChatMessagePartTypeText,
				Text: "public-one",
			}),
		},
		{
			name: "image output part",
			message: rich("", schema.MessageOutputPart{
				Type:  schema.ChatMessagePartTypeImageURL,
				Image: &schema.MessageOutputImage{},
			}),
		},
		{
			name: "audio output part",
			message: rich("", schema.MessageOutputPart{
				Type:  schema.ChatMessagePartTypeAudioURL,
				Audio: &schema.MessageOutputAudio{},
			}),
		},
		{
			name: "video output part",
			message: rich("", schema.MessageOutputPart{
				Type:  schema.ChatMessagePartTypeVideoURL,
				Video: &schema.MessageOutputVideo{},
			}),
		},
		{
			name: "nil reasoning payload",
			message: rich("", schema.MessageOutputPart{
				Type: schema.ChatMessagePartTypeReasoning,
			}),
		},
		{
			name: "unknown output type",
			message: rich("", schema.MessageOutputPart{
				Type: schema.ChatMessagePartType("private-unknown-type-marker"),
			}),
		},
		{
			name: "text part carries reasoning payload",
			message: rich("public", schema.MessageOutputPart{
				Type:      schema.ChatMessagePartTypeText,
				Text:      "public",
				Reasoning: &schema.MessageOutputReasoning{Text: "x"},
			}),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cwd := t.TempDir()
			dir := acpSessionTranscriptDir(cwd)
			sessionID := "p361-load-invalid-" + strings.ReplaceAll(test.name, " ", "-")
			p234bWriteReplayTranscript(
				t,
				dir,
				sessionID,
				p234bReplayRecord("entry-rich", test.message),
			)
			p234bAppendProjectGraphMetadata(t, dir, sessionID, cwd)
			agent := p234bNewReplayAgent(t, cwd)
			wire := newP231WireBuffer()
			p234bAttachReplayWriter(t, agent, wire)

			response, loadErr := agent.LoadSession(
				t.Context(),
				acpsdk.LoadSessionRequest{
					SessionId:  acpsdk.SessionId(sessionID),
					Cwd:        cwd,
					McpServers: []acpsdk.McpServer{},
				},
			)
			if loadErr == nil {
				t.Fatalf("malformed rich load response = %#v", response)
			}
			if got := len(wire.Bytes()); got != 0 {
				t.Fatalf("malformed rich load sent %d wire bytes: %s", got, wire.Bytes())
			}
			agent.mu.Lock()
			_, active := agent.sessions[acpsdk.SessionId(sessionID)]
			agent.mu.Unlock()
			if active {
				t.Fatal("malformed rich load registered an active session")
			}
			if strings.Contains(loadErr.Error(), "private-unknown-type-marker") {
				t.Fatalf("malformed rich load leaked provider type: %v", loadErr)
			}
		})
	}
}
