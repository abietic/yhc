package memdir

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/internal/identity"
	"github.com/abietic/yhc/internal/statepath"
)

func withEnv(t *testing.T, key, value string) {
	t.Helper()
	if strings.HasPrefix(key, "EINO_AGENT_") {
		setMemdirEnvironment(t, "YHC_"+strings.TrimPrefix(key, "EINO_AGENT_"), nil)
	}
	old, hadOld := os.LookupEnv(key)
	if value == "" {
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
			return
		}
	} else if err := os.Setenv(key, value); err != nil {
		t.Fatal(err)
		return
	}
	t.Cleanup(func() {
		if hadOld {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func TestYHCEnvironmentPrecedenceForMemoryPaths(t *testing.T) {
	booleanCases := []struct {
		name      string
		canonical *string
		legacy    *string
		want      bool
	}{
		{name: "canonical only", canonical: memdirEnvironmentValue("true")},
		{name: "legacy only", legacy: memdirEnvironmentValue("true")},
		{name: "both prefer canonical", canonical: memdirEnvironmentValue("yes"), legacy: memdirEnvironmentValue("false")},
		{name: "present empty canonical blocks legacy", canonical: memdirEnvironmentValue(""), legacy: memdirEnvironmentValue("true"), want: true},
		{name: "invalid canonical blocks legacy", canonical: memdirEnvironmentValue("on"), legacy: memdirEnvironmentValue("true"), want: true},
		{name: "neither", want: true},
	}
	for _, name := range []identity.RuntimeEnvName{
		identity.RuntimeEnvDisableAutoMemory,
		identity.RuntimeEnvSimple,
	} {
		t.Run(string(name), func(t *testing.T) {
			pair := name.Pair()
			for _, test := range booleanCases {
				t.Run(test.name, func(t *testing.T) {
					clearMemdirRuntimeEnvironment(t)
					setMemdirEnvironment(t, pair.Canonical, test.canonical)
					setMemdirEnvironment(t, pair.Legacy, test.legacy)
					if got := IsAutoMemoryEnabled(); got != test.want {
						t.Fatalf("IsAutoMemoryEnabled() = %t, want %t", got, test.want)
					}
				})
			}
		})
	}

	t.Run("REMOTE_MEMORY_DIR", func(t *testing.T) {
		pair := identity.RuntimeEnvRemoteMemoryDir.Pair()
		canonical := filepath.Join(t.TempDir(), "canonical")
		legacy := filepath.Join(t.TempDir(), "legacy")
		home := t.TempDir()
		t.Setenv("HOME", home)
		defaultRoot, err := statepath.UserRoots(home)
		if err != nil {
			t.Fatal(err)
		}
		testMemdirStringEnvironment(t, pair, canonical, legacy, "relative", func(*testing.T) string {
			return GetMemoryBaseDir()
		}, func(*testing.T) string {
			return defaultRoot.Canonical
		}, defaultRoot.Canonical)
	})

	t.Run("CONFIG_DIR", func(t *testing.T) {
		pair := identity.RuntimeEnvConfigDir.Pair()
		canonical := filepath.Join(t.TempDir(), "canonical")
		legacy := filepath.Join(t.TempDir(), "legacy")
		home := t.TempDir()
		t.Setenv("HOME", home)
		defaultRoot, err := statepath.UserRoots(home)
		if err != nil {
			t.Fatal(err)
		}
		testMemdirStringEnvironment(t, pair, canonical, legacy, "relative", func(*testing.T) string {
			return GetConfigHomeDir()
		}, func(*testing.T) string {
			return defaultRoot.Canonical
		}, defaultRoot.Canonical)
	})

	t.Run("MEMORY_PATH_OVERRIDE", func(t *testing.T) {
		pair := identity.RuntimeEnvMemoryPathOverride.Pair()
		canonical := filepath.Join(t.TempDir(), "canonical")
		legacy := filepath.Join(t.TempDir(), "legacy")
		base := t.TempDir()
		project := filepath.Join(t.TempDir(), "project")
		defaultPath := filepath.Join(base, "projects", sanitizePath(resolveProjectRoot(project)), autoMemDirname) + string(filepath.Separator)
		tests := []struct {
			name      string
			canonical *string
			legacy    *string
			want      string
		}{
			{name: "canonical only", canonical: memdirEnvironmentValue(canonical), want: canonical + string(filepath.Separator)},
			{name: "legacy only", legacy: memdirEnvironmentValue(legacy), want: legacy + string(filepath.Separator)},
			{name: "both prefer canonical", canonical: memdirEnvironmentValue(canonical), legacy: memdirEnvironmentValue(legacy), want: canonical + string(filepath.Separator)},
			{name: "present empty canonical blocks legacy", canonical: memdirEnvironmentValue(""), legacy: memdirEnvironmentValue(legacy), want: defaultPath},
			{name: "invalid canonical blocks legacy", canonical: memdirEnvironmentValue("relative"), legacy: memdirEnvironmentValue(legacy), want: defaultPath},
			{name: "neither", want: defaultPath},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				clearMemdirRuntimeEnvironment(t)
				remotePair := identity.RuntimeEnvRemoteMemoryDir.Pair()
				setMemdirEnvironment(t, remotePair.Legacy, memdirEnvironmentValue(base))
				setMemdirEnvironment(t, pair.Canonical, test.canonical)
				setMemdirEnvironment(t, pair.Legacy, test.legacy)
				if got := GetAutoMemPathForProject(project); got != test.want {
					t.Fatalf("GetAutoMemPathForProject() = %q, want %q", got, test.want)
				}
				if got := HasAutoMemPathOverride(); got != (test.want != defaultPath) {
					t.Fatalf("HasAutoMemPathOverride() = %t", got)
				}
			})
		}
	})

	t.Run("TEAM_MEMORY_DIR", func(t *testing.T) {
		pair := identity.RuntimeEnvTeamMemoryDir.Pair()
		canonical := filepath.Join(t.TempDir(), "canonical") + string(filepath.Separator)
		legacy := filepath.Join(t.TempDir(), "legacy") + string(filepath.Separator)
		testMemdirStringEnvironment(t, pair, canonical, legacy, "relative", func(*testing.T) string {
			return GetTeamMemPath()
		}, func(*testing.T) string { return "" }, "")
	})
}

func testMemdirStringEnvironment(
	t *testing.T,
	pair identity.EnvPair,
	canonical string,
	legacy string,
	invalidCanonical string,
	evaluate func(*testing.T) string,
	defaultValue func(*testing.T) string,
	invalidWant string,
) {
	t.Helper()
	tests := []struct {
		name      string
		canonical *string
		legacy    *string
		want      func(*testing.T) string
	}{
		{name: "canonical only", canonical: memdirEnvironmentValue(canonical), want: func(*testing.T) string { return canonical }},
		{name: "legacy only", legacy: memdirEnvironmentValue(legacy), want: func(*testing.T) string { return legacy }},
		{name: "both prefer canonical", canonical: memdirEnvironmentValue(canonical), legacy: memdirEnvironmentValue(legacy), want: func(*testing.T) string { return canonical }},
		{name: "present empty canonical blocks legacy", canonical: memdirEnvironmentValue(""), legacy: memdirEnvironmentValue(legacy), want: defaultValue},
		{name: "invalid canonical blocks legacy", canonical: memdirEnvironmentValue(invalidCanonical), legacy: memdirEnvironmentValue(legacy), want: func(*testing.T) string { return invalidWant }},
		{name: "neither", want: defaultValue},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearMemdirRuntimeEnvironment(t)
			setMemdirEnvironment(t, pair.Canonical, test.canonical)
			setMemdirEnvironment(t, pair.Legacy, test.legacy)
			want := test.want(t)
			if got := evaluate(t); got != want {
				t.Fatalf("environment result = %q, want %q", got, want)
			}
		})
	}
}

func clearMemdirRuntimeEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []identity.RuntimeEnvName{
		identity.RuntimeEnvDisableAutoMemory,
		identity.RuntimeEnvSimple,
		identity.RuntimeEnvRemoteMemoryDir,
		identity.RuntimeEnvConfigDir,
		identity.RuntimeEnvMemoryPathOverride,
		identity.RuntimeEnvTeamMemoryDir,
	} {
		pair := name.Pair()
		setMemdirEnvironment(t, pair.Canonical, nil)
		setMemdirEnvironment(t, pair.Legacy, nil)
	}
}

func memdirEnvironmentValue(value string) *string { return &value }

func setMemdirEnvironment(t *testing.T, name string, value *string) {
	t.Helper()
	old, present := os.LookupEnv(name)
	t.Cleanup(func() {
		if present {
			_ = os.Setenv(name, old)
			return
		}
		_ = os.Unsetenv(name)
	})
	if value == nil {
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := os.Setenv(name, *value); err != nil {
		t.Fatal(err)
	}
}

func withProjectRoot(t *testing.T, root string) {
	t.Helper()
	old := GetProjectRoot()
	SetProjectRoot(root)
	t.Cleanup(func() { SetProjectRoot(old) })
}

func TestAutoMemoryPathResolutionAndGuards(t *testing.T) {
	base := t.TempDir()
	project := filepath.Join(t.TempDir(), "repo with spaces")
	withEnv(t, "EINO_AGENT_CONFIG_DIR", filepath.Join(base, "config"))
	withEnv(t, "EINO_AGENT_REMOTE_MEMORY_DIR", filepath.Join(base, "remote"))
	withEnv(t, "EINO_AGENT_MEMORY_PATH_OVERRIDE", "")
	withEnv(t, "EINO_AGENT_DISABLE_AUTO_MEMORY", "")
	withEnv(t, "EINO_AGENT_SIMPLE", "")
	withProjectRoot(t, project)

	if !IsAutoMemoryEnabled() {
		t.Fatal("auto memory should be enabled by default")
	}
	withEnv(t, "EINO_AGENT_DISABLE_AUTO_MEMORY", "true")
	if IsAutoMemoryEnabled() {
		t.Fatal("disable env should turn auto memory off")
	}
	_ = os.Unsetenv("EINO_AGENT_DISABLE_AUTO_MEMORY")
	withEnv(t, "EINO_AGENT_SIMPLE", "1")
	if IsAutoMemoryEnabled() {
		t.Fatal("simple mode should turn auto memory off")
	}
	_ = os.Unsetenv("EINO_AGENT_SIMPLE")

	autoPath := GetAutoMemPath()
	if !strings.HasPrefix(autoPath, filepath.Join(base, "remote", "projects")) {
		t.Fatalf("auto memory path did not use remote base: %q", autoPath)
	}
	if !strings.HasSuffix(autoPath, string(filepath.Separator)) {
		t.Fatalf("auto memory path should have trailing separator: %q", autoPath)
	}
	if strings.Contains(filepath.Base(filepath.Dir(autoPath)), " ") {
		t.Fatalf("project key should be sanitized: %q", autoPath)
	}
	if GetAutoMemEntrypoint() != filepath.Join(autoPath, EntrypointName) {
		t.Fatalf("unexpected entrypoint: %q", GetAutoMemEntrypoint())
	}
	if !IsAutoMemPath(filepath.Join(autoPath, "topic.md")) {
		t.Fatal("file under memory dir should be recognized")
	}
	if IsAutoMemPath(filepath.Join(base, "remote", "projects-other", "topic.md")) {
		t.Fatal("unrelated prefix should not be recognized as memory path")
	}

	override := filepath.Join(base, "override")
	withEnv(t, "EINO_AGENT_MEMORY_PATH_OVERRIDE", override)
	if !HasAutoMemPathOverride() {
		t.Fatal("valid absolute override should be accepted")
	}
	if got := GetAutoMemPath(); got != override+string(filepath.Separator) {
		t.Fatalf("override path = %q", got)
	}
	_ = os.Setenv("EINO_AGENT_MEMORY_PATH_OVERRIDE", "relative")
	if HasAutoMemPathOverride() {
		t.Fatal("relative override should be rejected")
	}
}

func TestDailyAndAgentMemoryPaths(t *testing.T) {
	base := t.TempDir()
	project := t.TempDir()
	withEnv(t, "EINO_AGENT_REMOTE_MEMORY_DIR", base)
	withEnv(t, "EINO_AGENT_MEMORY_PATH_OVERRIDE", "")
	withProjectRoot(t, project)
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
		return
	}
	if err := os.Chdir(project); err != nil {
		t.Fatal(err)
		return
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })
	projectCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
		return
	}

	if got := GetAutoMemDailyLogPath("2026-06-13"); got != filepath.Join(GetAutoMemPath(), "logs", "2026", "06", "2026-06-13.md") {
		t.Fatalf("daily path = %q", got)
	}
	if got := GetAutoMemDailyLogPath("bad-date"); got != filepath.Join(GetAutoMemPath(), "logs", "bad-date.md") {
		t.Fatalf("fallback daily path = %q", got)
	}

	userDir := GetAgentMemoryDir("writer:go", ScopeUser)
	if want := filepath.Join(base, "agent-memory", "writer-go") + string(filepath.Separator); userDir != want {
		t.Fatalf("user agent dir = %q want %q", userDir, want)
	}
	projectDir := GetAgentMemoryDir("writer:go", ScopeProject)
	if want := filepath.Join(projectCWD, identity.ProjectDirName, "agent-memory", "writer-go") + string(filepath.Separator); projectDir != want {
		t.Fatalf("project agent dir = %q want %q", projectDir, want)
	}
	localDir := GetAgentMemoryDir("writer:go", ScopeLocal)
	if wantPrefix := filepath.Join(base, "projects"); !strings.HasPrefix(localDir, wantPrefix) || !strings.Contains(localDir, "agent-memory-local") {
		t.Fatalf("local remote agent dir = %q", localDir)
	}
	if !IsAgentMemoryPath(filepath.Join(userDir, "note.md")) {
		t.Fatal("user agent memory path should be recognized")
	}
	if !IsAgentMemoryPath(filepath.Join(projectDir, "note.md")) {
		t.Fatal("project agent memory path should be recognized")
	}
}

func TestScanMemoryFilesFrontmatterSortingAndLimit(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("index"), 0o644); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatal(err)
		return
	}

	writeMemory := func(name, body string, mod time.Time) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
			return
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
			return
		}
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatal(err)
			return
		}
	}
	old := time.Now().Add(-2 * time.Hour)
	newer := time.Now().Add(-1 * time.Hour)
	writeMemory("older.md", "---\ndescription: old memory\ntype: project\n---\nbody", old)
	writeMemory("nested/newer.md", "---\ndescription: \"new memory\"\ntype: user\n---\nbody", newer)
	writeMemory("not-md.txt", "---\ndescription: ignored\n---\n", time.Now())

	headers, err := ScanMemoryFiles(dir)
	if err != nil {
		t.Fatalf("ScanMemoryFiles failed: %v", err)
		return
	}
	if len(headers) != 2 {
		t.Fatalf("expected two memory headers, got %#v", headers)
	}
	if headers[0].Filename != filepath.Join("nested", "newer.md") || headers[0].Description != "new memory" || headers[0].Type != MemoryTypeUser {
		t.Fatalf("unexpected newest header: %#v", headers[0])
	}
	if headers[1].Filename != "older.md" || headers[1].Description != "old memory" || headers[1].Type != MemoryTypeProject {
		t.Fatalf("unexpected older header: %#v", headers[1])
	}

	many := filepath.Join(t.TempDir(), "many")
	if err := os.MkdirAll(many, 0o755); err != nil {
		t.Fatal(err)
		return
	}
	for i := 0; i < MaxMemoryFiles+5; i++ {
		path := filepath.Join(many, fmt.Sprintf("%03d.md", i))
		if err := os.WriteFile(path, []byte("---\ntype: feedback\n---\n"), 0o644); err != nil {
			t.Fatal(err)
			return
		}
		mod := time.Now().Add(time.Duration(i) * time.Second)
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatal(err)
			return
		}
	}
	headers, err = ScanMemoryFiles(many)
	if err != nil {
		t.Fatal(err)
		return
	}
	if len(headers) != MaxMemoryFiles {
		t.Fatalf("expected cap at %d, got %d", MaxMemoryFiles, len(headers))
	}
	if headers[0].Filename != fmt.Sprintf("%03d.md", MaxMemoryFiles+4) {
		t.Fatalf("expected newest file first, got %q", headers[0].Filename)
	}
}

func TestMemoryAgeAndTypeParsing(t *testing.T) {
	if got := MemoryAge(time.Now().UnixMilli()); got != "today" {
		t.Fatalf("today age = %q", got)
	}
	if got := MemoryAge(time.Now().Add(-25 * time.Hour).UnixMilli()); got != "yesterday" {
		t.Fatalf("yesterday age = %q", got)
	}
	if got := MemoryAge(time.Now().Add(-72 * time.Hour).UnixMilli()); got != "3 days ago" {
		t.Fatalf("older age = %q", got)
	}
	if got := MemoryAgeDays(time.Now().Add(24 * time.Hour).UnixMilli()); got != 0 {
		t.Fatalf("future age should clamp to zero, got %d", got)
	}

	for _, typ := range ValidMemoryTypes {
		if got := ParseMemoryType(string(typ)); got != typ {
			t.Fatalf("ParseMemoryType(%q) = %q", typ, got)
		}
	}
	if got := ParseMemoryType("other"); got != "" {
		t.Fatalf("invalid type should return empty string, got %q", got)
	}
}

func TestEntrypointTruncationAndPromptLoading(t *testing.T) {
	base := t.TempDir()
	memPath := filepath.Join(base, "memory")
	withEnv(t, "EINO_AGENT_MEMORY_PATH_OVERRIDE", memPath)
	withEnv(t, "EINO_AGENT_DISABLE_AUTO_MEMORY", "")

	if err := EnsureMemoryDirExists(); err != nil {
		t.Fatalf("EnsureMemoryDirExists failed: %v", err)
		return
	}
	if _, err := os.Stat(memPath); err != nil {
		t.Fatalf("expected memory dir created: %v", err)
		return
	}

	if got := BuildMemoryPrompt(); got != "" {
		t.Fatalf("missing MEMORY.md should produce empty prompt, got %q", got)
	}
	if err := os.WriteFile(GetAutoMemEntrypoint(), []byte("  remembered fact  \n"), 0o644); err != nil {
		t.Fatal(err)
		return
	}
	if got := BuildMemoryPrompt(); got != "remembered fact" {
		t.Fatalf("prompt should trim entrypoint content, got %q", got)
	}
	withEnv(t, "EINO_AGENT_DISABLE_AUTO_MEMORY", "1")
	if got := BuildMemoryPrompt(); got != "" {
		t.Fatalf("disabled memory should produce empty prompt, got %q", got)
	}

	var lines []string
	for i := 0; i < MaxEntrypointLines+1; i++ {
		lines = append(lines, fmt.Sprintf("line-%03d", i))
	}
	truncated := TruncateEntrypointContent(strings.Join(lines, "\n"))
	if !truncated.WasLineTruncated || truncated.WasByteTruncated {
		t.Fatalf("expected line-only truncation, got %#v", truncated)
	}
	if !strings.Contains(truncated.Content, "WARNING: MEMORY.md is") {
		t.Fatalf("warning missing from truncated content: %q", truncated.Content)
	}

	longLine := strings.Repeat("x", MaxEntrypointBytes+1)
	byteTruncated := TruncateEntrypointContent(longLine)
	if !byteTruncated.WasByteTruncated || byteTruncated.WasLineTruncated {
		t.Fatalf("expected byte-only truncation, got %#v", byteTruncated)
	}
	if len(byteTruncated.Content) <= MaxEntrypointBytes {
		t.Fatalf("warning should be appended after byte truncation")
	}
}

func TestListMemoryFilesIncludesMarkdownFiles(t *testing.T) {
	memPath := filepath.Join(t.TempDir(), "memory")
	withEnv(t, "EINO_AGENT_MEMORY_PATH_OVERRIDE", memPath)
	if files, err := ListMemoryFiles(); err != nil || files != nil {
		t.Fatalf("missing memory dir should return nil files, got %#v err=%v", files, err)
		return
	}
	if err := os.MkdirAll(filepath.Join(memPath, "nested"), 0o755); err != nil {
		t.Fatal(err)
		return
	}
	for _, name := range []string{"MEMORY.md", "nested/topic.md", "ignore.txt"} {
		if err := os.WriteFile(filepath.Join(memPath, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
			return
		}
	}
	files, err := ListMemoryFiles()
	if err != nil {
		t.Fatal(err)
		return
	}
	if len(files) != 2 {
		t.Fatalf("expected two markdown files including entrypoint, got %#v", files)
	}
}

func TestTeamMemoryPathsRequireAutoMemoryAndSafeSharedRoot(t *testing.T) {
	teamDir := filepath.Join(t.TempDir(), "team")
	withEnv(t, identity.RuntimeEnvTeamMemoryDir.Pair().Legacy, teamDir)
	withEnv(t, "EINO_AGENT_DISABLE_AUTO_MEMORY", "")
	withEnv(t, "EINO_AGENT_SIMPLE", "")

	if !IsTeamMemoryEnabled() {
		t.Fatal("absolute shared root should enable team memory")
	}
	if got := GetTeamMemEntrypoint(); got != filepath.Join(teamDir, EntrypointName) {
		t.Fatalf("team entrypoint = %q", got)
	}
	if !IsTeamMemPath(filepath.Join(teamDir, "nested", "topic.md")) {
		t.Fatal("nested logical path should be recognized")
	}
	if _, err := ValidateTeamMemWritePath(filepath.Join(teamDir, "nested", "topic.md")); err != nil {
		t.Fatalf("safe non-existent tail rejected: %v", err)
	}

	outside := t.TempDir()
	if err := os.MkdirAll(teamDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(teamDir, "escape")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := ValidateTeamMemWritePath(filepath.Join(teamDir, "escape", "secret.md")); err == nil {
		t.Fatal("symlink escape should fail closed")
	}
	if _, err := ValidateTeamMemWritePath(filepath.Join(teamDir, "..", "outside.md")); err == nil {
		t.Fatal("lexical traversal should fail closed")
	}

	withEnv(t, "EINO_AGENT_DISABLE_AUTO_MEMORY", "1")
	if IsTeamMemoryEnabled() {
		t.Fatal("disabled auto memory must disable team memory")
	}
	withEnv(t, "EINO_AGENT_DISABLE_AUTO_MEMORY", "")
	withEnv(t, identity.RuntimeEnvTeamMemoryDir.Pair().Legacy, "relative/path")
	if IsTeamMemoryEnabled() {
		t.Fatal("relative shared root must fail closed")
	}
	for _, invalid := range []string{"", string(filepath.Separator)} {
		withEnv(t, identity.RuntimeEnvTeamMemoryDir.Pair().Legacy, invalid)
		if IsTeamMemoryEnabled() {
			t.Fatalf("invalid shared root %q must fail closed", invalid)
		}
	}
	if got := validateMemoryPath("bad\x00path"); got != "" {
		t.Fatalf("NUL-containing root must fail closed: %q", got)
	}
}

func TestUnifiedMemoryPromptInjectsIndependentIndexes(t *testing.T) {
	privateDir := filepath.Join(t.TempDir(), "private")
	teamDir := filepath.Join(t.TempDir(), "team")
	withEnv(t, "EINO_AGENT_MEMORY_PATH_OVERRIDE", privateDir)
	withEnv(t, identity.RuntimeEnvTeamMemoryDir.Pair().Legacy, teamDir)
	withEnv(t, "EINO_AGENT_DISABLE_AUTO_MEMORY", "")
	withEnv(t, "EINO_AGENT_SIMPLE", "")

	if err := os.MkdirAll(privateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(teamDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(privateDir, EntrypointName), []byte("private-index"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(teamDir, EntrypointName), []byte("team-index"), 0o644); err != nil {
		t.Fatal(err)
	}

	prompt, err := BuildUnifiedMemoryPrompt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	privateAt := strings.Index(prompt, "private-index")
	teamAt := strings.Index(prompt, "team-index")
	if privateAt < 0 || teamAt < 0 || privateAt >= teamAt {
		t.Fatalf("indexes not injected private-before-team: %q", prompt)
	}
	for _, want := range []string{"private", "team", `<team-memory-content source="shared">`} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q", want)
		}
	}
}

func TestScanMemoryDirectoriesPreservesScope(t *testing.T) {
	privateDir := filepath.Join(t.TempDir(), "private")
	teamDir := filepath.Join(t.TempDir(), "team")
	withEnv(t, "EINO_AGENT_MEMORY_PATH_OVERRIDE", privateDir)
	withEnv(t, identity.RuntimeEnvTeamMemoryDir.Pair().Legacy, teamDir)
	withEnv(t, "EINO_AGENT_DISABLE_AUTO_MEMORY", "")
	withEnv(t, "EINO_AGENT_SIMPLE", "")
	for _, item := range []struct {
		path string
		body string
	}{
		{filepath.Join(privateDir, "private.md"), "---\ndescription: private\ntype: user\n---\n"},
		{filepath.Join(teamDir, "nested", "team.md"), "---\ndescription: shared\ntype: project\n---\n"},
		{filepath.Join(teamDir, EntrypointName), "index"},
	} {
		if err := os.MkdirAll(filepath.Dir(item.path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(item.path, []byte(item.body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	headers, err := ScanMemoryDirectories(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(headers) != 2 || headers[0].Scope != MemoryScopePrivate || headers[1].Scope != MemoryScopeTeam {
		t.Fatalf("scoped headers = %#v", headers)
	}
	if headers[1].Filename != filepath.Join("nested", "team.md") {
		t.Fatalf("team header = %#v", headers[1])
	}
}

func TestTeamMemorySecretScannerIsBoundedAndDeduplicated(t *testing.T) {
	labels := ScanForSecrets("token ghp_abcdefghijklmnopqrstuvwxyzABCDEFGHIJ and ghp_abcdefghijklmnopqrstuvwxyzABCDEFGHIJ")
	if len(labels) != 1 || labels[0] != "GitHub token" {
		t.Fatalf("labels = %#v", labels)
	}
	if labels := ScanForSecrets("ordinary prose mentioning api keys without a credential"); len(labels) != 0 {
		t.Fatalf("generic prose should not match: %#v", labels)
	}
}
