package cron

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCronLegacyInspectIsReadOnlyAndValueFree(t *testing.T) {
	project := t.TempDir()
	privatePrompt := "legacy-private-prompt-sentinel"
	legacy := writeLegacyCronFixture(t, project, cronFixture(privatePrompt))
	before := captureCronFileState(t, legacy)

	inspection, err := InspectLegacy(t.Context(), project)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Status != MigrationReady || inspection.TaskCount != 1 {
		t.Fatalf("inspection=%#v", inspection)
	}
	diagnostic, err := json.Marshal(inspection)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(diagnostic), privatePrompt) || strings.Contains(string(diagnostic), project) {
		t.Fatalf("inspection leaked a private value: %s", diagnostic)
	}
	assertCronFileState(t, legacy, before)
	if _, err := os.Lstat(filepath.Join(project, ".yhc")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspect created canonical state: %v", err)
	}
}

func TestCronAutomaticImportAlwaysRefuses(t *testing.T) {
	project := t.TempDir()
	legacy := writeLegacyCronFixture(t, project, cronFixture("do not import"))
	before := captureCronFileState(t, legacy)

	_, err := ImportLegacy(t.Context(), ImportRequest{ProjectDir: project})
	if !errors.Is(err, ErrLegacyStoppedAttestationRequired) {
		t.Fatalf("unconfirmed import error=%v", err)
	}
	assertCronFileState(t, legacy, before)
	if _, err := os.Lstat(filepath.Join(project, ".yhc")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unconfirmed import created canonical state: %v", err)
	}
}

func TestCronExplicitImportRequiresStoppedAttestationDeadPIDAndStableSnapshot(t *testing.T) {
	project := t.TempDir()
	legacy := writeLegacyCronFixture(t, project, cronFixture("stable import"))
	legacyRoot := filepath.Dir(legacy)
	lockPath := filepath.Join(legacyRoot, "scheduler.lock")
	if err := os.WriteFile(lockPath, []byte(fmt.Sprintf("%d %d", os.Getpid(), time.Now().UnixMilli())), 0o644); err != nil {
		t.Fatal(err)
	}
	before := captureCronFileState(t, legacy)

	inspection, err := ImportLegacy(t.Context(), ImportRequest{
		ProjectDir: project, ConfirmLegacyStopped: true,
	})
	if err != nil || inspection.Status != MigrationLegacyBusy {
		t.Fatalf("live import=%#v err=%v", inspection, err)
	}
	assertCronFileState(t, legacy, before)
	if _, err := os.Lstat(filepath.Join(project, ".yhc")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("live scheduler import created canonical state: %v", err)
	}

	originalAlive := migrationProcessAlive
	originalWait := migrationWaitForStability
	migrationProcessAlive = func(int) bool { return false }
	waits := 0
	migrationWaitForStability = func(context.Context, time.Duration) error {
		waits++
		return nil
	}
	t.Cleanup(func() {
		migrationProcessAlive = originalAlive
		migrationWaitForStability = originalWait
	})
	if err := os.WriteFile(lockPath, []byte("999999 1"), 0o644); err != nil {
		t.Fatal(err)
	}
	lockBefore := captureCronFileState(t, lockPath)
	inspection, err = ImportLegacy(t.Context(), ImportRequest{
		ProjectDir: project, ConfirmLegacyStopped: true,
	})
	if err != nil || inspection.Status != MigrationImported || inspection.TaskCount != 1 {
		t.Fatalf("stopped import=%#v err=%v", inspection, err)
	}
	if waits != 1 {
		t.Fatalf("stability waits=%d, want 1", waits)
	}
	canonical, err := os.ReadFile(GetCronFilePath(project))
	if err != nil || !bytes.Equal(canonical, before.data) {
		t.Fatalf("canonical cron mismatch: %v", err)
	}
	assertCronFileState(t, legacy, before)
	assertCronFileState(t, lockPath, lockBefore)
}

func TestCronMigrationRejectsMalformedCollisionAndReplacement(t *testing.T) {
	t.Run("malformed", func(t *testing.T) {
		project := t.TempDir()
		legacy := writeLegacyCronFixture(t, project, []byte(`{"tasks":[{"id":"bad","cron":"not cron"}]}`))
		before := captureCronFileState(t, legacy)
		if _, err := InspectLegacy(t.Context(), project); !errors.Is(err, ErrCronMigrationUnsafe) {
			t.Fatalf("malformed inspect error=%v", err)
		}
		if _, err := ImportLegacy(t.Context(), ImportRequest{ProjectDir: project, ConfirmLegacyStopped: true}); !errors.Is(err, ErrCronMigrationUnsafe) {
			t.Fatalf("malformed import error=%v", err)
		}
		assertCronFileState(t, legacy, before)
	})

	t.Run("collision", func(t *testing.T) {
		project := t.TempDir()
		legacy := writeLegacyCronFixture(t, project, cronFixture("legacy"))
		beforeLegacy := captureCronFileState(t, legacy)
		canonical := filepath.Join(project, ".yhc", "scheduled_tasks.json")
		if err := os.MkdirAll(filepath.Dir(canonical), 0o700); err != nil {
			t.Fatal(err)
		}
		collision := cronFixture("canonical collision")
		if err := os.WriteFile(canonical, collision, 0o600); err != nil {
			t.Fatal(err)
		}
		inspection, err := ImportLegacy(t.Context(), ImportRequest{
			ProjectDir: project, ConfirmLegacyStopped: true,
		})
		if err != nil || inspection.Status != MigrationDestinationExists {
			t.Fatalf("collision import=%#v err=%v", inspection, err)
		}
		got, err := os.ReadFile(canonical)
		if err != nil || !bytes.Equal(got, collision) {
			t.Fatalf("collision target changed: %v", err)
		}
		assertCronFileState(t, legacy, beforeLegacy)
	})

	t.Run("replacement during stability interval", func(t *testing.T) {
		project := t.TempDir()
		legacy := writeLegacyCronFixture(t, project, cronFixture("before"))
		before := captureCronFileState(t, legacy)
		originalWait := migrationWaitForStability
		migrationWaitForStability = func(context.Context, time.Duration) error {
			return os.WriteFile(legacy, cronFixture("replacement"), 0o644)
		}
		t.Cleanup(func() { migrationWaitForStability = originalWait })
		if _, err := ImportLegacy(t.Context(), ImportRequest{
			ProjectDir: project, ConfirmLegacyStopped: true,
		}); !errors.Is(err, ErrCronMigrationUnsafe) {
			t.Fatalf("replacement import error=%v", err)
		}
		if _, err := os.Lstat(filepath.Join(project, ".yhc")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("replacement import created canonical state: %v", err)
		}
		if after := captureCronFileState(t, legacy); bytes.Equal(after.data, before.data) {
			t.Fatal("replacement hook did not alter the legacy oracle")
		}
	})
}

func TestCronMigrationLeavesLegacyBytesUnchanged(t *testing.T) {
	project := t.TempDir()
	legacy := writeLegacyCronFixture(t, project, cronFixture("immutable legacy"))
	before := captureCronFileState(t, legacy)
	originalWait := migrationWaitForStability
	migrationWaitForStability = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { migrationWaitForStability = originalWait })
	inspection, err := ImportLegacy(t.Context(), ImportRequest{
		ProjectDir: project, ConfirmLegacyStopped: true,
	})
	if err != nil || inspection.Status != MigrationImported {
		t.Fatalf("import=%#v err=%v", inspection, err)
	}
	assertCronFileState(t, legacy, before)
}

type cronFileState struct {
	data    []byte
	mode    os.FileMode
	modTime time.Time
}

func cronFixture(prompt string) []byte {
	return []byte(fmt.Sprintf(`{"tasks":[{"id":"task-1","cron":"*/5 * * * *","prompt":%q,"createdAt":1,"recurring":true}]}`, prompt))
}

func writeLegacyCronFixture(t *testing.T, project string, data []byte) string {
	t.Helper()
	root := filepath.Join(project, ".eino-agent")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "scheduled_tasks.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	return path
}

func captureCronFileState(t *testing.T, path string) cronFileState {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return cronFileState{data: data, mode: info.Mode(), modTime: info.ModTime()}
}

func assertCronFileState(t *testing.T, path string, want cronFileState) {
	t.Helper()
	got := captureCronFileState(t, path)
	if !bytes.Equal(got.data, want.data) || got.mode != want.mode || !got.modTime.Equal(want.modTime) {
		t.Fatalf("legacy file changed: data=%t mode=%v/%v mtime=%v/%v", bytes.Equal(got.data, want.data), got.mode, want.mode, got.modTime, want.modTime)
	}
}
