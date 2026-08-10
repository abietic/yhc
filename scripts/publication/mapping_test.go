package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParsePublicationMappingsExactDirectoryAndTests(t *testing.T) {
	manifest := []byte(`version: 4
files:
  - path: src/source.ts
    targets: [engine/exact.go, tools/]
    tests: [engine/exact_test.go]
`)
	mappings, err := parsePublicationMappings(manifest, []string{"engine/exact.go", "engine/exact_test.go", "tools/a.go", "tools/deep/b.go", "other.go"})
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{"engine/exact.go", "engine/exact_test.go", "tools/a.go", "tools/deep/b.go"} {
		if !mappings.mapped(candidate) {
			t.Fatalf("expected %q to be mapped", candidate)
		}
	}
	if mappings.mapped("other.go") {
		t.Fatal("unmapped candidate was authorized")
	}
}

func TestParsePublicationMappingsDirectoryNeedsCandidateDescendant(t *testing.T) {
	manifest := []byte("version: 4\nfiles:\n  - path: src/source.ts\n    targets: [engine]\n")
	mappings, err := parsePublicationMappings(manifest, []string{"engine"})
	if err != nil {
		t.Fatal(err)
	}
	if !mappings.mapped("engine") {
		t.Fatal("exact mapping was not retained")
	}
	if mappings.mapped("engine/imaginary.go") {
		t.Fatal("non-candidate path was mapped")
	}
}

func TestParsePublicationMappingsRejectsUnsafeMalformedAndMultipleDocuments(t *testing.T) {
	for _, manifest := range []string{
		"version: 3\nfiles: []\n",
		"version: 4\nfiles:\n  - path: src/a.ts\n    targets: [/absolute.go]\n",
		"version: 4\nfiles:\n  - path: src/a.ts\n    targets: [../escape.go]\n",
		"version: 4\nfiles:\n  - path: src/a.ts\n    targets: [a\\\\b.go]\n",
		"version: 4\nfiles:\n  - path: src/a.ts\n---\nversion: 4\nfiles: []\n",
		"version: 4\nfiles:\n  - path: src/a.ts\n    unexpected: value\n",
	} {
		if _, err := parsePublicationMappings([]byte(manifest), []string{"engine/a.go"}); err == nil {
			t.Fatalf("accepted invalid manifest: %q", manifest)
		}
	}
	if _, err := parsePublicationMappings(bytes.Repeat([]byte{'x'}, maximumMappingBytes+1), nil); err == nil {
		t.Fatal("accepted oversized mapping manifest")
	}
}

func TestParsePublicationMappingsRejectsDuplicateReferencePath(t *testing.T) {
	manifest := []byte("version: 4\nfiles:\n  - path: src/a.ts\n  - path: src/a.ts\n")
	if _, err := parsePublicationMappings(manifest, []string{"engine/a.go"}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate path error = %v", err)
	}
}

func TestBuildInventoryUsesTrackedMappingManifestNotWorktreeOrReference(t *testing.T) {
	tracked := "version: 4\nfiles:\n  - path: src/a.ts\n    targets: [README.md]\n"
	repo := newPublicationRepo(t, map[string]string{
		"README.md":                        "public\n",
		migrationMappingManifest:           tracked,
		".gitignore":                       ".reference/\n",
		"quality/dependency-licenses.yaml": "licenses: []\n",
		"sbom.cdx.json":                    "{}\n",
	})
	writePublicationFile(t, repo, migrationMappingManifest, "version: 4\nfiles: []\n")
	writePublicationFile(t, repo, ".reference/secret.txt", "never read")
	if runtime.GOOS != "windows" {
		if err := os.Chmod(filepath.Join(repo, ".reference", "secret.txt"), 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(repo, ".reference", "secret.txt"), 0o600) })
	}
	config := Config{Mappings: MappingPolicy{Manifest: migrationMappingManifest}, Rules: []PathRule{
		{ID: "readme", Include: []string{"README.md"}, Class: "reference-informed-independent", Decision: "include", Evidence: []string{"review"}},
		{ID: "manifest", Include: []string{migrationMappingManifest}, Class: "project-owned-original", Decision: "include", Evidence: []string{"review"}},
		{ID: "ignore", Include: []string{".gitignore"}, Class: "project-owned-original", Decision: "include", Evidence: []string{"review"}},
		{ID: "licenses", Include: []string{"quality/**"}, Class: "project-owned-original", Decision: "include", Evidence: []string{"review"}},
		{ID: "sbom", Include: []string{"sbom.cdx.json"}, Class: "project-owned-original", Decision: "include", Evidence: []string{"review"}},
	}}
	inRepo(t, repo, func() {
		inventory, err := buildInventory(context.Background(), config)
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range inventory.Files {
			if file.Path == "README.md" && !file.Mapped {
				t.Fatal("tracked mapping manifest was not used")
			}
		}
	})
}
