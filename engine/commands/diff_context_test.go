package commands

import (
	"strings"
	"testing"
)

func TestParseDiffContextHunkIsolationAndFullPatch(t *testing.T) {
	var body strings.Builder
	body.WriteString("diff --git a/one.txt b/one.txt\nindex 1..2 100644\n--- a/one.txt\n+++ b/one.txt\n@@ -1 +1 @@\n-old\n+new\n@@ -10 +10 @@\n-before\n+after\n")
	body.WriteString("diff --git a/two.txt b/two.txt\nindex 1..2 100644\n--- a/two.txt\n+++ b/two.txt\n@@ -1,205 +1,205 @@\n")
	for i := 0; i < 205; i++ {
		body.WriteString(" line\n")
	}
	files, err := ParseDiffContext(body.String())
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || len(files[0].Hunks) != 2 || len(files[1].Patch) <= 200*6 {
		t.Fatalf("unexpected files: %#v", files)
	}
	if strings.Contains(files[0].Hunks[1].Patch, "-old\n+new") || !strings.Contains(files[0].Hunks[1].Patch, "@@ -10 +10 @@") {
		t.Fatalf("second hunk leaked first hunk: %q", files[0].Hunks[1].Patch)
	}
}

func TestParseDiffContextQuotedRenameAddDeleteAndZeroCount(t *testing.T) {
	patch := "diff --git \"a/old name\" \"b/新 名.txt\"\nsimilarity index 100%\nrename from old name\nrename to 新 名.txt\n" +
		"diff --git a/new.txt b/new.txt\nnew file mode 100644\n--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1 @@\n+new\n" +
		"diff --git a/gone.txt b/gone.txt\ndeleted file mode 100644\n--- a/gone.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-old\n"
	files, err := ParseDiffContext(patch)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{files[0].Path, files[1].Path, files[2].Path}; strings.Join(got, ",") != "新 名.txt,new.txt,gone.txt" {
		t.Fatalf("paths = %#v", got)
	}
	if files[0].Hunks != nil || files[1].Hunks[0].OldLines != 0 || files[2].Hunks[0].NewLines != 0 {
		t.Fatalf("unexpected hunk ranges: %#v", files)
	}
}

func TestParseDiffContextNoNewlineBinaryAndModeOnly(t *testing.T) {
	patch := "diff --git a/text b/text\n--- a/text\n+++ b/text\n@@ -1 +1 @@\n-old\n\\ No newline at end of file\n+new\n\\ No newline at end of file\n" +
		"diff --git a/image.png b/image.png\nindex 1..2 100644\nBinary files a/image.png and b/image.png differ\n" +
		"diff --git a/mode b/mode\nold mode 100644\nnew mode 100755\n"
	files, err := ParseDiffContext(patch)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(files[0].Hunks[0].Patch, "\\ No newline at end of file") {
		t.Fatal("no-newline marker was lost")
	}
	if files[1].UnavailableReason == "" || len(files[1].Hunks) != 0 || files[2].Path != "mode" || len(files[2].Hunks) != 0 {
		t.Fatalf("unexpected binary/mode entries: %#v", files)
	}
}

func TestParseDiffContextRejectsInvalidInputs(t *testing.T) {
	bad := []string{
		"diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old\n", // count mismatch
		"diff --git a/x b/x\n@@@ -1 +1 +1 @@@\n",                    // combined
		"diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -x +1 @@\n+ok\n",  // header
		"diff --git a/x b/x\n@@broken\n",                            // header
		"diff --git a/x b/x\x00\n",
		"diff --git a/x b/x\n\xff",
		strings.Repeat("x", MaxDiffContextBytes+1),
	}
	for _, patch := range bad {
		if _, err := ParseDiffContext(patch); err == nil {
			t.Fatalf("ParseDiffContext accepted %q", patch[:min(len(patch), 40)])
		}
	}
}

func TestParseDiffContextResourceLimits(t *testing.T) {
	var files strings.Builder
	for i := 0; i <= maxDiffContextFiles; i++ {
		files.WriteString("diff --git a/x b/x\n")
	}
	if _, err := ParseDiffContext(files.String()); err == nil {
		t.Fatal("accepted too many files")
	}
	var hunks strings.Builder
	hunks.WriteString("diff --git a/x b/x\n--- a/x\n+++ b/x\n")
	for i := 0; i <= maxDiffContextHunks; i++ {
		hunks.WriteString("@@ -0,0 +0,0 @@\n")
	}
	if _, err := ParseDiffContext(hunks.String()); err == nil {
		t.Fatal("accepted too many hunks")
	}
}

func TestDiffContextPathsPreserveLiteralRenameAndTransportPrefix(t *testing.T) {
	patch := "diff --git a/a/inner.txt b/a/inner.txt\n--- a/a/inner.txt\n+++ b/a/inner.txt\n@@ -1 +1 @@\n-old\n+new\n" +
		"diff --git a/old b/a/inner.txt\nsimilarity index 100%\nrename from old\nrename to a/inner.txt\n" +
		"diff --git a/path with spaces b/path with spaces\nold mode 100644\nnew mode 100755\n"
	files, err := ParseDiffContext(patch)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{files[0].Path, files[1].Path, files[2].Path}
	if want := "a/inner.txt,a/inner.txt,path with spaces"; strings.Join(got, ",") != want {
		t.Fatalf("paths = %#v, want %q", got, want)
	}
}

func TestDiffContextRejectsUntrustedMetadataAndHunkHeaders(t *testing.T) {
	bad := []string{
		"diff --git a/x b/x\nnot a Git patch\n",
		"diff --git a/x b/x\n@@ -1 +1 @@\n-old\n+new\n",
		"diff --git a/x b/x\n\\ No newline at end of file\n",
		"diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -0,0 +0,0 @@\n\\ No newline at end of file\n",
		"diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -+1 +1 @@\n-old\n+new\n",
		"diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@junk\n-old\n+new\n",
	}
	for _, patch := range bad {
		if _, err := ParseDiffContext(patch); err == nil {
			t.Fatalf("accepted invalid patch %q", patch)
		}
	}
}

func TestDiffContextRejectsExpandedHunkRetention(t *testing.T) {
	path := strings.Repeat("segment/", 400) + "file"
	var patch strings.Builder
	patch.WriteString("diff --git a/" + path + " b/" + path + "\n--- a/" + path + "\n+++ b/" + path + "\n")
	for i := 0; i < 900; i++ {
		patch.WriteString("@@ -1 +1 @@\n-old\n+new\n")
	}
	if len(patch.String()) >= MaxDiffContextBytes {
		t.Fatal("fixture must exercise expansion rather than input-size rejection")
	}
	if _, err := ParseDiffContext(patch.String()); err == nil {
		t.Fatal("accepted hunk patches whose retained expansion exceeds the budget")
	}
}
