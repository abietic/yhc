package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/abietic/yhc/internal/identity"
	"github.com/abietic/yhc/internal/statemigration"
	"github.com/abietic/yhc/internal/statepath"
)

const (
	sessionCatalogVersion = 1
	catalogLockTimeout    = 10 * time.Second
)

// SessionRoot identifies one project-local transcript store. The catalog makes
// project-local persistence discoverable without recursively scanning the home
// directory whenever the resume picker opens.
type SessionRoot struct {
	CWD           string    `json:"cwd"`
	TranscriptDir string    `json:"transcript_dir"`
	RepositoryKey string    `json:"repository_key,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type sessionCatalogFile struct {
	Version int           `json:"version"`
	Roots   []SessionRoot `json:"roots"`
}

// DefaultCatalogPaths returns the effective writable catalog and, only for a
// default-root selection, the immutable legacy catalog eligible for read-only
// discovery. A non-empty explicit override is exact and disables the legacy
// union.
func DefaultCatalogPaths() (string, string) {
	pair := identity.RuntimeEnvSessionCatalog.Pair()
	value, _, present := identity.LookupEnv(pair)
	if present && strings.TrimSpace(value) != "" {
		if !filepath.IsAbs(value) || strings.ContainsRune(value, '\x00') {
			return "", ""
		}
		return value, ""
	}

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", ""
	}
	roots, err := statepath.UserRoots(home)
	if err != nil {
		return "", ""
	}
	defaults := statepath.Roots{
		Canonical: filepath.Join(roots.Canonical, "session-roots.json"),
		Legacy:    filepath.Join(roots.Legacy, "session-roots.json"),
	}
	return defaults.Canonical, defaults.Legacy
}

// DefaultCatalogPath returns the effective user-level catalog used by CLI
// entrypoints. Tests and embedded callers can leave
// QueryEngineConfig.SessionCatalogPath empty to avoid user-level writes.
func DefaultCatalogPath() string {
	effective, _ := DefaultCatalogPaths()
	return effective
}

// RegisterSessionRoot atomically records or refreshes a transcript store.
func RegisterSessionRoot(catalogPath, cwd, transcriptDir string, now time.Time) error {
	if strings.TrimSpace(catalogPath) == "" || strings.TrimSpace(transcriptDir) == "" {
		return nil
	}
	canonicalCWD, err := canonicalPath(cwd)
	if err != nil {
		return fmt.Errorf("canonicalize session cwd: %w", err)
	}
	canonicalTranscriptDir, err := canonicalPath(transcriptDir)
	if err != nil {
		return fmt.Errorf("canonicalize transcript dir: %w", err)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	root := SessionRoot{
		CWD:           canonicalCWD,
		TranscriptDir: canonicalTranscriptDir,
		RepositoryKey: repositoryKey(canonicalCWD),
		UpdatedAt:     now.UTC(),
	}

	return withSessionCatalogLocked(catalogPath, func(catalog *sessionCatalogTransaction) error {
		catalog.upsert(root)
		return catalog.commit(sessionCatalogWriteHooks{})
	})
}

type sessionCatalogTransaction struct {
	path     string
	fileName string
	store    *statemigration.CanonicalStore
	roots    []SessionRoot
}

type sessionCatalogWriteHooks struct {
	afterReplace    func() error
	afterParentSync func() error
}

func withSessionCatalogLocked(
	catalogPath string,
	operation func(*sessionCatalogTransaction) error,
) error {
	if strings.TrimSpace(catalogPath) == "" || operation == nil {
		return errors.New("session catalog transaction is invalid")
	}
	if !filepath.IsAbs(catalogPath) || filepath.Clean(catalogPath) != catalogPath {
		return errors.New("session catalog path is invalid")
	}
	store, exists, secureErr := statemigration.OpenCanonicalStore(
		filepath.Dir(catalogPath),
		".",
		true,
	)
	if secureErr == nil && exists {
		defer store.Close() //nolint:errcheck
		return withSessionCatalogStoreLocked(
			context.Background(), catalogPath, store, operation,
		)
	}
	parentInfo, statErr := os.Lstat(filepath.Dir(catalogPath))
	if statErr != nil || parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
		return fmt.Errorf("open private session catalog directory: %w", secureErr)
	}
	unlock, err := acquireCatalogLock(catalogPath+".lock", catalogLockTimeout)
	if err != nil {
		return err
	}
	defer unlock()
	roots, err := LoadSessionRoots(catalogPath)
	if err != nil {
		return err
	}
	return operation(&sessionCatalogTransaction{
		path: catalogPath, fileName: filepath.Base(catalogPath),
		roots: append([]SessionRoot(nil), roots...),
	})
}

func withPrivateSessionCatalogLocked(
	ctx context.Context,
	catalogPath string,
	operation func(*sessionCatalogTransaction) error,
) error {
	if strings.TrimSpace(catalogPath) == "" || operation == nil {
		return errors.New("session catalog transaction is invalid")
	}
	if !filepath.IsAbs(catalogPath) || filepath.Clean(catalogPath) != catalogPath {
		return errors.New("session catalog path is invalid")
	}
	store, exists, err := statemigration.OpenCanonicalStore(
		filepath.Dir(catalogPath),
		".",
		true,
	)
	if err != nil || !exists {
		return fmt.Errorf("open private session catalog directory: %w", err)
	}
	defer store.Close() //nolint:errcheck
	return withSessionCatalogStoreLocked(ctx, catalogPath, store, operation)
}

func withSessionCatalogStoreLocked(
	ctx context.Context,
	catalogPath string,
	store *statemigration.CanonicalStore,
	operation func(*sessionCatalogTransaction) error,
) error {
	fileName := filepath.Base(catalogPath)
	unlock, err := store.Lock(ctx, fileName+".lock")
	if err != nil {
		return err
	}
	defer unlock()

	roots, err := loadSessionRootsFromStore(store, fileName)
	if err != nil {
		return err
	}
	return operation(&sessionCatalogTransaction{
		path: catalogPath, fileName: fileName, store: store,
		roots: append([]SessionRoot(nil), roots...),
	})
}

func (catalog *sessionCatalogTransaction) upsert(root SessionRoot) {
	replaced := false
	for index := range catalog.roots {
		if samePath(catalog.roots[index].TranscriptDir, root.TranscriptDir) {
			catalog.roots[index] = root
			replaced = true
			break
		}
	}
	if !replaced {
		catalog.roots = append(catalog.roots, root)
	}
	sort.Slice(catalog.roots, func(i, j int) bool {
		if !catalog.roots[i].UpdatedAt.Equal(catalog.roots[j].UpdatedAt) {
			return catalog.roots[i].UpdatedAt.After(catalog.roots[j].UpdatedAt)
		}
		return catalog.roots[i].TranscriptDir < catalog.roots[j].TranscriptDir
	})
}

func (catalog *sessionCatalogTransaction) containsRootIdentity(root SessionRoot) bool {
	for _, candidate := range catalog.roots {
		if sameSessionRootIdentity(candidate, root) {
			return true
		}
	}
	return false
}

func (catalog *sessionCatalogTransaction) loadPrivateRoots() ([]SessionRoot, error) {
	if catalog == nil || catalog.store == nil || catalog.fileName == "" {
		return nil, errors.New("private session catalog store is unavailable")
	}
	return loadSessionRootsFromStore(catalog.store, catalog.fileName)
}

func sameSessionRootIdentity(left, right SessionRoot) bool {
	return samePath(left.CWD, right.CWD) &&
		samePath(left.TranscriptDir, right.TranscriptDir) &&
		left.RepositoryKey == right.RepositoryKey
}

func (catalog *sessionCatalogTransaction) commit(hooks sessionCatalogWriteHooks) error {
	if catalog.store == nil {
		return writeSessionCatalogWithHooks(catalog.path, catalog.roots, hooks)
	}
	return writeSessionCatalogToStore(catalog.store, catalog.fileName, catalog.roots, hooks)
}

// LoadSessionRoots returns a defensive copy of the registered transcript roots.
func LoadSessionRoots(catalogPath string) ([]SessionRoot, error) {
	if strings.TrimSpace(catalogPath) == "" {
		return nil, nil
	}
	info, err := os.Lstat(catalogPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("inspect session catalog: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("session catalog is not a regular file")
	}
	file, err := os.Open(catalogPath)
	if err != nil {
		return nil, fmt.Errorf("read session catalog: %w", err)
	}
	defer file.Close() //nolint:errcheck
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("session catalog changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSessionImportJSONBytes+1))
	if err != nil || len(data) > maxSessionImportJSONBytes {
		return nil, errors.New("session catalog exceeds its read budget")
	}
	current, err := os.Lstat(catalogPath)
	if err != nil || !os.SameFile(info, current) {
		return nil, errors.New("session catalog changed while reading")
	}
	return decodeSessionCatalog(data)
}

func loadSessionRootsFromStore(
	store *statemigration.CanonicalStore,
	fileName string,
) ([]SessionRoot, error) {
	file, info, exists, err := store.OpenRegular(fileName, os.O_RDONLY, false)
	if err != nil || !exists {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSessionImportJSONBytes+1))
	closeErr := file.Close()
	if err != nil || closeErr != nil || len(data) > maxSessionImportJSONBytes ||
		store.ValidateRegular(fileName, info) != nil {
		return nil, errors.New("session catalog changed while reading")
	}
	return decodeSessionCatalog(data)
}

func decodeSessionCatalog(data []byte) ([]SessionRoot, error) {
	var catalog sessionCatalogFile
	if err := json.Unmarshal(data, &catalog); err != nil {
		return nil, fmt.Errorf("decode session catalog: %w", err)
	}
	if catalog.Version != sessionCatalogVersion {
		return nil, fmt.Errorf("unsupported session catalog version %d", catalog.Version)
	}
	return append([]SessionRoot(nil), catalog.Roots...), nil
}

func writeSessionCatalogToStore(
	store *statemigration.CanonicalStore,
	fileName string,
	roots []SessionRoot,
	hooks sessionCatalogWriteHooks,
) error {
	data, err := json.MarshalIndent(sessionCatalogFile{Version: sessionCatalogVersion, Roots: roots}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session catalog: %w", err)
	}
	token, err := newSessionImportToken()
	if err != nil {
		return err
	}
	tempName := "." + fileName + ".tmp-" + token
	temp, info, exists, err := store.OpenRegular(tempName, os.O_WRONLY, true)
	if err != nil || !exists {
		return errors.New("create session catalog temp file")
	}
	cleanup := true
	defer func() {
		if temp != nil {
			_ = temp.Close()
		}
		if cleanup {
			store.RemoveRegularIfSame(tempName, info)
			_ = store.Sync()
		}
	}()
	if err := writeAllSessionImport(temp, data); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		temp = nil
		return err
	}
	temp = nil
	if err := store.PromoteRegular(tempName, fileName, info); err != nil {
		return fmt.Errorf("replace session catalog: %w", err)
	}
	cleanup = false
	if hooks.afterReplace != nil {
		if err := hooks.afterReplace(); err != nil {
			return err
		}
	}
	if hooks.afterParentSync != nil {
		if err := hooks.afterParentSync(); err != nil {
			return err
		}
	}
	return nil
}

func writeSessionCatalogWithHooks(
	path string,
	roots []SessionRoot,
	hooks sessionCatalogWriteHooks,
) error {
	data, err := json.MarshalIndent(sessionCatalogFile{Version: sessionCatalogVersion, Roots: roots}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session catalog: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".session-roots-*.tmp")
	if err != nil {
		return fmt.Errorf("create session catalog temp file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath) //nolint:errcheck
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if err := writeAllSessionImport(temp, data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		if runtime.GOOS != "windows" {
			return fmt.Errorf("replace session catalog: %w", err)
		}
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("replace session catalog: %w", err)
		}
		if retryErr := os.Rename(tempPath, path); retryErr != nil {
			return fmt.Errorf("replace session catalog: %w", retryErr)
		}
	}
	if hooks.afterReplace != nil {
		if err := hooks.afterReplace(); err != nil {
			return err
		}
	}
	if err := syncSessionCatalogDirectory(filepath.Dir(path)); err != nil {
		return fmt.Errorf("sync session catalog directory: %w", err)
	}
	if hooks.afterParentSync != nil {
		if err := hooks.afterParentSync(); err != nil {
			return err
		}
	}
	return nil
}

func syncSessionCatalogDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close() //nolint:errcheck
	return directory.Sync()
}

func acquireCatalogLock(path string, staleAfter time.Duration) (func(), error) {
	for attempt := 0; attempt < 20; attempt++ {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = fmt.Fprintf(file, "%d", os.Getpid())
			_ = file.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("lock session catalog: %w", err)
		}
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > staleAfter {
			_ = os.Remove(path)
			continue
		}
		time.Sleep(25 * time.Millisecond)
	}
	return nil, errors.New("timed out locking session catalog")
}

func canonicalPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		path = "."
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
		return filepath.Clean(resolved), nil
	}
	return abs, nil
}

func samePath(left, right string) bool {
	return filepath.Clean(left) == filepath.Clean(right)
}

func repositoryKey(cwd string) string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--git-common-dir")
	command.Stderr = nil
	output, err := command.Output()
	if err != nil {
		return ""
	}
	path := strings.TrimSpace(string(output))
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	canonical, err := canonicalPath(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return canonical
}
