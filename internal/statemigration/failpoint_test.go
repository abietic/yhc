package statemigration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestImporterFailureIsAtomicAndRestartSafe(t *testing.T) {
	stages := []importerFailureStage{
		failureAfterSourceSnapshot,
		failureAfterStagedWrite,
		failureAfterStageSync,
		failureAfterTargetParentSync,
		failureAfterRename,
		failureAfterPromotionTargetSync,
		failureAfterPromotionSourceSync,
	}
	for _, stage := range stages {
		t.Run(string(stage), func(t *testing.T) {
			fixture := newTreeMigrationFixture(t)
			legacyBefore := captureTreeState(t, fixture.roots.Legacy)
			ctx := withImporterFailureHook(t.Context(), func(current importerFailureStage) error {
				if current == stage {
					return errors.New("injected-private-value-14b8")
				}
				return nil
			})

			result, err := (Importer{}).Import(ctx, fixture.roots, fixture.spec)
			if err == nil || result.Status != StatusUnsafe {
				t.Fatalf("Import() = %#v, %v, want unsafe", result, err)
			}
			assertNoDiagnosticLeak(t, err.Error(), "injected-private-value-14b8", fixture.roots.Legacy, fixture.roots.Canonical)
			assertPathAbsent(t, fixture.targetPath)
			assertTreeStateEqual(t, legacyBefore, captureTreeState(t, fixture.roots.Legacy))
			assertNoMigrationStageResidue(t, fixture.roots.Canonical)

			result, err = (Importer{}).Import(t.Context(), fixture.roots, fixture.spec)
			if err != nil || result.Status != StatusImported {
				t.Fatalf("restart Import() = %#v, %v", result, err)
			}
			assertImportedFixture(t, fixture)
		})
	}
}

func TestImporterRecoversAbandonedOwnedStageAndPersistentLock(t *testing.T) {
	fixture := newRegularMigrationFixture(t)
	migrationRoot := filepath.Join(fixture.roots.Canonical, ".migration", "v1")
	if err := os.MkdirAll(migrationRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(fixture.roots.Canonical, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(fixture.roots.Canonical, ".migration"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(migrationRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(migrationRoot, "settings-project.lock")
	writePrivateFile(t, lock, nil)
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	abandoned := filepath.Join(migrationRoot, "settings-project.stage-abandoned")
	if err := os.Mkdir(abandoned, 0o700); err != nil {
		t.Fatal(err)
	}
	writePrivateFile(t, filepath.Join(abandoned, "partial"), []byte("partial"))

	result, err := (Importer{}).Import(t.Context(), fixture.roots, fixture.spec)
	if err != nil || result.Status != StatusImported {
		t.Fatalf("Import() = %#v, %v", result, err)
	}
	assertPathAbsent(t, abandoned)
	assertImportedFixture(t, fixture)
}

func TestImporterStageOutputFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name  string
		stage func(*migrationFixture)
	}{
		{
			name: "extra entry",
			stage: func(fixture *migrationFixture) {
				fixture.spec.Stage = func(_ context.Context, snapshot Snapshot, root *os.Root) error {
					if err := copySnapshotFile(snapshot, ".", root, fixture.spec.TargetRel); err != nil {
						return err
					}
					return root.WriteFile("extra", []byte("private-extra"), 0o600)
				}
			},
		},
		{
			name: "unsafe mode",
			stage: func(fixture *migrationFixture) {
				fixture.spec.Stage = func(_ context.Context, snapshot Snapshot, root *os.Root) error {
					if err := copySnapshotFile(snapshot, ".", root, fixture.spec.TargetRel); err != nil {
						return err
					}
					return root.Chmod(fixture.spec.TargetRel, 0o644)
				}
			},
		},
		{
			name: "hardlink",
			stage: func(fixture *migrationFixture) {
				fixture.spec.Stage = func(_ context.Context, snapshot Snapshot, root *os.Root) error {
					if err := copySnapshotFile(snapshot, ".", root, fixture.spec.TargetRel); err != nil {
						return err
					}
					return root.Link(fixture.spec.TargetRel, "peer")
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRegularMigrationFixture(t)
			test.stage(&fixture)
			assertUnsafeImport(t, fixture, "private-extra")
			assertNoMigrationStageResidue(t, fixture.roots.Canonical)
		})
	}

	t.Run("tree owner validator", func(t *testing.T) {
		fixture := newTreeMigrationFixture(t)
		original := fixture.spec.Stage
		fixture.spec.Stage = func(ctx context.Context, snapshot Snapshot, root *os.Root) error {
			if err := original(ctx, snapshot, root); err != nil {
				return err
			}
			return root.WriteFile("credential.txt", []byte("private-extra"), 0o600)
		}
		assertUnsafeImport(t, fixture, "private-extra")
		assertNoMigrationStageResidue(t, fixture.roots.Canonical)
	})
}

func TestImporterDurablySyncsBothPromotionParents(t *testing.T) {
	for _, fixtureFactory := range []struct {
		name string
		new  func(*testing.T) migrationFixture
	}{
		{name: "regular", new: newRegularMigrationFixture},
		{name: "tree", new: newTreeMigrationFixture},
	} {
		t.Run(fixtureFactory.name, func(t *testing.T) {
			fixture := fixtureFactory.new(t)
			var observed []importerFailureStage
			ctx := withImporterFailureHook(t.Context(), func(stage importerFailureStage) error {
				observed = append(observed, stage)
				return nil
			})
			result, err := (Importer{}).Import(ctx, fixture.roots, fixture.spec)
			if err != nil || result.Status != StatusImported {
				t.Fatalf("Import() = %#v, %v", result, err)
			}
			rename := stageIndex(observed, failureAfterRename)
			targetSync := stageIndex(observed, failureAfterPromotionTargetSync)
			sourceSync := stageIndex(observed, failureAfterPromotionSourceSync)
			if rename < 0 || targetSync <= rename || sourceSync <= targetSync {
				t.Fatalf("promotion durability order = %v", observed)
			}
		})
	}
}

func stageIndex(stages []importerFailureStage, target importerFailureStage) int {
	for index, stage := range stages {
		if stage == target {
			return index
		}
	}
	return -1
}

func assertNoMigrationStageResidue(t *testing.T, canonical string) {
	t.Helper()
	root := filepath.Join(canonical, ".migration", "v1")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".stage-") {
			t.Fatalf("migration stage residue remains: %q", entry.Name())
		}
	}
}
