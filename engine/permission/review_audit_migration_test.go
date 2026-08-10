package permission

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/abietic/yhc/internal/identity"
	"github.com/abietic/yhc/internal/statemigration"
	"github.com/abietic/yhc/internal/statepath"
)

func TestReviewAuditMigrationRequiresQuiescence(t *testing.T) {
	roots, legacyDir := reviewAuditMigrationFixture(t)
	store, err := NewReviewAuditStore(ReviewAuditStoreOptions{
		Dir:            legacyDir,
		Now:            reviewAuditTestClock,
		LockTimeout:    50 * time.Millisecond,
		StaleLockAfter: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Record(t.Context(), validReviewAuditRecord(ReviewAuditKindEligible)); err != nil {
		t.Fatal(err)
	}
	spec, err := ReviewAuditMigrationSpec(roots)
	if err != nil {
		t.Fatal(err)
	}

	locked := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- store.withLock(context.Background(), func(*os.Root) error {
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked

	result, err := (statemigration.Importer{}).Inspect(t.Context(), roots, spec)
	if err != nil || result.Status != statemigration.StatusLegacyBusy {
		t.Fatalf("Inspect() = %#v, %v, want legacy_busy", result, err)
	}
	result, err = (statemigration.Importer{}).Import(t.Context(), roots, spec)
	if err != nil || result.Status != statemigration.StatusLegacyBusy {
		t.Fatalf("Import() = %#v, %v, want legacy_busy", result, err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	result, err = (statemigration.Importer{}).Import(t.Context(), roots, spec)
	if err != nil || result.Status != statemigration.StatusImported {
		t.Fatalf("quiescent Import() = %#v, %v", result, err)
	}
}

func TestReviewAuditMigrationPreservesRedactionRotationAndRecovery(t *testing.T) {
	roots, legacyDir := reviewAuditMigrationFixture(t)
	store, err := NewReviewAuditStore(ReviewAuditStoreOptions{
		Dir:             legacyDir,
		SegmentMaxBytes: 350,
		MaxSegments:     4,
		Now:             reviewAuditTestClock,
		LockTimeout:     100 * time.Millisecond,
		StaleLockAfter:  time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	for index := range 8 {
		record := validReviewAuditRecord(ReviewAuditKindEligible)
		record.EventID = reviewAuditEventIDForIndex(index)
		if err := store.Record(t.Context(), record); err != nil {
			t.Fatal(err)
		}
	}
	active := filepath.Join(legacyDir, reviewAuditActiveSegment)
	file, err := os.OpenFile(active, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"schema_version":1`); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	legacyBefore := readReviewAuditMigrationSegments(t, legacyDir)
	if len(legacyBefore) < 2 {
		t.Fatalf("rotation fixture has %d segments, want at least 2", len(legacyBefore))
	}

	spec, err := ReviewAuditMigrationSpec(roots)
	if err != nil {
		t.Fatal(err)
	}
	result, err := (statemigration.Importer{}).Import(t.Context(), roots, spec)
	if err != nil || result.Status != statemigration.StatusImported {
		t.Fatalf("Import() = %#v, %v", result, err)
	}
	if got := readReviewAuditMigrationSegments(t, legacyDir); !equalReviewAuditSegments(got, legacyBefore) {
		t.Fatal("migration changed legacy audit segments")
	}
	canonicalDir := filepath.Join(roots.Canonical, "permission-review-audit", "v1")
	if got := readReviewAuditMigrationSegments(t, canonicalDir); !equalReviewAuditSegments(got, legacyBefore) {
		t.Fatal("canonical audit did not preserve the bounded segment window")
	}
	for _, name := range []string{reviewAuditLockName, reviewAuditLockGuardName} {
		if _, err := os.Lstat(filepath.Join(canonicalDir, name)); !os.IsNotExist(err) {
			t.Fatalf("canonical migration copied coordination file %q: %v", name, err)
		}
	}

	canonical, err := NewReviewAuditStore(ReviewAuditStoreOptions{
		Dir:             canonicalDir,
		SegmentMaxBytes: 350,
		MaxSegments:     4,
		Now:             reviewAuditTestClock,
		LockTimeout:     100 * time.Millisecond,
		StaleLockAfter:  time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := canonical.Load()
	if err != nil || loaded.PartialTailRecords != 1 {
		t.Fatalf("Load() partial tails=%d err=%v, want one", loaded.PartialTailRecords, err)
	}
	record := validReviewAuditRecord(ReviewAuditKindEligible)
	record.EventID = reviewAuditEventIDForIndex(99)
	if err := canonical.Record(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	loaded, err = canonical.Load()
	if err != nil || loaded.TailRepairs != 1 || loaded.PartialTailRecords != 0 {
		t.Fatalf("recovered Load() = %#v, %v", loaded, err)
	}
}

func TestReviewAuditMigrationRefusesExactOverride(t *testing.T) {
	home := t.TempDir()
	roots, err := statepath.UserRoots(home)
	if err != nil {
		t.Fatal(err)
	}
	pair := identity.RuntimeEnvPermissionReviewAuditDir.Pair()
	t.Setenv(pair.Canonical, filepath.Join(t.TempDir(), "exact-audit"))
	t.Setenv(pair.Legacy, filepath.Join(t.TempDir(), "ignored-legacy-audit"))
	if _, err := ReviewAuditMigrationSpec(roots); !errors.Is(err, ErrReviewAuditMigrationUnavailable) {
		t.Fatalf("ReviewAuditMigrationSpec() error = %v, want unavailable", err)
	}
}

func reviewAuditMigrationFixture(t *testing.T) (statepath.Roots, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	pair := identity.RuntimeEnvPermissionReviewAuditDir.Pair()
	t.Setenv(pair.Canonical, "")
	t.Setenv(pair.Legacy, "")
	roots, err := statepath.UserRoots(home)
	if err != nil {
		t.Fatal(err)
	}
	return roots, filepath.Join(roots.Legacy, "permission-review-audit", "v1")
}

func reviewAuditEventIDForIndex(index int) string {
	const digits = "0123456789abcdef"
	encoded := make([]byte, 32)
	for position := range encoded {
		encoded[position] = digits[(index+position)%len(digits)]
	}
	return string(encoded)
}

func readReviewAuditMigrationSegments(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	segments := make(map[string][]byte)
	for _, entry := range entries {
		if !isReviewAuditSegmentName(entry.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		segments[entry.Name()] = data
	}
	return segments
}

func equalReviewAuditSegments(left, right map[string][]byte) bool {
	if len(left) != len(right) {
		return false
	}
	for name, leftData := range left {
		if !bytes.Equal(leftData, right[name]) {
			return false
		}
	}
	return true
}
