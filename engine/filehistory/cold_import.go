package filehistory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
)

type coldImportMarker struct {
	Version      int               `json:"version"`
	Request      ColdHistoryImport `json:"request"`
	Root         string            `json:"root"`
	RootIdentity string            `json:"root_identity"`
}

// CheckColdHistoryGate is shared by ordinary journal admission and opt-in
// proofs, including when the active history directory is not present.
func CheckColdHistoryGate(dir string) error {
	_, err := os.Lstat(dir + ".cold-import.json")
	if os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR) {
		// No child marker can exist below a regular file. Preserve the ordinary
		// transcript owner's existing failure/repair path for a new Session.
		return nil
	}
	return fmt.Errorf("workspace history cold import is incomplete")
}

func coldReadPrivate(parent *os.Root, name string, limit int64) ([]byte, error) {
	info, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !restoreFileSingleLink(info) || info.Size() > limit {
		return nil, fmt.Errorf("invalid history migration control file")
	}
	f, err := parent.Open(name)
	if err != nil {
		return nil, err
	}
	opened, statErr := f.Stat()
	raw, readErr := io.ReadAll(io.LimitReader(f, limit+1))
	ended, endErr := f.Stat()
	closeErr := f.Close()
	current, currentErr := parent.Lstat(name)
	if statErr != nil || readErr != nil || endErr != nil || closeErr != nil || currentErr != nil || !os.SameFile(info, opened) || !os.SameFile(opened, ended) || !os.SameFile(ended, current) || !sameCaptureMeta(info, ended) || !sameCaptureMeta(ended, current) || int64(len(raw)) != info.Size() || int64(len(raw)) > limit {
		return nil, fmt.Errorf("migration control file changed")
	}
	return raw, nil
}

func coldWritePrivate(parent *os.Root, name string, raw []byte) error {
	f, err := parent.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	n, writeErr := f.Write(raw)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || n != len(raw) || syncErr != nil || closeErr != nil {
		return fmt.Errorf("history migration write incomplete")
	}
	return nil
}

func coldSync(parent *os.Root) error {
	f, err := parent.Open(".")
	if err != nil {
		return err
	}
	syncErr := f.Sync()
	closeErr := f.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

// ImportColdState is an explicit, provider-free filesystem transaction. The
// caller must hold its admitted closed-workspace epoch and retain SourceSHA256
// and BindingSHA256 outside the task. It never resumes a restore or an agent.
// Root rebinding is local to these relative file records. SessionService owns
// the stricter unchanged logical CWD contract for native Session continuation.
func ImportColdState(ctx context.Context, root, dir, session string, request ColdHistoryImport) (ColdHistoryReceipt, error) {
	return importColdState(ctx, root, dir, session, request, nil)
}

func ValidateColdHistoryImport(request ColdHistoryImport) error {
	if request.Version != 1 || !coldMigrationID(request.MigrationID) || !coldHex(request.BindingSHA256) || request.SourceSHA256 != request.Source.SHA256 || ValidateColdHistoryProof(request.Source) != nil {
		return fmt.Errorf("invalid cold history import request")
	}
	return nil
}

func importColdState(ctx context.Context, root, dir, session string, request ColdHistoryImport, fault func(string) error) (ColdHistoryReceipt, error) {
	var zero ColdHistoryReceipt
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if !restoreFilesystemSupported() || request.Source.SessionID != session || !validColdSession(session) || ValidateColdHistoryImport(request) != nil {
		return zero, fmt.Errorf("invalid cold history import request")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || canonical != root || !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(filepath.Separator) || len(root) > 2048 || filepath.Base(dir) != session+".workspace-history" || filepath.Clean(dir) != dir {
		return zero, fmt.Errorf("unverified history import root")
	}
	parentPath := filepath.Dir(dir)
	rel, err := filepath.Rel(root, parentPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return zero, fmt.Errorf("history import outside workspace")
	}
	resolved, err := filepath.EvalSymlinks(parentPath)
	if err != nil || resolved != parentPath {
		return zero, fmt.Errorf("history import parent is not canonical")
	}
	workspace, err := os.OpenRoot(root)
	if err != nil {
		return zero, err
	}
	defer workspace.Close()
	identity, err := durableRootIdentity(workspace)
	if err != nil {
		return zero, err
	}
	parent, err := workspace.OpenRoot(rel)
	if err != nil {
		return zero, err
	}
	defer parent.Close()
	parentInfo, err := parent.Stat(".")
	if err != nil || parentInfo.Mode().Perm()&0o022 != 0 {
		return zero, fmt.Errorf("history import parent is writable by others")
	}
	parentLock, err := parent.Open(".")
	if err != nil {
		return zero, err
	}
	defer parentLock.Close()
	unlock, err := lockRestoreDirectory(parentLock)
	if err != nil {
		return zero, err
	}
	defer unlock()
	validateRoot := func() error {
		current, err := os.Lstat(filepath.Clean(root))
		opened, openErr := workspace.Stat(".")
		currentParent, parentErr := os.Lstat(filepath.Clean(parentPath))
		boundParent, boundErr := parent.Stat(".")
		now, idErr := durableRootIdentity(workspace)
		if err != nil || openErr != nil || parentErr != nil || boundErr != nil || idErr != nil || !os.SameFile(current, opened) || !os.SameFile(currentParent, boundParent) || !os.SameFile(parentInfo, boundParent) || now != identity {
			return fmt.Errorf("history import namespace changed")
		}
		return nil
	}
	if err := validateRoot(); err != nil {
		return zero, err
	}
	base := filepath.Base(dir)
	markerName := base + ".cold-import.json"
	archive := base + ".cold-source-" + request.MigrationID
	markerRaw, markerErr := coldReadPrivate(parent, markerName, 16384)
	markerExists := markerErr == nil
	if markerErr != nil && !os.IsNotExist(markerErr) {
		return zero, markerErr
	}
	marker := coldImportMarker{Version: 1, Request: request, Root: root, RootIdentity: identity}
	if markerExists {
		var stored coldImportMarker
		if coldDecode(markerRaw, &stored) != nil || !reflect.DeepEqual(stored, marker) {
			return zero, fmt.Errorf("cold import request or target changed")
		}
	}
	active, err := readColdFiles(parent, base)
	if err != nil {
		return zero, err
	}
	activeDir, err := parent.OpenRoot(base)
	if err != nil {
		return zero, err
	}
	defer activeDir.Close()
	activeLock, err := activeDir.Open(".")
	if err != nil {
		return zero, err
	}
	defer activeLock.Close()
	activeUnlock, err := lockRestoreDirectory(activeLock)
	if err != nil {
		return zero, err
	}
	defer activeUnlock()
	activeAgain, err := readColdFiles(parent, base)
	if err != nil || !coldMapsEqual(active, activeAgain) {
		return zero, fmt.Errorf("history changed before lock")
	}
	validator := &RevisionJournal{rootPath: root, rootIdentity: identity, root: workspace, dir: dir, sessionID: session, coldImportValidation: true}
	if !markerExists {
		origin, originErr := coldReadOrigin(active)
		if originErr != nil {
			return zero, originErr
		}
		if origin != nil && origin.MigrationID == request.MigrationID && origin.BindingSHA256 == request.BindingSHA256 && origin.Source.SHA256 == request.SourceSHA256 {
			if origin.Root != root || origin.RootIdentity != identity || !coldExactImported(active, *origin) {
				return zero, fmt.Errorf("history advanced after import")
			}
			view, err := decodeColdState(active, root, identity, session, validator)
			if err != nil || view.semanticSHA() != request.Source.SemanticSHA256 {
				return zero, fmt.Errorf("imported history changed")
			}
			if err := view.verifyFiles(validator); err != nil {
				return zero, err
			}
			if err := verifyColdOriginChain(parent, view, validator); err != nil {
				return zero, err
			}
			return *origin, nil
		}
	}
	sourceFiles := active
	if !coldMatchFiles(sourceFiles, request.Source) {
		if !markerExists {
			return zero, fmt.Errorf("history source bytes or origin changed")
		}
		sourceFiles, err = readColdFiles(parent, archive)
		if err != nil || !coldMatchFiles(sourceFiles, request.Source) {
			return zero, fmt.Errorf("cold import source cannot be reconciled")
		}
	}
	source, err := decodeColdState(sourceFiles, request.Source.Root, request.Source.RootIdentity, session, validator)
	if err != nil || source.semanticSHA() != request.Source.SemanticSHA256 || source.journal.frameSequence != request.Source.FrameSequence {
		return zero, fmt.Errorf("cold history source proof mismatch")
	}
	if source.proof(request.Source.Root, request.Source.RootIdentity) != request.Source {
		return zero, fmt.Errorf("cold source metadata mismatch")
	}
	if err := source.verifyFiles(validator); err != nil {
		return zero, err
	}
	if err := verifyColdOriginChainBounded(parent, source, validator, 15); err != nil {
		return zero, err
	}
	normalized, err := coldNormalized(source, root, identity, request.MigrationID)
	if err != nil {
		return zero, err
	}
	receipt := ColdHistoryReceipt{Version: 1, Scope: "workspace-history-import-v1", SessionID: session, MigrationID: request.MigrationID, BindingSHA256: request.BindingSHA256, Source: request.Source, Root: root, RootIdentity: identity, JournalSHA256: coldFileSHA(normalized, journalFile), JournalBytes: int64(len(normalized[journalFile])), RestoreSHA256: coldFileSHA(normalized, restoreOperationFile), RestoreBytes: int64(len(normalized[restoreOperationFile])), ArchiveDirectory: archive}
	originRaw, err := json.Marshal(receipt)
	if err != nil || len(originRaw) > 16384 {
		return zero, fmt.Errorf("cold origin exceeds limit")
	}
	normalized[coldOriginFile] = originRaw
	checkpoint := func(stage string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if fault != nil {
			return fault(stage)
		}
		return nil
	}
	if !markerExists {
		if _, err := parent.Lstat(archive); !os.IsNotExist(err) {
			return zero, fmt.Errorf("cold archive namespace occupied")
		}
		markerRaw, err = json.Marshal(marker)
		if err != nil || len(markerRaw) > 16384 {
			return zero, fmt.Errorf("cold marker exceeds limit")
		}
		if err := coldWritePrivate(parent, markerName, markerRaw); err != nil {
			return zero, err
		}
		if err := coldSync(parent); err != nil {
			return zero, err
		}
		if err := checkpoint("marker_published"); err != nil {
			return zero, err
		}
	}
	if coldMapsEqual(active, sourceFiles) {
		stage, stageErr := readColdFiles(parent, archive)
		if os.IsNotExist(stageErr) {
			if err := parent.Mkdir(archive, 0o700); err != nil {
				return zero, err
			}
			staging, err := parent.OpenRoot(archive)
			if err != nil {
				return zero, err
			}
			for _, name := range []string{journalFile, restoreOperationFile, coldOriginFile} {
				if raw, ok := normalized[name]; ok {
					if err := coldWritePrivate(staging, name, raw); err != nil {
						staging.Close()
						return zero, err
					}
				}
			}
			syncErr := coldSync(staging)
			closeErr := staging.Close()
			if syncErr != nil || closeErr != nil {
				return zero, fmt.Errorf("cold stage sync incomplete")
			}
			stage, stageErr = readColdFiles(parent, archive)
		}
		if stageErr != nil || !coldMapsEqual(stage, normalized) {
			return zero, fmt.Errorf("cold stage incomplete or changed")
		}
		if err := validateRoot(); err != nil {
			return zero, err
		}
		fresh, err := readColdFiles(parent, base)
		if err != nil || !coldMapsEqual(fresh, sourceFiles) {
			return zero, fmt.Errorf("history changed before exchange")
		}
		if err := checkpoint("before_exchange"); err != nil {
			return zero, err
		}
		if err := coldExchange(int(parentLock.Fd()), base, archive); err != nil {
			return zero, err
		}
		if err := checkpoint("after_exchange"); err != nil {
			return zero, err
		}
	} else if !coldMapsEqual(active, normalized) {
		return zero, fmt.Errorf("ambiguous cold history exchange")
	}
	if err := coldSync(parent); err != nil {
		return zero, err
	}
	if err := checkpoint("parent_synced"); err != nil {
		return zero, err
	}
	current, err := readColdFiles(parent, base)
	original, oldErr := readColdFiles(parent, archive)
	if err != nil || oldErr != nil || !coldMapsEqual(current, normalized) || !coldMapsEqual(original, sourceFiles) || validateRoot() != nil {
		return zero, fmt.Errorf("cold exchange verification failed")
	}
	stored, err := coldReadPrivate(parent, markerName, 16384)
	if err != nil || !reflect.DeepEqual(stored, markerRaw) {
		return zero, fmt.Errorf("cold marker changed")
	}
	if err := checkpoint("before_marker_remove"); err != nil {
		return zero, err
	}
	if err := parent.Remove(markerName); err != nil {
		return zero, err
	}
	if err := coldSync(parent); err != nil {
		_ = coldWritePrivate(parent, markerName, markerRaw)
		_ = coldSync(parent)
		return zero, fmt.Errorf("cold marker removal durability uncertain")
	}
	return receipt, nil
}
