package permission

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abietic/yhc/internal/identity"
	"github.com/abietic/yhc/internal/statepath"
)

var reviewAuditTestClock = func() time.Time {
	return time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
}

const reviewAuditTestEventID = "0123456789abcdef0123456789abcdef"

func newReviewAuditTestStore(t *testing.T, mutate func(*ReviewAuditStoreOptions)) *ReviewAuditStore {
	t.Helper()
	opts := ReviewAuditStoreOptions{
		Dir:            filepath.Join(t.TempDir(), "audit"),
		Now:            reviewAuditTestClock,
		LockTimeout:    500 * time.Millisecond,
		StaleLockAfter: 30 * time.Second,
	}
	if mutate != nil {
		mutate(&opts)
	}
	store, err := NewReviewAuditStore(opts)
	if err != nil {
		t.Fatalf("NewReviewAuditStore: %v", err)
	}
	return store
}

// validReviewAuditRecord returns a fully valid record of the given kind.
func validReviewAuditRecord(kind ReviewAuditKind) ReviewAuditRecord {
	record := ReviewAuditRecord{
		SchemaVersion: ReviewAuditSchemaVersion,
		EventID:       reviewAuditTestEventID,
		OccurredAt:    reviewAuditTestClock(),
		Kind:          kind,
	}
	switch kind {
	case ReviewAuditKindEligible:
		record.CanonicalTool = "Bash"
		record.ActionKind = "filesystem_read"
		record.DeterministicClass = "review"
	case ReviewAuditKindAttempt:
		record.Provider = "openai"
		record.Model = "openai/gpt-5.2@2026-07-01"
		record.DataBoundary = PermissionReviewDataBoundary
	case ReviewAuditKindTerminal:
		record.ReviewerStatus = "completed"
		record.ReviewerDecision = ReviewDecisionApprove
		record.ReasonCode = ReviewReasonExpectedSafe
		record.LatencyMS = 12
	case ReviewAuditKindComparison:
		record.ComparisonSource = "legacy_classifier"
		record.ExpectedDecision = "allow"
	case ReviewAuditKindCorpusGroundTruth:
		record.ComparisonSource = "versioned_corpus"
		record.CorpusID = "corpus_v1"
		record.CorpusCaseID = "case_0007"
		record.ExpectedDecision = "deny"
	case ReviewAuditKindStorageRecovery:
		record.RecoveredBytes = 3
	case ReviewAuditKindDispatcherDiagnostic:
		record.DispatcherDiagnostic = ReviewAuditDiagnosticEnqueueDrop
		record.DiagnosticCount = 1
	}
	return record
}

func TestDefaultReviewAuditDirPrefersYHCOverride(t *testing.T) {
	pair := identity.RuntimeEnvPermissionReviewAuditDir.Pair()
	canonical := filepath.Join(t.TempDir(), "canonical-audit")
	legacy := filepath.Join(t.TempDir(), "legacy-audit")
	invalidCanonical := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(invalidCanonical, []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name           string
		canonical      *string
		legacy         *string
		want           string
		wantStoreError bool
	}{
		{name: "canonical only", canonical: permissionEnvironmentValue(canonical), want: canonical},
		{name: "legacy only", legacy: permissionEnvironmentValue(legacy), want: legacy},
		{name: "both prefer canonical", canonical: permissionEnvironmentValue(canonical), legacy: permissionEnvironmentValue(legacy), want: canonical},
		{name: "present empty canonical blocks legacy", canonical: permissionEnvironmentValue(""), legacy: permissionEnvironmentValue(legacy)},
		{name: "invalid canonical blocks legacy", canonical: permissionEnvironmentValue(invalidCanonical), legacy: permissionEnvironmentValue(legacy), want: invalidCanonical, wantStoreError: true},
		{name: "neither"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			setPermissionEnvironment(t, pair.Canonical, test.canonical)
			setPermissionEnvironment(t, pair.Legacy, test.legacy)
			want := test.want
			if want == "" {
				roots, err := statepath.UserRoots(home)
				if err != nil {
					t.Fatal(err)
				}
				want = filepath.Join(roots.Canonical, "permission-review-audit", "v1")
			}
			dir, err := DefaultReviewAuditDir()
			if err != nil {
				t.Fatalf("DefaultReviewAuditDir: %v", err)
			}
			if dir != want {
				t.Fatalf("dir = %q, want %q", dir, want)
			}
			if test.wantStoreError {
				if _, err := NewReviewAuditStore(ReviewAuditStoreOptions{}); err == nil {
					t.Fatal("invalid canonical directory fell through to the valid legacy directory")
				}
			}
		})
	}

	t.Run("empty dir option selects env dir", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "env-audit")
		setPermissionEnvironment(t, pair.Canonical, permissionEnvironmentValue(dir))
		setPermissionEnvironment(t, pair.Legacy, nil)
		store, err := NewReviewAuditStore(ReviewAuditStoreOptions{})
		if err != nil {
			t.Fatalf("NewReviewAuditStore: %v", err)
		}
		if store.dir != dir {
			t.Fatalf("store.dir = %q, want %q", store.dir, dir)
		}
	})
}

func TestDefaultReviewAuditStoreRejectsSymlinkCanonicalRoot(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	t.Setenv("HOME", home)
	pair := identity.RuntimeEnvPermissionReviewAuditDir.Pair()
	t.Setenv(pair.Canonical, "")
	t.Setenv(pair.Legacy, "")
	if err := os.Symlink(outside, filepath.Join(home, identity.ProjectDirName)); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := NewReviewAuditStore(ReviewAuditStoreOptions{}); err == nil {
		t.Fatal("default audit store accepted a symlink canonical root")
	}
	if _, err := os.Lstat(filepath.Join(outside, "permission-review-audit")); !os.IsNotExist(err) {
		t.Fatalf("audit store escaped canonical root: %v", err)
	}
}

func TestDefaultReviewAuditStoreRejectsCanonicalRootReplacement(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	t.Setenv("HOME", home)
	pair := identity.RuntimeEnvPermissionReviewAuditDir.Pair()
	t.Setenv(pair.Canonical, "")
	t.Setenv(pair.Legacy, "")
	store, err := NewReviewAuditStore(ReviewAuditStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	roots, err := statepath.UserRoots(home)
	if err != nil {
		t.Fatal(err)
	}
	detached := roots.Canonical + "-detached"
	if err := os.Rename(roots.Canonical, detached); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, roots.Canonical); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := store.Record(t.Context(), validReviewAuditRecord(ReviewAuditKindEligible)); err == nil {
		t.Fatal("audit record accepted a replaced canonical root")
	}
	if _, err := os.Lstat(filepath.Join(outside, "permission-review-audit")); !os.IsNotExist(err) {
		t.Fatalf("root replacement redirected audit state: %v", err)
	}
}

func permissionEnvironmentValue(value string) *string { return &value }

func setPermissionEnvironment(t *testing.T, name string, value *string) {
	t.Helper()
	old, present := os.LookupEnv(name)
	t.Cleanup(func() {
		if present {
			_ = os.Setenv(name, old)
			return
		}
		_ = os.Unsetenv(name)
	})
	if value == nil {
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := os.Setenv(name, *value); err != nil {
		t.Fatal(err)
	}
}

func TestReviewAuditStoreSecurePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audit")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := NewReviewAuditStore(ReviewAuditStoreOptions{Dir: dir})
	if err != nil {
		t.Fatalf("NewReviewAuditStore: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("dir mode = %o, want 0700 (repaired)", got)
	}

	if err := store.Record(context.Background(), validReviewAuditRecord(ReviewAuditKindEligible)); err != nil {
		t.Fatalf("Record: %v", err)
	}
	segInfo, err := os.Stat(filepath.Join(dir, reviewAuditActiveSegment))
	if err != nil {
		t.Fatal(err)
	}
	if got := segInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("segment mode = %o, want 0600", got)
	}

	// Lock file mode while held.
	var lockMode os.FileMode
	if err := store.withLock(context.Background(), func(*os.Root) error {
		info, err := os.Stat(filepath.Join(dir, reviewAuditLockName))
		if err != nil {
			return err
		}
		lockMode = info.Mode().Perm()
		return nil
	}); err != nil {
		t.Fatalf("withLock: %v", err)
	}
	if lockMode != 0o600 {
		t.Fatalf("lock mode = %o, want 0600", lockMode)
	}
	guardInfo, err := os.Stat(filepath.Join(dir, reviewAuditLockGuardName))
	if err != nil {
		t.Fatal(err)
	}
	if got := guardInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("coordination file mode = %o, want 0600", got)
	}

	// A pre-created loose segment is repaired to 0600 on the next append.
	if err := os.Chmod(filepath.Join(dir, reviewAuditActiveSegment), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(context.Background(), validReviewAuditRecord(ReviewAuditKindEligible)); err != nil {
		t.Fatalf("Record: %v", err)
	}
	segInfo, err = os.Stat(filepath.Join(dir, reviewAuditActiveSegment))
	if err != nil {
		t.Fatal(err)
	}
	if got := segInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("segment mode after repair = %o, want 0600", got)
	}
}

func TestReviewAuditStoreRotationAndRetention(t *testing.T) {
	store := newReviewAuditTestStore(t, func(opts *ReviewAuditStoreOptions) {
		opts.SegmentMaxBytes = 240
		opts.MaxSegments = 3
	})
	written := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		record := validReviewAuditRecord(ReviewAuditKindEligible)
		record.EventID = fmt.Sprintf("%032x", i+1)
		if err := store.Record(context.Background(), record); err != nil {
			t.Fatalf("Record %d: %v", i, err)
		}
		written = append(written, record.EventID)
	}

	// At most 3 owned segments retained.
	entries, err := os.ReadDir(store.dir)
	if err != nil {
		t.Fatal(err)
	}
	segmentCount := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "events.") &&
			strings.HasSuffix(entry.Name(), ".jsonl") {
			segmentCount++
			if info, err := entry.Info(); err != nil || !info.Mode().IsRegular() {
				t.Fatalf("segment %s is not regular", entry.Name())
			}
		}
	}
	if segmentCount > 3 {
		t.Fatalf("retained segments = %d, want <= 3", segmentCount)
	}

	// Load reports the retained window only, as a suffix of what was written.
	result, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if result.MalformedRecords != 0 {
		t.Fatalf("MalformedRecords = %d, want 0", result.MalformedRecords)
	}
	if len(result.Records) == 0 {
		t.Fatal("Load returned no records")
	}
	position := len(written) - len(result.Records)
	for i, record := range result.Records {
		if record.EventID != written[position+i] {
			t.Fatalf("record %d id = %s, want suffix id %s", i, record.EventID, written[position+i])
		}
	}
}

func TestReviewAuditStoreMalformedLoad(t *testing.T) {
	store := newReviewAuditTestStore(t, nil)
	valid := validReviewAuditRecord(ReviewAuditKindTerminal)
	line, err := encodeReviewAuditRecord(&valid)
	if err != nil {
		t.Fatal(err)
	}
	content := append([]byte("this is not json\n"), line...)
	content = append(content, []byte(`{"schema_version":99,"event_id":"zz","kind":"bogus"}`+"\n")...)
	if err := os.WriteFile(filepath.Join(store.dir, reviewAuditActiveSegment), content, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if result.MalformedRecords != 2 {
		t.Fatalf("MalformedRecords = %d, want 2", result.MalformedRecords)
	}
	if len(result.Records) != 1 {
		t.Fatalf("records = %d, want 1", len(result.Records))
	}
	// Only typed records come back; raw corrupt bytes never surface.
	encoded, err := json.Marshal(result.Records)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "this is not json") || strings.Contains(string(encoded), "bogus") {
		t.Fatal("Load leaked raw corrupt bytes")
	}
	if result.Records[0].ReviewerDecision != "approve" {
		t.Fatalf("valid record lost, got %+v", result.Records[0])
	}
}

func TestReviewAuditStoreStrictJSONLoad(t *testing.T) {
	store := newReviewAuditTestStore(t, nil)
	valid := validReviewAuditRecord(ReviewAuditKindTerminal)
	line, err := encodeReviewAuditRecord(&valid)
	if err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSuffix(string(line), "}\n")
	content := []byte(base + `,"raw_input":"must-not-load"}` + "\n")
	content = append(content, []byte(base+`,"event_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`+"\n")...)
	content = append(content, []byte(strings.TrimSuffix(string(line), "\n")+`{"schema_version":1}`+"\n")...)
	content = append(content, []byte(strings.Replace(
		strings.TrimSuffix(string(line), "\n"),
		`"latency_ms":12`,
		`"latency_ms":null`,
		1,
	)+"\n")...)
	content = append(content, line...)
	if err := os.WriteFile(filepath.Join(store.dir, reviewAuditActiveSegment), content, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if result.MalformedRecords != 4 {
		t.Fatalf("MalformedRecords = %d, want 4", result.MalformedRecords)
	}
	if len(result.Records) != 1 || result.Records[0].EventID != valid.EventID {
		t.Fatalf("valid records = %+v, want only the final valid record", result.Records)
	}
}

func TestDecodeReviewAuditRecordRejectsNullForEveryField(t *testing.T) {
	for field := range reviewAuditJSONFields {
		t.Run(field, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(`{"%s":null}`, field))
			if _, err := decodeReviewAuditRecord(raw); err == nil {
				t.Fatal("decodeReviewAuditRecord accepted null")
			} else if !strings.Contains(err.Error(), "null record value") {
				t.Fatalf("decodeReviewAuditRecord error = %v, want null rejection", err)
			}
		})
	}
}

func TestReviewAuditStorePartialTailRepair(t *testing.T) {
	store := newReviewAuditTestStore(t, nil)
	valid := validReviewAuditRecord(ReviewAuditKindEligible)
	line, err := encodeReviewAuditRecord(&valid)
	if err != nil {
		t.Fatal(err)
	}
	partial := `{"schema_version":1,"event_id":"0123`
	active := filepath.Join(store.dir, reviewAuditActiveSegment)
	content := append(append([]byte{}, line...), partial...)
	if err := os.WriteFile(active, content, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := store.Load()
	if err != nil {
		t.Fatalf("Load before repair: %v", err)
	}
	if before.PartialTailRecords != 1 || len(before.Records) != 1 {
		t.Fatalf("Load before repair = %+v, want one visible partial tail and one valid record", before)
	}

	caller := validReviewAuditRecord(ReviewAuditKindAttempt)
	caller.EventID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := store.Record(context.Background(), caller); err != nil {
		t.Fatalf("Record: %v", err)
	}

	data, err := os.ReadFile(active)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatal("active segment does not end with a newline after repair")
	}
	for i, raw := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		var parsed map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			t.Fatalf("line %d is not complete JSON after truncation: %v", i, err)
		}
	}

	result, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if result.TailRepairs != 1 {
		t.Fatalf("TailRepairs = %d, want 1", result.TailRepairs)
	}
	if result.PartialTailRecords != 0 {
		t.Fatalf("PartialTailRecords = %d, want 0 after repair", result.PartialTailRecords)
	}
	if len(result.Records) != 3 {
		t.Fatalf("records = %d, want 3 (valid, recovery, caller)", len(result.Records))
	}
	recovery := result.Records[1]
	if recovery.Kind != ReviewAuditKindStorageRecovery {
		t.Fatalf("record 1 kind = %s, want storage_recovery", recovery.Kind)
	}
	if recovery.RecoveredBytes != int64(len(partial)) {
		t.Fatalf("RecoveredBytes = %d, want %d", recovery.RecoveredBytes, len(partial))
	}
	if result.Records[2].EventID != caller.EventID {
		t.Fatal("caller record was not appended after the recovery record")
	}
}

func TestReviewAuditStoreRejectsSymlinkDirectory(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "audit")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewReviewAuditStore(ReviewAuditStoreOptions{Dir: link}); err == nil {
		t.Fatal("NewReviewAuditStore accepted a symlink directory")
	}
}

func TestReviewAuditStoreRejectsOperationTimeDirectoryReplacement(t *testing.T) {
	for _, operation := range []string{"record", "load", "delete"} {
		t.Run(operation, func(t *testing.T) {
			store := newReviewAuditTestStore(t, nil)
			if err := os.RemoveAll(store.dir); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), "target")
			if err := os.MkdirAll(target, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, store.dir); err != nil {
				t.Fatal(err)
			}
			var err error
			switch operation {
			case "record":
				err = store.Record(context.Background(), validReviewAuditRecord(ReviewAuditKindEligible))
			case "load":
				_, err = store.Load()
			case "delete":
				_, err = store.Delete()
			}
			if err == nil {
				t.Fatalf("%s accepted a replaced symlink directory", operation)
			}
		})
	}
}

func TestReviewAuditStoreRejectsSymlinkSegment(t *testing.T) {
	store := newReviewAuditTestStore(t, nil)
	if err := store.Record(context.Background(), validReviewAuditRecord(ReviewAuditKindEligible)); err != nil {
		t.Fatalf("Record: %v", err)
	}
	active := filepath.Join(store.dir, reviewAuditActiveSegment)
	if err := os.Remove(active); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, active); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(context.Background(), validReviewAuditRecord(ReviewAuditKindEligible)); err == nil {
		t.Fatal("Record accepted a symlink segment")
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("Load accepted a symlink segment")
	}
	if _, err := store.Delete(); err == nil {
		t.Fatal("Delete accepted a symlink segment")
	}
}

func TestReviewAuditStoreRejectsSegmentSwapAfterLstat(t *testing.T) {
	store := newReviewAuditTestStore(t, nil)
	if err := store.Record(
		context.Background(),
		validReviewAuditRecord(ReviewAuditKindEligible),
	); err != nil {
		t.Fatalf("Record: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	const outsideContent = "outside-must-remain"
	if err := os.WriteFile(outside, []byte(outsideContent), 0o600); err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(store.dir, reviewAuditActiveSegment)
	var swapped atomic.Bool
	store.testAfterSegmentLstat = func(name string) {
		if name != reviewAuditActiveSegment || !swapped.CompareAndSwap(false, true) {
			return
		}
		if err := os.Remove(active); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, active); err != nil {
			t.Fatal(err)
		}
	}
	next := validReviewAuditRecord(ReviewAuditKindEligible)
	next.EventID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := store.Record(context.Background(), next); err == nil {
		t.Fatal("Record accepted a segment replaced after Lstat")
	}
	data, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != outsideContent {
		t.Fatalf("outside content = %q, want unchanged", data)
	}
}

func TestReviewAuditPinnedRootDoesNotFollowDirectoryReplacement(t *testing.T) {
	store := newReviewAuditTestStore(t, nil)
	if err := store.Record(
		context.Background(),
		validReviewAuditRecord(ReviewAuditKindEligible),
	); err != nil {
		t.Fatalf("Record: %v", err)
	}
	root, exists, err := openReviewAuditRoot(store.dir)
	if err != nil || !exists {
		t.Fatalf("openReviewAuditRoot: exists=%t err=%v", exists, err)
	}
	defer root.Close()

	moved := store.dir + ".moved"
	if err := os.Rename(store.dir, moved); err != nil {
		t.Fatal(err)
	}
	replacement := t.TempDir()
	if err := os.Symlink(replacement, store.dir); err != nil {
		t.Fatal(err)
	}
	record := validReviewAuditRecord(ReviewAuditKindEligible)
	record.EventID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	line, err := encodeReviewAuditRecord(&record)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.appendLine(root, line); err != nil {
		t.Fatalf("appendLine through pinned root: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(replacement, reviewAuditActiveSegment)); !os.IsNotExist(err) {
		t.Fatal("appendLine followed the replacement directory")
	}
	data, err := os.ReadFile(filepath.Join(moved, reviewAuditActiveSegment))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(record.EventID)) {
		t.Fatal("appendLine did not remain bound to the pinned directory")
	}
}

func TestReviewAuditStoreRejectsIntermediateSymlinkDuringRotation(t *testing.T) {
	store := newReviewAuditTestStore(t, func(opts *ReviewAuditStoreOptions) {
		opts.SegmentMaxBytes = 240
		opts.MaxSegments = 4
	})
	if err := store.Record(context.Background(), validReviewAuditRecord(ReviewAuditKindEligible)); err != nil {
		t.Fatalf("Record: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(store.dir, store.segmentName(1))); err != nil {
		t.Fatal(err)
	}
	next := validReviewAuditRecord(ReviewAuditKindEligible)
	next.EventID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := store.Record(context.Background(), next); err == nil {
		t.Fatal("Record rotated an intermediate symlink")
	}
}

func TestReviewAuditStoreRejectsSymlinkLock(t *testing.T) {
	store := newReviewAuditTestStore(t, nil)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(store.dir, reviewAuditLockName)); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(context.Background(), validReviewAuditRecord(ReviewAuditKindEligible)); err == nil {
		t.Fatal("Record accepted a symlink lock")
	}
}

func TestReviewAuditStoreCrossInstanceLocking(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audit")
	opts := func() ReviewAuditStoreOptions {
		return ReviewAuditStoreOptions{
			Dir:            dir,
			LockTimeout:    5 * time.Second,
			StaleLockAfter: 30 * time.Second,
		}
	}
	first, err := NewReviewAuditStore(opts())
	if err != nil {
		t.Fatalf("NewReviewAuditStore: %v", err)
	}
	second, err := NewReviewAuditStore(opts())
	if err != nil {
		t.Fatalf("NewReviewAuditStore: %v", err)
	}

	// A fresh foreign lock blocks acquisition until the timeout.
	lockPath := filepath.Join(dir, reviewAuditLockName)
	if err := os.WriteFile(lockPath, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	blocked, err := NewReviewAuditStore(ReviewAuditStoreOptions{
		Dir:            dir,
		LockTimeout:    150 * time.Millisecond,
		StaleLockAfter: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewReviewAuditStore: %v", err)
	}
	if err := blocked.Record(context.Background(), validReviewAuditRecord(ReviewAuditKindEligible)); err == nil {
		t.Fatal("Record acquired a held lock")
	}

	// A stale lock is broken per the stale-lock policy.
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(lockPath, past, past); err != nil {
		t.Fatal(err)
	}
	if err := first.Record(context.Background(), validReviewAuditRecord(ReviewAuditKindEligible)); err != nil {
		t.Fatalf("Record did not break a stale lock: %v", err)
	}

	// Concurrent appends from both instances serialize and lose nothing.
	var wg sync.WaitGroup
	for instance, store := range []*ReviewAuditStore{first, second} {
		for i := 0; i < 25; i++ {
			wg.Add(1)
			go func(store *ReviewAuditStore, instance, i int) {
				defer wg.Done()
				record := validReviewAuditRecord(ReviewAuditKindEligible)
				record.EventID = fmt.Sprintf("%032x", instance*1000+i+1)
				if err := store.Record(context.Background(), record); err != nil {
					t.Errorf("Record: %v", err)
				}
			}(store, instance, i)
		}
	}
	wg.Wait()
	result, err := first.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if result.MalformedRecords != 0 {
		t.Fatalf("MalformedRecords = %d, want 0", result.MalformedRecords)
	}
	if len(result.Records) != 51 { // 1 stale-lock record + 50 concurrent
		t.Fatalf("records = %d, want 51", len(result.Records))
	}
}

func TestReviewAuditStoreSerializesStaleLockReclamation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audit")
	newStore := func() *ReviewAuditStore {
		store, err := NewReviewAuditStore(ReviewAuditStoreOptions{
			Dir:            dir,
			LockTimeout:    2 * time.Second,
			StaleLockAfter: 30 * time.Second,
		})
		if err != nil {
			t.Fatalf("NewReviewAuditStore: %v", err)
		}
		return store
	}
	first := newStore()
	second := newStore()
	lockPath := filepath.Join(dir, reviewAuditLockName)
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(lockPath, past, past); err != nil {
		t.Fatal(err)
	}

	var staleObservations atomic.Int32
	firstObserved := make(chan struct{})
	releaseFirst := make(chan struct{})
	hook := func() {
		if staleObservations.Add(1) == 1 {
			close(firstObserved)
			<-releaseFirst
		}
	}
	first.testAfterStaleLock = hook
	second.testAfterStaleLock = hook

	errs := make(chan error, 2)
	go func() {
		record := validReviewAuditRecord(ReviewAuditKindEligible)
		record.EventID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		errs <- first.Record(context.Background(), record)
	}()
	<-firstObserved
	go func() {
		record := validReviewAuditRecord(ReviewAuditKindEligible)
		record.EventID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		errs <- second.Record(context.Background(), record)
	}()
	time.Sleep(50 * time.Millisecond)
	if got := staleObservations.Load(); got != 1 {
		t.Fatalf("stale observations while first reclaimer holds guard = %d, want 1", got)
	}
	close(releaseFirst)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	if got := staleObservations.Load(); got != 1 {
		t.Fatalf("stale observations = %d, want exactly one", got)
	}
	result, err := first.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(result.Records) != 2 {
		t.Fatalf("records = %d, want 2", len(result.Records))
	}
}

func TestReviewAuditStoreLoadUsesCrossProcessLock(t *testing.T) {
	store := newReviewAuditTestStore(t, func(opts *ReviewAuditStoreOptions) {
		opts.LockTimeout = time.Second
	})
	if err := store.Record(context.Background(), validReviewAuditRecord(ReviewAuditKindEligible)); err != nil {
		t.Fatalf("Record: %v", err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	lockDone := make(chan error, 1)
	go func() {
		lockDone <- store.withLock(context.Background(), func(*os.Root) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started

	loadDone := make(chan error, 1)
	go func() {
		_, err := store.Load()
		loadDone <- err
	}()
	select {
	case err := <-loadDone:
		t.Fatalf("Load did not wait for journal lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-lockDone; err != nil {
		t.Fatalf("withLock: %v", err)
	}
	if err := <-loadDone; err != nil {
		t.Fatalf("Load after lock release: %v", err)
	}
}

func TestReviewAuditStoreDelete(t *testing.T) {
	store := newReviewAuditTestStore(t, func(opts *ReviewAuditStoreOptions) {
		opts.SegmentMaxBytes = 240
		opts.MaxSegments = 3
	})
	for i := 0; i < 12; i++ {
		record := validReviewAuditRecord(ReviewAuditKindEligible)
		record.EventID = fmt.Sprintf("%032x", i+1)
		if err := store.Record(context.Background(), record); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	neighbor := filepath.Join(store.dir, "notes.txt")
	if err := os.WriteFile(neighbor, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	unknownSegment := filepath.Join(store.dir, "events.8.jsonl")
	if err := os.WriteFile(unknownSegment, []byte("not owned\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := store.Delete()
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if result.SegmentsRemoved == 0 || result.BytesRemoved == 0 {
		t.Fatalf("Delete removed %d segments / %d bytes, want positive", result.SegmentsRemoved, result.BytesRemoved)
	}
	for i := 0; i < store.maxSegments; i++ {
		if _, err := os.Lstat(filepath.Join(store.dir, store.segmentName(i))); !os.IsNotExist(err) {
			t.Fatalf("owned segment %d still present after Delete", i)
		}
	}
	if _, err := os.Stat(neighbor); err != nil {
		t.Fatal("Delete removed an unknown neighbor file")
	}
	if _, err := os.Stat(unknownSegment); err != nil {
		t.Fatal("Delete removed an unowned events.* neighbor")
	}
	if info, err := os.Stat(store.dir); err != nil || !info.IsDir() {
		t.Fatal("Delete removed the store directory")
	}

	// Deleting again is an empty no-op.
	again, err := store.Delete()
	if err != nil {
		t.Fatalf("second Delete: %v", err)
	}
	if again.SegmentsRemoved != 0 || again.BytesRemoved != 0 {
		t.Fatalf("second Delete = %+v, want zeros", again)
	}
}

func TestReviewAuditStoreValidation(t *testing.T) {
	store := newReviewAuditTestStore(t, nil)
	ctx := context.Background()

	cases := map[string]func(*ReviewAuditRecord){
		"schema version": func(r *ReviewAuditRecord) { r.SchemaVersion = 2 },
		"short event id": func(r *ReviewAuditRecord) { r.EventID = "abcd" },
		"uppercase event id": func(r *ReviewAuditRecord) {
			r.EventID = strings.ToUpper(reviewAuditTestEventID)
		},
		"negative latency": func(r *ReviewAuditRecord) { r.LatencyMS = -1 },
		"unsafe token":     func(r *ReviewAuditRecord) { r.CanonicalTool = "bad/tool name" },
	}
	for name, mutate := range cases {
		record := validReviewAuditRecord(ReviewAuditKindTerminal)
		mutate(&record)
		if err := store.Record(ctx, record); err == nil {
			t.Fatalf("%s: Record accepted an invalid record", name)
		} else if strings.Contains(err.Error(), "bad/tool name") {
			t.Fatalf("%s: error leaked record contents: %v", name, err)
		}
	}

	kindCases := map[string]ReviewAuditRecord{
		"eligible wrong class": func() ReviewAuditRecord {
			r := validReviewAuditRecord(ReviewAuditKindEligible)
			r.DeterministicClass = "allow"
			return r
		}(),
		"eligible missing identity": func() ReviewAuditRecord {
			r := validReviewAuditRecord(ReviewAuditKindEligible)
			r.ActionKind = ""
			return r
		}(),
		"attempt missing route": func() ReviewAuditRecord {
			r := validReviewAuditRecord(ReviewAuditKindAttempt)
			r.Provider = ""
			return r
		}(),
		"attempt unsafe real route": func() ReviewAuditRecord {
			r := validReviewAuditRecord(ReviewAuditKindAttempt)
			r.Model = "model with spaces"
			return r
		}(),
		"attempt extra action": func() ReviewAuditRecord {
			r := validReviewAuditRecord(ReviewAuditKindAttempt)
			r.CanonicalTool = "Bash"
			return r
		}(),
		"terminal bad decision": func() ReviewAuditRecord {
			r := validReviewAuditRecord(ReviewAuditKindTerminal)
			r.ReviewerDecision = "maybe"
			return r
		}(),
		"terminal invalid decision reason pair": func() ReviewAuditRecord {
			r := validReviewAuditRecord(ReviewAuditKindTerminal)
			r.ReasonCode = ReviewReasonUnexpectedRisk
			return r
		}(),
		"terminal missing completed reason": func() ReviewAuditRecord {
			r := validReviewAuditRecord(ReviewAuditKindTerminal)
			r.ReasonCode = ""
			return r
		}(),
		"terminal unavailable without reason": func() ReviewAuditRecord {
			r := validReviewAuditRecord(ReviewAuditKindTerminal)
			r.ReviewerStatus = "unavailable"
			r.ReviewerDecision = ""
			return r
		}(),
		"terminal unavailable with decision": func() ReviewAuditRecord {
			r := validReviewAuditRecord(ReviewAuditKindTerminal)
			r.ReviewerStatus = "unavailable"
			r.ReasonCode = "timeout"
			return r
		}(),
		"comparison bad source": func() ReviewAuditRecord {
			r := validReviewAuditRecord(ReviewAuditKindComparison)
			r.ComparisonSource = "reviewer"
			return r
		}(),
		"comparison bad decision": func() ReviewAuditRecord {
			r := validReviewAuditRecord(ReviewAuditKindComparison)
			r.ExpectedDecision = "escalate"
			return r
		}(),
		"comparison extra action": func() ReviewAuditRecord {
			r := validReviewAuditRecord(ReviewAuditKindComparison)
			r.ActionKind = "filesystem_read"
			return r
		}(),
		"corpus bad source": func() ReviewAuditRecord {
			r := validReviewAuditRecord(ReviewAuditKindCorpusGroundTruth)
			r.ComparisonSource = "human"
			return r
		}(),
		"corpus missing case": func() ReviewAuditRecord {
			r := validReviewAuditRecord(ReviewAuditKindCorpusGroundTruth)
			r.CorpusCaseID = ""
			return r
		}(),
		"storage recovery extra field": func() ReviewAuditRecord {
			r := validReviewAuditRecord(ReviewAuditKindStorageRecovery)
			r.CanonicalTool = "Bash"
			return r
		}(),
		"storage recovery zero bytes": func() ReviewAuditRecord {
			r := validReviewAuditRecord(ReviewAuditKindStorageRecovery)
			r.RecoveredBytes = 0
			return r
		}(),
		"dispatcher diagnostic unknown code": func() ReviewAuditRecord {
			r := validReviewAuditRecord(ReviewAuditKindDispatcherDiagnostic)
			r.DispatcherDiagnostic = "unknown"
			return r
		}(),
		"dispatcher diagnostic zero count": func() ReviewAuditRecord {
			r := validReviewAuditRecord(ReviewAuditKindDispatcherDiagnostic)
			r.DiagnosticCount = 0
			return r
		}(),
		"dispatcher diagnostic mixed payload": func() ReviewAuditRecord {
			r := validReviewAuditRecord(ReviewAuditKindDispatcherDiagnostic)
			r.ActionKind = "filesystem_read"
			return r
		}(),
		"unknown kind": func() ReviewAuditRecord {
			r := validReviewAuditRecord(ReviewAuditKindTerminal)
			r.Kind = "nonsense"
			return r
		}(),
	}
	for name, record := range kindCases {
		if err := store.Record(ctx, record); err == nil {
			t.Fatalf("%s: Record accepted an invalid record", name)
		}
	}

	// Valid records of every kind are accepted and defaulted.
	for _, kind := range []ReviewAuditKind{
		ReviewAuditKindEligible, ReviewAuditKindAttempt, ReviewAuditKindTerminal,
		ReviewAuditKindComparison, ReviewAuditKindCorpusGroundTruth,
		ReviewAuditKindDispatcherDiagnostic,
	} {
		record := validReviewAuditRecord(kind)
		record.EventID = ""
		record.OccurredAt = time.Time{}
		if err := store.Record(ctx, record); err != nil {
			t.Fatalf("%s: Record rejected a valid record: %v", kind, err)
		}
	}
	result, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(result.Records) != 6 {
		t.Fatalf("records = %d, want 6", len(result.Records))
	}
	for _, record := range result.Records {
		if !reviewAuditEventIDRE.MatchString(record.EventID) {
			t.Fatalf("defaulted event id %q is not 32 lowercase hex", record.EventID)
		}
		if record.OccurredAt.IsZero() || record.OccurredAt.Location() != time.UTC {
			t.Fatalf("defaulted occurred_at is not nonzero UTC: %v", record.OccurredAt)
		}
	}
}

func TestReviewAuditStoreSchemaFields(t *testing.T) {
	record := ReviewAuditRecord{
		SchemaVersion:        ReviewAuditSchemaVersion,
		EventID:              reviewAuditTestEventID,
		OccurredAt:           reviewAuditTestClock(),
		Kind:                 ReviewAuditKindTerminal,
		CanonicalTool:        "Bash",
		ActionKind:           "filesystem_read",
		DeterministicClass:   "review",
		ReviewerStatus:       "completed",
		ReviewerDecision:     "deny",
		ReasonCode:           "policy_deny",
		LatencyMS:            42,
		Provider:             "anthropic",
		Model:                "claude-opus",
		DataBoundary:         PermissionReviewDataBoundary,
		ComparisonSource:     "human",
		ExpectedDecision:     "allow",
		CorpusID:             "corpus_v1",
		CorpusCaseID:         "case_0007",
		RecoveredBytes:       9,
		DispatcherDiagnostic: ReviewAuditDiagnosticEnqueueDrop,
		DiagnosticCount:      1,
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"schema_version": true, "event_id": true, "occurred_at": true, "kind": true,
		"canonical_tool": true, "action_kind": true, "deterministic_class": true,
		"reviewer_status": true, "reviewer_decision": true, "reason_code": true,
		"latency_ms": true, "provider": true, "model": true, "data_boundary": true,
		"comparison_source": true, "expected_decision": true, "corpus_id": true,
		"corpus_case_id": true, "recovered_bytes": true,
		"dispatcher_diagnostic": true, "diagnostic_count": true,
	}
	if len(fields) != len(want) {
		t.Fatalf("encoded fields = %d, want %d: %v", len(fields), len(want), fields)
	}
	for field := range fields {
		if !want[field] {
			t.Fatalf("unexpected sensitive or free-form field %q in schema", field)
		}
	}
	for _, forbidden := range []string{
		"request", "input", "path", "digest", "nonce", "rationale",
		"credential", "secret", "transcript", "session", "agent", "cwd", "message",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("schema contains forbidden sensitive token %q", forbidden)
		}
	}
}

func TestReviewAuditDispatcherDiagnosticJSONShape(t *testing.T) {
	record := validReviewAuditRecord(ReviewAuditKindDispatcherDiagnostic)
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"schema_version":        true,
		"event_id":              true,
		"occurred_at":           true,
		"kind":                  true,
		"dispatcher_diagnostic": true,
		"diagnostic_count":      true,
	}
	if len(fields) != len(want) {
		t.Fatalf("encoded fields = %d, want %d: %v", len(fields), len(want), fields)
	}
	for field := range fields {
		if !want[field] {
			t.Fatalf("dispatcher diagnostic exposed non-diagnostic field %q", field)
		}
	}
}

func TestReviewAuditStoreMissingIsEmpty(t *testing.T) {
	store := newReviewAuditTestStore(t, nil)
	result, err := store.Load()
	if err != nil {
		t.Fatalf("Load on empty store: %v", err)
	}
	if len(result.Records) != 0 || result.MalformedRecords != 0 ||
		result.PartialTailRecords != 0 || result.TailRepairs != 0 {
		t.Fatalf("Load on empty store = %+v, want zeros", result)
	}
	if err := os.RemoveAll(store.dir); err != nil {
		t.Fatal(err)
	}
	result, err = store.Load()
	if err != nil {
		t.Fatalf("Load with missing directory: %v", err)
	}
	if len(result.Records) != 0 {
		t.Fatalf("Load with missing directory returned %d records", len(result.Records))
	}
	deleted, err := store.Delete()
	if err != nil {
		t.Fatalf("Delete with missing directory: %v", err)
	}
	if deleted.SegmentsRemoved != 0 || deleted.BytesRemoved != 0 {
		t.Fatalf("Delete with missing directory = %+v, want zeros", deleted)
	}
}
