package commands

import (
	"fmt"
	"strings"
	"testing"
)

func TestDiffContextPreservesPathPrefixes(t *testing.T) {
	for _, path := range []string{"a/inner.txt", "b/inner.txt"} {
		patch := fmt.Sprintf("diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n@@ -1 +1 @@\n-old\n+new\n", path, path, path, path)
		files, err := ParseDiffContext(patch)
		if err != nil || len(files) != 1 || files[0].Path != path {
			t.Fatalf("path %q was changed: %#v, %v", path, files, err)
		}
	}
	files, err := ParseDiffContext("diff --git a/old b/b/new\nsimilarity index 100%\nrename from old\nrename to b/new\n")
	if err != nil || len(files) != 1 || files[0].Path != "b/new" {
		t.Fatalf("rename target lost its literal prefix: %#v, %v", files, err)
	}
}

func TestDiffContextRejectsMalformedMetadata(t *testing.T) {
	for _, body := range []string{
		"diff --git malformed\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old\n+new\n",
		"diff --git a/x b/x extra\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old\n+new\n",
		"diff --git \"a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old\n+new\n",
		"diff --git x y\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old\n+new\n",
		"diff --git a/x b/x\nnot a Git patch\n",
		"diff --git a/x b/x\n@@ -1 +1 @@\n-old\n+new\n",
		"diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -+1 +1 @@\n-old\n+new\n",
		"diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@junk\n-old\n+new\n",
	} {
		if _, err := ParseDiffContext(body); err == nil {
			t.Fatalf("malformed patch accepted: %q", body)
		}
	}
}

func TestDiffContextBoundsExpandedHunkHeaders(t *testing.T) {
	path := strings.Repeat("segment/", 400) + "file"
	var patch strings.Builder
	fmt.Fprintf(&patch, "diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n", path, path, path, path)
	for i := 1; i <= 900; i++ {
		fmt.Fprintf(&patch, "@@ -%d +%d @@\n-old\n+new\n", i, i)
	}
	if _, err := ParseDiffContext(patch.String()); err == nil {
		t.Fatal("small raw patch expanded its repeated hunk headers beyond the materialization budget")
	}
}

func TestDiffContextUnquotedRenameSpaces(t *testing.T) {
	patch := "diff --git a/old name b/new name\nsimilarity index 100%\nrename from old name\nrename to new name\n"
	files, err := ParseDiffContext(patch)
	if err != nil || len(files) != 1 || files[0].Path != "new name" || files[0].UnavailableReason != "" {
		t.Fatalf("valid unquoted Git rename rejected: %#v, %v", files, err)
	}
}
