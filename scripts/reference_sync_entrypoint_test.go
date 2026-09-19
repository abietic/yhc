package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReferenceSyncEntrypointIsRegistered(t *testing.T) {
	t.Parallel()

	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	repositoryRoot := filepath.Clean(filepath.Join(workingDir, ".."))
	entrypoint := filepath.Join(repositoryRoot, "scripts", "reference_sync")
	info, err := os.Stat(entrypoint)
	if err != nil {
		t.Fatalf("reference sync entrypoint: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("reference sync entrypoint %q is not a directory", entrypoint)
	}
	configPath := filepath.Join(repositoryRoot, "docs", "migration", "reference", "reference-repositories.yaml")
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("reference sync configuration: %v", err)
	}

	cmd := exec.Command("go", "list", "./scripts/reference_sync")
	cmd.Dir = repositoryRoot
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("list reference sync package: %v: %s", err, output)
	}
	if got := strings.TrimSpace(string(output)); got != "github.com/abietic/yhc/scripts/reference_sync" {
		t.Fatalf("reference sync package = %q", got)
	}
}
