package session

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/transcript"
	"github.com/abietic/yhc/internal/identity"
)

func TestDefaultCatalogPathPrefersYHCSessionCatalog(t *testing.T) {
	pair := identity.RuntimeEnvSessionCatalog.Pair()
	canonical := filepath.Join(t.TempDir(), "canonical.json")
	legacy := filepath.Join(t.TempDir(), "legacy.json")
	tests := []struct {
		name      string
		canonical *string
		legacy    *string
		want      string
	}{
		{name: "canonical only", canonical: sessionEnvironmentValue(canonical), want: canonical},
		{name: "legacy only", legacy: sessionEnvironmentValue(legacy), want: legacy},
		{name: "both prefer canonical", canonical: sessionEnvironmentValue(canonical), legacy: sessionEnvironmentValue(legacy), want: canonical},
		{name: "present empty canonical blocks legacy", canonical: sessionEnvironmentValue(""), legacy: sessionEnvironmentValue(legacy)},
		{name: "invalid canonical blocks legacy", canonical: sessionEnvironmentValue(" \t "), legacy: sessionEnvironmentValue(legacy)},
		{name: "neither"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			var err error
			home, err = filepath.EvalSymlinks(home)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			setSessionEnvironment(t, pair.Canonical, test.canonical)
			setSessionEnvironment(t, pair.Legacy, test.legacy)
			want := test.want
			legacyWant := ""
			if want == "" {
				want = filepath.Join(home, ".yhc", "session-roots.json")
				legacyWant = filepath.Join(home, ".eino-agent", "session-roots.json")
			}
			got, legacy := DefaultCatalogPaths()
			if got != want || legacy != legacyWant {
				t.Fatalf("DefaultCatalogPaths() = (%q, %q), want (%q, %q)", got, legacy, want, legacyWant)
			}
			if got := DefaultCatalogPath(); got != want {
				t.Fatalf("DefaultCatalogPath() = %q, want %q", got, want)
			}
		})
	}
}

func TestExplicitCatalogOverrideDoesNotRequireHome(t *testing.T) {
	pair := identity.RuntimeEnvSessionCatalog.Pair()
	override := filepath.Join(t.TempDir(), "explicit-catalog.json")
	t.Setenv("HOME", "")
	t.Setenv(pair.Canonical, override)
	setSessionEnvironment(t, pair.Legacy, nil)

	got, legacy := DefaultCatalogPaths()
	if got != override || legacy != "" {
		t.Fatalf("DefaultCatalogPaths() = (%q, %q), want (%q, empty)", got, legacy, override)
	}
}

func TestLegacyCatalogRowsAreDiscoverableReadOnly(t *testing.T) {
	project := t.TempDir()
	canonicalDir := filepath.Join(project, ".yhc", "transcripts")
	legacyDir := filepath.Join(project, ".eino-agent", "transcripts")
	canonicalCatalog := filepath.Join(t.TempDir(), "canonical.json")
	legacyCatalog := filepath.Join(t.TempDir(), "legacy.json")
	writeQuerySession(t, canonicalDir, "canonical", "canonical prompt", time.Now().Add(-time.Minute), nil)
	writeQuerySession(t, legacyDir, "legacy", "legacy prompt", time.Now(), nil)
	if err := RegisterSessionRoot(canonicalCatalog, project, canonicalDir, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := RegisterSessionRoot(legacyCatalog, project, legacyDir, time.Now()); err != nil {
		t.Fatal(err)
	}
	legacyBytes, err := os.ReadFile(legacyCatalog)
	if err != nil {
		t.Fatal(err)
	}
	legacyInfo, err := os.Stat(legacyCatalog)
	if err != nil {
		t.Fatal(err)
	}

	page, err := QuerySessions(SessionQuery{
		Scope:             SessionScopeCWD,
		CWD:               project,
		TranscriptDir:     canonicalDir,
		CatalogPath:       canonicalCatalog,
		LegacyCatalogPath: legacyCatalog,
		Limit:             10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := querySessionIDs(page.Sessions); strings.Join(got, ",") != "legacy,canonical" {
		t.Fatalf("discoverable sessions = %v", got)
	}
	for _, info := range page.Sessions {
		if info.SessionID == "legacy" && (!info.ReadOnly || !info.NeedsImport) {
			t.Fatalf("legacy session flags = %#v", info)
		}
		if info.SessionID == "canonical" && (info.ReadOnly || info.NeedsImport) {
			t.Fatalf("canonical session flags = %#v", info)
		}
	}
	afterBytes, err := os.ReadFile(legacyCatalog)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(legacyCatalog)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(legacyBytes, afterBytes) {
		t.Fatal("legacy catalog bytes changed during read-only discovery")
	}
	if !legacyInfo.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("legacy catalog mtime changed during read-only discovery")
	}
}

func TestCanonicalAndLegacyCatalogDeduplicateByRepositoryAndSession(t *testing.T) {
	project := t.TempDir()
	canonicalDir := filepath.Join(project, ".yhc", "transcripts")
	legacyDir := filepath.Join(project, ".eino-agent", "transcripts")
	canonicalCatalog := filepath.Join(t.TempDir(), "canonical.json")
	legacyCatalog := filepath.Join(t.TempDir(), "legacy.json")
	writeQuerySession(t, canonicalDir, "same-session", "canonical wins", time.Now(), nil)
	writeQuerySession(t, legacyDir, "same-session", "legacy loses", time.Now().Add(-time.Minute), nil)
	if err := RegisterSessionRoot(canonicalCatalog, project, canonicalDir, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := RegisterSessionRoot(legacyCatalog, project, legacyDir, time.Now()); err != nil {
		t.Fatal(err)
	}

	page, err := QuerySessions(SessionQuery{
		Scope:             SessionScopeRepository,
		CWD:               project,
		TranscriptDir:     canonicalDir,
		CatalogPath:       canonicalCatalog,
		LegacyCatalogPath: legacyCatalog,
		Limit:             10,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantCanonicalDir, err := canonicalPath(canonicalDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sessions) != 1 || page.Sessions[0].SessionID != "same-session" ||
		page.Sessions[0].TranscriptDir != wantCanonicalDir ||
		page.Sessions[0].ReadOnly || page.Sessions[0].NeedsImport {
		t.Fatalf("canonical session did not win legacy duplicate: %#v", page.Sessions)
	}
}

func TestSessionCursorRejectsCanonicalCatalogSwitch(t *testing.T) {
	project := t.TempDir()
	currentDir := filepath.Join(project, ".yhc", "transcripts")
	firstDir := filepath.Join(project, "first-catalog-transcripts")
	secondDir := filepath.Join(project, "second-catalog-transcripts")
	firstCatalog := filepath.Join(t.TempDir(), "first.json")
	secondCatalog := filepath.Join(t.TempDir(), "second.json")
	now := time.Now()
	writeQuerySession(t, firstDir, "first-new", "first page", now, nil)
	writeQuerySession(t, firstDir, "first-old", "second page", now.Add(-time.Minute), nil)
	writeQuerySession(t, secondDir, "second-only", "different catalog", now.Add(-time.Hour), nil)
	if err := RegisterSessionRoot(firstCatalog, project, firstDir, now); err != nil {
		t.Fatal(err)
	}
	if err := RegisterSessionRoot(secondCatalog, project, secondDir, now); err != nil {
		t.Fatal(err)
	}

	firstPage, err := QuerySessions(SessionQuery{
		Scope:         SessionScopeCWD,
		CWD:           project,
		TranscriptDir: currentDir,
		CatalogPath:   firstCatalog,
		Limit:         1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !firstPage.HasMore || firstPage.NextCursor == "" {
		t.Fatalf("first catalog did not produce a continuation: %#v", firstPage)
	}
	_, err = QuerySessions(SessionQuery{
		Scope:         SessionScopeCWD,
		CWD:           project,
		TranscriptDir: currentDir,
		CatalogPath:   secondCatalog,
		Limit:         1,
		Cursor:        firstPage.NextCursor,
	})
	if !errors.Is(err, ErrSessionCursorInvalid) {
		t.Fatalf("canonical catalog switch error = %v, want ErrSessionCursorInvalid", err)
	}
}

func sessionEnvironmentValue(value string) *string { return &value }

func setSessionEnvironment(t *testing.T, name string, value *string) {
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

func TestQuerySessionsCursorSurvivesMovingPages(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	for index, id := range []string{"oldest", "middle", "newest"} {
		writeQuerySession(t, dir, id, id+" prompt", base.Add(time.Duration(index)*time.Minute), nil)
	}

	query := SessionQuery{
		Scope:         SessionScopeCWD,
		CWD:           t.TempDir(),
		TranscriptDir: dir,
		Limit:         2,
	}
	first, err := QuerySessions(query)
	if err != nil {
		t.Fatal(err)
	}
	if got := querySessionIDs(first.Sessions); strings.Join(got, ",") != "newest,middle" {
		t.Fatalf("first page = %v", got)
	}
	if !first.HasMore || first.NextCursor == "" {
		t.Fatalf("first page lacks continuation: %#v", first)
	}

	// A returned row moves ahead of the cursor and a new row appears ahead of it.
	// Neither may duplicate into the older continuation.
	newer := base.Add(10 * time.Minute)
	if err := os.Chtimes(filepath.Join(dir, "newest.jsonl"), newer, newer); err != nil {
		t.Fatal(err)
	}
	writeQuerySession(t, dir, "inserted", "inserted prompt", newer.Add(time.Minute), nil)
	query.Cursor = first.NextCursor
	second, err := QuerySessions(query)
	if err != nil {
		t.Fatal(err)
	}
	if got := querySessionIDs(second.Sessions); strings.Join(got, ",") != "oldest" {
		t.Fatalf("second page = %v", got)
	}
}

func TestQuerySessionsRejectsCursorFromDifferentQuery(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	writeQuerySession(t, dir, "one", "needle one", base, nil)
	writeQuerySession(t, dir, "two", "needle two", base.Add(time.Minute), nil)

	query := SessionQuery{Scope: SessionScopeCWD, CWD: t.TempDir(), TranscriptDir: dir, Limit: 1}
	page, err := QuerySessions(query)
	if err != nil {
		t.Fatal(err)
	}
	query.Cursor = page.NextCursor
	query.Filter.Search = "different"
	if _, err := QuerySessions(query); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched cursor error = %v", err)
	}
}

func TestQuerySessionsScanCapBoundsSelectiveFilter(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	for index := 0; index < 5; index++ {
		prompt := "ordinary"
		if index == 0 {
			prompt = "needle"
		}
		writeQuerySession(t, dir, string(rune('a'+index)), prompt, base.Add(time.Duration(index)*time.Minute), nil)
	}

	query := SessionQuery{
		Scope:         SessionScopeCWD,
		CWD:           t.TempDir(),
		TranscriptDir: dir,
		Limit:         2,
		ScanLimit:     2,
		Filter:        ListFilter{Search: "needle"},
	}
	var sessions []SessionInfo
	for pageNumber := 0; pageNumber < 4; pageNumber++ {
		page, err := QuerySessions(query)
		if err != nil {
			t.Fatal(err)
		}
		if page.Scanned > query.ScanLimit {
			t.Fatalf("page scanned %d candidates", page.Scanned)
		}
		sessions = append(sessions, page.Sessions...)
		if !page.HasMore {
			break
		}
		query.Cursor = page.NextCursor
	}
	if got := querySessionIDs(sessions); strings.Join(got, ",") != "a" {
		t.Fatalf("filtered sessions = %v", got)
	}
}

func TestQuerySessionsModelFilterUsesLiteMetadata(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	writeQuerySession(t, dir, "sonnet", "one", base, &SessionMetadataFull{Model: "claude-sonnet-4", Provider: "anthropic"})
	writeQuerySession(t, dir, "gpt", "two", base.Add(time.Minute), &SessionMetadataFull{Model: "gpt-5", Provider: "openai"})

	page, err := QuerySessions(SessionQuery{
		Scope:         SessionScopeCWD,
		CWD:           t.TempDir(),
		TranscriptDir: dir,
		Filter:        ListFilter{Model: "anthropic"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := querySessionIDs(page.Sessions); strings.Join(got, ",") != "sonnet" {
		t.Fatalf("model-filtered sessions = %v", got)
	}
	if page.Sessions[0].Model != "claude-sonnet-4" {
		t.Fatalf("model metadata = %#v", page.Sessions[0])
	}
}

func TestP234bQuerySessionsMergesActiveOverlayWithinBounds(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, ".eino-agent", "transcripts")
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	writeQuerySession(t, dir, "durable-old", "old", base, nil)
	writeQuerySession(
		t,
		dir,
		"durable-active",
		"active durable",
		base.Add(time.Minute),
		nil,
	)
	active := []SessionInfo{
		{
			SessionID:      "durable-active",
			CWD:            cwd,
			CreatedAt:      base.Add(time.Minute),
			LastModified:   base.Add(time.Minute),
			TranscriptDir:  dir,
			TranscriptPath: filepath.Join(dir, "durable-active.jsonl"),
		},
		{
			SessionID:      "active-only",
			CWD:            cwd,
			CreatedAt:      base.Add(2 * time.Minute),
			LastModified:   base.Add(2 * time.Minute),
			TranscriptDir:  dir,
			TranscriptPath: filepath.Join(dir, "active-only.jsonl"),
		},
	}
	query := SessionQuery{
		Scope:                    SessionScopeCWD,
		CWD:                      cwd,
		TranscriptDir:            dir,
		Limit:                    2,
		ActiveOverlay:            active,
		BindCandidateGenerations: true,
	}

	var got []string
	for pageNumber := 0; pageNumber < 3; pageNumber++ {
		page, err := QuerySessions(query)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Sessions) > query.Limit {
			t.Fatalf("page %d exceeded limit: %#v", pageNumber, page)
		}
		got = append(got, querySessionIDs(page.Sessions)...)
		if !page.HasMore {
			break
		}
		if page.NextCursor == "" {
			t.Fatalf("page %d has no cursor: %#v", pageNumber, page)
		}
		query.Cursor = page.NextCursor
	}
	if joined := strings.Join(got, ","); joined !=
		"active-only,durable-active,durable-old" {
		t.Fatalf("merged pages = %s", joined)
	}
}

func TestP234bQuerySessionsGenerationBoundCursorFailsClosed(t *testing.T) {
	t.Run("durable generation", func(t *testing.T) {
		cwd := t.TempDir()
		dir := filepath.Join(cwd, ".eino-agent", "transcripts")
		base := time.Now().Add(-time.Hour).Truncate(time.Second)
		writeQuerySession(t, dir, "one", "one", base, nil)
		writeQuerySession(t, dir, "two", "two", base.Add(time.Minute), nil)
		query := SessionQuery{
			Scope:                    SessionScopeCWD,
			CWD:                      cwd,
			TranscriptDir:            dir,
			Limit:                    1,
			BindCandidateGenerations: true,
		}
		first, err := QuerySessions(query)
		if err != nil {
			t.Fatal(err)
		}
		writeQuerySession(t, dir, "three", "three", base.Add(2*time.Minute), nil)
		query.Cursor = first.NextCursor
		if _, err := QuerySessions(query); !errors.Is(
			err,
			ErrSessionCursorInvalid,
		) {
			t.Fatalf("durable generation error = %v", err)
		}
	})

	t.Run("active generation", func(t *testing.T) {
		cwd := t.TempDir()
		dir := filepath.Join(cwd, ".eino-agent", "transcripts")
		base := time.Now().Add(-time.Hour).Truncate(time.Second)
		active := func(id string, offset time.Duration) SessionInfo {
			return SessionInfo{
				SessionID:      id,
				CWD:            cwd,
				CreatedAt:      base.Add(offset),
				LastModified:   base.Add(offset),
				TranscriptDir:  dir,
				TranscriptPath: filepath.Join(dir, id+".jsonl"),
			}
		}
		query := SessionQuery{
			Scope:                    SessionScopeCWD,
			CWD:                      cwd,
			TranscriptDir:            dir,
			Limit:                    1,
			ActiveOverlay:            []SessionInfo{active("one", 0), active("two", time.Minute)},
			BindCandidateGenerations: true,
		}
		first, err := QuerySessions(query)
		if err != nil {
			t.Fatal(err)
		}
		query.ActiveOverlay = append(
			query.ActiveOverlay,
			active("three", 2*time.Minute),
		)
		query.Cursor = first.NextCursor
		if _, err := QuerySessions(query); !errors.Is(
			err,
			ErrSessionCursorInvalid,
		) {
			t.Fatalf("active generation error = %v", err)
		}
	})
}

func TestQuerySessionsScopesUseCatalogAndGitCommonDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	subdir := filepath.Join(root, "nested")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	dirs := []string{
		filepath.Join(root, ".eino-agent", "transcripts"),
		filepath.Join(subdir, ".eino-agent", "transcripts"),
		filepath.Join(outside, ".eino-agent", "transcripts"),
	}
	base := time.Now().Add(-time.Hour)
	writeQuerySession(t, dirs[0], "root-session", "root", base, nil)
	writeQuerySession(t, dirs[1], "nested-session", "nested", base.Add(time.Minute), nil)
	writeQuerySession(t, dirs[2], "outside-session", "outside", base.Add(2*time.Minute), nil)
	catalog := filepath.Join(t.TempDir(), "roots.json")
	for index, cwd := range []string{root, subdir, outside} {
		if err := RegisterSessionRoot(catalog, cwd, dirs[index], base.Add(time.Duration(index)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}

	baseQuery := SessionQuery{CWD: root, TranscriptDir: dirs[0], CatalogPath: catalog, Limit: 20}
	baseQuery.Scope = SessionScopeCWD
	cwdPage, err := QuerySessions(baseQuery)
	if err != nil {
		t.Fatal(err)
	}
	if got := querySessionIDs(cwdPage.Sessions); strings.Join(got, ",") != "root-session" {
		t.Fatalf("cwd scope = %v", got)
	}

	baseQuery.Scope = SessionScopeRepository
	repositoryPage, err := QuerySessions(baseQuery)
	if err != nil {
		t.Fatal(err)
	}
	if got := querySessionIDs(repositoryPage.Sessions); strings.Join(got, ",") != "nested-session,root-session" {
		t.Fatalf("repository scope = %v", got)
	}

	baseQuery.Scope = SessionScopeAll
	allPage, err := QuerySessions(baseQuery)
	if err != nil {
		t.Fatal(err)
	}
	if got := querySessionIDs(allPage.Sessions); strings.Join(got, ",") != "outside-session,nested-session,root-session" {
		t.Fatalf("all scope = %v", got)
	}
}

func TestRegisterSessionRootUpdatesAtomicallyAndDeduplicates(t *testing.T) {
	catalog := filepath.Join(t.TempDir(), "roots.json")
	cwd := t.TempDir()
	dir := filepath.Join(cwd, "transcripts")
	first := time.Now().Add(-time.Hour).UTC()
	if err := RegisterSessionRoot(catalog, cwd, dir, first); err != nil {
		t.Fatal(err)
	}
	second := first.Add(time.Minute)
	if err := RegisterSessionRoot(catalog, cwd, dir, second); err != nil {
		t.Fatal(err)
	}
	roots, err := LoadSessionRoots(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 1 || !roots[0].UpdatedAt.Equal(second) {
		t.Fatalf("catalog roots = %#v", roots)
	}
	if matches, err := filepath.Glob(filepath.Join(filepath.Dir(catalog), ".session-roots-*.tmp")); err != nil || len(matches) != 0 {
		t.Fatalf("catalog temp files = %v, err=%v", matches, err)
	}
}

func writeQuerySession(t *testing.T, dir, id, prompt string, modified time.Time, metadata *SessionMetadataFull) {
	t.Helper()
	recorder := transcript.NewRecorder(id, dir)
	if err := recorder.Record([]*schema.Message{{Role: schema.User, Content: prompt}}, false); err != nil {
		t.Fatal(err)
	}
	if metadata != nil {
		metadata.SessionID = id
		if err := WriteSessionMetadata(recorder, metadata); err != nil {
			t.Fatal(err)
		}
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(recorder.Path(), modified, modified); err != nil {
		t.Fatal(err)
	}
}

func querySessionIDs(sessions []SessionInfo) []string {
	ids := make([]string, 0, len(sessions))
	for _, info := range sessions {
		ids = append(ids, info.SessionID)
	}
	return ids
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
