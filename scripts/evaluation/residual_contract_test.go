package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResidualUsesCurrentRuntimeDirectory(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind string
		want string
	}{
		{"current", "current", ""},
		{"retired-directory", "retired", "residual_unexpected"},
		{"extra-product-file", "extra", "residual_unexpected"},
		{"oversized-metadata", "oversized", "runtime_metadata_invalid"},
		{"linked-metadata", "link", "runtime_metadata_invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.kind == "link" && runtime.GOOS == "windows" {
				t.Skip("symlinks require additional Windows privileges")
			}
			repo := privateTempDir(t)
			write := func(name, content string) {
				t.Helper()
				path := filepath.Join(repo, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			write("tracked.txt", "fixture")
			write("greet/greet.go", "package greet\n")
			if err := initializeRepository(repo); err != nil {
				t.Fatal(err)
			}
			write("greet/decorate.go", "package greet\n")
			dir := ".yhc"
			if tc.kind == "retired" {
				dir = ".eino-agent"
			}
			write(dir+"/state.json", "{}")
			switch tc.kind {
			case "extra":
				write("unexpected.txt", "extra")
			case "oversized":
				write(dir+"/state.json", "oversized")
			case "link":
				if err := os.Symlink(filepath.Join(repo, "tracked.txt"), filepath.Join(repo, dir, "linked")); err != nil {
					t.Fatal(err)
				}
			}
			manifest := scenarioManifest{}
			manifest.Expected.Addition = "greet/decorate.go"
			manifest.Execution.ArtifactMaxBytes = 4
			changes, state, err := inspectResidual(repo, manifest)
			if (err == nil) != (tc.want == "") || (err != nil && errorCode(err) != tc.want) {
				t.Fatalf("error=%v, want code %q", err, tc.want)
			}
			if err == nil && (len(changes) != 1 || changes[0] != manifest.Expected.Addition || state != "classified") {
				t.Fatalf("changes=%v, state=%q", changes, state)
			}
			if tc.kind == "current" {
				before, err := treeDigest(repo)
				if err != nil {
					t.Fatal(err)
				}
				write(dir+"/state.json", "changed runtime state")
				after, err := treeDigest(repo)
				if err != nil || before != after {
					t.Fatalf("runtime state changed product digest: err=%v", err)
				}
				write("greet/decorate.go", "package changed\n")
				after, err = treeDigest(repo)
				if err != nil || before == after {
					t.Fatalf("product change missing from digest: err=%v", err)
				}
			}
		})
	}
}
