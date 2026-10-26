package filehistory

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRevisionJournalSealsWhenInitialDirectorySyncFails(t *testing.T) {
	j, _, dir := openJournal(t)
	j.syncDirectory = func(*os.Root) error { return errors.New("sync failed") }
	if err := j.RecordUncovered("t", "u", "Bash", "external"); err == nil {
		t.Fatal("accepted frame after directory sync failure")
	}
	if _, err := os.Stat(filepath.Join(dir, journalFile)); !os.IsNotExist(err) {
		t.Fatalf("created journal despite failed directory sync: %v", err)
	}
	if j.Snapshot().Revision != 0 || j.Snapshot().Warning == "" {
		t.Fatal("failed intent was accepted or unreported")
	}
}

func TestRevisionCaptureRejectsMetadataDrift(t *testing.T) {
	j, root, dir := openJournal(t)
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	j.captureOpened = func(string) {
		_ = os.Rename(path, path+".old")
		_ = os.WriteFile(path, []byte("after"), 0o600)
	}
	id, err := j.Begin("t", "u", "Edit", "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	r, _ := j.Read(id)
	if r.Before.Captured || r.Before.Reason != "changed_during_capture" || len(r.Before.Data) != 0 || r.Before.Digest != "" {
		t.Fatalf("captured drifting bytes: %#v", r.Before)
	}
	if err := j.Finish(id, "failed"); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRevisionJournal(root, dir, "session-1"); err != nil {
		t.Fatal(err)
	}
}

func TestRevisionJournalRejectsUncertainNewLogDirectorySync(t *testing.T) {
	j, _, dir := openJournal(t)
	defer j.Close()
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	calls := 0
	j.syncDirectory = func(*os.Root) error { calls++; return errors.New("log entry sync failed") }
	if err := j.RecordUncovered("t", "u", "Bash", "external"); err == nil {
		t.Fatal("accepted uncertain log entry")
	}
	if calls != 1 {
		t.Fatalf("post-create directory sync calls=%d", calls)
	}
	if info, err := os.Stat(filepath.Join(dir, journalFile)); err != nil || info.Size() == 0 {
		t.Fatal("fixture did not reach persisted log write")
	}
	if s := j.Snapshot(); s.Revision != 0 || s.Warning == "" || len(s.Records) != 0 {
		t.Fatalf("uncertain frame accepted: %+v", s)
	}
	if err := j.RecordUncovered("t", "next", "Bash", "external"); !errors.Is(err, ErrJournalClosed) {
		t.Fatal("uncertain journal remained writable")
	}
}

func TestRevisionJournalRejectsNonUTF8Path(t *testing.T) {
	j, _, _ := openJournal(t)
	defer j.Close()
	path := "file" + string([]byte{0xff})
	if _, err := j.Begin("t", "u", "Write", path); err == nil {
		t.Fatal("accepted filename that JSON cannot preserve")
	}
	if validStoredPath(path) {
		t.Fatal("replay accepted non-UTF8 path")
	}
}

func TestTurnTerminalRejectsOverflowingToolCounts(t *testing.T) {
	now := time.Now().UTC()
	largest := int(^uint(0) >> 1)
	turn := TurnRecord{SessionID: "s", ThreadID: "thread", TurnID: "t", StartedAt: now, FinishedAt: now, TerminalReason: "completed", KnownToolRecords: largest, KnownToolsSucceeded: largest, KnownToolsFailed: largest}
	if validTurnTerminal(&turn, "s") {
		t.Fatal("accepted tool counts exceeding retained records through overflow")
	}
}

func TestRevisionJournalRejectsRootReplacementAtSamePath(t *testing.T) {
	j, root, dir := openJournal(t)
	if err := j.RecordUncovered("t", "u", "Bash", "external"); err != nil {
		t.Fatal(err)
	}
	old := root + "-moved"
	if err := os.Rename(root, old); err != nil {
		t.Skipf("root cannot be moved on this platform: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(old) })
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := j.RecordUncovered("t", "u2", "Bash", "external"); err == nil {
		t.Fatal("old owner appended after namespace replacement")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := OpenRevisionJournal(root, dir, "session-1"); err == nil {
		reopened.Close()
		t.Fatal("replayed history against a different root at the same path")
	}
}
