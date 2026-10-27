package engine

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/filehistory"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/tools"
	"github.com/google/uuid"
)

func TestColdHistoryGuardOwnerInvalidatesCompletedRestorePaths(t *testing.T) {
	source, path, turn, _, _ := newRestoreEngineFixture(t, PermissionAllowOnce)
	preview, err := source.PrepareWorkspaceRestore(turn, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if result, err := source.ConfirmWorkspaceRestore(t.Context(), preview.ID, true); err != nil || result.State != "completed" {
		t.Fatal("source restore", err)
	}
	root, dir, id := source.GetCWD(), source.GetTranscriptDir(), source.SessionID()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	snapshot := tools.ReadGuardSnapshot{Version: 1, CWD: root, Entries: []tools.ReadGuardEntry{{Path: filepath.Base(path), SHA256: hex.EncodeToString(hash[:]), Size: int64(len(data))}}}
	guards, err := tools.RestoreFileReadGuards(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	fresh := NewSessionAdministrationEngine(SessionAdministrationConfig{CWD: root, TranscriptDir: dir})
	t.Cleanup(fresh.Close)
	// Only exercise the owner seam here; the actual public handoff also checks
	// source native state, import provenance and closed financial checkpoints.
	fresh.config.SessionID, fresh.config.ThreadID = id, id
	if err := fresh.bindColdHistoryGuards(guards); err != nil {
		t.Fatal(err)
	}
	actual, err := guards.Snapshot(root)
	if err != nil || len(actual.Entries) != 0 || len(snapshot.Entries) != 1 {
		t.Fatal("stale credit or caller snapshot changed", err)
	}
	ctx, notice, err := fresh.workspaceRestoreQueryContext(tools.WithFileReadGuards(t.Context(), guards))
	if err != nil || tools.BoundFileReadGuards(ctx) != guards || !strings.Contains(notice, "dirty.txt") {
		t.Fatalf("owner=%p notice=%q err=%v", tools.BoundFileReadGuards(ctx), notice, err)
	}
	if _, _, err := fresh.workspaceRestoreQueryContext(tools.WithFileReadGuards(t.Context(), tools.NewFileReadGuards())); err == nil {
		t.Fatal("foreign scope accepted")
	}
}

func TestColdHistoryPendingMarkerBlocksAbsentHistoryBeforeProvider(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".yhc", "transcripts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pending.workspace-history.cold-import.json"), []byte("incomplete"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := NewSessionAdministrationEngine(SessionAdministrationConfig{CWD: root, TranscriptDir: dir})
	t.Cleanup(e.Close)
	e.config.SessionID, e.config.ThreadID = "pending", "pending"
	if got := e.recoveryProofWorkspaceHistoryBlocker(); got != "workspace_history_migration_incomplete" {
		t.Fatalf("marker ignored: %s", got)
	}
	if err := e.workspaceRestorePendingActionError(); err == nil {
		t.Fatal("command gate ignored pending marker")
	}
	if _, _, err := e.workspaceRestoreQueryContext(t.Context()); err == nil {
		t.Fatal("query gate ignored pending marker")
	}
}

func TestColdHistorySessionImportRejectsLogicalRootRemappingBeforeResume(t *testing.T) {
	source, _, _, _, _ := newRestoreEngineFixture(t, PermissionAllowOnce)
	root, dir, id := source.GetCWD(), source.GetTranscriptDir(), source.SessionID()
	source.Close()
	j, err := filehistory.OpenRevisionJournal(root, filepath.Join(dir, id+".workspace-history"), id)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := j.ExportColdState()
	j.Close()
	if err != nil {
		t.Fatal(err)
	}
	target, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fresh := NewSessionAdministrationEngine(SessionAdministrationConfig{CWD: target, TranscriptDir: filepath.Join(target, ".yhc", "transcripts")})
	t.Cleanup(fresh.Close)
	before := fresh.SessionID()
	request := filehistory.ColdHistoryImport{Version: 1, Source: proof, SourceSHA256: proof.SHA256, MigrationID: uuid.NewString(), BindingSHA256: strings.Repeat("a", 64)}
	_, err = fresh.SessionService().ImportWorkspaceHistory(t.Context(), session.SessionInfo{SessionID: id, CWD: target}, request)
	if err == nil || !strings.Contains(err.Error(), "logical root") || fresh.SessionID() != before {
		t.Fatal("Session remapping reached canonical resume or history import", err)
	}
	if names, err := os.ReadDir(target); err != nil || len(names) != 0 {
		t.Fatal("denial mutated the target", err)
	}
}

func TestColdHistoryChangedImportedStateBlocksQueryAndCommand(t *testing.T) {
	for _, scenario := range []struct{ cached, restore bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		t.Run(fmt.Sprintf("cached=%t/restore=%t", scenario.cached, scenario.restore), func(t *testing.T) {
			source, _, turn, _, _ := newRestoreEngineFixture(t, PermissionAllowOnce)
			if scenario.restore {
				preview, err := source.PrepareWorkspaceRestore(turn, "", "")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := source.ConfirmWorkspaceRestore(t.Context(), preview.ID, true); err != nil {
					t.Fatal(err)
				}
			}
			root, dir, id := source.GetCWD(), source.GetTranscriptDir(), source.SessionID()
			source.Close()
			// The shared UI fixture intentionally keeps transcripts outside CWD;
			// this cold protocol requires all of its state inside the owned volume.
			raw, err := os.ReadFile(filepath.Join(dir, id+".workspace-history", "revisions.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			dir = filepath.Join(root, ".yhc", "transcripts")
			historyDir := filepath.Join(dir, id+".workspace-history")
			if err := os.MkdirAll(historyDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(historyDir, "revisions.jsonl"), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if scenario.restore {
				data, err := os.ReadFile(filepath.Join(source.GetTranscriptDir(), id+".workspace-history", "restore.json"))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(historyDir, "restore.json"), data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			j, err := filehistory.OpenRevisionJournal(root, historyDir, id)
			if err != nil {
				t.Fatal(err)
			}
			proof, err := j.ExportColdState()
			j.Close()
			if err != nil {
				t.Fatal(err)
			}
			_, err = filehistory.ImportColdState(t.Context(), root, historyDir, id, filehistory.ColdHistoryImport{
				Version: 1, Source: proof, SourceSHA256: proof.SHA256, MigrationID: uuid.NewString(), BindingSHA256: strings.Repeat("a", 64),
			})
			if err != nil {
				t.Fatal(err)
			}
			fresh := NewSessionAdministrationEngine(SessionAdministrationConfig{CWD: root, TranscriptDir: dir})
			t.Cleanup(fresh.Close)
			fresh.config.SessionID, fresh.config.ThreadID = id, id
			if scenario.cached {
				if err := fresh.workspaceRestorePendingActionError(); err != nil {
					t.Fatal(err)
				}
			}
			name := filepath.Join(historyDir, "revisions.jsonl")
			if scenario.restore {
				name = filepath.Join(historyDir, "restore.json")
			}
			raw, err = os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			changed := bytes.ReplaceAll(raw, []byte(`"ToolName":"Write"`), []byte(`"ToolName":"Edits"`))
			if scenario.restore {
				var envelope map[string]any
				if err := json.Unmarshal(raw, &envelope); err != nil {
					t.Fatal(err)
				}
				op := envelope["operation"].(map[string]any)
				op["ConfirmationID"] = strings.Repeat("x", len(op["ConfirmationID"].(string)))
				changed, err = json.Marshal(envelope)
				if err != nil {
					t.Fatal(err)
				}
			}
			if bytes.Equal(raw, changed) {
				t.Fatal("fixture did not change a valid logical record")
			}
			if err := os.WriteFile(name, changed, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := fresh.workspaceRestorePendingActionError(); err == nil {
				t.Fatal("command admitted changed imported prefix")
			}
			if _, _, err := fresh.workspaceRestoreQueryContext(t.Context()); err == nil {
				t.Fatal("query admitted changed imported prefix")
			}
			if got, err := os.ReadFile(name); err != nil || !bytes.Equal(got, changed) {
				t.Fatal("rejection rewrote the original evidence", err)
			}
		})
	}
}
