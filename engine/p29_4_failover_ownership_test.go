package engine

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP294SingleFailoverOwnerSourceGate(t *testing.T) {
	engineDir := p293SourceEngineDir(t)
	assertP293SourceTokensAbsent(t, engineDir, []string{
		"FallbackTriggeredError",
		"resolveFallbackRetry",
		"handleFallbackRetry",
		"ModelFailoverConfig",
		"FailoverContext",
		"lastSuccessModel",
		"adaptiveHealth",
		"routeHealth",
	})
	assertP293SourceTokensAbsent(
		t,
		filepath.Join(engineDir, "execution", "retry.go"),
		[]string{"FallbackModel", "FallbackTriggered"},
	)
	failoverOwnerTokens := []string{
		"modelAttemptCoordinator",
		"ResolveFailoverChain",
		"EventModelAttempt",
		"ModelAttemptID",
		"ModelRetryIndex",
	}
	assertP293SourceTokensAbsent(
		t,
		filepath.Join(engineDir, "..", "server", "mcp"),
		failoverOwnerTokens,
	)
	sessionDir := filepath.Join(engineDir, "session")
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() ||
			!strings.HasSuffix(entry.Name(), ".go") ||
			strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		forbidden := failoverOwnerTokens
		if entry.Name() == "branch.go" {
			// Session metadata may retain inert provider-attempt identity for
			// restart-safe Goal usage settlement. It still cannot own routing,
			// failover coordination, or attempt events.
			forbidden = []string{
				"modelAttemptCoordinator",
				"ResolveFailoverChain",
				"EventModelAttempt",
			}
		}
		assertP293SourceTokensAbsent(
			t,
			filepath.Join(sessionDir, entry.Name()),
			forbidden,
		)
	}

	assertP294TokenOwners(t, engineDir, "newModelAttemptCoordinator(", map[string]int{
		"model_failover.go": 1,
		"model_round.go":    1,
	})
	assertP294TokenOwners(t, engineDir, "NewModelAttemptBudget(", map[string]int{
		"model_failover.go":  1,
		"execution/retry.go": 1,
	})
}

func assertP294TokenOwners(
	t *testing.T,
	root string,
	token string,
	want map[string]int,
) {
	t.Helper()
	got := make(map[string]int)
	err := filepath.WalkDir(
		root,
		func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() ||
				!strings.HasSuffix(path, ".go") ||
				strings.HasSuffix(path, "_test.go") {
				return nil
			}
			source, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			count := strings.Count(string(source), token)
			if count > 0 {
				relative, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				got[relative] = count
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("scan %s for %q: %v", root, token, err)
	}
	if len(got) != len(want) {
		t.Fatalf("%q owners = %#v, want %#v", token, got, want)
	}
	for path, wantCount := range want {
		if got[path] != wantCount {
			t.Fatalf(
				"%q owner %s count = %d, want %d (all=%#v)",
				token,
				path,
				got[path],
				wantCount,
				got,
			)
		}
	}
}
