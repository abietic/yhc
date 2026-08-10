package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
	enginemcp "github.com/abietic/yhc/engine/mcp"
	"github.com/abietic/yhc/engine/skills"
)

func TestFileMentionLoadsByElementIDAndBuildsContext(t *testing.T) {
	dir := t.TempDir()
	content := "package sample\n"
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{CWD: dir, TranscriptDir: t.TempDir()})
	t.Cleanup(eng.Close)
	app := New(Config{Engine: eng})
	app.mentionIndex = composerMentionIndex{loaded: true, files: []string{"main.go"}}
	app.textarea.SetValue("inspect @ma")
	app.textarea.CursorEnd()
	app.updateMentionHints()

	if len(app.mentionHints) != 1 || app.mentionHints[0].Kind != composerElementKindFile {
		t.Fatalf("file hints = %#v", app.mentionHints)
	}
	cmd := app.acceptMentionHint()
	if cmd == nil || app.textarea.Value() != "inspect @main.go " || len(app.composerElements) != 1 {
		t.Fatalf("accepted file mention: value=%q elements=%#v", app.textarea.Value(), app.composerElements)
	}
	if _, _, err := app.composerSubmissionPrompt(); err == nil || !strings.Contains(err.Error(), "still loading") {
		t.Fatalf("pending mention submission error = %v", err)
	}
	app.Update(cmd())
	if app.composerElements[0].Data != content {
		t.Fatalf("file payload = %q", app.composerElements[0].Data)
	}
	display, prompt, err := app.composerSubmissionPrompt()
	if err != nil || display != "inspect @main.go" || !strings.Contains(prompt, `"kind":"file"`) ||
		!strings.Contains(prompt, `"content":"package sample\n"`) {
		t.Fatalf("file submission: display=%q prompt=%q err=%v", display, prompt, err)
	}
}

func TestSkillMentionUsesLoadedRegistryContent(t *testing.T) {
	registry := skills.NewSkillRegistry()
	registry.Register(&skills.Skill{
		Name: "review", Description: "Review this change", Content: "Follow the review checklist.",
		FilePath: "/skills/review.md",
	})
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		CWD: t.TempDir(), TranscriptDir: t.TempDir(), SkillRegistry: registry,
	})
	t.Cleanup(eng.Close)
	app := New(Config{Engine: eng})
	app.mentionIndex.loaded = true
	app.textarea.SetValue("use @skill:rev")
	app.textarea.CursorEnd()
	app.updateMentionHints()

	if len(app.mentionHints) != 1 || app.mentionHints[0].Kind != composerElementKindSkill {
		t.Fatalf("skill hints = %#v", app.mentionHints)
	}
	if cmd := app.acceptMentionHint(); cmd != nil {
		t.Fatal("in-memory skill mention unexpectedly scheduled I/O")
	}
	if len(app.composerElements) != 1 || app.composerElements[0].Data != "Follow the review checklist." {
		t.Fatalf("skill element = %#v", app.composerElements)
	}
	_, prompt, err := app.composerSubmissionPrompt()
	if err != nil || !strings.Contains(prompt, `"kind":"skill"`) || !strings.Contains(prompt, "review checklist") {
		t.Fatalf("skill prompt = %q, err=%v", prompt, err)
	}
}

func TestMCPResourceMentionLoadsAsynchronously(t *testing.T) {
	app := New(Config{})
	app.mentionIndex = composerMentionIndex{
		loaded: true,
		resources: []enginemcp.MCPResource{{
			URI: "memo://guide", Name: "docs", ServerName: "knowledge", MimeType: "text/markdown",
		}},
		readMCP: func(_ context.Context, server, uri string) ([]enginemcp.ResourceContent, error) {
			if server != "knowledge" || uri != "memo://guide" {
				t.Fatalf("read resource = %s %s", server, uri)
			}
			return []enginemcp.ResourceContent{{URI: uri, MimeType: "text/markdown", Text: "resource body"}}, nil
		},
	}
	app.textarea.SetValue("read @mcp:doc")
	app.textarea.CursorEnd()
	app.updateMentionHints()

	if len(app.mentionHints) != 1 || app.mentionHints[0].Kind != composerElementKindMCPResource {
		t.Fatalf("MCP hints = %#v", app.mentionHints)
	}
	cmd := app.acceptMentionHint()
	if cmd == nil {
		t.Fatal("MCP mention did not schedule resource read")
	}
	app.Update(cmd())
	if len(app.composerElements) != 1 || app.composerElements[0].Data != "resource body" {
		t.Fatalf("MCP element = %#v", app.composerElements)
	}
	if persisted := expandComposerElementsForPersistence(app.textarea.Value(), app.composerElements); !strings.Contains(persisted, "@mcp:knowledge/docs") ||
		strings.Contains(persisted, "memo://guide") ||
		strings.Contains(persisted, "resource body") {
		t.Fatalf("MCP text history = %q", persisted)
	}
}

func TestMentionPayloadResultDoesNotResurrectEditedElement(t *testing.T) {
	app := New(Config{})
	app.mentionIndex = composerMentionIndex{loaded: true, files: []string{"main.go"}}
	app.textarea.SetValue("@ma")
	app.textarea.CursorEnd()
	app.updateMentionHints()
	cmd := app.acceptMentionHint()
	if cmd == nil {
		t.Fatal("file mention did not schedule loading")
	}

	app.textarea.SetCursorColumn(len([]rune("@main.go")))
	app.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if len(app.composerElements) != 0 {
		t.Fatalf("edited mention retained element: %#v", app.composerElements)
	}
	app.Update(cmd())
	if len(app.composerElements) != 0 {
		t.Fatal("late file result resurrected a deleted mention")
	}
}

func TestMentionFileIndexSkipsHeavyGeneratedDirectories(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"src/main.go", ".reference/codex/main.rs", ".git/config"} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(path), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	files, err := buildMentionFileIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != "src/main.go" {
		t.Fatalf("mention file index = %#v", files)
	}
}

func TestMentionIndexLoadsFilesAndMCPWithoutBlockingUpdate(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o600); err != nil {
		t.Fatal(err)
	}
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{CWD: dir, TranscriptDir: t.TempDir()})
	t.Cleanup(eng.Close)
	app := New(Config{Engine: eng})
	app.mentionIndex.listMCP = func(context.Context) ([]enginemcp.MCPResource, error) {
		return []enginemcp.MCPResource{{URI: "memo://one", Name: "one", ServerName: "memory"}}, nil
	}
	app.textarea.SetValue("@")
	app.textarea.CursorEnd()

	cmd := app.ensureMentionIndex()
	if cmd == nil || !app.mentionIndex.loading {
		t.Fatal("mention index did not enter asynchronous loading state")
	}
	app.Update(cmd())
	if !app.mentionIndex.loaded || app.mentionIndex.loading || len(app.mentionIndex.files) != 1 ||
		len(app.mentionIndex.resources) != 1 {
		t.Fatalf("mention index = %#v", app.mentionIndex)
	}
	if len(app.mentionHints) != 2 {
		t.Fatalf("combined mention hints = %#v", app.mentionHints)
	}
}
