package mediastore

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash/fnv"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/abietic/yhc/engine/internal/mediaimage"
)

const (
	manifestFilename = "manifest.json"
	blobDirectory    = "blobs"
	digestDirectory  = "sha256"
)

// Step names one deterministic filesystem boundary for fault injection.
type Step string

const (
	StepCreateRoot         Step = "create_root"
	StepCreateBlobDir      Step = "create_blob_dir"
	StepCreateDigestDir    Step = "create_digest_dir"
	StepCreatePrefixDir    Step = "create_prefix_dir"
	StepCreateBlobTemp     Step = "create_blob_temp"
	StepWriteBlob          Step = "write_blob"
	StepSyncBlob           Step = "sync_blob"
	StepCloseBlob          Step = "close_blob"
	StepPublishBlob        Step = "publish_blob"
	StepValidateBlob       Step = "validate_blob"
	StepRemoveBlobTemp     Step = "remove_blob_temp"
	StepSyncBlobDir        Step = "sync_blob_dir"
	StepReadManifest       Step = "read_manifest"
	StepCreateManifestTemp Step = "create_manifest_temp"
	StepWriteManifest      Step = "write_manifest"
	StepSyncManifest       Step = "sync_manifest"
	StepCloseManifest      Step = "close_manifest"
	StepReplaceManifest    Step = "replace_manifest"
	StepSyncRoot           Step = "sync_root"
	StepOpenBlob           Step = "open_blob"
	StepReadBlob           Step = "read_blob"
	StepCollectScan        Step = "collect_scan"
	StepCollectPrecommit   Step = "collect_precommit"
	StepCollectRemoveBlob  Step = "collect_remove_blob"
	StepCollectSyncBlobDir Step = "collect_sync_blob_dir"
)

type faultHook func(Step) error

const storeLockStripeCount = 64

var storeLocks [storeLockStripeCount]sync.Mutex

// Store owns one session-private media sidecar.
type Store struct {
	root string
	mu   *sync.Mutex
	hook faultHook
}

// New returns a session store rooted at the exact transcript sidecar path.
func New(root string) *Store {
	return newWithHook(root, nil)
}

func newWithHook(root string, hook faultHook) *Store {
	clean := filepath.Clean(root)
	if root == "" || clean == "." {
		clean = ""
	} else if absolute, err := filepath.Abs(clean); err == nil {
		clean = filepath.Clean(absolute)
	}
	return &Store{
		root: clean,
		mu:   storeLockFor(clean),
		hook: hook,
	}
}

func storeLockFor(root string) *sync.Mutex {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(root))
	return &storeLocks[hash.Sum32()%storeLockStripeCount]
}

func (s *Store) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}

// Put durably publishes bytes and their manifest entry before returning the
// opaque Ref that a transcript may publish.
func (s *Store) Put(
	ctx context.Context,
	data []byte,
	metadata Metadata,
) (Ref, error) {
	if s == nil || s.root == "" || s.root == "." {
		return Ref{}, bounded(CategoryStoreUnavailable, "put", nil)
	}
	if err := contextError(ctx); err != nil {
		return Ref{}, err
	}
	if err := metadata.Validate(int64(len(data))); err != nil {
		return Ref{}, err
	}
	owned := append([]byte(nil), data...)
	defer clear(owned)
	data = owned
	if err := validateStoredImage(data, metadata); err != nil {
		return Ref{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := contextError(ctx); err != nil {
		return Ref{}, err
	}
	root, err := s.ensureLayout()
	if err != nil {
		return Ref{}, err
	}
	defer root.Close() //nolint:errcheck

	digestBytes := sha256.Sum256(data)
	digest := hex.EncodeToString(digestBytes[:])
	prefixPath := filepath.Join(
		blobDirectory,
		digestDirectory,
		digest[:2],
	)
	if err := s.ensurePrivateDir(
		root,
		prefixPath,
		StepCreatePrefixDir,
	); err != nil {
		return Ref{}, err
	}
	prefixRoot, err := openPrivateSubroot(root, prefixPath)
	if err != nil {
		return Ref{}, err
	}
	defer prefixRoot.Close() //nolint:errcheck

	mediaID, err := newMediaID()
	if err != nil {
		return Ref{}, bounded(CategoryStoreUnavailable, "create media identity", err)
	}
	entry := Entry{
		Digest:    digest,
		SizeBytes: int64(len(data)),
		MIMEType:  metadata.MIMEType,
		Width:     metadata.Width,
		Height:    metadata.Height,
		Kind:      metadata.Kind,
	}

	tempName, err := s.writeBlobTemp(ctx, prefixRoot, mediaID, data)
	if err != nil {
		return Ref{}, err
	}
	defer prefixRoot.Remove(tempName) //nolint:errcheck

	existing, err := s.publishBlob(prefixRoot, tempName, digest)
	if err != nil {
		return Ref{}, err
	}
	if existing {
		if err := s.validateBlob(ctx, prefixRoot, digest, entry); err != nil {
			return Ref{}, err
		}
		if err := s.before(StepRemoveBlobTemp); err != nil {
			return Ref{}, err
		}
		if err := prefixRoot.Remove(tempName); err != nil &&
			!errors.Is(err, os.ErrNotExist) {
			return Ref{}, bounded(
				CategoryDurabilityUncertain,
				"remove duplicate blob staging file",
				err,
			)
		}
	}
	if err := s.before(StepSyncBlobDir); err != nil {
		return Ref{}, err
	}
	if err := syncRootDirectory(prefixRoot); err != nil {
		return Ref{}, bounded(
			CategoryDurabilityUncertain,
			"sync blob directory",
			err,
		)
	}
	if err := subrootStillBound(root, prefixPath, prefixRoot); err != nil {
		return Ref{}, err
	}
	if err := contextError(ctx); err != nil {
		return Ref{}, err
	}

	manifest, err := s.readManifestFrom(ctx, root)
	if err != nil {
		return Ref{}, err
	}
	if len(manifest.Entries) >= MaxManifestEntries {
		return Ref{}, bounded(CategoryManifestLimit, "add manifest entry", nil)
	}
	if _, exists := manifest.Entries[mediaID]; exists {
		return Ref{}, bounded(CategoryStoreUnavailable, "add manifest entry", nil)
	}
	manifest.Entries[mediaID] = entry
	if err := s.writeManifest(root, manifest); err != nil {
		return Ref{}, err
	}
	if err := contextError(ctx); err != nil {
		return Ref{}, err
	}
	if err := rootStillBound(s.root, root); err != nil {
		return Ref{}, err
	}
	return entry.Ref(mediaID), nil
}

// Resolve validates both the public ref and private manifest authority before
// returning a detached byte copy.
func (s *Store) Resolve(ctx context.Context, ref Ref) ([]byte, error) {
	if s == nil || s.root == "" || s.root == "." {
		return nil, bounded(CategoryStoreUnavailable, "resolve", nil)
	}
	if err := ref.Validate(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := contextError(ctx); err != nil {
		return nil, err
	}
	root, err := openPrivateRoot(s.root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, bounded(CategoryMediaMissing, "open store", err)
		}
		return nil, err
	}
	defer root.Close() //nolint:errcheck
	manifest, err := s.readManifestFrom(ctx, root)
	if err != nil {
		return nil, err
	}
	entry, ok := manifest.Entries[ref.MediaID]
	if !ok {
		return nil, bounded(CategoryMediaMissing, "resolve reference", nil)
	}
	if !entry.MatchesRef(ref) {
		return nil, bounded(CategoryMediaCorrupt, "resolve reference", nil)
	}
	prefixPath := filepath.Join(
		blobDirectory,
		digestDirectory,
		entry.Digest[:2],
	)
	prefixRoot, err := openPrivateSubroot(root, prefixPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, bounded(CategoryMediaMissing, "open blob directory", err)
		}
		return nil, err
	}
	defer prefixRoot.Close() //nolint:errcheck
	data, err := s.readRegularFile(
		ctx,
		prefixRoot,
		entry.Digest,
		MaxBlobBytes,
		CategoryMediaCorrupt,
		StepOpenBlob,
		StepReadBlob,
	)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, bounded(CategoryMediaMissing, "read blob", err)
		}
		return nil, err
	}
	if err := validateBlobBytes(data, entry); err != nil {
		clear(data)
		return nil, err
	}
	if err := subrootStillBound(root, prefixPath, prefixRoot); err != nil {
		clear(data)
		return nil, err
	}
	if err := rootStillBound(s.root, root); err != nil {
		clear(data)
		return nil, err
	}
	return data, nil
}

// CopyTo resolves and revalidates one source ref, then publishes an ordinary
// byte copy under a newly minted identity in the target Session store.
func (s *Store) CopyTo(
	ctx context.Context,
	ref Ref,
	target *Store,
) (Ref, error) {
	if target == nil || target.root == "" || target.root == "." {
		return Ref{}, bounded(CategoryStoreUnavailable, "copy media", nil)
	}
	if s == nil || s.root == "" || s.root == "." || s.root == target.root {
		return Ref{}, bounded(CategoryStoreUnavailable, "copy media", nil)
	}
	data, err := s.Resolve(ctx, ref)
	if err != nil {
		return Ref{}, err
	}
	defer clear(data)
	return target.Put(ctx, data, Metadata{
		MIMEType: ref.MIMEType,
		Width:    ref.Width,
		Height:   ref.Height,
		Kind:     "prompt_image",
	})
}

// CollectResult reports one conservative manual collection.
type CollectResult struct {
	ManifestEntriesRemoved int
	BlobsRemoved           int
	BytesRemoved           int64
}

// Collect atomically prunes unreachable manifest entries before unlinking
// blobs that no retained entry can reach. precommit revalidates the caller's
// transcript/coordinator snapshot while the Store mutation lock is held.
func (s *Store) Collect(
	ctx context.Context,
	liveRefs []Ref,
	precommit func() error,
) (CollectResult, error) {
	if s == nil || s.root == "" || s.root == "." {
		return CollectResult{}, bounded(
			CategoryStoreUnavailable,
			"collect media",
			nil,
		)
	}
	if err := contextError(ctx); err != nil {
		return CollectResult{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	root, err := openPrivateRoot(s.root)
	if err != nil {
		return CollectResult{}, err
	}
	defer root.Close() //nolint:errcheck
	manifest, err := s.readManifestFrom(ctx, root)
	if err != nil {
		return CollectResult{}, err
	}

	retained := emptyManifest()
	retainedDigests := make(map[string]struct{})
	for _, ref := range liveRefs {
		if err := ref.Validate(); err != nil {
			return CollectResult{}, err
		}
		entry, exists := manifest.Entries[ref.MediaID]
		if !exists {
			return CollectResult{}, bounded(
				CategoryMediaMissing,
				"collect live reference",
				nil,
			)
		}
		if !entry.MatchesRef(ref) {
			return CollectResult{}, bounded(
				CategoryMediaCorrupt,
				"collect live reference",
				nil,
			)
		}
		prefixPath := filepath.Join(
			blobDirectory,
			digestDirectory,
			entry.Digest[:2],
		)
		prefixRoot, openErr := openPrivateSubroot(root, prefixPath)
		if openErr != nil {
			return CollectResult{}, openErr
		}
		validateErr := s.validateBlob(ctx, prefixRoot, entry.Digest, entry)
		closeErr := prefixRoot.Close()
		if validateErr != nil {
			return CollectResult{}, validateErr
		}
		if closeErr != nil {
			return CollectResult{}, bounded(
				CategoryStoreUnavailable,
				"close live blob directory",
				closeErr,
			)
		}
		retained.Entries[ref.MediaID] = entry
		retainedDigests[entry.Digest] = struct{}{}
	}

	blobs, err := s.collectBlobInventory(root)
	if err != nil {
		return CollectResult{}, err
	}
	if err := s.before(StepCollectPrecommit); err != nil {
		return CollectResult{}, err
	}
	if precommit != nil {
		if err := precommit(); err != nil {
			return CollectResult{}, bounded(
				CategoryStoreUnavailable,
				"revalidate collection",
				err,
			)
		}
	}
	if err := contextError(ctx); err != nil {
		return CollectResult{}, err
	}

	result := CollectResult{
		ManifestEntriesRemoved: len(manifest.Entries) - len(retained.Entries),
	}
	if result.ManifestEntriesRemoved > 0 {
		if err := s.writeManifest(root, retained); err != nil {
			return CollectResult{}, err
		}
	}

	syncedDirs := make(map[string]struct{})
	for _, blob := range blobs {
		if !blob.temporary {
			if _, keep := retainedDigests[blob.digest]; keep {
				continue
			}
		}
		if err := s.before(StepCollectRemoveBlob); err != nil {
			return result, err
		}
		if err := root.Remove(blob.path); err != nil {
			return result, bounded(
				CategoryDurabilityUncertain,
				"remove orphan blob",
				err,
			)
		}
		result.BlobsRemoved++
		result.BytesRemoved += blob.size
		syncedDirs[filepath.Dir(blob.path)] = struct{}{}
	}
	for directory := range syncedDirs {
		subroot, openErr := openPrivateSubroot(root, directory)
		if openErr != nil {
			return result, openErr
		}
		if err := s.before(StepCollectSyncBlobDir); err != nil {
			subroot.Close() //nolint:errcheck
			return result, err
		}
		syncErr := syncRootDirectory(subroot)
		closeErr := subroot.Close()
		if syncErr != nil {
			return result, bounded(
				CategoryDurabilityUncertain,
				"sync collected blob directory",
				syncErr,
			)
		}
		if closeErr != nil {
			return result, bounded(
				CategoryStoreUnavailable,
				"close collected blob directory",
				closeErr,
			)
		}
	}
	if err := rootStillBound(s.root, root); err != nil {
		return result, err
	}
	return result, nil
}

type collectBlobFile struct {
	path      string
	digest    string
	size      int64
	temporary bool
}

func (s *Store) collectBlobInventory(
	root *os.Root,
) ([]collectBlobFile, error) {
	if err := s.before(StepCollectScan); err != nil {
		return nil, err
	}
	const maxCollectEntries = MaxManifestEntries*2 + 512
	start := filepath.Join(blobDirectory, digestDirectory)
	files := make([]collectBlobFile, 0)
	entries := 0
	err := fs.WalkDir(root.FS(), start, func(
		path string,
		entry fs.DirEntry,
		walkErr error,
	) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := root.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return bounded(CategoryUnsafePath, "scan media blobs", nil)
		}
		if entry.IsDir() {
			if err := validateCollectDirectory(path, start, info); err != nil {
				return err
			}
			return nil
		}
		entries++
		if entries > maxCollectEntries ||
			!info.Mode().IsRegular() ||
			info.Mode().Perm()&0o077 != 0 {
			return bounded(CategoryUnsafePath, "scan media blobs", nil)
		}
		name := filepath.Base(path)
		prefix := filepath.Base(filepath.Dir(path))
		switch {
		case digestPattern.MatchString(name) && strings.HasPrefix(name, prefix):
			files = append(files, collectBlobFile{
				path:   path,
				digest: name,
				size:   info.Size(),
			})
		case strings.HasPrefix(name, ".tmp-") &&
			validMediaID(strings.TrimPrefix(name, ".tmp-")):
			files = append(files, collectBlobFile{
				path:      path,
				size:      info.Size(),
				temporary: true,
			})
		default:
			return bounded(CategoryUnsafePath, "scan media blobs", nil)
		}
		return nil
	})
	if err != nil {
		return nil, bounded(CategoryUnsafePath, "scan media blobs", err)
	}
	return files, nil
}

func validateCollectDirectory(
	path string,
	start string,
	info os.FileInfo,
) error {
	if err := validatePrivateDirectoryInfo(info); err != nil {
		return err
	}
	if filepath.Clean(path) == filepath.Clean(start) {
		return nil
	}
	rel, err := filepath.Rel(start, path)
	if err != nil ||
		filepath.Dir(rel) != "." ||
		len(rel) != 2 {
		return bounded(CategoryUnsafePath, "scan media blob directory", nil)
	}
	decoded, err := hex.DecodeString(rel)
	if err != nil || len(decoded) != 1 {
		return bounded(CategoryUnsafePath, "scan media blob directory", nil)
	}
	return nil
}

func (s *Store) ensureLayout() (*os.Root, error) {
	if err := s.before(StepCreateRoot); err != nil {
		return nil, err
	}
	err := os.Mkdir(s.root, 0o700)
	if err != nil && !errors.Is(err, os.ErrExist) {
		return nil, bounded(CategoryStoreUnavailable, "create store root", err)
	}
	root, err := openPrivateRoot(s.root)
	if err != nil {
		return nil, err
	}
	if err := s.ensurePrivateDir(root, blobDirectory, StepCreateBlobDir); err != nil {
		root.Close() //nolint:errcheck
		return nil, err
	}
	if err := s.ensurePrivateDir(
		root,
		filepath.Join(blobDirectory, digestDirectory),
		StepCreateDigestDir,
	); err != nil {
		root.Close() //nolint:errcheck
		return nil, err
	}
	return root, nil
}

func (s *Store) ensurePrivateDir(
	root *os.Root,
	path string,
	step Step,
) error {
	if err := s.before(step); err != nil {
		return err
	}
	err := root.Mkdir(path, 0o700)
	if err != nil && !errors.Is(err, os.ErrExist) {
		return bounded(CategoryStoreUnavailable, "create directory", err)
	}
	info, err := root.Lstat(path)
	if err != nil {
		return bounded(CategoryStoreUnavailable, "inspect directory", err)
	}
	return validatePrivateDirectoryInfo(info)
}

func (s *Store) writeBlobTemp(
	ctx context.Context,
	root *os.Root,
	mediaID string,
	data []byte,
) (string, error) {
	if err := s.before(StepCreateBlobTemp); err != nil {
		return "", err
	}
	tempName := ".tmp-" + mediaID
	file, err := root.OpenFile(
		tempName,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return "", bounded(CategoryStoreUnavailable, "create blob staging file", err)
	}
	closed := false
	committed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
		if !committed {
			_ = root.Remove(tempName)
		}
	}()
	if err := contextError(ctx); err != nil {
		return "", err
	}
	if err := s.before(StepWriteBlob); err != nil {
		return "", err
	}
	if err := writeAll(file, data); err != nil {
		return "", bounded(CategoryStoreUnavailable, "write blob staging file", err)
	}
	if err := s.before(StepSyncBlob); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", bounded(
			CategoryDurabilityUncertain,
			"sync blob staging file",
			err,
		)
	}
	if err := s.before(StepCloseBlob); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", bounded(
			CategoryDurabilityUncertain,
			"close blob staging file",
			err,
		)
	}
	closed = true
	committed = true
	return tempName, nil
}

func (s *Store) publishBlob(
	root *os.Root,
	tempName string,
	blobName string,
) (bool, error) {
	if err := s.before(StepPublishBlob); err != nil {
		return false, err
	}
	if err := root.Link(tempName, blobName); err != nil {
		if errors.Is(err, os.ErrExist) {
			return true, nil
		}
		return false, bounded(
			CategoryDurabilityUncertain,
			"publish blob",
			err,
		)
	}
	if err := root.Remove(tempName); err != nil {
		return false, bounded(
			CategoryDurabilityUncertain,
			"remove published blob staging file",
			err,
		)
	}
	return false, nil
}

func (s *Store) validateBlob(
	ctx context.Context,
	root *os.Root,
	name string,
	entry Entry,
) error {
	if err := s.before(StepValidateBlob); err != nil {
		return err
	}
	data, err := s.readRegularFile(
		ctx,
		root,
		name,
		MaxBlobBytes,
		CategoryMediaCorrupt,
		StepOpenBlob,
		StepReadBlob,
	)
	if err != nil {
		return err
	}
	defer clear(data)
	return validateBlobBytes(data, entry)
}

func (s *Store) readManifest() (Manifest, error) {
	root, err := openPrivateRoot(s.root)
	if err != nil {
		return Manifest{}, err
	}
	defer root.Close() //nolint:errcheck
	return s.readManifestFrom(context.Background(), root)
}

func (s *Store) readManifestFrom(
	ctx context.Context,
	root *os.Root,
) (Manifest, error) {
	if err := s.before(StepReadManifest); err != nil {
		return Manifest{}, err
	}
	data, err := s.readRegularFile(
		ctx,
		root,
		manifestFilename,
		MaxManifestBytes,
		CategoryManifestLimit,
		"",
		"",
	)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return emptyManifest(), nil
		}
		return Manifest{}, err
	}
	defer clear(data)
	return decodeManifest(data)
}

func (s *Store) writeManifest(root *os.Root, manifest Manifest) error {
	data, err := encodeManifest(manifest)
	if err != nil {
		return err
	}
	defer clear(data)
	mediaID, err := newMediaID()
	if err != nil {
		return bounded(CategoryStoreUnavailable, "create manifest staging identity", err)
	}
	if err := s.before(StepCreateManifestTemp); err != nil {
		return err
	}
	tempName := ".manifest.tmp-" + mediaID
	file, err := root.OpenFile(
		tempName,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return bounded(CategoryStoreUnavailable, "create manifest staging file", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
		_ = root.Remove(tempName)
	}()
	if err := s.before(StepWriteManifest); err != nil {
		return err
	}
	if err := writeAll(file, data); err != nil {
		return bounded(CategoryStoreUnavailable, "write manifest staging file", err)
	}
	if err := s.before(StepSyncManifest); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return bounded(
			CategoryDurabilityUncertain,
			"sync manifest staging file",
			err,
		)
	}
	if err := s.before(StepCloseManifest); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return bounded(
			CategoryDurabilityUncertain,
			"close manifest staging file",
			err,
		)
	}
	closed = true
	if err := s.before(StepReplaceManifest); err != nil {
		return err
	}
	if err := root.Rename(tempName, manifestFilename); err != nil {
		return bounded(
			CategoryDurabilityUncertain,
			"replace manifest",
			err,
		)
	}
	if err := s.before(StepSyncRoot); err != nil {
		return err
	}
	if err := syncRootDirectory(root); err != nil {
		return bounded(
			CategoryDurabilityUncertain,
			"sync store root",
			err,
		)
	}
	return nil
}

func (s *Store) readRegularFile(
	ctx context.Context,
	root *os.Root,
	name string,
	maxBytes int,
	tooLargeCategory string,
	openStep Step,
	readStep Step,
) ([]byte, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	before, err := root.Lstat(name)
	if err != nil {
		return nil, bounded(CategoryStoreUnavailable, "inspect regular file", err)
	}
	if !before.Mode().IsRegular() || before.Mode()&0o077 != 0 {
		return nil, bounded(CategoryUnsafePath, "inspect regular file", nil)
	}
	if openStep != "" {
		if err := s.before(openStep); err != nil {
			return nil, err
		}
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, bounded(CategoryStoreUnavailable, "open regular file", err)
	}
	defer file.Close() //nolint:errcheck
	after, err := file.Stat()
	if err != nil {
		return nil, bounded(CategoryStoreUnavailable, "inspect opened file", err)
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return nil, bounded(CategoryUnsafePath, "open regular file", nil)
	}
	if readStep != "" {
		if err := s.before(readStep); err != nil {
			return nil, err
		}
	}
	limited := io.LimitReader(file, int64(maxBytes)+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, bounded(CategoryStoreUnavailable, "read regular file", err)
	}
	if len(data) > maxBytes {
		clear(data)
		return nil, bounded(tooLargeCategory, "read regular file", nil)
	}
	if err := contextError(ctx); err != nil {
		clear(data)
		return nil, err
	}
	return data, nil
}

func (s *Store) before(step Step) error {
	if step == "" || s == nil || s.hook == nil {
		return nil
	}
	if err := s.hook(step); err != nil {
		return bounded(CategoryStoreUnavailable, string(step), err)
	}
	return nil
}

func decodeManifest(data []byte) (Manifest, error) {
	if len(data) == 0 || len(data) > MaxManifestBytes {
		return Manifest{}, bounded(CategoryManifestLimit, "decode manifest", nil)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, bounded(CategoryManifestCorrupt, "decode manifest", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Manifest{}, bounded(CategoryManifestCorrupt, "decode manifest", err)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func validateBlobBytes(data []byte, entry Entry) error {
	if int64(len(data)) != entry.SizeBytes {
		return bounded(CategoryMediaCorrupt, "validate blob", nil)
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != entry.Digest {
		return bounded(CategoryMediaCorrupt, "validate blob", nil)
	}
	info, reason := mediaimage.Inspect(data, entry.MIMEType)
	if reason != "" ||
		info.Width != entry.Width ||
		info.Height != entry.Height {
		return bounded(CategoryMediaCorrupt, "validate blob", nil)
	}
	return nil
}

func validateStoredImage(data []byte, metadata Metadata) error {
	info, reason := mediaimage.Inspect(data, metadata.MIMEType)
	if reason != "" ||
		info.Width != metadata.Width ||
		info.Height != metadata.Height {
		return bounded(CategoryInvalidMetadata, "validate media bytes", nil)
	}
	return nil
}

func openPrivateRoot(path string) (*os.Root, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, bounded(
			CategoryStoreUnavailable,
			"inspect store root",
			err,
		)
	}
	if err := validatePrivateDirectoryInfo(before); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, bounded(CategoryStoreUnavailable, "open store root", err)
	}
	after, err := root.Stat(".")
	if err != nil {
		root.Close() //nolint:errcheck
		return nil, bounded(CategoryStoreUnavailable, "inspect store root", err)
	}
	if !os.SameFile(before, after) {
		root.Close() //nolint:errcheck
		return nil, bounded(CategoryUnsafePath, "open store root", nil)
	}
	return root, nil
}

func openPrivateSubroot(root *os.Root, name string) (*os.Root, error) {
	before, err := root.Lstat(name)
	if err != nil {
		return nil, bounded(CategoryStoreUnavailable, "inspect directory", err)
	}
	if err := validatePrivateDirectoryInfo(before); err != nil {
		return nil, err
	}
	subroot, err := root.OpenRoot(name)
	if err != nil {
		return nil, bounded(CategoryUnsafePath, "open directory", err)
	}
	after, err := subroot.Stat(".")
	if err != nil {
		subroot.Close() //nolint:errcheck
		return nil, bounded(CategoryStoreUnavailable, "inspect directory", err)
	}
	if !os.SameFile(before, after) {
		subroot.Close() //nolint:errcheck
		return nil, bounded(CategoryUnsafePath, "open directory", nil)
	}
	return subroot, nil
}

func validatePrivateDirectoryInfo(info os.FileInfo) error {
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm()&0o077 != 0 {
		return bounded(CategoryUnsafePath, "inspect directory", nil)
	}
	return nil
}

func subrootStillBound(
	root *os.Root,
	name string,
	subroot *os.Root,
) error {
	current, err := root.Lstat(name)
	if err != nil {
		return bounded(CategoryUnsafePath, "revalidate directory", err)
	}
	if err := validatePrivateDirectoryInfo(current); err != nil {
		return err
	}
	opened, err := subroot.Stat(".")
	if err != nil {
		return bounded(CategoryUnsafePath, "revalidate directory", err)
	}
	if !os.SameFile(current, opened) {
		return bounded(CategoryUnsafePath, "revalidate directory", nil)
	}
	return nil
}

func rootStillBound(path string, root *os.Root) error {
	current, err := os.Lstat(path)
	if err != nil {
		return bounded(CategoryUnsafePath, "revalidate store root", err)
	}
	if err := validatePrivateDirectoryInfo(current); err != nil {
		return err
	}
	opened, err := root.Stat(".")
	if err != nil {
		return bounded(CategoryUnsafePath, "revalidate store root", err)
	}
	if !os.SameFile(current, opened) {
		return bounded(CategoryUnsafePath, "revalidate store root", nil)
	}
	return nil
}

func syncRootDirectory(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close() //nolint:errcheck
	return syncDirectoryFile(directory)
}

func newMediaID() (string, error) {
	var value [32]byte
	if _, err := io.ReadFull(rand.Reader, value[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return bounded(CategoryCanceled, "context", err)
	}
	return nil
}
