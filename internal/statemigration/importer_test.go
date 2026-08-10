package statemigration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abietic/yhc/internal/statepath"
)

type migrationFixture struct {
	roots      statepath.Roots
	spec       ArtifactSpec
	legacyPath string
	targetPath string
	stageCalls *atomic.Int32
}

type treeStateEntry struct {
	Mode    fs.FileMode
	ModTime time.Time
	Data    []byte
}

func TestImporterInspectReportsOnlyBoundedStates(t *testing.T) {
	fixture := newRegularMigrationFixture(t)
	result, err := (Importer{}).Inspect(t.Context(), fixture.roots, fixture.spec)
	if err != nil || result.Status != StatusReady {
		t.Fatalf("Inspect() = %#v, %v", result, err)
	}
	if fixture.stageCalls.Load() != 0 {
		t.Fatalf("Inspect invoked Stage %d times", fixture.stageCalls.Load())
	}
	inspectOnly := fixture.spec
	inspectOnly.AcquireSourceLease = func(context.Context, string) (func(), bool, error) {
		t.Fatal("Inspect invoked the mutating import lease callback")
		return nil, false, nil
	}
	result, err = (Importer{}).Inspect(t.Context(), fixture.roots, inspectOnly)
	if err != nil || result.Status != StatusReady {
		t.Fatalf("lease-aware Inspect() = %#v, %v", result, err)
	}

	busy := fixture.spec
	busy.Quiescent = func(context.Context, Snapshot) (bool, error) {
		return false, nil
	}
	result, err = (Importer{}).Inspect(t.Context(), fixture.roots, busy)
	if err != nil || result.Status != StatusLegacyBusy {
		t.Fatalf("busy Inspect() = %#v, %v", result, err)
	}

	if err := os.Remove(fixture.legacyPath); err != nil {
		t.Fatal(err)
	}
	result, err = (Importer{}).Inspect(t.Context(), fixture.roots, fixture.spec)
	if err != nil || result.Status != StatusAbsent {
		t.Fatalf("absent Inspect() = %#v, %v", result, err)
	}
}

func TestImporterImportsRegularFileAndDirectoryTree(t *testing.T) {
	for _, test := range []struct {
		name    string
		fixture func(*testing.T) migrationFixture
	}{
		{name: "regular", fixture: newRegularMigrationFixture},
		{name: "tree", fixture: newTreeMigrationFixture},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := test.fixture(t)
			legacyBefore := captureTreeState(t, fixture.roots.Legacy)

			result, err := (Importer{}).Import(t.Context(), fixture.roots, fixture.spec)
			if err != nil || result.Status != StatusImported {
				t.Fatalf("Import() = %#v, %v", result, err)
			}
			if fixture.stageCalls.Load() != 1 {
				t.Fatalf("Stage calls = %d, want 1", fixture.stageCalls.Load())
			}
			assertTreeStateEqual(t, legacyBefore, captureTreeState(t, fixture.roots.Legacy))
			assertImportedFixture(t, fixture)
		})
	}
}

func TestImporterHoldsOwnerSourceLeaseAcrossImportTransaction(t *testing.T) {
	fixture := newRegularMigrationFixture(t)
	var leased atomic.Bool
	var releases atomic.Int32
	fixture.spec.AcquireSourceLease = func(_ context.Context, source string) (func(), bool, error) {
		if source != fixture.legacyPath {
			t.Fatalf("lease source = %q, want declared artifact", source)
		}
		if !leased.CompareAndSwap(false, true) {
			t.Fatal("source lease acquired twice")
		}
		return func() {
			leased.Store(false)
			releases.Add(1)
		}, true, nil
	}
	originalStage := fixture.spec.Stage
	fixture.spec.Stage = func(ctx context.Context, snapshot Snapshot, stage *os.Root) error {
		if !leased.Load() {
			t.Fatal("source lease was not held during staging")
		}
		return originalStage(ctx, snapshot, stage)
	}

	result, err := (Importer{}).Import(t.Context(), fixture.roots, fixture.spec)
	if err != nil || result.Status != StatusImported {
		t.Fatalf("Import() = %#v, %v", result, err)
	}
	if leased.Load() || releases.Load() != 1 {
		t.Fatalf("lease held=%t releases=%d, want released once", leased.Load(), releases.Load())
	}
}

func TestImporterOwnerSourceLeaseReportsBusyWithoutStaging(t *testing.T) {
	fixture := newRegularMigrationFixture(t)
	fixture.spec.AcquireSourceLease = func(context.Context, string) (func(), bool, error) {
		return nil, false, nil
	}

	result, err := (Importer{}).Import(t.Context(), fixture.roots, fixture.spec)
	if err != nil || result.Status != StatusLegacyBusy {
		t.Fatalf("Import() = %#v, %v, want legacy busy", result, err)
	}
	if fixture.stageCalls.Load() != 0 {
		t.Fatalf("busy lease invoked Stage %d times", fixture.stageCalls.Load())
	}
	assertPathAbsent(t, fixture.targetPath)
}

func TestImporterOwnerSourceLeaseRecapturesAfterWriterRace(t *testing.T) {
	fixture := newRegularMigrationFixture(t)
	var releases atomic.Int32
	fixture.spec.AcquireSourceLease = func(context.Context, string) (func(), bool, error) {
		writePrivateFile(t, fixture.legacyPath, []byte(`{"version":1,"raced":true}`))
		return func() { releases.Add(1) }, true, nil
	}

	result, err := (Importer{}).Import(t.Context(), fixture.roots, fixture.spec)
	if err == nil || result.Status != StatusUnsafe {
		t.Fatalf("Import() = %#v, %v, want unsafe", result, err)
	}
	if releases.Load() != 1 || fixture.stageCalls.Load() != 0 {
		t.Fatalf("releases=%d stage calls=%d, want one release and no stage", releases.Load(), fixture.stageCalls.Load())
	}
	assertPathAbsent(t, fixture.targetPath)
}

func TestImporterRejectsSymlinkHardlinkAndRootReplacement(t *testing.T) {
	t.Run("source symlink", func(t *testing.T) {
		fixture := newRegularMigrationFixture(t)
		outside := filepath.Join(t.TempDir(), "outside.json")
		writePrivateFile(t, outside, []byte("outside"))
		if err := os.Remove(fixture.legacyPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, fixture.legacyPath); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		assertUnsafeImport(t, fixture, "outside")
	})

	t.Run("source hardlink", func(t *testing.T) {
		fixture := newRegularMigrationFixture(t)
		peer := filepath.Join(t.TempDir(), "peer.json")
		if err := os.Link(fixture.legacyPath, peer); err != nil {
			t.Skipf("hardlink unavailable: %v", err)
		}
		assertUnsafeImport(t, fixture, "peer.json")
	})

	t.Run("legacy root replacement", func(t *testing.T) {
		fixture := newRegularMigrationFixture(t)
		original := fixture.roots.Legacy + "-original"
		ctx := withImporterFailureHook(t.Context(), func(stage importerFailureStage) error {
			if stage != failureAfterSourceSnapshot {
				return nil
			}
			if err := os.Rename(fixture.roots.Legacy, original); err != nil {
				return err
			}
			if err := os.Mkdir(fixture.roots.Legacy, 0o700); err != nil {
				return err
			}
			writePrivateFile(t, fixture.legacyPath, []byte(`{"version":1,"replacement":true}`))
			return nil
		})
		result, err := (Importer{}).Import(ctx, fixture.roots, fixture.spec)
		if err == nil || result.Status != StatusUnsafe {
			t.Fatalf("Import() = %#v, %v, want unsafe", result, err)
		}
		assertPathAbsent(t, fixture.targetPath)
	})

	t.Run("canonical root replacement", func(t *testing.T) {
		fixture := newRegularMigrationFixture(t)
		detached := fixture.roots.Canonical + "-detached"
		ctx := withImporterFailureHook(t.Context(), func(stage importerFailureStage) error {
			if stage != failureAfterTargetParentSync {
				return nil
			}
			if err := os.Rename(fixture.roots.Canonical, detached); err != nil {
				return err
			}
			return os.Mkdir(fixture.roots.Canonical, 0o700)
		})
		result, err := (Importer{}).Import(ctx, fixture.roots, fixture.spec)
		if err == nil || result.Status != StatusUnsafe {
			t.Fatalf("Import() = %#v, %v, want unsafe", result, err)
		}
		assertPathAbsent(t, fixture.targetPath)
		assertPathAbsent(t, filepath.Join(detached, fixture.spec.TargetRel))
	})
}

func TestImporterRejectsDestinationCollisionWithoutMerge(t *testing.T) {
	fixture := newRegularMigrationFixture(t)
	if err := os.Mkdir(fixture.roots.Canonical, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := []byte("canonical-wins")
	writePrivateFile(t, fixture.targetPath, existing)
	legacyBefore := captureTreeState(t, fixture.roots.Legacy)

	result, err := (Importer{}).Import(t.Context(), fixture.roots, fixture.spec)
	if err != nil || result.Status != StatusDestinationExists {
		t.Fatalf("Import() = %#v, %v", result, err)
	}
	got, readErr := os.ReadFile(fixture.targetPath)
	if readErr != nil || !bytes.Equal(got, existing) {
		t.Fatalf("canonical target = %q, %v", got, readErr)
	}
	if fixture.stageCalls.Load() != 0 {
		t.Fatalf("collision invoked Stage %d times", fixture.stageCalls.Load())
	}
	assertTreeStateEqual(t, legacyBefore, captureTreeState(t, fixture.roots.Legacy))
}

func TestImporterPromotionNeverOverwritesRacingDestination(t *testing.T) {
	fixture := newRegularMigrationFixture(t)
	racing := []byte("created-by-canonical-owner")
	ctx := withImporterFailureHook(t.Context(), func(stage importerFailureStage) error {
		if stage == failureAfterTargetParentSync {
			writePrivateFile(t, fixture.targetPath, racing)
		}
		return nil
	})

	result, err := (Importer{}).Import(ctx, fixture.roots, fixture.spec)
	if err != nil || result.Status != StatusDestinationExists {
		t.Fatalf("Import() = %#v, %v", result, err)
	}
	got, readErr := os.ReadFile(fixture.targetPath)
	if readErr != nil || !bytes.Equal(got, racing) {
		t.Fatalf("racing target = %q, %v", got, readErr)
	}
}

func TestImporterConcurrentSinglePromotion(t *testing.T) {
	fixture := newRegularMigrationFixture(t)
	start := make(chan struct{})
	type outcome struct {
		result Result
		err    error
	}
	outcomes := make(chan outcome, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			result, err := (Importer{}).Import(t.Context(), fixture.roots, fixture.spec)
			outcomes <- outcome{result: result, err: err}
		}()
	}
	close(start)
	group.Wait()
	close(outcomes)

	statuses := make([]Status, 0, 2)
	for got := range outcomes {
		if got.err != nil {
			t.Fatalf("concurrent Import error: %v", got.err)
		}
		statuses = append(statuses, got.result.Status)
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i] < statuses[j] })
	want := []Status{StatusDestinationExists, StatusImported}
	if fmt.Sprint(statuses) != fmt.Sprint(want) {
		t.Fatalf("statuses = %v, want %v", statuses, want)
	}
	if fixture.stageCalls.Load() != 1 {
		t.Fatalf("Stage calls = %d, want 1", fixture.stageCalls.Load())
	}
	assertImportedFixture(t, fixture)
}

func TestImporterLeavesLegacyBytesModeAndMtimeUnchanged(t *testing.T) {
	fixture := newTreeMigrationFixture(t)
	before := captureTreeState(t, fixture.roots.Legacy)
	result, err := (Importer{}).Import(t.Context(), fixture.roots, fixture.spec)
	if err != nil || result.Status != StatusImported {
		t.Fatalf("Import() = %#v, %v", result, err)
	}
	assertTreeStateEqual(t, before, captureTreeState(t, fixture.roots.Legacy))
}

func TestImporterRefusesUnknownEntryUnsupportedSchemaAndUnsafeMode(t *testing.T) {
	t.Run("unknown entry", func(t *testing.T) {
		fixture := newTreeMigrationFixture(t)
		writePrivateFile(t, filepath.Join(fixture.legacyPath, "credential.txt"), []byte("never-copy"))
		assertUnsafeImport(t, fixture, "never-copy")
	})

	t.Run("unsupported schema", func(t *testing.T) {
		fixture := newRegularMigrationFixture(t)
		writePrivateFile(t, fixture.legacyPath, []byte(`{"version":2,"secret":"schema-secret"}`))
		assertUnsafeImport(t, fixture, "schema-secret")
	})

	t.Run("unsafe mode", func(t *testing.T) {
		fixture := newRegularMigrationFixture(t)
		if err := os.Chmod(fixture.legacyPath, 0o644); err != nil {
			t.Fatal(err)
		}
		assertUnsafeImport(t, fixture, fixture.legacyPath)
	})

	t.Run("special file", func(t *testing.T) {
		fixture := newTreeMigrationFixture(t)
		if !createSpecialMigrationFile(t, filepath.Join(fixture.legacyPath, "pipe")) {
			t.Skip("special file fixture unavailable")
		}
		assertUnsafeImport(t, fixture, "pipe")
	})
}

func TestImporterEnforcesFileAndByteBudgets(t *testing.T) {
	t.Run("files", func(t *testing.T) {
		fixture := newTreeMigrationFixture(t)
		fixture.spec.MaxFiles = 2
		assertUnsafeImport(t, fixture, fixture.legacyPath)
	})

	t.Run("bytes", func(t *testing.T) {
		fixture := newRegularMigrationFixture(t)
		info, err := os.Stat(fixture.legacyPath)
		if err != nil {
			t.Fatal(err)
		}
		fixture.spec.MaxBytes = info.Size() - 1
		assertUnsafeImport(t, fixture, fixture.legacyPath)
	})

	t.Run("exact boundary", func(t *testing.T) {
		fixture := newRegularMigrationFixture(t)
		info, err := os.Stat(fixture.legacyPath)
		if err != nil {
			t.Fatal(err)
		}
		fixture.spec.MaxFiles = 1
		fixture.spec.MaxBytes = info.Size()
		result, importErr := (Importer{}).Import(t.Context(), fixture.roots, fixture.spec)
		if importErr != nil || result.Status != StatusImported {
			t.Fatalf("exact-boundary Import() = %#v, %v", result, importErr)
		}
	})
}

func TestImporterDiagnosticsAreValueFree(t *testing.T) {
	const secret = "private-path-and-value-7f4a"
	fixture := newRegularMigrationFixture(t)
	fixture.spec.Validate = func(context.Context, Snapshot) error {
		return errors.New("validation exposed " + secret + " " + fixture.legacyPath)
	}
	result, err := (Importer{}).Import(t.Context(), fixture.roots, fixture.spec)
	if err == nil || result.Status != StatusUnsafe {
		t.Fatalf("Import() = %#v, %v", result, err)
	}
	assertNoDiagnosticLeak(t, fmt.Sprint(result, err), secret, fixture.legacyPath, fixture.roots.Canonical)

	invalid := fixture.spec
	invalid.SourceRel = "../" + secret
	result, err = (Importer{}).Inspect(t.Context(), fixture.roots, invalid)
	if err == nil || result.Status != StatusUnsafe {
		t.Fatalf("invalid Inspect() = %#v, %v", result, err)
	}
	assertNoDiagnosticLeak(t, fmt.Sprint(result, err), secret, fixture.legacyPath, fixture.roots.Canonical)
}

func TestImporterRejectsInvalidArtifactSpecsWithoutWalkingRoots(t *testing.T) {
	fixture := newRegularMigrationFixture(t)
	tests := []ArtifactSpec{
		{},
		withSpecField(fixture.spec, func(spec *ArtifactSpec) { spec.Owner = "bad/owner" }),
		withSpecField(fixture.spec, func(spec *ArtifactSpec) { spec.Scope = "all" }),
		withSpecField(fixture.spec, func(spec *ArtifactSpec) { spec.SourceRel = "." }),
		withSpecField(fixture.spec, func(spec *ArtifactSpec) { spec.TargetRel = "../escape" }),
		withSpecField(fixture.spec, func(spec *ArtifactSpec) { spec.Kind = Kind(99) }),
		withSpecField(fixture.spec, func(spec *ArtifactSpec) { spec.LegacyMode = LegacyMode(99) }),
		withSpecField(fixture.spec, func(spec *ArtifactSpec) { spec.MaxFiles = 0 }),
		withSpecField(fixture.spec, func(spec *ArtifactSpec) { spec.MaxBytes = 0 }),
		withSpecField(fixture.spec, func(spec *ArtifactSpec) { spec.Validate = nil }),
	}
	for index, spec := range tests {
		result, err := (Importer{}).Inspect(t.Context(), fixture.roots, spec)
		if err == nil || result.Status != StatusUnsafe {
			t.Fatalf("case %d: Inspect() = %#v, %v", index, result, err)
		}
	}
}

func newRegularMigrationFixture(t *testing.T) migrationFixture {
	t.Helper()
	parent := t.TempDir()
	legacy := filepath.Join(parent, ".eino-agent")
	canonical := filepath.Join(parent, ".yhc")
	if err := os.Mkdir(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	sourceRel := "settings.json"
	targetRel := "settings.json"
	source := filepath.Join(legacy, sourceRel)
	writePrivateFile(t, source, []byte("{\"version\":1,\"theme\":\"dark\"}\n"))
	calls := &atomic.Int32{}
	spec := ArtifactSpec{
		Owner:     "settings",
		Scope:     "project",
		SourceRel: sourceRel,
		TargetRel: targetRel,
		Kind:      RegularFile,
		MaxFiles:  1,
		MaxBytes:  4096,
		Validate: func(_ context.Context, snapshot Snapshot) error {
			data, err := readSnapshotFile(snapshot, ".")
			if err != nil {
				return err
			}
			if !bytes.Contains(data, []byte(`"version":1`)) {
				return errors.New("unsupported settings schema with private value " + string(data))
			}
			return nil
		},
		Stage: func(_ context.Context, snapshot Snapshot, stage *os.Root) error {
			calls.Add(1)
			return copySnapshotFile(snapshot, ".", stage, targetRel)
		},
	}
	return migrationFixture{
		roots:      statepath.Roots{Canonical: canonical, Legacy: legacy},
		spec:       spec,
		legacyPath: source,
		targetPath: filepath.Join(canonical, targetRel),
		stageCalls: calls,
	}
}

func newTreeMigrationFixture(t *testing.T) migrationFixture {
	t.Helper()
	parent := t.TempDir()
	legacy := filepath.Join(parent, ".eino-agent")
	canonical := filepath.Join(parent, ".yhc")
	if err := os.Mkdir(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	sourceRel := "memory"
	targetRel := "memory"
	source := filepath.Join(legacy, sourceRel)
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	writePrivateFile(t, filepath.Join(source, "VERSION"), []byte("1\n"))
	if err := os.Mkdir(filepath.Join(source, "notes"), 0o700); err != nil {
		t.Fatal(err)
	}
	writePrivateFile(t, filepath.Join(source, "notes", "project.md"), []byte("public-safe-memory\n"))
	calls := &atomic.Int32{}
	allowed := map[string]bool{
		".":                true,
		"VERSION":          true,
		"notes":            true,
		"notes/project.md": true,
	}
	spec := ArtifactSpec{
		Owner:     "memory",
		Scope:     "user",
		SourceRel: sourceRel,
		TargetRel: targetRel,
		Kind:      DirectoryTree,
		MaxFiles:  8,
		MaxBytes:  4096,
		Validate: func(_ context.Context, snapshot Snapshot) error {
			if err := snapshot.Walk(func(relative string, _ fs.DirEntry) error {
				if !allowed[relative] {
					return errors.New("unknown private entry " + relative)
				}
				return nil
			}); err != nil {
				return err
			}
			version, err := readSnapshotFile(snapshot, "VERSION")
			if err != nil || string(version) != "1\n" {
				return errors.New("unsupported memory schema")
			}
			return nil
		},
		Stage: func(_ context.Context, snapshot Snapshot, stage *os.Root) error {
			calls.Add(1)
			return copySnapshotTree(snapshot, stage)
		},
	}
	return migrationFixture{
		roots:      statepath.Roots{Canonical: canonical, Legacy: legacy},
		spec:       spec,
		legacyPath: source,
		targetPath: filepath.Join(canonical, targetRel),
		stageCalls: calls,
	}
}

func copySnapshotTree(snapshot Snapshot, stage *os.Root) error {
	return snapshot.Walk(func(relative string, entry fs.DirEntry) error {
		if relative == "." {
			return nil
		}
		name := filepath.FromSlash(relative)
		if entry.IsDir() {
			return stage.Mkdir(name, 0o700)
		}
		return copySnapshotFile(snapshot, relative, stage, name)
	})
}

func copySnapshotFile(snapshot Snapshot, source string, stage *os.Root, target string) error {
	input, _, err := snapshot.Open(source)
	if err != nil {
		return err
	}
	defer input.Close() //nolint:errcheck
	output, err := stage.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	return errors.Join(copyErr, closeErr)
}

func readSnapshotFile(snapshot Snapshot, relative string) ([]byte, error) {
	reader, _, err := snapshot.Open(relative)
	if err != nil {
		return nil, err
	}
	defer reader.Close() //nolint:errcheck
	return io.ReadAll(reader)
}

func writePrivateFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

func captureTreeState(t *testing.T, root string) map[string]treeStateEntry {
	t.Helper()
	state := map[string]treeStateEntry{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		item := treeStateEntry{Mode: info.Mode(), ModTime: info.ModTime()}
		if info.Mode().IsRegular() {
			item.Data, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		state[filepath.ToSlash(relative)] = item
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func assertTreeStateEqual(t *testing.T, want, got map[string]treeStateEntry) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("tree entry count = %d, want %d", len(got), len(want))
	}
	for relative, expected := range want {
		actual, ok := got[relative]
		if !ok {
			t.Fatalf("tree entry %q disappeared", relative)
		}
		if actual.Mode != expected.Mode || !actual.ModTime.Equal(expected.ModTime) || !bytes.Equal(actual.Data, expected.Data) {
			t.Fatalf("tree entry %q changed: got mode=%v mtime=%v data=%q; want mode=%v mtime=%v data=%q", relative, actual.Mode, actual.ModTime, actual.Data, expected.Mode, expected.ModTime, expected.Data)
		}
	}
}

func assertImportedFixture(t *testing.T, fixture migrationFixture) {
	t.Helper()
	switch fixture.spec.Kind {
	case RegularFile:
		legacy, err := os.ReadFile(fixture.legacyPath)
		if err != nil {
			t.Fatal(err)
		}
		canonical, err := os.ReadFile(fixture.targetPath)
		if err != nil || !bytes.Equal(canonical, legacy) {
			t.Fatalf("canonical = %q, %v; legacy = %q", canonical, err, legacy)
		}
	case DirectoryTree:
		legacy := captureTreeState(t, fixture.legacyPath)
		canonical := captureTreeState(t, fixture.targetPath)
		for relative, item := range legacy {
			canonicalItem, ok := canonical[relative]
			if !ok || item.Mode != canonicalItem.Mode || !bytes.Equal(item.Data, canonicalItem.Data) {
				t.Fatalf("canonical tree mismatch at %q", relative)
			}
		}
	}
}

func assertUnsafeImport(t *testing.T, fixture migrationFixture, forbidden ...string) {
	t.Helper()
	result, err := (Importer{}).Import(t.Context(), fixture.roots, fixture.spec)
	if err == nil || result.Status != StatusUnsafe {
		t.Fatalf("Import() = %#v, %v, want unsafe", result, err)
	}
	assertNoDiagnosticLeak(t, fmt.Sprint(result, err), forbidden...)
	assertPathAbsent(t, fixture.targetPath)
}

func assertNoDiagnosticLeak(t *testing.T, diagnostic string, forbidden ...string) {
	t.Helper()
	for _, value := range forbidden {
		if value != "" && strings.Contains(diagnostic, value) {
			t.Fatalf("diagnostic leaked forbidden value %q: %q", value, diagnostic)
		}
	}
}

func assertPathAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("path %q exists or stat failed: %v", path, err)
	}
}

func withSpecField(spec ArtifactSpec, update func(*ArtifactSpec)) ArtifactSpec {
	update(&spec)
	return spec
}
