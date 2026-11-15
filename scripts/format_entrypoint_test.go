package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFormattingEntrypointsPreserveBuildArtifacts(t *testing.T) {
	t.Parallel()
	root := formattingFixture(t)
	const unformatted = "package fixture\nfunc f( ) { }\n"
	const formatted = "package fixture\n\nfunc f() {}\n"
	sources := []string{"cmd", "engine", "internal", "scripts", "server", "tools", "third_party"}
	for _, dir := range sources {
		writeFormattingFixture(t, root, filepath.Join(dir, "source.go"), unformatted)
	}
	artifacts := []string{"build/history/original.go", "build/worktrees/old/source.go", ".reference/original.go"}
	for _, name := range artifacts {
		writeFormattingFixture(t, root, name, unformatted)
	}
	gofmt := filepath.Join(runtime.GOROOT(), "bin", "gofmt")
	// The standard formatter has the same traversal flags and ships with Go;
	// this exercises the real Make targets without installing another tool.
	if err := runFormattingMake(t, root, gofmt, "fmt-check"); err == nil {
		t.Fatal("fmt-check accepted unformatted repository source")
	}
	if err := runFormattingMake(t, root, gofmt, "fmt"); err != nil {
		t.Fatal(err)
	}
	for _, dir := range sources {
		assertFormattingFixture(t, root, filepath.Join(dir, "source.go"), formatted)
	}
	for _, name := range artifacts {
		assertFormattingFixture(t, root, name, unformatted)
	}
	if err := runFormattingMake(t, root, gofmt, "fmt-check"); err != nil {
		t.Fatalf("historical artifacts must not fail source formatting: %v", err)
	}
}

func TestFormattingCheckPropagatesFormatterFailure(t *testing.T) {
	t.Parallel()
	root := formattingFixture(t)
	// go rejects gofumpt's flags with stderr only. fmt-check must propagate
	// that failure instead of treating empty stdout as a clean source tree.
	if err := runFormattingMake(t, root, filepath.Join(runtime.GOROOT(), "bin", "go"), "fmt-check"); err == nil {
		t.Fatal("fmt-check hid a formatter failure")
	}
}

func formattingFixture(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is unavailable")
	}
	makefile, err := os.ReadFile(filepath.Join("..", "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writeFormattingFixture(t, root, "Makefile", string(makefile))
	for _, dir := range []string{"cmd", "engine", "internal", "scripts", "server", "tools", "third_party"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func runFormattingMake(t *testing.T, root, formatter, target string) error {
	t.Helper()
	cmd := exec.Command("make", "--no-print-directory", target, "GOFM="+formatter)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("%s: %s", target, output)
	}
	return err
}

func writeFormattingFixture(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertFormattingFixture(t *testing.T, root, name, expected string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != expected {
		t.Errorf("%s changed unexpectedly: %q", name, raw)
	}
}
