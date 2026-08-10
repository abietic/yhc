package engine

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/session"
)

func TestSessionAdministrationEngineIsProviderFreeAndCreatesNoSyntheticTranscript(t *testing.T) {
	root := t.TempDir()
	transcriptDir := filepath.Join(root, "transcripts")
	catalogPath := filepath.Join(root, "catalog.json")
	eng := NewSessionAdministrationEngine(SessionAdministrationConfig{
		CWD:                root,
		TranscriptDir:      transcriptDir,
		SessionCatalogPath: catalogPath,
	})
	syntheticPath := filepath.Join(transcriptDir, eng.SessionID()+".jsonl")
	if eng.config.ChatModel != nil || eng.config.CommandEntrypoint != commands.EntrypointAdministration {
		t.Fatalf("administration engine config = %#v", eng.config)
	}
	if eng.settingsWatcher != nil || eng.skillRegistry != nil || eng.backgroundServices != nil {
		t.Fatal("administration engine initialized interactive/runtime services")
	}
	if eng.queryKernelSelection.kernel != nil || eng.queryKernelSelection.err != nil ||
		eng.queryKernelSelection.version != queryKernelVersionProjectGraph ||
		eng.queryKernelSelection.stage != queryKernelStageFull {
		t.Fatalf("administration query kernel selection = %#v", eng.queryKernelSelection)
	}
	if _, err := eng.SessionService().Query(t.Context(), session.SessionQuery{
		Scope: session.SessionScopeCWD,
		Limit: 10,
	}); err != nil {
		t.Fatal(err)
	}
	eng.Close()
	if _, err := os.Stat(syntheticPath); !os.IsNotExist(err) {
		t.Fatalf("synthetic transcript exists after read-only close: %v", err)
	}
}

func TestSessionAdministrationResumeUsesDurableIdentityWithoutSyntheticSource(t *testing.T) {
	root := t.TempDir()
	transcriptDir := filepath.Join(root, "transcripts")
	catalogPath := filepath.Join(root, "catalog.json")
	writeServiceSession(t, transcriptDir, "saved-session", "resume me")
	if err := os.MkdirAll(filepath.Join(root, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".claude", "hooks.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(transcriptDir, "saved-session.jsonl")
	beforeResume, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}

	eng := NewSessionAdministrationEngine(SessionAdministrationConfig{
		CWD:                root,
		TranscriptDir:      transcriptDir,
		SessionCatalogPath: catalogPath,
	})
	syntheticPath := filepath.Join(transcriptDir, eng.SessionID()+".jsonl")
	initialMCP := eng.mcpManager
	resumed, err := eng.SessionService().Resume(t.Context(), "saved-session")
	if err != nil {
		t.Fatal(err)
	}
	if resumed.SessionID != "saved-session" || eng.SessionID() != "saved-session" || len(resumed.Messages) != 2 {
		t.Fatalf("resumed = %#v, active=%q", resumed, eng.SessionID())
	}
	if eng.settingsWatcher != nil || eng.skillRegistry != nil ||
		eng.mcpManager != initialMCP || eng.backgroundServices != nil {
		t.Fatal("administration resume activated project runtime dependencies")
	}
	afterResume, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(beforeResume, afterResume) {
		t.Fatal("administration resume did not preserve the canonical target-session checkpoint")
	}
	eng.Close()
	if _, err := os.Stat(syntheticPath); !os.IsNotExist(err) {
		t.Fatalf("synthetic source transcript exists after resume: %v", err)
	}
	if _, err := os.Stat(sourcePath); err != nil {
		t.Fatalf("resumed transcript missing: %v", err)
	}
	afterClose, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterResume, afterClose) {
		t.Fatal("administration close appended a second target-session checkpoint")
	}
}
