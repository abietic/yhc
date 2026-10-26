package filehistory

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func openJournal(t *testing.T) (*RevisionJournal, string, string) {
	t.Helper()
	root, dir := t.TempDir(), filepath.Join(t.TempDir(), "private")
	j, err := OpenRevisionJournal(root, dir, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	return j, root, dir
}

func TestRevisionJournalRoundTripPendingAndDefensiveCopies(t *testing.T) {
	j, root, dir := openJournal(t)
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	id, err := j.Begin("turn", "tool", "Edit", "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := j.Finish(id, "succeeded"); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}

	j, err = OpenRevisionJournal(root, dir, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	r, ok := j.Read(id)
	if !ok || string(r.Before.Data) != "before\n" || r.Before.Mode != 0o640 || string(r.After.Data) != "after\n" || r.After.Mode != 0o600 {
		t.Fatalf("unexpected replay: %#v", r)
	}
	r.Before.Data[0] = 'X'
	if got, _ := j.Read(id); string(got.Before.Data) != "before\n" {
		t.Fatal("Read leaked mutable bytes")
	}
	s := j.Snapshot()
	if len(s.Records) != 1 || !s.Records[0].BeforeCaptured || !s.Records[0].AfterCaptured || !s.Records[0].Changed {
		t.Fatalf("bad snapshot: %#v", s)
	}
	if _, err := j.Begin("t", "u", "Edit", "a.txt"); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = OpenRevisionJournal(root, dir, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	pending := j.Snapshot().Records[1]
	if pending.AfterCaptured {
		t.Fatal("pending before frame inferred an after state")
	}
}

func TestRevisionJournalRejectsCorruptionWithoutRewriting(t *testing.T) {
	j, root, dir := openJournal(t)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Begin("t", "u", "Edit", "a.txt"); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, journalFile)
	original, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	cases := [][]byte{
		original[:len(original)-1],
		bytes.Replace(original, []byte("\n"), []byte(" {}\n"), 1),
		bytes.Replace(original, []byte(`"session-1"`), []byte(`"other"`), 1),
		bytes.Replace(original, []byte(`"frame_sequence":1`), []byte(`"frame_sequence":2`), 1),
		bytes.Replace(original, []byte(`"digest":"`), []byte(`"digest":"bad`), 1),
	}
	for i, bad := range cases {
		if err := os.WriteFile(log, bad, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenRevisionJournal(root, dir, "session-1"); err == nil {
			t.Fatalf("case %d accepted corrupt journal", i)
		}
		got, err := os.ReadFile(log)
		if err != nil || !bytes.Equal(got, bad) {
			t.Fatalf("case %d rewrote journal", i)
		}
	}
}

func TestRevisionJournalDelayedCreationAndPathBounds(t *testing.T) {
	j, root, dir := openJournal(t)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("Open created private directory: %v", err)
	}
	if err := j.RecordUncovered("t", "u", "Bash", "external"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("private directory: %v %#o", err, info.Mode())
	}
	log, err := os.Stat(filepath.Join(dir, journalFile))
	if err != nil || log.Mode().Perm() != 0o600 {
		t.Fatalf("private log: %v %#o", err, log.Mode())
	}
	for _, p := range []string{"../x", ".git/config", filepath.Join(root, "a")} {
		if _, err := j.Begin("t", "u", "Edit", p); err == nil {
			t.Fatalf("accepted invalid path %q", p)
		}
	}
	if err := os.Symlink("missing", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Begin("t", "u", "Edit", "link"); err == nil {
		t.Fatal("accepted symlink")
	}
	if err := os.WriteFile(filepath.Join(root, "binary"), []byte{'a', 0}, 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := j.Begin("t", "u", "Edit", "binary")
	if err != nil {
		t.Fatal(err)
	}
	r, _ := j.Read(id)
	if r.Before.Captured || r.Before.Reason != "binary" {
		t.Fatalf("binary state: %#v", r.Before)
	}
	beforeFinish := j.Snapshot().Revision
	if err := j.Finish(id, "failed"); err != nil {
		t.Fatal(err)
	}
	if got := j.Snapshot().Revision; got != beforeFinish+1 {
		t.Fatalf("Finish did not advance revision: %d -> %d", beforeFinish, got)
	}
	if err := j.Finish(id, "failed"); err == nil {
		t.Fatal("accepted duplicate unsupported finish")
	}
	if err := os.WriteFile(filepath.Join(root, "large"), make([]byte, maxRevisionBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err = j.Begin("t", "u", "Edit", "large")
	if err != nil {
		t.Fatal(err)
	}
	r, _ = j.Read(id)
	if r.Before.Captured || r.Before.Reason != "too_large" {
		t.Fatalf("large state: %#v", r.Before)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = OpenRevisionJournal(root, dir, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	r, _ = j.Read(j.Snapshot().Records[1].ID)
	if r.After.Captured || r.After.Reason != "binary" || r.Outcome != "failed" {
		t.Fatalf("unsupported after did not replay: %#v", r)
	}
}

func TestRevisionJournalRejectsForeignRootAndInvalidFrameDigest(t *testing.T) {
	j, root, dir := openJournal(t)
	if err := os.WriteFile(filepath.Join(root, "a"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := j.Begin("t", "u", "Edit", "a")
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Finish(id, "failed"); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, journalFile)
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var f journalFrame
	if err := json.Unmarshal(bytes.SplitN(b, []byte{'\n'}, 2)[0], &f); err != nil {
		t.Fatal(err)
	}
	f.Root = t.TempDir()
	bad, _ := json.Marshal(f)
	bad = append(bad, '\n')
	if err := os.WriteFile(log, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRevisionJournal(root, dir, "session-1"); err == nil {
		t.Fatal("accepted foreign root")
	}
}

func TestRevisionJournalCloseAndOutcomes(t *testing.T) {
	j, root, _ := openJournal(t)
	if err := os.WriteFile(filepath.Join(root, "a"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := j.Begin("t", "u", "Edit", "a")
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Finish(id, "unknown"); err == nil {
		t.Fatal("accepted outcome")
	}
	if err := j.Finish(id, "cancelled"); err != nil {
		t.Fatal(err)
	}
	if err := j.Finish(id, "cancelled"); err == nil {
		t.Fatal("accepted duplicate finish")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Begin("t", "u", "Edit", "a"); !errors.Is(err, ErrJournalClosed) {
		t.Fatalf("closed begin: %v", err)
	}
}

func TestRevisionJournalSealsOnExternalLogGrowth(t *testing.T) {
	j, _, dir := openJournal(t)
	if err := j.RecordUncovered("t", "u", "Bash", "external"); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, journalFile)
	if f, err := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0); err != nil {
		t.Fatal(err)
	} else if _, err := f.WriteString("{}\n"); err != nil {
		f.Close()
		t.Fatal(err)
	} else if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := j.RecordUncovered("t", "u", "Bash", "external"); err == nil {
		t.Fatal("accepted append after external log growth")
	}
	if j.Snapshot().Warning == "" {
		t.Fatal("sealed journal did not expose warning")
	}
}
