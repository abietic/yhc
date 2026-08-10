package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/transcript"
)

func TestP234aReplaySnapshotMatchesResumeAndIsMutationIsolated(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	const sessionID = "snapshot-equivalence"
	recorder := transcript.NewRecorder(sessionID, dir)
	if err := recorder.RecordMessages([]*schema.Message{{
		Role:    schema.User,
		Content: "superseded",
	}}); err != nil {
		t.Fatal(err)
	}
	active := []*schema.Message{
		{
			Role:    schema.User,
			Content: "active user",
			Extra: map[string]any{
				"nested": map[string]any{"values": []any{"original"}},
			},
		},
		{
			Role:    schema.Assistant,
			Content: "active assistant",
			Extra:   map[string]any{"message_id": "logical-active"},
			ToolCalls: []schema.ToolCall{{
				ID:   "tool-success",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Read",
					Arguments: `{"path":"original"}`,
				},
			}},
		},
		{
			Role:       schema.Tool,
			ToolCallID: "tool-success",
			Content:    "ok",
		},
	}
	if err := recorder.RecordLifecycleBoundary(
		transcript.LifecycleCheckpoint,
		active,
		nil,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	later := []*schema.Message{
		{
			Role:    schema.Assistant,
			Content: "later assistant",
			Extra:   map[string]any{"message_id": "logical-later"},
			ToolCalls: []schema.ToolCall{{
				ID:   "tool-failed",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Bash",
					Arguments: `{"command":"false"}`,
				},
			}},
		},
		{
			Role:       schema.Tool,
			ToolCallID: "tool-failed",
			Content:    "exit 1",
			Extra:      map[string]any{"is_error": true},
		},
	}
	if err := recorder.RecordMessages(later); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}

	path := recorder.Path()
	before := readP234aReplayBytes(t, path)
	beforeFiles := p234aReplayDirectoryNames(t, dir)
	resumed, err := ResumeSession(t.Context(), ResumeOptions{
		SessionID:  sessionID,
		SessionDir: dir,
	})
	if err != nil {
		t.Fatalf("ordinary resume: %v", err)
	}
	snapshot, err := LoadSessionReplaySnapshot(t.Context(), ResumeOptions{
		SessionID:  sessionID,
		SessionDir: dir,
	})
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	items := snapshot.Items()
	itemMessages := make([]*schema.Message, len(items))
	for index := range items {
		itemMessages[index] = items[index].Message
	}
	if !reflect.DeepEqual(itemMessages, resumed.Messages) {
		t.Fatalf("snapshot messages differ from resume:\n got %#v\nwant %#v", itemMessages, resumed.Messages)
	}
	if snapshot.SessionID != sessionID || snapshot.Revision == "" || len(items) != 5 {
		t.Fatalf("snapshot header/items = %#v / %d", snapshot, len(items))
	}
	if items[1].LogicalMessageID != "logical-active" ||
		items[3].LogicalMessageID != "logical-later" {
		t.Fatalf("logical IDs = %q, %q", items[1].LogicalMessageID, items[3].LogicalMessageID)
	}
	if items[0].ToolOutcome != SessionReplayToolOutcomeNone ||
		items[2].ToolOutcome != SessionReplayToolOutcomeSucceeded ||
		items[4].ToolOutcome != SessionReplayToolOutcomeFailed {
		t.Fatalf(
			"tool outcomes = %q, %q, %q",
			items[0].ToolOutcome,
			items[2].ToolOutcome,
			items[4].ToolOutcome,
		)
	}

	loaded, err := transcript.NewRecorder(sessionID, dir).LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	wantIdentities := []transcript.MessageEntryIdentity{
		{Record: loaded.Entries[1].Identity, Index: 0},
		{Record: loaded.Entries[1].Identity, Index: 1},
		{Record: loaded.Entries[1].Identity, Index: 2},
		{Record: loaded.Entries[2].Identity, Index: 0},
		{Record: loaded.Entries[3].Identity, Index: 0},
	}
	for index, want := range wantIdentities {
		if items[index].Identity.Key() != want.Key() ||
			items[index].Identity.Record.IsLegacy() {
			t.Fatalf(
				"item[%d] identity = %s legacy=%v, want %s",
				index,
				items[index].Identity.Key(),
				items[index].Identity.Record.IsLegacy(),
				want.Key(),
			)
		}
	}

	items[0].Message.Content = "mutated"
	items[0].Message.Extra["nested"].(map[string]any)["values"].([]any)[0] = "mutated"
	items[1].Message.ToolCalls[0].Function.Arguments = `{"path":"mutated"}`
	secondRead := snapshot.Items()
	if secondRead[0].Message.Content != "active user" ||
		secondRead[0].Message.Extra["nested"].(map[string]any)["values"].([]any)[0] != "original" ||
		secondRead[1].Message.ToolCalls[0].Function.Arguments != `{"path":"original"}` {
		t.Fatalf("snapshot mutation escaped into later Items call: %#v", secondRead)
	}
	secondRead[0].Message.Content = "second mutation"
	if thirdRead := snapshot.Items(); thirdRead[0].Message.Content != "active user" {
		t.Fatal("one Items result mutated another result")
	}
	if !bytes.Equal(before, readP234aReplayBytes(t, path)) {
		t.Fatal("snapshot read or caller mutation changed transcript bytes")
	}
	if afterFiles := p234aReplayDirectoryNames(t, dir); !reflect.DeepEqual(beforeFiles, afterFiles) {
		t.Fatalf("snapshot read changed directory entries: before=%v after=%v", beforeFiles, afterFiles)
	}
}

func TestP305bReplaySnapshotBindsLogicalPromptPartsWithoutWrites(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	const sessionID = "p305b-rich-snapshot"
	path, _ := createP305aRichSession(t, dir, sessionID)
	before := readP305bReplayTree(t, dir)

	snapshot, err := LoadSessionReplaySnapshot(t.Context(), ResumeOptions{
		SessionID:  sessionID,
		SessionDir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	items := snapshot.Items()
	if len(items) != 2 ||
		items[0].Message == nil ||
		items[0].Message.Role != schema.User ||
		len(items[0].PromptParts) != 6 ||
		len(items[1].PromptParts) != 0 {
		t.Fatalf("replay items = %#v", items)
	}
	wantKinds := []SessionReplayPromptPartKind{
		SessionReplayPromptPartText,
		SessionReplayPromptPartResourceLink,
		SessionReplayPromptPartImage,
		SessionReplayPromptPartEmbeddedText,
		SessionReplayPromptPartEmbeddedBlob,
		SessionReplayPromptPartText,
	}
	for index, want := range wantKinds {
		if got := items[0].PromptParts[index].Kind; got != want {
			t.Fatalf("prompt part %d kind = %q, want %q", index, got, want)
		}
	}
	parts := items[0].PromptParts
	if parts[0].Text == nil || parts[0].Text.Text != "head" ||
		parts[1].ResourceLink == nil ||
		parts[1].ResourceLink.URI != "file:///workspace/schema.json" ||
		parts[2].Image == nil ||
		parts[2].Image.Data == "" ||
		parts[2].Image.MIMEType != "image/png" ||
		parts[3].EmbeddedText == nil ||
		parts[3].EmbeddedText.Text != "embedded context" ||
		parts[4].EmbeddedBlob == nil ||
		parts[4].EmbeddedBlob.Data == "" ||
		parts[4].EmbeddedBlob.URI != "file:///workspace/pixel.png" ||
		parts[5].Text == nil ||
		parts[5].Text.Text != "tail" {
		t.Fatalf("logical prompt parts = %#v", parts)
	}

	parts[0].Text.Text = "mutated"
	*parts[1].ResourceLink.MIMEType = "mutated/type"
	parts[2].Image.Data = "mutated"
	second := snapshot.Items()
	if second[0].PromptParts[0].Text.Text != "head" ||
		*second[0].PromptParts[1].ResourceLink.MIMEType != "application/json" ||
		second[0].PromptParts[2].Image.Data == "mutated" {
		t.Fatalf("prompt part mutation escaped into snapshot: %#v", second[0])
	}
	if after := readP305bReplayTree(t, dir); !reflect.DeepEqual(before, after) {
		t.Fatalf("replay snapshot changed transcript/media tree %s", path)
	}
}

func TestP234aReplaySnapshotLegacyIdentityAndAnonymousPairing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	const sessionID = "legacy-anonymous"
	path := p234aWriteReplayLines(
		t,
		dir,
		sessionID,
		p234aReplayRecord(t, "assistant", &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Read",
					Arguments: `{}`,
				},
			}},
		}, nil, nil),
		p234aReplayRecord(t, "tool", &schema.Message{
			Role:    schema.Tool,
			Content: "ok",
		}, nil, nil),
	)
	before := readP234aReplayBytes(t, path)
	first := p234aLoadReplaySnapshot(t, dir, sessionID)
	second := p234aLoadReplaySnapshot(t, dir, sessionID)
	firstItems := first.Items()
	secondItems := second.Items()
	if len(firstItems) != 2 ||
		!firstItems[0].Identity.Record.IsLegacy() ||
		!firstItems[1].Identity.Record.IsLegacy() {
		t.Fatalf("legacy items = %#v", firstItems)
	}
	if firstItems[0].RecordOrdinal != 0 ||
		firstItems[1].RecordOrdinal != 1 ||
		!reflect.DeepEqual(
			firstItems[0].AnonymousToolCallIndexes,
			[]int{0},
		) {
		t.Fatalf("legacy physical projection = %#v", firstItems)
	}
	resolvedID := firstItems[0].Message.ToolCalls[0].ID
	if resolvedID == "" ||
		resolvedID != firstItems[1].Message.ToolCallID ||
		resolvedID != secondItems[0].Message.ToolCalls[0].ID ||
		resolvedID != secondItems[1].Message.ToolCallID ||
		!strings.HasSuffix(resolvedID, "/tool/0") {
		t.Fatalf("resolved legacy IDs = %q / %#v", resolvedID, secondItems)
	}
	if first.Revision != second.Revision ||
		firstItems[0].Identity.Key() != secondItems[0].Identity.Key() {
		t.Fatal("legacy identities changed within one transcript revision")
	}
	firstItems[0].AnonymousToolCallIndexes[0] = 9
	if got := first.Items()[0].AnonymousToolCallIndexes; !reflect.DeepEqual(
		got,
		[]int{0},
	) {
		t.Fatalf("anonymous tool indexes were not clone-on-read: %v", got)
	}
	if !bytes.Equal(before, readP234aReplayBytes(t, path)) {
		t.Fatal("legacy snapshot changed transcript bytes")
	}
}

func TestP234aReplaySnapshotFailsClosed(t *testing.T) {
	t.Parallel()

	message := func(role schema.RoleType, content string) *schema.Message {
		return &schema.Message{Role: role, Content: content}
	}
	call := func(id string) schema.ToolCall {
		return schema.ToolCall{
			ID:   id,
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "Read",
				Arguments: `{}`,
			},
		}
	}
	persistedID := func(id string) *transcript.EntryIdentity {
		return &transcript.EntryIdentity{Version: 1, ID: id}
	}
	tests := []struct {
		name  string
		lines func(*testing.T) []string
	}{
		{
			name: "corruption",
			lines: func(t *testing.T) []string {
				return []string{
					"not-json",
					p234aReplayRecord(t, "user", message(schema.User, "valid"), nil, nil),
				}
			},
		},
		{
			name: "duplicate durable identity",
			lines: func(t *testing.T) []string {
				id := persistedID("duplicate")
				return []string{
					p234aReplayRecord(t, "user", message(schema.User, "one"), nil, id),
					p234aReplayRecord(t, "assistant", message(schema.Assistant, "two"), nil, id),
				}
			},
		},
		{
			name: "duplicate logical identity",
			lines: func(t *testing.T) []string {
				first := message(schema.Assistant, "one")
				first.Extra = map[string]any{"message_id": "same"}
				second := message(schema.Assistant, "two")
				second.Extra = map[string]any{"message_id": "same"}
				return []string{
					p234aReplayRecord(t, "assistant", first, nil, nil),
					p234aReplayRecord(t, "assistant", second, nil, nil),
				}
			},
		},
		{
			name: "malformed logical identity",
			lines: func(t *testing.T) []string {
				msg := message(schema.Assistant, "bad")
				msg.Extra = map[string]any{"message_id": 42}
				return []string{p234aReplayRecord(t, "assistant", msg, nil, nil)}
			},
		},
		{
			name: "duplicate tool call ID",
			lines: func(t *testing.T) []string {
				msg := message(schema.Assistant, "")
				msg.ToolCalls = []schema.ToolCall{call("same"), call("same")}
				return []string{p234aReplayRecord(t, "assistant", msg, nil, nil)}
			},
		},
		{
			name: "orphan tool result",
			lines: func(t *testing.T) []string {
				msg := message(schema.Tool, "orphan")
				msg.ToolCallID = "missing"
				return []string{p234aReplayRecord(t, "tool", msg, nil, nil)}
			},
		},
		{
			name: "unknown tool outcome",
			lines: func(t *testing.T) []string {
				assistant := message(schema.Assistant, "")
				assistant.ToolCalls = []schema.ToolCall{call("call")}
				result := message(schema.Tool, "bad")
				result.ToolCallID = "call"
				result.Extra = map[string]any{"is_error": "yes"}
				return []string{
					p234aReplayRecord(t, "assistant", assistant, nil, nil),
					p234aReplayRecord(t, "tool", result, nil, nil),
				}
			},
		},
		{
			name: "unsettled tool call",
			lines: func(t *testing.T) []string {
				msg := message(schema.Assistant, "")
				msg.ToolCalls = []schema.ToolCall{call("pending")}
				return []string{p234aReplayRecord(t, "assistant", msg, nil, nil)}
			},
		},
		{
			name: "ambiguous anonymous pairing",
			lines: func(t *testing.T) []string {
				assistant := message(schema.Assistant, "")
				assistant.ToolCalls = []schema.ToolCall{call(""), call("")}
				result := message(schema.Tool, "ambiguous")
				return []string{
					p234aReplayRecord(t, "assistant", assistant, nil, nil),
					p234aReplayRecord(t, "tool", result, nil, nil),
				}
			},
		},
		{
			name: "unknown role",
			lines: func(t *testing.T) []string {
				return []string{
					p234aReplayRecord(t, "developer", message(schema.RoleType("developer"), "bad"), nil, nil),
				}
			},
		},
		{
			name: "nil lifecycle message",
			lines: func(t *testing.T) []string {
				return []string{
					p234aReplayRecord(t, string(transcript.LifecycleReset), nil, []*schema.Message{nil}, nil),
				}
			},
		},
		{
			name: "metadata only",
			lines: func(t *testing.T) []string {
				return []string{p234aReplayMetadataRecord(t)}
			},
		},
		{
			name: "tool calls on user role",
			lines: func(t *testing.T) []string {
				msg := message(schema.User, "bad")
				msg.ToolCalls = []schema.ToolCall{call("unexpected")}
				return []string{p234aReplayRecord(t, "user", msg, nil, nil)}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			sessionID := strings.ReplaceAll(test.name, " ", "-")
			p234aWriteReplayLines(t, dir, sessionID, test.lines(t)...)
			snapshot, err := LoadSessionReplaySnapshot(t.Context(), ResumeOptions{
				SessionID:  sessionID,
				SessionDir: dir,
			})
			if !errors.Is(err, ErrSessionReplayInvalid) || snapshot != nil {
				t.Fatalf("snapshot=%#v error=%v", snapshot, err)
			}
		})
	}
}

func TestP234aReplaySnapshotCancellationReturnsNoPartialValue(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	const sessionID = "snapshot-cancel"
	lines := make([]string, 0, 8)
	for index := 0; index < 8; index++ {
		lines = append(lines, p234aReplayRecord(t, "user", &schema.Message{
			Role:    schema.User,
			Content: strings.Repeat("x", 128),
		}, nil, nil))
	}
	p234aWriteReplayLines(t, dir, sessionID, lines...)

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	snapshot, err := LoadSessionReplaySnapshot(canceled, ResumeOptions{
		SessionID:  sessionID,
		SessionDir: dir,
	})
	if !errors.Is(err, context.Canceled) || snapshot != nil {
		t.Fatalf("pre-load cancellation snapshot=%#v error=%v", snapshot, err)
	}

	during := &p234aCancelAfterErrContext{
		Context: t.Context(),
		after:   3,
	}
	snapshot, err = LoadSessionReplaySnapshot(during, ResumeOptions{
		SessionID:  sessionID,
		SessionDir: dir,
	})
	if !errors.Is(err, context.Canceled) || snapshot != nil {
		t.Fatalf("construction cancellation snapshot=%#v error=%v", snapshot, err)
	}
}

func TestP234aReplaySnapshotAllowsEmptyLifecycleContext(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	const sessionID = "empty-lifecycle"
	p234aWriteReplayLines(
		t,
		dir,
		sessionID,
		p234aReplayRecord(
			t,
			string(transcript.LifecycleReset),
			nil,
			[]*schema.Message{},
			nil,
		),
	)
	resumed, err := ResumeSession(t.Context(), ResumeOptions{
		SessionID:  sessionID,
		SessionDir: dir,
	})
	if err != nil || len(resumed.Messages) != 0 {
		t.Fatalf("ordinary empty lifecycle resume = %#v, %v", resumed, err)
	}
	snapshot := p234aLoadReplaySnapshot(t, dir, sessionID)
	if len(snapshot.Items()) != 0 {
		t.Fatalf("empty lifecycle items = %#v", snapshot.Items())
	}
}

type p234aCancelAfterErrContext struct {
	context.Context
	calls atomic.Int32
	after int32
}

func (c *p234aCancelAfterErrContext) Err() error {
	if c.calls.Add(1) >= c.after {
		return context.Canceled
	}
	return nil
}

func p234aLoadReplaySnapshot(
	t *testing.T,
	dir string,
	sessionID string,
) *SessionReplaySnapshot {
	t.Helper()
	snapshot, err := LoadSessionReplaySnapshot(t.Context(), ResumeOptions{
		SessionID:  sessionID,
		SessionDir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot == nil {
		t.Fatal("snapshot is nil")
	}
	return snapshot
}

func p234aWriteReplayLines(
	t *testing.T,
	dir string,
	sessionID string,
	lines ...string,
) string {
	t.Helper()
	path := filepath.Join(dir, sessionID+".jsonl")
	if err := os.WriteFile(
		path,
		[]byte(strings.Join(lines, "\n")+"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	return path
}

func p234aReplayRecord(
	t *testing.T,
	kind string,
	message *schema.Message,
	messages []*schema.Message,
	identity *transcript.EntryIdentity,
) string {
	t.Helper()
	record := map[string]any{
		"timestamp": time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC),
		"kind":      kind,
	}
	if message != nil {
		record["message"] = message
	}
	if messages != nil {
		record["messages"] = messages
	}
	if identity != nil {
		record["entry_id"] = identity
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func p234aReplayMetadataRecord(t *testing.T) string {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"timestamp":  time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC),
		"kind":       "metadata",
		"meta_key":   "session_metadata_full",
		"meta_value": `{"session_id":"metadata-only"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func readP234aReplayBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func p234aReplayDirectoryNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

func readP305bReplayTree(t *testing.T, root string) map[string][]byte {
	t.Helper()
	files := make(map[string][]byte)
	if err := filepath.Walk(
		root,
		func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			files[relative] = data
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}
	return files
}

func TestP361ReplaySnapshotProjectsOrderedPublicTextOnly(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	const sessionID = "p361-rich-assistant"
	rich := &schema.Message{
		Role:    schema.Assistant,
		Content: "public-one public-two",
		Extra: map[string]any{
			"message_id":     "logical-p361",
			"provider_trace": "private-provider-marker",
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
	plain := &schema.Message{Role: schema.Assistant, Content: "plain tail"}
	path := p234aWriteReplayLines(
		t,
		dir,
		sessionID,
		p234aReplayRecord(t, "assistant", rich, nil, nil),
		p234aReplayRecord(t, "assistant", plain, nil, nil),
	)
	before := readP234aReplayBytes(t, path)

	snapshot := p234aLoadReplaySnapshot(t, dir, sessionID)
	items := snapshot.Items()
	if len(items) != 2 {
		t.Fatalf("items = %#v", items)
	}
	if items[0].LogicalMessageID != "logical-p361" {
		t.Fatalf("logical ID = %q", items[0].LogicalMessageID)
	}
	presentation := items[0].AssistantPresentation
	if presentation == nil || !reflect.DeepEqual(
		presentation.TextParts,
		[]string{"public-one ", "public-two"},
	) {
		t.Fatalf("assistant presentation = %#v", presentation)
	}
	encoded, err := json.Marshal(presentation)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{
		"private-chain-of-thought",
		"encrypted-signature-blob",
		"private-provider-marker",
		"private-part-extra",
		"stream_marker",
		"provider_trace",
	} {
		if bytes.Contains(encoded, []byte(private)) {
			t.Fatalf("assistant projection leaked %q: %s", private, encoded)
		}
	}
	// The projection clone retains the complete private continuation
	// material; only the derived presentation is private-free.
	clone := items[0].Message
	if clone.ReasoningContent != "private-chain-of-thought" ||
		len(clone.AssistantGenMultiContent) != 3 ||
		clone.AssistantGenMultiContent[1].Reasoning == nil ||
		clone.AssistantGenMultiContent[1].Reasoning.Signature !=
			"encrypted-signature-blob" {
		t.Fatalf("projection clone lost private continuation fields: %#v", clone)
	}
	if items[1].AssistantPresentation != nil {
		t.Fatalf("plain assistant presentation = %#v", items[1].AssistantPresentation)
	}

	presentation.TextParts[0] = "mutated"
	if again := snapshot.Items(); again[0].AssistantPresentation.TextParts[0] != "public-one " {
		t.Fatalf("assistant part mutation escaped into snapshot: %#v", again[0])
	}
	if !bytes.Equal(before, readP234aReplayBytes(t, path)) {
		t.Fatal("assistant projection changed transcript bytes")
	}
}

func TestP361ReplaySnapshotRejectsMalformedAssistantOutput(t *testing.T) {
	t.Parallel()

	textPart := func(text string) schema.MessageOutputPart {
		return schema.MessageOutputPart{
			Type: schema.ChatMessagePartTypeText,
			Text: text,
		}
	}
	rich := func(content string, parts ...schema.MessageOutputPart) *schema.Message {
		return &schema.Message{
			Role:                     schema.Assistant,
			Content:                  content,
			ReasoningContent:         "private-chain-of-thought",
			AssistantGenMultiContent: parts,
		}
	}
	mixedUnions := rich("public", textPart("public"))
	mixedUnions.UserInputMultiContent = []schema.MessageInputPart{{
		Type: schema.ChatMessagePartTypeText,
		Text: "legacy",
	}}

	tests := []struct {
		name    string
		message *schema.Message
	}{
		{
			name:    "public text mismatch",
			message: rich("different", textPart("public-one")),
		},
		{
			name: "text part carries reasoning payload",
			message: rich("public", schema.MessageOutputPart{
				Type:      schema.ChatMessagePartTypeText,
				Text:      "public",
				Reasoning: &schema.MessageOutputReasoning{Text: "x"},
			}),
		},
		{
			name: "text part carries image payload",
			message: rich("public", schema.MessageOutputPart{
				Type:  schema.ChatMessagePartTypeText,
				Text:  "public",
				Image: &schema.MessageOutputImage{},
			}),
		},
		{
			name: "nil reasoning payload",
			message: rich("", schema.MessageOutputPart{
				Type: schema.ChatMessagePartTypeReasoning,
			}),
		},
		{
			name: "reasoning part carries text",
			message: rich("", schema.MessageOutputPart{
				Type: schema.ChatMessagePartTypeReasoning,
				Text: "leak",
				Reasoning: &schema.MessageOutputReasoning{
					Text: "private-chain-of-thought",
				},
			}),
		},
		{
			name: "unknown output type",
			message: rich("", schema.MessageOutputPart{
				Type: schema.ChatMessagePartType("private-unknown-type-marker"),
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
			name:    "mixed content unions",
			message: mixedUnions,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			sessionID := "p361-invalid-" + strings.ReplaceAll(test.name, " ", "-")
			p234aWriteReplayLines(
				t,
				dir,
				sessionID,
				p234aReplayRecord(t, "assistant", test.message, nil, nil),
			)
			snapshot, err := LoadSessionReplaySnapshot(t.Context(), ResumeOptions{
				SessionID:  sessionID,
				SessionDir: dir,
			})
			if !errors.Is(err, ErrSessionReplayInvalid) || snapshot != nil {
				t.Fatalf("snapshot=%#v error=%v", snapshot, err)
			}
			for _, private := range []string{
				"private-chain-of-thought",
				"encrypted-signature-blob",
				"leak",
				"private-unknown-type-marker",
			} {
				if strings.Contains(err.Error(), private) {
					t.Fatalf("diagnostic leaked private content %q: %v", private, err)
				}
			}
		})
	}
}

func TestP361ReplaySnapshotReasoningOnlyAssistantKeepsToolPairing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	const sessionID = "p361-reasoning-only"
	assistant := &schema.Message{
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
				Arguments: `{}`,
			},
		}},
	}
	result := &schema.Message{
		Role:       schema.Tool,
		ToolCallID: "p361-tool",
		Content:    "ok",
	}
	path := p234aWriteReplayLines(
		t,
		dir,
		sessionID,
		p234aReplayRecord(t, "assistant", assistant, nil, nil),
		p234aReplayRecord(t, "tool", result, nil, nil),
	)
	before := readP234aReplayBytes(t, path)

	snapshot := p234aLoadReplaySnapshot(t, dir, sessionID)
	items := snapshot.Items()
	if len(items) != 2 {
		t.Fatalf("items = %#v", items)
	}
	presentation := items[0].AssistantPresentation
	if presentation == nil || len(presentation.TextParts) != 0 {
		t.Fatalf("reasoning-only presentation = %#v", presentation)
	}
	if items[0].Message.ToolCalls[0].ID != "p361-tool" ||
		items[1].Message.ToolCallID != "p361-tool" ||
		items[1].ToolOutcome != SessionReplayToolOutcomeSucceeded {
		t.Fatalf("reasoning-only tool pairing = %#v / %#v", items[0], items[1])
	}
	if !bytes.Equal(before, readP234aReplayBytes(t, path)) {
		t.Fatal("reasoning-only snapshot changed transcript bytes")
	}
}
