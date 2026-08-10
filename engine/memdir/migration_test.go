package memdir

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/internal/identity"
	"github.com/abietic/yhc/internal/statemigration"
	"github.com/abietic/yhc/internal/statepath"
)

func TestMemoryDefaultsWriteOnlyYHCRoots(t *testing.T) {
	clearMemdirRuntimeEnvironment(t)
	home := t.TempDir()
	project := t.TempDir()
	t.Setenv("HOME", home)
	withProjectRoot(t, project)

	userRoots, err := statepath.UserRoots(home)
	if err != nil {
		t.Fatal(err)
	}
	userRoot := userRoots.Canonical
	if got := GetConfigHomeDir(); got != userRoot {
		t.Fatalf("config home = %q, want %q", got, userRoot)
	}
	if got := GetMemoryBaseDir(); got != userRoot {
		t.Fatalf("memory base = %q, want %q", got, userRoot)
	}
	projectStateRoots, err := statepath.ProjectRoots(project)
	if err != nil {
		t.Fatal(err)
	}
	canonicalProject := filepath.Dir(projectStateRoots.Canonical)
	if got := GetAgentMemoryRootForProject(ScopeProject, project); got != filepath.Join(canonicalProject, identity.ProjectDirName, "agent-memory") {
		t.Fatalf("project agent root = %q", got)
	}
	if got := GetAgentMemoryRootForProject(ScopeLocal, project); got != filepath.Join(canonicalProject, identity.ProjectDirName, "agent-memory-local") {
		t.Fatalf("local agent root = %q", got)
	}

	if _, err := BuildUnifiedMemoryPrompt(project); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []AgentMemoryScope{ScopeUser, ScopeProject, ScopeLocal} {
		if _, err := BuildAgentMemoryPrompt("reviewer", scope, project); err != nil {
			t.Fatal(err)
		}
	}
	for _, root := range []string{
		GetAutoMemPathForProject(project),
		GetAgentMemoryDirForProject("reviewer", ScopeUser, project),
		GetAgentMemoryDirForProject("reviewer", ScopeProject, project),
		GetAgentMemoryDirForProject("reviewer", ScopeLocal, project),
	} {
		info, err := os.Stat(root)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("canonical memory root mode = %v err=%v path=%q", infoMode(info), err, root)
		}
	}

	for _, legacy := range []string{
		filepath.Join(home, identity.LegacyDirName),
		filepath.Join(project, identity.LegacyDirName),
	} {
		if _, err := os.Lstat(legacy); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("legacy root was written: path=%q err=%v", legacy, err)
		}
	}
}

func infoMode(info os.FileInfo) os.FileMode {
	if info == nil {
		return 0
	}
	return info.Mode().Perm()
}

func TestMemoryMigrationRebasesProjectEncoding(t *testing.T) {
	clearMemdirRuntimeEnvironment(t)
	home := t.TempDir()
	realProject := filepath.Join(t.TempDir(), "real-project")
	if err := os.Mkdir(realProject, 0o700); err != nil {
		t.Fatal(err)
	}
	projectAlias := filepath.Join(t.TempDir(), "project-alias")
	if err := os.Symlink(realProject, projectAlias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	roots, err := statepath.UserRoots(home)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := MemoryMigrationSpec("memory", "user", projectAlias)
	if err != nil {
		t.Fatal(err)
	}
	legacyEncoding := sanitizePath(filepath.Clean(projectAlias))
	projectRoots, err := statepath.ProjectRoots(realProject)
	if err != nil {
		t.Fatal(err)
	}
	canonicalEncoding := sanitizePath(filepath.Dir(projectRoots.Canonical))
	if legacyEncoding == canonicalEncoding {
		t.Fatal("fixture did not produce distinct legacy and canonical encodings")
	}
	if spec.SourceRel != filepath.ToSlash(filepath.Join("projects", legacyEncoding, "memory")) ||
		spec.TargetRel != filepath.ToSlash(filepath.Join("projects", canonicalEncoding, "memory")) {
		t.Fatalf("migration rels = %q -> %q", spec.SourceRel, spec.TargetRel)
	}
	legacyArtifact := writeMemoryMigrationTree(t, roots.Legacy, spec.SourceRel, map[string]string{
		"MEMORY.md":          "index\n",
		"logs/2026/08/10.md": "daily\n",
	})

	result, err := (statemigration.Importer{}).Import(t.Context(), roots, spec)
	if err != nil || result.Status != statemigration.StatusImported {
		t.Fatalf("import result=%#v err=%v", result, err)
	}
	target := filepath.Join(roots.Canonical, filepath.FromSlash(spec.TargetRel))
	data, err := os.ReadFile(filepath.Join(target, "MEMORY.md"))
	if err != nil || string(data) != "index\n" {
		t.Fatalf("canonical memory = %q err=%v", data, err)
	}
	if _, err := os.Stat(legacyArtifact); err != nil {
		t.Fatalf("legacy artifact changed: %v", err)
	}
	oldTarget := filepath.Join(roots.Canonical, "projects", legacyEncoding, "memory")
	if _, err := os.Lstat(oldTarget); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old project encoding retained: err=%v", err)
	}
}

func TestMemoryMigrationRejectsSymlinkUnknownEntryAndCollision(t *testing.T) {
	clearMemdirRuntimeEnvironment(t)
	project := t.TempDir()

	t.Run("symlink", func(t *testing.T) {
		roots, spec := newMemoryMigrationFixture(t, project, "memory", "user")
		artifact := writeMemoryMigrationTree(t, roots.Legacy, spec.SourceRel, map[string]string{"MEMORY.md": "index\n"})
		if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), filepath.Join(artifact, "escape.md")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		assertMemoryMigrationUnsafe(t, roots, spec)
	})

	t.Run("unknown entry", func(t *testing.T) {
		roots, spec := newMemoryMigrationFixture(t, project, "memory", "user")
		writeMemoryMigrationTree(t, roots.Legacy, spec.SourceRel, map[string]string{
			"MEMORY.md": "index\n",
			"cache.bin": "unknown",
		})
		assertMemoryMigrationUnsafe(t, roots, spec)
	})

	t.Run("credential content", func(t *testing.T) {
		roots, spec := newMemoryMigrationFixture(t, project, "memory", "user")
		credential := "gh" + "p_" + strings.Repeat("a", 36)
		writeMemoryMigrationTree(t, roots.Legacy, spec.SourceRel, map[string]string{
			"MEMORY.md": "credential: " + credential,
		})
		assertMemoryMigrationUnsafe(t, roots, spec)
	})

	t.Run("invalid metadata", func(t *testing.T) {
		roots, spec := newMemoryMigrationFixture(t, project, "agent-memory", "project")
		writeMemoryMigrationTree(t, roots.Legacy, spec.SourceRel, map[string]string{
			"reviewer/MEMORY.md":             "index\n",
			"reviewer/.snapshot-synced.json": `{"syncedFrom":"not-a-time"}`,
		})
		assertMemoryMigrationUnsafe(t, roots, spec)
	})

	t.Run("collision", func(t *testing.T) {
		roots, spec := newMemoryMigrationFixture(t, project, "agent-memory-local", "project")
		legacy := writeMemoryMigrationTree(t, roots.Legacy, spec.SourceRel, map[string]string{
			"reviewer/MEMORY.md": "legacy\n",
		})
		writeMemoryMigrationTree(t, roots.Canonical, spec.TargetRel, map[string]string{
			"reviewer/MEMORY.md": "canonical\n",
		})
		result, err := (statemigration.Importer{}).Import(t.Context(), roots, spec)
		if err != nil || result.Status != statemigration.StatusDestinationExists {
			t.Fatalf("collision result=%#v err=%v", result, err)
		}
		data, err := os.ReadFile(filepath.Join(legacy, "reviewer", "MEMORY.md"))
		if err != nil || string(data) != "legacy\n" {
			t.Fatalf("legacy collision source changed: %q err=%v", data, err)
		}
	})
}

func TestMemoryExplicitOverridesAreNeverMigrated(t *testing.T) {
	project := t.TempDir()
	override := t.TempDir() + string(filepath.Separator) + "selected" +
		string(filepath.Separator) + ".." + string(filepath.Separator) + "explicit-memory"
	tests := []struct {
		name  string
		env   string
		owner string
		scope string
	}{
		{name: "canonical auto path", env: "YHC_MEMORY_PATH_OVERRIDE", owner: "memory", scope: "user"},
		{name: "legacy auto path", env: "EINO_AGENT_MEMORY_PATH_OVERRIDE", owner: "memory", scope: "user"},
		{name: "canonical remote auto", env: "YHC_REMOTE_MEMORY_DIR", owner: "memory", scope: "user"},
		{name: "legacy remote auto", env: "EINO_AGENT_REMOTE_MEMORY_DIR", owner: "memory", scope: "user"},
		{name: "canonical config auto", env: "YHC_CONFIG_DIR", owner: "memory", scope: "user"},
		{name: "legacy config auto", env: "EINO_AGENT_CONFIG_DIR", owner: "memory", scope: "user"},
		{name: "canonical remote user agent", env: "YHC_REMOTE_MEMORY_DIR", owner: "agent-memory", scope: "user"},
		{name: "legacy config user agent", env: "EINO_AGENT_CONFIG_DIR", owner: "agent-memory", scope: "user"},
		{name: "canonical remote local agent", env: "YHC_REMOTE_MEMORY_DIR", owner: "agent-memory-local", scope: "project"},
		{name: "legacy remote local agent", env: "EINO_AGENT_REMOTE_MEMORY_DIR", owner: "agent-memory-local", scope: "project"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearMemdirRuntimeEnvironment(t)
			t.Setenv(test.env, override)
			_, err := MemoryMigrationSpec(test.owner, test.scope, project)
			if !errors.Is(err, ErrMemoryMigrationUnavailable) {
				t.Fatalf("migration error = %v, want unavailable", err)
			}
			if test.env == "YHC_MEMORY_PATH_OVERRIDE" {
				if got := GetAutoMemPathForProject(project); got != withTrailingSeparator(override) {
					t.Fatalf("explicit auto-memory path = %q, want exact %q", got, withTrailingSeparator(override))
				}
				if !IsAutoMemPath(filepath.Join(filepath.Clean(override), "topic.md")) {
					t.Fatal("exact lexical override lost auto-memory containment")
				}
			}
		})
	}
}

func TestMemoryMigrationLeavesLegacyTreeUnchanged(t *testing.T) {
	clearMemdirRuntimeEnvironment(t)
	project := t.TempDir()
	roots, spec := newMemoryMigrationFixture(t, project, "agent-memory", "project")
	legacy := writeMemoryMigrationTree(t, roots.Legacy, spec.SourceRel, map[string]string{
		"reviewer/MEMORY.md":             "index\n",
		"reviewer/notes.log":             "event\n",
		"reviewer/.snapshot-synced.json": `{"syncedFrom":"2026-08-10T01:02:03Z"}`,
	})
	oldTime := time.Unix(1_700_000_000, 0)
	if err := filepath.WalkDir(legacy, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		return os.Chtimes(path, oldTime, oldTime)
	}); err != nil {
		t.Fatal(err)
	}
	before := captureMemoryMigrationTree(t, legacy)

	result, err := (statemigration.Importer{}).Import(t.Context(), roots, spec)
	if err != nil || result.Status != statemigration.StatusImported {
		t.Fatalf("import result=%#v err=%v", result, err)
	}
	after := captureMemoryMigrationTree(t, legacy)
	if len(before) != len(after) {
		t.Fatalf("legacy entry count changed: before=%d after=%d", len(before), len(after))
	}
	for path, want := range before {
		if got, ok := after[path]; !ok || got != want {
			t.Fatalf("legacy entry changed at %q: before=%#v after=%#v", path, want, got)
		}
	}
}

func TestMemoryMigrationAcceptsLegacyWriterModesAndPrivatizesOutput(t *testing.T) {
	clearMemdirRuntimeEnvironment(t)
	project := t.TempDir()
	tests := []struct {
		name  string
		owner string
		scope string
		files map[string]string
	}{
		{
			name: "auto memory", owner: "memory", scope: "user",
			files: map[string]string{"MEMORY.md": "index\n", "logs/2026-08-10.md": "daily\n"},
		},
		{
			name: "user agent memory", owner: "agent-memory", scope: "user",
			files: map[string]string{"reviewer/MEMORY.md": "index\n"},
		},
		{
			name: "project agent memory", owner: "agent-memory", scope: "project",
			files: map[string]string{"reviewer/MEMORY.md": "index\n"},
		},
		{
			name: "local agent memory", owner: "agent-memory-local", scope: "project",
			files: map[string]string{"reviewer/MEMORY.md": "index\n"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			roots, spec := newMemoryMigrationFixture(t, project, test.owner, test.scope)
			legacy := writeMemoryMigrationTree(t, roots.Legacy, spec.SourceRel, test.files)
			chmodMemoryMigrationTree(t, roots.Legacy, 0o755, 0o644)
			legacyBefore := captureMemoryMigrationTree(t, legacy)

			result, err := (statemigration.Importer{}).Import(t.Context(), roots, spec)
			if err != nil || result.Status != statemigration.StatusImported {
				t.Fatalf("import result=%#v err=%v", result, err)
			}
			if after := captureMemoryMigrationTree(t, legacy); !memoryMigrationTreesEqual(legacyBefore, after) {
				t.Fatal("legacy writer-mode tree changed")
			}
			canonical := filepath.Join(roots.Canonical, filepath.FromSlash(spec.TargetRel))
			for path, item := range captureMemoryMigrationTree(t, canonical) {
				if item.mode.IsDir() && item.mode.Perm() != 0o700 {
					t.Fatalf("canonical directory mode at %q = %v", path, item.mode.Perm())
				}
				if item.mode.IsRegular() && item.mode.Perm() != 0o600 {
					t.Fatalf("canonical file mode at %q = %v", path, item.mode.Perm())
				}
			}
		})
	}

	t.Run("non-owner write remains unsafe", func(t *testing.T) {
		roots, spec := newMemoryMigrationFixture(t, project, "memory", "user")
		legacy := writeMemoryMigrationTree(t, roots.Legacy, spec.SourceRel, map[string]string{
			"MEMORY.md": "index\n",
		})
		chmodMemoryMigrationTree(t, roots.Legacy, 0o755, 0o644)
		if err := os.Chmod(filepath.Join(legacy, "MEMORY.md"), 0o666); err != nil {
			t.Fatal(err)
		}
		assertMemoryMigrationUnsafe(t, roots, spec)
	})
}

type memoryMigrationTreeEntry struct {
	mode    os.FileMode
	modTime time.Time
	data    string
}

func newMemoryMigrationFixture(
	t *testing.T,
	project string,
	owner string,
	scope string,
) (statepath.Roots, statemigration.ArtifactSpec) {
	t.Helper()
	var (
		roots statepath.Roots
		err   error
	)
	if scope == "user" {
		roots, err = statepath.UserRoots(t.TempDir())
	} else {
		roots, err = statepath.ProjectRoots(project)
	}
	if err != nil {
		t.Fatal(err)
	}
	spec, err := MemoryMigrationSpec(owner, scope, project)
	if err != nil {
		t.Fatal(err)
	}
	return roots, spec
}

func writeMemoryMigrationTree(
	t *testing.T,
	root string,
	relative string,
	files map[string]string,
) string {
	t.Helper()
	artifact := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(artifact, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := chmodMemoryMigrationParents(root, artifact); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		path := filepath.Join(artifact, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := chmodMemoryMigrationParents(artifact, filepath.Dir(path)); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return artifact
}

func chmodMemoryMigrationParents(root, leaf string) error {
	current := filepath.Clean(leaf)
	root = filepath.Clean(root)
	for {
		if err := os.Chmod(current, 0o700); err != nil {
			return err
		}
		if current == root {
			return nil
		}
		parent := filepath.Dir(current)
		if parent == current || !strings.HasPrefix(current, root+string(filepath.Separator)) {
			return errors.New("memory migration fixture escaped root")
		}
		current = parent
	}
}

func assertMemoryMigrationUnsafe(
	t *testing.T,
	roots statepath.Roots,
	spec statemigration.ArtifactSpec,
) {
	t.Helper()
	result, err := (statemigration.Importer{}).Inspect(t.Context(), roots, spec)
	if err == nil || result.Status != statemigration.StatusUnsafe {
		t.Fatalf("unsafe inspect result=%#v err=%v", result, err)
	}
}

func captureMemoryMigrationTree(t *testing.T, root string) map[string]memoryMigrationTreeEntry {
	t.Helper()
	result := make(map[string]memoryMigrationTreeEntry)
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		item := memoryMigrationTreeEntry{mode: info.Mode(), modTime: info.ModTime()}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			item.data = string(data)
		}
		result[relative] = item
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func chmodMemoryMigrationTree(
	t *testing.T,
	root string,
	directoryMode os.FileMode,
	fileMode os.FileMode,
) {
	t.Helper()
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return os.Chmod(path, directoryMode)
		}
		return os.Chmod(path, fileMode)
	}); err != nil {
		t.Fatal(err)
	}
}

func memoryMigrationTreesEqual(
	left map[string]memoryMigrationTreeEntry,
	right map[string]memoryMigrationTreeEntry,
) bool {
	if len(left) != len(right) {
		return false
	}
	for path, leftEntry := range left {
		if rightEntry, ok := right[path]; !ok || rightEntry != leftEntry {
			return false
		}
	}
	return true
}
