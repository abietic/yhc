package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func TestMaterializeRejectsDirtyOrMismatchedSourceCommit(t *testing.T) {
	repo, config, head := materializerFixture(t)
	writePublicationFile(t, repo, "local.txt", "not tracked")
	inRepo(t, repo, func() {
		if err := materialize(context.Background(), config, head, filepath.Join(t.TempDir(), "release")); err == nil {
			t.Fatal("accepted dirty source")
		}
		runGit(t, repo, "clean", "-fdq")
		if err := materialize(context.Background(), config, strings.Repeat("a", 40), filepath.Join(t.TempDir(), "release")); err == nil {
			t.Fatal("accepted mismatched commit")
		}
	})
}

func TestScanTreeRecomputesMappingsFromPinnedTreeManifest(t *testing.T) {
	root := t.TempDir()
	writePublicationFile(t, root, "README.md", "reference-informed\n")
	writePublicationFile(t, root, "sbom.cdx.json", "{}\n")
	writePublicationFile(t, root, migrationMappingManifest, "version: 4\nfiles:\n  - path: src/source.ts\n    targets: [README.md]\n")
	for _, directory := range []string{root, filepath.Join(root, "docs"), filepath.Join(root, "docs", "migration")} {
		if err := os.Chmod(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	config := Config{Mappings: MappingPolicy{Manifest: migrationMappingManifest}, Dependencies: DependencyPolicy{SBOM: "sbom.cdx.json"}, Rules: []PathRule{
		{ID: "readme", Include: []string{"README.md"}, Class: "reference-informed-independent", Decision: "include", Evidence: []string{"review"}},
		{ID: "mapping", Include: []string{migrationMappingManifest}, Class: "project-owned-original", Decision: "include", Evidence: []string{"review"}},
		{ID: "sbom", Include: []string{"sbom.cdx.json"}, Class: "project-owned-original", Decision: "include", Evidence: []string{"review"}},
	}}
	if _, _, err := scanTree(root, config, false); err != nil {
		t.Fatalf("tree mapping: %v", err)
	}
	oldHook := publicationRaceHook
	t.Cleanup(func() { publicationRaceHook = oldHook })
	publicationRaceHook = func(stage, name string) {
		if stage == "after-tree-file-read" && name == migrationMappingManifest {
			if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte("version: 4\nfiles: []\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, _, err := scanTree(root, config, false); err == nil {
		t.Fatal("accepted a mapping manifest replaced after its digest read")
	}
}

func TestMaterializeCopiesOnlyIncludedRegularFiles(t *testing.T) {
	repo, config, head := materializerFixture(t)
	writePublicationFile(t, repo, ".reference/secret.txt", "secret")
	writePublicationFile(t, repo, ".eino-agent/transcripts/session.jsonl", "secret")
	writePublicationFile(t, repo, ".claude/credentials.json", "secret")
	out := filepath.Join(t.TempDir(), "release")
	opened := map[string]bool{}
	oldHook := publicationRaceHook
	t.Cleanup(func() { publicationRaceHook = oldHook })
	publicationRaceHook = func(stage, name string) {
		if stage == "before-source-open" {
			opened[name] = true
		}
	}
	inRepo(t, repo, func() {
		if err := materialize(context.Background(), config, head, out); err != nil {
			t.Fatal(err)
		}
	})
	for _, name := range []string{"README.md", "sbom.cdx.json"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	for _, name := range []string{".reference", ".eino-agent", ".claude", ".git"} {
		if _, err := os.Lstat(filepath.Join(out, name)); !os.IsNotExist(err) {
			t.Fatalf("copied forbidden %s", name)
		}
	}
	for name := range opened {
		if strings.HasPrefix(name, ".reference/") || strings.HasPrefix(name, ".eino-agent/") || strings.HasPrefix(name, ".claude/") {
			t.Fatalf("opened forbidden or ignored source path %q", name)
		}
	}
	if info, err := os.Stat(out); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("release root mode is not 0700: %v, %v", info, err)
	}
	for _, name := range []string{"README.md", "sbom.cdx.json", ".gitignore"} {
		if info, err := os.Stat(filepath.Join(out, name)); err != nil || info.Mode().Perm() != 0o644 {
			t.Fatalf("release file %s mode is not 0644: %v, %v", name, info, err)
		}
	}
}

func TestMaterializePreservesOnlyTrackedExecuteBits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable mode is not stable on Windows")
	}
	repo, config, _ := materializerFixture(t)
	writePublicationFile(t, repo, "run.sh", "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(filepath.Join(repo, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "run.sh")
	runGit(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "executable")
	head := repositoryHead(t, repo)
	config.Rules = append(config.Rules, PathRule{ID: "script", Include: []string{"run.sh"}, Class: "project-owned-original", Decision: "include", Evidence: []string{"review"}})
	out := filepath.Join(t.TempDir(), "release")
	inRepo(t, repo, func() {
		if err := materialize(context.Background(), config, head, out); err != nil {
			t.Fatal(err)
		}
	})
	if info, err := os.Stat(filepath.Join(out, "run.sh")); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("tracked executable mode was not preserved: %v, %v", info, err)
	}
}

func TestMaterializeRejectsSymlinkSubmoduleSpecialFileAndCollision(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Run("worktree replacement symlink", func(t *testing.T) {
			repo, config, head := materializerFixture(t)
			if err := os.Remove(filepath.Join(repo, "README.md")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("sbom.cdx.json", filepath.Join(repo, "README.md")); err != nil {
				t.Fatal(err)
			}
			inRepo(t, repo, func() {
				if err := materialize(context.Background(), config, head, filepath.Join(t.TempDir(), "release")); err == nil {
					t.Fatal("accepted source replacement symlink")
				}
			})
		})
		t.Run("tracked symlink", func(t *testing.T) {
			repo, config, _ := materializerFixture(t)
			if err := os.Remove(filepath.Join(repo, "README.md")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("sbom.cdx.json", filepath.Join(repo, "README.md")); err != nil {
				t.Fatal(err)
			}
			runGit(t, repo, "add", "README.md")
			runGit(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "tracked symlink")
			head := repositoryHead(t, repo)
			inRepo(t, repo, func() {
				if err := materialize(context.Background(), config, head, filepath.Join(t.TempDir(), "release")); err == nil {
					t.Fatal("accepted tracked symlink")
				}
			})
		})
		t.Run("gitlink", func(t *testing.T) {
			repo, config, _ := materializerFixture(t)
			module := filepath.Join(repo, "module")
			if err := os.Mkdir(module, 0o700); err != nil {
				t.Fatal(err)
			}
			runGit(t, module, "init", "-q")
			writePublicationFile(t, module, "module.txt", "module\n")
			runGit(t, module, "add", "module.txt")
			runGit(t, module, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "module")
			moduleHead := repositoryHead(t, module)
			runGit(t, repo, "update-index", "--add", "--cacheinfo", "160000", moduleHead, "module")
			runGit(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "gitlink")
			head := repositoryHead(t, repo)
			inRepo(t, repo, func() {
				if err := materialize(context.Background(), config, head, filepath.Join(t.TempDir(), "release")); err == nil {
					t.Fatal("accepted gitlink")
				}
			})
		})
	}
	if safeSegment("CON.txt") || safeSegment("trailing.") || safeSegment("bad:name") {
		t.Fatal("portable collision names accepted")
	}
	if err := collisionFree([]treeFile{{path: "README.md"}, {path: "readme.md"}}); err == nil {
		t.Fatal("accepted case-folded path collision")
	}
	if err := collisionFree([]treeFile{{path: "caf\u00e9.md"}, {path: "cafe\u0301.md"}}); err == nil {
		t.Fatal("accepted NFC path collision")
	}
}

func TestMaterializePinsSourceAndDestinationRootsAgainstReplacement(t *testing.T) {
	repo, config, head := materializerFixture(t)
	parent := t.TempDir()
	out := filepath.Join(parent, "release")
	inRepo(t, repo, func() {
		if err := materialize(context.Background(), config, head, out); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := os.Stat(out); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(parent, "second")); err == nil {
		inRepo(t, repo, func() {
			if err := materialize(context.Background(), config, head, filepath.Join(parent, "second", "release")); err == nil {
				t.Fatal("accepted symlink parent")
			}
		})
	}
	t.Run("caller parent replacement before promotion", func(t *testing.T) {
		repo, config, head := materializerFixture(t)
		parent := t.TempDir()
		detached := parent + "-detached"
		oldHook := publicationRaceHook
		t.Cleanup(func() { publicationRaceHook = oldHook })
		publicationRaceHook = func(stage, _ string) {
			if stage != "before-promotion" {
				return
			}
			if err := os.Rename(parent, detached); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(parent, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		inRepo(t, repo, func() {
			if err := materialize(context.Background(), config, head, filepath.Join(parent, "release")); err == nil {
				t.Fatal("accepted replaced caller parent")
			}
		})
		for _, candidate := range []string{filepath.Join(parent, "release"), filepath.Join(detached, "release")} {
			if _, err := os.Lstat(candidate); !os.IsNotExist(err) {
				t.Fatalf("failed promotion left output at %s", candidate)
			}
		}
	})
	t.Run("caller parent replacement after promotion", func(t *testing.T) {
		repo, config, head := materializerFixture(t)
		parent := t.TempDir()
		detached := parent + "-detached"
		oldHook := publicationRaceHook
		t.Cleanup(func() { publicationRaceHook = oldHook })
		publicationRaceHook = func(stage, _ string) {
			if stage != "after-promotion" {
				return
			}
			if err := os.Rename(parent, detached); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(parent, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		inRepo(t, repo, func() {
			if err := materialize(context.Background(), config, head, filepath.Join(parent, "release")); err == nil {
				t.Fatal("accepted caller parent replacement after promotion")
			}
		})
		for _, candidate := range []string{filepath.Join(parent, "release"), filepath.Join(detached, "release")} {
			if _, err := os.Lstat(candidate); !os.IsNotExist(err) {
				t.Fatalf("post-promotion rollback left output at %s", candidate)
			}
		}
	})
}

func TestMaterializeRejectsOutputInsideSourceAndNoncleanOutput(t *testing.T) {
	repo, config, head := materializerFixture(t)
	inRepo(t, repo, func() {
		if err := materialize(context.Background(), config, head, filepath.Join(repo, "release")); err == nil {
			t.Fatal("accepted output inside source")
		}
		parent := t.TempDir()
		if err := os.Mkdir(filepath.Join(parent, "alias"), 0o700); err != nil {
			t.Fatal(err)
		}
		dirty := parent + string(filepath.Separator) + "alias" + string(filepath.Separator) + ".." + string(filepath.Separator) + "release"
		if err := materialize(context.Background(), config, head, dirty); err == nil {
			t.Fatal("accepted nonclean output path")
		}
	})
	nestedRepo, nestedConfig, nestedHead := nestedMaterializerFixture(t)
	inRepo(t, filepath.Join(nestedRepo, "docs"), func() {
		if err := materialize(context.Background(), nestedConfig, nestedHead, filepath.Join(t.TempDir(), "release")); err == nil {
			t.Fatal("accepted a source worktree subdirectory")
		}
	})
}

func TestMaterializeRejectsSourceAndDestinationDirectoryReplacement(t *testing.T) {
	t.Run("source directory", func(t *testing.T) {
		repo, config, head := nestedMaterializerFixture(t)
		out := filepath.Join(t.TempDir(), "release")
		oldHook := publicationRaceHook
		t.Cleanup(func() { publicationRaceHook = oldHook })
		publicationRaceHook = func(stage, name string) {
			if stage != "after-file-copy" || name != "docs/README.md" {
				return
			}
			if err := os.Rename(filepath.Join(repo, "docs"), filepath.Join(repo, "docs-detached")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(repo, "docs"), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		inRepo(t, repo, func() {
			if err := materialize(context.Background(), config, head, out); err == nil {
				t.Fatal("accepted replaced source directory")
			}
		})
		if _, err := os.Lstat(out); !os.IsNotExist(err) {
			t.Fatal("failed source replacement left a release tree")
		}
	})
	t.Run("destination directory", func(t *testing.T) {
		repo, config, head := nestedMaterializerFixture(t)
		parent := t.TempDir()
		out := filepath.Join(parent, "release")
		oldHook := publicationRaceHook
		t.Cleanup(func() { publicationRaceHook = oldHook })
		publicationRaceHook = func(stage, name string) {
			if stage != "after-file-copy" || name != "docs/README.md" {
				return
			}
			entries, err := os.ReadDir(parent)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if !strings.HasPrefix(entry.Name(), ".publication-stage-") {
					continue
				}
				stageRoot := filepath.Join(parent, entry.Name())
				if err := os.Rename(filepath.Join(stageRoot, "docs"), filepath.Join(stageRoot, "docs-detached")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(stageRoot, "docs"), 0o700); err != nil {
					t.Fatal(err)
				}
				return
			}
			t.Fatal("publication stage was not found")
		}
		inRepo(t, repo, func() {
			if err := materialize(context.Background(), config, head, out); err == nil {
				t.Fatal("accepted replaced destination directory")
			}
		})
		if _, err := os.Lstat(out); !os.IsNotExist(err) {
			t.Fatal("failed destination replacement left a release tree")
		}
	})
	t.Run("source root", func(t *testing.T) {
		repo, config, head := nestedMaterializerFixture(t)
		detached := repo + "-detached"
		out := filepath.Join(t.TempDir(), "release")
		oldHook := publicationRaceHook
		t.Cleanup(func() { publicationRaceHook = oldHook })
		replaced := false
		publicationRaceHook = func(stage, _ string) {
			if stage != "after-file-copy" || replaced {
				return
			}
			replaced = true
			if err := os.Rename(repo, detached); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(repo, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		inRepo(t, repo, func() {
			if err := materialize(context.Background(), config, head, out); err == nil {
				t.Fatal("accepted replaced source root")
			}
		})
		if _, err := os.Lstat(out); !os.IsNotExist(err) {
			t.Fatal("failed source-root replacement left a release tree")
		}
	})
}

func TestMaterializeRejectsGitStateChangesDuringCopyAndPromotion(t *testing.T) {
	tests := []struct {
		name   string
		stage  string
		mutate func(*testing.T, string)
	}{
		{
			name:  "index mode during copy",
			stage: "after-file-copy",
			mutate: func(t *testing.T, repo string) {
				runGit(t, repo, "update-index", "--chmod=+x", "README.md")
			},
		},
		{
			name:  "HEAD during copy",
			stage: "after-file-copy",
			mutate: func(t *testing.T, repo string) {
				runGit(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "concurrent head")
			},
		},
		{
			name:  "index mode after promotion",
			stage: "after-promotion",
			mutate: func(t *testing.T, repo string) {
				runGit(t, repo, "update-index", "--chmod=+x", "README.md")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo, config, head := materializerFixture(t)
			parent := t.TempDir()
			out := filepath.Join(parent, "release")
			oldHook := publicationRaceHook
			t.Cleanup(func() { publicationRaceHook = oldHook })
			mutated := false
			publicationRaceHook = func(stage, _ string) {
				if stage != test.stage || mutated {
					return
				}
				mutated = true
				test.mutate(t, repo)
			}
			inRepo(t, repo, func() {
				if err := materialize(context.Background(), config, head, out); err == nil {
					t.Fatal("accepted concurrent Git state change")
				}
			})
			if !mutated {
				t.Fatal("Git mutation hook was not invoked")
			}
			if _, err := os.Lstat(out); !os.IsNotExist(err) {
				t.Fatal("failed Git-state check left a final release")
			}
			entries, err := os.ReadDir(parent)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".publication-stage-") {
					t.Fatalf("failed Git-state check leaked stage %q", entry.Name())
				}
			}
		})
	}
}

func TestCheckTreeRejectsGitReferenceStateAndPrivateOperationalRoots(t *testing.T) {
	root := t.TempDir()
	config := materializerConfig("deadbeef")
	writePublicationFile(t, root, ".git/config", "private")
	if _, err := checkTree(context.Background(), config, root); err == nil {
		t.Fatal("accepted .git root")
	}
	_ = os.RemoveAll(filepath.Join(root, ".git"))
	writePublicationFile(t, root, ".reference/secret.txt", "private")
	if _, err := checkTree(context.Background(), config, root); err == nil {
		t.Fatal("accepted reference root")
	}
}

func TestReleaseManifestIsDeterministicAndContainsNoFindingValues(t *testing.T) {
	repo, config, head := materializerFixture(t)
	out := filepath.Join(t.TempDir(), "release")
	inRepo(t, repo, func() {
		if err := materialize(context.Background(), config, head, out); err != nil {
			t.Fatal(err)
		}
	})
	var m1, m2 ReleaseManifest
	var err error
	inRepo(t, repo, func() {
		m1, err = writeReleaseManifest(context.Background(), config, out, filepath.Join(out, publicationManifest))
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(out, publicationManifest))
	if err != nil {
		t.Fatal(err)
	}
	inRepo(t, repo, func() {
		m2, err = writeReleaseManifest(context.Background(), config, out, filepath.Join(out, publicationManifest))
	})
	if err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(filepath.Join(out, publicationManifest))
	if string(first) != string(second) || m1.TreeSHA256 != m2.TreeSHA256 {
		t.Fatal("manifest is not deterministic")
	}
	var decoded ReleaseManifest
	if err := json.Unmarshal(first, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Checks) != 4 || strings.Contains(string(first), "finding") {
		t.Fatal("manifest exposes scan details")
	}
}

func TestMaterializeExcludesTrackedPriorReleaseManifest(t *testing.T) {
	repo, config, _ := materializerFixture(t)
	prior := ReleaseManifest{
		SchemaVersion:    1,
		SourceTreeSHA256: strings.Repeat("a", 64),
		TreeSHA256:       strings.Repeat("a", 64),
		FileCount:        3,
		Checks:           map[string]string{"policy": "pass", "tree": "pass", "expression": "pass", "sbom": "pass"},
		SBOMSHA256:       strings.Repeat("b", 64),
	}
	encoded, err := json.MarshalIndent(prior, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writePublicationFile(t, repo, publicationManifest, string(append(encoded, '\n')))
	runGit(t, repo, "add", publicationManifest)
	runGit(t, repo, "-c", "user.name=Test", "-c", "user.email=test@invalid", "commit", "-qm", "prior release manifest")
	head := repositoryHead(t, repo)
	config.Source.BaselineCommit = head
	config.Rules = append(config.Rules, PathRule{ID: "publication-manifest", Include: []string{publicationManifest}, Class: "project-owned-original", Decision: "include", Evidence: []string{"review"}})

	var inventory Inventory
	inRepo(t, repo, func() {
		inventory, err = approvedInventory(context.Background(), config)
	})
	if err != nil {
		t.Fatal(err)
	}
	manifestClassified := false
	for _, file := range inventory.Files {
		if file.Path == publicationManifest {
			manifestClassified = file.RuleID == "publication-manifest" && file.Decision == "include"
		}
	}
	if !manifestClassified {
		t.Fatal("tracked prior manifest was not explicitly classified")
	}

	out := filepath.Join(t.TempDir(), "release")
	inRepo(t, repo, func() {
		err = materialize(context.Background(), config, head, out)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(out, publicationManifest)); !os.IsNotExist(err) {
		t.Fatal("materialized payload retained the prior release manifest")
	}

	var manifest ReleaseManifest
	inRepo(t, repo, func() {
		manifest, err = writeReleaseManifest(context.Background(), config, out, filepath.Join(out, publicationManifest))
	})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.FileCount != len(inventory.Files)-1 {
		t.Fatalf("manifest describes %d files, want %d payload files", manifest.FileCount, len(inventory.Files)-1)
	}
	inRepo(t, t.TempDir(), func() {
		if _, checkErr := checkTree(context.Background(), config, out); checkErr != nil {
			t.Fatalf("materialized tree with refreshed manifest failed verification: %v", checkErr)
		}
	})
}

func TestReleaseManifestRejectsInvalidExistingManifest(t *testing.T) {
	repo, config, head := materializerFixture(t)
	out := filepath.Join(t.TempDir(), "release")
	inRepo(t, repo, func() {
		if err := materialize(context.Background(), config, head, out); err != nil {
			t.Fatal(err)
		}
		if _, err := writeReleaseManifest(context.Background(), config, out, filepath.Join(out, publicationManifest)); err != nil {
			t.Fatal(err)
		}
	})
	writePublicationFile(t, out, publicationManifest, "{\"schema_version\":1}\n")
	inRepo(t, repo, func() {
		if _, err := writeReleaseManifest(context.Background(), config, out, filepath.Join(out, publicationManifest)); err == nil {
			t.Fatal("overwrote an invalid existing manifest")
		}
	})
}

func TestReleaseManifestPinsTargetAndRootAgainstReplacement(t *testing.T) {
	t.Run("target replacement", func(t *testing.T) {
		repo, config, head := materializerFixture(t)
		out := filepath.Join(t.TempDir(), "release")
		manifestPath := filepath.Join(out, publicationManifest)
		inRepo(t, repo, func() {
			if err := materialize(context.Background(), config, head, out); err != nil {
				t.Fatal(err)
			}
			if _, err := writeReleaseManifest(context.Background(), config, out, manifestPath); err != nil {
				t.Fatal(err)
			}
		})
		contents, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		oldHook := publicationRaceHook
		t.Cleanup(func() { publicationRaceHook = oldHook })
		publicationRaceHook = func(stage, _ string) {
			if stage != "before-manifest-promotion" {
				return
			}
			if err := os.Rename(manifestPath, manifestPath+".detached"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(manifestPath, contents, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		inRepo(t, repo, func() {
			if _, err := writeReleaseManifest(context.Background(), config, out, manifestPath); err == nil {
				t.Fatal("accepted manifest target replacement")
			}
		})
		entries, err := os.ReadDir(out)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".publication-manifest-") {
				t.Fatalf("manifest failure leaked staging file %q", entry.Name())
			}
		}
	})
	t.Run("root replacement during read", func(t *testing.T) {
		repo, config, head := materializerFixture(t)
		out := filepath.Join(t.TempDir(), "release")
		inRepo(t, repo, func() {
			if err := materialize(context.Background(), config, head, out); err != nil {
				t.Fatal(err)
			}
			if _, err := writeReleaseManifest(context.Background(), config, out, filepath.Join(out, publicationManifest)); err != nil {
				t.Fatal(err)
			}
		})
		detached := out + "-detached"
		oldHook := publicationRaceHook
		t.Cleanup(func() { publicationRaceHook = oldHook })
		publicationRaceHook = func(stage, _ string) {
			if stage != "after-manifest-read" {
				return
			}
			if err := os.Rename(out, detached); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(out, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := readManifest(out); err == nil {
			t.Fatal("accepted publication root replacement during manifest read")
		}
	})
}

func TestReleaseManifestRejectsNoncanonicalMalformedAndUppercaseJSON(t *testing.T) {
	digest := strings.Repeat("ab", 32)
	manifest := ReleaseManifest{
		SchemaVersion:    1,
		SourceTreeSHA256: digest,
		TreeSHA256:       digest,
		FileCount:        2,
		Checks:           map[string]string{"policy": "pass", "tree": "pass", "expression": "pass", "sbom": "pass"},
		SBOMSHA256:       strings.Repeat("cd", 32),
	}
	canonical, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	canonical = append(canonical, '\n')
	tests := []struct {
		name      string
		contents  []byte
		shapeOnly bool
	}{
		{name: "duplicate key", contents: []byte("{\n  \"schema_version\": 1,\n  \"schema_version\": 1\n}\n")},
		{name: "unknown key", contents: append([]byte{}, bytesReplaceBeforeObjectEnd(canonical, []byte(",\n  \"unknown\": true"))...)},
		{name: "trailing JSON", contents: append(append([]byte{}, canonical...), []byte("{}\n")...)},
		{name: "uppercase digest", shapeOnly: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			contents := test.contents
			if test.shapeOnly {
				upper := manifest
				upper.SourceTreeSHA256 = strings.ToUpper(digest)
				upper.TreeSHA256 = strings.ToUpper(digest)
				contents, err = json.MarshalIndent(upper, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				contents = append(contents, '\n')
			}
			if err := os.WriteFile(filepath.Join(root, publicationManifest), contents, 0o644); err != nil {
				t.Fatal(err)
			}
			decoded, readErr := readManifest(root)
			if test.shapeOnly {
				if readErr != nil {
					t.Fatalf("canonical uppercase manifest should reach shape validation: %v", readErr)
				}
				if err := checkManifestShape(decoded); err == nil {
					t.Fatal("accepted uppercase manifest digest")
				}
				return
			}
			if readErr == nil {
				t.Fatal("accepted malformed or noncanonical manifest")
			}
		})
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, publicationManifest), canonical, 0o644); err != nil {
		t.Fatal(err)
	}
	decoded, err := readManifest(root)
	if err != nil || checkManifestShape(decoded) != nil {
		t.Fatalf("rejected canonical manifest: %v", err)
	}
}

func bytesReplaceBeforeObjectEnd(contents, insertion []byte) []byte {
	trimmed := strings.TrimSuffix(string(contents), "\n")
	index := strings.LastIndex(trimmed, "\n}")
	if index < 0 {
		return contents
	}
	return []byte(trimmed[:index] + string(insertion) + trimmed[index:] + "\n")
}

func TestCheckTreeRejectsManifestSourceDigestMismatchAndRootReplacement(t *testing.T) {
	repo, config, head := materializerFixture(t)
	out := filepath.Join(t.TempDir(), "release")
	var manifest ReleaseManifest
	inRepo(t, repo, func() {
		if err := materialize(context.Background(), config, head, out); err != nil {
			t.Fatal(err)
		}
		var err error
		manifest, err = writeReleaseManifest(context.Background(), config, out, filepath.Join(out, publicationManifest))
		if err != nil {
			t.Fatal(err)
		}
	})
	manifest.SourceTreeSHA256 = strings.Repeat("a", 64)
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writePublicationFile(t, out, publicationManifest, string(append(encoded, '\n')))
	inRepo(t, t.TempDir(), func() {
		if _, err := checkTree(context.Background(), config, out); err == nil {
			t.Fatal("accepted a manifest whose source digest differs from its tree digest")
		}
	})

	if runtime.GOOS != "windows" {
		link := filepath.Join(t.TempDir(), "release-link")
		if err := os.Symlink(out, link); err != nil {
			t.Fatal(err)
		}
		if _, _, err := scanTree(link, config, true); err == nil {
			t.Fatal("scanTree followed a root symlink")
		}
	}
}

func TestCheckTreeRejectsSpecialFilesAndNoncanonicalModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("FIFO and Unix mode assertions are not available")
	}
	root := t.TempDir()
	config := materializerConfig(strings.Repeat("a", 40))
	writePublicationFile(t, root, "README.md", "public\n")
	writePublicationFile(t, root, "sbom.cdx.json", "{}\n")
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	if _, _, err := scanTree(root, config, false); err == nil {
		t.Fatal("accepted a special file")
	}
	if err := os.Remove(filepath.Join(root, "pipe")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "README.md"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := scanTree(root, config, false); err == nil {
		t.Fatal("accepted a noncanonical file mode")
	}
}

func TestCheckTreePinsRootDirectoriesAndFilesAgainstReplacement(t *testing.T) {
	tests := []struct {
		name  string
		stage string
		path  string
	}{
		{name: "root", stage: "after-tree-root-walk"},
		{name: "directory", stage: "after-tree-directory-walk", path: "docs"},
		{name: "file", stage: "after-tree-file-read", path: "docs/README.md"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo, config, head := nestedMaterializerFixture(t)
			out := filepath.Join(t.TempDir(), "release")
			inRepo(t, repo, func() {
				if err := materialize(context.Background(), config, head, out); err != nil {
					t.Fatal(err)
				}
			})
			oldHook := publicationRaceHook
			t.Cleanup(func() { publicationRaceHook = oldHook })
			publicationRaceHook = func(stage, path string) {
				if stage != test.stage || (test.path != "" && path != test.path) {
					return
				}
				switch test.name {
				case "root":
					if err := os.Rename(out, out+"-detached"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(out, 0o700); err != nil {
						t.Fatal(err)
					}
				case "directory":
					if err := os.Rename(filepath.Join(out, "docs"), filepath.Join(out, "docs-detached")); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(filepath.Join(out, "docs"), 0o700); err != nil {
						t.Fatal(err)
					}
				case "file":
					file := filepath.Join(out, "docs", "README.md")
					if err := os.Rename(file, file+".detached"); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(file, []byte("replacement\n"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, _, err := scanTree(out, config, false); err == nil {
				t.Fatal("accepted publication tree replacement")
			}
		})
	}
}

func TestCheckTreeRehashesAfterExpressionScan(t *testing.T) {
	t.Run("regular file", func(t *testing.T) {
		repo, config, head := materializerFixture(t)
		out := filepath.Join(t.TempDir(), "release")
		inRepo(t, repo, func() {
			if err := materialize(context.Background(), config, head, out); err != nil {
				t.Fatal(err)
			}
		})
		oldHook := scanReadFile
		t.Cleanup(func() { scanReadFile = oldHook })
		reads := 0
		scanReadFile = func(_ *os.Root, name string) ([]byte, error) {
			if name == "README.md" {
				reads++
				if reads == 2 {
					if err := os.WriteFile(filepath.Join(out, name), []byte("api_key = post-scan-secret-123\n"), 0o644); err != nil {
						return nil, err
					}
				}
			}
			return nil, nil
		}
		inRepo(t, repo, func() {
			if _, err := checkTree(context.Background(), config, out); err == nil {
				t.Fatal("accepted an in-place tree change after expression scanning")
			}
		})
	})
	t.Run("manifest", func(t *testing.T) {
		repo, config, head := materializerFixture(t)
		out := filepath.Join(t.TempDir(), "release")
		inRepo(t, repo, func() {
			if err := materialize(context.Background(), config, head, out); err != nil {
				t.Fatal(err)
			}
			if _, err := writeReleaseManifest(context.Background(), config, out, filepath.Join(out, publicationManifest)); err != nil {
				t.Fatal(err)
			}
		})
		oldHook := scanReadFile
		t.Cleanup(func() { scanReadFile = oldHook })
		reads := 0
		scanReadFile = func(_ *os.Root, name string) ([]byte, error) {
			if name == publicationManifest {
				reads++
				if reads == 2 {
					if err := os.WriteFile(filepath.Join(out, publicationManifest), []byte("{\"changed\":true}\n"), 0o644); err != nil {
						return nil, err
					}
				}
			}
			return nil, nil
		}
		inRepo(t, t.TempDir(), func() {
			if _, err := checkTree(context.Background(), config, out); err == nil {
				t.Fatal("accepted an in-place manifest change after expression scanning")
			}
		})
	})
}

func materializerFixture(t *testing.T) (string, Config, string) {
	t.Helper()
	repo := newPublicationRepo(t, map[string]string{"README.md": "public\n", "sbom.cdx.json": "{}\n", ".gitignore": ".reference/\n.eino-agent/\n.claude/\n"})
	var head string
	inRepo(t, repo, func() {
		out, err := gitOutput(context.Background(), "rev-parse", "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		head = strings.TrimSpace(out)
	})
	return repo, materializerConfig(head), head
}

func nestedMaterializerFixture(t *testing.T) (string, Config, string) {
	t.Helper()
	repo := newPublicationRepo(t, map[string]string{"docs/README.md": "public\n", "sbom.cdx.json": "{}\n"})
	head := repositoryHead(t, repo)
	config := Config{
		Mappings:     MappingPolicy{Manifest: migrationMappingManifest},
		Source:       SourcePolicy{BaselineCommit: head},
		Dependencies: DependencyPolicy{SBOM: "sbom.cdx.json"},
		Rules: []PathRule{
			{ID: "docs", Include: []string{"docs/**"}, Class: "project-owned-original", Decision: "include", Evidence: []string{"review"}},
			{ID: "sbom", Include: []string{"sbom.cdx.json"}, Class: "project-owned-original", Decision: "include", Evidence: []string{"review"}},
		},
	}
	return repo, config, head
}

func materializerConfig(baseline string) Config {
	return Config{Mappings: MappingPolicy{Manifest: migrationMappingManifest}, Source: SourcePolicy{BaselineCommit: baseline}, Dependencies: DependencyPolicy{SBOM: "sbom.cdx.json"}, Rules: []PathRule{{ID: "docs", Include: []string{"README.md", ".gitignore"}, Class: "project-owned-original", Decision: "include", Evidence: []string{"review"}}, {ID: "mapping", Include: []string{migrationMappingManifest}, Class: "project-owned-original", Decision: "include", Evidence: []string{"review"}}, {ID: "sbom", Include: []string{"sbom.cdx.json"}, Class: "project-owned-original", Decision: "include", Evidence: []string{"review"}}}}
}

func repositoryHead(t *testing.T, repo string) string {
	t.Helper()
	var head string
	inRepo(t, repo, func() {
		output, err := gitOutput(context.Background(), "rev-parse", "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		head = strings.TrimSpace(output)
	})
	return head
}

func inRepo(t *testing.T, repo string, fn func()) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(old) }()
	fn()
}
