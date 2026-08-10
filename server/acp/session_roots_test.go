package acp

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
)

func TestACPSessionRootLocatorRepeatedObservationStaysExact(t *testing.T) {
	locator := newACPSessionRootLocator()
	id := acpsdk.SessionId("repeated")
	root := t.TempDir()

	locator.remember(id, root)
	locator.remember(id, root)
	got, observed, ambiguous := locator.resolve(id, "")
	if got != canonicalACPSessionDirectory(root) || !observed || ambiguous {
		t.Fatalf("resolve = (%q, %t, %t), want (%q, true, false)", got, observed, ambiguous, canonicalACPSessionDirectory(root))
	}
}

func TestACPSessionRootLocatorTreatsCleanAndSymlinkAliasesAsEquivalent(t *testing.T) {
	root := t.TempDir()
	assertEquivalent := func(t *testing.T, aliases ...string) {
		t.Helper()
		locator := newACPSessionRootLocator()
		id := acpsdk.SessionId(t.Name())
		for _, alias := range aliases {
			locator.remember(id, alias)
		}
		got, observed, ambiguous := locator.resolve(id, "")
		if got != canonicalACPSessionDirectory(root) || !observed || ambiguous {
			t.Fatalf("resolve = (%q, %t, %t), want (%q, true, false)", got, observed, ambiguous, canonicalACPSessionDirectory(root))
		}
	}
	t.Run("clean_path", func(t *testing.T) { assertEquivalent(t, root, filepath.Join(root, ".")) })
	t.Run("symlink", func(t *testing.T) {
		alias := filepath.Join(t.TempDir(), "root-alias")
		if err := os.Symlink(root, alias); err != nil {
			t.Skipf("create symlink fixture: %v", err)
		}
		assertEquivalent(t, root, alias)
	})
}

func TestACPSessionRootLocatorAmbiguityIsMonotonic(t *testing.T) {
	locator := newACPSessionRootLocator()
	id := acpsdk.SessionId("ambiguous")
	first, second := t.TempDir(), t.TempDir()

	locator.remember(id, first)
	locator.remember(id, second)
	locator.remember(id, first)
	got, observed, ambiguous := locator.resolve(id, t.TempDir())
	if got != "" || !observed || !ambiguous {
		t.Fatalf("resolve = (%q, %t, %t), want (empty, true, true)", got, observed, ambiguous)
	}
}

func TestACPSessionRootLocatorUnknownUsesCanonicalFallback(t *testing.T) {
	locator := newACPSessionRootLocator()
	fallback := filepath.Join(t.TempDir(), ".")

	got, observed, ambiguous := locator.resolve(acpsdk.SessionId("unknown"), fallback)
	if got != canonicalACPSessionDirectory(fallback) || observed || ambiguous {
		t.Fatalf("resolve = (%q, %t, %t), want (%q, false, false)", got, observed, ambiguous, canonicalACPSessionDirectory(fallback))
	}
}

func TestACPSessionRootLocatorCloseDoesNotMutateObservation(t *testing.T) {
	locator := newACPSessionRootLocator()
	id := acpsdk.SessionId("closed")
	root := t.TempDir()
	locator.remember(id, root)

	// A simulated close leaves the process-local locator untouched.
	got, observed, ambiguous := locator.resolve(id, "")
	if got != canonicalACPSessionDirectory(root) || !observed || ambiguous {
		t.Fatalf("resolve after simulated close = (%q, %t, %t)", got, observed, ambiguous)
	}
}

func TestACPSessionRootLocatorForgetOnlyClearsMatchingExactRoot(t *testing.T) {
	locator := newACPSessionRootLocator()
	exactID, ambiguousID := acpsdk.SessionId("exact"), acpsdk.SessionId("ambiguous")
	exact, other := t.TempDir(), t.TempDir()
	locator.remember(exactID, exact)
	locator.forget(exactID, other)
	if _, observed, ambiguous := locator.resolve(exactID, ""); !observed || ambiguous {
		t.Fatalf("wrong expected root changed exact observation: observed=%t ambiguous=%t", observed, ambiguous)
	}
	locator.forget(exactID, filepath.Join(exact, "."))
	if _, observed, ambiguous := locator.resolve(exactID, ""); observed || ambiguous {
		t.Fatalf("matching expected root did not clear exact observation: observed=%t ambiguous=%t", observed, ambiguous)
	}

	locator.remember(ambiguousID, exact)
	locator.remember(ambiguousID, other)
	locator.forget(ambiguousID, exact)
	if _, observed, ambiguous := locator.resolve(ambiguousID, ""); !observed || !ambiguous {
		t.Fatalf("forget cleared ambiguous observation: observed=%t ambiguous=%t", observed, ambiguous)
	}
}

func TestACPSessionRootLocatorConcurrentRememberAndResolve(t *testing.T) {
	for _, tc := range []struct {
		name      string
		roots     []string
		ambiguous bool
	}{
		{name: "same_root", roots: []string{t.TempDir(), ""}},
		{name: "two_roots", roots: []string{t.TempDir(), t.TempDir()}, ambiguous: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.roots[1] == "" {
				tc.roots[1] = filepath.Join(tc.roots[0], ".")
			}
			locator := newACPSessionRootLocator()
			id := acpsdk.SessionId(tc.name)
			var group sync.WaitGroup
			for _, root := range tc.roots {
				group.Add(1)
				go func(root string) {
					defer group.Done()
					locator.remember(id, root)
					locator.resolve(id, root)
				}(root)
			}
			group.Wait()
			got, observed, ambiguous := locator.resolve(id, "")
			if !observed || ambiguous != tc.ambiguous {
				t.Fatalf("resolve = (%q, %t, %t), want observed=true ambiguous=%t", got, observed, ambiguous, tc.ambiguous)
			}
			if !tc.ambiguous && got != canonicalACPSessionDirectory(tc.roots[0]) {
				t.Fatalf("exact root = %q, want %q", got, canonicalACPSessionDirectory(tc.roots[0]))
			}
			if tc.ambiguous && got != "" {
				t.Fatalf("ambiguous root = %q, want empty", got)
			}
		})
	}
}
