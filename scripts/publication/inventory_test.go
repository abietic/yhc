package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInventoryRequiresExactlyOneDecisionPerTrackedPath(t *testing.T) {
	t.Run("zero match", func(t *testing.T) {
		repo := newPublicationRepo(t, map[string]string{"README.md": "public\n", "go.mod": "module test\n"})
		writePublicationFile(t, repo, "policy.yaml", publicationConfig(`
rules:
  - id: docs
    include: [README.md]
    class: project-owned-original
    decision: include
    evidence: [review]
`))
		if err := publicationRun(repo, "inventory", "--config", "policy.yaml"); err == nil {
			t.Fatal("inventory accepted an unmatched tracked path")
		}
	})
	t.Run("overlapping rules", func(t *testing.T) {
		repo := newPublicationRepo(t, map[string]string{"README.md": "public\n"})
		writePublicationFile(t, repo, "policy.yaml", publicationConfig(`
rules:
  - id: docs-one
    include: [README.md]
    class: project-owned-original
    decision: include
    evidence: [review]
  - id: docs-two
    include: [README.*]
    class: project-owned-original
    decision: include
    evidence: [review]
`))
		if err := publicationRun(repo, "inventory", "--config", "policy.yaml"); err == nil {
			t.Fatal("inventory accepted overlapping rules")
		}
	})
}

func TestInventoryRejectsUnresolvedForCheckButReportsItForReview(t *testing.T) {
	repo := newPublicationRepo(t, map[string]string{"README.md": "public\n"})
	writePublicationFile(t, repo, "policy.yaml", publicationConfig(`
rules:
  - id: docs
    include: [README.md]
    class: reference-informed-independent
    decision: unresolved
    evidence: [review]
`))
	var stdout, stderr bytes.Buffer
	if err := runIn(context.Background(), repo, []string{"inventory", "--config", "policy.yaml"}, &stdout, &stderr); err != nil {
		t.Fatalf("inventory: %v", err)
	}
	if !strings.Contains(stdout.String(), `"decision": "unresolved"`) {
		t.Fatalf("inventory omitted unresolved decision: %s", stdout.String())
	}
	if err := publicationRun(repo, "check", "--config", "policy.yaml"); err == nil {
		t.Fatal("check accepted unresolved path")
	}
}

func TestInventoryTreatsTestsFixturesPromptsAssetsAndVendorAsOrdinaryPaths(t *testing.T) {
	repo := newPublicationRepo(t, map[string]string{
		"x_test.go": "package x\n", "testdata/golden.txt": "fixture\n", "prompts/a.txt": "prompt\n", "assets/a.svg": "asset\n", "vendor/a.go": "package vendor\n",
	})
	writePublicationFile(t, repo, "policy.yaml", publicationConfig(`
rules:
  - id: test
    include: [x_test.go]
    class: reference-informed-independent
    decision: unresolved
    evidence: [review]
  - id: fixtures
    include: [testdata/**]
    class: reference-informed-independent
    decision: unresolved
    evidence: [review]
  - id: prompts
    include: [prompts/**]
    class: reference-informed-independent
    decision: unresolved
    evidence: [review]
  - id: assets
    include: [assets/**]
    class: reference-informed-independent
    decision: unresolved
    evidence: [review]
  - id: vendor
    include: [vendor/**]
    class: license-compatible-third-party
    decision: unresolved
    evidence: [review]
`))
	if err := publicationRun(repo, "inventory", "--config", "policy.yaml"); err != nil {
		t.Fatalf("inventory: %v", err)
	}
}

func TestInventoryDoesNotReadIgnoredOrUntrackedRoots(t *testing.T) {
	repo := newPublicationRepo(t, map[string]string{"README.md": "public\n", ".gitignore": "private/\n"})
	writePublicationFile(t, repo, "private/secret.txt", "do not read")
	writePublicationFile(t, repo, "untracked.txt", "do not read")
	for _, name := range []string{".reference/secret.txt", ".eino-agent/transcript.jsonl", ".claude/credentials.json"} {
		writePublicationFile(t, repo, name, "do not read")
	}
	if runtime.GOOS != "windows" {
		for _, name := range []string{"private/secret.txt", "untracked.txt", ".reference/secret.txt", ".eino-agent/transcript.jsonl", ".claude/credentials.json"} {
			full := filepath.Join(repo, filepath.FromSlash(name))
			if err := os.Chmod(full, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(full, 0o600) })
		}
		if err := os.Remove(filepath.Join(repo, "README.md")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("private/secret.txt", filepath.Join(repo, "README.md")); err != nil {
			t.Fatal(err)
		}
	}
	writePublicationFile(t, repo, "policy.yaml", publicationConfig(`
rules:
  - id: docs
    include: [README.md]
    class: project-owned-original
    decision: include
    evidence: [review]
  - id: ignore
    include: [.gitignore]
    class: project-owned-original
    decision: include
    evidence: [review]
`))
	var stdout, stderr bytes.Buffer
	if err := runIn(context.Background(), repo, []string{"inventory", "--config", "policy.yaml"}, &stdout, &stderr); err != nil {
		t.Fatalf("inventory: %v", err)
	}
	for _, forbidden := range []string{"private/secret.txt", "untracked.txt", ".reference/secret.txt", ".eino-agent/transcript.jsonl", ".claude/credentials.json", ".git/config"} {
		if strings.Contains(stdout.String(), forbidden) {
			t.Fatalf("inventory exposed untracked or private path %q", forbidden)
		}
	}
	if runtime.GOOS != "windows" {
		var inventory Inventory
		if err := json.Unmarshal(stdout.Bytes(), &inventory); err != nil {
			t.Fatal(err)
		}
		want := sha256.Sum256([]byte("public\n"))
		for _, file := range inventory.Files {
			if file.Path == "README.md" && file.BlobSHA256 != hex.EncodeToString(want[:]) {
				t.Fatalf("inventory read worktree replacement instead of staged README blob")
			}
		}
	}
}

func TestInventoryRejectsNonUTF8TrackedPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Git path byte fixture is Unix-specific")
	}
	repo := newPublicationRepo(t, map[string]string{"README.md": "public\n"})
	badName := string([]byte{'b', 'a', 'd', '-', 0xff})
	hash := exec.Command("git", "hash-object", "-w", "--stdin")
	hash.Dir = repo
	hash.Stdin = strings.NewReader("unsafe\n")
	output, err := hash.Output()
	if err != nil {
		t.Fatal(err)
	}
	oid := strings.TrimSpace(string(output))
	runGit(t, repo, "update-index", "--add", "--cacheinfo", "100644,"+oid+","+badName)
	writePublicationFile(t, repo, "policy.yaml", publicationConfig(`
rules:
  - id: docs
    include: [README.md]
    class: project-owned-original
    decision: include
    evidence: [review]
`))
	if err := publicationRun(repo, "inventory", "--config", "policy.yaml"); err == nil {
		t.Fatal("inventory accepted a non-UTF-8 tracked path")
	}
}

func TestInventoryHonorsCanceledContext(t *testing.T) {
	repo := newPublicationRepo(t, map[string]string{"README.md": "public\n"})
	writePublicationFile(t, repo, "policy.yaml", publicationConfig(`
rules:
  - id: docs
    include: [README.md]
    class: project-owned-original
    decision: include
    evidence: [review]
`))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	if err := runIn(ctx, repo, []string{"inventory", "--config", "policy.yaml"}, &stdout, &stderr); err == nil {
		t.Fatal("inventory ignored a canceled context")
	}
}

func TestInventoryOutputIsPrivateAtomicAndRejectsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission and symlink assertion is Unix-specific")
	}
	repo := newPublicationRepo(t, map[string]string{"README.md": "public\n"})
	writePublicationFile(t, repo, "policy.yaml", publicationConfig(`
rules:
  - id: docs
    include: [README.md]
    class: project-owned-original
    decision: include
    evidence: [review]
`))
	output := "build/publication/inventory.json"
	if err := publicationRun(repo, "inventory", "--config", "policy.yaml", "--output", output); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(output)))
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(repo, "build", "publication")); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("output directory mode: %v, %v", info, err)
	}
	if info, err := os.Stat(filepath.Join(repo, filepath.FromSlash(output))); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("output file mode: %v, %v", info, err)
	}
	identityOutput := filepath.Join(repo, "build", "publication", currentIdentityPathsFilename)
	if info, err := os.Stat(identityOutput); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("identity path file mode: %v, %v", info, err)
	}
	if err := publicationRun(repo, "inventory", "--config", "policy.yaml", "--output", output); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(output)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("inventory output is not deterministic")
	}

	outside := filepath.Join(repo, "outside.json")
	writePublicationFile(t, repo, "outside.json", "outside")
	if err := os.Remove(filepath.Join(repo, filepath.FromSlash(output))); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, filepath.FromSlash(output))); err != nil {
		t.Fatal(err)
	}
	if err := publicationRun(repo, "inventory", "--config", "policy.yaml", "--output", output); err == nil {
		t.Fatal("inventory followed output symlink")
	}
	if contents, err := os.ReadFile(outside); err != nil || string(contents) != "outside" {
		t.Fatalf("external output changed: %q, %v", contents, err)
	}

	if err := os.Remove(filepath.Join(repo, filepath.FromSlash(output))); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(identityOutput); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, "build", "publication")); err != nil {
		t.Fatal(err)
	}
	outsideDirectory := filepath.Join(repo, "outside-dir")
	if err := os.Mkdir(outsideDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDirectory, filepath.Join(repo, "build", "publication")); err != nil {
		t.Fatal(err)
	}
	if err := publicationRun(repo, "inventory", "--config", "policy.yaml", "--output", output); err == nil {
		t.Fatal("inventory followed output parent symlink")
	}
	if _, err := os.Stat(filepath.Join(outsideDirectory, "inventory.json")); !os.IsNotExist(err) {
		t.Fatalf("external directory changed: %v", err)
	}

	if err := os.Remove(filepath.Join(repo, "build", "publication")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, "build")); err != nil {
		t.Fatal(err)
	}
	outsideAncestor := filepath.Join(repo, "outside-ancestor")
	if err := os.Mkdir(outsideAncestor, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideAncestor, filepath.Join(repo, "build")); err != nil {
		t.Fatal(err)
	}
	if err := publicationRun(repo, "inventory", "--config", "policy.yaml", "--output", output); err == nil {
		t.Fatal("inventory followed output ancestor symlink")
	}
	if _, err := os.Stat(filepath.Join(outsideAncestor, "publication", "inventory.json")); !os.IsNotExist(err) {
		t.Fatalf("external ancestor changed: %v", err)
	}
}

func TestInventoryOutputAncestorReplacementRaceDoesNotEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission and symlink assertion is Unix-specific")
	}
	tests := []struct {
		name        string
		stage       string
		path        string
		preparePath string
	}{
		{
			name:  "replace ancestor before open",
			stage: "before-open",
			path:  "build",
		},
		{
			name:  "replace ancestor before mkdir",
			stage: "before-mkdir",
			path:  filepath.Join("build", "publication"),
		},
		{
			name:        "replace destination before chmod",
			stage:       "before-chmod",
			path:        filepath.Join("build", "publication"),
			preparePath: filepath.Join("build", "publication"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := t.TempDir()
			if err := os.Mkdir(filepath.Join(repo, "build"), 0o700); err != nil {
				t.Fatal(err)
			}
			if test.preparePath != "" {
				if err := os.Mkdir(filepath.Join(repo, test.preparePath), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			outside := filepath.Join(repo, "outside")
			if err := os.Mkdir(outside, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(outside, 0o755); err != nil {
				t.Fatal(err)
			}

			old, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chdir(repo); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := os.Chdir(old); err != nil {
					t.Errorf("restore working directory: %v", err)
				}
			}()

			invoked := false
			var hookErr error
			hook := func(stage, path string) {
				if invoked || stage != test.stage || path != test.path {
					return
				}
				invoked = true
				if test.stage == "before-chmod" {
					hookErr = os.Rename(test.path, filepath.Join("build", "detached-publication"))
				} else {
					hookErr = os.Rename("build", "detached-build")
				}
				if hookErr == nil {
					replacementPath := test.path
					if test.stage != "before-chmod" {
						replacementPath = "build"
					}
					hookErr = os.Symlink(outside, replacementPath)
				}
			}
			output, err := openInventoryOutputDirectoryWithHook(
				filepath.Join("build", "publication"),
				hook,
			)
			if output != nil {
				_ = output.Close()
			}
			if hookErr != nil {
				t.Fatalf("replace output path: %v", hookErr)
			}
			if !invoked {
				t.Fatalf("replacement hook %q for %q was not invoked", test.stage, test.path)
			}
			if err == nil {
				t.Fatal("output directory replacement was accepted")
			}
			if info, statErr := os.Stat(outside); statErr != nil || info.Mode().Perm() != 0o755 {
				t.Fatalf("external directory mode changed: %v, %v", info, statErr)
			}
			if entries, readErr := os.ReadDir(outside); readErr != nil || len(entries) != 0 {
				t.Fatalf("external directory changed: %v, %v", entries, readErr)
			}
		})
	}
}

func publicationConfig(rules string) string {
	policy := "version: 1\nsource:\n  repository: github.com/abietic/yhc\n  baseline_commit: 8e34cc4794f0e1e9ae404c5bcf453d5e71a159c0\nmappings:\n  manifest: docs/migration/manifest.yaml\ndependencies:\n  license_policy: quality/dependency-licenses.yaml\n  sbom: sbom.cdx.json\n" + rules
	if !strings.Contains(rules, "docs/**") {
		policy += "\n  - id: migration-mapping\n    include: [docs/migration/manifest.yaml]\n    class: project-owned-original\n    decision: include\n    evidence: [review]\n"
	}
	return policy
}

func publicationRun(root string, args ...string) error {
	return runIn(context.Background(), root, args, &bytes.Buffer{}, &bytes.Buffer{})
}

func runIn(ctx context.Context, root string, args []string, stdout, stderr *bytes.Buffer) error {
	old, err := os.Getwd()
	if err != nil {
		return err
	}
	defer func() { _ = os.Chdir(old) }()
	if err := os.Chdir(root); err != nil {
		return err
	}
	return run(ctx, args, stdout, stderr)
}

func runGit(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}
