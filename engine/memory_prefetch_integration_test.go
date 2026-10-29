package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/compact"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestQueryEngineMemoryPrefetchIsNotRepeatedAcrossToolRounds(t *testing.T) {
	for _, state := range []string{"fresh", "already_visible", "after_compact_boundary"} {
		t.Run(state, func(t *testing.T) {
			cwd := t.TempDir()
			path := filepath.Join(cwd, "fixture.txt")
			const readContent = "independent memory-prefetch read fixture"
			if err := os.WriteFile(path, []byte(readContent), 0o644); err != nil {
				t.Fatal(err)
			}
			const memoryContent = "independent retained fact for the multi-round fixture"
			const attachmentContent = "[Session Memory — context from previous interactions:]\n\n- [fact] " + memoryContent + "\n"
			mdl := &memoryPrefetchFixtureModel{path: path, readContent: readContent, memoryContent: memoryContent}
			registry := tools.NewRegistry()
			tools.RegisterDefaults(registry)
			eng := NewQueryEngine(QueryEngineConfig{
				CWD: cwd, TranscriptDir: t.TempDir(), ChatModel: mdl,
				CustomSystemPrompt: "Use the fixture tools.", ToolRegistry: registry,
				PermissionMode: permission.ModeBypassPermissions,
			})
			t.Cleanup(eng.Close)
			if err := eng.memoryStore.Add(compact.MemoryEntry{
				Category: "fact", Content: memoryContent, CreatedAt: time.Unix(1, 0),
			}); err != nil {
				t.Fatal(err)
			}
			if state != "fresh" {
				history := []*schema.Message{
					{Role: schema.User, Content: "Earlier fixture request."},
					{Role: schema.System, Content: attachmentContent, Extra: map[string]any{
						"is_meta": true, "attachment_kind": "memory_prefetch",
					}},
				}
				if state == "after_compact_boundary" {
					history = append(history, &schema.Message{
						Role: schema.System, Content: "retained compact summary",
						Extra: map[string]any{"subtype": "compact_boundary"},
					})
				}
				eng.SetResumedMessages(history)
			}
			events, admission := eng.SubmitMessage(t.Context(), "Inspect the independent fixture.")
			if admission.Err != nil {
				t.Fatal(admission.Err)
			}
			var terminal *Terminal
			attachments := 0
			for event := range events {
				if event.Type == EventTerminal {
					terminal = event.TerminalInfo
				}
				if event.Type == EventAttachment && event.AttachmentMessage != nil &&
					event.AttachmentMessage.Extra["attachment_kind"] == "memory_prefetch" {
					attachments++
				}
			}
			if terminal == nil || terminal.Reason != TerminalCompleted {
				t.Fatalf("terminal = %#v", terminal)
			}
			wantCopies := []int{0, 1, 1, 1}
			wantAttachments := 1
			if state == "already_visible" {
				wantCopies[0] = 1
				wantAttachments = 0
			}
			t.Logf("model request memory copies = %v; attachments = %d", mdl.copies, attachments)
			if !slices.Equal(mdl.copies, wantCopies) || attachments != wantAttachments {
				t.Fatalf("copies=%v attachments=%d; want copies=%v attachments=%d", mdl.copies, attachments, wantCopies, wantAttachments)
			}
		})
	}
}

// Scripted Read rounds inspect the exact input handed to the model by the
// production QueryEngine. No provider, memory tool, or benchmark is involved.
type memoryPrefetchFixtureModel struct {
	path          string
	readContent   string
	memoryContent string
	copies        []int
}

func (m *memoryPrefetchFixtureModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, fmt.Errorf("fixture requires streaming")
}

func (m *memoryPrefetchFixtureModel) Stream(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	copies := 0
	readSeen := false
	for _, message := range input {
		if message == nil {
			continue
		}
		copies += strings.Count(message.Content, m.memoryContent)
		readSeen = readSeen || (message.Role == schema.Tool && strings.HasPrefix(message.ToolCallID, "memory-prefetch-read-") && strings.Contains(message.Content, m.readContent))
	}
	m.copies = append(m.copies, copies)
	round := len(m.copies)
	if round > 1 && !readSeen {
		return nil, fmt.Errorf("request %d missing the completed Read result", round)
	}
	response := &schema.Message{Role: schema.Assistant}
	switch {
	case round <= 3:
		args, err := json.Marshal(map[string]string{"file_path": m.path})
		if err != nil {
			return nil, err
		}
		response.ToolCalls = []schema.ToolCall{{
			ID: fmt.Sprintf("memory-prefetch-read-%d", round), Type: "function",
			Function: schema.FunctionCall{Name: "Read", Arguments: string(args)},
		}}
	case round == 4:
		response.Content = "Fixture complete."
	default:
		return nil, fmt.Errorf("unexpected model request %d", round)
	}
	return schema.StreamReaderFromArray([]*schema.Message{response}), nil
}
