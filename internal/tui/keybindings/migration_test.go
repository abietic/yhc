package keybindings

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abietic/yhc/internal/statemigration"
	"github.com/abietic/yhc/internal/statepath"
)

func TestKeybindingsMigrationUsesResolverValidation(t *testing.T) {
	privateSentinel := "sk-" + strings.Repeat("1", 24)
	valid := []byte(`{"apiKey":"` + privateSentinel + `","bindings":[{"context":"Chat","bindings":{"alt+up":"chat:nextAgent"}}]}`)
	roots := newKeybindingsMigrationRoots(t, valid)
	result, err := (statemigration.Importer{}).Import(
		t.Context(),
		roots,
		UserMigrationSpec(),
	)
	if err != nil || result.Status != statemigration.StatusImported {
		t.Fatalf("valid Import() = %#v, %v", result, err)
	}
	got, err := os.ReadFile(filepath.Join(roots.Canonical, "keybindings.json"))
	if err != nil {
		t.Fatalf("canonical keybindings = %q, %v", got, err)
	}
	if bytes.Contains(got, []byte(privateSentinel)) || bytes.Contains(got, []byte("apiKey")) {
		t.Fatalf("canonical keybindings copied an ignored private field: %s", got)
	}
	resolver := NewResolver()
	issues, err := resolver.LoadUserBindings(roots.Canonical)
	if err != nil || HasValidationErrors(issues) {
		t.Fatalf("rebuilt canonical keybindings err=%v issues=%#v", err, issues)
	}

	invalidRoots := newKeybindingsMigrationRoots(
		t,
		[]byte(`{"bindings":[{"context":"Chat","bindings":{"ctrl+c":"chat:submit"}}]}`),
	)
	result, err = (statemigration.Importer{}).Import(
		t.Context(),
		invalidRoots,
		UserMigrationSpec(),
	)
	if err == nil || result.Status != statemigration.StatusUnsafe {
		t.Fatalf("invalid Import() = %#v, %v, want unsafe", result, err)
	}
	if _, statErr := os.Lstat(filepath.Join(invalidRoots.Canonical, "keybindings.json")); !os.IsNotExist(statErr) {
		t.Fatalf("invalid import created a destination: %v", statErr)
	}
}

func newKeybindingsMigrationRoots(t *testing.T, source []byte) statepath.Roots {
	t.Helper()
	root := t.TempDir()
	roots := statepath.Roots{
		Canonical: filepath.Join(root, ".yhc"),
		Legacy:    filepath.Join(root, ".eino-agent"),
	}
	if err := os.Mkdir(roots.Legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(roots.Legacy, "keybindings.json"), source, 0o600); err != nil {
		t.Fatal(err)
	}
	return roots
}
