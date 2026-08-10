package mediastore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestStorePutResolveRoundTrip(t *testing.T) {
	root := filepath.Join(t.TempDir(), "session.jsonl.media")
	store := New(root)
	data := testImageData(t, 0x5a)
	ref, err := store.Put(context.Background(), data, testMetadata())
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := ref.Validate(); err != nil {
		t.Fatalf("Validate ref: %v", err)
	}
	if strings.Contains(ref.MediaID, "/") || strings.Contains(ref.MediaID, "\\") {
		t.Fatal("media ID exposed a path separator")
	}

	resolved, err := store.Resolve(context.Background(), ref)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !bytes.Equal(resolved, data) {
		t.Fatal("resolved bytes differ")
	}
	clear(resolved)

	assertMode(t, root, os.ModeDir|0o700)
	assertMode(t, filepath.Join(root, blobDirectory), os.ModeDir|0o700)
	assertMode(t, filepath.Join(root, blobDirectory, digestDirectory), os.ModeDir|0o700)
	assertMode(t, filepath.Join(root, manifestFilename), 0o600)

	manifest, err := store.readManifest()
	if err != nil {
		t.Fatalf("readManifest: %v", err)
	}
	entry := manifest.Entries[ref.MediaID]
	if !entry.MatchesRef(ref) {
		t.Fatal("manifest entry does not match ref")
	}
	blobPath := filepath.Join(
		root,
		blobDirectory,
		digestDirectory,
		entry.Digest[:2],
		entry.Digest,
	)
	assertMode(t, filepath.Dir(blobPath), os.ModeDir|0o700)
	assertMode(t, blobPath, 0o600)
}

func TestStoreDeduplicatesBytesButNotOpaqueIdentity(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "session.jsonl.media"))
	data := testImageData(t, 0x31)
	first, err := store.Put(context.Background(), data, testMetadata())
	if err != nil {
		t.Fatalf("first Put: %v", err)
	}
	second, err := store.Put(context.Background(), data, testMetadata())
	if err != nil {
		t.Fatalf("second Put: %v", err)
	}
	if first.MediaID == second.MediaID {
		t.Fatal("deduplicated bytes reused public identity")
	}
	manifest, err := store.readManifest()
	if err != nil {
		t.Fatalf("readManifest: %v", err)
	}
	if len(manifest.Entries) != 2 {
		t.Fatalf("manifest entries = %d, want 2", len(manifest.Entries))
	}
	digest := manifest.Entries[first.MediaID].Digest
	prefix := filepath.Join(
		store.root,
		blobDirectory,
		digestDirectory,
		digest[:2],
	)
	entries, err := os.ReadDir(prefix)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	regular := 0
	for _, entry := range entries {
		if entry.Name() == digest {
			regular++
		}
	}
	if regular != 1 {
		t.Fatalf("content-addressed blobs = %d, want 1", regular)
	}
}

func TestP302cCollectPrunesOnlyUnreachableMedia(t *testing.T) {
	root := filepath.Join(t.TempDir(), "session.jsonl.media")
	store := New(root)
	live, err := store.Put(
		context.Background(),
		testImageData(t, 0x31),
		testMetadata(),
	)
	if err != nil {
		t.Fatal(err)
	}
	sharedOrphan, err := store.Put(
		context.Background(),
		testImageData(t, 0x31),
		testMetadata(),
	)
	if err != nil {
		t.Fatal(err)
	}
	uniqueOrphan, err := store.Put(
		context.Background(),
		testImageData(t, 0x62),
		testMetadata(),
	)
	if err != nil {
		t.Fatal(err)
	}

	precommitCalls := 0
	result, err := store.Collect(
		context.Background(),
		[]Ref{live},
		func() error {
			precommitCalls++
			return nil
		},
	)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if precommitCalls != 1 ||
		result.ManifestEntriesRemoved != 2 ||
		result.BlobsRemoved != 1 ||
		result.BytesRemoved <= 0 {
		t.Fatalf("Collect result = %#v, precommit=%d", result, precommitCalls)
	}
	if _, err := store.Resolve(context.Background(), live); err != nil {
		t.Fatalf("live Resolve: %v", err)
	}
	for _, ref := range []Ref{sharedOrphan, uniqueOrphan} {
		if _, err := store.Resolve(context.Background(), ref); !IsCategory(
			err,
			CategoryMediaMissing,
		) {
			t.Fatalf("orphan Resolve = %v", err)
		}
	}
	manifest, err := store.readManifest()
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Entries) != 1 {
		t.Fatalf("manifest entries = %d", len(manifest.Entries))
	}
}

func TestP302cCollectPrecommitFailureDoesNotMutateStore(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "session.jsonl.media"))
	ref, err := store.Put(
		context.Background(),
		testImageData(t, 0x47),
		testMetadata(),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Collect(
		context.Background(),
		nil,
		func() error { return errors.New("revision changed") },
	)
	if err == nil || strings.Contains(err.Error(), ref.MediaID) {
		t.Fatalf("Collect error = %v", err)
	}
	if _, err := store.Resolve(context.Background(), ref); err != nil {
		t.Fatalf("Resolve after rejected collect: %v", err)
	}
}

func TestP302cCollectCancellationBeforeCommitDoesNotMutateStore(t *testing.T) {
	root := filepath.Join(t.TempDir(), "session.jsonl.media")
	ref, err := New(root).Put(
		context.Background(),
		testImageData(t, 0x58),
		testMetadata(),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	store := newWithHook(root, func(step Step) error {
		if step == StepCollectPrecommit {
			cancel()
		}
		return nil
	})
	_, err = store.Collect(ctx, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Collect error = %v", err)
	}
	if _, err := store.Resolve(context.Background(), ref); err != nil {
		t.Fatalf("Resolve after cancelled collect: %v", err)
	}
}

func TestStoreResolveRejectsMissingCorruptAndMismatchedMedia(t *testing.T) {
	root := filepath.Join(t.TempDir(), "session.jsonl.media")
	store := New(root)
	data := testImageData(t, 0x22)
	ref, err := store.Put(context.Background(), data, testMetadata())
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	manifest, err := store.readManifest()
	if err != nil {
		t.Fatalf("readManifest: %v", err)
	}
	entry := manifest.Entries[ref.MediaID]
	blobPath := filepath.Join(
		root,
		blobDirectory,
		digestDirectory,
		entry.Digest[:2],
		entry.Digest,
	)

	t.Run("metadata mismatch", func(t *testing.T) {
		changed := ref
		changed.Width++
		_, resolveErr := store.Resolve(context.Background(), changed)
		if !IsCategory(resolveErr, CategoryMediaCorrupt) {
			t.Fatalf("Resolve error = %v", resolveErr)
		}
	})

	t.Run("corrupt", func(t *testing.T) {
		if err := os.WriteFile(blobPath, bytes.Repeat([]byte{0x99}, len(data)), 0o600); err != nil {
			t.Fatal(err)
		}
		_, resolveErr := store.Resolve(context.Background(), ref)
		if !IsCategory(resolveErr, CategoryMediaCorrupt) {
			t.Fatalf("Resolve error = %v", resolveErr)
		}
	})

	t.Run("missing", func(t *testing.T) {
		if err := os.Remove(blobPath); err != nil {
			t.Fatal(err)
		}
		_, resolveErr := store.Resolve(context.Background(), ref)
		if !IsCategory(resolveErr, CategoryMediaMissing) {
			t.Fatalf("Resolve error = %v", resolveErr)
		}
	})
}

func TestStoreRejectsStrictManifestViolationsAndRedactsErrors(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private-secret-session.jsonl.media")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, manifestFilename)
	cases := []string{
		`{"version":1,"entries":{},"unknown":"secret"}`,
		`{"version":1,"entries":{}}{"version":1,"entries":{}}`,
		`{"version":2,"entries":{}}`,
		`{"version":1,"entries":null}`,
	}
	for _, content := range cases {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := New(root).Resolve(context.Background(), Ref{
			Version:   RefVersion,
			MediaID:   strings.Repeat("a", 43),
			MIMEType:  "image/png",
			SizeBytes: 1,
			Width:     1,
			Height:    1,
		})
		if err == nil {
			t.Fatalf("Resolve accepted %q", content)
		}
		if strings.Contains(err.Error(), "private-secret") ||
			strings.Contains(err.Error(), "secret") ||
			strings.Contains(err.Error(), strings.Repeat("a", 43)) {
			t.Fatalf("error leaked private identity: %v", err)
		}
	}
}

func TestStorePutFaultBoundariesReturnNoReference(t *testing.T) {
	steps := []Step{
		StepCreateRoot,
		StepCreateBlobDir,
		StepCreateDigestDir,
		StepCreatePrefixDir,
		StepCreateBlobTemp,
		StepWriteBlob,
		StepSyncBlob,
		StepCloseBlob,
		StepPublishBlob,
		StepSyncBlobDir,
		StepReadManifest,
		StepCreateManifestTemp,
		StepWriteManifest,
		StepSyncManifest,
		StepCloseManifest,
		StepReplaceManifest,
		StepSyncRoot,
	}
	for _, step := range steps {
		t.Run(string(step), func(t *testing.T) {
			injected := errors.New("injected")
			store := newWithHook(
				filepath.Join(t.TempDir(), "session.jsonl.media"),
				func(current Step) error {
					if current == step {
						return injected
					}
					return nil
				},
			)
			ref, err := store.Put(
				context.Background(),
				testImageData(t, 0x44),
				testMetadata(),
			)
			if err == nil {
				t.Fatal("Put succeeded")
			}
			if ref != (Ref{}) {
				t.Fatalf("Put returned reference after failure: %#v", ref)
			}
			if strings.Contains(err.Error(), "injected") {
				t.Fatalf("error leaked injected detail: %v", err)
			}
			assertNoMediaTemps(t, store.root)
		})
	}
}

func TestStoreDuplicateAndResolveFaultBoundaries(t *testing.T) {
	root := filepath.Join(t.TempDir(), "session.jsonl.media")
	data := testImageData(t, 0x77)
	if _, err := New(root).Put(context.Background(), data, testMetadata()); err != nil {
		t.Fatalf("seed Put: %v", err)
	}
	for _, step := range []Step{StepValidateBlob, StepRemoveBlobTemp} {
		t.Run(string(step), func(t *testing.T) {
			store := newWithHook(root, func(current Step) error {
				if current == step {
					return errors.New("injected")
				}
				return nil
			})
			ref, err := store.Put(context.Background(), data, testMetadata())
			if err == nil || ref != (Ref{}) {
				t.Fatalf("Put = %#v, %v", ref, err)
			}
		})
	}

	ref, err := New(root).Put(
		context.Background(),
		testImageData(t, 0x33),
		testMetadata(),
	)
	if err != nil {
		t.Fatalf("Put distinct: %v", err)
	}
	for _, step := range []Step{StepOpenBlob, StepReadBlob} {
		t.Run(string(step), func(t *testing.T) {
			store := newWithHook(root, func(current Step) error {
				if current == step {
					return errors.New("injected")
				}
				return nil
			})
			data, resolveErr := store.Resolve(context.Background(), ref)
			clear(data)
			if resolveErr == nil {
				t.Fatal("Resolve succeeded")
			}
		})
	}
}

func TestStoreRejectsSymlinkedRootAndReplacement(t *testing.T) {
	parent := t.TempDir()
	outside := filepath.Join(parent, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "session.jsonl.media")
	if err := os.Symlink(outside, root); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := New(root).Put(
		context.Background(),
		testImageData(t, 0x11),
		testMetadata(),
	); !IsCategory(err, CategoryUnsafePath) {
		t.Fatalf("symlink root Put error = %v", err)
	}

	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	var replaced bool
	data := testImageData(t, 0x6a)
	digestBytes := sha256.Sum256(data)
	digest := hex.EncodeToString(digestBytes[:])
	store := newWithHook(root, func(step Step) error {
		if step != StepCreateBlobTemp || replaced {
			return nil
		}
		replaced = true
		prefix := filepath.Join(
			root,
			blobDirectory,
			digestDirectory,
			digest[:2],
		)
		if err := os.Rename(prefix, prefix+".owned"); err != nil {
			return err
		}
		return os.Symlink(outside, prefix)
	})
	ref, err := store.Put(context.Background(), data, testMetadata())
	if err == nil || ref != (Ref{}) {
		t.Fatalf("replacement Put = %#v, %v", ref, err)
	}
	entries, readErr := os.ReadDir(outside)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("replacement wrote outside store: %v", entries)
	}
}

func TestStoreRootReplacementNeverWritesOutsideOrReturnsReference(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "session.jsonl.media")
	outside := filepath.Join(parent, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	owned := root + ".owned"
	replaced := false
	store := newWithHook(root, func(step Step) error {
		if step != StepCreateManifestTemp || replaced {
			return nil
		}
		replaced = true
		if err := os.Rename(root, owned); err != nil {
			return err
		}
		return os.Symlink(outside, root)
	})
	ref, err := store.Put(
		context.Background(),
		testImageData(t, 0x7a),
		testMetadata(),
	)
	if err == nil || ref != (Ref{}) {
		t.Fatalf("root replacement Put = %#v, %v", ref, err)
	}
	entries, readErr := os.ReadDir(outside)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("root replacement wrote outside: %v, %v", entries, readErr)
	}
}

func TestStoreConcurrentPutPreservesEveryManifestEntry(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "session.jsonl.media"))
	const count = 24
	var wg sync.WaitGroup
	wg.Add(count)
	errs := make(chan error, count)
	for index := range count {
		go func() {
			defer wg.Done()
			_, err := New(store.root).Put(
				context.Background(),
				testImageData(t, byte(index+1)),
				testMetadata(),
			)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	manifest, err := store.readManifest()
	if err != nil {
		t.Fatalf("readManifest: %v", err)
	}
	if len(manifest.Entries) != count {
		t.Fatalf("manifest entries = %d, want %d", len(manifest.Entries), count)
	}
}

func TestStoreContextCancellationIsTerminal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ref, err := New(filepath.Join(t.TempDir(), "session.jsonl.media")).Put(
		ctx,
		[]byte("payload"),
		testMetadata(),
	)
	if ref != (Ref{}) || !IsCategory(err, CategoryCanceled) {
		t.Fatalf("Put = %#v, %v", ref, err)
	}
}

func TestStoreRejectsInvalidRootAndUnverifiedMedia(t *testing.T) {
	valid := testImageData(t, 0x42)
	if ref, err := New("").Put(
		context.Background(),
		valid,
		testMetadata(),
	); ref != (Ref{}) || !IsCategory(err, CategoryStoreUnavailable) {
		t.Fatalf("empty-root Put = %#v, %v", ref, err)
	}
	if ref, err := New(filepath.Join(t.TempDir(), "invalid.media")).Put(
		context.Background(),
		[]byte("not an image"),
		testMetadata(),
	); ref != (Ref{}) || !IsCategory(err, CategoryInvalidMetadata) {
		t.Fatalf("invalid-media Put = %#v, %v", ref, err)
	}
	metadata := testMetadata()
	metadata.Width++
	if ref, err := New(filepath.Join(t.TempDir(), "mismatch.media")).Put(
		context.Background(),
		valid,
		metadata,
	); ref != (Ref{}) || !IsCategory(err, CategoryInvalidMetadata) {
		t.Fatalf("mismatched-metadata Put = %#v, %v", ref, err)
	}
}

func testMetadata() Metadata {
	return Metadata{
		MIMEType: "image/png",
		Width:    32,
		Height:   32,
		Kind:     "prompt_image",
	}
}

func testImageData(t *testing.T, value byte) []byte {
	t.Helper()
	source := image.NewRGBA(image.Rect(0, 0, 32, 32))
	fill := color.RGBA{
		R: value,
		G: value ^ 0x55,
		B: value ^ 0xaa,
		A: 0xff,
	}
	for y := range 32 {
		for x := range 32 {
			source.SetRGBA(x, y, fill)
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, source); err != nil {
		t.Fatalf("encode PNG: %v", err)
	}
	return encoded.Bytes()
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat %s: %v", filepath.Base(path), err)
	}
	got := info.Mode()
	if want&os.ModeDir != 0 {
		if !got.IsDir() {
			t.Fatalf("%s is not a directory: %v", filepath.Base(path), got)
		}
		want &= os.ModePerm
	}
	if got.Perm() != want.Perm() {
		t.Fatalf("%s mode = %o, want %o", filepath.Base(path), got.Perm(), want.Perm())
	}
}

func assertNoMediaTemps(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(
		_ string,
		entry os.DirEntry,
		walkErr error,
	) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if strings.HasPrefix(entry.Name(), ".tmp-") ||
			strings.HasPrefix(entry.Name(), ".manifest.tmp-") {
			t.Fatalf("staging file remains: %s", entry.Name())
		}
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("walk media store: %v", err)
	}
}
