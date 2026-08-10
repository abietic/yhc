package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestLoadPolicy(t *testing.T) {
	root := openPolicyRoot(t, "../..")
	policy, err := loadPolicy(root, "quality/iteration.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if policy.Version != 1 || policy.Repository != "github.com/abietic/yhc" {
		t.Fatalf("loaded policy = %#v", policy)
	}
}

func TestLoadPolicyValidFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/policy-valid.yaml")
	if err != nil {
		t.Fatal(err)
	}
	root := writePolicyRoot(t, string(data))
	if _, err := loadPolicy(root, "iteration.yaml"); err != nil {
		t.Fatal(err)
	}
}

func TestLoadPolicyRejectsInvalidFixtures(t *testing.T) {
	for _, name := range []string{"policy-unknown-field.yaml", "policy-second-document.yaml"} {
		t.Run(name, func(t *testing.T) {
			root := openPolicyRoot(t, "testdata")
			if _, err := loadPolicy(root, name); err == nil {
				t.Fatal("loadPolicy succeeded")
			}
		})
	}
}

func TestLoadPolicyRejectsSemanticViolations(t *testing.T) {
	tests := []struct {
		name    string
		policy  string
		wantErr string
	}{
		{"version", strings.Replace(validPolicyYAML, "version: 1", "version: 2", 1), "version must be 1"},
		{"repository", strings.Replace(validPolicyYAML, "repository: example/repository", "repository: ", 1), "repository must not be empty"},
		{"unknown risk", strings.Replace(validPolicyYAML, "risks: []", "risks: [missing]", 1), "unknown risk"},
		{"missing target", strings.Replace(validPolicyYAML, "targets: [test]", "targets: [missing]", 1), "missing Make target"},
		{"missing owner document", strings.Replace(validPolicyYAML, "docs/owner.md", "docs/missing.md", 1), "missing owner document"},
		{"empty module", `version: 1
repository: example/repository
modules: {}
risk_packs: {}
change_classes:
  metadata:
    priority: 1
    paths: [Makefile]
    targets: [test]
boundaries: {}`, "modules must not be empty"},
		{"duplicate paths", strings.Replace(validPolicyYAML, "paths: [Makefile]", "paths: [Makefile, Makefile]", 1), "contains duplicate"},
		{"invalid platform", strings.Replace(validPolicyYAML, "platforms: [all]", "platforms: [other]", 1), "invalid platform"},
		{"unsupported deep target", strings.Replace(validPolicyYAML, "deep_targets: []", "deep_targets: [test]", 1), "unsupported deep target"},
		{"malformed pattern", strings.Replace(validPolicyYAML, "include: scripts/**", "include: scripts/[", 1), "invalid repository path pattern"},
		{
			"unknown boundary module",
			strings.Replace(
				validPolicyYAML,
				"forbidden_production_edges: []",
				"forbidden_production_edges:\n    - from: [tooling]\n      to: [missing]",
				1,
			),
			"references unknown module",
		},
		{
			"invalid flat root",
			strings.Replace(validPolicyYAML, "flat_package_roots: []", "flat_package_roots: [../tools]", 1),
			"flat package root",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := writePolicyRoot(t, test.policy)
			_, err := loadPolicy(root, "iteration.yaml")
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("loadPolicy error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestPolicyCoversTrackedFiles(t *testing.T) {
	root := openPolicyRoot(t, "../..")
	policy, err := loadPolicy(root, "quality/iteration.yaml")
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := unknownTrackedPaths(policy)
	if err != nil {
		t.Fatal(err)
	}
	if len(unknown) != 0 {
		t.Fatalf("unknown tracked paths:\n%s", strings.Join(unknown, "\n"))
	}
}

func TestDistributionPathsSkipsGeneratedEvidence(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"README.md", "docs/owner.md", "build/publication/report.json", "PUBLICATION_MANIFEST.json"} {
		fullPath := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	paths, err := distributionPaths(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"README.md", "docs/owner.md"}
	if !slices.Equal(paths, want) {
		t.Fatalf("distribution paths = %v, want %v", paths, want)
	}
}

func TestDistributionPathsRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "target"), []byte("target"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := distributionPaths(root); err == nil {
		t.Fatal("distributionPaths accepted a symlink")
	}
}

func TestRepositoryReviewPathsUsesDistributionWithoutGit(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"README.md", "docs/owner.md", "build/report.json", "PUBLICATION_MANIFEST.json"} {
		fullPath := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	paths, err := repositoryReviewPaths(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"README.md", "docs/owner.md"}
	if !slices.Equal(paths, want) {
		t.Fatalf("repository review paths = %v, want %v", paths, want)
	}
}

func TestRepositoryReviewPathsDoesNotMaskGitErrors(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("invalid git metadata\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := repositoryReviewPaths(context.Background(), root); err == nil || !strings.Contains(err.Error(), "list tracked files") {
		t.Fatalf("repositoryReviewPaths error = %v, want Git listing failure", err)
	}
}

func openPolicyRoot(t *testing.T, name string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(filepath.Clean(name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

func writePolicyRoot(t *testing.T, policy string) *os.Root {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"docs"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte("test:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docs", "owner.md"), []byte("owner\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "iteration.yaml"), []byte(policy), 0o644); err != nil {
		t.Fatal(err)
	}
	return openPolicyRoot(t, dir)
}

const validPolicyYAML = `version: 1
repository: example/repository
modules:
  tooling:
    priority: 1
    production_paths:
      - include: scripts/**
        exclude: []
    test_paths: []
    packages: [./scripts/...]
    owner_docs: [docs/owner.md]
    risks: []
    focused_packages: [./scripts/...]
risk_packs:
  contract:
    target: test
    deep_targets: []
    platforms: [all]
change_classes:
  metadata:
    priority: 1
    paths: [Makefile]
    targets: [test]
    focused_packages: []
boundaries:
  forbidden_production_edges: []
  flat_package_roots: []
`
