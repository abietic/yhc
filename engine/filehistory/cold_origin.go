package filehistory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/google/uuid"
)

func coldNormalized(s *coldState, root, identity, migration string) (map[string][]byte, error) {
	files := map[string][]byte{}
	if _, found := s.files[journalFile]; found {
		raw := []byte{}
		for _, frame := range s.frames {
			frame.Root, frame.RootIdentity = root, identity
			frame.ColdMigrationID = migration
			line, err := json.Marshal(frame)
			if err != nil || len(line)+1 > maxFrameBytes {
				return nil, fmt.Errorf("normalized frame exceeds limit")
			}
			raw = append(raw, line...)
			raw = append(raw, '\n')
		}
		if len(raw) > maxJournalBytes {
			return nil, fmt.Errorf("normalized journal exceeds limit")
		}
		files[journalFile] = raw
	}
	if s.operation != nil {
		op := cloneRestoreOperation(*s.operation)
		op.Root, op.RootIdentity = root, identity
		op.PlanDigest = RestoreOperationPlanDigest(op)
		version := restoreOperationVersion
		if s.evolution != nil {
			version = 2
		}
		raw, err := json.Marshal(restoreOperationEnvelope{Kind: restoreOperationKind, Version: version, Operation: op, Evolution: s.evolution})
		if err != nil || len(raw) > maxRestoreEncodedBytes {
			return nil, fmt.Errorf("normalized operation exceeds limit")
		}
		files[restoreOperationFile] = raw
	}
	return files, nil
}

func coldMatchFiles(files map[string][]byte, p ColdHistoryProof) bool {
	return coldFileSHA(files, journalFile) == p.JournalSHA256 && int64(len(files[journalFile])) == p.JournalBytes && coldFileSHA(files, restoreOperationFile) == p.RestoreSHA256 && int64(len(files[restoreOperationFile])) == p.RestoreBytes && coldFileSHA(files, coldOriginFile) == p.OriginSHA256 && int64(len(files[coldOriginFile])) == p.OriginBytes
}

func coldMigrationID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

func coldReceiptValid(r ColdHistoryReceipt) bool {
	return r.Version == 1 && r.Scope == "workspace-history-import-v1" && !r.CanResumeAgent && validColdSession(r.SessionID) && r.SessionID == r.Source.SessionID && coldMigrationID(r.MigrationID) && coldHex(r.BindingSHA256) && ValidateColdHistoryProof(r.Source) == nil && filepath.IsAbs(r.Root) && filepath.Clean(r.Root) == r.Root && r.Root != "/" && len(r.RootIdentity) > 0 && len(r.RootIdentity) <= 256 && r.ArchiveDirectory == r.SessionID+".workspace-history.cold-source-"+r.MigrationID && r.JournalBytes >= 0 && r.JournalBytes <= maxJournalBytes && r.RestoreBytes >= 0 && r.RestoreBytes <= maxRestoreEncodedBytes && (r.JournalSHA256 == "" && r.JournalBytes == 0 || coldHex(r.JournalSHA256)) && (r.RestoreSHA256 == "" && r.RestoreBytes == 0 || coldHex(r.RestoreSHA256))
}

func coldReadOrigin(files map[string][]byte) (*ColdHistoryReceipt, error) {
	raw, found := files[coldOriginFile]
	if !found {
		return nil, nil
	}
	var r ColdHistoryReceipt
	if len(raw) > 16384 || coldDecode(raw, &r) != nil || !coldReceiptValid(r) {
		return nil, fmt.Errorf("invalid history origin receipt")
	}
	return &r, nil
}

func verifyColdOriginChain(parent *os.Root, current *coldState, actual *RevisionJournal) error {
	return verifyColdOriginChainBounded(parent, current, actual, 16)
}

func verifyColdOriginChainBounded(parent *os.Root, current *coldState, actual *RevisionJournal, limit int) error {
	seen := map[string]bool{}
	total := 0
	for depth := 0; depth <= limit; depth++ {
		r, err := coldReadOrigin(current.files)
		if err != nil {
			return err
		}
		if r == nil {
			if current.journal.coldMigrationID != "" || current.evolution != nil {
				return fmt.Errorf("history migration origin missing")
			}
			return nil
		}
		if r.Root != current.journal.rootPath || r.RootIdentity != current.journal.rootIdentity || r.SessionID != current.journal.sessionID || current.journal.frameSequence != 0 && current.journal.coldMigrationID != r.MigrationID || seen[r.ArchiveDirectory] {
			return fmt.Errorf("history origin identity mismatch")
		}
		seen[r.ArchiveDirectory] = true
		journal := current.files[journalFile]
		if int64(len(journal)) < r.JournalBytes || r.JournalSHA256 != "" && coldSHA(journal[:r.JournalBytes]) != r.JournalSHA256 {
			return fmt.Errorf("imported history prefix changed")
		}
		files, err := readColdFiles(parent, r.ArchiveDirectory)
		if err != nil || !coldMatchFiles(files, r.Source) {
			return fmt.Errorf("original history archive unavailable")
		}
		for _, b := range files {
			total += len(b)
		}
		if total > 64<<20 {
			return fmt.Errorf("history origin chain exceeds limit")
		}
		s, err := decodeColdState(files, r.Source.Root, r.Source.RootIdentity, r.Source.SessionID, actual)
		if err != nil || s.semanticSHA() != r.Source.SemanticSHA256 || s.journal.frameSequence != r.Source.FrameSequence {
			return fmt.Errorf("original history semantic binding mismatch")
		}
		normalized, err := coldNormalized(s, r.Root, r.RootIdentity, r.MigrationID)
		if err != nil || coldFileSHA(normalized, journalFile) != r.JournalSHA256 || int64(len(normalized[journalFile])) != r.JournalBytes || coldFileSHA(normalized, restoreOperationFile) != r.RestoreSHA256 || int64(len(normalized[restoreOperationFile])) != r.RestoreBytes {
			return fmt.Errorf("history normalization receipt mismatch")
		}
		if err := current.journal.verifyRestoreExtension(normalized, current.files, r.Source.FrameSequence); err != nil {
			return err
		}
		current = s
	}
	return fmt.Errorf("history origin chain too deep")
}

func coldExactImported(files map[string][]byte, r ColdHistoryReceipt) bool {
	return coldFileSHA(files, journalFile) == r.JournalSHA256 && int64(len(files[journalFile])) == r.JournalBytes && coldFileSHA(files, restoreOperationFile) == r.RestoreSHA256 && int64(len(files[restoreOperationFile])) == r.RestoreBytes
}

func coldMapsEqual(a, b map[string][]byte) bool { return reflect.DeepEqual(a, b) }

// checkColdOriginLocked also protects ordinary opens, query admission and
// writes after import. Only the immutable imported prefix is required to be
// closed; a later live turn is allowed to use the normal journal API.
func (j *RevisionJournal) checkColdOriginLocked() error {
	_, err := os.Lstat(filepath.Join(j.dir, coldOriginFile))
	if os.IsNotExist(err) && j.coldMigrationID == "" && j.coldOriginSHA256 == "" {
		return nil
	}
	if err != nil {
		return fmt.Errorf("history migration origin unavailable")
	}
	parent, err := os.OpenRoot(filepath.Dir(j.dir))
	if err != nil {
		return err
	}
	defer parent.Close()
	dir, err := parent.OpenRoot(filepath.Base(j.dir))
	if err != nil {
		return err
	}
	defer dir.Close()
	if _, err := dir.Lstat(restoreOperationTemp); !os.IsNotExist(err) {
		return fmt.Errorf("unpublished imported restore state")
	}
	raw, err := coldReadPrivate(dir, coldOriginFile, 16384)
	if err != nil || j.coldOriginSHA256 != "" && j.coldOriginSHA256 != coldSHA(raw) {
		return fmt.Errorf("history migration origin changed")
	}
	files := map[string][]byte{coldOriginFile: raw}
	journal, err := coldReadPrivate(dir, journalFile, maxJournalBytes)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		files[journalFile] = journal
	}
	restore, err := coldReadPrivate(dir, restoreOperationFile, maxRestoreEncodedBytes)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		files[restoreOperationFile] = restore
	}
	origin, err := coldReadOrigin(files)
	if err != nil || origin == nil || j.frameSequence != 0 && origin.MigrationID != j.coldMigrationID {
		return fmt.Errorf("history migration origin identity mismatch")
	}
	state := &coldState{files: files, journal: j}
	if err := verifyColdOriginChain(parent, state, j); err != nil {
		return err
	}
	// No unknown state is accepted by learning a new origin in a live owner.
	if j.coldOriginSHA256 == "" {
		j.coldOriginSHA256, j.coldMigrationID = coldSHA(raw), origin.MigrationID
	}
	return nil
}

// ColdOrigin reads only the receipt corresponding to an already observed
// origin digest. It does not activate or repair the stored transaction.
func (j *RevisionJournal) ColdOrigin(expectedSHA256 string) (*ColdHistoryReceipt, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.checkRootIdentity(); err != nil {
		return nil, err
	}
	parent, err := os.OpenRoot(filepath.Dir(j.dir))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	files, err := readColdFiles(parent, filepath.Base(j.dir))
	if err != nil {
		return nil, err
	}
	if coldFileSHA(files, coldOriginFile) != expectedSHA256 {
		return nil, fmt.Errorf("history origin changed")
	}
	return coldReadOrigin(files)
}
