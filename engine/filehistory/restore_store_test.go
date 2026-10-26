package filehistory

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func restoreState(data string) RevisionFileState {
	b := []byte(data)
	sum := sha256.Sum256(b)
	return RevisionFileState{Captured: true, Exists: true, Mode: 0o600, Data: b, Digest: hex.EncodeToString(sum[:])}
}

func restoreOperationFor(j *RevisionJournal) RestoreOperation {
	op := RestoreOperation{
		ID: "restore-1", SessionID: j.sessionID, TurnID: "turn-1", Root: j.rootPath, RootIdentity: j.rootIdentity,
		ConfirmationID: "confirmation", PermissionRevision: "permission", Direction: "restore", State: "applying",
		JournalRevision: j.frameSequence, StartedAt: time.Now().UTC(),
		Paths: []RestorePath{{Path: "a.txt", BackupPath: ".yhc-restore-restore-1-0.backup", TempPath: ".yhc-restore-restore-1-0.tmp", Status: "pending", Original: restoreState("before"), Target: restoreState("after")}},
	}
	op.PlanDigest = RestoreOperationPlanDigest(op)
	return op
}

func TestRestoreOperationMissingDoesNotCreateStorage(t *testing.T) {
	j, _, dir := openJournal(t)
	defer j.Close()
	if op, found, err := j.LoadRestoreOperation(); err != nil || found || op.ID != "" {
		t.Fatalf("missing load = %#v %v %v", op, found, err)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("load created storage: %v", err)
	}
}

func TestRestoreOperationRoundTripModesAndDefensiveClone(t *testing.T) {
	j, _, dir := openJournal(t)
	defer j.Close()
	op := restoreOperationFor(j)
	if err := j.SaveRestoreOperation(op); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{restoreOperationFile} {
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode: %v %#o", name, err, info.Mode())
		}
	}
	got, found, err := j.LoadRestoreOperation()
	if err != nil || !found || got.Root != j.rootPath || got.RootIdentity != j.rootIdentity || string(got.Paths[0].Original.Data) != "before" {
		t.Fatalf("load = %#v %v %v", got, found, err)
	}
	got.Paths[0].Original.Data[0] = 'X'
	again, _, err := j.LoadRestoreOperation()
	if err != nil || string(again.Paths[0].Original.Data) != "before" {
		t.Fatalf("mutable bytes leaked: %#v %v", again, err)
	}
}

func TestRestoreOperationIgnoresInterruptedTemp(t *testing.T) {
	j, _, dir := openJournal(t)
	defer j.Close()
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, restoreOperationTemp), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := j.LoadRestoreOperation(); err != nil || found {
		t.Fatalf("temp became authority: found=%v err=%v", found, err)
	}
}

func TestRestoreOperationTransitionGuards(t *testing.T) {
	j, _, _ := openJournal(t)
	defer j.Close()
	op := restoreOperationFor(j)
	if err := j.SaveRestoreOperation(op); err != nil {
		t.Fatal(err)
	}
	replacement := restoreOperationFor(j)
	replacement.ID = "restore-2"
	replacement.Paths[0].BackupPath = ".yhc-restore-restore-2-0.backup"
	replacement.Paths[0].TempPath = ".yhc-restore-restore-2-0.tmp"
	replacement.PlanDigest = RestoreOperationPlanDigest(replacement)
	if err := j.SaveRestoreOperation(replacement); err == nil {
		t.Fatal("replaced active operation")
	}
	planMismatch := op
	planMismatch.PlanDigest = "other"
	if err := j.SaveRestoreOperation(planMismatch); err == nil {
		t.Fatal("changed immutable plan")
	}
	op.State, op.Paths[0].Status = "completed", "applied"
	if err := j.SaveRestoreOperation(op); err != nil {
		t.Fatal(err)
	}
	if err := j.SaveRestoreOperation(restoreOperationFor(j)); err == nil {
		t.Fatal("reapplied completed operation")
	}
	replacement = restoreOperationFor(j)
	replacement.ID = "restore-2"
	replacement.Paths[0].BackupPath = ".yhc-restore-restore-2-0.backup"
	replacement.Paths[0].TempPath = ".yhc-restore-restore-2-0.tmp"
	replacement.PlanDigest = RestoreOperationPlanDigest(replacement)
	replacement.State = "applying"
	if err := j.SaveRestoreOperation(replacement); err != nil {
		t.Fatalf("replace completed operation: %v", err)
	}
}

func TestRestoreOperationSameIDAllowsAdvancedJournalRevision(t *testing.T) {
	j, _, _ := openJournal(t)
	defer j.Close()
	op := restoreOperationFor(j)
	if err := j.SaveRestoreOperation(op); err != nil {
		t.Fatal(err)
	}
	if err := j.RecordUncovered("turn-later", "tool", "Bash", "external"); err != nil {
		t.Fatal(err)
	}
	op.State, op.Paths[0].Status = "partial", "failed"
	if err := j.SaveRestoreOperation(op); err != nil {
		t.Fatalf("same operation at advanced journal revision: %v", err)
	}
}

func TestRestoreOperationRejectsInvalidAndPreservesStableFile(t *testing.T) {
	j, _, dir := openJournal(t)
	defer j.Close()
	op := restoreOperationFor(j)
	if err := j.SaveRestoreOperation(op); err != nil {
		t.Fatal(err)
	}
	stablePath := filepath.Join(dir, restoreOperationFile)
	stable, err := os.ReadFile(stablePath)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		edit func(*RestoreOperation)
	}{
		{"digest", func(op *RestoreOperation) { op.Paths[0].Target.Digest = "bad" }},
		{"duplicate", func(op *RestoreOperation) { op.Paths = append(op.Paths, op.Paths[0]); op.Paths[1].Path = "z.txt" }},
		{"traversal", func(op *RestoreOperation) { op.Paths[0].BackupPath = "../backup" }},
		{"foreign-session", func(op *RestoreOperation) { op.SessionID = "other" }},
		{"foreign-root", func(op *RestoreOperation) { op.Root = t.TempDir() }},
		{"oversize", func(op *RestoreOperation) {
			op.Paths[0].Original.Data = bytes.Repeat([]byte("x"), maxRestoreRawBytes+1)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := cloneRestoreOperation(op)
			tc.edit(&bad)
			if err := j.SaveRestoreOperation(bad); err == nil {
				t.Fatal("accepted invalid operation")
			}
			got, err := os.ReadFile(stablePath)
			if err != nil || !bytes.Equal(got, stable) {
				t.Fatalf("stable operation overwritten: %v", err)
			}
		})
	}
	if err := os.Chmod(stablePath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := j.LoadRestoreOperation(); err == nil {
		t.Fatal("accepted insecure mode")
	}
	if err := os.Chmod(stablePath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(stablePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("elsewhere", stablePath); err != nil {
		t.Fatal(err)
	}
	if _, _, err := j.LoadRestoreOperation(); err == nil {
		t.Fatal("accepted symlink")
	}
}

func TestRestoreOperationDirectorySyncFailureDoesNotReportSuccess(t *testing.T) {
	j, _, _ := openJournal(t)
	defer j.Close()
	op := restoreOperationFor(j)
	if err := j.SaveRestoreOperation(op); err != nil {
		t.Fatal(err)
	}
	op.State, op.Paths[0].Status = "partial", "failed"
	j.syncDirectory = func(*os.Root) error { return errors.New("sync failed") }
	if err := j.SaveRestoreOperation(op); err == nil {
		t.Fatal("reported success after directory sync failure")
	}
}

func TestRestoreOperationTerminalAndDigestInvariants(t *testing.T) {
	j, _, _ := openJournal(t)
	defer j.Close()
	op := restoreOperationFor(j)
	if got, want := RestoreOperationPlanDigest(op), op.PlanDigest; got != want {
		t.Fatalf("plan digest = %q, want %q", got, want)
	}
	mutable := cloneRestoreOperation(op)
	mutable.State, mutable.Direction, mutable.ConfirmationID, mutable.PermissionRevision, mutable.Paths[0].Status = "partial", "rollback", "next", "next", "failed"
	mutable.Paths[0].Original.Data = []byte("different bytes are not plan identity")
	if RestoreOperationPlanDigest(mutable) != op.PlanDigest {
		t.Fatal("plan digest included mutable progress or data bytes")
	}
	badComplete := op
	badComplete.State = "completed"
	if err := j.SaveRestoreOperation(badComplete); err == nil {
		t.Fatal("accepted completed operation with pending path")
	}
	rollback := op
	rollback.Direction, rollback.State, rollback.Paths[0].Status = "rollback", "rolled_back", "rolled_back"
	if err := j.SaveRestoreOperation(rollback); err != nil {
		t.Fatalf("save rolled back terminal operation: %v", err)
	}
	rollback.Paths[0].Status = "already_applied"
	if err := j.SaveRestoreOperation(rollback); err == nil {
		t.Fatal("changed terminal path status")
	}
}

func TestRestoreOperationRejectsStoreDirectoryReplacementAndClosedJournal(t *testing.T) {
	j, _, dir := openJournal(t)
	op := restoreOperationFor(j)
	if err := j.SaveRestoreOperation(op); err != nil {
		t.Fatal(err)
	}
	moved := dir + "-moved"
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(moved) })
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := j.LoadRestoreOperation(); err == nil {
		t.Fatal("loaded after private directory replacement")
	}
	if err := j.SaveRestoreOperation(op); err == nil {
		t.Fatal("saved after private directory replacement")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := j.LoadRestoreOperation(); !errors.Is(err, ErrJournalClosed) {
		t.Fatalf("closed load error = %v", err)
	}
	if err := j.SaveRestoreOperation(op); !errors.Is(err, ErrJournalClosed) {
		t.Fatalf("closed save error = %v", err)
	}
}

func TestRestoreOperationRejectsLongAndControlText(t *testing.T) {
	j, _, _ := openJournal(t)
	defer j.Close()
	for _, badID := range []string{string(bytes.Repeat([]byte("x"), 257)), "restore\n1"} {
		op := restoreOperationFor(j)
		op.ID = badID
		op.PlanDigest = RestoreOperationPlanDigest(op)
		if err := j.SaveRestoreOperation(op); err == nil {
			t.Fatalf("accepted invalid text %q", badID)
		}
	}
}

func TestRestoreOperationLoadRejectsUnknownSchemaVersionWithoutRewrite(t *testing.T) {
	j, _, dir := openJournal(t)
	defer j.Close()
	if err := j.SaveRestoreOperation(restoreOperationFor(j)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, restoreOperationFile)
	var envelope map[string]any
	if b, err := os.ReadFile(path); err != nil || json.Unmarshal(b, &envelope) != nil {
		t.Fatal("read valid operation")
	}
	envelope["version"] = float64(2)
	bad, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := j.LoadRestoreOperation(); err == nil {
		t.Fatal("accepted unknown schema version")
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, bad) {
		t.Fatalf("invalid schema was rewritten: %v", err)
	}
}

func TestRestoreStoreRejectsTemporaryNamespaceReplacement(t *testing.T) {
	j, _, dir := openJournal(t)
	defer j.Close()
	operation := restoreOperationFor(j)
	j.restoreBeforeStorePublish = func() {
		if err := os.Remove(filepath.Join(dir, restoreOperationTemp)); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, restoreOperationTemp), []byte("unrelated replacement"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.SaveRestoreOperation(operation); err == nil {
		t.Fatal("published an unknown replacement intent")
	}
	if _, err := os.Lstat(filepath.Join(dir, restoreOperationFile)); !os.IsNotExist(err) {
		t.Fatal("unknown temporary became stable authority")
	}
}
