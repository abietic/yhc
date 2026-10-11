package config

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSettingsDocumentRevisionTransactionPreservesOpaqueValues(t *testing.T) {
	project := workbenchSettingsFixture(t, "", `{"max_turns":4,"opaque":9007199254740993,"permissions":{"allow":["Read"]}}`)
	path := ProjectConfigPath(project)
	fields, revision, err := ReadSettingsDocument(path)
	if err != nil || string(fields["opaque"]) != "9007199254740993" {
		t.Fatalf("read failed: %v", err)
	}
	updated, err := UpdateSettingsDocumentRevision(path, revision, func(fields map[string]json.RawMessage) (bool, error) {
		fields["permissions"] = json.RawMessage(`{"allow":["Read","Edit"]}`)
		return true, nil
	})
	if err != nil || updated == revision {
		t.Fatalf("save failed: %v", err)
	}
	fields, actual, err := ReadSettingsDocument(path)
	if err != nil || actual != updated || string(fields["opaque"]) != "9007199254740993" || string(fields["max_turns"]) != "4" {
		t.Fatal("transaction lost unrelated values or revision")
	}
	called := false
	_, err = UpdateSettingsDocumentRevision(path, revision, func(map[string]json.RawMessage) (bool, error) { called = true; return true, nil })
	if !errors.Is(err, ErrSettingsConflict) || called {
		t.Fatal("stale revision entered mutation")
	}
}

func TestSettingsDocumentCancellationWhileWaitingForSharedLock(t *testing.T) {
	project := workbenchSettingsFixture(t, "", `{"opaque":9007199254740993}`)
	path := ProjectConfigPath(project)
	_, revision, err := ReadSettingsDocument(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := openWorkbenchSettingsStore(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	if err := store.lock(); err != nil {
		t.Fatal(err)
	}
	defer store.unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	called := false
	_, err = UpdateSettingsDocumentRevisionContext(ctx, path, revision, func(map[string]json.RawMessage) (bool, error) { called = true; return true, nil })
	if !errors.Is(err, context.DeadlineExceeded) || called {
		t.Fatalf("cancelled wait=%v callback=%v", err, called)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != `{"opaque":9007199254740993}` {
		t.Fatal("cancelled wait changed document")
	}
}

func TestSettingsDocumentCancellationBeforeCommit(t *testing.T) {
	project := workbenchSettingsFixture(t, "", `{"max_turns":4}`)
	path := ProjectConfigPath(project)
	_, revision, err := ReadSettingsDocument(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err = UpdateSettingsDocumentRevisionContext(ctx, path, revision, func(fields map[string]json.RawMessage) (bool, error) {
		fields["max_turns"] = json.RawMessage("8")
		cancel()
		return true, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("commit after cancellation=%v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != `{"max_turns":4}` {
		t.Fatal("cancelled callback changed document")
	}
}

func TestSettingsDocumentInspectionDoesNotCreateMissingFiles(t *testing.T) {
	project := t.TempDir()
	fields, revision, err := ReadSettingsDocument(ProjectLocalConfigPath(project))
	if err != nil || len(fields) != 0 || len(revision) != 64 {
		t.Fatalf("missing read=%v", err)
	}
	if _, err := os.Stat(ProjectLocalConfigPath(project)); !os.IsNotExist(err) {
		t.Fatal("inspection created settings")
	}
}

func TestSettingsDocumentSizeIncludesFinalNewline(t *testing.T) {
	for _, writer := range []string{"revision", "workbench"} {
		for _, extra := range []int{0, 1} {
			name := writer + "/at_limit"
			if extra != 0 {
				name = writer + "/over_limit"
			}
			t.Run(name, func(t *testing.T) {
				fields := map[string]json.RawMessage{"opaque": json.RawMessage(`""`), "theme": json.RawMessage(`"dark"`)}
				minimal, err := json.MarshalIndent(fields, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				padding := strings.Repeat("x", settingsDocumentLimit-1-len(minimal)+extra)
				fields["opaque"], err = json.Marshal(padding)
				if err != nil {
					t.Fatal(err)
				}
				fields["theme"] = json.RawMessage(`"light"`)
				original, err := json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
				project := workbenchSettingsFixture(t, "", string(original))
				path := ProjectConfigPath(project)
				_, revision, err := ReadSettingsDocument(path)
				if err != nil {
					t.Fatal(err)
				}
				if writer == "revision" {
					_, err = UpdateSettingsDocumentRevision(path, revision, func(fields map[string]json.RawMessage) (bool, error) {
						fields["theme"] = json.RawMessage(`"dark"`)
						return true, nil
					})
				} else {
					_, err = SettingsPatch(project, "project", revision, map[string]json.RawMessage{"theme": json.RawMessage(`"dark"`)})
				}
				data, readErr := os.ReadFile(path)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if extra != 0 {
					if !errors.Is(err, ErrSettingsInvalid) || string(data) != string(original) {
						t.Fatal("oversized write was not rejected without changing the document")
					}
					return
				}
				if err != nil || len(data) != settingsDocumentLimit || data[len(data)-1] != '\n' {
					t.Fatalf("exact-limit write size=%d err=%v", len(data), err)
				}
				if _, _, err := ReadSettingsDocument(path); err != nil {
					t.Fatalf("writer produced an unreadable document: %v", err)
				}
			})
		}
	}
}
