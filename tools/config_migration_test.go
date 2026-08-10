package tools

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/abietic/yhc/internal/statemigration"
	"github.com/abietic/yhc/internal/statepath"
)

func TestSettingsMigrationCopiesOnlyNonSecretAllowlist(t *testing.T) {
	roots := newSettingsMigrationRoots(t, []byte(`{
  "provider": "openai",
  "memory": {"enabled": true},
  "compact": {"strategy": "auto", "threshold": 4096},
  "permissions": {
    "deny": ["Bash(rm *)"],
    "allow": ["Read", "Grep"],
    "defaultMode": "plan"
  },
  "theme": "dark",
  "model": "gpt-4o"
}`))

	result, err := (statemigration.Importer{}).Import(
		t.Context(),
		roots,
		SettingsMigrationSpec("project"),
	)
	if err != nil || result.Status != statemigration.StatusImported {
		t.Fatalf("Import() = %#v, %v", result, err)
	}

	data, err := os.ReadFile(filepath.Join(roots.Canonical, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"model": "gpt-4o",
		"theme": "dark",
		"permissions": map[string]any{
			"defaultMode": "plan",
			"allow":       []any{"Read", "Grep"},
			"deny":        []any{"Bash(rm *)"},
		},
		"compact": map[string]any{
			"threshold": float64(4096),
			"strategy":  "auto",
		},
		"memory":   map[string]any{"enabled": true},
		"provider": "openai",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("migrated settings = %#v, want %#v", got, want)
	}
	if bytes.Equal(bytes.TrimSpace(data), bytes.TrimSpace(mustReadSettingsSource(t, roots))) {
		t.Fatal("migration copied source bytes instead of rebuilding the allowlisted object")
	}
	assertPrivateMode(t, roots.Canonical, 0o700)
	assertPrivateMode(t, filepath.Join(roots.Canonical, "settings.json"), 0o600)
}

func TestSettingsMigrationRejectsUnknownAndCredentialLikeValues(t *testing.T) {
	tests := map[string]string{
		"unknown key":           `{"model":"gpt-4o","privateExtension":"value"}`,
		"hooks":                 `{"model":"gpt-4o","hooks":{"preToolUse":["echo safe"]}}`,
		"provider object":       `{"provider":{"apiKey":"ordinary-looking-value"}}`,
		"dotted provider key":   `{"provider.apiKey":"ordinary-looking-value"}`,
		"credential-like model": `{"model":"sk-` + strings.Repeat("1", 24) + `"}`,
		"credential in rule":    `{"permissions":{"allow":["Bash(export API_KEY=private-value)"]}}`,
	}
	for name, source := range tests {
		t.Run(name, func(t *testing.T) {
			roots := newSettingsMigrationRoots(t, []byte(source))
			result, err := (statemigration.Importer{}).Import(
				t.Context(),
				roots,
				SettingsMigrationSpec("user"),
			)
			if err == nil || result.Status != statemigration.StatusUnsafe {
				t.Fatalf("Import() = %#v, %v, want unsafe", result, err)
			}
			if _, statErr := os.Lstat(filepath.Join(roots.Canonical, "settings.json")); !os.IsNotExist(statErr) {
				t.Fatalf("unsafe migration created a destination: %v", statErr)
			}
		})
	}
}

func TestSettingsCanonicalCollisionWinsWithoutMerge(t *testing.T) {
	roots := newSettingsMigrationRoots(t, []byte(`{"model":"legacy","theme":"light"}`))
	if err := os.Mkdir(roots.Canonical, 0o700); err != nil {
		t.Fatal(err)
	}
	canonical := []byte(`{"model":"canonical"}`)
	target := filepath.Join(roots.Canonical, "settings.json")
	if err := os.WriteFile(target, canonical, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := (statemigration.Importer{}).Import(
		t.Context(),
		roots,
		SettingsMigrationSpec("project"),
	)
	if err != nil || result.Status != statemigration.StatusDestinationExists {
		t.Fatalf("Import() = %#v, %v", result, err)
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, canonical) {
		t.Fatalf("canonical settings = %q, %v", got, err)
	}
}

func newSettingsMigrationRoots(t *testing.T, source []byte) statepath.Roots {
	t.Helper()
	root := t.TempDir()
	roots := statepath.Roots{
		Canonical: filepath.Join(root, ".yhc"),
		Legacy:    filepath.Join(root, ".eino-agent"),
	}
	if err := os.Mkdir(roots.Legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(roots.Legacy, "settings.json"), source, 0o600); err != nil {
		t.Fatal(err)
	}
	return roots
}

func mustReadSettingsSource(t *testing.T, roots statepath.Roots) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(roots.Legacy, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertPrivateMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", filepath.Base(path), got, want)
	}
}
