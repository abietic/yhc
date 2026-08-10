package permission

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePermissionPathIncludesExistingSymlinkTarget(t *testing.T) {
	cwd := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(cwd, "secret.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	resolution := ResolvePermissionPath(link, cwd)
	if !containsPermissionPath(resolution.Paths, link) || !containsPermissionPath(resolution.Paths, outside) {
		t.Fatalf("resolution paths = %#v, want logical and target", resolution.Paths)
	}
	if PermissionPathsWithinRoots(resolution, []string{cwd}) {
		t.Fatal("symlink target outside cwd was treated as inside")
	}
}

func TestResolvePermissionPathResolvesNonexistentTailThroughSymlinkParent(t *testing.T) {
	cwd := t.TempDir()
	outside := t.TempDir()
	linkDir := filepath.Join(cwd, "generated")
	if err := os.Symlink(outside, linkDir); err != nil {
		t.Fatal(err)
	}

	requested := filepath.Join(linkDir, "new", "file.txt")
	resolved := filepath.Join(outside, "new", "file.txt")
	resolution := ResolvePermissionPath(requested, cwd)
	if !containsPermissionPath(resolution.Paths, resolved) {
		t.Fatalf("resolution paths = %#v, want %q", resolution.Paths, resolved)
	}
	if AcceptEditsCheck("Write", map[string]any{"file_path": requested}, cwd) {
		t.Fatal("acceptEdits auto-allowed a create through an escaping symlink parent")
	}
}

func TestPermissionPathsWithinAdditionalRoot(t *testing.T) {
	cwd := t.TempDir()
	additional := t.TempDir()
	path := filepath.Join(additional, "file.txt")
	resolution := ResolvePermissionPath(path, cwd)
	if !PermissionPathsWithinRoots(resolution, []string{cwd, additional}) {
		t.Fatal("additional working root did not allow its child")
	}
	if !AcceptEditsCheck("Write", map[string]any{"file_path": path}, cwd, additional) {
		t.Fatal("acceptEdits did not honor the additional working root")
	}
}

func TestResolvePermissionPathMarksUNCUnsafe(t *testing.T) {
	resolution := ResolvePermissionPath(`\\server\share\secret.txt`, t.TempDir())
	if !resolution.Unsafe || PermissionPathsWithinRoots(resolution, []string{t.TempDir()}) {
		t.Fatalf("UNC resolution = %#v, want unsafe and outside roots", resolution)
	}
}

func containsPermissionPath(paths []string, want string) bool {
	want = filepath.Clean(want)
	for _, path := range paths {
		if filepath.Clean(path) == want {
			return true
		}
	}
	return false
}
