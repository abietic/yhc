package session

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/transcript"
)

// --- Export Tests ---

func TestExportSession_Markdown_Basic(t *testing.T) {
	dir := t.TempDir()

	// Create a session with user/assistant messages.
	rec := transcript.NewRecorder("export-md-test", dir)
	msgs := []*schema.Message{
		{Role: schema.User, Content: "What is Go?"},
		{Role: schema.Assistant, Content: "Go is a programming language."},
		{Role: schema.User, Content: "Tell me more."},
		{Role: schema.Assistant, Content: "It was created at Google."},
	}
	if err := rec.Record(msgs[:2], false); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.Record(msgs[2:], false); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	result, err := ExportSession(ExportOptions{
		SessionID:        "export-md-test",
		Dir:              dir,
		Format:           ExportMarkdown,
		IncludeToolCalls: false,
		IncludeMetadata:  false,
	})
	if err != nil {
		t.Fatalf("export: %v", err)
		return
	}

	if result.Format != ExportMarkdown {
		t.Fatalf("expected markdown format")
	}
	if result.MessageCount != 4 {
		t.Fatalf("expected 4 messages, got %d", result.MessageCount)
	}

	// Verify content structure.
	if !strings.Contains(result.Content, "# Session: export-md-test") {
		t.Fatal("missing session header")
	}
	if !strings.Contains(result.Content, "## User") {
		t.Fatal("missing user section")
	}
	if !strings.Contains(result.Content, "## Assistant") {
		t.Fatal("missing assistant section")
	}
	if !strings.Contains(result.Content, "What is Go?") {
		t.Fatal("missing user content")
	}
	if !strings.Contains(result.Content, "Go is a programming language.") {
		t.Fatal("missing assistant content")
	}
}

func TestExportSession_Markdown_WithMetadata(t *testing.T) {
	dir := t.TempDir()

	rec := transcript.NewRecorder("export-meta-md", dir)
	if err := rec.Record([]*schema.Message{{Role: schema.User, Content: "hello"}}, false); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.RecordMetadata("git_branch", "main"); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.RecordMetadata("cwd", "/project"); err != nil {
		t.Fatal(err)
		return
	}
	metaFull := &SessionMetadataFull{
		SessionID: "export-meta-md",
		Model:     "claude-sonnet-4-20250514",
		Provider:  "claude",
		GitBranch: "main",
		CWD:       "/project",
	}
	if err := WriteSessionMetadata(rec, metaFull); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	result, err := ExportSession(ExportOptions{
		SessionID:       "export-meta-md",
		Dir:             dir,
		Format:          ExportMarkdown,
		IncludeMetadata: true,
	})
	if err != nil {
		t.Fatal(err)
		return
	}

	// Verify metadata header.
	if !strings.Contains(result.Content, "model: claude-sonnet-4-20250514") {
		t.Fatal("missing model in metadata")
	}
	if !strings.Contains(result.Content, "provider: claude") {
		t.Fatal("missing provider in metadata")
	}
	if !strings.Contains(result.Content, "git_branch: main") {
		t.Fatal("missing git_branch in metadata")
	}
	if !strings.Contains(result.Content, "cwd: /project") {
		t.Fatal("missing cwd in metadata")
	}
}

func TestExportSession_Markdown_WithToolCalls(t *testing.T) {
	dir := t.TempDir()

	rec := transcript.NewRecorder("export-tools", dir)
	msgs := []*schema.Message{
		{Role: schema.User, Content: "Read file.txt"},
		{Role: schema.Assistant, Content: "I'll read that file.", ToolCalls: []schema.ToolCall{
			{ID: "tc-1", Function: schema.FunctionCall{Name: "Read", Arguments: `{"path":"file.txt"}`}},
		}},
		{Role: schema.Tool, Content: "file contents here", ToolCallID: "tc-1"},
		{Role: schema.Assistant, Content: "The file contains: file contents here"},
	}
	if err := rec.Record(msgs, false); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	// Export without tool calls.
	result, err := ExportSession(ExportOptions{
		SessionID:        "export-tools",
		Dir:              dir,
		Format:           ExportMarkdown,
		IncludeToolCalls: false,
	})
	if err != nil {
		t.Fatal(err)
		return
	}
	// Should not include tool-only messages.
	if strings.Contains(result.Content, "Tool Result") {
		t.Fatal("tool results should not appear when IncludeToolCalls=false")
	}
	// The assistant message with only tool calls and no text should be excluded.
	if strings.Contains(result.Content, "Tool Call:") {
		t.Fatal("tool calls should not appear when IncludeToolCalls=false")
	}

	// Export with tool calls.
	resultWithTools, err := ExportSession(ExportOptions{
		SessionID:        "export-tools",
		Dir:              dir,
		Format:           ExportMarkdown,
		IncludeToolCalls: true,
	})
	if err != nil {
		t.Fatal(err)
		return
	}
	if !strings.Contains(resultWithTools.Content, "Tool Call:") {
		t.Fatal("tool calls should appear when IncludeToolCalls=true")
	}
	if !strings.Contains(resultWithTools.Content, "Tool Result") {
		t.Fatal("tool results should appear when IncludeToolCalls=true")
	}
}

func TestExportSession_JSON_Basic(t *testing.T) {
	dir := t.TempDir()

	rec := transcript.NewRecorder("export-json-test", dir)
	msgs := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "world"},
	}
	if err := rec.Record(msgs, false); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	result, err := ExportSession(ExportOptions{
		SessionID:       "export-json-test",
		Dir:             dir,
		Format:          ExportJSON,
		IncludeMetadata: true,
	})
	if err != nil {
		t.Fatal(err)
		return
	}

	if result.Format != ExportJSON {
		t.Fatal("expected JSON format")
	}

	// Verify valid JSON.
	var exported ExportedSession
	if err := json.Unmarshal([]byte(result.Content), &exported); err != nil {
		t.Fatalf("export is not valid JSON: %v", err)
		return
	}

	if exported.SessionID != "export-json-test" {
		t.Fatalf("session ID: %q", exported.SessionID)
	}
	if len(exported.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(exported.Messages))
	}
	if exported.Messages[0].Role != "user" || exported.Messages[0].Content != "hello" {
		t.Fatalf("unexpected first message: %+v", exported.Messages[0])
	}
	if exported.Messages[1].Role != "assistant" || exported.Messages[1].Content != "world" {
		t.Fatalf("unexpected second message: %+v", exported.Messages[1])
	}
	if exported.ExportedAt.IsZero() {
		t.Fatal("exported_at should be set")
	}
}

func TestExportSession_JSON_WithMetadata(t *testing.T) {
	dir := t.TempDir()

	rec := transcript.NewRecorder("export-json-meta", dir)
	if err := rec.Record([]*schema.Message{{Role: schema.User, Content: "test"}}, false); err != nil {
		t.Fatal(err)
		return
	}
	metaFull := &SessionMetadataFull{
		SessionID: "export-json-meta",
		Model:     "gpt-4",
		Provider:  "openai",
		GitBranch: "develop",
		CWD:       "/workspace",
	}
	if err := WriteSessionMetadata(rec, metaFull); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	result, err := ExportSession(ExportOptions{
		SessionID:       "export-json-meta",
		Dir:             dir,
		Format:          ExportJSON,
		IncludeMetadata: true,
	})
	if err != nil {
		t.Fatal(err)
		return
	}

	var exported ExportedSession
	if err := json.Unmarshal([]byte(result.Content), &exported); err != nil {
		t.Fatal(err)
		return
	}

	if exported.Metadata == nil {
		t.Fatal("metadata should be present")
		return
	}
	if exported.Metadata.Model != "gpt-4" {
		t.Fatalf("model: %q", exported.Metadata.Model)
	}
	if exported.Metadata.Provider != "openai" {
		t.Fatalf("provider: %q", exported.Metadata.Provider)
	}
	if exported.Metadata.GitBranch != "develop" {
		t.Fatalf("git branch: %q", exported.Metadata.GitBranch)
	}
}

func TestExportSession_NonExistent(t *testing.T) {
	dir := t.TempDir()
	_, err := ExportSession(ExportOptions{SessionID: "ghost", Dir: dir})
	if err == nil {
		t.Fatal("expected error for non-existent session")
		return
	}
}

func TestExportSession_EmptyID(t *testing.T) {
	dir := t.TempDir()
	_, err := ExportSession(ExportOptions{Dir: dir})
	if err == nil {
		t.Fatal("expected error for empty session ID")
		return
	}
}

func TestExportSession_JSON_WithToolCalls(t *testing.T) {
	dir := t.TempDir()

	rec := transcript.NewRecorder("export-json-tools", dir)
	msgs := []*schema.Message{
		{Role: schema.User, Content: "Read the file"},
		{Role: schema.Assistant, Content: "", ToolCalls: []schema.ToolCall{
			{ID: "call-1", Function: schema.FunctionCall{Name: "Read", Arguments: `{"path":"x.go"}`}},
		}},
		{Role: schema.Tool, Content: "package main", ToolCallID: "call-1"},
		{Role: schema.Assistant, Content: "Here is the file content."},
	}
	if err := rec.Record(msgs, false); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	// With tool calls included.
	result, err := ExportSession(ExportOptions{
		SessionID:        "export-json-tools",
		Dir:              dir,
		Format:           ExportJSON,
		IncludeToolCalls: true,
	})
	if err != nil {
		t.Fatal(err)
		return
	}

	var exported ExportedSession
	if err := json.Unmarshal([]byte(result.Content), &exported); err != nil {
		t.Fatal(err)
		return
	}
	if len(exported.Messages) != 4 {
		t.Fatalf("expected 4 messages with tools, got %d", len(exported.Messages))
	}
	// Check tool call structure.
	if len(exported.Messages[1].ToolCalls) != 1 {
		t.Fatal("expected tool call in second message")
	}
	if exported.Messages[1].ToolCalls[0].Name != "Read" {
		t.Fatalf("tool name: %q", exported.Messages[1].ToolCalls[0].Name)
	}
	if exported.Messages[2].Role != "tool" {
		t.Fatalf("third message role: %q", exported.Messages[2].Role)
	}
}
