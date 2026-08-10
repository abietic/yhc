package main

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestCountLinesIncludesFinalUnterminatedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("first\nsecond"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := countLines(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Fatalf("countLines() = %d, want 2", got)
	}
}

func TestGoProjectHeadingUsesYHC(t *testing.T) {
	if goProjectHeading != "## Go Project (YHC)" {
		t.Fatalf("Go project heading = %q", goProjectHeading)
	}
}

func TestScanGoProjectUsesExplicitMetricSemantics(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "go.mod", "module example.test/scan\n")
	writeFixture(t, root, "pkg/sample.go", "package pkg\n\nfunc Value() int { return 1 }")
	writeFixture(t, root, "pkg/sample_test.go", "package pkg\n\nfunc helper() {}\n")
	writeFixture(t, root, "tools/read.go", "package tools\n\ntype ToolImpl struct{}\nfunc ReadTool() ToolImpl { return ToolImpl{} }\n")
	writeFixture(t, root, "tools/helper_test.go", "package tools\n\nfunc FakeTool() ToolImpl { return ToolImpl{} }\n")
	writeFixture(t, root, "engine/commands/cmd_one.go", "package commands\n")
	writeFixture(t, root, "engine/commands/cmd_two_test.go", "package commands\n")
	writeFixture(t, root, "docs/ignored.go", "package ignored\n")
	writeFixture(t, root, "scripts/ignored.go", "package ignored\n")

	stats := scanGoProject(root)
	if stats.ProductionFiles != 3 {
		t.Fatalf("ProductionFiles = %d, want 3", stats.ProductionFiles)
	}
	if stats.TestFiles != 3 {
		t.Fatalf("TestFiles = %d, want 3", stats.TestFiles)
	}
	if stats.Packages != 3 {
		t.Fatalf("Packages = %d, want 3", stats.Packages)
	}
	if stats.ToolConstructors != 1 {
		t.Fatalf("ToolConstructors = %d, want 1", stats.ToolConstructors)
	}
	if stats.CommandFiles != 1 {
		t.Fatalf("CommandFiles = %d, want 1", stats.CommandFiles)
	}
}

func TestScanReferenceCountsSourceAndTopLevelDirectories(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "src/query.ts", "export const query = 1;\n")
	writeFixture(t, root, "src/view.tsx", "export const view = <div />;\n")
	writeFixture(t, root, "src/commands/one/index.ts", "export {};\n")
	writeFixture(t, root, "src/tools/read/index.ts", "export {};\n")
	writeFixture(t, root, "node_modules/ignored.ts", "export {};\n")

	stats := scanReference(root)
	if stats.TSFiles != 4 {
		t.Fatalf("TSFiles = %d, want 4", stats.TSFiles)
	}
	if stats.CommandDirs != 1 {
		t.Fatalf("CommandDirs = %d, want 1", stats.CommandDirs)
	}
	if stats.ToolDirs != 1 {
		t.Fatalf("ToolDirs = %d, want 1", stats.ToolDirs)
	}
}

func writeFixture(t *testing.T, root, relative, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestScanPackageLivenessIdentifiesProductionImporter(t *testing.T) {
	root := t.TempDir()
	mod := "example.test/liveness"
	writeFixture(t, root, "go.mod", "module "+mod+"\n")
	writeFixture(t, root, "a/a.go", "package a\n")
	writeFixture(t, root, "b/b.go", "package b\n\nimport _ \""+mod+"/a\"\n")

	liveness, err := scanPackageLiveness(root)
	if err != nil {
		t.Fatalf("scanPackageLiveness() error = %v", err)
	}

	info := livenessInfo(t, liveness.ByPackage, mod+"/a")
	want := []string{mod + "/b"}
	if !reflect.DeepEqual(info.ProductionImporters, want) {
		t.Fatalf("ProductionImporters = %v, want %v", info.ProductionImporters, want)
	}
	if len(info.TestImporters) != 0 {
		t.Fatalf("TestImporters = %v, want empty", info.TestImporters)
	}
}

func TestScanPackageLivenessIdentifiesTestOnlyImporter(t *testing.T) {
	root := t.TempDir()
	mod := "example.test/liveness"
	writeFixture(t, root, "go.mod", "module "+mod+"\n")
	writeFixture(t, root, "a/a.go", "package a\n")
	writeFixture(t, root, "b/b_test.go", "package b\n\nimport _ \""+mod+"/a\"\n")
	writeFixture(t, root, "c/c.go", "package c\n")
	writeFixture(t, root, "c/c_test.go", "package c_test\n\nimport _ \""+mod+"/a\"\n")

	liveness, err := scanPackageLiveness(root)
	if err != nil {
		t.Fatalf("scanPackageLiveness() error = %v", err)
	}

	info := livenessInfo(t, liveness.ByPackage, mod+"/a")
	if len(info.ProductionImporters) != 0 {
		t.Fatalf("ProductionImporters = %v, want empty", info.ProductionImporters)
	}
	want := []string{mod + "/b", mod + "/c"}
	if !reflect.DeepEqual(info.TestImporters, want) {
		t.Fatalf("TestImporters = %v, want %v", info.TestImporters, want)
	}
}

func TestScanPackageLivenessPartitionsZeroProductionImportEntrypoints(t *testing.T) {
	root := t.TempDir()
	mod := "example.test/liveness"
	writeFixture(t, root, "go.mod", "module "+mod+"\n")
	writeFixture(t, root, "cmd/foo/main.go", "package main\n\nfunc main() {}\n")
	writeFixture(t, root, "lib/lib.go", "package lib\n")

	liveness, err := scanPackageLiveness(root)
	if err != nil {
		t.Fatalf("scanPackageLiveness() error = %v", err)
	}

	if !contains(liveness.ZeroProductionImportMain, mod+"/cmd/foo") {
		t.Fatalf("ZeroProductionImportMain missing %s: %v", mod+"/cmd/foo", liveness.ZeroProductionImportMain)
	}
	if !contains(liveness.ZeroProductionImportOther, mod+"/lib") {
		t.Fatalf("ZeroProductionImportOther missing %s: %v", mod+"/lib", liveness.ZeroProductionImportOther)
	}
}

func TestScanPackageLivenessProducesDeterministicSortedOutput(t *testing.T) {
	root := t.TempDir()
	mod := "example.test/liveness"
	writeFixture(t, root, "go.mod", "module "+mod+"\n")
	writeFixture(t, root, "z/z.go", "package z\n")
	writeFixture(t, root, "m/m.go", "package m\n\nimport _ \""+mod+"/a\"\n")
	writeFixture(t, root, "a/a.go", "package a\n")
	writeFixture(t, root, "m/m_test.go", "package m\n\nimport _ \""+mod+"/z\"\n")

	liveness, err := scanPackageLiveness(root)
	if err != nil {
		t.Fatalf("scanPackageLiveness() error = %v", err)
	}

	paths := make([]string, len(liveness.ByPackage))
	for i, info := range liveness.ByPackage {
		paths[i] = info.Path
		if !sort.StringsAreSorted(info.ProductionImporters) {
			t.Fatalf("production importers for %s not sorted: %v", info.Path, info.ProductionImporters)
		}
		if !sort.StringsAreSorted(info.TestImporters) {
			t.Fatalf("test importers for %s not sorted: %v", info.Path, info.TestImporters)
		}
	}
	if !sort.StringsAreSorted(paths) {
		t.Fatalf("by_package paths not sorted: %v", paths)
	}
	if !sort.StringsAreSorted(liveness.ZeroProductionImportMain) {
		t.Fatalf("ZeroProductionImportMain not sorted: %v", liveness.ZeroProductionImportMain)
	}
	if !sort.StringsAreSorted(liveness.ZeroProductionImportOther) {
		t.Fatalf("ZeroProductionImportOther not sorted: %v", liveness.ZeroProductionImportOther)
	}
}

func TestScanPackageLivenessIgnoresStandardLibraryImports(t *testing.T) {
	root := t.TempDir()
	mod := "example.test/liveness"
	writeFixture(t, root, "go.mod", "module "+mod+"\n")
	writeFixture(t, root, "a/a.go", "package a\n\nimport \"fmt\"\n\nvar _ = fmt.Println\n")

	liveness, err := scanPackageLiveness(root)
	if err != nil {
		t.Fatalf("scanPackageLiveness() error = %v", err)
	}

	info := livenessInfo(t, liveness.ByPackage, mod+"/a")
	if len(info.ProductionImporters) != 0 {
		t.Fatalf("ProductionImporters = %v, want empty (stdlib imports ignored)", info.ProductionImporters)
	}
	for _, p := range liveness.ByPackage {
		if p.Path == "fmt" {
			t.Fatal("stdlib package fmt should not appear in module liveness report")
		}
	}
}

func TestScanPackageLivenessFailsClearlyOnGoListError(t *testing.T) {
	root := t.TempDir()
	// No go.mod, so go list must fail.
	_, err := scanPackageLiveness(root)
	if err == nil {
		t.Fatal("expected error when go list has no module")
	}
	if !strings.Contains(err.Error(), "go list failed") {
		t.Fatalf("error does not mention go list failure: %v", err)
	}
}

func livenessInfo(t *testing.T, byPackage []PackageImporters, path string) *PackageImporters {
	t.Helper()
	for i := range byPackage {
		if byPackage[i].Path == path {
			return &byPackage[i]
		}
	}
	t.Fatalf("package %s not found in liveness report", path)
	return nil
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
