package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const coverageDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestCoverageAbsentAndNoApplicableChanges(t *testing.T) {
	root := coverageRoot(t)
	plan := coveragePlan(ChangedPath{Path: "pkg/a.go", Status: "M", Kind: PathProduction})
	got, err := writeCoverageAdvisory(root, plan)
	if err != nil || got.Available || len(got.Packages) != 0 {
		t.Fatalf("absent profile: %#v, %v", got, err)
	}
	plan.Changed = []ChangedPath{{Path: "pkg/a_test.go", Status: "M", Kind: PathProduction}, {Path: "pkg/old.go", Status: "D", Kind: PathProduction}}
	got, err = writeCoverageAdvisory(root, plan)
	if err != nil || got.Available || len(got.Packages) != 0 {
		t.Fatalf("inapplicable changes: %#v, %v", got, err)
	}
}

func TestCoverageMalformedProfile(t *testing.T) {
	root := coverageRoot(t)
	writeProfile(t, root, "not a cover profile\n")
	if _, err := writeCoverageAdvisory(root, coveragePlan(ChangedPath{Path: "pkg/a.go", Status: "M", Kind: PathProduction})); err == nil {
		t.Fatal("malformed profile accepted")
	}
}

func TestCoverageMixedPackagesAndOrdering(t *testing.T) {
	root := coverageRoot(t)
	writeProfile(t, root, "mode: set\nexample.test/mod/b/b.go:1.1,1.2 2 1\nexample.test/mod/b/x.go:1.1,1.2 2 0\nexample.test/mod/a/a.go:1.1,1.2 3 1\nexample.test/mod/a2/not-a.go:1.1,1.2 20 0\n")
	plan := coveragePlan(
		ChangedPath{Path: "b/z.go", Status: "M", Kind: PathProduction},
		ChangedPath{Path: "a/y.go", Status: "M", Kind: PathProduction},
		ChangedPath{Path: "a/x.go", Status: "M", Kind: PathProduction},
		ChangedPath{Path: "b/no_test.go", Status: "M", Kind: PathProduction},
	)
	got, err := writeCoverageAdvisory(root, plan)
	if err != nil || !got.Available {
		t.Fatalf("coverage: %#v, %v", got, err)
	}
	want := []PackageCoverage{{Package: "example.test/mod/a", Statements: 100, ChangedFiles: []string{"a/x.go", "a/y.go"}}, {Package: "example.test/mod/b", Statements: 50, ChangedFiles: []string{"b/z.go"}}}
	if !reflect.DeepEqual(got.Packages, want) {
		t.Fatalf("packages = %#v, want %#v", got.Packages, want)
	}
	data, err := os.ReadFile(filepath.Join(root, "build", "iteration", coverageDigest, "coverage.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded CoverageAdvisory
	if err := json.Unmarshal(data, &decoded); err != nil || !reflect.DeepEqual(decoded, got) {
		t.Fatalf("artifact = %#v, %v", decoded, err)
	}
}

func TestCoverageRoundsOnlyWrittenAdvisory(t *testing.T) {
	root := coverageRoot(t)
	writeProfile(t, root, "mode: set\nexample.test/mod/a/a.go:1.1,1.2 1 1\nexample.test/mod/a/a.go:2.1,2.2 2 0\n")
	wanted := map[string][]string{"example.test/mod/a": {"a/a.go"}}
	parsed, err := parseCoverageProfile(mustReadProfile(t, root), wanted)
	if err != nil {
		t.Fatal(err)
	}
	if parsed[0].Statements == 33.3 {
		t.Fatal("parser rounded the underlying percentage")
	}
	got, err := writeCoverageAdvisory(root, coveragePlan(ChangedPath{Path: "a/a.go", Status: "M", Kind: PathProduction}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Packages[0].Statements != 33.3 {
		t.Fatalf("rendered percentage = %v, want 33.3", got.Packages[0].Statements)
	}
}

func TestCoverageAtomicReplacement(t *testing.T) {
	root := coverageRoot(t)
	plan := coveragePlan(ChangedPath{Path: "a/a.go", Status: "M", Kind: PathProduction})
	writeProfile(t, root, "mode: set\nexample.test/mod/a/a.go:1.1,1.2 1 0\n")
	if _, err := writeCoverageAdvisory(root, plan); err != nil {
		t.Fatal(err)
	}
	writeProfile(t, root, "mode: set\nexample.test/mod/a/a.go:1.1,1.2 1 1\n")
	if _, err := writeCoverageAdvisory(root, plan); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "build", "iteration", coverageDigest, "coverage.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "\x00") || !strings.Contains(string(data), "100") {
		t.Fatalf("replacement not complete: %s", data)
	}
}

func TestCoverageRejectsSymlinkPaths(t *testing.T) {
	root := coverageRoot(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "build")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := writeCoverageAdvisory(root, coveragePlan(ChangedPath{Path: "a/a.go", Status: "M", Kind: PathProduction})); err == nil {
		t.Fatal("build symlink accepted")
	}
	if _, err := os.Lstat(filepath.Join(outside, "iteration")); !os.IsNotExist(err) {
		t.Fatalf("outside mutated: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "build")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "build"), 0o700); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(outside, "coverage.out")
	if err := os.WriteFile(profile, []byte("mode: set\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(profile, filepath.Join(root, "build", "coverage.out")); err != nil {
		t.Skipf("profile symlink unavailable: %v", err)
	}
	if _, err := writeCoverageAdvisory(root, coveragePlan(ChangedPath{Path: "a/a.go", Status: "M", Kind: PathProduction})); err == nil {
		t.Fatal("profile symlink accepted")
	}
}

func coverageRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/mod\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func coveragePlan(changes ...ChangedPath) Plan {
	return Plan{DiffDigest: coverageDigest, Changed: changes}
}

func writeProfile(t *testing.T, root, profile string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "build"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "build", "coverage.out"), []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustReadProfile(t *testing.T, root string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "build", "coverage.out"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}
