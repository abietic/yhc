package statepath

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/abietic/yhc/internal/identity"
)

func TestProjectAndUserRootsUseYHCAndPreserveLegacy(t *testing.T) {
	realParent := t.TempDir()
	linkParent := filepath.Join(t.TempDir(), "linked-parent")
	if err := os.Symlink(realParent, linkParent); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink creation is unavailable: %v", err)
		}
		t.Fatal(err)
	}

	missingProject := filepath.Join(linkParent, "missing", "project")
	projectRoots, err := ProjectRoots(missingProject)
	if err != nil {
		t.Fatal(err)
	}
	canonicalProject := filepath.Join(canonicalTestPath(t, realParent), "missing", "project")
	assertRoots(t, projectRoots, canonicalProject)

	userRoots, err := UserRoots(linkParent)
	if err != nil {
		t.Fatal(err)
	}
	assertRoots(t, userRoots, canonicalTestPath(t, realParent))

	for _, resolve := range []struct {
		name string
		call func(string) (Roots, error)
	}{
		{name: "project", call: ProjectRoots},
		{name: "user", call: UserRoots},
	} {
		t.Run(resolve.name+" rejects empty input", func(t *testing.T) {
			if _, err := resolve.call(" "); err == nil {
				t.Fatal("empty state root input was accepted")
			}
		})
	}

	dangling := filepath.Join(t.TempDir(), "dangling-root")
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing-target"), dangling); err != nil {
		t.Fatal(err)
	}
	for _, resolve := range []func(string) (Roots, error){ProjectRoots, UserRoots} {
		if _, err := resolve(dangling); err == nil {
			t.Fatal("dangling state-root symlink was accepted")
		}
	}

	fileRoot := filepath.Join(t.TempDir(), "state-root-file")
	if err := os.WriteFile(fileRoot, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, resolve := range []func(string) (Roots, error){ProjectRoots, UserRoots} {
		if _, err := resolve(fileRoot); err == nil {
			t.Fatal("regular-file state root was accepted")
		}
	}
}

func TestCanonicalAndLegacyOverridesPreserveExactPath(t *testing.T) {
	pair := identity.EnvPair{
		Canonical: "YHC_STATEPATH_TEST_OVERRIDE",
		Legacy:    "EINO_AGENT_STATEPATH_TEST_OVERRIDE",
	}
	defaults := testRoots(t)
	canonicalOverride := filepath.Join(t.TempDir(), "canonical-state")
	legacyOverride := filepath.Join(t.TempDir(), "legacy-state")

	t.Setenv(pair.Canonical, canonicalOverride)
	t.Setenv(pair.Legacy, legacyOverride)
	selection, err := ResolveOverride(pair, defaults)
	if err != nil {
		t.Fatal(err)
	}
	assertSelection(t, selection, canonicalOverride, defaults, SourceCanonicalEnv, false)

	unsetStatepathEnv(t, pair.Canonical)
	selection, err = ResolveOverride(pair, defaults)
	if err != nil {
		t.Fatal(err)
	}
	assertSelection(t, selection, legacyOverride, defaults, SourceLegacyEnv, false)
}

func TestEmptyCanonicalOverrideBlocksLegacyAndUsesDefault(t *testing.T) {
	pair := identity.EnvPair{
		Canonical: "YHC_STATEPATH_TEST_EMPTY",
		Legacy:    "EINO_AGENT_STATEPATH_TEST_EMPTY",
	}
	defaults := testRoots(t)
	t.Setenv(pair.Canonical, "")
	t.Setenv(pair.Legacy, filepath.Join(t.TempDir(), "must-not-win"))

	selection, err := ResolveOverride(pair, defaults)
	if err != nil {
		t.Fatal(err)
	}
	assertSelection(t, selection, defaults.Canonical, defaults, SourceCanonicalEnv, true)
}

func TestInvalidCanonicalOverrideDoesNotFallThrough(t *testing.T) {
	pair := identity.EnvPair{
		Canonical: "YHC_STATEPATH_TEST_INVALID",
		Legacy:    "EINO_AGENT_STATEPATH_TEST_INVALID",
	}
	defaults := testRoots(t)
	canonicalSentinel := "relative-canonical-private-sentinel"
	legacySentinel := filepath.Join(t.TempDir(), "legacy-private-sentinel")
	t.Setenv(pair.Canonical, canonicalSentinel)
	t.Setenv(pair.Legacy, legacySentinel)

	selection, err := ResolveOverride(pair, defaults)
	if err == nil {
		t.Fatal("relative canonical override was accepted")
	}
	if selection != (Selection{}) {
		t.Fatalf("failed selection = %#v, want zero value", selection)
	}
	for _, forbidden := range []string{canonicalSentinel, legacySentinel} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("override error leaked a path value: %q", err)
		}
	}
}

func assertRoots(t *testing.T, got Roots, base string) {
	t.Helper()
	want := Roots{
		Canonical: filepath.Join(base, identity.ProjectDirName),
		Legacy:    filepath.Join(base, identity.LegacyDirName),
	}
	if got != want {
		t.Fatalf("roots = %#v, want %#v", got, want)
	}
}

func assertSelection(
	t *testing.T,
	got Selection,
	effective string,
	roots Roots,
	source Source,
	migratable bool,
) {
	t.Helper()
	want := Selection{
		Effective:  effective,
		Roots:      roots,
		Source:     source,
		Migratable: migratable,
	}
	if got != want {
		t.Fatalf("selection = %#v, want %#v", got, want)
	}
}

func testRoots(t *testing.T) Roots {
	t.Helper()
	base := t.TempDir()
	return Roots{
		Canonical: filepath.Join(base, identity.ProjectDirName),
		Legacy:    filepath.Join(base, identity.LegacyDirName),
	}
}

func unsetStatepathEnv(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
}

func canonicalTestPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
